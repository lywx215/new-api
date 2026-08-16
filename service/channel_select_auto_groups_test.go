package service

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelSelectAutoGroupsTest(t *testing.T) *gorm.DB {
	t.Helper()

	originalDB := model.DB
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalRetryTimes := common.RetryTimes
	originalAutoGroups := setting.AutoGroups2JsonString()
	originalUsableGroups := setting.UserUsableGroups2JSONString()
	originalGroupRatios := ratio_setting.GroupRatio2JSONString()
	originalMaxTokenAutoGroups := setting.GetMaxTokenAutoGroups()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	common.MemoryCacheEnabled = true
	common.RetryTimes = 0

	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`[]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":2}`))
	require.NoError(t, setting.UpdateMaxTokenAutoGroups("2"))

	t.Cleanup(func() {
		model.DB = originalDB
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		common.RetryTimes = originalRetryTimes
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalGroupRatios))
		require.NoError(t, setting.UpdateMaxTokenAutoGroups(fmt.Sprintf("%d", originalMaxTokenAutoGroups)))

		if originalMemoryCacheEnabled && originalDB != nil &&
			originalDB.Migrator().HasTable(&model.Channel{}) && originalDB.Migrator().HasTable(&model.Ability{}) {
			model.InitChannelCache()
		}
		sqlDB, err := db.DB()
		if err == nil {
			require.NoError(t, sqlDB.Close())
		}
	})

	return db
}

func createChannelSelectAutoGroupsChannel(t *testing.T, db *gorm.DB, id int, group, modelName string) {
	createChannelSelectAutoGroupsChannelOfType(t, db, id, group, modelName, constant.ChannelTypeOpenAI)
}

func createChannelSelectAutoGroupsChannelOfType(t *testing.T, db *gorm.DB, id int, group, modelName string, channelType int) {
	t.Helper()
	priority := int64(0)
	weight := uint(100)
	require.NoError(t, db.Create(&model.Channel{
		Id:       id,
		Type:     channelType,
		Key:      fmt.Sprintf("key-%d", id),
		Status:   common.ChannelStatusEnabled,
		Name:     fmt.Sprintf("channel-%d", id),
		Weight:   &weight,
		Models:   modelName,
		Group:    group,
		Priority: &priority,
	}).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group:     group,
		Model:     modelName,
		ChannelId: id,
		Enabled:   true,
		Priority:  &priority,
		Weight:    weight,
	}).Error)
}

func setupChannelSelectRPMGuardTest(t *testing.T) {
	t.Helper()
	server := miniredis.RunT(t)
	originalRDB, originalRedisEnabled := common.RDB, common.RedisEnabled
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	common.RedisEnabled = true

	affinitySetting := operation_setting.GetChannelAffinitySetting()
	originalAffinitySetting := *affinitySetting
	affinitySetting.RPMGuardEnabled = true
	affinitySetting.DefaultAccountRPM = 60
	affinitySetting.AccountBurst = 1

	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RDB, common.RedisEnabled = originalRDB, originalRedisEnabled
		*affinitySetting = originalAffinitySetting
	})
}

func TestCacheGetRandomSatisfiedChannelUsesTokenAutoGroupsWhenGlobalAutoIsEmpty(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	const modelName = "auto-groups-runtime-model"
	createChannelSelectAutoGroupsChannel(t, db, 2101, "vip", modelName)
	createChannelSelectAutoGroupsChannel(t, db, 2102, "default", modelName)
	model.InitChannelCache()

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"vip", "default"})
	common.SetContextKey(ctx, constant.ContextKeyTokenCrossGroupRetry, true)

	retry := 0
	param := &RetryParam{
		Ctx:         ctx,
		TokenGroup:  "auto",
		ModelName:   modelName,
		RequestPath: "/v1/chat/completions",
		Retry:       &retry,
	}

	first, selectedGroup, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, 2101, first.Id)
	assert.Equal(t, "vip", selectedGroup)
	assert.Equal(t, "vip", common.GetContextKeyString(ctx, constant.ContextKeyAutoGroup))
	assert.Empty(t, setting.GetAutoGroups(), "the selection must not depend on the global Auto list")

	param.IncreaseRetry()
	second, selectedGroup, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.Equal(t, 2102, second.Id)
	assert.Equal(t, "default", selectedGroup)
	assert.Equal(t, "default", common.GetContextKeyString(ctx, constant.ContextKeyAutoGroup))
}

func TestCacheGetRandomSatisfiedChannelTriesNextAutoGroupWhenRPMCapacityIsExhausted(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	setupChannelSelectRPMGuardTest(t)
	const modelName = "auto-groups-rpm-fallback-model"
	createChannelSelectAutoGroupsChannelOfType(t, db, 2201, "vip", modelName, constant.ChannelTypeOpenCodeGo)
	createChannelSelectAutoGroupsChannelOfType(t, db, 2202, "default", modelName, constant.ChannelTypeOpenCodeGo)
	model.InitChannelCache()

	firstGroupChannel, err := model.CacheGetChannel(2201)
	require.NoError(t, err)
	allowed, _, _ := TryReserveOpenCodeGoRPM(nil, firstGroupChannel)
	require.True(t, allowed, "the setup request must consume the first group's only burst token")

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"vip", "default"})
	common.SetContextKey(ctx, constant.ContextKeyTokenCrossGroupRetry, true)

	channel, selectedGroup, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx:         ctx,
		TokenGroup:  "auto",
		ModelName:   modelName,
		RequestPath: "/v1/chat/completions",
		Retry:       common.GetPointer(0),
	})

	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 2202, channel.Id)
	assert.Equal(t, "default", selectedGroup)
	assert.Equal(t, "default", common.GetContextKeyString(ctx, constant.ContextKeyAutoGroup))
}

func TestChannelSelectionFallsBackToLowerPriorityAfterHighPriorityExclusionWithAndWithoutCache(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	const modelName = "priority-exclusion-parity-model"
	createChannelSelectAutoGroupsChannel(t, db, 2251, "default", modelName)
	createChannelSelectAutoGroupsChannel(t, db, 2252, "default", modelName)
	highPriority := int64(100)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 2251).Update("priority", highPriority).Error)
	require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", 2251).Update("priority", highPriority).Error)
	model.InitChannelCache()

	for _, memoryCache := range []bool{false, true} {
		common.MemoryCacheEnabled = memoryCache
		channel, err := model.GetRandomSatisfiedChannelExcluding("default", modelName, 0, "/v1/chat/completions", map[int]struct{}{2251: {}})
		require.NoError(t, err)
		require.NotNil(t, channel)
		assert.Equal(t, 2252, channel.Id)
	}
}

func TestCacheGetRandomSatisfiedChannelReturnsMinimumWaitAfterAllAutoGroupsAreSaturated(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	setupChannelSelectRPMGuardTest(t)
	const modelName = "auto-groups-rpm-exhausted-model"
	createChannelSelectAutoGroupsChannelOfType(t, db, 2301, "vip", modelName, constant.ChannelTypeOpenCodeGo)
	createChannelSelectAutoGroupsChannelOfType(t, db, 2302, "default", modelName, constant.ChannelTypeOpenCodeGo)
	model.InitChannelCache()

	for _, channelID := range []int{2301, 2302} {
		channel, err := model.CacheGetChannel(channelID)
		require.NoError(t, err)
		allowed, _, _ := TryReserveOpenCodeGoRPM(nil, channel)
		require.True(t, allowed)
	}

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"vip", "default"})
	common.SetContextKey(ctx, constant.ContextKeyTokenCrossGroupRetry, true)

	channel, selectedGroup, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx:         ctx,
		TokenGroup:  "auto",
		ModelName:   modelName,
		RequestPath: "/v1/chat/completions",
		Retry:       common.GetPointer(0),
	})

	assert.Nil(t, channel)
	assert.Equal(t, "default", selectedGroup)
	rpmErr, ok := AsOpenCodeGoRPMError(err)
	require.True(t, ok)
	assert.GreaterOrEqual(t, rpmErr.RetryAfter, 1)
}
