package common

import (
	"net/http/httptest"
	"strings"
	"testing"

	rootcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func generateTestAffinityHeader(t *testing.T, tokenID int, request dto.Request, session string) string {
	t.Helper()
	rootcommon.ReloadInternalAffinitySecret()
	recorder := httptest.NewRecorder()
	httpRequest := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	if session != "" {
		httpRequest.Header.Set("x-opencode-session", session)
	}
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httpRequest
	info := &RelayInfo{
		TokenId:         tokenID,
		OriginModelName: "gpt-test",
		RelayFormat:     types.RelayFormatOpenAI,
		Request:         request,
	}
	ApplyInternalAffinityHeader(ctx, info)
	value, ok := info.RuntimeHeadersOverride["x-newapi-affinity-key"].(string)
	require.True(t, ok)
	return value
}

func TestApplyInternalAffinityHeaderNamespacesPromptCacheKeyByToken(t *testing.T) {
	t.Setenv("AFFINITY_SECRET", "test-affinity-secret")
	setting := operation_setting.GetChannelAffinitySetting()
	original := *setting
	t.Cleanup(func() { *setting = original })
	setting.GenerateInternalKey = true
	setting.UsePromptCacheKey = true
	setting.MaxSourceBytes = 32768

	request := &dto.GeneralOpenAIRequest{PromptCacheKey: "conversation-1"}
	first := generateTestAffinityHeader(t, 10, request, "")
	second := generateTestAffinityHeader(t, 10, request, "")
	otherCustomer := generateTestAffinityHeader(t, 11, request, "")

	assert.Equal(t, first, second)
	assert.NotEqual(t, first, otherCustomer)
}

func TestApplyInternalAffinityHeaderPromptCacheKeyDoesNotUseStableBody(t *testing.T) {
	setting := operation_setting.GetChannelAffinitySetting()
	original := *setting
	t.Cleanup(func() { *setting = original })
	setting.GenerateInternalKey = true
	setting.UsePromptCacheKey = true
	setting.MaxSourceBytes = 32

	first := &dto.GeneralOpenAIRequest{PromptCacheKey: "same-cache-key", Messages: []dto.Message{{Role: "user", Content: strings.Repeat("a", 1<<20)}}}
	second := &dto.GeneralOpenAIRequest{PromptCacheKey: "same-cache-key", Messages: []dto.Message{{Role: "user", Content: strings.Repeat("b", 1<<20)}}}
	assert.Equal(t, generateTestAffinityHeader(t, 10, first, ""), generateTestAffinityHeader(t, 10, second, ""))
}

func TestApplyInternalAffinityHeaderPreservesExistingChannelHeaderOverrides(t *testing.T) {
	t.Setenv("AFFINITY_SECRET", "test-affinity-secret")
	setting := operation_setting.GetChannelAffinitySetting()
	original := *setting
	t.Cleanup(func() { *setting = original })
	setting.GenerateInternalKey = true
	setting.UsePromptCacheKey = true

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	info := &RelayInfo{
		TokenId:         10,
		OriginModelName: "gpt-test",
		RelayFormat:     types.RelayFormatOpenAIResponses,
		Request:         &dto.OpenAIResponsesRequest{PromptCacheKey: []byte(`"conversation-1"`)},
		ChannelMeta: &ChannelMeta{HeadersOverride: map[string]any{
			"Originator": "Codex CLI",
			"Session_id": "session-1",
		}},
	}

	ApplyInternalAffinityHeader(ctx, info)

	require.True(t, info.UseRuntimeHeadersOverride)
	assert.Equal(t, "Codex CLI", info.RuntimeHeadersOverride["originator"])
	assert.Equal(t, "session-1", info.RuntimeHeadersOverride["session_id"])
	assert.NotEmpty(t, info.RuntimeHeadersOverride["x-newapi-affinity-key"])
}

func TestApplyInternalAffinityHeaderCombinesSessionWithStableFingerprint(t *testing.T) {
	t.Setenv("AFFINITY_SECRET", "test-affinity-secret")
	setting := operation_setting.GetChannelAffinitySetting()
	original := *setting
	t.Cleanup(func() { *setting = original })
	setting.GenerateInternalKey = true
	setting.UsePromptCacheKey = false
	setting.UseOpenCodeSession = true
	setting.UseMetadataUserID = false
	setting.MaxSourceBytes = 32768

	firstRequest := &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: "first conversation"}}}
	secondRequest := &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: "second conversation"}}}
	first := generateTestAffinityHeader(t, 10, firstRequest, "shared-user-session")
	second := generateTestAffinityHeader(t, 10, secondRequest, "shared-user-session")

	assert.NotEqual(t, first, second)
}

func TestApplyInternalAffinityHeaderDoesNotChangeWhenCryptoSecretRotates(t *testing.T) {
	t.Setenv("AFFINITY_SECRET", "stable-dedicated-affinity-secret")
	rootcommon.ReloadInternalAffinitySecret()
	originalCrypto := rootcommon.CryptoSecret
	t.Cleanup(func() { rootcommon.CryptoSecret = originalCrypto })
	setting := operation_setting.GetChannelAffinitySetting()
	original := *setting
	t.Cleanup(func() { *setting = original })
	setting.GenerateInternalKey = true
	setting.UsePromptCacheKey = false
	setting.UseOpenCodeSession = true
	setting.GenerateFallbackKey = false
	setting.MaxSourceBytes = 32768
	request := &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: "same conversation"}}}

	first := generateTestAffinityHeader(t, 10, request, "same-session")
	rootcommon.CryptoSecret = "rotated-crypto-secret"
	second := generateTestAffinityHeader(t, 10, request, "same-session")
	assert.Equal(t, first, second)
}

func TestApplyInternalAffinityHeaderMetadataUserIDDefaultsOff(t *testing.T) {
	t.Setenv("AFFINITY_SECRET", "test-affinity-secret")
	setting := operation_setting.GetChannelAffinitySetting()
	original := *setting
	t.Cleanup(func() { *setting = original })
	setting.GenerateInternalKey = true
	setting.UsePromptCacheKey = false
	setting.UseOpenCodeSession = false
	setting.UseMetadataUserID = false
	setting.GenerateFallbackKey = false

	request := &dto.ClaudeRequest{Metadata: []byte(`{"user_id":"user-1"}`), Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello"}}}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	info := &RelayInfo{TokenId: 1, OriginModelName: "claude-test", RelayFormat: types.RelayFormatClaude, Request: request}

	ApplyInternalAffinityHeader(ctx, info)

	assert.Empty(t, info.RuntimeHeadersOverride)
	_, valid := rootcommon.VerifyInternalAffinityHeader("not-signed")
	assert.False(t, valid)
}

func TestApplyInternalAffinityHeaderMetadataUserIDStillUsesFingerprint(t *testing.T) {
	t.Setenv("AFFINITY_SECRET", "test-affinity-secret")
	setting := operation_setting.GetChannelAffinitySetting()
	original := *setting
	t.Cleanup(func() { *setting = original })
	setting.GenerateInternalKey = true
	setting.UsePromptCacheKey = false
	setting.UseOpenCodeSession = false
	setting.UseMetadataUserID = true
	setting.GenerateFallbackKey = false
	setting.MaxSourceBytes = 32768

	first := &dto.ClaudeRequest{Metadata: []byte(`{"user_id":"shared-user"}`), Messages: []dto.ClaudeMessage{{Role: "user", Content: "conversation one"}}}
	second := &dto.ClaudeRequest{Metadata: []byte(`{"user_id":"shared-user"}`), Messages: []dto.ClaudeMessage{{Role: "user", Content: "conversation two"}}}

	assert.NotEqual(t, generateTestAffinityHeader(t, 10, first, ""), generateTestAffinityHeader(t, 10, second, ""))
}

func TestApplyInternalAffinityHeaderSessionTakesPriorityOverMetadataUserID(t *testing.T) {
	t.Setenv("AFFINITY_SECRET", "test-affinity-secret")
	setting := operation_setting.GetChannelAffinitySetting()
	original := *setting
	t.Cleanup(func() { *setting = original })
	setting.GenerateInternalKey = true
	setting.UsePromptCacheKey = false
	setting.UseOpenCodeSession = true
	setting.UseMetadataUserID = true
	setting.GenerateFallbackKey = false
	setting.MaxSourceBytes = 32768

	firstMetadata := &dto.GeneralOpenAIRequest{
		Metadata: []byte(`{"user_id":"user-one"}`),
		Messages: []dto.Message{{Role: "user", Content: "same conversation"}},
	}
	secondMetadata := &dto.GeneralOpenAIRequest{
		Metadata: []byte(`{"user_id":"user-two"}`),
		Messages: []dto.Message{{Role: "user", Content: "same conversation"}},
	}

	first := generateTestAffinityHeader(t, 10, firstMetadata, "session-one")
	sameSession := generateTestAffinityHeader(t, 10, secondMetadata, "session-one")
	differentSession := generateTestAffinityHeader(t, 10, firstMetadata, "session-two")

	assert.Equal(t, first, sameSession, "metadata.user_id must not affect the key when a session header is available")
	assert.NotEqual(t, first, differentSession)
}

func TestApplyInternalAffinityHeaderFallbackSupportsClaudeAndResponses(t *testing.T) {
	t.Setenv("AFFINITY_SECRET", "test-affinity-secret")
	setting := operation_setting.GetChannelAffinitySetting()
	original := *setting
	t.Cleanup(func() { *setting = original })
	setting.GenerateInternalKey = true
	setting.UsePromptCacheKey = false
	setting.UseOpenCodeSession = false
	setting.UseMetadataUserID = false
	setting.GenerateFallbackKey = true
	setting.MaxSourceBytes = 32768

	claudeRequest := &dto.ClaudeRequest{System: "system", Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello"}}}
	responsesRequest := &dto.OpenAIResponsesRequest{Instructions: []byte(`"system"`), Input: []byte(`[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]`)}

	assert.NotEmpty(t, generateTestAffinityHeader(t, 10, claudeRequest, ""))
	assert.NotEmpty(t, generateTestAffinityHeader(t, 10, responsesRequest, ""))
}

func TestApplyInternalAffinityHeaderIgnoresBinaryMedia(t *testing.T) {
	t.Setenv("AFFINITY_SECRET", "test-affinity-secret")
	setting := operation_setting.GetChannelAffinitySetting()
	original := *setting
	t.Cleanup(func() { *setting = original })
	setting.GenerateInternalKey = true
	setting.UsePromptCacheKey = false
	setting.UseOpenCodeSession = true
	setting.MaxSourceBytes = 32768

	first := &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: []any{
		map[string]any{"type": "text", "text": "same text"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}},
	}}}}
	second := &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: []any{
		map[string]any{"type": "text", "text": "same text"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,BBBB"}},
	}}}}

	assert.Equal(t, generateTestAffinityHeader(t, 10, first, "session"), generateTestAffinityHeader(t, 10, second, "session"))
}

func TestApplyInternalAffinityHeaderResponsesSkipsToolOutputBeforeFirstUserInput(t *testing.T) {
	t.Setenv("AFFINITY_SECRET", "test-affinity-secret")
	setting := operation_setting.GetChannelAffinitySetting()
	original := *setting
	t.Cleanup(func() { *setting = original })
	setting.GenerateInternalKey = true
	setting.UsePromptCacheKey = false
	setting.UseOpenCodeSession = true
	setting.UseMetadataUserID = false
	setting.GenerateFallbackKey = false
	setting.MaxSourceBytes = 32768

	first := &dto.OpenAIResponsesRequest{Input: []byte(`[
		{"type":"function_call_output","call_id":"call-1","output":"tool result one"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"same user input"}]}
	]`)}
	second := &dto.OpenAIResponsesRequest{Input: []byte(`[
		{"type":"function_call_output","call_id":"call-1","output":"tool result two"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"same user input"}]}
	]`)}

	assert.Equal(t, generateTestAffinityHeader(t, 10, first, "session"), generateTestAffinityHeader(t, 10, second, "session"))
}

func TestApplyInternalAffinityHeaderStopsAtConfiguredStableSourceLimit(t *testing.T) {
	t.Setenv("AFFINITY_SECRET", "test-affinity-secret")
	setting := operation_setting.GetChannelAffinitySetting()
	original := *setting
	t.Cleanup(func() { *setting = original })
	setting.GenerateInternalKey = true
	setting.UsePromptCacheKey = false
	setting.UseOpenCodeSession = true
	setting.UseMetadataUserID = false
	setting.GenerateFallbackKey = false
	setting.MaxSourceBytes = 64

	sharedPrefix := strings.Repeat("a", 128)
	first := &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: sharedPrefix + "first suffix"}}}
	second := &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: sharedPrefix + "second suffix"}}}

	assert.Equal(t, generateTestAffinityHeader(t, 10, first, "session"), generateTestAffinityHeader(t, 10, second, "session"))
}

func TestApplyInternalAffinityHeaderBoundsClaudeAndResponsesSources(t *testing.T) {
	setting := operation_setting.GetChannelAffinitySetting()
	original := *setting
	t.Cleanup(func() { *setting = original })
	setting.GenerateInternalKey = true
	setting.UsePromptCacheKey = false
	setting.UseOpenCodeSession = true
	setting.GenerateFallbackKey = false
	setting.MaxSourceBytes = 64
	sharedPrefix := strings.Repeat("stable", 40)

	claudeFirst := &dto.ClaudeRequest{Messages: []dto.ClaudeMessage{{Role: "user", Content: sharedPrefix + "one"}}}
	claudeSecond := &dto.ClaudeRequest{Messages: []dto.ClaudeMessage{{Role: "user", Content: sharedPrefix + "two"}}}
	assert.Equal(t, generateTestAffinityHeader(t, 10, claudeFirst, "session"), generateTestAffinityHeader(t, 10, claudeSecond, "session"))

	responsesFirst := &dto.OpenAIResponsesRequest{Input: []byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":"` + sharedPrefix + `one"}]}]`)}
	responsesSecond := &dto.OpenAIResponsesRequest{Input: []byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":"` + sharedPrefix + `two"}]}]`)}
	assert.Equal(t, generateTestAffinityHeader(t, 10, responsesFirst, "session"), generateTestAffinityHeader(t, 10, responsesSecond, "session"))
}
