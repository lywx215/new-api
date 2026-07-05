package opencodego

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIStreamKeepsUsageBeforeInferenceCost(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	body := strings.Join([]string{
		`data: {"id":"chunk-1","model":"mimo-v2.5","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
		`data: {"id":"chunk-1","model":"mimo-v2.5","choices":[],"usage":{"prompt_tokens":5490,"completion_tokens":56,"total_tokens":5546,"prompt_cache_hit_tokens":5376,"prompt_cache_miss_tokens":114,"prompt_tokens_details":{"cached_tokens":5376}}}`,
		`data: {"choices":[],"x-opencode-type":"inference-cost","cost":"0.00004669","normalizedUsage":{"inputTokens":114,"outputTokens":56,"reasoningTokens":51,"cacheReadTokens":5376,"cacheWrite5mTokens":0,"cacheWrite1hTokens":0}}`,
		`data: [DONE]`,
		"",
	}, "\n")
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenCodeGo,
			UpstreamModelName: "mimo-v2.5",
		},
	}
	adaptor := &Adaptor{}
	adaptor.Init(info)

	rawUsage, apiErr := adaptor.DoResponse(c, resp, info)
	require.Nil(t, apiErr)
	usage, ok := rawUsage.(*dto.Usage)
	require.True(t, ok)
	assert.Equal(t, 5490, usage.PromptTokens)
	assert.Equal(t, 5376, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 114, usage.PromptCacheMissTokens)
	assert.Equal(t, "opencodego:standard", usage.UsageSource)
	assert.Equal(t, 1, strings.Count(recorder.Body.String(), `"usage"`))
}

func TestOpenAIStreamUsesInferenceCostOnlyAsFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	body := "data:{\"x-opencode-type\":\"inference-cost\",\"cost\":\"0.25\",\"normalizedUsage\":{\"inputTokens\":12,\"outputTokens\":3,\"cacheReadTokens\":88}}\n\ndata:[DONE]\n\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenCodeGo,
			UpstreamModelName: "deepseek-v4-pro",
		},
	}

	rawUsage, apiErr := (&Adaptor{}).DoResponse(c, resp, info)
	require.Nil(t, apiErr)
	usage := rawUsage.(*dto.Usage)
	assert.Equal(t, 100, usage.PromptTokens)
	assert.Equal(t, 88, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 3, usage.CompletionTokens)
	assert.Equal(t, "opencodego:inference-cost", usage.UsageSource)
	assert.Contains(t, recorder.Body.String(), `"usage"`)
}

func TestOpenAIStreamHidesUsageButKeepsNormalizedBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	body := strings.Join([]string{
		`data: {"id":"chunk-1","model":"mimo-v2.5","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
		`data: {"id":"chunk-1","model":"mimo-v2.5","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"prompt_tokens_details":{"cached_tokens":70}}}`,
		`data: {"x-opencode-type":"inference-cost","normalizedUsage":{"inputTokens":30,"outputTokens":10,"cacheReadTokens":70}}`,
		`data: [DONE]`,
		"",
	}, "\n")
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenCodeGo,
			UpstreamModelName: "mimo-v2.5",
		},
	}

	rawUsage, apiErr := (&Adaptor{}).DoResponse(c, resp, info)

	require.Nil(t, apiErr)
	usage, ok := rawUsage.(*dto.Usage)
	require.True(t, ok)
	assert.Equal(t, 100, usage.InputTokens)
	assert.Equal(t, 70, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, "opencodego:standard", usage.UsageSource)
	assert.NotContains(t, strings.ToLower(recorder.Body.String()), `"usage":`)
}
