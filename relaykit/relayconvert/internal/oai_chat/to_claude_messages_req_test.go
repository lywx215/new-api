package oaichat

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIChatRequestToClaudeMessagesPreservesCacheControl(t *testing.T) {
	req := dto.GeneralOpenAIRequest{
		Model:     "qwen3.6-plus",
		MaxTokens: lo.ToPtr(uint(32)),
		Tools: []dto.ToolCallRequest{{
			Type:         "function",
			CacheControl: json.RawMessage(`{"type":"ephemeral","ttl":"1h"}`),
			Function: dto.FunctionRequest{
				Name:       "lookup",
				Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
			},
		}},
		Messages: []dto.Message{
			{Role: "system", Content: []any{map[string]any{
				"type":          "text",
				"text":          "stable system",
				"cache_control": map[string]any{"type": "ephemeral", "ttl": "5m"},
			}}},
			{Role: "user", Content: []any{map[string]any{
				"type":          "text",
				"text":          "question",
				"cache_control": map[string]any{"type": "ephemeral"},
			}}},
		},
	}

	got, err := OpenAIChatRequestToClaudeMessages(t.Context(), &convmeta.Values{}, req)
	require.NoError(t, err)

	system, err := anyToClaudeMedia(got.System)
	require.NoError(t, err)
	require.Len(t, system, 1)
	assert.JSONEq(t, `{"type":"ephemeral","ttl":"5m"}`, string(system[0].CacheControl))

	messages, err := got.Messages[0].ParseContent()
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.JSONEq(t, `{"type":"ephemeral"}`, string(messages[0].CacheControl))

	tools, ok := got.Tools.([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	tool, ok := tools[0].(*dto.Tool)
	require.True(t, ok)
	assert.JSONEq(t, `{"type":"ephemeral","ttl":"1h"}`, string(tool.CacheControl))
}

func anyToClaudeMedia(value any) ([]dto.ClaudeMediaMessage, error) {
	return kitutil.Any2Type[[]dto.ClaudeMediaMessage](value)
}
