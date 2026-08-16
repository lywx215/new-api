package kitutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMaskSensitiveInfoMasksStandaloneOpenCodeWorkspaceID(t *testing.T) {
	input := `{"workspace":"wrk_TEST_WORKSPACE_123","url":"https://opencode.ai/workspace/wrk_URLSECRET/go"}`

	masked := MaskSensitiveInfo(input)

	assert.NotContains(t, masked, "wrk_TEST_WORKSPACE_123")
	assert.NotContains(t, masked, "wrk_URLSECRET")
	assert.Contains(t, masked, `"workspace":"wrk_***"`)
}
