package service

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

type AutoDisableReason string

const (
	AutoDisableReasonGlobalDisabled         AutoDisableReason = "global_disabled"
	AutoDisableReasonNoError                AutoDisableReason = "no_error"
	AutoDisableReasonChannelAutoBanDisabled AutoDisableReason = "channel_auto_ban_disabled"
	AutoDisableReasonOpenCodeGoSoftLimit    AutoDisableReason = "opencodego_rpm_soft_limit"
	AutoDisableReasonNewAPI429              AutoDisableReason = "new_api_429"
	AutoDisableReasonOpenCodeGoTransient429 AutoDisableReason = "opencodego_transient_429"
	AutoDisableReasonKeyword                AutoDisableReason = "keyword"
	AutoDisableReasonChannelError           AutoDisableReason = "channel_error"
	AutoDisableReasonSkipRetry              AutoDisableReason = "skip_retry"
	AutoDisableReasonStatusCode             AutoDisableReason = "status_code"
	AutoDisableReasonNoMatchingRule         AutoDisableReason = "no_matching_rule"
)

func formatNotifyType(channelId int, status int) string {
	return fmt.Sprintf("%s_%d_%d", dto.NotifyTypeChannelUpdate, channelId, status)
}

func sanitizeChannelDisableReason(reason string) string {
	return kitutil.MaskSensitiveInfo(reason)
}

// disable & notify
func DisableChannel(channelError types.ChannelError, reason string) {
	reason = sanitizeChannelDisableReason(reason)
	common.SysLog(fmt.Sprintf("通道「%s」（#%d）发生错误，准备禁用，原因：%s", channelError.ChannelName, channelError.ChannelId, common.LocalLogPreview(reason)))

	// 检查是否启用自动禁用功能
	if !channelError.AutoBan {
		common.SysLog(fmt.Sprintf("通道「%s」（#%d）未启用自动禁用功能，跳过禁用操作", channelError.ChannelName, channelError.ChannelId))
		return
	}

	success := model.UpdateChannelStatus(channelError.ChannelId, channelError.UsingKey, common.ChannelStatusAutoDisabled, reason)
	if success {
		subject := fmt.Sprintf("通道「%s」（#%d）已被禁用", channelError.ChannelName, channelError.ChannelId)
		content := fmt.Sprintf("通道「%s」（#%d）已被禁用，原因：%s", channelError.ChannelName, channelError.ChannelId, reason)
		NotifyRootUser(formatNotifyType(channelError.ChannelId, common.ChannelStatusAutoDisabled), subject, content)
	}
}

func EnableChannel(channelId int, usingKey string, channelName string) {
	success := model.UpdateChannelStatus(channelId, usingKey, common.ChannelStatusEnabled, "")
	if success {
		subject := fmt.Sprintf("通道「%s」（#%d）已被启用", channelName, channelId)
		content := fmt.Sprintf("通道「%s」（#%d）已被启用", channelName, channelId)
		NotifyRootUser(formatNotifyType(channelId, common.ChannelStatusEnabled), subject, content)
	}
}

func ShouldDisableChannel(channelType int, err *types.NewAPIError) (bool, AutoDisableReason) {
	if !common.AutomaticDisableChannelEnabled {
		return false, AutoDisableReasonGlobalDisabled
	}
	if err == nil {
		return false, AutoDisableReasonNoError
	}
	if err.GetErrorCode() == types.ErrorCodeOpenCodeGoRPMLimit {
		return false, AutoDisableReasonOpenCodeGoSoftLimit
	}
	originalStatusCode := err.GetOriginalHTTPStatusCode()

	// A New API channel represents the entire lower gateway. A downstream 429
	// must never permanently disable that shared entry point.
	if channelType == constant.ChannelTypeNewAPI && originalStatusCode == http.StatusTooManyRequests {
		return false, AutoDisableReasonNewAPI429
	}

	// OpenCodeGo uses 429 both for transient RPM pressure and for durable account
	// limits. Only an administrator-configured keyword can distinguish the latter
	// safely; status-code and generic channel-error rules must not ban every 429.
	if channelType == constant.ChannelTypeOpenCodeGo && originalStatusCode == http.StatusTooManyRequests {
		if matchesAutomaticDisableKeyword(err) {
			return true, AutoDisableReasonKeyword
		}
		return false, AutoDisableReasonOpenCodeGoTransient429
	}
	if types.IsChannelError(err) {
		return true, AutoDisableReasonChannelError
	}
	if types.IsSkipRetryError(err) {
		return false, AutoDisableReasonSkipRetry
	}
	if operation_setting.ShouldDisableByStatusCode(err.StatusCode) {
		return true, AutoDisableReasonStatusCode
	}

	if matchesAutomaticDisableKeyword(err) {
		return true, AutoDisableReasonKeyword
	}
	return false, AutoDisableReasonNoMatchingRule
}

func matchesAutomaticDisableKeyword(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	lowerMessage := strings.ToLower(err.Error())
	search, _ := AcSearch(lowerMessage, operation_setting.AutomaticDisableKeywords, true)
	return search
}

func ShouldEnableChannel(newAPIError *types.NewAPIError, status int) bool {
	if !common.AutomaticEnableChannelEnabled {
		return false
	}
	if newAPIError != nil {
		return false
	}
	if status != common.ChannelStatusAutoDisabled {
		return false
	}
	return true
}
