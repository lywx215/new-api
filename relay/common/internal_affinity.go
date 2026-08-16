package common

import (
	"bytes"
	"fmt"
	"hash"
	"strings"

	rootcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type affinitySourceHasher struct {
	h         hash.Hash
	remaining int
	wrote     bool
	content   bool
}

func newAffinitySourceHasher(limit int) *affinitySourceHasher {
	return &affinitySourceHasher{h: rootcommon.NewInternalAffinityHasher(), remaining: limit}
}

func (c *affinitySourceHasher) full() bool { return c.remaining <= 0 }

func (c *affinitySourceHasher) writeBytes(value []byte) {
	if c.full() || len(value) == 0 {
		return
	}
	if len(value) > c.remaining {
		value = value[:c.remaining]
	}
	written := len(value)
	_, _ = c.h.Write(value)
	c.remaining -= written
	c.wrote = true
}

func (c *affinitySourceHasher) writeString(value string) {
	if c.full() || value == "" {
		return
	}
	if len(value) > c.remaining {
		value = value[:c.remaining]
	}
	written := len(value)
	if len(value) <= 4096 {
		_, _ = c.h.Write([]byte(value))
	} else {
		var buffer [4096]byte
		for len(value) > 0 {
			chunkSize := min(len(value), len(buffer))
			copy(buffer[:chunkSize], value[:chunkSize])
			_, _ = c.h.Write(buffer[:chunkSize])
			value = value[chunkSize:]
		}
	}
	c.remaining -= written
	c.wrote = true
}

func (c *affinitySourceHasher) add(label, value string) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "data:") || c.full() {
		return
	}
	c.writeString(label + "=")
	c.writeString(value)
	c.writeString("\n")
	if label != "algorithm" {
		c.content = true
	}
}

func (c *affinitySourceHasher) addRaw(label string, value []byte) {
	value = bytes.TrimSpace(value)
	if len(value) == 0 || c.full() {
		return
	}
	c.writeString(label + "=")
	c.writeBytes(value)
	c.writeString("\n")
	c.content = true
}

func (c *affinitySourceHasher) sum() string {
	if !c.wrote || !c.content {
		return ""
	}
	return fmt.Sprintf("%x", c.h.Sum(nil))
}

func addOpenAIMessageText(collector *affinitySourceHasher, label string, content any) {
	switch value := content.(type) {
	case string:
		collector.add(label, value)
	case []dto.MediaContent:
		for i := range value {
			if collector.full() {
				return
			}
			if value[i].Type == dto.ContentTypeText {
				collector.add(label, value[i].Text)
			}
		}
	case []any:
		for _, item := range value {
			if collector.full() {
				return
			}
			if block, ok := item.(map[string]any); ok && block["type"] == dto.ContentTypeText {
				if text, ok := block["text"].(string); ok {
					collector.add(label, text)
				}
			}
		}
	}
}

func addClaudeContent(collector *affinitySourceHasher, label string, content any) {
	switch value := content.(type) {
	case string:
		collector.add(label, value)
	case []dto.ClaudeMediaMessage:
		for i := range value {
			if collector.full() {
				return
			}
			if value[i].Type == "text" {
				collector.add(label, value[i].GetText())
			}
		}
	case []any:
		for _, item := range value {
			if collector.full() {
				return
			}
			if block, ok := item.(map[string]any); ok && block["type"] == "text" {
				if text, ok := block["text"].(string); ok {
					collector.add(label, text)
				}
			}
		}
	}
}

func addClaudeToolIdentities(collector *affinitySourceHasher, tools any) {
	addMap := func(tool map[string]any) {
		if value, ok := tool["type"].(string); ok {
			collector.add("tool_type", value)
		}
		if value, ok := tool["name"].(string); ok {
			collector.add("tool_name", value)
		}
		if value, ok := tool["description"].(string); ok {
			collector.add("tool_description", value)
		}
		if schema, ok := tool["input_schema"].(map[string]any); ok {
			if value, ok := schema["type"].(string); ok {
				collector.add("tool_schema_type", value)
			}
		}
	}
	switch value := tools.(type) {
	case []dto.Tool:
		for i := range value {
			collector.add("tool_name", value[i].Name)
			collector.add("tool_description", value[i].Description)
			if schemaType, ok := value[i].InputSchema["type"].(string); ok {
				collector.add("tool_schema_type", schemaType)
			}
			if collector.full() {
				return
			}
		}
	case []any:
		for _, item := range value {
			if tool, ok := item.(map[string]any); ok {
				addMap(tool)
			}
			if collector.full() {
				return
			}
		}
	}
}

func addResponsesText(collector *affinitySourceHasher, label string, value gjson.Result) {
	if collector.full() || !value.Exists() {
		return
	}
	if value.Type == gjson.String {
		collector.addRaw(label, []byte(value.Raw))
		return
	}
	value.ForEach(func(_, item gjson.Result) bool {
		if item.Type == gjson.String {
			collector.addRaw(label, []byte(item.Raw))
		} else {
			itemType := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
			if itemType == "" || itemType == "text" || itemType == "input_text" || itemType == "output_text" {
				if text := item.Get("text"); text.Type == gjson.String {
					collector.addRaw(label, []byte(text.Raw))
				}
			}
		}
		return !collector.full()
	})
}

func addResponsesToolIdentities(collector *affinitySourceHasher, raw []byte) {
	gjson.ParseBytes(raw).ForEach(func(_, tool gjson.Result) bool {
		collector.addRaw("tool_type", []byte(tool.Get("type").Raw))
		collector.addRaw("tool_name", []byte(tool.Get("name").Raw))
		collector.addRaw("tool_description", []byte(tool.Get("description").Raw))
		return !collector.full()
	})
}

func addResponsesFirstUser(collector *affinitySourceHasher, raw []byte) {
	result := gjson.ParseBytes(raw)
	if result.Type == gjson.String {
		collector.addRaw("first_user", []byte(result.Raw))
		return
	}
	result.ForEach(func(_, value gjson.Result) bool {
		itemType := strings.ToLower(strings.TrimSpace(value.Get("type").String()))
		if (itemType == "" || itemType == "message") && strings.EqualFold(value.Get("role").String(), "user") {
			before := collector.wrote
			addResponsesText(collector, "first_user", value.Get("content"))
			if collector.wrote == before {
				collector.addRaw("first_user", []byte(value.Get("text").Raw))
			}
			return false
		}
		return !collector.full()
	})
}

func stableRequestFingerprint(info *RelayInfo, limit int) string {
	if info == nil || info.Request == nil || limit <= 0 {
		return ""
	}
	collector := newAffinitySourceHasher(limit)
	collector.add("algorithm", "fp2")
	switch request := info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		for i := range request.Messages {
			role := strings.ToLower(request.Messages[i].Role)
			if role == "system" || role == "developer" {
				addOpenAIMessageText(collector, "system", request.Messages[i].Content)
			}
			if collector.full() {
				break
			}
		}
		for i := range request.Tools {
			collector.add("tool_type", request.Tools[i].Type)
			collector.add("tool_name", request.Tools[i].Function.Name)
			collector.add("tool_description", request.Tools[i].Function.Description)
			if request.Tools[i].Function.Parameters != nil {
				collector.add("tool_schema", fmt.Sprintf("%T", request.Tools[i].Function.Parameters))
			}
			if collector.full() {
				break
			}
		}
		for i := range request.Messages {
			if strings.EqualFold(request.Messages[i].Role, "user") {
				addOpenAIMessageText(collector, "first_user", request.Messages[i].Content)
				break
			}
		}
	case *dto.ClaudeRequest:
		addClaudeContent(collector, "system", request.System)
		addClaudeToolIdentities(collector, request.Tools)
		for i := range request.Messages {
			if strings.EqualFold(request.Messages[i].Role, "user") {
				addClaudeContent(collector, "first_user", request.Messages[i].Content)
				break
			}
		}
	case *dto.OpenAIResponsesRequest:
		addResponsesText(collector, "instructions", gjson.ParseBytes(request.Instructions))
		addResponsesToolIdentities(collector, request.Tools)
		addResponsesFirstUser(collector, request.Input)
	}
	return collector.sum()
}

func rawJSONString(raw []byte) string {
	result := gjson.ParseBytes(raw)
	if result.Type == gjson.String {
		return strings.TrimSpace(result.String())
	}
	return ""
}

// ApplyInternalAffinityHeader generates a trusted header only for New API channels.
func ApplyInternalAffinityHeader(c *gin.Context, info *RelayInfo) {
	setting := operation_setting.GetChannelAffinitySetting()
	if setting == nil || !setting.GenerateInternalKey || info == nil || info.Request == nil {
		return
	}
	var promptCacheKey string
	switch request := info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		if setting.UsePromptCacheKey {
			promptCacheKey = strings.TrimSpace(request.PromptCacheKey)
		}
	case *dto.OpenAIResponsesRequest:
		if setting.UsePromptCacheKey {
			promptCacheKey = rawJSONString(request.PromptCacheKey)
		}
	}
	var sourceType, sourceValue string
	if promptCacheKey != "" {
		sourceType, sourceValue = "prompt_cache_key", promptCacheKey
	}
	if sourceValue == "" && setting.UseOpenCodeSession && c != nil && c.Request != nil {
		if value := strings.TrimSpace(c.Request.Header.Get("x-opencode-session")); value != "" {
			sourceType, sourceValue = "x-opencode-session", value
		}
	}
	if sourceValue == "" && setting.UseMetadataUserID {
		var metadataUserID string
		switch request := info.Request.(type) {
		case *dto.GeneralOpenAIRequest:
			metadataUserID = gjson.GetBytes(request.Metadata, "user_id").String()
		case *dto.OpenAIResponsesRequest:
			metadataUserID = gjson.GetBytes(request.Metadata, "user_id").String()
		case *dto.ClaudeRequest:
			metadataUserID = gjson.GetBytes(request.Metadata, "user_id").String()
		}
		if metadataUserID != "" {
			sourceType, sourceValue = "metadata.user_id", metadataUserID
		}
	}
	var fingerprint string
	if sourceType != "prompt_cache_key" {
		limit := setting.MaxSourceBytes
		if limit <= 0 {
			limit = 32768
		}
		fingerprint = stableRequestFingerprint(info, limit)
		if sourceValue == "" && setting.GenerateFallbackKey {
			sourceType, sourceValue = "fallback", fingerprint
		}
	}
	if sourceValue == "" || (sourceType != "prompt_cache_key" && fingerprint == "") {
		if setting.GenerateFallbackKey {
			rootcommon.ObserveInternalAffinityFallbackNotGenerated()
		}
		return
	}
	if sourceType != "prompt_cache_key" {
		sourceValue += "|" + fingerprint
	}
	source := fmt.Sprintf("%d|%s|%s|%s|%s", info.TokenId, info.OriginModelName, info.RelayFormat, sourceType, sourceValue)
	headerValue := rootcommon.SignInternalAffinitySource(source)
	overrides := GetEffectiveHeaderOverride(info)
	overrides[strings.ToLower(rootcommon.InternalAffinityHeader)] = headerValue
	info.RuntimeHeadersOverride = overrides
	info.UseRuntimeHeadersOverride = true
	rootcommon.ObserveInternalAffinityGenerated(sourceType)
}
