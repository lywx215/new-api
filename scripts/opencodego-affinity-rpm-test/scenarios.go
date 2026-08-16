package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	rootcommon "github.com/QuantumNous/new-api/common"
)

type logObservation struct {
	LogID           int64
	ChannelID       int
	KeyFP           string
	SourceType      string
	MigrationReason string
	FinalChannelID  int
}

func latestOpenCodeGoObservation(databasePath string, afterLogID int64, responseRequestID ...string) (logObservation, error) {
	db, err := openReadOnlySQLite(databasePath)
	if err != nil {
		return logObservation{}, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return logObservation{}, err
	}
	defer sqlDB.Close()
	targetRequestID := ""
	if len(responseRequestID) > 0 {
		targetRequestID = strings.TrimSpace(responseRequestID[0])
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var rows []struct {
			ID                int64
			ChannelID         int
			RequestID         string
			UpstreamRequestID string
			Other             string
		}
		queryErr := db.Raw("SELECT id,channel_id,request_id,upstream_request_id,other FROM logs WHERE id>? AND model_name=? ORDER BY id", afterLogID, defaultModel).Scan(&rows).Error
		if queryErr != nil {
			return logObservation{}, queryErr
		}
		linkedRequestIDs := map[string]bool{}
		if targetRequestID != "" {
			linkedRequestIDs[targetRequestID] = true
			for changed := true; changed; {
				changed = false
				for _, row := range rows {
					if linkedRequestIDs[row.RequestID] && row.UpstreamRequestID != "" && !linkedRequestIDs[row.UpstreamRequestID] {
						linkedRequestIDs[row.UpstreamRequestID] = true
						changed = true
					}
				}
			}
		}
		for index := len(rows) - 1; index >= 0; index-- {
			if targetRequestID != "" && !linkedRequestIDs[rows[index].RequestID] {
				continue
			}
			var other map[string]any
			if rootcommon.UnmarshalJsonStr(rows[index].Other, &other) != nil {
				continue
			}
			if intFromAny(other["pricing_channel_type"]) != 99 {
				continue
			}
			observation := logObservation{LogID: rows[index].ID, ChannelID: rows[index].ChannelID}
			if adminInfo, ok := other["admin_info"].(map[string]any); ok {
				if affinity, ok := adminInfo["channel_affinity"].(map[string]any); ok {
					observation.KeyFP = stringFromAny(affinity["key_fp"])
					observation.SourceType = stringFromAny(affinity["source_type"])
					observation.MigrationReason = stringFromAny(affinity["migration_reason"])
					observation.FinalChannelID = intFromAny(affinity["final_channel_id"])
				}
			}
			return observation, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return logObservation{}, fmt.Errorf("no OpenCodeGo log appeared after id %d", afterLogID)
}

func latestOpenCodeGoObservationAcrossDatabases(lowerDatabasePath, outerDatabasePath string, afterLowerLogID int64, outerRequestID string) (logObservation, error) {
	if strings.TrimSpace(outerDatabasePath) == "" || filepath.Clean(outerDatabasePath) == filepath.Clean(lowerDatabasePath) {
		return latestOpenCodeGoObservation(lowerDatabasePath, afterLowerLogID, outerRequestID)
	}
	outerDB, err := openReadOnlySQLite(outerDatabasePath)
	if err != nil {
		return logObservation{}, err
	}
	sqlDB, err := outerDB.DB()
	if err != nil {
		return logObservation{}, err
	}
	defer sqlDB.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var upstreamRequestID string
		queryErr := outerDB.Raw("SELECT upstream_request_id FROM logs WHERE request_id=? AND model_name=? ORDER BY id DESC LIMIT 1", outerRequestID, defaultModel).Scan(&upstreamRequestID).Error
		if queryErr != nil {
			return logObservation{}, queryErr
		}
		if strings.TrimSpace(upstreamRequestID) != "" {
			return latestOpenCodeGoObservation(lowerDatabasePath, afterLowerLogID, upstreamRequestID)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return logObservation{}, fmt.Errorf("outer request %s did not record an upstream request id", outerRequestID)
}

func latestLogID(databasePath string) (int64, error) {
	db, err := openReadOnlySQLite(databasePath)
	if err != nil {
		return 0, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return 0, err
	}
	defer sqlDB.Close()
	var id int64
	if err := db.Raw("SELECT COALESCE(MAX(id),0) FROM logs").Scan(&id).Error; err != nil {
		return 0, err
	}
	return id, nil
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case string:
		parsed, _ := strconv.Atoi(typed)
		return parsed
	default:
		return 0
	}
}

func stringFromAny(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func loadProvisionedSecret(runDir, name string) (string, error) {
	secrets := map[string]string{}
	if err := readJSONFile(filepath.Join(runDir, "secrets.local.json"), &secrets); err != nil {
		return "", err
	}
	value := strings.TrimSpace(secrets[name])
	if value == "" {
		return "", fmt.Errorf("secret for token %s is missing", name)
	}
	return value, nil
}

func clearTrustedAffinity(baseURL, pat string) error {
	client, err := newAPIClient(baseURL, pat)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err = client.request(ctx, http.MethodDelete, "/api/option/channel_affinity_cache?rule_name=trusted_internal", nil)
	return err
}

func affinityProbe(ctx context.Context, baseURL string, token namedToken, promptCacheKey, session, userMessage, metadataUserID, forgedHeader string) requestRecord {
	payload := map[string]any{
		"model": defaultModel,
		"messages": []map[string]string{
			{"role": "system", "content": "Stable affinity smoke system prompt."},
			{"role": "user", "content": userMessage},
		},
		"max_tokens": 1,
		"stream":     false,
	}
	if promptCacheKey != "" {
		payload["prompt_cache_key"] = promptCacheKey
	}
	if metadataUserID != "" {
		payload["metadata"] = map[string]string{"user_id": metadataUserID}
	}
	body, _ := rootcommon.Marshal(payload)
	record := requestRecord{StartedAt: utcNow(), Client: token.Name, RealUpstream: true, EstimatedCostUSD: estimatedRequestCost(len(body), 1)}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+normalizeToken(token.Value))
	request.Header.Set("Content-Type", "application/json")
	if session != "" {
		request.Header.Set("x-opencode-session", session)
	}
	if forgedHeader != "" {
		request.Header.Set("X-NewAPI-Affinity-Key", forgedHeader)
	}
	started := time.Now()
	response, err := (&http.Client{Timeout: 90 * time.Second}).Do(request)
	record.DurationMS = time.Since(started).Milliseconds()
	if err != nil {
		record.ErrorClassification = "connection_error"
		record.ErrorPreview = sanitizePreview(err.Error())
		return record
	}
	defer response.Body.Close()
	record.Status = response.StatusCode
	record.RetryAfter = response.Header.Get("Retry-After")
	record.RequestID = firstNonEmpty(response.Header.Get(rootcommon.RequestIdKey), response.Header.Get("X-Request-Id"), response.Header.Get("Request-Id"))
	data, _ := ioReadAllLimited(response.Body)
	usage := parseUsage(data)
	record.CachedTokens = usage.CachedTokens
	record.PromptTokens = usage.PromptTokens
	if response.StatusCode >= 400 {
		record.ErrorPreview = sanitizePreview(string(data))
	}
	return record
}

func ioReadAllLimited(reader io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(reader, 2*1024*1024))
}

func runAffinitySmoke(runDir, runID, baseURL, pat, databasePath string) error {
	if err := checkLoadBudget(runDir, loadConfig{RunDir: runDir, Count: 9, Model: defaultModel, MaxTokens: 1, RealUpstream: true}); err != nil {
		return err
	}
	if err := clearTrustedAffinity(baseURL, pat); err != nil {
		return err
	}
	token1, err := loadProvisionedSecret(runDir, "ocg-e2e-customer-1")
	if err != nil {
		return err
	}
	token2, err := loadProvisionedSecret(runDir, "ocg-e2e-customer-2")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	writer, err := newDurableJSONLWriter(filepath.Join(runDir, "requests.ndjson"))
	if err != nil {
		return err
	}
	defer writer.Close()
	type observed struct {
		Name    string `json:"name"`
		Status  int    `json:"status"`
		Channel int    `json:"channel_id"`
		KeyFP   string `json:"key_fp"`
		Source  string `json:"source_type"`
	}
	observations := make([]observed, 0)
	probe := func(name string, token namedToken, promptKey, session, user, metadata, forged string) error {
		before, err := latestLogID(databasePath)
		if err != nil {
			return err
		}
		record := affinityProbe(ctx, baseURL, token, promptKey, session, user, metadata, forged)
		record.RunID = runID
		record.Scenario = "affinity-smoke"
		record.Sequence = len(observations) + 1
		observation, logErr := latestOpenCodeGoObservation(databasePath, before, record.RequestID)
		if logErr == nil {
			record.ChannelID = observation.ChannelID
			record.AffinityKeyFP = observation.KeyFP
			record.AffinitySource = observation.SourceType
		}
		if err := writer.Write(record); err != nil {
			return err
		}
		if err := writer.Sync(); err != nil {
			return err
		}
		observations = append(observations, observed{Name: name, Status: record.Status, Channel: record.ChannelID, KeyFP: record.AffinityKeyFP, Source: record.AffinitySource})
		if record.Status < 200 || record.Status >= 300 {
			return fmt.Errorf("affinity probe %s returned %d: %s", name, record.Status, record.ErrorPreview)
		}
		if logErr != nil {
			return fmt.Errorf("affinity probe %s log correlation: %w", name, logErr)
		}
		return nil
	}
	for index := 0; index < 5; index++ {
		if err := probe(fmt.Sprintf("same-prompt-key-%d", index+1), namedToken{Name: "customer-1", Value: token1}, runID+"-same", "session-ignored", "same request", "", ""); err != nil {
			return err
		}
	}
	if err := probe("different-customer", namedToken{Name: "customer-2", Value: token2}, runID+"-same", "session-ignored", "same request", "", ""); err != nil {
		return err
	}
	if err := probe("session-message-a", namedToken{Name: "customer-1", Value: token1}, "", runID+"-session", "first stable user message A", "", ""); err != nil {
		return err
	}
	if err := probe("session-message-b", namedToken{Name: "customer-1", Value: token1}, "", runID+"-session", "first stable user message B", "", ""); err != nil {
		return err
	}
	if err := probe("forged-header-overridden", namedToken{Name: "customer-1", Value: token1}, runID+"-forged", "", "forged header probe", "", "v1.invalid.invalid"); err != nil {
		return err
	}
	firstChannel, firstFP := observations[0].Channel, observations[0].KeyFP
	for index := 1; index < 5; index++ {
		if observations[index].Channel != firstChannel || observations[index].KeyFP != firstFP {
			return fmt.Errorf("same prompt_cache_key did not remain stable: %+v", observations[:5])
		}
	}
	if observations[5].KeyFP == firstFP {
		return errors.New("different customer token generated the same internal affinity fingerprint")
	}
	if observations[6].KeyFP == observations[7].KeyFP {
		return errors.New("same x-opencode-session with different stable first user messages generated the same fingerprint")
	}
	return writeJSONAtomic(filepath.Join(runDir, "affinity-smoke.json"), map[string]any{"status": "passed", "completed_at": utcNow(), "observations": observations})
}

func runCacheMigration(runDir, runID, baseURL, databasePath, outerDatabasePath, redisURL string, requestCount int) (returnErr error) {
	if requestCount < 13 {
		requestCount = 13
	}
	state, err := loadState(runDir)
	if err != nil {
		return err
	}
	attempt := 1
	if step := state.Steps["cache-migration-low-rpm"]; step != nil && step.Attempt > 0 {
		attempt = step.Attempt
	}
	channelIDs, err := openCodeGoTestChannelIDs(databasePath, "ocg-affinity-lower-e2e")
	if err != nil {
		return err
	}
	if len(channelIDs) != 3 {
		return fmt.Errorf("expected exactly 3 enabled OpenCodeGo channels in ocg-affinity-lower-e2e, found %v", channelIDs)
	}
	if strings.TrimSpace(redisURL) == "" {
		return errors.New("cache migration requires the dedicated test Redis URL")
	}
	if err := cleanupChannelRedis(redisURL, channelIDs, true); err != nil {
		return fmt.Errorf("reset exact OpenCodeGo test RPM keys: %w", err)
	}
	token, err := loadProvisionedSecret(runDir, "ocg-e2e-customer-1")
	if err != nil {
		return err
	}
	config := loadConfig{
		RunDir: runDir, RunID: runID, Scenario: "cache-migration-low-rpm", BaseURL: baseURL,
		Attempt: attempt,
		Tokens:  []namedToken{{Name: "customer-1", Value: token}}, Model: defaultModel,
		PromptCacheKeys: []string{cacheMigrationPromptKey(runID, attempt)},
		StablePrefix:    strings.Repeat("Stable OpenCodeGo cache migration context. ", 900), MaxTokens: 1,
		RealUpstream: true,
	}
	if err := checkLoadBudget(runDir, loadConfig{RunDir: runDir, Count: requestCount + 1, Model: config.Model, StablePrefix: config.StablePrefix, MaxTokens: 1, RealUpstream: true, PromptCacheKeys: config.PromptCacheKeys}); err != nil {
		return err
	}
	writer, err := newDurableJSONLWriter(filepath.Join(runDir, "requests.ndjson"))
	if err != nil {
		return err
	}
	defer writer.Close()
	client := &http.Client{Timeout: 90 * time.Second}
	transitions := make([]cacheTransition, 0, requestCount+1)
	channels := make([]int, 0)
	retryWait := 0
	defer func() {
		if len(transitions) == 0 {
			return
		}
		if err := writeCacheTransitions(runDir, transitions); err != nil && returnErr == nil {
			returnErr = err
		}
		status := "passed"
		errorText := ""
		if returnErr != nil {
			status = "failed"
			errorText = sanitizePreview(returnErr.Error())
		}
		summary := map[string]any{
			"status": status, "attempt": attempt, "completed_at": utcNow(),
			"channel_order": channels, "transitions": transitions, "error": errorText,
		}
		attemptPath := filepath.Join(runDir, fmt.Sprintf("cache-migration-attempt-%d-summary.json", attempt))
		if err := writeJSONAtomic(attemptPath, summary); err != nil && returnErr == nil {
			returnErr = err
		}
		if err := writeJSONAtomic(filepath.Join(runDir, "cache-migration-summary.json"), summary); err != nil && returnErr == nil {
			returnErr = err
		}
		_, _ = rebuildBudget(runDir)
	}()
	for index := 0; index < requestCount; index++ {
		before, err := latestLogID(databasePath)
		if err != nil {
			return err
		}
		record := performRequest(context.Background(), client, config, index, config.Tokens[0])
		var logErr error
		if record.Status >= 200 && record.Status < 300 {
			observation, observationErr := latestOpenCodeGoObservationAcrossDatabases(databasePath, outerDatabasePath, before, record.RequestID)
			logErr = observationErr
			if observationErr == nil {
				record.ChannelID = observation.ChannelID
				record.AffinityKeyFP = observation.KeyFP
				record.AffinitySource = observation.SourceType
				record.MigrationReason = observation.MigrationReason
			}
		}
		if err := writer.Write(record); err != nil {
			return err
		}
		if err := writer.Sync(); err != nil {
			return err
		}
		classification := classifyCache(record.CachedTokens, record.PromptTokens)
		transitions = append(transitions, cacheTransition{Attempt: attempt, Sequence: index + 1, ChannelID: record.ChannelID, CachedTokens: record.CachedTokens, PromptTokens: record.PromptTokens, CacheRatio: cacheRatio(record.CachedTokens, record.PromptTokens), Classification: classification, MigrationReason: record.MigrationReason, Status: record.Status})
		if record.ChannelID > 0 && (len(channels) == 0 || channels[len(channels)-1] != record.ChannelID) {
			channels = append(channels, record.ChannelID)
		}
		if record.Status == http.StatusTooManyRequests {
			retryWait = parseRetrySeconds(record.RetryAfter)
			if retryWait < 1 {
				return fmt.Errorf("controlled 429 did not include a valid Retry-After header; body=%s", record.ErrorPreview)
			}
			if retryWait > 70 {
				retryWait = 70
			}
			break
		}
		if logErr != nil {
			return logErr
		}
	}
	if retryWait > 0 {
		time.Sleep(time.Duration(retryWait+1) * time.Second)
		before, err := latestLogID(databasePath)
		if err != nil {
			return err
		}
		record := performRequest(context.Background(), client, config, len(transitions), config.Tokens[0])
		var logErr error
		if record.Status >= 200 && record.Status < 300 {
			observation, observationErr := latestOpenCodeGoObservationAcrossDatabases(databasePath, outerDatabasePath, before, record.RequestID)
			logErr = observationErr
			if observationErr == nil {
				record.ChannelID = observation.ChannelID
				record.AffinityKeyFP = observation.KeyFP
				record.AffinitySource = observation.SourceType
				record.MigrationReason = observation.MigrationReason
			}
		}
		if err := writer.Write(record); err != nil {
			return err
		}
		if err := writer.Sync(); err != nil {
			return err
		}
		transitions = append(transitions, cacheTransition{Attempt: attempt, Sequence: len(transitions) + 1, ChannelID: record.ChannelID, CachedTokens: record.CachedTokens, PromptTokens: record.PromptTokens, CacheRatio: cacheRatio(record.CachedTokens, record.PromptTokens), Classification: classifyCache(record.CachedTokens, record.PromptTokens), MigrationReason: record.MigrationReason, Status: record.Status})
		if record.Status < 200 || record.Status >= 300 {
			return fmt.Errorf("request did not recover after Retry-After; status=%d", record.Status)
		}
		if logErr != nil {
			return logErr
		}
	}
	if retryWait == 0 {
		return errors.New("cache migration did not reach the expected all-accounts-saturated 429")
	}
	if err := writer.Sync(); err != nil {
		return err
	}
	if len(channels) < 3 {
		return fmt.Errorf("expected migration across 3 channels, observed %v", channels)
	}
	for _, channelID := range channels[:3] {
		if !channelHasWarmFollowup(transitions, channelID) {
			return fmt.Errorf("channel %d did not become warm on a follow-up request", channelID)
		}
	}
	return nil
}

func cacheMigrationPromptKey(runID string, attempt int) string {
	return fmt.Sprintf("%s-cache-migration-a%d", runID, attempt)
}

func openCodeGoTestChannelIDs(databasePath, group string) ([]int, error) {
	db, err := openReadOnlySQLite(databasePath)
	if err != nil {
		return nil, err
	}
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		defer sqlDB.Close()
	}
	var rows []struct {
		ID    int
		Group string
	}
	if err := db.Raw("SELECT id, `group` FROM channels WHERE type=99 AND status=1 ORDER BY id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(rows))
	for _, row := range rows {
		for _, candidate := range strings.Split(row.Group, ",") {
			if strings.TrimSpace(candidate) == group {
				ids = append(ids, row.ID)
				break
			}
		}
	}
	return ids, nil
}

func classifyCache(cached, prompt int) string {
	ratio := cacheRatio(cached, prompt)
	switch {
	case ratio >= 0.8:
		return "hot"
	case ratio >= 0.1:
		return "partial"
	default:
		return "cold"
	}
}

func channelHasWarmFollowup(transitions []cacheTransition, channelID int) bool {
	seen := 0
	for _, transition := range transitions {
		if transition.ChannelID != channelID || transition.Status < 200 || transition.Status >= 300 {
			continue
		}
		seen++
		if seen >= 2 && transition.Classification == "hot" {
			return true
		}
	}
	return false
}

func parseRetrySeconds(value string) int {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
		return seconds
	}
	if parsed, err := http.ParseTime(value); err == nil {
		return int(time.Until(parsed).Seconds()) + 1
	}
	return 0
}

func writeCacheTransitions(runDir string, transitions []cacheTransition) error {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	_ = writer.Write([]string{"attempt", "sequence", "channel_id", "status", "cached_tokens", "prompt_tokens", "cache_ratio", "classification", "migration_reason"})
	for _, transition := range transitions {
		_ = writer.Write([]string{strconv.Itoa(transition.Attempt), strconv.Itoa(transition.Sequence), strconv.Itoa(transition.ChannelID), strconv.Itoa(transition.Status), strconv.Itoa(transition.CachedTokens), strconv.Itoa(transition.PromptTokens), fmt.Sprintf("%.6f", transition.CacheRatio), transition.Classification, transition.MigrationReason})
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return err
	}
	return writeBytesAtomic(filepath.Join(runDir, "cache-transitions.csv"), buffer.Bytes(), 0o600)
}

func runThreeCustomerLoad(runDir, runID, baseURL, databasePath, redisURL string, count int, duration time.Duration, concurrency int) (loadSummary, error) {
	tokens := make([]namedToken, 0, 3)
	keys := make([]string, 0, 3)
	for index := 1; index <= 3; index++ {
		name := fmt.Sprintf("ocg-e2e-customer-%d", index)
		value, err := loadProvisionedSecret(runDir, name)
		if err != nil {
			return loadSummary{}, err
		}
		tokens = append(tokens, namedToken{Name: fmt.Sprintf("customer-%d", index), Value: value})
		keys = append(keys, fmt.Sprintf("%s-customer-%d", runID, index))
	}
	beforeLogID, err := latestLogID(databasePath)
	if err != nil {
		return loadSummary{}, err
	}
	now := time.Now()
	wait := time.Until(now.Truncate(time.Minute).Add(time.Minute).Add(time.Second))
	if wait > 0 {
		time.Sleep(wait)
	}
	summary, err := runLoad(context.Background(), loadConfig{RunDir: runDir, RunID: runID, Scenario: "three-customer-4800-rpm", BaseURL: baseURL, Tokens: tokens, Count: count, Duration: duration, Concurrency: concurrency, Model: defaultModel, PromptCacheKeys: keys, MaxTokens: 1, RealUpstream: true})
	if err != nil {
		return summary, err
	}
	if summary.Sent != count {
		return summary, fmt.Errorf("planned %d requests but sent %d", count, summary.Sent)
	}
	if summary.ConnectionErrors > 0 || summary.UpstreamOrUnknown429 > 0 {
		return summary, fmt.Errorf("load had connection_errors=%d upstream_or_unknown_429=%d", summary.ConnectionErrors, summary.UpstreamOrUnknown429)
	}
	if summary.SendRateErrorPercent > 1 {
		return summary, fmt.Errorf("three-customer send-rate error %.3f%% exceeded 1%%", summary.SendRateErrorPercent)
	}
	validation, err := validateThreeCustomerLogs(databasePath, beforeLogID)
	if err != nil {
		return summary, err
	}
	if len(validation.KeyFingerprints) < 3 {
		return summary, fmt.Errorf("expected at least 3 independent affinity fingerprints, observed %v", validation.KeyFingerprints)
	}
	for channelID, requests := range validation.ChannelRequests {
		if requests > 1500 {
			return summary, fmt.Errorf("channel %d received %d successful relay attempts, above 1500 soft capacity", channelID, requests)
		}
	}
	redisSnapshots := make([]channelRedisSnapshot, 0, len(validation.ChannelRequests))
	for channelID := range validation.ChannelRequests {
		snapshot, snapshotErr := snapshotChannelRedis(redisURL, channelID)
		if snapshotErr != nil {
			return summary, snapshotErr
		}
		redisSnapshots = append(redisSnapshots, snapshot)
	}
	if err := writeJSONAtomic(filepath.Join(runDir, "three-customer-validation.json"), map[string]any{
		"completed_at": utcNow(), "load": summary, "logs": validation, "redis": redisSnapshots,
	}); err != nil {
		return summary, err
	}
	return summary, nil
}

func runDualTopologySmoke(runDir, runID, upperBaseURL, lowerBaseURL, lowerDatabasePath, pat string) error {
	if err := checkLoadBudget(runDir, loadConfig{RunDir: runDir, Count: 1, Model: defaultModel, MaxTokens: 1, RealUpstream: true}); err != nil {
		return err
	}
	tokenValue, err := loadProvisionedSecret(runDir, "ocg-e2e-customer-1")
	if err != nil {
		return err
	}
	before, err := latestLogID(lowerDatabasePath)
	if err != nil {
		return err
	}
	config := loadConfig{RunDir: runDir, RunID: runID, Scenario: "dual-instance-smoke", BaseURL: upperBaseURL, Tokens: []namedToken{{Name: "customer-1", Value: tokenValue}}, Model: defaultModel, PromptCacheKeys: []string{runID + "-dual-smoke"}, MaxTokens: 1, RealUpstream: true}
	record := performRequest(context.Background(), &http.Client{Timeout: 90 * time.Second}, config, 0, config.Tokens[0])
	// The outer and lower request IDs live in separate SQLite databases. The
	// isolated lower instance receives only this probe, so correlate by the
	// lower log boundary instead of the outer response request ID.
	observation, err := latestOpenCodeGoObservation(lowerDatabasePath, before)
	if err != nil {
		_ = appendJSONLine(filepath.Join(runDir, "requests.ndjson"), record)
		_, _ = rebuildBudget(runDir)
		return err
	}
	record.ChannelID = observation.ChannelID
	record.AffinityKeyFP = observation.KeyFP
	record.AffinitySource = observation.SourceType
	if record.Status < 200 || record.Status >= 300 || record.ChannelID <= 0 || record.AffinityKeyFP == "" {
		return fmt.Errorf("dual-instance relay did not produce a trusted OpenCodeGo observation: %+v", record)
	}
	writer, err := newDurableJSONLWriter(filepath.Join(runDir, "requests.ndjson"))
	if err != nil {
		return err
	}
	if err := writer.Write(record); err != nil {
		writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	upperClient, err := newAPIClient(upperBaseURL, pat)
	if err != nil {
		return err
	}
	lowerClient, err := newAPIClient(lowerBaseURL, pat)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	upperMetrics, err := upperClient.request(ctx, http.MethodGet, "/api/option/channel_affinity_cache", nil)
	if err != nil {
		return err
	}
	lowerMetrics, err := lowerClient.request(ctx, http.MethodGet, "/api/option/channel_affinity_cache", nil)
	if err != nil {
		return err
	}
	upperText, lowerText := string(upperMetrics.Data), string(lowerMetrics.Data)
	if !strings.Contains(upperText, `"scope":"node"`) || !strings.Contains(upperText, `"node_name":"ocg-upper-test"`) {
		return errors.New("upper metrics do not report scope=node and node_name=ocg-upper-test")
	}
	if !strings.Contains(lowerText, `"scope":"node"`) || !strings.Contains(lowerText, `"node_name":"ocg-lower-test"`) {
		return errors.New("lower metrics do not report scope=node and node_name=ocg-lower-test")
	}
	_, _ = rebuildBudget(runDir)
	return writeJSONAtomic(filepath.Join(runDir, "dual-instance-smoke.json"), map[string]any{
		"completed_at": utcNow(), "request": record,
		"upper_metrics": upperMetrics.Data, "lower_metrics": lowerMetrics.Data,
	})
}

type threeCustomerLogValidation struct {
	ChannelRequests map[int]int `json:"channel_requests"`
	KeyFingerprints []string    `json:"key_fingerprints"`
	Upstream429     int         `json:"upstream_429"`
}

func validateThreeCustomerLogs(databasePath string, afterLogID int64) (threeCustomerLogValidation, error) {
	db, err := openReadOnlySQLite(databasePath)
	if err != nil {
		return threeCustomerLogValidation{}, err
	}
	var rows []struct {
		ChannelID int
		Other     string
	}
	if err := db.Raw("SELECT channel_id,other FROM logs WHERE id>? AND model_name=?", afterLogID, defaultModel).Scan(&rows).Error; err != nil {
		return threeCustomerLogValidation{}, err
	}
	result := threeCustomerLogValidation{ChannelRequests: map[int]int{}}
	fingerprints := map[string]bool{}
	for _, row := range rows {
		var other map[string]any
		if rootcommon.UnmarshalJsonStr(row.Other, &other) != nil || intFromAny(other["pricing_channel_type"]) != 99 {
			continue
		}
		channelID := row.ChannelID
		if admin, ok := other["admin_info"].(map[string]any); ok {
			if affinity, ok := admin["channel_affinity"].(map[string]any); ok {
				if finalID := intFromAny(affinity["final_channel_id"]); finalID > 0 {
					channelID = finalID
				}
				if fp := stringFromAny(affinity["key_fp"]); fp != "" {
					fingerprints[fp] = true
				}
			}
		}
		if channelID > 0 {
			result.ChannelRequests[channelID]++
		}
	}
	for fp := range fingerprints {
		result.KeyFingerprints = append(result.KeyFingerprints, fp)
	}
	sort.Strings(result.KeyFingerprints)
	return result, nil
}

func hardTokenForChannel(databasePath, runDir string, channelID int) (namedToken, error) {
	db, err := openReadOnlySQLite(databasePath)
	if err != nil {
		return namedToken{}, err
	}
	var group string
	if err := db.Raw("SELECT `group` FROM channels WHERE id=? AND type=99", channelID).Scan(&group).Error; err != nil {
		return namedToken{}, err
	}
	for _, item := range []struct{ group, token string }{
		{"ocg-hardlimit-a", "ocg-e2e-hard-a"},
		{"ocg-hardlimit-b", "ocg-e2e-hard-b"},
		{"ocg-hardlimit-c", "ocg-e2e-hard-c"},
	} {
		if !csvSet(group)[item.group] {
			continue
		}
		value, err := loadProvisionedSecret(runDir, item.token)
		if err != nil {
			return namedToken{}, err
		}
		return namedToken{Name: item.token, Value: value}, nil
	}
	return namedToken{}, fmt.Errorf("channel %d is not mapped to exactly one ocg-hardlimit group", channelID)
}

func runHardLimitAndPost429(runDir, runID, outerBaseURL, lowerBaseURL, databasePath, redisURL string) error {
	outerTokenValue, err := loadProvisionedSecret(runDir, "ocg-e2e-customer-1")
	if err != nil {
		return err
	}
	outerToken := namedToken{Name: "customer-1", Value: outerTokenValue}
	cacheConfig := loadConfig{
		RunDir: runDir, RunID: runID, Scenario: "hard-limit-cache-warm", BaseURL: outerBaseURL,
		Tokens: []namedToken{outerToken}, Model: defaultModel,
		PromptCacheKeys: []string{runID + "-hard-limit-cache"},
		StablePrefix:    strings.Repeat("Stable pre-429 cache migration context. ", 900), MaxTokens: 1, RealUpstream: true,
	}
	if err := checkLoadBudget(runDir, loadConfig{RunDir: runDir, Count: 2 + 1550 + 1650 + 1750 + 5, Model: defaultModel, StablePrefix: cacheConfig.StablePrefix, MaxTokens: 1, RealUpstream: true, PromptCacheKeys: cacheConfig.PromptCacheKeys}); err != nil {
		return err
	}
	client := &http.Client{Timeout: 90 * time.Second}
	requestWriter, err := newDurableJSONLWriter(filepath.Join(runDir, "requests.ndjson"))
	if err != nil {
		return err
	}
	defer requestWriter.Close()
	warmRecords := make([]requestRecord, 0, 2)
	var targetChannelID int
	for index := 0; index < 2; index++ {
		before, err := latestLogID(databasePath)
		if err != nil {
			return err
		}
		record := performRequest(context.Background(), client, cacheConfig, index, outerToken)
		observation, err := latestOpenCodeGoObservation(databasePath, before, record.RequestID)
		if err != nil {
			return err
		}
		record.ChannelID = observation.ChannelID
		record.AffinityKeyFP = observation.KeyFP
		record.AffinitySource = observation.SourceType
		warmRecords = append(warmRecords, record)
		if err := requestWriter.Write(record); err != nil {
			return err
		}
		if err := requestWriter.Sync(); err != nil {
			return err
		}
		if index == 0 {
			targetChannelID = observation.ChannelID
		} else if observation.ChannelID != targetChannelID {
			return fmt.Errorf("cache warm-up migrated unexpectedly from channel %d to %d", targetChannelID, observation.ChannelID)
		}
	}
	if classifyCache(warmRecords[1].CachedTokens, warmRecords[1].PromptTokens) != "hot" {
		return fmt.Errorf("target channel %d did not become hot before the hard-limit test", targetChannelID)
	}
	if err := requestWriter.Sync(); err != nil {
		return err
	}
	if _, err := rebuildBudget(runDir); err != nil {
		return err
	}
	hardToken, err := hardTokenForChannel(databasePath, runDir, targetChannelID)
	if err != nil {
		return err
	}
	if err := poisonRPMKey(redisURL, targetChannelID, true); err != nil {
		return err
	}
	poisoned := true
	defer func() {
		if poisoned {
			_ = cleanupChannelRedis(redisURL, []int{targetChannelID}, false)
		}
	}()
	baseline, err := runLoad(context.Background(), loadConfig{
		RunDir: runDir, RunID: runID, Scenario: "hard-limit-1550", BaseURL: lowerBaseURL,
		Tokens: []namedToken{hardToken}, Count: 1550, Duration: 58 * time.Second, Concurrency: 128,
		Model: defaultModel, PromptCacheKeys: []string{runID + "-hard-load"}, MaxTokens: 1, RealUpstream: true,
	})
	if err != nil {
		return err
	}
	if baseline.UpstreamOrUnknown429 > 0 || baseline.LocalGuard429 > 0 {
		return fmt.Errorf("1550-RPM baseline encountered %d 429 responses; account may have external traffic", baseline.UpstreamOrUnknown429+baseline.LocalGuard429)
	}
	if baseline.SendRateErrorPercent > 1 {
		return fmt.Errorf("1550-RPM baseline send-rate error %.3f%% exceeded 1%%", baseline.SendRateErrorPercent)
	}
	time.Sleep(70 * time.Second)
	if err := poisonRPMKey(redisURL, targetChannelID, true); err != nil {
		return err
	}
	over, err := runLoad(context.Background(), loadConfig{
		RunDir: runDir, RunID: runID, Scenario: "hard-limit-1650", BaseURL: lowerBaseURL,
		Tokens: []namedToken{hardToken}, Count: 1650, Duration: 58 * time.Second, Concurrency: 128,
		Model: defaultModel, PromptCacheKeys: []string{runID + "-hard-load"}, MaxTokens: 1, RealUpstream: true, StopOn429: true,
	})
	if err != nil {
		return err
	}
	if over.UpstreamOrUnknown429 == 0 && over.LocalGuard429 == 0 {
		time.Sleep(70 * time.Second)
		if err := poisonRPMKey(redisURL, targetChannelID, true); err != nil {
			return err
		}
		over, err = runLoad(context.Background(), loadConfig{
			RunDir: runDir, RunID: runID, Scenario: "hard-limit-1750", BaseURL: lowerBaseURL,
			Tokens: []namedToken{hardToken}, Count: 1750, Duration: 58 * time.Second, Concurrency: 128,
			Model: defaultModel, PromptCacheKeys: []string{runID + "-hard-load"}, MaxTokens: 1, RealUpstream: true, StopOn429: true,
		})
		if err != nil {
			return err
		}
	}
	if over.UpstreamOrUnknown429 == 0 && over.LocalGuard429 == 0 {
		return errors.New("OpenCodeGo hard limit was not reproduced at up to 1750 RPM; result is PARTIAL")
	}
	preCleanupSnapshot, _ := snapshotChannelRedis(redisURL, targetChannelID)
	if err := cleanupChannelRedis(redisURL, []int{targetChannelID}, false); err != nil {
		return err
	}
	poisoned = false
	postRecords := make([]requestRecord, 0, 5)
	for index := 0; index < 2; index++ {
		before, err := latestLogID(databasePath)
		if err != nil {
			return err
		}
		record := performRequest(context.Background(), client, cacheConfig, index+2, outerToken)
		observation, err := latestOpenCodeGoObservation(databasePath, before, record.RequestID)
		if err != nil {
			return err
		}
		record.ChannelID = observation.ChannelID
		record.MigrationReason = observation.MigrationReason
		postRecords = append(postRecords, record)
		if err := requestWriter.Write(record); err != nil {
			return err
		}
		if err := requestWriter.Sync(); err != nil {
			return err
		}
	}
	if postRecords[0].ChannelID == targetChannelID {
		return fmt.Errorf("post-429 request remained on cooled channel %d", targetChannelID)
	}
	if classifyCache(postRecords[1].CachedTokens, postRecords[1].PromptTokens) != "hot" {
		return fmt.Errorf("migrated channel %d did not rewarm on its second request", postRecords[1].ChannelID)
	}
	waitSeconds := int(math.Ceil(float64(preCleanupSnapshot.CooldownMilliseconds) / 1000))
	if waitSeconds > 0 && waitSeconds <= 60 {
		time.Sleep(time.Duration(waitSeconds+1) * time.Second)
	}
	for index := 0; index < 3; index++ {
		before, _ := latestLogID(databasePath)
		record := performRequest(context.Background(), client, cacheConfig, index+4, outerToken)
		if observation, observationErr := latestOpenCodeGoObservation(databasePath, before, record.RequestID); observationErr == nil {
			record.ChannelID = observation.ChannelID
		}
		postRecords = append(postRecords, record)
		if err := requestWriter.Write(record); err != nil {
			return err
		}
		if err := requestWriter.Sync(); err != nil {
			return err
		}
	}
	for _, record := range postRecords[2:] {
		if record.ChannelID != postRecords[0].ChannelID {
			return fmt.Errorf("affinity did not remain on migrated channel %d after cooldown; observed %d", postRecords[0].ChannelID, record.ChannelID)
		}
	}
	if err := requestWriter.Sync(); err != nil {
		return err
	}
	_, _ = rebuildBudget(runDir)
	return writeJSONAtomic(filepath.Join(runDir, "hard-limit-post429-summary.json"), map[string]any{
		"status": "passed", "completed_at": utcNow(), "target_channel_id": targetChannelID,
		"baseline": baseline, "over_limit": over, "cooldown_before_cleanup": preCleanupSnapshot,
		"warm_before_429": warmRecords, "after_429": postRecords,
	})
}

func sortedUniqueChannels(transitions []cacheTransition) []int {
	set := map[int]bool{}
	for _, transition := range transitions {
		if transition.ChannelID > 0 {
			set[transition.ChannelID] = true
		}
	}
	result := make([]int, 0, len(set))
	for channelID := range set {
		result = append(result, channelID)
	}
	sort.Ints(result)
	return result
}

func writeChannelTransitions(runDir string, records []requestRecord) error {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	_ = writer.Write([]string{"scenario", "attempt", "sequence", "client", "channel_id", "key_fp", "source", "migration_reason", "status"})
	for _, record := range records {
		_ = writer.Write([]string{record.Scenario, strconv.Itoa(record.Attempt), strconv.Itoa(record.Sequence), record.Client, strconv.Itoa(record.ChannelID), record.AffinityKeyFP, record.AffinitySource, record.MigrationReason, strconv.Itoa(record.Status)})
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return err
	}
	return writeBytesAtomic(filepath.Join(runDir, "channel-transitions.csv"), buffer.Bytes(), 0o600)
}
