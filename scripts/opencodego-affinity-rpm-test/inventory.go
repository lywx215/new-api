package main

import (
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	rootcommon "github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openReadOnlySQLite(path string) (*gorm.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(absolute) + "?mode=ro"
	return gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
}

func captureInventory(runDir, runID, databasePath, binaryPath, serviceURL string) (*inventoryResult, error) {
	db, err := openReadOnlySQLite(databasePath)
	if err != nil {
		return nil, fmt.Errorf("open SQLite read-only: %w", err)
	}
	result := &inventoryResult{
		CapturedAt:      utcNow(),
		AffinityOptions: map[string]string{},
		ModelRateLimit:  map[string]string{},
	}
	if err := db.Raw("PRAGMA integrity_check").Scan(&result.SQLiteIntegrity).Error; err != nil {
		return nil, fmt.Errorf("SQLite integrity check: %w", err)
	}
	if err := db.Raw("SELECT COALESCE(MAX(id), 0) FROM logs").Scan(&result.LatestLogID).Error; err != nil {
		return nil, fmt.Errorf("read latest log id: %w", err)
	}
	var rows []struct {
		ID       int
		Name     string
		Type     int
		Status   int
		Group    string
		Models   string
		Priority int
		Weight   int
		Key      string
		BaseURL  string `gorm:"column:base_url"`
		Settings string
	}
	if err := db.Raw("SELECT id,name,type,status,`group`,models,priority,weight,`key`,base_url,settings FROM channels WHERE type IN (60,99) ORDER BY type,id").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("read channels: %w", err)
	}
	accountChannelsByKey := map[string][]string{}
	for _, row := range rows {
		rpmLimit := 0
		if strings.TrimSpace(row.Settings) != "" {
			var settings map[string]any
			if err := rootcommon.UnmarshalJsonStr(row.Settings, &settings); err == nil {
				switch value := settings["opencodego_rpm_limit"].(type) {
				case float64:
					rpmLimit = int(value)
				case string:
					rpmLimit, _ = strconv.Atoi(value)
				}
			}
		}
		channel := channelInventory{
			ID: row.ID, Name: row.Name, Type: row.Type, Status: row.Status, Group: row.Group,
			Models: row.Models, Priority: row.Priority, Weight: row.Weight, RPMLimit: rpmLimit,
			KeyHash8: hash8(row.Key), HasRawKey: row.Key != "", BaseURL: row.BaseURL, Settings: row.Settings,
		}
		if row.Type == 99 {
			result.OpenCodeGo = append(result.OpenCodeGo, channel)
			if row.Key != "" && !csvSet(row.Group)["ocg-mock-lower"] {
				accountChannelsByKey[row.Key] = append(accountChannelsByKey[row.Key], fmt.Sprintf("%d (%s)", row.ID, row.Name))
			}
		} else {
			result.NewAPIChannels = append(result.NewAPIChannels, channel)
		}
	}
	var options []struct{ Key, Value string }
	if err := db.Raw("SELECT `key`,value FROM options WHERE `key` LIKE 'channel_affinity_setting.%' OR `key` IN ('ModelRequestRateLimitEnabled','ModelRequestRateLimitCount','ModelRequestRateLimitSuccessCount','ModelRequestRateLimitDurationMinutes','ModelRequestRateLimitGroup') ORDER BY `key`").Scan(&options).Error; err != nil {
		return nil, fmt.Errorf("read options: %w", err)
	}
	for _, option := range options {
		if strings.HasPrefix(option.Key, "channel_affinity_setting.") {
			result.AffinityOptions[option.Key] = option.Value
		} else {
			result.ModelRateLimit[option.Key] = option.Value
		}
	}
	liveOpenCodeGo := make([]channelInventory, 0, len(result.OpenCodeGo))
	for _, channel := range result.OpenCodeGo {
		if csvSet(channel.Group)["ocg-mock-lower"] {
			continue
		}
		liveOpenCodeGo = append(liveOpenCodeGo, channel)
		if channel.Status != 1 {
			result.ManualBlockers = append(result.ManualBlockers, fmt.Sprintf("OpenCodeGo channel %d (%s) is not enabled", channel.ID, channel.Name))
		}
		if !containsCSV(channel.Models, defaultModel) {
			result.ManualBlockers = append(result.ManualBlockers, fmt.Sprintf("OpenCodeGo channel %d (%s) does not expose %s", channel.ID, channel.Name, defaultModel))
		}
	}
	if len(liveOpenCodeGo) != 3 {
		result.ManualBlockers = append(result.ManualBlockers, fmt.Sprintf("need exactly 3 non-Mock OpenCodeGo channels for the live suite; found %d", len(liveOpenCodeGo)))
	}
	for key, channels := range accountChannelsByKey {
		if len(channels) > 1 {
			result.ManualBlockers = append(result.ManualBlockers, fmt.Sprintf("OpenCodeGo channels %s use the same account key fingerprint %s; the live suite requires 3 independent accounts", strings.Join(channels, ", "), hash8(key)))
		}
	}
	if serviceURL != "" {
		client := &http.Client{Timeout: 5 * time.Second}
		response, requestErr := client.Get(strings.TrimRight(serviceURL, "/") + "/api/status")
		if requestErr == nil {
			result.ServiceStatusCode = response.StatusCode
			response.Body.Close()
		}
	}
	manifestValue := manifest{
		RunID: runID, ToolVersion: toolVersion, CreatedAt: utcNow(), DatabasePath: databasePath,
		ServiceURL: serviceURL, ServiceHealthy: result.ServiceStatusCode == http.StatusOK, Model: defaultModel,
		Environment: map[string]string{},
	}
	manifestValue.GitCommit = commandOutput("git", "rev-parse", "HEAD")
	manifestValue.GitBranch = commandOutput("git", "branch", "--show-current")
	manifestValue.GitDirty = strings.TrimSpace(commandOutput("git", "status", "--porcelain")) != ""
	if hash, hashErr := fileSHA256(databasePath); hashErr == nil {
		manifestValue.DatabaseSHA256 = hash
	}
	if binaryPath != "" {
		manifestValue.BinaryPath = binaryPath
		if hash, hashErr := fileSHA256(binaryPath); hashErr == nil {
			manifestValue.BinarySHA256 = hash
		}
	}
	if err := writeJSONAtomic(filepath.Join(runDir, "manifest.json"), manifestValue); err != nil {
		return nil, err
	}
	if err := writeJSONAtomic(filepath.Join(runDir, "inventory.json"), result); err != nil {
		return nil, err
	}
	if err := writeJSONAtomic(filepath.Join(runDir, "config-before.json"), map[string]any{
		"captured_at": result.CapturedAt,
		"affinity":    result.AffinityOptions,
		"rate_limit":  result.ModelRateLimit,
		"channels":    append(append([]channelInventory{}, result.OpenCodeGo...), result.NewAPIChannels...),
	}); err != nil {
		return nil, err
	}
	return result, nil
}

func commandOutput(name string, args ...string) string {
	command := exec.Command(name, args...)
	output, err := command.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func containsCSV(value, expected string) bool {
	for _, item := range strings.Split(value, ",") {
		if strings.TrimSpace(item) == expected {
			return true
		}
	}
	return false
}

func verifyLiveChannelLayout(databasePath string) ([]string, error) {
	db, err := openReadOnlySQLite(databasePath)
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	defer sqlDB.Close()
	var channels []struct {
		ID     int
		Name   string
		Type   int
		Status int
		Group  string
		Models string
		Key    string
	}
	if err := db.Raw("SELECT id,name,type,status,`group`,models,`key` FROM channels WHERE type IN (60,99) ORDER BY id").Scan(&channels).Error; err != nil {
		return nil, err
	}
	issues := make([]string, 0)
	hardGroups := []string{"ocg-hardlimit-a", "ocg-hardlimit-b", "ocg-hardlimit-c"}
	counts := map[string]int{}
	accountChannelsByKey := map[string][]string{}
	opencodeCount := 0
	for _, channel := range channels {
		groups := csvSet(channel.Group)
		if channel.Type == 60 {
			for _, forbidden := range append([]string{"ocg-affinity-lower-e2e"}, hardGroups...) {
				if groups[forbidden] {
					issues = append(issues, fmt.Sprintf("New API channel %d (%s) must not belong to %s", channel.ID, channel.Name, forbidden))
				}
			}
			continue
		}
		if groups["ocg-mock-lower"] {
			for _, forbidden := range append([]string{"ocg-affinity-lower-e2e"}, hardGroups...) {
				if groups[forbidden] {
					issues = append(issues, fmt.Sprintf("Mock OpenCodeGo channel %d (%s) must not belong to %s", channel.ID, channel.Name, forbidden))
				}
			}
			continue
		}
		opencodeCount++
		if channel.Key != "" {
			accountChannelsByKey[channel.Key] = append(accountChannelsByKey[channel.Key], fmt.Sprintf("%d (%s)", channel.ID, channel.Name))
		}
		if channel.Status != 1 {
			issues = append(issues, fmt.Sprintf("OpenCodeGo channel %d (%s) is disabled", channel.ID, channel.Name))
		}
		if !groups["ocg-affinity-lower-e2e"] {
			issues = append(issues, fmt.Sprintf("OpenCodeGo channel %d (%s) is not in ocg-affinity-lower-e2e", channel.ID, channel.Name))
		}
		if !containsCSV(channel.Models, defaultModel) {
			issues = append(issues, fmt.Sprintf("OpenCodeGo channel %d (%s) does not support %s", channel.ID, channel.Name, defaultModel))
		}
		for _, hardGroup := range hardGroups {
			if groups[hardGroup] {
				counts[hardGroup]++
			}
		}
	}
	if opencodeCount != 3 {
		issues = append(issues, fmt.Sprintf("expected exactly 3 OpenCodeGo channels, found %d", opencodeCount))
	}
	for _, hardGroup := range hardGroups {
		if counts[hardGroup] != 1 {
			issues = append(issues, fmt.Sprintf("%s must contain exactly one OpenCodeGo channel, found %d", hardGroup, counts[hardGroup]))
		}
	}
	for key, channels := range accountChannelsByKey {
		if len(channels) > 1 {
			issues = append(issues, fmt.Sprintf("OpenCodeGo channels %s use the same account key fingerprint %s; the live suite requires 3 independent accounts", strings.Join(channels, ", "), hash8(key)))
		}
	}
	return issues, nil
}

func csvSet(value string) map[string]bool {
	result := map[string]bool{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			result[item] = true
		}
	}
	return result
}
