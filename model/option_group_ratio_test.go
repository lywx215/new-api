package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpecialGroupRatioValidationCoversSingleAndBulkOptionPaths(t *testing.T) {
	require.NoError(t, validateOptionValue("GroupGroupRatio", `{"vip":{"default":0}}`))
	assert.Error(t, validateOptionValue("GroupGroupRatio", `{"vip":{"default":-0.1}}`))
	assert.Error(t, validateOptionValue("GroupGroupRatio", `{"vip":{"default":null}}`))

	// Bulk updates run the same validator before opening a transaction, so an
	// unsafe nested multiplier is rejected even without a configured database.
	assert.Error(t, UpdateOptionsBulk(map[string]string{
		"GroupGroupRatio": `{"vip":{"default":-1}}`,
	}))
}
