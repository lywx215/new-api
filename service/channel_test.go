package service

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldDisableChannelAppliesProviderAwareRules(t *testing.T) {
	originalEnabled := common.AutomaticDisableChannelEnabled
	originalKeywords := append([]string(nil), operation_setting.AutomaticDisableKeywords...)
	originalStatusRanges := append([]operation_setting.StatusCodeRange(nil), operation_setting.AutomaticDisableStatusCodeRanges...)
	t.Cleanup(func() {
		common.AutomaticDisableChannelEnabled = originalEnabled
		operation_setting.AutomaticDisableKeywords = originalKeywords
		operation_setting.AutomaticDisableStatusCodeRanges = originalStatusRanges
	})

	common.AutomaticDisableChannelEnabled = true
	operation_setting.AutomaticDisableKeywords = []string{"enable usage from your available balance"}
	require.NoError(t, operation_setting.AutomaticDisableStatusCodesFromString("429"))

	tests := []struct {
		name        string
		channelType int
		err         *types.NewAPIError
		disable     bool
		reason      AutoDisableReason
	}{
		{
			name:        "OpenCodeGo durable limit keyword overrides transient 429 protection",
			channelType: constant.ChannelTypeOpenCodeGo,
			err: types.NewErrorWithStatusCode(
				errors.New("Weekly usage limit reached. ENABLE USAGE FROM YOUR AVAILABLE BALANCE"),
				types.ErrorCodeBadResponseStatusCode,
				http.StatusTooManyRequests,
			),
			disable: true,
			reason:  AutoDisableReasonKeyword,
		},
		{
			name:        "OpenCodeGo transient 429 ignores generic status-code rule",
			channelType: constant.ChannelTypeOpenCodeGo,
			err: types.NewErrorWithStatusCode(
				errors.New("requests per minute exceeded"),
				types.ErrorCode("channel:rate_limited"),
				http.StatusTooManyRequests,
			),
			disable: false,
			reason:  AutoDisableReasonOpenCodeGoTransient429,
		},
		{
			name:        "New API 429 never disables shared lower gateway",
			channelType: constant.ChannelTypeNewAPI,
			err: types.NewErrorWithStatusCode(
				errors.New("enable usage from your available balance"),
				types.ErrorCodeBadResponseStatusCode,
				http.StatusTooManyRequests,
			),
			disable: false,
			reason:  AutoDisableReasonNewAPI429,
		},
		{
			name:        "controlled soft limit never disables any channel",
			channelType: constant.ChannelTypeOpenAI,
			err: types.NewErrorWithStatusCode(
				errors.New("enable usage from your available balance"),
				types.ErrorCodeOpenCodeGoRPMLimit,
				http.StatusTooManyRequests,
			),
			disable: false,
			reason:  AutoDisableReasonOpenCodeGoSoftLimit,
		},
		{
			name:        "OpenCodeGo non-429 still uses configured keyword",
			channelType: constant.ChannelTypeOpenCodeGo,
			err: types.NewErrorWithStatusCode(
				errors.New("enable usage from your available balance"),
				types.ErrorCodeBadResponseStatusCode,
				http.StatusBadGateway,
			),
			disable: true,
			reason:  AutoDisableReasonKeyword,
		},
		{
			name:        "ordinary providers retain configured 429 status rule",
			channelType: constant.ChannelTypeOpenAI,
			err: types.NewErrorWithStatusCode(
				errors.New("requests per minute exceeded"),
				types.ErrorCodeBadResponseStatusCode,
				http.StatusTooManyRequests,
			),
			disable: true,
			reason:  AutoDisableReasonStatusCode,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			disable, reason := ShouldDisableChannel(test.channelType, test.err)
			assert.Equal(t, test.disable, disable)
			assert.Equal(t, test.reason, reason)
		})
	}
}

func TestShouldDisableChannelPreservesCommonGates(t *testing.T) {
	originalEnabled := common.AutomaticDisableChannelEnabled
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = originalEnabled })

	channelError := types.NewErrorWithStatusCode(
		errors.New("upstream failed"),
		types.ErrorCode("channel:server_error"),
		http.StatusInternalServerError,
	)

	common.AutomaticDisableChannelEnabled = false
	disable, reason := ShouldDisableChannel(constant.ChannelTypeOpenAI, channelError)
	assert.False(t, disable)
	assert.Equal(t, AutoDisableReasonGlobalDisabled, reason)

	common.AutomaticDisableChannelEnabled = true
	disable, reason = ShouldDisableChannel(constant.ChannelTypeOpenAI, nil)
	assert.False(t, disable)
	assert.Equal(t, AutoDisableReasonNoError, reason)

	disable, reason = ShouldDisableChannel(constant.ChannelTypeOpenAI, channelError)
	assert.True(t, disable)
	assert.Equal(t, AutoDisableReasonChannelError, reason)

	skipRetryError := types.NewErrorWithStatusCode(
		errors.New("do not retry"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadRequest,
		types.ErrOptionWithSkipRetry(),
	)
	disable, reason = ShouldDisableChannel(constant.ChannelTypeOpenAI, skipRetryError)
	assert.False(t, disable)
	assert.Equal(t, AutoDisableReasonSkipRetry, reason)
}

func TestShouldDisableChannelClassifiesMappedErrorsByOriginalStatus(t *testing.T) {
	originalEnabled := common.AutomaticDisableChannelEnabled
	originalKeywords := append([]string(nil), operation_setting.AutomaticDisableKeywords...)
	originalStatusRanges := append([]operation_setting.StatusCodeRange(nil), operation_setting.AutomaticDisableStatusCodeRanges...)
	t.Cleanup(func() {
		common.AutomaticDisableChannelEnabled = originalEnabled
		operation_setting.AutomaticDisableKeywords = originalKeywords
		operation_setting.AutomaticDisableStatusCodeRanges = originalStatusRanges
	})

	common.AutomaticDisableChannelEnabled = true
	operation_setting.AutomaticDisableKeywords = []string{"durable quota limit"}
	require.NoError(t, operation_setting.AutomaticDisableStatusCodesFromString("401,429,500"))

	tests := []struct {
		name         string
		channelType  int
		upstreamCode int
		mappedCode   int
		message      string
		disable      bool
		reason       AutoDisableReason
	}{
		{
			name:         "OpenCodeGo transient 429 mapped to 401 remains protected",
			channelType:  constant.ChannelTypeOpenCodeGo,
			upstreamCode: http.StatusTooManyRequests,
			mappedCode:   http.StatusUnauthorized,
			message:      "requests per minute exceeded",
			disable:      false,
			reason:       AutoDisableReasonOpenCodeGoTransient429,
		},
		{
			name:         "OpenCodeGo keyword 429 mapped to 500 still disables",
			channelType:  constant.ChannelTypeOpenCodeGo,
			upstreamCode: http.StatusTooManyRequests,
			mappedCode:   http.StatusInternalServerError,
			message:      "durable quota limit reached",
			disable:      true,
			reason:       AutoDisableReasonKeyword,
		},
		{
			name:         "New API 429 mapped to 401 still protects shared gateway",
			channelType:  constant.ChannelTypeNewAPI,
			upstreamCode: http.StatusTooManyRequests,
			mappedCode:   http.StatusUnauthorized,
			message:      "durable quota limit reached",
			disable:      false,
			reason:       AutoDisableReasonNewAPI429,
		},
		{
			name:         "OpenCodeGo non-429 mapped to 429 follows generic mapped status rule",
			channelType:  constant.ChannelTypeOpenCodeGo,
			upstreamCode: http.StatusInternalServerError,
			mappedCode:   http.StatusTooManyRequests,
			message:      "ordinary upstream failure",
			disable:      true,
			reason:       AutoDisableReasonStatusCode,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := types.NewErrorWithStatusCode(
				errors.New(test.message),
				types.ErrorCodeBadResponseStatusCode,
				test.upstreamCode,
			)
			mapping := fmt.Sprintf(`{"%d":%d}`, test.upstreamCode, test.mappedCode)
			ResetStatusCode(err, mapping)

			assert.Equal(t, test.mappedCode, err.StatusCode)
			assert.Equal(t, test.upstreamCode, err.GetOriginalHTTPStatusCode())
			disable, reason := ShouldDisableChannel(test.channelType, err)
			assert.Equal(t, test.disable, disable)
			assert.Equal(t, test.reason, reason)
		})
	}
}

func TestSanitizeChannelDisableReasonMasksOpenCodeWorkspaceContent(t *testing.T) {
	reason := "status_code=429, weekly limit: https://opencode.ai/workspace/wrk_secret/go?token=supersecret, metadata workspace=wrk_metadata_secret"

	sanitized := sanitizeChannelDisableReason(reason)

	assert.Contains(t, sanitized, "status_code=429")
	assert.NotContains(t, sanitized, "wrk_secret")
	assert.NotContains(t, sanitized, "wrk_metadata_secret")
	assert.NotContains(t, sanitized, "supersecret")
	assert.NotContains(t, sanitized, "opencode.ai")
}
