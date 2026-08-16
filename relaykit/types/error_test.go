package types

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewAPIErrorPreservesOriginalHTTPStatusCode(t *testing.T) {
	err := NewErrorWithStatusCode(errors.New("upstream failed"), ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)

	assert.Equal(t, http.StatusTooManyRequests, err.GetOriginalHTTPStatusCode())
	err.CaptureOriginalHTTPStatusCode()
	err.StatusCode = http.StatusServiceUnavailable
	err.CaptureOriginalHTTPStatusCode()

	assert.Equal(t, http.StatusTooManyRequests, err.GetOriginalHTTPStatusCode())
	assert.Equal(t, http.StatusServiceUnavailable, err.StatusCode)
}

func TestMaskSensitiveErrorPrefersStructuredRelayMessage(t *testing.T) {
	err := WithOpenAIError(OpenAIError{
		Message: "Weekly usage limit reached",
		Type:    "GoUsageLimitError",
		Code:    "unknown_error",
	}, http.StatusTooManyRequests)
	err.Err = errors.New(`Weekly usage limit reached, body: {"workspace":"wrk_secret","api_key":"raw-secret"}`)

	masked := err.MaskSensitiveErrorWithStatusCode()

	assert.Equal(t, "status_code=429, Weekly usage limit reached", masked)
	assert.NotContains(t, masked, "wrk_secret")
	assert.NotContains(t, masked, "raw-secret")
}
