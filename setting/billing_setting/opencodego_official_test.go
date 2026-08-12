package billing_setting

import (
	"testing"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeGoOfficialBillingExpressions(t *testing.T) {
	require.Len(t, openCodeGoOfficialBillingExpr, 19)
	for model, expression := range openCodeGoOfficialBillingExpr {
		t.Run(model, func(t *testing.T) {
			require.NoError(t, SmokeTestExpr(expression))
		})
	}
}

func TestOpenCodeGoOfficialBillingExactMillionTokenVectors(t *testing.T) {
	tests := map[string]float64{
		"grok-4.5":          8.3,
		"gpt-5.6-luna":      3.24,
		"glm-5.2":           6.06,
		"glm-5.1":           6.06,
		"kimi-k3":           18.3,
		"kimi-k2.7-code":    5.14,
		"kimi-k2.6":         5.11,
		"mimo-v2.5":         0.4228,
		"mimo-v2.5-pro":     1.308625,
		"minimax-m3":        1.56,
		"minimax-m2.7":      2.31,
		"minimax-m2.5":      2.31,
		"qwen3.8-max":       13.25,
		"qwen3.7-max":       16.75,
		"qwen3.7-plus":      9.12,
		"qwen3.6-plus":      13.2,
		"deepseek-v4-pro":   1.308625,
		"deepseek-v4-flash": 0.4228,
		"hy3":               0.755,
	}
	for model, dollars := range tests {
		t.Run(model, func(t *testing.T) {
			amount, _, err := billingexpr.RunExpr(openCodeGoOfficialBillingExpr[model], billingexpr.TokenParams{
				P: 1_000_000, C: 1_000_000, CR: 1_000_000, CC: 1_000_000, CC1h: 1_000_000, Len: 1_000_000,
			})
			require.NoError(t, err)
			assert.InDelta(t, dollars*1_000_000, amount, 1e-6)
		})
	}
}

func TestOpenCodeGoOfficialBillingTierBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		inputLen   float64
		wantTier   string
		wantAmount float64
	}{
		{name: "luna standard boundary", model: "gpt-5.6-luna", inputLen: 272000, wantTier: "standard", wantAmount: 54400},
		{name: "luna long context", model: "gpt-5.6-luna", inputLen: 272001, wantTier: "long_context", wantAmount: 108800.4},
		{name: "qwen 3.7 plus standard boundary", model: "qwen3.7-plus", inputLen: 256000, wantTier: "standard", wantAmount: 102400},
		{name: "qwen 3.6 plus long context", model: "qwen3.6-plus", inputLen: 256001, wantTier: "long_context", wantAmount: 512002},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, detail, err := billingexpr.RunExpr(openCodeGoOfficialBillingExpr[test.model], billingexpr.TokenParams{
				P: test.inputLen, Len: test.inputLen,
			})
			require.NoError(t, err)
			assert.InDelta(t, test.wantAmount, result, 1e-8)
			assert.Equal(t, test.wantTier, detail.MatchedTier)
		})
	}
}

func TestOpenCodeGoOperatorModeOverridesOfficialDefault(t *testing.T) {
	oldModes := billingSetting.BillingMode
	oldExpr := billingSetting.BillingExpr
	t.Cleanup(func() {
		billingSetting.BillingMode = oldModes
		billingSetting.BillingExpr = oldExpr
		SetOpenCodeGoOfficialDefaultsEnabled(false)
	})
	billingSetting.BillingMode = map[string]string{"hy3": BillingModeRatio}
	billingSetting.BillingExpr = map[string]string{}
	SetOpenCodeGoOfficialDefaultsEnabled(true)

	assert.Equal(t, BillingModeRatio, GetBillingMode("hy3"))
	_, ok := GetBillingExpr("hy3")
	assert.False(t, ok)
	assert.Equal(t, "operator", GetEffectiveBillingSource("hy3"))
}
