package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newOpenCodeGoPricingMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Option{}))
	t.Cleanup(func() { billing_setting.SetOpenCodeGoOfficialDefaultsEnabled(false) })
	return db
}

func TestMigrateOpenCodeGoOfficialPricingRequiresExplicitActivationForExistingChannel(t *testing.T) {
	db := newOpenCodeGoPricingMigrationDB(t)
	require.NoError(t, db.Create(&Channel{Name: "test", Type: constant.ChannelTypeOpenCodeGo}).Error)

	protected, err := migrateOpenCodeGoOfficialPricing(db, false)
	require.NoError(t, err)
	assert.Empty(t, protected)
	assert.False(t, billing_setting.OpenCodeGoOfficialDefaultsEnabled())

	var markerCount int64
	require.NoError(t, db.Model(&Option{}).Where("key = ?", openCodeGoOfficialPricingMigrationKey).Count(&markerCount).Error)
	assert.Zero(t, markerCount)
}

func TestMigrateOpenCodeGoOfficialPricingPreservesLegacyOverridesAndIsIdempotent(t *testing.T) {
	db := newOpenCodeGoPricingMigrationDB(t)
	require.NoError(t, db.Create(&Channel{Name: "test", Type: constant.ChannelTypeOpenCodeGo}).Error)
	require.NoError(t, db.Create(&Option{Key: "ModelRatio", Value: `{"glm-5.2":9,"unrelated":2}`}).Error)
	require.NoError(t, db.Create(&Option{Key: "CreateCacheRatio", Value: `{"gpt-5.6-luna":1.25}`}).Error)

	protected, err := migrateOpenCodeGoOfficialPricing(db, true)
	require.NoError(t, err)
	assert.Equal(t, "ModelRatio", protected["glm-5.2"])
	assert.NotContains(t, protected, "gpt-5.6-luna")
	assert.True(t, billing_setting.OpenCodeGoOfficialDefaultsEnabled())
	assert.Equal(t, billing_setting.BillingModeTieredExpr, billing_setting.GetBillingMode("hy3"))

	var mode Option
	require.NoError(t, db.First(&mode, "key = ?", "billing_setting.billing_mode").Error)
	assert.JSONEq(t, `{"glm-5.2":"ratio"}`, mode.Value)

	protected, err = migrateOpenCodeGoOfficialPricing(db, true)
	require.NoError(t, err)
	assert.Empty(t, protected)
	assert.True(t, billing_setting.OpenCodeGoOfficialDefaultsEnabled())
}

func TestMigrateOpenCodeGoOfficialPricingRollsBackMalformedLegacyJSON(t *testing.T) {
	db := newOpenCodeGoPricingMigrationDB(t)
	require.NoError(t, db.Create(&Channel{Name: "test", Type: constant.ChannelTypeOpenCodeGo}).Error)
	require.NoError(t, db.Create(&Option{Key: "ModelRatio", Value: `{broken`}).Error)

	_, err := migrateOpenCodeGoOfficialPricing(db, true)
	require.Error(t, err)

	var markerCount int64
	require.NoError(t, db.Model(&Option{}).Where("key = ?", openCodeGoOfficialPricingMigrationKey).Count(&markerCount).Error)
	assert.Zero(t, markerCount)
}

func TestMigrateOpenCodeGoOfficialPricingRejectsInvalidTieredOverrides(t *testing.T) {
	tests := []struct {
		name        string
		expressions string
	}{
		{name: "missing expression", expressions: `{}`},
		{name: "invalid expression", expressions: `{"glm-5.2":"v1:unknown(p)"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := newOpenCodeGoPricingMigrationDB(t)
			require.NoError(t, db.Create(&Channel{Name: "test", Type: constant.ChannelTypeOpenCodeGo}).Error)
			require.NoError(t, db.Create(&Option{Key: "billing_setting.billing_mode", Value: `{"glm-5.2":"tiered_expr"}`}).Error)
			require.NoError(t, db.Create(&Option{Key: "billing_setting.billing_expr", Value: test.expressions}).Error)

			_, err := migrateOpenCodeGoOfficialPricing(db, true)
			require.Error(t, err)

			var markerCount int64
			require.NoError(t, db.Model(&Option{}).Where("key = ?", openCodeGoOfficialPricingMigrationKey).Count(&markerCount).Error)
			assert.Zero(t, markerCount)
		})
	}
}
