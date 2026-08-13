package model

import "gorm.io/gorm"

// optionByKey builds a dialect-safe options query. The key column is a
// reserved word in MySQL and must use the database-specific quoted name.
func optionByKey(tx *gorm.DB, key string) *gorm.DB {
	return tx.Where(commonKeyCol+" = ?", key)
}

func optionByKeys(tx *gorm.DB, keys []string) *gorm.DB {
	return tx.Where(commonKeyCol+" IN ?", keys)
}

func optionByKeyForUpdate(tx *gorm.DB, key string) *gorm.DB {
	return lockForUpdate(optionByKey(tx, key))
}
