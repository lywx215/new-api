package opencodego

import (
	"path"
	"sort"
	"strings"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

func PassThroughCompatible(info *relaycommon.RelayInfo, incoming types.RelayFormat) bool {
	protocol := protocolForInfo(info)
	switch protocol {
	case ProtocolAnthropic:
		return incoming == types.RelayFormatClaude
	case ProtocolResponses:
		return incoming == types.RelayFormatOpenAIResponses
	default:
		return incoming == types.RelayFormatOpenAI
	}
}

type Protocol string

const (
	ProtocolOpenAI    Protocol = "openai"
	ProtocolAnthropic Protocol = "anthropic"
	ProtocolResponses Protocol = "responses"
)

var modelProtocols = map[string]Protocol{
	"grok-4.5":          ProtocolOpenAI,
	"gpt-5.6-luna":      ProtocolResponses,
	"glm-5.2":           ProtocolOpenAI,
	"glm-5.1":           ProtocolOpenAI,
	"kimi-k3":           ProtocolOpenAI,
	"kimi-k2.7-code":    ProtocolOpenAI,
	"kimi-k2.6":         ProtocolOpenAI,
	"mimo-v2.5":         ProtocolOpenAI,
	"mimo-v2.5-pro":     ProtocolOpenAI,
	"deepseek-v4-pro":   ProtocolOpenAI,
	"deepseek-v4-flash": ProtocolOpenAI,
	"minimax-m3":        ProtocolAnthropic,
	"minimax-m2.7":      ProtocolAnthropic,
	"minimax-m2.5":      ProtocolAnthropic,
	"qwen3.8-max":       ProtocolAnthropic,
	"qwen3.7-max":       ProtocolAnthropic,
	"qwen3.7-plus":      ProtocolAnthropic,
	"qwen3.6-plus":      ProtocolAnthropic,
	"hy3":               ProtocolOpenAI,
}

func resolveProtocol(model string, overrides map[string]string) Protocol {
	model = strings.ToLower(strings.TrimSpace(model))
	if override, ok := overrides[model]; ok {
		if protocol, valid := parseProtocol(override); valid {
			return protocol
		}
	}

	patterns := make([]string, 0, len(overrides))
	for pattern := range overrides {
		if strings.ContainsAny(pattern, "*?[") {
			patterns = append(patterns, pattern)
		}
	}
	sort.Slice(patterns, func(i, j int) bool {
		if len(patterns[i]) == len(patterns[j]) {
			return patterns[i] < patterns[j]
		}
		return len(patterns[i]) > len(patterns[j])
	})
	for _, pattern := range patterns {
		matched, err := path.Match(strings.ToLower(pattern), model)
		if err == nil && matched {
			if protocol, valid := parseProtocol(overrides[pattern]); valid {
				return protocol
			}
		}
	}

	if protocol, ok := modelProtocols[model]; ok {
		return protocol
	}
	return ProtocolOpenAI
}

func parseProtocol(value string) (Protocol, bool) {
	switch Protocol(strings.ToLower(strings.TrimSpace(value))) {
	case ProtocolOpenAI:
		return ProtocolOpenAI, true
	case ProtocolAnthropic:
		return ProtocolAnthropic, true
	case ProtocolResponses:
		return ProtocolResponses, true
	default:
		return "", false
	}
}
