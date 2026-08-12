package claudemessages

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeMessagesRequestToOpenAIChatPreservesForcedToolChoice(t *testing.T) {
	request := dto.ClaudeRequest{
		Model: "gpt-5.6-luna",
		Tools: []dto.Tool{{
			Name:         "lookup",
			InputSchema:  map[string]any{"type": "object"},
			CacheControl: []byte(`{"type":"ephemeral","ttl":"1h"}`),
		}},
		ToolChoice: dto.ClaudeToolChoice{
			Type:                   "tool",
			Name:                   "lookup",
			DisableParallelToolUse: true,
		},
	}

	converted, err := ClaudeMessagesRequestToOpenAIChat(request, nil)
	require.NoError(t, err)
	require.Len(t, converted.Tools, 1)
	assert.JSONEq(t, `{"type":"ephemeral","ttl":"1h"}`, string(converted.Tools[0].CacheControl))
	assert.Equal(t, false, *converted.ParallelTooCalls)
	assert.Equal(t, map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "lookup",
		},
	}, converted.ToolChoice)
}
