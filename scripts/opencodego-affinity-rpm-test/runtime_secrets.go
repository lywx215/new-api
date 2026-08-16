package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	rootcommon "github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const runtimeSecretsFileName = "runtime-secrets.local.json"

type runtimeSecrets struct {
	SessionSecret  string `json:"session_secret"`
	CryptoSecret   string `json:"crypto_secret"`
	AffinitySecret string `json:"affinity_secret"`
	RootPAT        string `json:"root_pat"`
	RedisURL       string `json:"redis_url"`
	CreatedAt      string `json:"created_at"`
}

type runtimeRootUser struct {
	ID          int     `gorm:"column:id"`
	AccessToken *string `gorm:"column:access_token"`
}

func secureRandomBase64URL(byteCount int) (string, error) {
	buffer := make([]byte, byteCount)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func prepareRuntimeSecrets(runDir, databasePath, redisURL string, apply bool) error {
	if !apply {
		return errors.New("refusing to generate runtime secrets or rotate the root PAT without --apply")
	}
	if strings.TrimSpace(redisURL) == "" {
		return errors.New("test Redis URL is required")
	}
	if err := ensureRunDir(runDir); err != nil {
		return err
	}
	secretsPath := filepath.Join(runDir, runtimeSecretsFileName)
	secrets := runtimeSecrets{}
	if err := readJSONFile(secretsPath, &secrets); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		var generateErr error
		if secrets.SessionSecret, generateErr = secureRandomBase64URL(32); generateErr != nil {
			return generateErr
		}
		if secrets.CryptoSecret, generateErr = secureRandomBase64URL(32); generateErr != nil {
			return generateErr
		}
		if secrets.AffinitySecret, generateErr = secureRandomBase64URL(32); generateErr != nil {
			return generateErr
		}
		if secrets.RootPAT, generateErr = secureRandomBase64URL(24); generateErr != nil {
			return generateErr
		}
		secrets.RedisURL = redisURL
		secrets.CreatedAt = utcNow()
		if err := writeJSONAtomic(secretsPath, secrets); err != nil {
			return err
		}
	}
	if secrets.SessionSecret == "" || secrets.CryptoSecret == "" || secrets.AffinitySecret == "" || secrets.RootPAT == "" || secrets.RedisURL == "" {
		return errors.New("runtime secrets file is incomplete")
	}
	if redactSecret(secrets.RedisURL) != redactSecret(redisURL) {
		return errors.New("runtime secrets file belongs to a different Redis connection")
	}

	databaseAbsolute, err := filepath.Abs(databasePath)
	if err != nil {
		return err
	}
	db, err := gorm.Open(sqlite.Open(databaseAbsolute), &gorm.Config{})
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	var root runtimeRootUser
	if err := db.Table("users").Select("id", "access_token").Where("role = ?", rootcommon.RoleRootUser).Where("deleted_at IS NULL").Order("id").First(&root).Error; err != nil {
		return fmt.Errorf("find root user: %w", err)
	}
	if err := db.Table("users").Where("id = ?", root.ID).Update("access_token", secrets.RootPAT).Error; err != nil {
		return fmt.Errorf("rotate root PAT: %w", err)
	}
	return nil
}
