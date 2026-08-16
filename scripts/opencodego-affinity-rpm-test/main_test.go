package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rootcommon "github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestInterruptedStepIsRecoverable(t *testing.T) {
	runDir := t.TempDir()
	require.NoError(t, initRun(runDir, "resume-test"))
	require.NoError(t, updateStep(runDir, "inventory", statusRunning, "input-a", "", nil))

	require.NoError(t, initRun(runDir, "resume-test"))
	state, err := loadState(runDir)
	require.NoError(t, err)
	assert.Equal(t, statusAborted, state.Steps["inventory"].Status)
	assert.Contains(t, state.Steps["inventory"].Error, "previous process ended")

	require.NoError(t, updateStep(runDir, "inventory", statusRunning, "input-a", "", nil))
	require.NoError(t, updateStep(runDir, "inventory", statusPassed, "input-a", "", []string{"inventory.json"}))
	err = updateStep(runDir, "inventory", statusRunning, "input-a", "", nil)
	assert.ErrorIs(t, err, errStepAlreadyPassed)
}

func TestTerminalStepUpdateClearsStaleErrorAndArtifacts(t *testing.T) {
	runDir := t.TempDir()
	require.NoError(t, initRun(runDir, "state-clear-test"))
	require.NoError(t, updateStep(runDir, "upgrade", statusNeedsManual, "input-a", "manual gate", []string{"staged.exe"}))
	require.NoError(t, updateStep(runDir, "upgrade", statusPassed, "input-a", "", nil))
	state, err := loadState(runDir)
	require.NoError(t, err)
	assert.Empty(t, state.Steps["upgrade"].Error)
	assert.Empty(t, state.Steps["upgrade"].Artifacts)
}

func TestSkippedStepIsTerminalAndProducesPartialVerdict(t *testing.T) {
	runDir := t.TempDir()
	require.NoError(t, initRun(runDir, "skipped-pressure-test"))
	for name := range map[string]bool{
		"inventory": true, "code-validation": true, "mock-selftest": true, "redis-validation": true,
		"verify-channels": true, "mock-gateway-e2e": true, "affinity-smoke": true, "cache-migration-low-rpm": true,
		"dual-instance": true,
	} {
		require.NoError(t, updateStep(runDir, name, statusPassed, "input-a", "", nil))
	}
	for _, name := range []string{"live-gate", "hard-limit", "post429-cache-migration", "three-customer-4800-rpm"} {
		require.NoError(t, updateStep(runDir, name, statusSkipped, "input-a", "declined by user", nil))
	}

	require.NoError(t, generateReport(runDir))
	state, err := loadState(runDir)
	require.NoError(t, err)
	assert.Equal(t, statusSkipped, state.Steps["hard-limit"].Status)
	assert.Equal(t, "declined by user", state.Steps["hard-limit"].Error)
	summary, err := os.ReadFile(filepath.Join(runDir, "summary.md"))
	require.NoError(t, err)
	assert.Contains(t, string(summary), "Verdict: **PARTIAL**")
	assert.Contains(t, string(summary), "| `hard-limit` | skipped |")
}

func TestMockAccountResetRemovesReadinessState(t *testing.T) {
	mock := newMockAccount("readiness", 2, "seconds", "")
	mock.count.Store(3)
	mock.headerLeak.Store(1)
	mock.mu.Lock()
	mock.cache["readiness-key"] = true
	mock.windowUsed = 3
	mock.blockedTil = time.Now().Add(10 * time.Second)
	mock.mu.Unlock()

	mock.reset()

	assert.Zero(t, mock.count.Load())
	assert.Zero(t, mock.headerLeak.Load())
	mock.mu.Lock()
	defer mock.mu.Unlock()
	assert.Empty(t, mock.cache)
	assert.Zero(t, mock.windowUsed)
	assert.True(t, mock.blockedTil.IsZero())
}

func TestVerifyLiveChannelLayoutIgnoresIsolatedMockChannels(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "channel-layout.db")
	db, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE channels (id INTEGER PRIMARY KEY, name TEXT, type INTEGER, status INTEGER, `group` TEXT, models TEXT, `key` TEXT)").Error)
	rows := []string{
		"(1,'real-a',99,1,'ocg-affinity-lower-e2e,ocg-hardlimit-a','deepseek-v4-flash','account-a')",
		"(2,'real-b',99,1,'ocg-affinity-lower-e2e,ocg-hardlimit-b','deepseek-v4-flash','account-b')",
		"(3,'real-c',99,1,'ocg-affinity-lower-e2e,ocg-hardlimit-c','deepseek-v4-flash','account-c')",
		"(4,'mock-a',99,1,'ocg-mock-lower','deepseek-v4-flash','mock-key')",
		"(5,'mock-b',99,1,'ocg-mock-lower','deepseek-v4-flash','mock-key')",
		"(6,'mock-c',99,1,'ocg-mock-lower','deepseek-v4-flash','mock-key')",
		"(7,'mock-loopback',60,1,'ocg-mock-upper','deepseek-v4-flash','loopback-key')",
	}
	require.NoError(t, db.Exec("INSERT INTO channels (id,name,type,status,`group`,models,`key`) VALUES "+strings.Join(rows, ",")).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	issues, err := verifyLiveChannelLayout(databasePath)
	require.NoError(t, err)
	assert.Empty(t, issues)
}

func TestVerifyLiveChannelLayoutRejectsDuplicateRealAccountKeys(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "duplicate-account-layout.db")
	db, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE channels (id INTEGER PRIMARY KEY, name TEXT, type INTEGER, status INTEGER, `group` TEXT, models TEXT, `key` TEXT)").Error)
	rows := []string{
		"(1,'real-a',99,1,'ocg-affinity-lower-e2e,ocg-hardlimit-a','deepseek-v4-flash','duplicate-account')",
		"(2,'real-b',99,1,'ocg-affinity-lower-e2e,ocg-hardlimit-b','deepseek-v4-flash','account-b')",
		"(3,'real-c',99,1,'ocg-affinity-lower-e2e,ocg-hardlimit-c','deepseek-v4-flash','duplicate-account')",
	}
	require.NoError(t, db.Exec("INSERT INTO channels (id,name,type,status,`group`,models,`key`) VALUES "+strings.Join(rows, ",")).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	issues, err := verifyLiveChannelLayout(databasePath)
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Contains(t, issues[0], "1 (real-a), 3 (real-c)")
	assert.Contains(t, issues[0], "requires 3 independent accounts")
}

func TestLowerLowProfilePreservesDualInstanceRoles(t *testing.T) {
	profile, err := profileByName("lower-low")
	require.NoError(t, err)
	assert.Equal(t, "true", profile.Values["channel_affinity_setting.accept_internal_key"])
	assert.Equal(t, "false", profile.Values["channel_affinity_setting.generate_internal_key"])
	assert.Equal(t, "true", profile.Values["channel_affinity_setting.rpm_guard_enabled"])
	assert.Equal(t, "1", profile.Values["channel_affinity_setting.default_account_rpm"])
	assert.Equal(t, "4", profile.Values["channel_affinity_setting.account_burst"])
}

func TestOpenCodeGoObservationCorrelatesType60RequestChain(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "request-correlation.db")
	db, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE logs (id INTEGER PRIMARY KEY, channel_id INTEGER, request_id TEXT, upstream_request_id TEXT, model_name TEXT, other TEXT)").Error)
	trusted := `{"pricing_channel_type":99,"admin_info":{"channel_affinity":{"key_fp":"linked123","source_type":"internal_header","final_channel_id":7}}}`
	stale := `{"pricing_channel_type":99,"admin_info":{"channel_affinity":{"key_fp":"stale999","source_type":"internal_header","final_channel_id":5}}}`
	require.NoError(t, db.Exec("INSERT INTO logs (id,channel_id,request_id,upstream_request_id,model_name,other) VALUES (1,7,'lower-request','','deepseek-v4-flash',?),(2,8,'outer-request','lower-request','deepseek-v4-flash','{\"pricing_channel_type\":60}'),(3,5,'unrelated-request','','deepseek-v4-flash',?)", trusted, stale).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	observation, err := latestOpenCodeGoObservation(databasePath, 0, "outer-request")
	require.NoError(t, err)
	assert.Equal(t, 7, observation.ChannelID)
	assert.Equal(t, "linked123", observation.KeyFP)
}

func TestOpenCodeGoObservationCorrelatesAcrossUpperAndLowerDatabases(t *testing.T) {
	upperPath := filepath.Join(t.TempDir(), "upper.db")
	lowerPath := filepath.Join(t.TempDir(), "lower.db")
	for _, path := range []string{upperPath, lowerPath} {
		db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
		require.NoError(t, err)
		require.NoError(t, db.Exec("CREATE TABLE logs (id INTEGER PRIMARY KEY, channel_id INTEGER, request_id TEXT, upstream_request_id TEXT, model_name TEXT, other TEXT)").Error)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())
	}
	upper, err := gorm.Open(sqlite.Open(upperPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, upper.Exec("INSERT INTO logs (id,channel_id,request_id,upstream_request_id,model_name,other) VALUES (1,4,'outer-request','lower-request','deepseek-v4-flash','{\"pricing_channel_type\":60}')").Error)
	upperSQL, err := upper.DB()
	require.NoError(t, err)
	require.NoError(t, upperSQL.Close())
	lower, err := gorm.Open(sqlite.Open(lowerPath), &gorm.Config{})
	require.NoError(t, err)
	other := `{"pricing_channel_type":99,"admin_info":{"channel_affinity":{"key_fp":"crossdb1","source_type":"internal_header","final_channel_id":3}}}`
	require.NoError(t, lower.Exec("INSERT INTO logs (id,channel_id,request_id,upstream_request_id,model_name,other) VALUES (1,3,'lower-request','','deepseek-v4-flash',?)", other).Error)
	lowerSQL, err := lower.DB()
	require.NoError(t, err)
	require.NoError(t, lowerSQL.Close())

	observation, err := latestOpenCodeGoObservationAcrossDatabases(lowerPath, upperPath, 0, "outer-request")
	require.NoError(t, err)
	assert.Equal(t, 3, observation.ChannelID)
	assert.Equal(t, "crossdb1", observation.KeyFP)
}

func TestBudgetRebuildUsesDurableRequestLedger(t *testing.T) {
	runDir := t.TempDir()
	require.NoError(t, initRun(runDir, "budget-test"))
	require.NoError(t, appendJSONLine(filepath.Join(runDir, "requests.ndjson"), requestRecord{RealUpstream: true, EstimatedCostUSD: 0.25}))
	require.NoError(t, appendJSONLine(filepath.Join(runDir, "requests.ndjson"), requestRecord{RealUpstream: false, EstimatedCostUSD: 9}))
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "budget.json"), []byte("not-json"), 0o600))

	budget, err := rebuildBudget(runDir)
	require.NoError(t, err)
	assert.Equal(t, 1, budget.Requests)
	assert.InDelta(t, 0.25, budget.EstimatedUSD, 0.000001)
	assert.Equal(t, defaultMaxRequests, budget.MaxRequests)
	assert.InDelta(t, defaultMaxUSD, budget.MaxEstimatedUSD, 0.000001)
}

func TestBudgetCarryoverSurvivesLedgerRebuild(t *testing.T) {
	sourceDir := t.TempDir()
	require.NoError(t, initRun(sourceDir, "old-run"))
	require.NoError(t, appendJSONLine(filepath.Join(sourceDir, "requests.ndjson"), requestRecord{RealUpstream: true, EstimatedCostUSD: 0.75}))
	_, err := rebuildBudget(sourceDir)
	require.NoError(t, err)

	targetDir := t.TempDir()
	require.NoError(t, initRun(targetDir, "new-run"))
	require.NoError(t, carryBudget(targetDir, sourceDir))
	require.NoError(t, appendJSONLine(filepath.Join(targetDir, "requests.ndjson"), requestRecord{RealUpstream: true, EstimatedCostUSD: 0.25}))
	budget, err := rebuildBudget(targetDir)
	require.NoError(t, err)
	assert.Equal(t, 2, budget.Requests)
	assert.Equal(t, 1, budget.CarryoverRequests)
	assert.InDelta(t, 1.0, budget.EstimatedUSD, 0.000001)
	assert.InDelta(t, 0.75, budget.CarryoverEstimatedUSD, 0.000001)
}

func TestPrepareRuntimeSecretsIsIdempotentAndRotatesRootPAT(t *testing.T) {
	runDir := t.TempDir()
	databasePath := filepath.Join(t.TempDir(), "runtime-secrets.db")
	db, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, role INTEGER, access_token TEXT, deleted_at DATETIME)").Error)
	require.NoError(t, db.Exec("INSERT INTO users (id, role, access_token) VALUES (1, ?, 'old-pat')", rootcommon.RoleRootUser).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	redisURL := "redis://:test-password@127.0.0.1:6380/0"
	require.NoError(t, prepareRuntimeSecrets(runDir, databasePath, redisURL, true))
	first := runtimeSecrets{}
	require.NoError(t, readJSONFile(filepath.Join(runDir, runtimeSecretsFileName), &first))
	require.NotEmpty(t, first.SessionSecret)
	require.NotEmpty(t, first.CryptoSecret)
	require.NotEmpty(t, first.AffinitySecret)
	require.Len(t, first.RootPAT, 32)

	require.NoError(t, prepareRuntimeSecrets(runDir, databasePath, redisURL, true))
	second := runtimeSecrets{}
	require.NoError(t, readJSONFile(filepath.Join(runDir, runtimeSecretsFileName), &second))
	assert.Equal(t, first, second)

	check, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{})
	require.NoError(t, err)
	var stored string
	require.NoError(t, check.Table("users").Select("access_token").Where("id = 1").Scan(&stored).Error)
	assert.Equal(t, first.RootPAT, stored)
	checkSQLDB, err := check.DB()
	require.NoError(t, err)
	require.NoError(t, checkSQLDB.Close())
}

func TestSettingsOrderAvoidsInvalidIntermediateRPMCombination(t *testing.T) {
	current := map[string]string{
		"channel_affinity_setting.default_account_rpm": "1450",
		"channel_affinity_setting.account_burst":       "50",
	}
	target := map[string]string{
		"channel_affinity_setting.default_account_rpm": "1",
		"channel_affinity_setting.account_burst":       "1599",
	}

	keys := orderedProfileKeys(current, target)
	require.Len(t, keys, 2)
	assert.Equal(t, "channel_affinity_setting.default_account_rpm", keys[0])
	assert.Equal(t, "channel_affinity_setting.account_burst", keys[1])
}

func TestCacheClassificationBoundaries(t *testing.T) {
	assert.Equal(t, "cold", classifyCache(9, 100))
	assert.Equal(t, "partial", classifyCache(10, 100))
	assert.Equal(t, "partial", classifyCache(79, 100))
	assert.Equal(t, "hot", classifyCache(80, 100))
	assert.Equal(t, "cold", classifyCache(0, 0))
}

func TestCacheTransitionCSVIncludesAttempt(t *testing.T) {
	runDir := t.TempDir()
	require.NoError(t, writeCacheTransitions(runDir, []cacheTransition{{Attempt: 3, Sequence: 1, ChannelID: 9, Status: 200}}))
	data, err := os.ReadFile(filepath.Join(runDir, "cache-transitions.csv"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)
	assert.Contains(t, lines[0], "attempt")
	assert.True(t, strings.HasPrefix(lines[1], "3,"))
}

func TestCacheMigrationAttemptsUseDistinctAffinitySources(t *testing.T) {
	first := cacheMigrationPromptKey("run-1", 1)
	second := cacheMigrationPromptKey("run-1", 2)
	assert.NotEqual(t, first, second)
	assert.Equal(t, "run-1-cache-migration-a1", first)
	assert.Equal(t, "run-1-cache-migration-a2", second)
}

func TestOpenCodeGoTestChannelIDsUseExactGroupMembership(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "channels.db")
	db, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE channels (id INTEGER PRIMARY KEY, type INTEGER, status INTEGER, `group` TEXT)").Error)
	require.NoError(t, db.Exec("INSERT INTO channels (id,type,status,`group`) VALUES (1,99,1,'default,ocg-affinity-lower-e2e'),(2,99,1,'ocg-affinity-lower-e2e-extra'),(3,99,0,'ocg-affinity-lower-e2e'),(4,99,1,'ocg-affinity-lower-e2e')").Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	ids, err := openCodeGoTestChannelIDs(databasePath, "ocg-affinity-lower-e2e")
	require.NoError(t, err)
	assert.Equal(t, []int{1, 4}, ids)
}

func TestCleanupChannelRedisDeletesOnlyExactTargetKeys(t *testing.T) {
	server := miniredis.RunT(t)
	server.Set("opencodego:rpm:1", "target")
	server.Set("opencodego:cooldown:1", "target")
	currentMinute := time.Now().Unix() / 60
	server.Set(fmt.Sprintf("opencodego:rpm_count:1:%d", currentMinute), "target")
	server.Set(fmt.Sprintf("opencodego:rpm_count:1:%d", currentMinute-1), "expired-audit")
	server.Set("opencodego:rpm:2", "keep")
	server.Set("channel_affinity:trusted_internal:keep", "keep")
	require.NoError(t, cleanupChannelRedis("redis://"+server.Addr()+"/0", []int{1}, true))
	assert.False(t, server.Exists("opencodego:rpm:1"))
	assert.False(t, server.Exists("opencodego:cooldown:1"))
	assert.False(t, server.Exists(fmt.Sprintf("opencodego:rpm_count:1:%d", currentMinute)))
	assert.True(t, server.Exists(fmt.Sprintf("opencodego:rpm_count:1:%d", currentMinute-1)))
	assert.True(t, server.Exists("opencodego:rpm:2"))
	assert.True(t, server.Exists("channel_affinity:trusted_internal:keep"))
}

func TestSanitizePreviewRemovesSecretsAndAffinityHeader(t *testing.T) {
	input := `Authorization: Bearer sk-secret X-NewAPI-Affinity-Key: v1.payload.signature "key":"provider-secret"`
	output := sanitizePreview(input)
	assert.NotContains(t, output, "sk-secret")
	assert.NotContains(t, output, "v1.payload.signature")
	assert.NotContains(t, output, "provider-secret")
	assert.Contains(t, output, "[REDACTED]")
}

func TestMockContractCoversExactHardLimitAndCacheIsolation(t *testing.T) {
	require.NoError(t, mockSelfTest(t.TempDir()))
}
