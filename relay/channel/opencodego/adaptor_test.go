package opencodego

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveProtocol(t *testing.T) {
	overrides := map[string]string{
		"qwen3.*":   "openai",
		"custom-*":  "anthropic",
		"custom-v1": "responses",
	}
	tests := []struct {
		model string
		want  Protocol
	}{
		{model: "glm-5.2", want: ProtocolOpenAI},
		{model: "gpt-5.6-luna", want: ProtocolResponses},
		{model: "minimax-m3", want: ProtocolAnthropic},
		{model: "minimax-m2.5", want: ProtocolAnthropic},
		{model: "qwen3.8-max", want: ProtocolOpenAI},
		{model: "qwen3.7-max", want: ProtocolOpenAI},
		{model: "custom-v1", want: ProtocolResponses},
		{model: "custom-v2", want: ProtocolAnthropic},
		{model: "unknown", want: ProtocolOpenAI},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.want, resolveProtocol(tt.model, overrides))
		})
	}
	assert.Equal(t, ProtocolAnthropic, resolveProtocol("qwen3.8-max", nil))
	assert.Equal(t, ProtocolAnthropic, resolveProtocol("minimax-m2.5", nil))
}

func TestPassThroughCompatibleRequiresNativeProtocol(t *testing.T) {
	tests := []struct {
		model  string
		format types.RelayFormat
		want   bool
	}{
		{model: "glm-5.2", format: types.RelayFormatOpenAI, want: true},
		{model: "glm-5.2", format: types.RelayFormatClaude, want: false},
		{model: "minimax-m2.5", format: types.RelayFormatClaude, want: true},
		{model: "minimax-m2.5", format: types.RelayFormatOpenAI, want: false},
		{model: "gpt-5.6-luna", format: types.RelayFormatOpenAIResponses, want: true},
		{model: "gpt-5.6-luna", format: types.RelayFormatOpenAI, want: false},
	}
	for _, test := range tests {
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: test.model}}
		assert.Equal(t, test.want, PassThroughCompatible(info, test.format), test.model)
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
		{model: "gpt-5.6-luna", wantURL: "https://opencode.ai/zen/go/v1/responses", wantHeader: "Bearer secret"},
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

func TestKimiRequestDropsUnsupportedTemperatureOnly(t *testing.T) {
	tests := []struct {
		name            string
		model           string
		temperature     float64
		wantTemperature *float64
	}{
		{
			name:        "unsupported Kimi K3 temperature is omitted",
			model:       "kimi-k3",
			temperature: 0,
		},
		{
			name:        "unsupported Kimi temperature is omitted",
			model:       "kimi-k2.7-code",
			temperature: 0.7,
		},
		{
			name:            "supported Kimi temperature is preserved",
			model:           "kimi-k2.7-code",
			temperature:     1,
			wantTemperature: common.GetPointer(1.0),
		},
		{
			name:            "other model temperature is preserved",
			model:           "glm-5.2",
			temperature:     0.7,
			wantTemperature: common.GetPointer(0.7),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: tt.model},
			}
			request := &dto.GeneralOpenAIRequest{
				Model:       tt.model,
				Temperature: common.GetPointer(tt.temperature),
			}

			converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)

			require.NoError(t, err)
			actual := converted.(*dto.GeneralOpenAIRequest)
			if tt.wantTemperature == nil {
				assert.Nil(t, actual.Temperature)
			} else {
				require.NotNil(t, actual.Temperature)
				assert.Equal(t, *tt.wantTemperature, *actual.Temperature)
			}
		})
	}
}

func TestOpenAIRequestDropsOnlyConfirmedUnsupportedTopPZero(t *testing.T) {
	for _, model := range []string{"glm-5.2", "glm-5.1", "deepseek-v4-pro", "deepseek-v4-flash"} {
		t.Run(model, func(t *testing.T) {
			zero := 0.0
			request := &dto.GeneralOpenAIRequest{Model: model, TopP: &zero}
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: model}}
			converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)
			require.NoError(t, err)
			assert.Nil(t, converted.(*dto.GeneralOpenAIRequest).TopP)
		})
	}

	zero := 0.0
	request := &dto.GeneralOpenAIRequest{Model: "mimo-v2.5", TopP: &zero}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "mimo-v2.5"}}
	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)
	require.NoError(t, err)
	require.NotNil(t, converted.(*dto.GeneralOpenAIRequest).TopP)
	assert.Zero(t, *converted.(*dto.GeneralOpenAIRequest).TopP)
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

	t.Run("OpenAI client to Responses model", func(t *testing.T) {
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-5.6-luna"}}
		request := &dto.GeneralOpenAIRequest{Model: "gpt-5.6-luna", Messages: []dto.Message{{Role: "user", Content: "hello"}}}

		converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)
		require.NoError(t, err)
		assert.IsType(t, &dto.OpenAIResponsesRequest{}, converted)
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
