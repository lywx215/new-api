package service

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOpenCodeGoRPMGuardDeniesAfterBurst(t *testing.T) {
	server := miniredis.RunT(t)
	originalRDB, originalEnabled := common.RDB, common.RedisEnabled
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	common.RedisEnabled = true
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RDB, common.RedisEnabled = originalRDB, originalEnabled
	})
	setting := operation_setting.GetChannelAffinitySetting()
	originalSetting := *setting
	t.Cleanup(func() { *setting = originalSetting })
	setting.RPMGuardEnabled = true
	setting.DefaultAccountRPM = 60
	setting.AccountBurst = 1

	channel := &model.Channel{Id: 9001, Type: constant.ChannelTypeOpenCodeGo}
	firstContext, _ := gin.CreateTestContext(nil)
	allowed, _, remaining := TryReserveOpenCodeGoRPM(firstContext, channel)
	require.True(t, allowed)
	assert.Equal(t, 0, remaining)
	secondContext, _ := gin.CreateTestContext(nil)
	allowed, wait, _ := TryReserveOpenCodeGoRPM(secondContext, channel)
	assert.False(t, allowed)
	assert.GreaterOrEqual(t, wait, 1)
}

func TestOpenCodeGoRPMGuardFailsOpenWithoutRedis(t *testing.T) {
	originalRDB, originalEnabled := common.RDB, common.RedisEnabled
	common.RDB = nil
	common.RedisEnabled = false
	t.Cleanup(func() { common.RDB, common.RedisEnabled = originalRDB, originalEnabled })
	setting := operation_setting.GetChannelAffinitySetting()
	originalSetting := *setting
	t.Cleanup(func() { *setting = originalSetting })
	setting.RPMGuardEnabled = true

	allowed, _, _ := TryReserveOpenCodeGoRPM(nil, &model.Channel{Id: 9002, Type: constant.ChannelTypeOpenCodeGo})
	assert.True(t, allowed)
}

func TestOpenCodeGoRPMGuardHonorsUpstreamRateLimitCooldown(t *testing.T) {
	server := miniredis.RunT(t)
	originalRDB, originalEnabled := common.RDB, common.RedisEnabled
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	common.RedisEnabled = true
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RDB, common.RedisEnabled = originalRDB, originalEnabled
	})
	setting := operation_setting.GetChannelAffinitySetting()
	originalSetting := *setting
	t.Cleanup(func() { *setting = originalSetting })
	setting.RPMGuardEnabled = true
	setting.DefaultAccountRPM = 60
	setting.AccountBurst = 1

	channel := &model.Channel{Id: 9003, Type: constant.ChannelTypeOpenCodeGo}
	MarkOpenCodeGoRateLimited(channel.Id, 4)

	allowed, wait, _ := TryReserveOpenCodeGoRPM(nil, channel)
	assert.False(t, allowed)
	assert.GreaterOrEqual(t, wait, 3)
	assert.LessOrEqual(t, wait, 4)

	server.FastForward(4 * time.Second)
	allowed, _, _ = TryReserveOpenCodeGoRPM(nil, channel)
	assert.True(t, allowed)
}

func TestOpenCodeGoRPMGuardUsesConfiguredCooldownWhenRetryAfterIsMissing(t *testing.T) {
	server := miniredis.RunT(t)
	originalRDB, originalEnabled := common.RDB, common.RedisEnabled
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	common.RedisEnabled = true
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RDB, common.RedisEnabled = originalRDB, originalEnabled
	})
	setting := operation_setting.GetChannelAffinitySetting()
	originalSetting := *setting
	t.Cleanup(func() { *setting = originalSetting })
	setting.RPMGuardEnabled = true
	setting.DefaultAccountRPM = 60
	setting.AccountBurst = 1
	setting.RateLimitCooldownSecs = 7

	channel := &model.Channel{Id: 9004, Type: constant.ChannelTypeOpenCodeGo}
	MarkOpenCodeGoRateLimited(channel.Id, 0)

	allowed, wait, _ := TryReserveOpenCodeGoRPM(nil, channel)
	assert.False(t, allowed)
	assert.GreaterOrEqual(t, wait, 6)
	assert.LessOrEqual(t, wait, 7)
}

func TestParseOpenCodeGoRetryAfter(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name, value string
		want        int
	}{
		{name: "integer", value: "12", want: 12},
		{name: "http date", value: now.Add(5 * time.Second).Format(http.TimeFormat), want: 5},
		{name: "past date", value: now.Add(-time.Second).Format(http.TimeFormat)},
		{name: "invalid", value: "later"},
		{name: "missing"},
		{name: "capped", value: "120", want: 60},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, parseOpenCodeGoRetryAfter(test.value, now))
		})
	}
}

func TestObserveOpenCodeGoUpstreamResponseNormalizesCooldown(t *testing.T) {
	server := miniredis.RunT(t)
	originalRDB, originalEnabled := common.RDB, common.RedisEnabled
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	common.RedisEnabled = true
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RDB, common.RedisEnabled = originalRDB, originalEnabled
	})
	setting := operation_setting.GetChannelAffinitySetting()
	originalSetting := *setting
	setting.RPMGuardEnabled = true
	setting.RateLimitCooldownSecs = 7
	t.Cleanup(func() { *setting = originalSetting })

	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header)}
	ObserveOpenCodeGoUpstreamResponse(constant.ChannelTypeOpenCodeGo, 9005, resp, time.Now())
	assert.Equal(t, "7", resp.Header.Get("Retry-After"))
	ttl := server.TTL("opencodego:cooldown:9005")
	assert.GreaterOrEqual(t, ttl, 6*time.Second)
	assert.LessOrEqual(t, ttl, 7*time.Second)

	nonOpenCodeGo := &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header)}
	ObserveOpenCodeGoUpstreamResponse(constant.ChannelTypeOpenAI, 9006, nonOpenCodeGo, time.Now())
	assert.Empty(t, nonOpenCodeGo.Header.Get("Retry-After"))
	assert.False(t, server.Exists("opencodego:cooldown:9006"))
}

func TestObserveOpenCodeGoUpstreamResponseRetryAfterFormats(t *testing.T) {
	server := miniredis.RunT(t)
	originalRDB, originalEnabled := common.RDB, common.RedisEnabled
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	common.RedisEnabled = true
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RDB, common.RedisEnabled = originalRDB, originalEnabled
	})
	setting := operation_setting.GetChannelAffinitySetting()
	originalSetting := *setting
	setting.RPMGuardEnabled = true
	setting.RateLimitCooldownSecs = 7
	t.Cleanup(func() { *setting = originalSetting })

	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	httpDate := now.Add(9 * time.Second).Format(http.TimeFormat)
	tests := []struct {
		name       string
		channelID  int
		header     string
		wantHeader string
		wantTTL    time.Duration
	}{
		{name: "integer", channelID: 9007, header: "5", wantHeader: "5", wantTTL: 5 * time.Second},
		{name: "http date", channelID: 9008, header: httpDate, wantHeader: httpDate, wantTTL: 9 * time.Second},
		{name: "missing", channelID: 9009, wantHeader: "7", wantTTL: 7 * time.Second},
		{name: "invalid", channelID: 9011, header: "later", wantHeader: "7", wantTTL: 7 * time.Second},
		{name: "past", channelID: 9012, header: now.Add(-time.Second).Format(http.TimeFormat), wantHeader: "7", wantTTL: 7 * time.Second},
		{name: "cooldown capped", channelID: 9013, header: "120", wantHeader: "120", wantTTL: 60 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header)}
			if test.header != "" {
				response.Header.Set("Retry-After", test.header)
			}
			ObserveOpenCodeGoUpstreamResponse(constant.ChannelTypeOpenCodeGo, test.channelID, response, now)
			assert.Equal(t, test.wantHeader, response.Header.Get("Retry-After"))
			assert.Equal(t, test.wantTTL, server.TTL(fmt.Sprintf("opencodego:cooldown:%d", test.channelID)))
		})
	}
}

func TestOpenCodeGoRPMWarningLimiterUsesDeterministicWindow(t *testing.T) {
	original := lastOpenCodeGoRPMWarning.Load()
	lastOpenCodeGoRPMWarning.Store(0)
	t.Cleanup(func() { lastOpenCodeGoRPMWarning.Store(original) })
	assert.True(t, shouldLogOpenCodeGoRPMWarning(100))
	assert.False(t, shouldLogOpenCodeGoRPMWarning(129))
	assert.True(t, shouldLogOpenCodeGoRPMWarning(130))
}

func TestOpenCodeGoCooldownValueIsOnlyASentinel(t *testing.T) {
	server := miniredis.RunT(t)
	originalRDB, originalEnabled := common.RDB, common.RedisEnabled
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	common.RedisEnabled = true
	t.Cleanup(func() { _ = common.RDB.Close(); common.RDB, common.RedisEnabled = originalRDB, originalEnabled })
	setting := operation_setting.GetChannelAffinitySetting()
	originalSetting := *setting
	setting.RPMGuardEnabled = true
	setting.DefaultAccountRPM = 60
	setting.AccountBurst = 1
	t.Cleanup(func() { *setting = originalSetting })

	MarkOpenCodeGoRateLimited(9010, 5)
	value, err := server.Get("opencodego:cooldown:9010")
	require.NoError(t, err)
	assert.Equal(t, "1", value)
	allowed, wait, _ := TryReserveOpenCodeGoRPM(nil, &model.Channel{Id: 9010, Type: constant.ChannelTypeOpenCodeGo})
	assert.False(t, allowed)
	assert.GreaterOrEqual(t, wait, 4)
}

func TestOpenCodeGoRPMStatusesExposeGlobalTotalAndSkipWhenDisabled(t *testing.T) {
	originalDB := model.DB
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	model.DB = db
	t.Cleanup(func() { model.DB = originalDB })
	for id := 9020; id < 9022; id++ {
		require.NoError(t, db.Create(&model.Channel{Id: id, Name: fmt.Sprintf("account-%d", id), Type: constant.ChannelTypeOpenCodeGo}).Error)
	}
	server := miniredis.RunT(t)
	originalRDB, originalEnabled := common.RDB, common.RedisEnabled
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	common.RedisEnabled = true
	t.Cleanup(func() { _ = common.RDB.Close(); common.RDB, common.RedisEnabled = originalRDB, originalEnabled })
	setting := operation_setting.GetChannelAffinitySetting()
	originalSetting := *setting
	setting.RPMGuardEnabled = true
	setting.DefaultAccountRPM = 60
	setting.AccountBurst = 1
	t.Cleanup(func() { *setting = originalSetting })

	allowed, _, _ := TryReserveOpenCodeGoRPM(nil, &model.Channel{Id: 9020, Type: constant.ChannelTypeOpenCodeGo})
	require.True(t, allowed)
	result := GetOpenCodeGoRPMStatuses()
	assert.Equal(t, 2, result.Total)
	assert.False(t, result.Truncated)
	assert.Len(t, result.Statuses, 2)

	setting.RPMGuardEnabled = false
	disabled := GetOpenCodeGoRPMStatuses()
	assert.Empty(t, disabled.Statuses)
	assert.Zero(t, disabled.Total)
}
