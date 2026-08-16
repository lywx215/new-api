package common

import "sync/atomic"

type InternalAffinityMetrics struct {
	Scope                string           `json:"scope"`
	NodeName             string           `json:"node_name"`
	ResetOnRestart       bool             `json:"reset_on_restart"`
	Generated            int64            `json:"generated"`
	GeneratedBySource    map[string]int64 `json:"generated_by_source"`
	SignatureInvalid     int64            `json:"signature_invalid"`
	AffinityLookups      int64            `json:"affinity_lookups"`
	AffinityHits         int64            `json:"affinity_hits"`
	RPMMigrations        int64            `json:"rpm_migrations"`
	Upstream429          int64            `json:"upstream_429"`
	FallbackNotGenerated int64            `json:"fallback_not_generated"`
}

var internalAffinityGenerated atomic.Int64
var internalAffinityPromptCacheKey atomic.Int64
var internalAffinityOpenCodeSession atomic.Int64
var internalAffinityMetadataUserID atomic.Int64
var internalAffinityFallback atomic.Int64
var internalAffinitySignatureInvalid atomic.Int64
var internalAffinityLookups atomic.Int64
var internalAffinityHits atomic.Int64
var internalAffinityRPMMigrations atomic.Int64
var internalAffinityUpstream429 atomic.Int64
var internalAffinityFallbackNotGenerated atomic.Int64

func ObserveInternalAffinityGenerated(sourceType string) {
	internalAffinityGenerated.Add(1)
	switch sourceType {
	case "prompt_cache_key":
		internalAffinityPromptCacheKey.Add(1)
	case "x-opencode-session":
		internalAffinityOpenCodeSession.Add(1)
	case "metadata.user_id":
		internalAffinityMetadataUserID.Add(1)
	case "fallback":
		internalAffinityFallback.Add(1)
	}
}

func ObserveInternalAffinityFallbackNotGenerated() { internalAffinityFallbackNotGenerated.Add(1) }
func ObserveInternalAffinitySignatureInvalid()     { internalAffinitySignatureInvalid.Add(1) }
func ObserveInternalAffinityLookup(hit bool) {
	internalAffinityLookups.Add(1)
	if hit {
		internalAffinityHits.Add(1)
	}
}
func ObserveInternalAffinityRPMMigration() { internalAffinityRPMMigrations.Add(1) }
func ObserveInternalAffinityUpstream429()  { internalAffinityUpstream429.Add(1) }

func GetInternalAffinityMetrics() InternalAffinityMetrics {
	return InternalAffinityMetrics{
		Scope:          "node",
		NodeName:       NodeName,
		ResetOnRestart: true,
		Generated:      internalAffinityGenerated.Load(),
		GeneratedBySource: map[string]int64{
			"prompt_cache_key":   internalAffinityPromptCacheKey.Load(),
			"x-opencode-session": internalAffinityOpenCodeSession.Load(),
			"metadata.user_id":   internalAffinityMetadataUserID.Load(),
			"fallback":           internalAffinityFallback.Load(),
		},
		SignatureInvalid:     internalAffinitySignatureInvalid.Load(),
		AffinityLookups:      internalAffinityLookups.Load(),
		AffinityHits:         internalAffinityHits.Load(),
		RPMMigrations:        internalAffinityRPMMigrations.Load(),
		Upstream429:          internalAffinityUpstream429.Load(),
		FallbackNotGenerated: internalAffinityFallbackNotGenerated.Load(),
	}
}
