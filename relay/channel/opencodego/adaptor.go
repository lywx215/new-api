package opencodego

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
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
	header.Set("Authorization", "Bearer "+info.ApiKey)
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	if a.activeProtocol(info) == ProtocolAnthropic {
		return a.claude.ConvertOpenAIRequest(c, info, request)
	}
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
	converted, err := service.ClaudeToOpenAIRequest(*request, info)
	if err != nil {
		return nil, err
	}
	if info.IsStream {
		converted.StreamOptions = &dto.StreamOptions{IncludeUsage: true}
	}
	return converted, nil
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

func (a *Adaptor) ConvertOpenAIResponsesRequest(*gin.Context, *relaycommon.RelayInfo, dto.OpenAIResponsesRequest) (any, error) {
	return nil, errors.New("OpenCodeGo does not support responses requests")
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
	if protocol == ProtocolAnthropic {
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
