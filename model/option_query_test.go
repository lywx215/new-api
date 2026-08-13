package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/utils/tests"
)

func TestOptionQueriesQuoteReservedKeyForEveryDatabase(t *testing.T) {
	db, err := gorm.Open(tests.DummyDialector{}, &gorm.Config{DryRun: true})
	require.NoError(t, err)
	t.Cleanup(func() {
		common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
		initCol()
	})

	tests := []struct {
		name      string
		database  common.DatabaseType
		quotedKey string
		wantLock  bool
	}{
		{name: "mysql", database: common.DatabaseTypeMySQL, quotedKey: "`key`", wantLock: true},
		{name: "postgres", database: common.DatabaseTypePostgreSQL, quotedKey: `"key"`, wantLock: true},
		{name: "sqlite", database: common.DatabaseTypeSQLite, quotedKey: "`key`", wantLock: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			common.SetDatabaseTypes(tt.database, common.DatabaseTypeSQLite)
			initCol()

			var marker Option
			query := optionByKeyForUpdate(db, openCodeGoChannelTypeMigrationKey).First(&marker)
			sql := query.Statement.SQL.String()
			assert.Contains(t, sql, "WHERE "+tt.quotedKey+" = ?")
			if tt.wantLock {
				assert.Contains(t, sql, "FOR UPDATE")
			} else {
				assert.NotContains(t, sql, "FOR UPDATE")
			}

			query = optionByKeys(db, []string{"a", "b"}).Find(&[]Option{})
			assert.Contains(t, query.Statement.SQL.String(), "WHERE "+tt.quotedKey+" IN (?,?)")
		})
	}
}
