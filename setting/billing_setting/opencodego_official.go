package billing_setting

import (
	"sync/atomic"

	"github.com/QuantumNous/new-api/constant"
	"github.com/samber/lo"
)

const OpenCodeGoOfficialBillingRevision = "opencodego-go-2026-08-12"

var openCodeGoOfficialDefaultsEnabled atomic.Bool

var openCodeGoOfficialBillingExpr = map[string]string{
	"grok-4.5":          `v1:tier("base",p*2+c*6+cr*0.3)`,
	"gpt-5.6-luna":      `v1:len<=272000?tier("standard",p*0.2+c*1.2+cr*0.02+cc*0.25+cc1h*0.25):tier("long_context",p*0.4+c*1.8+cr*0.04+cc*0.5+cc1h*0.5)`,
	"glm-5.2":           `v1:tier("base",p*1.4+c*4.4+cr*0.26)`,
	"glm-5.1":           `v1:tier("base",p*1.4+c*4.4+cr*0.26)`,
	"kimi-k3":           `v1:tier("base",p*3+c*15+cr*0.3)`,
	"kimi-k2.7-code":    `v1:tier("base",p*0.95+c*4+cr*0.19)`,
	"kimi-k2.6":         `v1:tier("base",p*0.95+c*4+cr*0.16)`,
	"mimo-v2.5":         `v1:tier("base",p*0.14+c*0.28+cr*0.0028)`,
	"mimo-v2.5-pro":     `v1:tier("base",p*0.435+c*0.87+cr*0.003625)`,
	"minimax-m3":        `v1:tier("base",p*0.3+c*1.2+cr*0.06)`,
	"minimax-m2.7":      `v1:tier("base",p*0.3+c*1.2+cr*0.06+cc*0.375+cc1h*0.375)`,
	"minimax-m2.5":      `v1:tier("base",p*0.3+c*1.2+cr*0.06+cc*0.375+cc1h*0.375)`,
	"qwen3.8-max":       `v1:tier("base",p*2+c*6+cr*0.25+cc*2.5+cc1h*2.5)`,
	"qwen3.7-max":       `v1:tier("base",p*2.5+c*7.5+cr*0.5+cc*3.125+cc1h*3.125)`,
	"qwen3.7-plus":      `v1:len<=256000?tier("standard",p*0.4+c*1.6+cr*0.04+cc*0.5+cc1h*0.5):tier("long_context",p*1.2+c*4.8+cr*0.12+cc*1.5+cc1h*1.5)`,
	"qwen3.6-plus":      `v1:len<=256000?tier("standard",p*0.5+c*3+cr*0.05+cc*0.625+cc1h*0.625):tier("long_context",p*2+c*6+cr*0.2+cc*2.5+cc1h*2.5)`,
	"deepseek-v4-pro":   `v1:tier("base",p*0.435+c*0.87+cr*0.003625)`,
	"deepseek-v4-flash": `v1:tier("base",p*0.14+c*0.28+cr*0.0028)`,
	"hy3":               `v1:tier("base",p*0.14+c*0.58+cr*0.035)`,
}

func SetOpenCodeGoOfficialDefaultsEnabled(enabled bool) {
	openCodeGoOfficialDefaultsEnabled.Store(enabled)
}

func OpenCodeGoOfficialDefaultsEnabled() bool {
	return openCodeGoOfficialDefaultsEnabled.Load()
}

func GetOpenCodeGoOfficialBillingExprCopy() map[string]string {
	return lo.Assign(openCodeGoOfficialBillingExpr)
}

func GetEffectiveBillingSource(model string) string {
	return GetEffectiveBillingSourceForChannel(model, constant.ChannelTypeOpenCodeGo)
}

func GetEffectiveBillingSourceForChannel(model string, channelType int) string {
	if _, ok := billingSetting.BillingMode[model]; ok {
		return "operator"
	}
	if channelType == constant.ChannelTypeOpenCodeGo && OpenCodeGoOfficialDefaultsEnabled() {
		if _, ok := openCodeGoOfficialBillingExpr[model]; ok {
			return "official"
		}
	}
	return "legacy"
}

func GetEffectiveBillingSourceCopy() map[string]string {
	sources := make(map[string]string, len(openCodeGoOfficialBillingExpr))
	for model := range openCodeGoOfficialBillingExpr {
		sources[model] = GetEffectiveBillingSource(model)
	}
	return sources
}
