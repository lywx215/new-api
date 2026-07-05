package opencodego

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveProtocol(t *testing.T) {
	overrides := map[string]string{
		"qwen3.*":   "openai",
		"custom-*":  "anthropic",
		"custom-v1": "openai",
	}
	tests := []struct {
		model string
		want  Protocol
	}{
		{model: "glm-5.2", want: ProtocolOpenAI},
		{model: "minimax-m3", want: ProtocolAnthropic},
		{model: "qwen3.7-max", want: ProtocolOpenAI},
		{model: "custom-v1", want: ProtocolOpenAI},
		{model: "custom-v2", want: ProtocolAnthropic},
		{model: "unknown", want: ProtocolOpenAI},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.want, resolveProtocol(tt.model, overrides))
		})
	}
}

func TestAdaptorRoutesAndAuthenticatesByModelProtocol(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		model      string
		wantURL    string
		wantHeader string
	}{
		{model: "glm-5.2", wantURL: "https://opencode.ai/zen/go/v1/chat/completions", wantHeader: "Bearer secret"},
		{model: "qwen3.7-max", wantURL: "https://opencode.ai/zen/go/v1/messages", wantHeader: "secret"},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
				ChannelBaseUrl:    "https://opencode.ai/zen/go",
				UpstreamModelName: tt.model,
				ApiKey:            "secret",
			}}
			adaptor := &Adaptor{}
			adaptor.Init(info)

			url, err := adaptor.GetRequestURL(info)
			require.NoError(t, err)
			assert.Equal(t, tt.wantURL, url)

			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
			headers := http.Header{}
			require.NoError(t, adaptor.SetupRequestHeader(c, &headers, info))
			if adaptor.protocol == ProtocolAnthropic {
				assert.Equal(t, tt.wantHeader, headers.Get("x-api-key"))
				assert.Equal(t, "2023-06-01", headers.Get("anthropic-version"))
				assert.Empty(t, headers.Get("Authorization"))
			} else {
				assert.Equal(t, tt.wantHeader, headers.Get("Authorization"))
				assert.Empty(t, headers.Get("x-api-key"))
			}
		})
	}
}

func TestOpenAIStreamingRequestAlwaysIncludesUsage(t *testing.T) {
	info := &relaycommon.RelayInfo{
		IsStream: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "mimo-v2.5",
		},
		RelayFormat: types.RelayFormatOpenAI,
	}
	request := &dto.GeneralOpenAIRequest{Model: "mimo-v2.5"}
	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)
	require.NoError(t, err)

	actual := converted.(*dto.GeneralOpenAIRequest)
	require.NotNil(t, actual.StreamOptions)
	assert.True(t, actual.StreamOptions.IncludeUsage)
}

func TestAdaptorConvertsAcrossClientAndModelProtocols(t *testing.T) {
	t.Run("OpenAI client to Anthropic model", func(t *testing.T) {
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "minimax-m3"},
		}
		request := &dto.GeneralOpenAIRequest{
			Model:    "minimax-m3",
			Messages: []dto.Message{{Role: "user", Content: "hello"}},
		}

		converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)
		require.NoError(t, err)
		assert.IsType(t, &dto.ClaudeRequest{}, converted)
	})

	t.Run("Anthropic client to OpenAI model", func(t *testing.T) {
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "glm-5.2"},
		}
		request := &dto.ClaudeRequest{
			Model:     "glm-5.2",
			MaxTokens: common.GetPointer[uint](100),
			Messages:  []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
		}

		converted, err := (&Adaptor{}).ConvertClaudeRequest(nil, info, request)
		require.NoError(t, err)
		assert.IsType(t, &dto.GeneralOpenAIRequest{}, converted)
	})
}

func TestNormalizeUsagePreservesProtocolSemantics(t *testing.T) {
	openAIUsage := &dto.Usage{
		PromptTokens:          100,
		CompletionTokens:      10,
		PromptCacheMissTokens: 30,
		PromptTokensDetails:   dto.InputTokenDetails{CachedTokens: 70},
	}
	normalizedOpenAI := normalizeUsage(openAIUsage, ProtocolOpenAI)
	applyNormalizedUsage(openAIUsage, normalizedOpenAI)
	assert.EqualValues(t, 30, normalizedOpenAI.UncachedInput)
	assert.EqualValues(t, 70, normalizedOpenAI.CacheRead)
	assert.Equal(t, 100, openAIUsage.InputTokens)
	assert.Equal(t, "openai", openAIUsage.UsageSemantic)

	anthropicUsage := &dto.Usage{
		PromptTokens:                16,
		CompletionTokens:            119,
		PromptTokensDetails:         dto.InputTokenDetails{CachedTokens: 6304, CachedCreationTokens: 20},
		ClaudeCacheCreation5mTokens: 20,
	}
	normalizedAnthropic := normalizeUsage(anthropicUsage, ProtocolAnthropic)
	applyNormalizedUsage(anthropicUsage, normalizedAnthropic)
	assert.EqualValues(t, 16, normalizedAnthropic.UncachedInput)
	assert.EqualValues(t, 6304, normalizedAnthropic.CacheRead)
	assert.EqualValues(t, 20, normalizedAnthropic.CacheWrite5m)
	assert.Equal(t, 6340, anthropicUsage.InputTokens)
	assert.Equal(t, "anthropic", anthropicUsage.UsageSemantic)
}
