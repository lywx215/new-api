package common

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
)

func TestBridgeReleaseMapsLegacyAndCurrentOpenCodeGoTypes(t *testing.T) {
	for _, channelType := range []int{
		constant.ChannelTypeLegacyOpenCodeGo,
		constant.ChannelTypeOpenCodeGo,
	} {
		apiType, ok := ChannelType2APIType(channelType)
		assert.True(t, ok)
		assert.Equal(t, constant.APITypeOpenCodeGo, apiType)
		assert.Equal(t, "https://opencode.ai/zen/go", constant.ChannelBaseURLs[channelType])
	}
	assert.Greater(t, len(constant.ChannelBaseURLs), constant.ChannelTypeOpenCodeGo)
}
