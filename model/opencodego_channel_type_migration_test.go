package model

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newOpenCodeGoMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Option{}))
	return db
}

func TestOpenCodeGoChannelTypeMigrationRequiresExplicitEnablement(t *testing.T) {
	db := newOpenCodeGoMigrationDB(t)
	channel := Channel{Name: "legacy", Key: "secret", Type: constant.ChannelTypeLegacyOpenCodeGo, Models: "gpt-test", OtherSettings: `{"model_protocols":{"gpt-*":"openai"}}`}
	require.NoError(t, db.Create(&channel).Error)

	migrated, err := migrateOpenCodeGoChannelType(db, false, false)
	require.NoError(t, err)
	assert.Zero(t, migrated)

	var stored Channel
	require.NoError(t, db.First(&stored, channel.Id).Error)
	assert.Equal(t, constant.ChannelTypeLegacyOpenCodeGo, stored.Type)
	assert.Equal(t, channel.Key, stored.Key)
	assert.Equal(t, channel.Models, stored.Models)
	assert.Equal(t, channel.OtherSettings, stored.OtherSettings)
	assert.ErrorIs(t, db.Where("key = ?", openCodeGoChannelTypeMigrationKey).First(&Option{}).Error, gorm.ErrRecordNotFound)
}

func TestOpenCodeGoChannelTypeMigrationIsAtomicAndIdempotent(t *testing.T) {
	db := newOpenCodeGoMigrationDB(t)
	channels := []Channel{
		{Name: "legacy-1", Key: "one", Type: constant.ChannelTypeLegacyOpenCodeGo, Models: "model-a"},
		{Name: "legacy-2", Key: "two", Type: constant.ChannelTypeLegacyOpenCodeGo, Models: "model-b"},
	}
	require.NoError(t, db.Create(&channels).Error)

	migrated, err := migrateOpenCodeGoChannelType(db, true, false)
	require.NoError(t, err)
	assert.EqualValues(t, 2, migrated)

	var legacyCount int64
	var currentCount int64
	require.NoError(t, db.Model(&Channel{}).Where("type = ?", constant.ChannelTypeLegacyOpenCodeGo).Count(&legacyCount).Error)
	require.NoError(t, db.Model(&Channel{}).Where("type = ?", constant.ChannelTypeOpenCodeGo).Count(&currentCount).Error)
	assert.Zero(t, legacyCount)
	assert.EqualValues(t, 2, currentCount)

	var marker Option
	require.NoError(t, db.Where("key = ?", openCodeGoChannelTypeMigrationKey).First(&marker).Error)
	assert.Equal(t, openCodeGoChannelTypeMigrationComplete, marker.Value)

	newLegacy := Channel{Name: "sub2api-after-marker", Key: "three", Type: constant.ChannelTypeLegacyOpenCodeGo}
	require.NoError(t, db.Create(&newLegacy).Error)
	migrated, err = migrateOpenCodeGoChannelType(db, true, true)
	require.NoError(t, err)
	assert.Zero(t, migrated)
	require.NoError(t, db.First(&newLegacy, newLegacy.Id).Error)
	assert.Equal(t, constant.ChannelTypeLegacyOpenCodeGo, newLegacy.Type)
}

func TestOpenCodeGoChannelTypeMigrationFinalReleaseGuard(t *testing.T) {
	db := newOpenCodeGoMigrationDB(t)
	require.NoError(t, db.Create(&Channel{Name: "legacy", Key: "secret", Type: constant.ChannelTypeLegacyOpenCodeGo}).Error)

	_, err := migrateOpenCodeGoChannelType(db, false, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MIGRATE_OPENCODEGO_59_TO_99=true")
}
