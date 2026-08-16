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
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	rootcommon "github.com/QuantumNous/new-api/common"
)

func normalizeToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "sk-") {
		return value
	}
	return "sk-" + value
}

func requestBody(model, promptCacheKey, stablePrefix string, maxTokens int, suffix string) ([]byte, error) {
	if maxTokens <= 0 {
		maxTokens = 1
	}
	system := "You are concise."
	if stablePrefix != "" {
		system = stablePrefix
	}
	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": "Reply with OK-" + suffix},
		},
		"max_tokens": maxTokens,
		"stream":     false,
	}
	if promptCacheKey != "" {
		payload["prompt_cache_key"] = promptCacheKey
	}
	return rootcommon.Marshal(payload)
}

type responseUsage struct {
	PromptTokens          int
	CachedTokens          int
	PromptCacheMissTokens int
}

func parseUsage(data []byte) responseUsage {
	var payload struct {
		Usage struct {
			PromptTokens          int `json:"prompt_tokens"`
			PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
			PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
			PromptTokensDetails   struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if rootcommon.Unmarshal(data, &payload) != nil {
		return responseUsage{}
	}
	cached := payload.Usage.PromptTokensDetails.CachedTokens
	if cached == 0 {
		cached = payload.Usage.PromptCacheHitTokens
	}
	return responseUsage{PromptTokens: payload.Usage.PromptTokens, CachedTokens: cached, PromptCacheMissTokens: payload.Usage.PromptCacheMissTokens}
}

func estimatedRequestCost(bodyBytes int, maxTokens int) float64 {
	inputTokens := max(1, bodyBytes/4)
	return (float64(inputTokens)*0.14 + float64(max(1, maxTokens))*0.28) / 1_000_000
}

func performRequest(ctx context.Context, client *http.Client, config loadConfig, sequence int, token namedToken) requestRecord {
	key := ""
	if len(config.PromptCacheKeys) > 0 {
		key = config.PromptCacheKeys[sequence%len(config.PromptCacheKeys)]
	}
	body, err := requestBody(config.Model, key, config.StablePrefix, config.MaxTokens, strconv.Itoa(sequence))
	record := requestRecord{
		RunID: config.RunID, Scenario: config.Scenario, Attempt: config.Attempt, Sequence: sequence + 1, Client: token.Name,
		StartedAt: utcNow(), RealUpstream: config.RealUpstream,
		EstimatedCostUSD: estimatedRequestCost(len(body), config.MaxTokens),
	}
	if err != nil {
		record.ErrorClassification = "request_encode_error"
		record.ErrorPreview = sanitizePreview(err.Error())
		return record
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(config.BaseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		record.ErrorClassification = "request_build_error"
		record.ErrorPreview = sanitizePreview(err.Error())
		return record
	}
	request.Header.Set("Authorization", "Bearer "+normalizeToken(token.Value))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "new-api-opencodego-affinity-rpm-test/1")
	started := time.Now()
	response, err := client.Do(request)
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
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if readErr != nil {
		record.ErrorClassification = "response_read_error"
		record.ErrorPreview = sanitizePreview(readErr.Error())
		return record
	}
	usage := parseUsage(data)
	record.PromptTokens = usage.PromptTokens
	record.CachedTokens = usage.CachedTokens
	record.PromptCacheMiss = usage.PromptCacheMissTokens
	if response.StatusCode == http.StatusTooManyRequests {
		bodyText := strings.ToLower(string(data))
		record.LocalGuard429 = strings.Contains(bodyText, "all opencodego accounts") || strings.Contains(bodyText, "rpm soft limit")
		if record.LocalGuard429 {
			record.ErrorClassification = "local_rpm_guard_429"
		} else {
			record.ErrorClassification = "upstream_or_unknown_429"
		}
		record.ErrorPreview = sanitizePreview(string(data))
	} else if response.StatusCode < 200 || response.StatusCode >= 300 {
		record.ErrorClassification = "http_error"
		record.ErrorPreview = sanitizePreview(string(data))
	}
	return record
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func rebuildBudget(runDir string) (budgetState, error) {
	budget := budgetState{MaxRequests: defaultMaxRequests, MaxEstimatedUSD: defaultMaxUSD}
	path := filepath.Join(runDir, "budget.json")
	if _, err := os.Stat(path); err == nil {
		if err := readJSONFile(path, &budget); err != nil {
			// The request ledger is authoritative and can rebuild a corrupt budget file.
			budget = budgetState{MaxRequests: defaultMaxRequests, MaxEstimatedUSD: defaultMaxUSD}
		}
	}
	records, err := readJSONLines[requestRecord](filepath.Join(runDir, "requests.ndjson"))
	if err != nil {
		return budget, err
	}
	carryover := budgetCarryover{}
	if err := readJSONFile(filepath.Join(runDir, "budget-carryover.json"), &carryover); err != nil && !os.IsNotExist(err) {
		return budget, err
	}
	budget.CarryoverRequests = carryover.Requests
	budget.CarryoverEstimatedUSD = carryover.EstimatedUSD
	budget.Requests = carryover.Requests
	budget.EstimatedUSD = carryover.EstimatedUSD
	for _, record := range records {
		if !record.RealUpstream {
			continue
		}
		budget.Requests++
		budget.EstimatedUSD += record.EstimatedCostUSD
	}
	budget.LastRebuiltAtUTC = utcNow()
	return budget, writeJSONAtomic(path, budget)
}

func carryBudget(runDir, sourceRunDir string) error {
	if filepath.Clean(runDir) == filepath.Clean(sourceRunDir) {
		return errors.New("budget carryover source must be a different run")
	}
	var sourceState runState
	if err := readJSONFile(filepath.Join(sourceRunDir, "state.json"), &sourceState); err != nil {
		return err
	}
	var sourceBudget budgetState
	if err := readJSONFile(filepath.Join(sourceRunDir, "budget.json"), &sourceBudget); err != nil {
		return err
	}
	carryover := budgetCarryover{
		SourceRunID:  sourceState.RunID,
		Requests:     sourceBudget.Requests,
		EstimatedUSD: sourceBudget.EstimatedUSD,
		ImportedAt:   utcNow(),
	}
	if err := writeJSONAtomic(filepath.Join(runDir, "budget-carryover.json"), carryover); err != nil {
		return err
	}
	_, err := rebuildBudget(runDir)
	return err
}

func checkLoadBudget(runDir string, config loadConfig) error {
	if !config.RealUpstream {
		return nil
	}
	budget, err := rebuildBudget(runDir)
	if err != nil {
		return err
	}
	sample, err := requestBody(config.Model, firstPromptKey(config), config.StablePrefix, config.MaxTokens, "budget")
	if err != nil {
		return err
	}
	plannedCost := float64(config.Count) * estimatedRequestCost(len(sample), config.MaxTokens)
	if budget.Requests+config.Count > budget.MaxRequests {
		return fmt.Errorf("request budget would be exceeded: used=%d planned=%d max=%d", budget.Requests, config.Count, budget.MaxRequests)
	}
	if budget.EstimatedUSD+plannedCost > budget.MaxEstimatedUSD {
		return fmt.Errorf("estimated cost budget would be exceeded: used=%.6f planned=%.6f max=%.2f", budget.EstimatedUSD, plannedCost, budget.MaxEstimatedUSD)
	}
	return nil
}

func firstPromptKey(config loadConfig) string {
	if len(config.PromptCacheKeys) == 0 {
		return ""
	}
	return config.PromptCacheKeys[0]
}

func runLoad(ctx context.Context, config loadConfig) (loadSummary, error) {
	if config.Count <= 0 || config.Duration <= 0 || config.Concurrency <= 0 {
		return loadSummary{}, errors.New("count, duration, and concurrency must be positive")
	}
	if len(config.Tokens) == 0 {
		return loadSummary{}, errors.New("at least one client token is required")
	}
	if err := checkLoadBudget(config.RunDir, config); err != nil {
		return loadSummary{}, err
	}
	writer, err := newDurableJSONLWriter(filepath.Join(config.RunDir, "requests.ndjson"))
	if err != nil {
		return loadSummary{}, err
	}
	defer writer.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = config.Concurrency * 2
	transport.MaxIdleConnsPerHost = config.Concurrency
	transport.MaxConnsPerHost = config.Concurrency
	client := &http.Client{Transport: transport, Timeout: 90 * time.Second}
	results := make(chan requestRecord, config.Concurrency*2)
	semaphore := make(chan struct{}, config.Concurrency)
	var workers sync.WaitGroup
	var stop atomic.Bool
	start := time.Now()
	interval := config.Duration / time.Duration(config.Count)
	schedulerDone := make(chan struct{})
	go func() {
		defer close(schedulerDone)
		for index := 0; index < config.Count; index++ {
			if stop.Load() || ctx.Err() != nil {
				return
			}
			due := start.Add(time.Duration(index) * interval)
			if wait := time.Until(due); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				return
			}
			workers.Add(1)
			token := config.Tokens[index%len(config.Tokens)]
			go func(sequence int, selected namedToken) {
				defer workers.Done()
				defer func() { <-semaphore }()
				record := performRequest(ctx, client, config, sequence, selected)
				if config.StopOn429 && record.Status == http.StatusTooManyRequests {
					stop.Store(true)
				}
				results <- record
			}(index, token)
		}
	}()
	go func() {
		<-schedulerDone
		workers.Wait()
		close(results)
	}()
	summary := loadSummary{Scenario: config.Scenario, Planned: config.Count, StatusCounts: map[int]int{}}
	records := make([]requestRecord, 0, config.Count)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case record, ok := <-results:
			if !ok {
				if err := writer.Sync(); err != nil {
					return summary, err
				}
				summary.ElapsedSeconds = time.Since(start).Seconds()
				summary.PlannedRPM = float64(config.Count) * 60 / config.Duration.Seconds()
				if len(records) > 1 {
					starts := make([]time.Time, 0, len(records))
					for _, record := range records {
						if parsed, parseErr := time.Parse(time.RFC3339Nano, record.StartedAt); parseErr == nil {
							starts = append(starts, parsed)
						}
					}
					sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
					if len(starts) > 1 {
						span := starts[len(starts)-1].Sub(starts[0]).Seconds()
						if span > 0 {
							summary.AchievedRPM = float64(len(starts)-1) * 60 / span
						}
					}
				}
				if summary.PlannedRPM > 0 {
					summary.SendRateErrorPercent = math.Abs(summary.AchievedRPM-summary.PlannedRPM) * 100 / summary.PlannedRPM
				}
				summary.StoppedOn429 = stop.Load()
				if err := writeLoadRPMCSV(config.RunDir, config.Scenario, records); err != nil {
					return summary, err
				}
				if err := writeJSONAtomic(filepath.Join(config.RunDir, "load-"+safeFileName(config.Scenario)+".json"), summary); err != nil {
					return summary, err
				}
				_, _ = rebuildBudget(config.RunDir)
				return summary, nil
			}
			if err := writer.Write(record); err != nil {
				return summary, err
			}
			records = append(records, record)
			summary.Sent++
			summary.StatusCounts[record.Status]++
			summary.EstimatedCostUSD += record.EstimatedCostUSD
			if record.ErrorClassification == "connection_error" {
				summary.ConnectionErrors++
			}
			if record.LocalGuard429 {
				summary.LocalGuard429++
			} else if record.Status == http.StatusTooManyRequests {
				summary.UpstreamOrUnknown429++
			}
		case <-ticker.C:
			if err := writer.Sync(); err != nil {
				return summary, err
			}
			_, _ = rebuildBudget(config.RunDir)
		case <-ctx.Done():
			stop.Store(true)
		}
	}
}

func safeFileName(value string) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}

func writeLoadRPMCSV(runDir, scenario string, records []requestRecord) error {
	bySecond := map[string]int{}
	for _, record := range records {
		parsed, err := time.Parse(time.RFC3339Nano, record.StartedAt)
		if err != nil {
			continue
		}
		bySecond[parsed.UTC().Format(time.RFC3339)]++
	}
	seconds := make([]string, 0, len(bySecond))
	for second := range bySecond {
		seconds = append(seconds, second)
	}
	sort.Strings(seconds)
	path := filepath.Join(runDir, "rpm-per-second.csv")
	existing := []byte{}
	if data, err := os.ReadFile(path); err == nil {
		existing = data
	}
	var buffer bytes.Buffer
	if len(existing) == 0 {
		buffer.WriteString("scenario,second,requests\n")
	} else {
		buffer.Write(existing)
		if existing[len(existing)-1] != '\n' {
			buffer.WriteByte('\n')
		}
	}
	writer := csv.NewWriter(&buffer)
	for _, second := range seconds {
		_ = writer.Write([]string{scenario, second, strconv.Itoa(bySecond[second])})
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return err
	}
	return writeBytesAtomic(path, buffer.Bytes(), 0o600)
}
