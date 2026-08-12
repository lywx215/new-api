package model

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
)

const openCodeGoChannelTypeMigrationKey = "Migration.OpenCodeGoChannelType99"
const openCodeGoChannelTypeMigrationComplete = "complete"

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
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}

		var legacyCount int64
		if err := tx.Model(&Channel{}).
			Where("type = ?", constant.ChannelTypeLegacyOpenCodeGo).
			Count(&legacyCount).Error; err != nil {
			return err
		}

		if legacyCount > 0 && !enabled {
			if requireResolution {
				return fmt.Errorf("found %d legacy OpenCodeGo channels with type %d; set MIGRATE_OPENCODEGO_59_TO_99=true on exactly one drained instance before starting the final release", legacyCount, constant.ChannelTypeLegacyOpenCodeGo)
			}
			return nil
		}

		if legacyCount > 0 {
			result := tx.Model(&Channel{}).
				Where("type = ?", constant.ChannelTypeLegacyOpenCodeGo).
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

func migrateOpenCodeGoChannelTypeBridge() error {
	enabled := common.GetEnvOrDefaultBool("MIGRATE_OPENCODEGO_59_TO_99", false)
	migrated, err := migrateOpenCodeGoChannelType(DB, enabled, false)
	if err != nil {
		return err
	}
	if migrated > 0 {
		common.SysLog(fmt.Sprintf("migrated %d OpenCodeGo channels from type %d to %d", migrated, constant.ChannelTypeLegacyOpenCodeGo, constant.ChannelTypeOpenCodeGo))
	}
	return nil
}
