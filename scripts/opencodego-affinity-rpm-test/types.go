package main

import (
	"encoding/json"
	"time"
)

const (
	toolVersion        = "1"
	defaultModel       = "deepseek-v4-flash"
	defaultMaxRequests = 10000
	defaultMaxUSD      = 10.0
)

type stepStatus string

const (
	statusPending     stepStatus = "pending"
	statusRunning     stepStatus = "running"
	statusPassed      stepStatus = "passed"
	statusFailed      stepStatus = "failed"
	statusAborted     stepStatus = "aborted"
	statusNeedsManual stepStatus = "needs_manual"
	statusSkipped     stepStatus = "skipped"
)

type stepState struct {
	Step        string     `json:"step"`
	Status      stepStatus `json:"status"`
	Attempt     int        `json:"attempt"`
	StartedAt   string     `json:"started_at,omitempty"`
	CompletedAt string     `json:"completed_at,omitempty"`
	InputHash   string     `json:"input_hash,omitempty"`
	Artifacts   []string   `json:"artifacts,omitempty"`
	Error       string     `json:"error,omitempty"`
}

type runState struct {
	Version   string                `json:"version"`
	RunID     string                `json:"run_id"`
	CreatedAt string                `json:"created_at"`
	UpdatedAt string                `json:"updated_at"`
	Steps     map[string]*stepState `json:"steps"`
}

type budgetState struct {
	MaxRequests           int     `json:"max_requests"`
	MaxEstimatedUSD       float64 `json:"max_estimated_usd"`
	Requests              int     `json:"requests"`
	EstimatedUSD          float64 `json:"estimated_usd"`
	CarryoverRequests     int     `json:"carryover_requests,omitempty"`
	CarryoverEstimatedUSD float64 `json:"carryover_estimated_usd,omitempty"`
	LastRebuiltAtUTC      string  `json:"last_rebuilt_at_utc,omitempty"`
}

type budgetCarryover struct {
	SourceRunID  string  `json:"source_run_id"`
	Requests     int     `json:"requests"`
	EstimatedUSD float64 `json:"estimated_usd"`
	ImportedAt   string  `json:"imported_at"`
}

type manifest struct {
	RunID          string            `json:"run_id"`
	ToolVersion    string            `json:"tool_version"`
	CreatedAt      string            `json:"created_at"`
	GitCommit      string            `json:"git_commit,omitempty"`
	GitBranch      string            `json:"git_branch,omitempty"`
	GitDirty       bool              `json:"git_dirty"`
	DatabasePath   string            `json:"database_path,omitempty"`
	DatabaseSHA256 string            `json:"database_sha256,omitempty"`
	BinaryPath     string            `json:"binary_path,omitempty"`
	BinarySHA256   string            `json:"binary_sha256,omitempty"`
	ServiceURL     string            `json:"service_url,omitempty"`
	ServiceHealthy bool              `json:"service_healthy"`
	Model          string            `json:"model"`
	Environment    map[string]string `json:"environment,omitempty"`
}

type channelInventory struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Type      int    `json:"type"`
	Status    int    `json:"status"`
	Group     string `json:"group"`
	Models    string `json:"models"`
	Priority  int    `json:"priority"`
	Weight    int    `json:"weight"`
	RPMLimit  int    `json:"rpm_limit"`
	KeyHash8  string `json:"key_hash_8,omitempty"`
	HasRawKey bool   `json:"-"`
	BaseURL   string `json:"base_url,omitempty"`
	Settings  string `json:"-"`
}

type inventoryResult struct {
	CapturedAt        string             `json:"captured_at"`
	SQLiteIntegrity   string             `json:"sqlite_integrity"`
	LatestLogID       int64              `json:"latest_log_id"`
	OpenCodeGo        []channelInventory `json:"opencodego_channels"`
	NewAPIChannels    []channelInventory `json:"new_api_channels"`
	AffinityOptions   map[string]string  `json:"affinity_options"`
	ModelRateLimit    map[string]string  `json:"model_rate_limit"`
	ManualBlockers    []string           `json:"manual_blockers,omitempty"`
	ServiceStatusCode int                `json:"service_status_code,omitempty"`
}

type requestRecord struct {
	RunID               string  `json:"run_id"`
	Scenario            string  `json:"scenario"`
	Attempt             int     `json:"attempt,omitempty"`
	Sequence            int     `json:"sequence"`
	Client              string  `json:"client,omitempty"`
	StartedAt           string  `json:"started_at"`
	DurationMS          int64   `json:"duration_ms"`
	Status              int     `json:"status"`
	RetryAfter          string  `json:"retry_after,omitempty"`
	RequestID           string  `json:"request_id,omitempty"`
	CachedTokens        int     `json:"cached_tokens,omitempty"`
	PromptTokens        int     `json:"prompt_tokens,omitempty"`
	PromptCacheMiss     int     `json:"prompt_cache_miss_tokens,omitempty"`
	ChannelID           int     `json:"channel_id,omitempty"`
	AffinityKeyFP       string  `json:"affinity_key_fp,omitempty"`
	AffinitySource      string  `json:"affinity_source,omitempty"`
	MigrationReason     string  `json:"migration_reason,omitempty"`
	LocalGuard429       bool    `json:"local_guard_429,omitempty"`
	RealUpstream        bool    `json:"real_upstream"`
	EstimatedCostUSD    float64 `json:"estimated_cost_usd,omitempty"`
	ErrorClassification string  `json:"error_classification,omitempty"`
	ErrorPreview        string  `json:"error_preview,omitempty"`
}

type cacheTransition struct {
	Attempt         int     `json:"attempt"`
	Sequence        int     `json:"sequence"`
	ChannelID       int     `json:"channel_id"`
	CachedTokens    int     `json:"cached_tokens"`
	PromptTokens    int     `json:"prompt_tokens"`
	CacheRatio      float64 `json:"cache_ratio"`
	Classification  string  `json:"classification"`
	MigrationReason string  `json:"migration_reason,omitempty"`
	Status          int     `json:"status"`
}

type apiEnvelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type optionItem struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type optionChange struct {
	Key             string `json:"key"`
	Before          string `json:"before"`
	TestValue       string `json:"test_value"`
	ProductionValue string `json:"production_value"`
	Reason          string `json:"reason"`
	RestartRequired bool   `json:"restart_required"`
	Rollback        string `json:"rollback"`
	ResponseSuccess bool   `json:"response_success"`
	ResponseMessage string `json:"response_message,omitempty"`
	ChangedAt       string `json:"changed_at"`
	RunID           string `json:"run_id"`
}

type loadConfig struct {
	RunDir          string
	RunID           string
	Scenario        string
	Attempt         int
	BaseURL         string
	Tokens          []namedToken
	Count           int
	Duration        time.Duration
	Concurrency     int
	Model           string
	PromptCacheKeys []string
	StablePrefix    string
	MaxTokens       int
	RealUpstream    bool
	StopOn429       bool
}

type namedToken struct {
	Name  string
	Value string
}

type loadSummary struct {
	Scenario             string      `json:"scenario"`
	Planned              int         `json:"planned"`
	Sent                 int         `json:"sent"`
	StatusCounts         map[int]int `json:"status_counts"`
	ConnectionErrors     int         `json:"connection_errors"`
	LocalGuard429        int         `json:"local_guard_429"`
	UpstreamOrUnknown429 int         `json:"upstream_or_unknown_429"`
	ElapsedSeconds       float64     `json:"elapsed_seconds"`
	AchievedRPM          float64     `json:"achieved_rpm"`
	PlannedRPM           float64     `json:"planned_rpm"`
	SendRateErrorPercent float64     `json:"send_rate_error_percent"`
	StoppedOn429         bool        `json:"stopped_on_429"`
	EstimatedCostUSD     float64     `json:"estimated_cost_usd"`
}
