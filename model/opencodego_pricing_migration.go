package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
)

const openCodeGoOfficialPricingMigrationKey = "Migration.OpenCodeGoOfficialPricingV1"

var legacyOpenCodeGoPricingKeys = []string{
	"ModelPrice",
	"ModelRatio",
	"CompletionRatio",
	"CacheRatio",
	"CreateCacheRatio",
}

func legacyOpenCodeGoDefaultPricing(key string) map[string]float64 {
	switch key {
	case "ModelPrice":
		return ratio_setting.GetDefaultModelPriceMap()
	case "ModelRatio":
		return ratio_setting.GetDefaultModelRatioMap()
	case "CompletionRatio":
		return ratio_setting.GetDefaultCompletionRatioMap()
	case "CacheRatio":
		return ratio_setting.GetDefaultCacheRatioMap()
	case "CreateCacheRatio":
		return ratio_setting.GetDefaultCreateCacheRatioMap()
	default:
		return nil
	}
}

func migrateOpenCodeGoOfficialPricing(db *gorm.DB, enabled bool) (map[string]string, error) {
	protected := make(map[string]string)
	activated := false
	err := db.Transaction(func(tx *gorm.DB) error {
		var marker Option
		err := optionByKeyForUpdate(tx, openCodeGoOfficialPricingMigrationKey).First(&marker).Error
		if err == nil && marker.Value == billing_setting.OpenCodeGoOfficialBillingRevision {
			activated = true
			return nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var channelCount int64
		if err := tx.Model(&Channel{}).Where("type = ?", constant.ChannelTypeOpenCodeGo).Count(&channelCount).Error; err != nil {
			return err
		}
		if !enabled && channelCount > 0 {
			return nil
		}

		var options []Option
		keys := append([]string{}, legacyOpenCodeGoPricingKeys...)
		keys = append(keys, "billing_setting.billing_mode", "billing_setting.billing_expr")
		if err := optionByKeys(tx, keys).Find(&options).Error; err != nil {
			return err
		}
		values := make(map[string]string, len(options))
		for _, option := range options {
			values[option.Key] = option.Value
		}

		modes := make(map[string]string)
		if raw := values["billing_setting.billing_mode"]; raw != "" {
			if err := common.UnmarshalJsonStr(raw, &modes); err != nil {
				return fmt.Errorf("invalid billing_setting.billing_mode: %w", err)
			}
		}
		expressions := make(map[string]string)
		if raw := values["billing_setting.billing_expr"]; raw != "" {
			if err := common.UnmarshalJsonStr(raw, &expressions); err != nil {
				return fmt.Errorf("invalid billing_setting.billing_expr: %w", err)
			}
		}
		for model, mode := range modes {
			if mode != billing_setting.BillingModeTieredExpr {
				continue
			}
			expression, exists := expressions[model]
			if !exists || strings.TrimSpace(expression) == "" {
				return fmt.Errorf("missing tiered billing expression for %s", model)
			}
			if err := billing_setting.SmokeTestExpr(expression); err != nil {
				return fmt.Errorf("invalid tiered billing expression for %s: %w", model, err)
			}
		}

		official := billing_setting.GetOpenCodeGoOfficialBillingExprCopy()
		for _, key := range legacyOpenCodeGoPricingKeys {
			raw := values[key]
			if raw == "" {
				continue
			}
			var configured map[string]float64
			if err := common.UnmarshalJsonStr(raw, &configured); err != nil {
				return fmt.Errorf("invalid %s: %w", key, err)
			}
			defaults := legacyOpenCodeGoDefaultPricing(key)
			for model, configuredValue := range configured {
				if _, officialModel := official[model]; !officialModel {
					continue
				}
				if _, explicit := modes[model]; explicit {
					continue
				}
				if defaultValue, builtIn := defaults[model]; builtIn && configuredValue == defaultValue {
					continue
				}
				modes[model] = billing_setting.BillingModeRatio
				protected[model] = key
			}
		}

		modeJSON, err := common.Marshal(modes)
		if err != nil {
			return err
		}
		if err := tx.Save(&Option{Key: "billing_setting.billing_mode", Value: string(modeJSON)}).Error; err != nil {
			return err
		}
		if err := tx.Save(&Option{
			Key:   openCodeGoOfficialPricingMigrationKey,
			Value: billing_setting.OpenCodeGoOfficialBillingRevision,
		}).Error; err != nil {
			return err
		}
		activated = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	billing_setting.SetOpenCodeGoOfficialDefaultsEnabled(activated)
	return protected, nil
}

func migrateOpenCodeGoOfficialPricingOnStartup() error {
	enabled := common.GetEnvOrDefaultBool("MIGRATE_OPENCODEGO_OFFICIAL_PRICING_V1", false)
	protected, err := migrateOpenCodeGoOfficialPricing(DB, enabled)
	if err != nil {
		return err
	}
	if billing_setting.OpenCodeGoOfficialDefaultsEnabled() {
		common.SysLog(fmt.Sprintf("OpenCodeGo official pricing revision %s enabled; preserved %d legacy model overrides", billing_setting.OpenCodeGoOfficialBillingRevision, len(protected)))
	} else {
		common.SysLog("OpenCodeGo official pricing is pending; set MIGRATE_OPENCODEGO_OFFICIAL_PRICING_V1=true on one primary instance after reviewing legacy pricing")
	}
	return nil
}

func loadOpenCodeGoOfficialPricingState(db *gorm.DB) error {
	if !db.Migrator().HasTable(&Option{}) {
		billing_setting.SetOpenCodeGoOfficialDefaultsEnabled(false)
		return nil
	}
	var marker Option
	err := optionByKey(db, openCodeGoOfficialPricingMigrationKey).Where("value = ?", billing_setting.OpenCodeGoOfficialBillingRevision).
		First(&marker).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		billing_setting.SetOpenCodeGoOfficialDefaultsEnabled(false)
		return nil
	}
	if err != nil {
		return err
	}
	billing_setting.SetOpenCodeGoOfficialDefaultsEnabled(true)
	return nil
}
