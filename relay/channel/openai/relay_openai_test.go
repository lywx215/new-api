package openai

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCaptureStreamUsageSurvivesTrailingEvents(t *testing.T) {
	events := []string{
		`{"choices":[{"delta":{"content":"hello"}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":5490,"completion_tokens":56,"total_tokens":5546,"prompt_cache_hit_tokens":5376,"prompt_cache_miss_tokens":114,"prompt_tokens_details":{"cached_tokens":5376},"completion_tokens_details":{"reasoning_tokens":51}}}`,
		`{"choices":[],"x-opencode-type":"inference-cost","cost":"0.00004669","normalizedUsage":{"inputTokens":114,"outputTokens":56,"reasoningTokens":51,"cacheReadTokens":5376,"cacheWrite5mTokens":0,"cacheWrite1hTokens":0}}`,
		`{}`,
	}

	var captured *dto.Usage
	var cost *inferenceCostEvent
	for _, event := range events {
		var nextCost *inferenceCostEvent
		captured, nextCost = captureStreamUsage(event, captured)
		if nextCost != nil {
			cost = nextCost
		}
	}

	require.NotNil(t, captured)
	assert.Equal(t, 5490, captured.PromptTokens)
	assert.Equal(t, 56, captured.CompletionTokens)
	assert.Equal(t, 5376, captured.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 114, captured.PromptCacheMissTokens)
	require.NotNil(t, cost)
}

func TestCaptureStreamUsageMergesSnapshotsWithoutAdding(t *testing.T) {
	events := []string{
		`{"usage":{"prompt_tokens":4800,"prompt_tokens_details":{"cached_tokens":4700}}}`,
		`{"usage":{"prompt_tokens":4818,"completion_tokens":24,"total_tokens":4842,"prompt_tokens_details":{"cached_tokens":0}}}`,
		`{"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
	}

	var captured *dto.Usage
	for _, event := range events {
		captured, _ = captureStreamUsage(event, captured)
	}

	require.NotNil(t, captured)
	assert.Equal(t, 4818, captured.PromptTokens)
	assert.Equal(t, 24, captured.CompletionTokens)
	assert.Equal(t, 4842, captured.TotalTokens)
	assert.Equal(t, 4700, captured.PromptTokensDetails.CachedTokens)
}

func TestInferenceCostIsFallbackUsage(t *testing.T) {
	_, cost := captureStreamUsage(
		`{"x-opencode-type":"inference-cost","cost":"0.25","normalizedUsage":{"inputTokens":114,"outputTokens":56,"reasoningTokens":51,"cacheReadTokens":5376,"cacheWrite5mTokens":10,"cacheWrite1hTokens":20}}`,
		nil,
	)
	require.NotNil(t, cost)

	usage := usageFromInferenceCost(cost)
	require.NotNil(t, usage)
	assert.Equal(t, 5520, usage.PromptTokens)
	assert.Equal(t, 56, usage.CompletionTokens)
	assert.Equal(t, 5376, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 30, usage.PromptTokensDetails.CachedCreationTokens)
	assert.Equal(t, 51, usage.CompletionTokenDetails.ReasoningTokens)
}

func TestStreamDataForClientRemovesUnrequestedUsage(t *testing.T) {
	data := `{"id":"chunk-1","choices":[{"index":0,"delta":{},"finish_reason":"stop","usage":{"cached_tokens":12}}],"usage":{"prompt_tokens":20,"completion_tokens":5}}`

	filtered := streamDataForClient(data, false)
	assert.JSONEq(t, `{"id":"chunk-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, filtered)
	assert.Equal(t, data, streamDataForClient(data, true))
}

func TestCaptureStreamUsageNormalizesCompatibilityCacheFields(t *testing.T) {
	captured, _ := captureStreamUsage(
		`{"usage":{"prompt_tokens":100,"completion_tokens":10,"cached_tokens":70}}`,
		nil,
	)

	require.NotNil(t, captured)
	assert.Equal(t, 70, captured.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 70, captured.PromptCacheHitTokens)
}

func TestCaptureStreamUsageEventOrder(t *testing.T) {
	standard := `{"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"prompt_tokens_details":{"cached_tokens":70}}}`
	cost := `{"x-opencode-type":"inference-cost","normalizedUsage":{"inputTokens":30,"outputTokens":10,"cacheReadTokens":70}}`
	tests := []struct {
		name         string
		events       []string
		wantPrompt   int
		wantCached   int
		wantCost     bool
		wantCaptured bool
	}{
		{name: "usage is last", events: []string{`{"choices":[{"delta":{"content":"x"}}]}`, standard}, wantPrompt: 100, wantCached: 70, wantCaptured: true},
		{name: "usage before cost", events: []string{standard, cost}, wantPrompt: 100, wantCached: 70, wantCost: true, wantCaptured: true},
		{name: "usage before ping", events: []string{standard, `{"type":"ping"}`}, wantPrompt: 100, wantCached: 70, wantCaptured: true},
		{name: "usage before empty object", events: []string{standard, `{}`}, wantPrompt: 100, wantCached: 70, wantCaptured: true},
		{name: "zero snapshot does not overwrite", events: []string{standard, `{"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`}, wantPrompt: 100, wantCached: 70, wantCaptured: true},
		{name: "cost only", events: []string{cost}, wantCost: true},
		{name: "no usage", events: []string{`{"choices":[{"delta":{"content":"x"}}]}`, `{}`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured *dto.Usage
			var capturedCost *inferenceCostEvent
			for _, event := range tt.events {
				var nextCost *inferenceCostEvent
				captured, nextCost = captureStreamUsage(event, captured)
				if nextCost != nil {
					capturedCost = nextCost
				}
			}

			if tt.wantCaptured {
				require.NotNil(t, captured)
				assert.Equal(t, tt.wantPrompt, captured.PromptTokens)
				assert.Equal(t, tt.wantCached, captured.PromptTokensDetails.CachedTokens)
			} else {
				assert.Nil(t, captured)
			}
			if tt.wantCost {
				assert.NotNil(t, capturedCost)
			} else {
				assert.Nil(t, capturedCost)
			}
		})
	}
}
