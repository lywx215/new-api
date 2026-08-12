package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
)

const openCodeGoChannelTypeMigrationKey = "Migration.OpenCodeGoChannelType99"
const openCodeGoChannelTypeMigrationComplete = "complete"
const legacyOpenCodeGoChannelType = 59

// migrateOpenCodeGoChannelType moves the fork's persisted OpenCodeGo channel
// type away from upstream's newly assigned Sub2API type. The marker and row
// updates commit atomically so a later startup can never reinterpret a partial
// migration.
func migrateOpenCodeGoChannelType(db *gorm.DB, enabled bool, requireResolution bool) (int64, error) {
	var migrated int64
	err := db.Transaction(func(tx *gorm.DB) error {
		var marker Option
		err := lockForUpdate(tx).Where("key = ?", openCodeGoChannelTypeMigrationKey).First(&marker).Error
		if err == nil && marker.Value == openCodeGoChannelTypeMigrationComplete {
			return nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var legacyCount int64
		if err := tx.Model(&Channel{}).
			Where("type = ?", legacyOpenCodeGoChannelType).
			Count(&legacyCount).Error; err != nil {
			return err
		}

		if legacyCount > 0 && !enabled {
			if requireResolution {
				return fmt.Errorf("found %d unmarked channels with type %d; this database may contain legacy OpenCodeGo or upstream Sub2API data; set MIGRATE_OPENCODEGO_59_TO_99=true only after confirming every row is legacy OpenCodeGo", legacyCount, legacyOpenCodeGoChannelType)
			}
			return nil
		}

		if legacyCount > 0 {
			result := tx.Model(&Channel{}).
				Where("type = ?", legacyOpenCodeGoChannelType).
				Update("type", constant.ChannelTypeOpenCodeGo)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != legacyCount {
				return fmt.Errorf("OpenCodeGo channel migration updated %d of %d rows", result.RowsAffected, legacyCount)
			}
			migrated = result.RowsAffected
		}

		marker = Option{Key: openCodeGoChannelTypeMigrationKey, Value: openCodeGoChannelTypeMigrationComplete}
		return tx.Save(&marker).Error
	})
	return migrated, err
}

func migrateOpenCodeGoChannelTypeOnStartup() error {
	enabled := common.GetEnvOrDefaultBool("MIGRATE_OPENCODEGO_59_TO_99", false)
	migrated, err := migrateOpenCodeGoChannelType(DB, enabled, true)
	if err != nil {
		return err
	}
	if migrated > 0 {
		common.SysLog(fmt.Sprintf("migrated %d OpenCodeGo channels from type %d to %d", migrated, legacyOpenCodeGoChannelType, constant.ChannelTypeOpenCodeGo))
	}
	return nil
}

func verifyOpenCodeGoChannelTypeMigration() error {
	// Test fixtures and a not-yet-initialized replica may intentionally connect
	// before the primary has created the schema. Once options exists, every
	// replica must observe the completed marker before serving traffic.
	if !DB.Migrator().HasTable(&Option{}) {
		return nil
	}
	var marker Option
	if err := DB.Where("key = ? AND value = ?", openCodeGoChannelTypeMigrationKey, openCodeGoChannelTypeMigrationComplete).
		First(&marker).Error; err != nil {
		return fmt.Errorf("OpenCodeGo channel type migration is not complete on this database; start the migration-enabled primary instance first: %w", err)
	}
	return nil
}
