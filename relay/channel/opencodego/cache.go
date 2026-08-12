package opencodego

import (
	"encoding/json"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

var automaticCacheControl = json.RawMessage(`{"type":"ephemeral","ttl":"5m"}`)

func injectStableCacheBreakpoint(request *dto.ClaudeRequest) bool {
	if request == nil || requestHasExplicitCacheControl(request) {
		return false
	}

	if request.IsStringSystem() {
		text := request.GetStringSystem()
		if strings.TrimSpace(text) != "" {
			request.System = []dto.ClaudeMediaMessage{{
				Type:         dto.ContentTypeText,
				Text:         &text,
				CacheControl: append(json.RawMessage(nil), automaticCacheControl...),
			}}
			return true
		}
	} else {
		system := request.ParseSystem()
		for i := len(system) - 1; i >= 0; i-- {
			if !cacheableClaudeContent(system[i]) {
				continue
			}
			system[i].CacheControl = append(json.RawMessage(nil), automaticCacheControl...)
			request.System = system
			return true
		}
	}

	if request.Tools != nil {
		var tools []map[string]any
		if data, err := common.Marshal(request.Tools); err == nil {
			if err := common.Unmarshal(data, &tools); err == nil {
				for i := len(tools) - 1; i >= 0; i-- {
					name, _ := tools[i]["name"].(string)
					if strings.TrimSpace(name) == "" {
						continue
					}
					tools[i]["cache_control"] = map[string]any{"type": "ephemeral", "ttl": "5m"}
					request.Tools = tools
					return true
				}
			}
		}
	}

	for i := len(request.Messages) - 2; i >= 0; i-- {
		message := &request.Messages[i]
		if message.IsStringContent() {
			text := message.GetStringContent()
			if strings.TrimSpace(text) == "" {
				continue
			}
			message.Content = []dto.ClaudeMediaMessage{{
				Type:         dto.ContentTypeText,
				Text:         &text,
				CacheControl: append(json.RawMessage(nil), automaticCacheControl...),
			}}
			return true
		}
		content, err := message.ParseContent()
		if err != nil {
			continue
		}
		for j := len(content) - 1; j >= 0; j-- {
			if !cacheableClaudeContent(content[j]) {
				continue
			}
			content[j].CacheControl = append(json.RawMessage(nil), automaticCacheControl...)
			message.Content = content
			return true
		}
	}
	return false
}

func requestHasExplicitCacheControl(request *dto.ClaudeRequest) bool {
	data, err := common.Marshal(request)
	if err != nil {
		return true
	}
	var value any
	if err := common.Unmarshal(data, &value); err != nil {
		return true
	}
	var scan func(any) bool
	scan = func(current any) bool {
		switch typed := current.(type) {
		case map[string]any:
			if cacheControl, exists := typed["cache_control"]; exists && cacheControl != nil {
				return true
			}
			for _, child := range typed {
				if scan(child) {
					return true
				}
			}
		case []any:
			for _, child := range typed {
				if scan(child) {
					return true
				}
			}
		}
		return false
	}
	return scan(value)
}

func cacheableClaudeContent(content dto.ClaudeMediaMessage) bool {
	switch content.Type {
	case dto.ContentTypeText:
		return strings.TrimSpace(content.GetText()) != ""
	case "image", "document":
		return content.Source != nil
	default:
		return false
	}
}
