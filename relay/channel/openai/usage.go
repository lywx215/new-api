package openai

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

type inferenceCostEvent struct {
	Type            string `json:"x-opencode-type"`
	Cost            any    `json:"cost"`
	NormalizedUsage struct {
		InputTokens        int `json:"inputTokens"`
		OutputTokens       int `json:"outputTokens"`
		ReasoningTokens    int `json:"reasoningTokens"`
		CacheReadTokens    int `json:"cacheReadTokens"`
		CacheWrite5mTokens int `json:"cacheWrite5mTokens"`
		CacheWrite1hTokens int `json:"cacheWrite1hTokens"`
	} `json:"normalizedUsage"`
}

type streamUsagePayload struct {
	dto.Usage
	CachedTokens *int `json:"cached_tokens"`
}

func captureStreamUsage(data string, captured *dto.Usage) (*dto.Usage, *inferenceCostEvent) {
	if data == "" {
		return captured, nil
	}
	if !strings.Contains(data, `"usage"`) && !strings.Contains(data, `"x-opencode-type"`) {
		return captured, nil
	}

	var payload struct {
		Usage   *streamUsagePayload `json:"usage"`
		Choices []struct {
			Usage *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"usage"`
		} `json:"choices"`
		inferenceCostEvent
	}
	if err := common.UnmarshalJsonStr(data, &payload); err != nil {
		return captured, nil
	}

	if payload.Usage != nil {
		if payload.Usage.CachedTokens != nil && payload.Usage.PromptTokensDetails.CachedTokens == 0 {
			payload.Usage.PromptTokensDetails.CachedTokens = *payload.Usage.CachedTokens
		}
		normalizeUsageCacheFields(&payload.Usage.Usage)
		captured = mergeUsageSnapshot(captured, &payload.Usage.Usage)
	}
	for _, choice := range payload.Choices {
		if choice.Usage != nil && choice.Usage.CachedTokens > 0 {
			captured = mergeUsageSnapshot(captured, &dto.Usage{
				PromptTokensDetails: dto.InputTokenDetails{CachedTokens: choice.Usage.CachedTokens},
			})
		}
	}

	if payload.Type != "inference-cost" {
		return captured, nil
	}
	normalized := payload.NormalizedUsage
	if normalized.InputTokens == 0 && normalized.OutputTokens == 0 &&
		normalized.CacheReadTokens == 0 && normalized.CacheWrite5mTokens == 0 &&
		normalized.CacheWrite1hTokens == 0 {
		return captured, nil
	}
	return captured, &payload.inferenceCostEvent
}

func streamDataForClient(data string, includeUsage bool) string {
	if data == "" || includeUsage {
		return data
	}
	if !strings.Contains(data, `"usage"`) {
		return data
	}

	var payload map[string]any
	if err := common.UnmarshalJsonStr(data, &payload); err != nil {
		return data
	}
	delete(payload, "usage")
	if choices, ok := payload["choices"].([]any); ok {
		for _, rawChoice := range choices {
			if choice, ok := rawChoice.(map[string]any); ok {
				delete(choice, "usage")
			}
		}
	}
	if len(payload) == 0 {
		return ""
	}
	encoded, err := common.Marshal(payload)
	if err != nil {
		return data
	}
	return string(encoded)
}

func normalizeUsageCacheFields(usage *dto.Usage) {
	if usage == nil {
		return
	}
	if usage.PromptTokensDetails.CachedTokens == 0 {
		switch {
		case usage.InputTokensDetails != nil && usage.InputTokensDetails.CachedTokens > 0:
			usage.PromptTokensDetails.CachedTokens = usage.InputTokensDetails.CachedTokens
		case usage.PromptCacheHitTokens > 0:
			usage.PromptTokensDetails.CachedTokens = usage.PromptCacheHitTokens
		}
	}
	if usage.PromptCacheHitTokens == 0 && usage.PromptTokensDetails.CachedTokens > 0 {
		usage.PromptCacheHitTokens = usage.PromptTokensDetails.CachedTokens
	}
}

func mergeUsageSnapshot(current, next *dto.Usage) *dto.Usage {
	if !validUsageSnapshot(next) {
		return current
	}
	normalizeUsageCacheFields(next)
	if current == nil {
		copy := *next
		if copy.TotalTokens == 0 {
			copy.TotalTokens = copy.PromptTokens + copy.CompletionTokens
		}
		return &copy
	}

	tokenCountsChanged := false
	if next.PromptTokens > 0 {
		current.PromptTokens = next.PromptTokens
		tokenCountsChanged = true
	}
	if next.CompletionTokens > 0 {
		current.CompletionTokens = next.CompletionTokens
		tokenCountsChanged = true
	}
	if next.TotalTokens > 0 {
		current.TotalTokens = next.TotalTokens
	} else if tokenCountsChanged {
		current.TotalTokens = current.PromptTokens + current.CompletionTokens
	}
	if next.InputTokens > 0 {
		current.InputTokens = next.InputTokens
	}
	if next.OutputTokens > 0 {
		current.OutputTokens = next.OutputTokens
	}
	if next.PromptCacheHitTokens > current.PromptCacheHitTokens {
		current.PromptCacheHitTokens = next.PromptCacheHitTokens
	}
	if next.PromptCacheMissTokens > 0 {
		current.PromptCacheMissTokens = next.PromptCacheMissTokens
	}
	if next.PromptTokensDetails.CachedTokens > current.PromptTokensDetails.CachedTokens {
		current.PromptTokensDetails.CachedTokens = next.PromptTokensDetails.CachedTokens
	}
	if next.PromptTokensDetails.CachedCreationTokens > current.PromptTokensDetails.CachedCreationTokens {
		current.PromptTokensDetails.CachedCreationTokens = next.PromptTokensDetails.CachedCreationTokens
	}
	if next.CompletionTokenDetails.ReasoningTokens > 0 {
		current.CompletionTokenDetails.ReasoningTokens = next.CompletionTokenDetails.ReasoningTokens
	}
	if next.ClaudeCacheCreation5mTokens > current.ClaudeCacheCreation5mTokens {
		current.ClaudeCacheCreation5mTokens = next.ClaudeCacheCreation5mTokens
	}
	if next.ClaudeCacheCreation1hTokens > current.ClaudeCacheCreation1hTokens {
		current.ClaudeCacheCreation1hTokens = next.ClaudeCacheCreation1hTokens
	}
	if next.Cost != nil {
		current.Cost = next.Cost
	}
	if current.TotalTokens == 0 {
		current.TotalTokens = current.PromptTokens + current.CompletionTokens
	}
	return current
}

func validUsageSnapshot(usage *dto.Usage) bool {
	if usage == nil {
		return false
	}
	return usage.PromptTokens > 0 ||
		usage.CompletionTokens > 0 ||
		usage.TotalTokens > 0 ||
		usage.InputTokens > 0 ||
		usage.OutputTokens > 0 ||
		usage.PromptCacheHitTokens > 0 ||
		usage.PromptCacheMissTokens > 0 ||
		usage.PromptTokensDetails.CachedTokens > 0 ||
		usage.PromptTokensDetails.CachedCreationTokens > 0 ||
		usage.CompletionTokenDetails.ReasoningTokens > 0 ||
		usage.ClaudeCacheCreation5mTokens > 0 ||
		usage.ClaudeCacheCreation1hTokens > 0
}

func usageFromInferenceCost(event *inferenceCostEvent) *dto.Usage {
	if event == nil {
		return nil
	}
	normalized := event.NormalizedUsage
	promptTokens := normalized.InputTokens + normalized.CacheReadTokens +
		normalized.CacheWrite5mTokens + normalized.CacheWrite1hTokens
	usage := &dto.Usage{
		PromptTokens:          promptTokens,
		CompletionTokens:      normalized.OutputTokens,
		TotalTokens:           promptTokens + normalized.OutputTokens,
		PromptCacheHitTokens:  normalized.CacheReadTokens,
		PromptCacheMissTokens: normalized.InputTokens,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         normalized.CacheReadTokens,
			CachedCreationTokens: normalized.CacheWrite5mTokens + normalized.CacheWrite1hTokens,
		},
		CompletionTokenDetails: dto.OutputTokenDetails{
			ReasoningTokens: normalized.ReasoningTokens,
		},
		ClaudeCacheCreation5mTokens: normalized.CacheWrite5mTokens,
		ClaudeCacheCreation1hTokens: normalized.CacheWrite1hTokens,
		Cost:                        event.Cost,
		UsageSource:                 "inference-cost",
	}
	return usage
}

func applyUsagePostProcessing(info *relaycommon.RelayInfo, usage *dto.Usage, responseBody []byte) {
	if info == nil || usage == nil {
		return
	}

	switch info.ChannelType {
	case constant.ChannelTypeDeepSeek:
		if usage.PromptTokensDetails.CachedTokens == 0 && usage.PromptCacheHitTokens != 0 {
			usage.PromptTokensDetails.CachedTokens = usage.PromptCacheHitTokens
		}
	case constant.ChannelTypeZhipu_v4:
		// 智普的cached_tokens在标准位置: usage.prompt_tokens_details.cached_tokens
		if usage.PromptTokensDetails.CachedTokens == 0 {
			if usage.InputTokensDetails != nil && usage.InputTokensDetails.CachedTokens > 0 {
				usage.PromptTokensDetails.CachedTokens = usage.InputTokensDetails.CachedTokens
			} else if cachedTokens, ok := extractCachedTokensFromBody(responseBody); ok {
				usage.PromptTokensDetails.CachedTokens = cachedTokens
			} else if usage.PromptCacheHitTokens > 0 {
				usage.PromptTokensDetails.CachedTokens = usage.PromptCacheHitTokens
			}
		}
	case constant.ChannelTypeMoonshot:
		// Moonshot的cached_tokens在非标准位置: choices[].usage.cached_tokens
		if usage.PromptTokensDetails.CachedTokens == 0 {
			if usage.InputTokensDetails != nil && usage.InputTokensDetails.CachedTokens > 0 {
				usage.PromptTokensDetails.CachedTokens = usage.InputTokensDetails.CachedTokens
			} else if cachedTokens, ok := extractMoonshotCachedTokensFromBody(responseBody); ok {
				usage.PromptTokensDetails.CachedTokens = cachedTokens
			} else if cachedTokens, ok := extractCachedTokensFromBody(responseBody); ok {
				usage.PromptTokensDetails.CachedTokens = cachedTokens
			} else if usage.PromptCacheHitTokens > 0 {
				usage.PromptTokensDetails.CachedTokens = usage.PromptCacheHitTokens
			}
		}
	case constant.ChannelTypeOpenAI:
		if usage.PromptTokensDetails.CachedTokens == 0 {
			if cachedTokens, ok := extractLlamaCachedTokensFromBody(responseBody); ok {
				usage.PromptTokensDetails.CachedTokens = cachedTokens
			}
		}
	case constant.ChannelTypeOpenCodeGo:
		if usage.PromptTokensDetails.CachedTokens == 0 {
			switch {
			case usage.InputTokensDetails != nil && usage.InputTokensDetails.CachedTokens > 0:
				usage.PromptTokensDetails.CachedTokens = usage.InputTokensDetails.CachedTokens
			case usage.PromptCacheHitTokens > 0:
				usage.PromptTokensDetails.CachedTokens = usage.PromptCacheHitTokens
			case len(responseBody) > 0:
				if cachedTokens, ok := extractCachedTokensFromBody(responseBody); ok {
					usage.PromptTokensDetails.CachedTokens = cachedTokens
				} else if cachedTokens, ok := extractMoonshotCachedTokensFromBody(responseBody); ok {
					usage.PromptTokensDetails.CachedTokens = cachedTokens
				}
			}
		}
		if usage.PromptCacheHitTokens == 0 {
			usage.PromptCacheHitTokens = usage.PromptTokensDetails.CachedTokens
		}
	}
}

func extractCachedTokensFromBody(body []byte) (int, bool) {
	if len(body) == 0 {
		return 0, false
	}

	var payload struct {
		Usage struct {
			PromptTokensDetails struct {
				CachedTokens *int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CachedTokens         *int `json:"cached_tokens"`
			PromptCacheHitTokens *int `json:"prompt_cache_hit_tokens"`
		} `json:"usage"`
	}

	if err := common.Unmarshal(body, &payload); err != nil {
		return 0, false
	}

	if payload.Usage.PromptTokensDetails.CachedTokens != nil {
		return *payload.Usage.PromptTokensDetails.CachedTokens, true
	}
	if payload.Usage.CachedTokens != nil {
		return *payload.Usage.CachedTokens, true
	}
	if payload.Usage.PromptCacheHitTokens != nil {
		return *payload.Usage.PromptCacheHitTokens, true
	}
	return 0, false
}

// extractMoonshotCachedTokensFromBody 从Moonshot的非标准位置提取cached_tokens
// Moonshot的流式响应格式: {"choices":[{"usage":{"cached_tokens":111}}]}
func extractMoonshotCachedTokensFromBody(body []byte) (int, bool) {
	if len(body) == 0 {
		return 0, false
	}

	var payload struct {
		Choices []struct {
			Usage struct {
				CachedTokens *int `json:"cached_tokens"`
			} `json:"usage"`
		} `json:"choices"`
	}

	if err := common.Unmarshal(body, &payload); err != nil {
		return 0, false
	}

	// 遍历choices查找cached_tokens
	for _, choice := range payload.Choices {
		if choice.Usage.CachedTokens != nil && *choice.Usage.CachedTokens > 0 {
			return *choice.Usage.CachedTokens, true
		}
	}

	return 0, false
}

// extractLlamaCachedTokensFromBody 从llama.cpp的非标准位置提取cache_n
func extractLlamaCachedTokensFromBody(body []byte) (int, bool) {
	if len(body) == 0 {
		return 0, false
	}

	var payload struct {
		Timings struct {
			CachedTokens *int `json:"cache_n"`
		} `json:"timings"`
	}

	if err := common.Unmarshal(body, &payload); err != nil {
		return 0, false
	}

	if payload.Timings.CachedTokens == nil {
		return 0, false
	}
	return *payload.Timings.CachedTokens, true
}
