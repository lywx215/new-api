package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	rootcommon "github.com/QuantumNous/new-api/common"
)

type mockAccount struct {
	name       string
	rpmLimit   int64
	retryMode  string
	count      atomic.Int64
	mu         sync.Mutex
	cache      map[string]bool
	windowUsed int64
	blockedTil time.Time
	stateFile  string
	headerLeak atomic.Int64
}

type mockState struct {
	Account                  string `json:"account"`
	Requests                 int64  `json:"requests"`
	CachedKeys               int    `json:"cached_keys"`
	InternalAffinityReceived int64  `json:"internal_affinity_received"`
	UpdatedAt                string `json:"updated_at"`
}

func newMockAccount(name string, rpmLimit int, retryMode, stateFile string) *mockAccount {
	return &mockAccount{name: name, rpmLimit: int64(rpmLimit), retryMode: retryMode, cache: map[string]bool{}, stateFile: stateFile}
}

func (mock *mockAccount) reset() {
	mock.count.Store(0)
	mock.headerLeak.Store(0)
	mock.mu.Lock()
	mock.cache = map[string]bool{}
	mock.windowUsed = 0
	mock.blockedTil = time.Time{}
	mock.mu.Unlock()
}

func (mock *mockAccount) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/health" {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"ok":true}`))
		return
	}
	if request.Method != http.MethodPost {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if request.Header.Get("X-NewAPI-Affinity-Key") != "" {
		mock.headerLeak.Add(1)
	}
	sequence := mock.count.Add(1)
	now := time.Now()
	mock.mu.Lock()
	if !mock.blockedTil.IsZero() && !now.Before(mock.blockedTil) {
		mock.windowUsed = 0
		mock.blockedTil = time.Time{}
	}
	limited := !mock.blockedTil.IsZero() && now.Before(mock.blockedTil)
	if !limited {
		mock.windowUsed++
		if mock.rpmLimit > 0 && mock.windowUsed > mock.rpmLimit {
			limited = true
			mock.blockedTil = now.Add(10 * time.Second)
		}
	}
	retryUntil := mock.blockedTil
	mock.mu.Unlock()
	if limited {
		if mock.retryMode == "date" {
			writer.Header().Set("Retry-After", retryUntil.UTC().Format(http.TimeFormat))
		} else {
			writer.Header().Set("Retry-After", "10")
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":{"message":"mock account RPM limit","type":"rate_limit_error"}}`))
		mock.persistState()
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, 2*1024*1024))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	var payload struct {
		Model          string `json:"model"`
		PromptCacheKey string `json:"prompt_cache_key"`
	}
	_ = rootcommon.Unmarshal(body, &payload)
	key := strings.TrimSpace(payload.PromptCacheKey)
	if key == "" {
		key = hash8(string(body))
	}
	mock.mu.Lock()
	warm := mock.cache[key]
	mock.cache[key] = true
	mock.mu.Unlock()
	cached := 0
	miss := 7290
	if warm {
		cached = 7168
		miss = 122
	}
	response := map[string]any{
		"id":     "mock-" + mock.name + "-" + strconv.FormatInt(sequence, 10),
		"object": "chat.completion", "model": payload.Model,
		"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": "OK"}, "finish_reason": "stop"}},
		"usage": map[string]any{
			"prompt_tokens": 7290, "completion_tokens": 1, "total_tokens": 7291,
			"prompt_cache_hit_tokens": cached, "prompt_cache_miss_tokens": miss,
			"prompt_tokens_details": map[string]int{"cached_tokens": cached},
		},
	}
	encoded, _ := rootcommon.Marshal(response)
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("X-Mock-Account", mock.name)
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(encoded)
	if sequence%25 == 0 || sequence <= 2 {
		mock.persistState()
	}
}

func (mock *mockAccount) persistState() {
	if mock.stateFile == "" {
		return
	}
	mock.mu.Lock()
	cachedKeys := len(mock.cache)
	mock.mu.Unlock()
	_ = writeJSONAtomic(mock.stateFile, mockState{Account: mock.name, Requests: mock.count.Load(), CachedKeys: cachedKeys, InternalAffinityReceived: mock.headerLeak.Load(), UpdatedAt: utcNow()})
}

func runMockServer(listen, account string, rpm int, retryMode, stateFile string) error {
	if strings.TrimSpace(account) == "" {
		return errors.New("mock account name is required")
	}
	if retryMode != "seconds" && retryMode != "date" {
		return errors.New("retry mode must be seconds or date")
	}
	mock := newMockAccount(account, rpm, retryMode, stateFile)
	server := &http.Server{Addr: listen, Handler: mock, ReadHeaderTimeout: 5 * time.Second}
	fmt.Printf("mock account %s listening on %s with RPM limit %d\n", account, listen, rpm)
	return server.ListenAndServe()
}

func mockSelfTest(runDir string) error {
	accounts := []*mockAccount{
		newMockAccount("a", 1600, "seconds", ""),
		newMockAccount("b", 1, "date", ""),
		newMockAccount("c", 1600, "seconds", ""),
	}
	servers := make([]*httptest.Server, 0, len(accounts))
	for _, account := range accounts {
		server := httptest.NewServer(account)
		servers = append(servers, server)
		defer server.Close()
	}
	client := &http.Client{Timeout: 5 * time.Second}
	body, _ := rootcommon.Marshal(map[string]any{"model": defaultModel, "prompt_cache_key": "mock-cache-isolation", "messages": []map[string]string{{"role": "user", "content": "test"}}, "max_tokens": 1})
	firstCached, err := sendMockProbe(client, servers[0].URL, body)
	if err != nil || firstCached != 0 {
		return fmt.Errorf("mock account A first request cache = %d, err=%v", firstCached, err)
	}
	secondCached, err := sendMockProbe(client, servers[0].URL, body)
	if err != nil || secondCached < 7000 {
		return fmt.Errorf("mock account A second request cache = %d, err=%v", secondCached, err)
	}
	bFirstCached, err := sendMockProbe(client, servers[1].URL, body)
	if err != nil || bFirstCached != 0 {
		return fmt.Errorf("mock cache leaked across accounts: B first cache=%d err=%v", bFirstCached, err)
	}
	dateResponse, err := client.Post(servers[1].URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"test"}`))
	if err != nil {
		return err
	}
	dateResponse.Body.Close()
	if dateResponse.StatusCode != http.StatusTooManyRequests {
		return fmt.Errorf("date Retry-After mock returned %d", dateResponse.StatusCode)
	}
	if _, err := http.ParseTime(dateResponse.Header.Get("Retry-After")); err != nil {
		return fmt.Errorf("date Retry-After is invalid: %w", err)
	}
	// Two requests already consumed account A; requests 3..1600 must pass.
	for index := 2; index < 1600; index++ {
		response, requestErr := client.Post(servers[0].URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"test"}`))
		if requestErr != nil {
			return requestErr
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("mock request %d returned %d before the limit", index+1, response.StatusCode)
		}
	}
	response, err := client.Post(servers[0].URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"test"}`))
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests || response.Header.Get("Retry-After") != "10" {
		return fmt.Errorf("mock request 1601 returned %d Retry-After=%q", response.StatusCode, response.Header.Get("Retry-After"))
	}
	result := map[string]any{
		"status": "passed", "completed_at": utcNow(), "exact_limit": 1600,
		"request_1601_status": response.StatusCode, "cache_isolated": true,
		"retry_after_seconds": true, "retry_after_http_date": true,
		"account_a_second_cached_tokens": secondCached, "account_b_first_cached_tokens": bFirstCached,
	}
	return writeJSONAtomic(filepath.Join(runDir, "mock-selftest.json"), result)
}

func runMockGatewayE2E(runDir, runID, baseURL, pat, databasePath, redisURL string, apply bool) error {
	accounts := []*mockAccount{
		newMockAccount("gateway-a", 2, "seconds", ""),
		newMockAccount("gateway-b", 2, "date", ""),
		newMockAccount("gateway-c", 2, "seconds", ""),
	}
	servers := make([]*httptest.Server, 0, len(accounts))
	urls := make([]string, 0, len(accounts))
	for _, account := range accounts {
		server := httptest.NewServer(account)
		servers = append(servers, server)
		urls = append(urls, server.URL)
		defer server.Close()
	}
	channelIDs, err := provisionMockGatewayChannels(runDir, baseURL, pat, urls, apply)
	if err != nil {
		return err
	}
	if err := cleanupChannelRedis(redisURL, channelIDs, true); err != nil {
		return err
	}
	state, err := loadState(runDir)
	if err != nil {
		return err
	}
	attempt := 1
	if step := state.Steps["mock-gateway-e2e"]; step != nil && step.Attempt > 0 {
		attempt = step.Attempt
	}
	token, err := loadProvisionedSecret(runDir, "ocg-e2e-mock-customer")
	if err != nil {
		return err
	}
	config := loadConfig{
		RunDir: runDir, RunID: runID, Scenario: "mock-gateway-e2e", Attempt: attempt,
		BaseURL: baseURL, Tokens: []namedToken{{Name: "mock-customer", Value: token}},
		Model: defaultModel, PromptCacheKeys: []string{fmt.Sprintf("%s-mock-gateway-a%d", runID, attempt)},
		StablePrefix: strings.Repeat("Mock gateway cache isolation context. ", 900), MaxTokens: 1,
		RealUpstream: false,
	}
	client := &http.Client{Timeout: 30 * time.Second}
	readinessConfig := config
	readinessConfig.Scenario = "mock-gateway-readiness"
	readinessConfig.PromptCacheKeys = []string{fmt.Sprintf("%s-mock-gateway-readiness-a%d", runID, attempt)}
	readinessDeadline := time.Now().Add(45 * time.Second)
	for sequence := 0; ; sequence++ {
		probe := performRequest(context.Background(), client, readinessConfig, sequence, readinessConfig.Tokens[0])
		if probe.Status >= 200 && probe.Status < 300 {
			break
		}
		if probe.Status != http.StatusServiceUnavailable || !strings.Contains(probe.ErrorPreview, "model_not_found") {
			return fmt.Errorf("Mock gateway readiness probe returned %d: %s", probe.Status, probe.ErrorPreview)
		}
		if time.Now().After(readinessDeadline) {
			return errors.New("Mock gateway channels did not become available in the in-memory cache within 45 seconds")
		}
		time.Sleep(250 * time.Millisecond)
	}
	for _, account := range accounts {
		account.reset()
	}
	if err := cleanupChannelRedis(redisURL, channelIDs, true); err != nil {
		return err
	}
	writer, err := newDurableJSONLWriter(filepath.Join(runDir, "requests.ndjson"))
	if err != nil {
		return err
	}
	defer writer.Close()
	transitions := make([]cacheTransition, 0, 9)
	channelOrder := make([]int, 0, 3)
	retryWait := 0
	for index := 0; index < 9; index++ {
		before, err := latestLogID(databasePath)
		if err != nil {
			return err
		}
		record := performRequest(context.Background(), client, config, index, config.Tokens[0])
		if record.Status >= 200 && record.Status < 300 {
			observation, observationErr := latestOpenCodeGoObservation(databasePath, before, record.RequestID)
			if observationErr != nil {
				return observationErr
			}
			if observation.FinalChannelID > 0 && observation.FinalChannelID != observation.ChannelID {
				return fmt.Errorf("Mock gateway affinity telemetry final_channel_id=%d does not match actual channel_id=%d", observation.FinalChannelID, observation.ChannelID)
			}
			record.ChannelID = observation.ChannelID
			record.AffinityKeyFP = observation.KeyFP
			record.AffinitySource = observation.SourceType
			record.MigrationReason = observation.MigrationReason
			if len(channelOrder) == 0 || channelOrder[len(channelOrder)-1] != record.ChannelID {
				channelOrder = append(channelOrder, record.ChannelID)
			}
		}
		if err := writer.Write(record); err != nil {
			return err
		}
		if err := writer.Sync(); err != nil {
			return err
		}
		transitions = append(transitions, cacheTransition{
			Attempt: attempt, Sequence: index + 1, ChannelID: record.ChannelID,
			CachedTokens: record.CachedTokens, PromptTokens: record.PromptTokens,
			CacheRatio:      cacheRatio(record.CachedTokens, record.PromptTokens),
			Classification:  classifyCache(record.CachedTokens, record.PromptTokens),
			MigrationReason: record.MigrationReason, Status: record.Status,
		})
		if record.Status == http.StatusTooManyRequests {
			if !record.LocalGuard429 {
				return fmt.Errorf("Mock gateway returned an unclassified 429: %s", record.ErrorPreview)
			}
			retryWait = parseRetrySeconds(record.RetryAfter)
			if retryWait < 1 {
				return errors.New("Mock gateway controlled 429 did not preserve Retry-After")
			}
			break
		}
		if record.Status < 200 || record.Status >= 300 {
			return fmt.Errorf("Mock gateway request %d returned %d: %s", index+1, record.Status, record.ErrorPreview)
		}
	}
	if len(channelOrder) != 3 {
		return fmt.Errorf("Mock gateway expected 3 channel migrations, observed %v", channelOrder)
	}
	for _, channelID := range channelOrder {
		if !channelHasWarmFollowup(transitions, channelID) {
			return fmt.Errorf("Mock gateway channel %d did not become warm before migration", channelID)
		}
	}
	if retryWait < 1 {
		return errors.New("Mock gateway did not reach controlled all-account saturation")
	}
	cooldowns := make([]channelRedisSnapshot, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		snapshot, snapshotErr := snapshotChannelRedis(redisURL, channelID)
		if snapshotErr != nil {
			return snapshotErr
		}
		cooldowns = append(cooldowns, snapshot)
		if snapshot.CooldownMilliseconds <= 0 {
			return fmt.Errorf("Mock gateway channel %d did not enter cooldown after upstream 429", channelID)
		}
	}
	for _, account := range accounts {
		if account.headerLeak.Load() != 0 {
			return fmt.Errorf("internal affinity header leaked to Mock account %s", account.name)
		}
		if account.count.Load() < 3 {
			return fmt.Errorf("Mock account %s did not receive the expected upstream 429 probe", account.name)
		}
	}
	time.Sleep(time.Duration(retryWait+1) * time.Second)
	recovery := performRequest(context.Background(), client, config, len(transitions), config.Tokens[0])
	if err := writer.Write(recovery); err != nil {
		return err
	}
	if recovery.Status < 200 || recovery.Status >= 300 {
		return fmt.Errorf("Mock gateway did not recover after Retry-After: status=%d", recovery.Status)
	}
	if err := cleanupChannelRedis(redisURL, channelIDs, true); err != nil {
		return err
	}
	for _, channelID := range channelIDs {
		if err := poisonRPMKey(redisURL, channelID, true); err != nil {
			return err
		}
	}
	defer cleanupChannelRedis(redisURL, channelIDs, true)
	failOpenConfig := config
	failOpenConfig.PromptCacheKeys = []string{fmt.Sprintf("%s-mock-gateway-failopen-a%d", runID, attempt)}
	failOpen := performRequest(context.Background(), client, failOpenConfig, len(transitions)+1, failOpenConfig.Tokens[0])
	if err := writer.Write(failOpen); err != nil {
		return err
	}
	if failOpen.Status < 200 || failOpen.Status >= 300 {
		return fmt.Errorf("Mock gateway Redis failure did not fail open: status=%d", failOpen.Status)
	}
	return writeJSONAtomic(filepath.Join(runDir, "mock-gateway-e2e.json"), map[string]any{
		"status": "passed", "attempt": attempt, "completed_at": utcNow(),
		"channel_order": channelOrder, "transitions": transitions,
		"cooldowns_before_wait": cooldowns, "recovery": recovery, "redis_fail_open": failOpen,
	})
}

func sendMockProbe(client *http.Client, baseURL string, body []byte) (int, error) {
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/v1/chat/completions", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, err
	}
	var payload struct {
		Usage struct {
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := rootcommon.Unmarshal(data, &payload); err != nil {
		return 0, err
	}
	return payload.Usage.PromptTokensDetails.CachedTokens, nil
}
