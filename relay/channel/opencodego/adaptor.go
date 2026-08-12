package opencodego

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/gin-gonic/gin"
)

type Adaptor struct {
	protocol Protocol
	openai   openai.Adaptor
	claude   claude.Adaptor
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
	a.protocol = protocolForInfo(info)
	if a.protocol == ProtocolAnthropic {
		a.claude.Init(info)
		return
	}
	a.openai.Init(info)
}

func protocolForInfo(info *relaycommon.RelayInfo) Protocol {
	if info == nil || info.ChannelMeta == nil {
		return ProtocolOpenAI
	}
	return resolveProtocol(info.UpstreamModelName, info.ChannelOtherSettings.ModelProtocols)
}

func (a *Adaptor) activeProtocol(info *relaycommon.RelayInfo) Protocol {
	if a.protocol == "" {
		a.protocol = protocolForInfo(info)
	}
	return a.protocol
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if info == nil {
		return "", errors.New("relay info is nil")
	}
	baseURL := strings.TrimRight(info.ChannelBaseUrl, "/")
	if a.activeProtocol(info) == ProtocolAnthropic {
		return baseURL + "/v1/messages", nil
	}
	if a.activeProtocol(info) == ProtocolResponses {
		return baseURL + "/v1/responses", nil
	}
	return baseURL + "/v1/chat/completions", nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, header *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, header)
	if a.activeProtocol(info) == ProtocolAnthropic {
		header.Del("Authorization")
		header.Set("x-api-key", info.ApiKey)
		version := c.GetHeader("anthropic-version")
		if version == "" {
			version = "2023-06-01"
		}
		header.Set("anthropic-version", version)
		if beta := c.GetHeader("anthropic-beta"); beta != "" {
			header.Set("anthropic-beta", beta)
		}
		return nil
	}
	header.Del("x-api-key")
	header.Del("anthropic-version")
	header.Del("anthropic-beta")
	header.Set("Authorization", "Bearer "+info.ApiKey)
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	if a.activeProtocol(info) == ProtocolAnthropic {
		converted, err := a.claude.ConvertOpenAIRequest(c, info, request)
		if err != nil {
			return nil, err
		}
		if claudeRequest, ok := converted.(*dto.ClaudeRequest); ok &&
			info != nil && !info.ChannelOtherSettings.DisableOpenCodeGoAutoCache &&
			billing_setting.OpenCodeGoOfficialDefaultsEnabled() {
			injectStableCacheBreakpoint(claudeRequest)
		}
		return converted, nil
	}
	if a.activeProtocol(info) == ProtocolResponses {
		result, err := service.ConvertRequest(c, info, types.RelayFormatOpenAIResponses, request)
		if err != nil {
			return nil, err
		}
		return result.Value, nil
	}
	normalizeOpenAIRequest(info, request)
	if info.IsStream {
		request.StreamOptions = &dto.StreamOptions{IncludeUsage: true}
	}
	return request, nil
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	if a.activeProtocol(info) == ProtocolAnthropic {
		return request, nil
	}
	if a.activeProtocol(info) == ProtocolResponses {
		result, err := service.ConvertRequest(c, info, types.RelayFormatOpenAIResponses, request)
		if err != nil {
			return nil, err
		}
		return result.Value, nil
	}
	converted, err := service.ClaudeToOpenAIRequest(*request, info)
	if err != nil {
		return nil, err
	}
	normalizeOpenAIRequest(info, converted)
	if info.IsStream {
		converted.StreamOptions = &dto.StreamOptions{IncludeUsage: true}
	}
	return converted, nil
}

func normalizeOpenAIRequest(info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) {
	if request == nil {
		return
	}
	model := request.Model
	if info != nil && info.UpstreamModelName != "" {
		model = info.UpstreamModelName
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if request.Temperature != nil && (model == "kimi-k3" || model == "kimi-k2.7-code") && *request.Temperature != 1 {
		request.Temperature = nil
	}
	if request.TopP != nil && *request.TopP == 0 {
		switch model {
		case "glm-5.2", "glm-5.1", "deepseek-v4-pro", "deepseek-v4-flash":
			request.TopP = nil
		}
	}
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	if a.activeProtocol(info) == ProtocolAnthropic {
		return a.claude.ConvertGeminiRequest(c, info, request)
	}
	return a.openai.ConvertGeminiRequest(c, info, request)
}

func (a *Adaptor) ConvertRerankRequest(*gin.Context, int, dto.RerankRequest) (any, error) {
	return nil, errors.New("OpenCodeGo does not support rerank requests")
}

func (a *Adaptor) ConvertEmbeddingRequest(*gin.Context, *relaycommon.RelayInfo, dto.EmbeddingRequest) (any, error) {
	return nil, errors.New("OpenCodeGo does not support embedding requests")
}

func (a *Adaptor) ConvertAudioRequest(*gin.Context, *relaycommon.RelayInfo, dto.AudioRequest) (io.Reader, error) {
	return nil, errors.New("OpenCodeGo does not support audio requests")
}

func (a *Adaptor) ConvertImageRequest(*gin.Context, *relaycommon.RelayInfo, dto.ImageRequest) (any, error) {
	return nil, errors.New("OpenCodeGo does not support image requests")
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	if info != nil && info.RelayMode == relayconstant.RelayModeResponsesCompact {
		return nil, errors.New("OpenCodeGo does not support responses compaction requests")
	}
	if a.activeProtocol(info) != ProtocolResponses {
		return nil, errors.New("OpenCodeGo Responses endpoint requires a model configured with the responses protocol")
	}
	return a.openai.ConvertOpenAIResponsesRequest(c, info, request)
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (any, *types.NewAPIError) {
	var (
		usage any
		err   *types.NewAPIError
	)
	protocol := a.activeProtocol(info)
	if protocol == ProtocolResponses {
		info.FinalRequestRelayFormat = types.RelayFormatOpenAIResponses
		switch info.RelayFormat {
		case types.RelayFormatOpenAIResponses:
			if info.IsStream {
				usage, err = openai.OaiResponsesStreamHandler(c, info, resp)
			} else {
				usage, err = openai.OaiResponsesHandler(c, info, resp)
			}
		default:
			upstreamStream := strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
			clientStream := false
			switch request := info.Request.(type) {
			case *dto.GeneralOpenAIRequest:
				var httpRequest *http.Request
				if c != nil {
					httpRequest = c.Request
				}
				clientStream = request.IsStream(httpRequest)
			case *dto.ClaudeRequest:
				clientStream = request.Stream != nil && *request.Stream
			}
			if upstreamStream && clientStream {
				usage, err = openai.OaiResponsesToChatStreamHandler(c, info, resp)
			} else if upstreamStream {
				usage, err = openai.OaiResponsesToChatBufferedStreamHandler(c, info, resp)
			} else {
				usage, err = openai.OaiResponsesToChatHandler(c, info, resp)
			}
		}
	} else if protocol == ProtocolAnthropic {
		usage, err = a.claude.DoResponse(c, resp, info)
	} else {
		info.FinalRequestRelayFormat = types.RelayFormatOpenAI
		usage, err = a.openai.DoResponse(c, resp, info)
	}
	if err == nil {
		if typed, ok := usage.(*dto.Usage); ok {
			normalized := normalizeUsage(typed, protocol)
			applyNormalizedUsage(typed, normalized)
		}
	}
	return usage, err
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}
