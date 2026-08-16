package controller

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayAutoDisableHonorsChannelAutoBan(t *testing.T) {
	originalAutomaticDisable := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = originalAutomaticDisable })

	channelError := types.ChannelError{
		ChannelType: constant.ChannelTypeOpenAI,
		AutoBan:     false,
	}
	serverError := types.NewErrorWithStatusCode(
		errors.New("upstream failed"),
		types.ErrorCode("channel:server_error"),
		http.StatusInternalServerError,
	)

	shouldDisable, reason := shouldAutoDisableChannelAfterRelayError(channelError, serverError)
	assert.False(t, shouldDisable)
	assert.Equal(t, service.AutoDisableReasonChannelAutoBanDisabled, reason)

	channelError.AutoBan = true
	shouldDisable, reason = shouldAutoDisableChannelAfterRelayError(channelError, serverError)
	assert.True(t, shouldDisable)
	assert.Equal(t, service.AutoDisableReasonChannelError, reason)
}

func TestRelayErrorLogPreviewMasksSensitiveContent(t *testing.T) {
	err := types.WithOpenAIError(types.OpenAIError{
		Message: "weekly limit workspace=wrk_metadata_secret url=https://opencode.ai/workspace/wrk_path_secret/go?token=supersecret",
		Type:    "GoUsageLimitError",
		Code:    "unknown_error",
	}, http.StatusTooManyRequests)
	err.Err = errors.New(`raw body {"workspace":"wrk_raw_secret","api_key":"raw-secret"}`)

	preview := relayErrorLogPreview(err)

	assert.Contains(t, preview, "weekly limit")
	assert.NotContains(t, preview, "wrk_metadata_secret")
	assert.NotContains(t, preview, "wrk_path_secret")
	assert.NotContains(t, preview, "wrk_raw_secret")
	assert.NotContains(t, preview, "supersecret")
	assert.NotContains(t, preview, "raw-secret")
	assert.NotContains(t, preview, "opencode.ai")
}

func TestProcessChannelErrorMasksSensitiveLogBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var logBuffer bytes.Buffer
	common.LogWriterMu.Lock()
	originalWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logBuffer
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = originalWriter
		common.LogWriterMu.Unlock()
	})

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	err := types.NewErrorWithStatusCode(
		errors.New("weekly limit: https://opencode.ai/workspace/wrk_secret/go?token=supersecret, metadata workspace=wrk_metadata_secret"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusTooManyRequests,
		types.ErrOptionWithNoRecordErrorLog(),
	)
	processChannelError(ctx, types.ChannelError{
		ChannelId:   17,
		ChannelType: constant.ChannelTypeOpenCodeGo,
		AutoBan:     false,
	}, err)

	logOutput := logBuffer.String()
	assert.Contains(t, logOutput, "auto-disable reason: channel_auto_ban_disabled")
	assert.NotContains(t, logOutput, "wrk_secret")
	assert.NotContains(t, logOutput, "wrk_metadata_secret")
	assert.NotContains(t, logOutput, "supersecret")
	assert.NotContains(t, logOutput, "opencode.ai")
}

func TestApplyRelayErrorHeadersOnlyForFinal429(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	err := types.NewErrorWithStatusCode(errors.New("limited"), types.ErrorCodeOpenCodeGoRPMLimit, http.StatusTooManyRequests)
	err.SetRetryAfterHeader("17")
	applyRelayErrorHeaders(ctx, err)
	assert.Equal(t, "17", recorder.Header().Get("Retry-After"))

	successRecorder := httptest.NewRecorder()
	successContext, _ := gin.CreateTestContext(successRecorder)
	err.StatusCode = http.StatusOK
	applyRelayErrorHeaders(successContext, err)
	require.Empty(t, successRecorder.Header().Get("Retry-After"))
}

func TestOpenCodeGoSoftLimitSkipsType60Retry(t *testing.T) {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	controlledLimit := types.NewErrorWithStatusCode(
		errors.New("all OpenCodeGo accounts are limited"),
		types.ErrorCodeOpenCodeGoRPMLimit,
		http.StatusTooManyRequests,
	)
	assert.False(t, shouldRetry(ctx, controlledLimit, 3))

	ordinaryLimit := types.NewErrorWithStatusCode(
		errors.New("ordinary upstream limit"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusTooManyRequests,
	)
	assert.True(t, shouldRetry(ctx, ordinaryLimit, 3))
}

func TestType60ControlledLimitPreservesRetryAfterAndSkipsRetry(t *testing.T) {
	upstream := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"23"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"all accounts limited","type":"new_api_error","code":"opencodego_rpm_soft_limit"}}`)),
	}
	err := service.RelayErrorHandler(context.Background(), upstream, false)
	require.Equal(t, types.ErrorCodeOpenCodeGoRPMLimit, err.GetErrorCode())
	require.Equal(t, "23", err.GetRetryAfterHeader())

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	assert.False(t, shouldRetry(ctx, err, 3))
	applyRelayErrorHeaders(ctx, err)
	assert.Equal(t, "23", recorder.Header().Get("Retry-After"))
}

func TestType60TwoHTTPHopsPreserveRetryAfterFormats(t *testing.T) {
	httpDate := time.Now().UTC().Add(30 * time.Second).Format(http.TimeFormat)
	for _, retryAfter := range []string{"23", httpDate} {
		t.Run(retryAfter, func(t *testing.T) {
			lower := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", retryAfter)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":{"message":"all accounts limited","type":"new_api_error","code":"opencodego_rpm_soft_limit"}}`)
			}))
			t.Cleanup(lower.Close)

			upper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				response, requestErr := http.Get(lower.URL)
				if requestErr != nil {
					http.Error(w, requestErr.Error(), http.StatusBadGateway)
					return
				}
				defer response.Body.Close()
				relayErr := service.RelayErrorHandler(request.Context(), response, false)
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				applyRelayErrorHeaders(ctx, relayErr)
				for key, values := range recorder.Header() {
					w.Header()[key] = values
				}
				w.WriteHeader(relayErr.StatusCode)
			}))
			t.Cleanup(upper.Close)

			response, err := http.Get(upper.URL)
			require.NoError(t, err)
			defer response.Body.Close()
			assert.Equal(t, http.StatusTooManyRequests, response.StatusCode)
			assert.Equal(t, retryAfter, response.Header.Get("Retry-After"))
		})
	}
}

func TestIntermediate429DoesNotLeakRetryAfterAfterSuccessfulRetry(t *testing.T) {
	upper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		limited := &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"19"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"limited"}}`)),
		}
		intermediateErr := service.RelayErrorHandler(request.Context(), limited, false)
		if intermediateErr.GetRetryAfterHeader() != "19" {
			http.Error(w, "intermediate Retry-After was not captured", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upper.Close)

	response, err := http.Get(upper.URL)
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Empty(t, response.Header.Get("Retry-After"))
}
