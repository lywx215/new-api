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

func TestAnthropicStreamMergesStartAndDeltaUsageWithoutSSESpace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	body := strings.Join([]string{
		`data:{"type":"message_start","message":{"id":"msg-1","model":"qwen3.7-max","usage":{"input_tokens":16,"output_tokens":0,"cache_creation_input_tokens":20,"cache_read_input_tokens":6304}}}`,
		`data:{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`data:{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":16,"output_tokens":119,"cache_creation_input_tokens":20,"cache_read_input_tokens":6304}}`,
		`data:{"type":"message_stop"}`,
		`data:{"type":"ping"}`,
		"",
	}, "\n")
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatClaude,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenCodeGo,
			UpstreamModelName: "qwen3.7-max",
		},
	}
	adaptor := &Adaptor{}
	adaptor.Init(info)

	rawUsage, apiErr := adaptor.DoResponse(c, resp, info)
	require.Nil(t, apiErr)
	usage, ok := rawUsage.(*dto.Usage)
	require.True(t, ok)
	assert.Equal(t, 16, usage.PromptTokens)
	assert.Equal(t, 119, usage.CompletionTokens)
	assert.Equal(t, 6304, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 20, usage.PromptTokensDetails.CachedCreationTokens)
	assert.Equal(t, "anthropic", usage.UsageSemantic)
	assert.Equal(t, "opencodego:standard", usage.UsageSource)
}
