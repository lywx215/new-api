package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
)

type redisProbeResult struct {
	CapturedAt      string `json:"captured_at"`
	Address         string `json:"address"`
	Ping            string `json:"ping"`
	ServerTime      int64  `json:"server_time_unix_ms"`
	LuaTime         bool   `json:"lua_time"`
	PTTL            int64  `json:"pttl_ms"`
	Pipeline        bool   `json:"pipeline"`
	TokenBucket     bool   `json:"token_bucket"`
	LoopbackAddress bool   `json:"loopback_address"`
}

func redisClientFromURL(rawURL string) (*redis.Client, *redis.Options, error) {
	if strings.TrimSpace(rawURL) == "" {
		return nil, nil, errors.New("Redis URL is required")
	}
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, nil, fmt.Errorf("parse Redis URL: %w", err)
	}
	return redis.NewClient(options), options, nil
}

func isLoopbackRedisAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func probeRedis(runDir, rawURL string, allowRemote bool) (*redisProbeResult, error) {
	client, options, err := redisClientFromURL(rawURL)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	loopback := isLoopbackRedisAddress(options.Addr)
	if !loopback && !allowRemote {
		return nil, fmt.Errorf("refusing to run destructive probe against non-loopback Redis %s without --allow-remote", options.Addr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := &redisProbeResult{CapturedAt: utcNow(), Address: options.Addr, LoopbackAddress: loopback}
	result.Ping, err = client.Ping(ctx).Result()
	if err != nil {
		return nil, err
	}
	probePrefix := "ocg-e2e:probe:" + hash8(runDir+utcNow())
	ttlKey := probePrefix + ":ttl"
	bucketKey := probePrefix + ":bucket"
	countKey := probePrefix + ":count"
	defer client.Del(context.Background(), ttlKey, bucketKey, countKey)
	timeResult, err := client.Eval(ctx, `local t=redis.call('TIME'); return {t[1],t[2]}`, nil).Result()
	if err != nil {
		return nil, fmt.Errorf("Redis Lua TIME probe: %w", err)
	}
	timeValues, ok := timeResult.([]interface{})
	if !ok || len(timeValues) != 2 {
		return nil, fmt.Errorf("Redis Lua TIME returned unexpected result %T", timeResult)
	}
	seconds, _ := strconv.ParseInt(fmt.Sprint(timeValues[0]), 10, 64)
	microseconds, _ := strconv.ParseInt(fmt.Sprint(timeValues[1]), 10, 64)
	result.ServerTime = seconds*1000 + microseconds/1000
	result.LuaTime = result.ServerTime > 0
	if err := client.Set(ctx, ttlKey, "1", 3*time.Second).Err(); err != nil {
		return nil, err
	}
	pttl, err := client.PTTL(ctx, ttlKey).Result()
	if err != nil {
		return nil, err
	}
	result.PTTL = pttl.Milliseconds()
	_, err = client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Get(ctx, ttlKey)
		pipe.PTTL(ctx, ttlKey)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("Redis Pipeline probe: %w", err)
	}
	result.Pipeline = true
	bucketScript := `
local redis_time = redis.call('TIME')
local now = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local rpm = tonumber(ARGV[1])
local capacity = tonumber(ARGV[2])
local values = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(values[1]) or capacity
local ts = tonumber(values[2]) or now
tokens = math.min(capacity, tokens + math.max(0, now-ts) * rpm / 60000)
if tokens < 1 then return {0,1,math.floor(tokens)} end
tokens = tokens - 1
redis.call('HMSET',KEYS[1],'tokens',tokens,'ts',now)
redis.call('PEXPIRE',KEYS[1],120000)
redis.call('INCR',KEYS[2])
return {1,0,math.floor(tokens)}
`
	bucketResult, err := client.Eval(ctx, bucketScript, []string{bucketKey, countKey}, 60, 2).Result()
	if err != nil {
		return nil, fmt.Errorf("Redis Token Bucket probe: %w", err)
	}
	values, ok := bucketResult.([]interface{})
	result.TokenBucket = ok && len(values) == 3 && fmt.Sprint(values[0]) == "1"
	if !result.TokenBucket {
		return nil, fmt.Errorf("Redis Token Bucket returned unexpected result %v", bucketResult)
	}
	if result.PTTL < 1 || result.PTTL > 3000 {
		return nil, fmt.Errorf("Redis PTTL probe returned %dms", result.PTTL)
	}
	if err := writeJSONAtomic(filepath.Join(runDir, "redis-probe.json"), result); err != nil {
		return nil, err
	}
	return result, nil
}

type channelRedisSnapshot struct {
	ChannelID            int               `json:"channel_id"`
	CapturedAt           string            `json:"captured_at"`
	Tokens               map[string]string `json:"tokens,omitempty"`
	CooldownMilliseconds int64             `json:"cooldown_ms"`
	Minute               int64             `json:"minute"`
	MinuteCount          int64             `json:"minute_count"`
}

func snapshotChannelRedis(rawURL string, channelID int) (channelRedisSnapshot, error) {
	client, _, err := redisClientFromURL(rawURL)
	if err != nil {
		return channelRedisSnapshot{}, err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	id := strconv.Itoa(channelID)
	redisTime, err := client.Time(ctx).Result()
	if err != nil {
		return channelRedisSnapshot{}, err
	}
	minute := redisTime.Unix() / 60
	snapshot := channelRedisSnapshot{ChannelID: channelID, CapturedAt: utcNow(), Minute: minute}
	commands, err := client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HGetAll(ctx, "opencodego:rpm:"+id)
		pipe.PTTL(ctx, "opencodego:cooldown:"+id)
		pipe.Get(ctx, fmt.Sprintf("opencodego:rpm_count:%s:%d", id, minute))
		return nil
	})
	if err != nil && !errors.Is(err, redis.Nil) {
		return snapshot, err
	}
	if len(commands) >= 1 {
		if command, ok := commands[0].(*redis.StringStringMapCmd); ok {
			snapshot.Tokens, _ = command.Result()
		}
	}
	if len(commands) >= 2 {
		if command, ok := commands[1].(*redis.DurationCmd); ok {
			if ttl, commandErr := command.Result(); commandErr == nil && ttl > 0 {
				snapshot.CooldownMilliseconds = ttl.Milliseconds()
			}
		}
	}
	if len(commands) >= 3 {
		if command, ok := commands[2].(*redis.StringCmd); ok {
			snapshot.MinuteCount, _ = command.Int64()
		}
	}
	return snapshot, nil
}

func appendRedisSnapshot(runDir string, snapshot channelRedisSnapshot) error {
	path := filepath.Join(runDir, "redis-snapshots.json")
	snapshots := make([]channelRedisSnapshot, 0)
	if err := readJSONFile(path, &snapshots); err != nil && !os.IsNotExist(err) && !errors.Is(err, io.EOF) {
		return err
	}
	snapshots = append(snapshots, snapshot)
	return writeJSONAtomic(path, snapshots)
}

func poisonRPMKey(rawURL string, channelID int, apply bool) error {
	if !apply {
		return errors.New("refusing to poison RPM key without --apply")
	}
	client, options, err := redisClientFromURL(rawURL)
	if err != nil {
		return err
	}
	defer client.Close()
	if !isLoopbackRedisAddress(options.Addr) {
		return fmt.Errorf("RPM poison is restricted to loopback test Redis, got %s", options.Addr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	key := "opencodego:rpm:" + strconv.Itoa(channelID)
	if err := client.Del(ctx, key).Err(); err != nil {
		return err
	}
	return client.Set(ctx, key, "ocg-e2e-wrong-type", 15*time.Minute).Err()
}

func cleanupChannelRedis(rawURL string, channelIDs []int, includeCooldown bool) error {
	client, options, err := redisClientFromURL(rawURL)
	if err != nil {
		return err
	}
	defer client.Close()
	if !isLoopbackRedisAddress(options.Addr) {
		return fmt.Errorf("test Redis cleanup is restricted to loopback, got %s", options.Addr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	redisTime, err := client.Time(ctx).Result()
	if err != nil {
		return err
	}
	minute := redisTime.Unix() / 60
	keys := make([]string, 0, len(channelIDs)*3)
	for _, channelID := range channelIDs {
		id := strconv.Itoa(channelID)
		keys = append(keys, "opencodego:rpm:"+id, fmt.Sprintf("opencodego:rpm_count:%s:%d", id, minute))
		if includeCooldown {
			keys = append(keys, "opencodego:cooldown:"+id)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return client.Del(ctx, keys...).Err()
}

func parseRedisURLRedacted(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "invalid"
	}
	if parsed.User != nil {
		parsed.User = url.UserPassword("redacted", "redacted")
	}
	return parsed.String()
}

func cacheRatio(cached, prompt int) float64 {
	if prompt <= 0 {
		return 0
	}
	return math.Min(1, math.Max(0, float64(cached)/float64(prompt)))
}
