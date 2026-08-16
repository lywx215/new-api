package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

const (
	openCodeGoRPMStatusLimit     = 1000
	openCodeGoRPMWarningInterval = 30
)

var openCodeGoTokenBucketScript = `
local cooldown_ttl = redis.call('PTTL', KEYS[2])
if cooldown_ttl > 0 then
  return {0, math.max(1, math.ceil(cooldown_ttl / 1000)), 0}
end
local redis_time = redis.call('TIME')
local now = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local rpm = tonumber(ARGV[1])
local capacity = tonumber(ARGV[2])
local values = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(values[1]) or capacity
local ts = tonumber(values[2]) or now
tokens = math.min(capacity, tokens + math.max(0, now - ts) * rpm / 60000)
if tokens < 1 then
  redis.call('HMSET', KEYS[1], 'tokens', tokens, 'ts', now)
  redis.call('PEXPIRE', KEYS[1], 120000)
  return {0, math.max(1, math.ceil((1 - tokens) * 60 / rpm)), math.floor(tokens)}
end
tokens = tokens - 1
redis.call('HMSET', KEYS[1], 'tokens', tokens, 'ts', now)
redis.call('PEXPIRE', KEYS[1], 120000)
redis.call('INCR', KEYS[3])
redis.call('EXPIRE', KEYS[3], 120)
return {1, 0, math.floor(tokens)}
`

var lastOpenCodeGoRPMWarning atomic.Int64

type OpenCodeGoRPMStatus struct {
	ChannelID             int     `json:"channel_id"`
	ChannelName           string  `json:"channel_name"`
	RPMLimit              int     `json:"rpm_limit"`
	RequestsCurrentMinute int     `json:"requests_current_minute"`
	RemainingTokens       float64 `json:"remaining_tokens"`
	CooldownSeconds       int64   `json:"cooldown_seconds"`
}

type OpenCodeGoRPMStatusResult struct {
	Statuses  []OpenCodeGoRPMStatus
	Total     int
	Truncated bool
}

type OpenCodeGoRPMError struct{ RetryAfter int }

func (e *OpenCodeGoRPMError) Error() string {
	return fmt.Sprintf("all OpenCodeGo accounts are at their RPM soft limit; retry after %d seconds", e.RetryAfter)
}

func AsOpenCodeGoRPMError(err error) (*OpenCodeGoRPMError, bool) {
	var target *OpenCodeGoRPMError
	return target, errors.As(err, &target)
}

func shouldLogOpenCodeGoRPMWarning(now int64) bool {
	for {
		last := lastOpenCodeGoRPMWarning.Load()
		if last != 0 && now-last < openCodeGoRPMWarningInterval {
			return false
		}
		if lastOpenCodeGoRPMWarning.CompareAndSwap(last, now) {
			return true
		}
	}
}

func warnOpenCodeGoRPM(kind string, channelID int, err error) {
	if !shouldLogOpenCodeGoRPMWarning(time.Now().Unix()) {
		return
	}
	if err != nil {
		common.SysError(fmt.Sprintf("OpenCodeGo RPM guard fail-open: kind=%s channel_id=%d err=%v", kind, channelID, err))
		return
	}
	common.SysError(fmt.Sprintf("OpenCodeGo RPM guard fail-open: kind=%s channel_id=%d", kind, channelID))
}

func openCodeGoRPMLimit(channel *model.Channel) int {
	setting := operation_setting.GetChannelAffinitySetting()
	limit := setting.DefaultAccountRPM
	if channel != nil {
		if override := channel.GetOtherSettings().OpenCodeGoRPMLimit; override > 0 {
			limit = override
		}
	}
	return limit
}

func effectiveOpenCodeGoRPMConfig(channel *model.Channel) (int, int, bool) {
	setting := operation_setting.GetChannelAffinitySetting()
	rpm, burst := openCodeGoRPMLimit(channel), setting.AccountBurst
	valid := rpm >= 1 && burst >= 1 && rpm+burst <= dto.OpenCodeGoHardRPMLimit
	if valid {
		return rpm, burst, true
	}
	if burst < 1 || burst >= dto.OpenCodeGoHardRPMLimit {
		burst = 50
	}
	if rpm < 1 || rpm+burst > dto.OpenCodeGoHardRPMLimit {
		rpm = min(1450, dto.OpenCodeGoHardRPMLimit-burst)
	}
	return rpm, burst, false
}

func redisInteger(value interface{}) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		return parsed, err == nil
	case []byte:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func TryReserveOpenCodeGoRPM(c *gin.Context, channel *model.Channel) (bool, int, int) {
	setting := operation_setting.GetChannelAffinitySetting()
	if setting == nil || !setting.RPMGuardEnabled || channel == nil || channel.Type != constant.ChannelTypeOpenCodeGo {
		return true, 0, -1
	}
	rpm, burst, configValid := effectiveOpenCodeGoRPMConfig(channel)
	if !configValid {
		warnOpenCodeGoRPM("invalid_config", channel.Id, nil)
	}
	if !common.RedisEnabled || common.RDB == nil {
		warnOpenCodeGoRPM("redis_unavailable", channel.Id, nil)
		return true, 0, -1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	channelID := strconv.Itoa(channel.Id)
	minuteKey := strconv.FormatInt(time.Now().Unix()/60, 10)
	result, err := common.RDB.Eval(ctx, openCodeGoTokenBucketScript,
		[]string{"opencodego:rpm:" + channelID, "opencodego:cooldown:" + channelID, "opencodego:rpm_count:" + channelID + ":" + minuteKey}, rpm, burst).Result()
	if err != nil {
		warnOpenCodeGoRPM("lua_error", channel.Id, err)
		return true, 0, -1
	}
	values, ok := result.([]interface{})
	if !ok || len(values) < 3 {
		warnOpenCodeGoRPM("invalid_result", channel.Id, nil)
		return true, 0, -1
	}
	allowedValue, allowedOK := redisInteger(values[0])
	waitValue, waitOK := redisInteger(values[1])
	remainingValue, remainingOK := redisInteger(values[2])
	if !allowedOK || !waitOK || !remainingOK {
		warnOpenCodeGoRPM("invalid_result", channel.Id, nil)
		return true, 0, -1
	}
	return allowedValue == 1, int(waitValue), int(remainingValue)
}

func GetOpenCodeGoRPMStatuses() OpenCodeGoRPMStatusResult {
	setting := operation_setting.GetChannelAffinitySetting()
	if setting == nil || !setting.RPMGuardEnabled || model.DB == nil {
		return OpenCodeGoRPMStatusResult{}
	}
	totalCount, err := model.CountChannelsByType(constant.ChannelTypeOpenCodeGo)
	if err != nil {
		common.SysError(fmt.Sprintf("failed to count OpenCodeGo RPM status channels: %v", err))
		return OpenCodeGoRPMStatusResult{}
	}
	total := int(totalCount)
	channels, err := model.GetChannelsByType(0, openCodeGoRPMStatusLimit, true, constant.ChannelTypeOpenCodeGo)
	if err != nil {
		common.SysError(fmt.Sprintf("failed to list OpenCodeGo RPM status channels: %v", err))
		return OpenCodeGoRPMStatusResult{Total: total, Truncated: total > openCodeGoRPMStatusLimit}
	}
	result := OpenCodeGoRPMStatusResult{Statuses: make([]OpenCodeGoRPMStatus, len(channels)), Total: total, Truncated: total > len(channels)}
	for i, channel := range channels {
		rpm, _, _ := effectiveOpenCodeGoRPMConfig(channel)
		result.Statuses[i] = OpenCodeGoRPMStatus{ChannelID: channel.Id, ChannelName: channel.Name, RPMLimit: rpm, RemainingTokens: -1}
	}
	if len(channels) == 0 || !common.RedisEnabled || common.RDB == nil {
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	minuteKey := strconv.FormatInt(time.Now().Unix()/60, 10)
	tokenCommands := make([]*redis.StringStringMapCmd, len(channels))
	countCommands := make([]*redis.StringCmd, len(channels))
	cooldownCommands := make([]*redis.DurationCmd, len(channels))
	_, err = common.RDB.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for i, channel := range channels {
			channelID := strconv.Itoa(channel.Id)
			tokenCommands[i] = pipe.HGetAll(ctx, "opencodego:rpm:"+channelID)
			countCommands[i] = pipe.Get(ctx, "opencodego:rpm_count:"+channelID+":"+minuteKey)
			cooldownCommands[i] = pipe.PTTL(ctx, "opencodego:cooldown:"+channelID)
		}
		return nil
	})
	if err != nil && !errors.Is(err, redis.Nil) {
		warnOpenCodeGoRPM("status_pipeline", 0, err)
		return result
	}
	for i := range result.Statuses {
		if values, cmdErr := tokenCommands[i].Result(); cmdErr == nil {
			if tokens, parseErr := strconv.ParseFloat(values["tokens"], 64); parseErr == nil {
				result.Statuses[i].RemainingTokens = tokens
			}
		}
		if count, cmdErr := countCommands[i].Int(); cmdErr == nil {
			result.Statuses[i].RequestsCurrentMinute = count
		}
		if ttl, cmdErr := cooldownCommands[i].Result(); cmdErr == nil && ttl > 0 {
			result.Statuses[i].CooldownSeconds = int64(math.Ceil(ttl.Seconds()))
		}
	}
	return result
}

func MarkOpenCodeGoRateLimited(channelID int, retryAfter int) {
	setting := operation_setting.GetChannelAffinitySetting()
	if setting == nil || !setting.RPMGuardEnabled || channelID <= 0 || !common.RedisEnabled || common.RDB == nil {
		return
	}
	if retryAfter <= 0 {
		retryAfter = setting.RateLimitCooldownSecs
	}
	retryAfter = int(math.Min(60, math.Max(1, float64(retryAfter))))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := common.RDB.Set(ctx, "opencodego:cooldown:"+strconv.Itoa(channelID), "1", time.Duration(retryAfter)*time.Second).Err(); err != nil {
		warnOpenCodeGoRPM("cooldown_set", channelID, err)
	}
}

func ObserveOpenCodeGoUpstreamResponse(channelType, channelID int, resp *http.Response, now time.Time) {
	if channelType != constant.ChannelTypeOpenCodeGo || channelID <= 0 || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		return
	}
	common.ObserveInternalAffinityUpstream429()
	originalRetryAfter := strings.TrimSpace(resp.Header.Get("Retry-After"))
	retryAfter := parseOpenCodeGoRetryAfter(originalRetryAfter, now)
	if retryAfter <= 0 {
		setting := operation_setting.GetChannelAffinitySetting()
		if setting != nil {
			retryAfter = setting.RateLimitCooldownSecs
		}
		originalRetryAfter = ""
	}
	retryAfter = int(math.Min(60, math.Max(1, float64(retryAfter))))
	if originalRetryAfter == "" {
		resp.Header.Set("Retry-After", strconv.Itoa(retryAfter))
	} else {
		resp.Header.Set("Retry-After", originalRetryAfter)
	}
	MarkOpenCodeGoRateLimited(channelID, retryAfter)
}

func parseOpenCodeGoRetryAfter(value string, now time.Time) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	seconds, err := strconv.Atoi(value)
	if err != nil {
		retryAt, parseErr := http.ParseTime(value)
		if parseErr != nil {
			return 0
		}
		seconds = int(math.Ceil(retryAt.Sub(now).Seconds()))
	}
	if seconds <= 0 {
		return 0
	}
	return min(seconds, 60)
}
