package service

import (
	"net/http/httptest"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestGenerateTextOtherInfoFreezesFinalPricingChannelAndSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	now := time.Now()
	info := &relaycommon.RelayInfo{
		StartTime:         now,
		FirstResponseTime: now,
		ChannelMeta:       &relaycommon.ChannelMeta{},
		PriceData: hosttypes.PriceData{
			BillingSource:      "official",
			BillingChannelType: 99,
		},
	}

	other := GenerateTextOtherInfo(ctx, info, 0, 1, 0, 0, 0, 0, -1)

	assert.Equal(t, "official", other["pricing_source"])
	assert.Equal(t, 99, other["pricing_channel_type"])
}
