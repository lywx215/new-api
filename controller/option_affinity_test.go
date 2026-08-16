package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func updateAffinityOptionForTest(t *testing.T, key, value string) (bool, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(fmt.Sprintf(`{"key":%q,"value":%q}`, key, value)))
	UpdateOption(ctx)
	var payload struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	return payload.Success, payload.Message
}

func TestUpdateOptionRejectsInvalidOpenCodeGoRPMCombinations(t *testing.T) {
	setting := operation_setting.GetChannelAffinitySetting()
	original := *setting
	setting.DefaultAccountRPM = 1450
	setting.AccountBurst = 50
	t.Cleanup(func() { *setting = original })

	success, message := updateAffinityOptionForTest(t, "channel_affinity_setting.default_account_rpm", "1551")
	assert.False(t, success)
	assert.Contains(t, message, "1600")
	success, message = updateAffinityOptionForTest(t, "channel_affinity_setting.account_burst", "151")
	assert.False(t, success)
	assert.Contains(t, message, "1600")
	success, message = updateAffinityOptionForTest(t, "channel_affinity_setting.rate_limit_cooldown_seconds", "61")
	assert.False(t, success)
	assert.Contains(t, message, "60")
}

func TestUpdateOptionRejectsRetiredOverloadPolicy(t *testing.T) {
	success, message := updateAffinityOptionForTest(t, "channel_affinity_setting.overload_policy", "availability_first")
	assert.False(t, success)
	assert.Contains(t, message, "停用")
}

func TestValidateChannelRejectsOpenCodeGoRPMBurstOverflow(t *testing.T) {
	setting := operation_setting.GetChannelAffinitySetting()
	original := *setting
	setting.AccountBurst = 50
	t.Cleanup(func() { *setting = original })
	channel := &model.Channel{Type: constant.ChannelTypeOpenCodeGo, Key: "test", OtherSettings: `{"opencodego_rpm_limit":1551}`}
	err := validateChannel(channel, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1600")
}

func TestUpdateBurstRejectsExistingOpenCodeGoChannelOverrideConflict(t *testing.T) {
	originalDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:option-affinity-conflict?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	model.DB = db
	t.Cleanup(func() { model.DB = originalDB })
	require.NoError(t, db.Create(&model.Channel{Id: 9981, Name: "conflict-account", Type: constant.ChannelTypeOpenCodeGo, OtherSettings: `{"opencodego_rpm_limit":1500}`}).Error)

	setting := operation_setting.GetChannelAffinitySetting()
	original := *setting
	setting.DefaultAccountRPM = 1400
	setting.AccountBurst = 50
	t.Cleanup(func() { *setting = original })
	success, message := updateAffinityOptionForTest(t, "channel_affinity_setting.account_burst", "101")
	assert.False(t, success)
	assert.Contains(t, message, "conflict-account")
	assert.Contains(t, message, "9981")
}
