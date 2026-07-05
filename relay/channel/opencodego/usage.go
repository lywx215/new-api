package opencodego

import (
	"fmt"

	"github.com/QuantumNous/new-api/dto"
	"github.com/shopspring/decimal"
)

type UsageSource string

const (
	UsageSourceStandard      UsageSource = "standard"
	UsageSourceInferenceCost UsageSource = "inference-cost"
	UsageSourceEstimated     UsageSource = "estimated"
)

type OpenCodeGoUsage struct {
	UncachedInput int64
	CacheRead     int64
	CacheWrite5m  int64
	CacheWrite1h  int64
	Output        int64
	Reasoning     int64
	Cost          decimal.Decimal
	Source        UsageSource
}

func normalizeUsage(usage *dto.Usage, protocol Protocol) OpenCodeGoUsage {
	if usage == nil {
		return OpenCodeGoUsage{}
	}

	if usage.PromptTokensDetails.CachedTokens == 0 {
		switch {
		case usage.CachedTokens > 0:
			usage.PromptTokensDetails.CachedTokens = usage.CachedTokens
		case usage.InputTokensDetails != nil && usage.InputTokensDetails.CachedTokens > 0:
			usage.PromptTokensDetails.CachedTokens = usage.InputTokensDetails.CachedTokens
		case usage.PromptCacheHitTokens > 0:
			usage.PromptTokensDetails.CachedTokens = usage.PromptCacheHitTokens
		}
	}
	if usage.PromptCacheHitTokens == 0 {
		usage.PromptCacheHitTokens = usage.PromptTokensDetails.CachedTokens
	}
	usage.CachedTokens = 0

	source := UsageSourceStandard
	switch usage.UsageSource {
	case string(UsageSourceInferenceCost):
		source = UsageSourceInferenceCost
	case string(UsageSourceEstimated):
		source = UsageSourceEstimated
	}

	normalized := OpenCodeGoUsage{
		CacheRead:    int64(usage.PromptTokensDetails.CachedTokens),
		CacheWrite5m: int64(usage.ClaudeCacheCreation5mTokens),
		CacheWrite1h: int64(usage.ClaudeCacheCreation1hTokens),
		Output:       int64(usage.CompletionTokens),
		Reasoning:    int64(usage.CompletionTokenDetails.ReasoningTokens),
		Source:       source,
	}
	if normalized.CacheWrite5m == 0 && normalized.CacheWrite1h == 0 {
		normalized.CacheWrite5m = int64(usage.PromptTokensDetails.CachedCreationTokens)
	}

	if protocol == ProtocolAnthropic || usage.UsageSemantic == "anthropic" {
		normalized.UncachedInput = int64(usage.PromptTokens)
		usage.UsageSemantic = "anthropic"
	} else {
		uncached := usage.PromptTokens - usage.PromptTokensDetails.CachedTokens -
			usage.PromptTokensDetails.CachedCreationTokens
		if usage.PromptCacheMissTokens > 0 {
			uncached = usage.PromptCacheMissTokens
		}
		if uncached < 0 {
			uncached = 0
		}
		normalized.UncachedInput = int64(uncached)
		usage.UsageSemantic = "openai"
	}
	usage.InputTokens = int(normalized.UncachedInput + normalized.CacheRead +
		normalized.CacheWrite5m + normalized.CacheWrite1h)
	usage.OutputTokens = usage.CompletionTokens
	usage.UsageSource = "opencodego:" + string(source)
	if usage.Cost != nil {
		if cost, err := decimal.NewFromString(fmt.Sprint(usage.Cost)); err == nil {
			normalized.Cost = cost
		}
	}
	return normalized
}
