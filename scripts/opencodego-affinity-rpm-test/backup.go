package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func createBackup(runDir, databasePath, binaryPath string) error {
	backupDir := filepath.Join(runDir, "backup")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return err
	}
	databaseAbsolute, err := filepath.Abs(databasePath)
	if err != nil {
		return err
	}
	backupDatabase := filepath.Join(backupDir, "one-api.db")
	if _, err := os.Stat(backupDatabase); os.IsNotExist(err) {
		db, openErr := gorm.Open(sqlite.Open(databaseAbsolute), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if openErr != nil {
			return fmt.Errorf("open SQLite for backup: %w", openErr)
		}
		quotedTarget := strings.ReplaceAll(filepath.ToSlash(backupDatabase), "'", "''")
		if execErr := db.Exec("VACUUM INTO '" + quotedTarget + "'").Error; execErr != nil {
			return fmt.Errorf("create consistent SQLite backup: %w", execErr)
		}
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	}
	var integrity string
	backupDB, err := openReadOnlySQLite(backupDatabase)
	if err != nil {
		return err
	}
	if err := backupDB.Raw("PRAGMA integrity_check").Scan(&integrity).Error; err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("backup SQLite integrity_check returned %q", integrity)
	}
	if strings.TrimSpace(binaryPath) != "" {
		backupBinary := filepath.Join(backupDir, "new-api.exe")
		if _, err := os.Stat(backupBinary); os.IsNotExist(err) {
			if err := copyFile(binaryPath, backupBinary); err != nil {
				return err
			}
		}
	}
	if err := writeRollbackScript(runDir, databaseAbsolute, binaryPath); err != nil {
		return err
	}
	databaseHash, _ := fileSHA256(backupDatabase)
	binaryHash := ""
	if strings.TrimSpace(binaryPath) != "" {
		binaryHash, _ = fileSHA256(filepath.Join(backupDir, "new-api.exe"))
	}
	return writeJSONAtomic(filepath.Join(backupDir, "checksums.json"), map[string]any{
		"created_at": utcNow(), "sqlite_integrity": integrity,
		"database_sha256": databaseHash, "binary_sha256": binaryHash,
	})
}

func copyFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}
