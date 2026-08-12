package opencodego

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInjectStableCacheBreakpoint(t *testing.T) {
	t.Run("system string is preferred", func(t *testing.T) {
		request := &dto.ClaudeRequest{
			System:   "stable system",
			Messages: []dto.ClaudeMessage{{Role: "user", Content: "changing question"}},
		}

		assert.True(t, injectStableCacheBreakpoint(request))
		system := request.ParseSystem()
		require.Len(t, system, 1)
		assert.JSONEq(t, `{"type":"ephemeral","ttl":"5m"}`, string(system[0].CacheControl))
	})

	t.Run("tool is used when system is absent", func(t *testing.T) {
		request := &dto.ClaudeRequest{
			Tools:    []dto.Tool{{Name: "lookup", InputSchema: map[string]any{"type": "object"}}},
			Messages: []dto.ClaudeMessage{{Role: "user", Content: "changing question"}},
		}

		assert.True(t, injectStableCacheBreakpoint(request))
		encoded, err := json.Marshal(request.Tools)
		require.NoError(t, err)
		assert.Contains(t, string(encoded), `"cache_control":{"ttl":"5m","type":"ephemeral"}`)
	})

	t.Run("stable history is used before final message", func(t *testing.T) {
		request := &dto.ClaudeRequest{Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "stable history"},
			{Role: "assistant", Content: "stable answer"},
			{Role: "user", Content: "changing question"},
		}}

		assert.True(t, injectStableCacheBreakpoint(request))
		content, err := request.Messages[1].ParseContent()
		require.NoError(t, err)
		require.Len(t, content, 1)
		assert.NotEmpty(t, content[0].CacheControl)
		assert.Equal(t, "changing question", request.Messages[2].Content)
	})

	t.Run("explicit breakpoint disables injection", func(t *testing.T) {
		request := &dto.ClaudeRequest{
			System: []dto.ClaudeMediaMessage{{
				Type:         "text",
				Text:         stringPointer("stable"),
				CacheControl: json.RawMessage(`{"type":"ephemeral","ttl":"1h"}`),
			}},
			Messages: []dto.ClaudeMessage{{Role: "user", Content: "question"}},
		}

		assert.False(t, injectStableCacheBreakpoint(request))
		assert.JSONEq(t, `{"type":"ephemeral","ttl":"1h"}`, string(request.ParseSystem()[0].CacheControl))
	})

	t.Run("single user request is skipped", func(t *testing.T) {
		request := &dto.ClaudeRequest{Messages: []dto.ClaudeMessage{{Role: "user", Content: "question"}}}
		assert.False(t, injectStableCacheBreakpoint(request))
	})
}

func stringPointer(value string) *string {
	return &value
}
