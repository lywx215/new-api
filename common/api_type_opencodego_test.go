package common

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
)

func TestFinalChannelTypeMappings(t *testing.T) {
	assert.Equal(t, 59, constant.ChannelTypeSub2API)
	assert.Equal(t, 60, constant.ChannelTypeNewAPI)
	assert.Equal(t, 99, constant.ChannelTypeOpenCodeGo)
	assert.Equal(t, 100, constant.ChannelTypeDummy)

	tests := []struct {
		channelType int
		apiType     int
	}{
		{constant.ChannelTypeSub2API, constant.APITypeSub2API},
		{constant.ChannelTypeNewAPI, constant.APITypeNewAPI},
		{constant.ChannelTypeOpenCodeGo, constant.APITypeOpenCodeGo},
	}
	for _, test := range tests {
		apiType, ok := ChannelType2APIType(test.channelType)
		assert.True(t, ok)
		assert.Equal(t, test.apiType, apiType)
	}
	assert.Equal(t, "https://opencode.ai/zen/go", constant.ChannelBaseURLs[constant.ChannelTypeOpenCodeGo])
	assert.Greater(t, len(constant.ChannelBaseURLs), constant.ChannelTypeOpenCodeGo)
}
