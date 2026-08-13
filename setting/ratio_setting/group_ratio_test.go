package ratio_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckGroupGroupRatioRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "zero is free", value: `{"vip":{"default":0}}`},
		{name: "positive", value: `{"vip":{"default":0.8}}`},
		{name: "placeholder target allowed", value: `{"vip":{"future-group":1.2}}`},
		{name: "negative", value: `{"vip":{"default":-0.1}}`, wantErr: true},
		{name: "null nested map", value: `{"vip":null}`, wantErr: true},
		{name: "null value", value: `{"vip":{"default":null}}`, wantErr: true},
		{name: "string value", value: `{"vip":{"default":"0.8"}}`, wantErr: true},
		{name: "non finite exponent", value: `{"vip":{"default":1e999}}`, wantErr: true},
		{name: "invalid token", value: `{"vip":{"default":NaN}}`, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := CheckGroupGroupRatio(test.value)
			if test.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestUpdateGroupGroupRatioDoesNotMutateOnValidationFailure(t *testing.T) {
	original := GroupGroupRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, UpdateGroupGroupRatioByJSONString(original))
	})

	require.Error(t, UpdateGroupGroupRatioByJSONString(`{"vip":{"default":-1}}`))
	assert.JSONEq(t, original, GroupGroupRatio2JSONString())
}
