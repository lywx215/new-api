package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type needsManualError struct{ message string }

func (err needsManualError) Error() string { return err.message }

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}
	if err := runCommand(os.Args[1], os.Args[2:]); err != nil {
		var manual needsManualError
		if errors.As(err, &manual) {
			fmt.Fprintln(os.Stderr, manual.Error())
			os.Exit(20)
		}
		if errors.Is(err, errStepAlreadyPassed) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(10)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("OpenCodeGo affinity/RPM resumable test tool")
	fmt.Println("commands: init, state, next, carry-budget, prepare-runtime-secrets, inventory, backup, verify-channels, redis-probe, mock-server, mock-selftest, mock-gateway-e2e, api-snapshot, apply-profile, provision-groups, provision-tokens, provision-loopback, affinity-smoke, cache-migration, dual-smoke, load, hard-limit, three-customer, redis-poison, redis-cleanup, redis-snapshot, live-gate, report")
}

func runCommand(command string, args []string) error {
	switch command {
	case "init":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir := flags.String("run-dir", "", "run artifact directory")
		runID := flags.String("run-id", "", "stable run identifier")
		if err := flags.Parse(args); err != nil {
			return err
		}
		return initRun(*runDir, *runID)
	case "state":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir := flags.String("run-dir", "", "run artifact directory")
		step := flags.String("step", "", "step name")
		status := flags.String("status", "", "new status")
		inputHash := flags.String("input-hash", "", "input hash")
		errorMessage := flags.String("error", "", "error or blocker")
		artifacts := flags.String("artifacts", "", "comma-separated artifact paths")
		if err := flags.Parse(args); err != nil {
			return err
		}
		return updateStep(*runDir, *step, stepStatus(*status), *inputHash, *errorMessage, splitNonEmpty(*artifacts))
	case "next":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir := flags.String("run-dir", "", "run artifact directory")
		if err := flags.Parse(args); err != nil {
			return err
		}
		next, err := nextIncompleteStep(*runDir)
		if err == nil {
			fmt.Println(next)
		}
		return err
	case "carry-budget":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir := flags.String("run-dir", "", "target run directory")
		fromRunDir := flags.String("from-run-dir", "", "completed source run directory")
		if err := flags.Parse(args); err != nil {
			return err
		}
		return carryBudget(*runDir, *fromRunDir)
	case "prepare-runtime-secrets":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir := flags.String("run-dir", "", "run directory")
		databasePath := flags.String("db", "one-api.db", "SQLite database")
		redisURL := flags.String("redis-url", "", "test Redis URL")
		apply := flags.Bool("apply", false, "authorize local secret generation and root PAT rotation")
		if err := flags.Parse(args); err != nil {
			return err
		}
		return prepareRuntimeSecrets(*runDir, *databasePath, *redisURL, *apply)
	case "inventory":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir := flags.String("run-dir", "", "run artifact directory")
		runID := flags.String("run-id", "", "run identifier")
		db := flags.String("db", "one-api.db", "SQLite database")
		binary := flags.String("binary", "", "running binary")
		baseURL := flags.String("base-url", "http://127.0.0.1:3000", "service URL")
		if err := flags.Parse(args); err != nil {
			return err
		}
		result, err := captureInventory(*runDir, *runID, *db, *binary, *baseURL)
		if err != nil {
			return err
		}
		fmt.Printf("SQLite=%s OpenCodeGo=%d blockers=%d\n", result.SQLiteIntegrity, len(result.OpenCodeGo), len(result.ManualBlockers))
		return nil
	case "backup":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir := flags.String("run-dir", "", "run directory")
		db := flags.String("db", "one-api.db", "SQLite database")
		binary := flags.String("binary", "", "running binary")
		if err := flags.Parse(args); err != nil {
			return err
		}
		return createBackup(*runDir, *db, *binary)
	case "verify-channels":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		db := flags.String("db", "one-api.db", "SQLite database")
		runDir := flags.String("run-dir", "", "run directory")
		if err := flags.Parse(args); err != nil {
			return err
		}
		issues, err := verifyLiveChannelLayout(*db)
		if err != nil {
			return err
		}
		_ = writeJSONAtomic(filepath.Join(*runDir, "channel-layout.json"), map[string]any{"captured_at": utcNow(), "issues": issues})
		if len(issues) > 0 {
			return needsManualError{strings.Join(issues, "; ")}
		}
		return nil
	case "redis-probe":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir := flags.String("run-dir", "", "run directory")
		redisURL := flags.String("redis-url", "", "Redis URL")
		allowRemote := flags.Bool("allow-remote", false, "allow remote probe")
		if err := flags.Parse(args); err != nil {
			return err
		}
		_, err := probeRedis(*runDir, *redisURL, *allowRemote)
		return err
	case "mock-server":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		listen := flags.String("listen", "127.0.0.1:39001", "listen address")
		account := flags.String("account", "a", "account name")
		rpm := flags.Int("rpm", 1600, "hard RPM")
		retryMode := flags.String("retry-mode", "seconds", "seconds or date")
		stateFile := flags.String("state-file", "", "state output")
		if err := flags.Parse(args); err != nil {
			return err
		}
		return runMockServer(*listen, *account, *rpm, *retryMode, *stateFile)
	case "mock-selftest":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir := flags.String("run-dir", "", "run directory")
		if err := flags.Parse(args); err != nil {
			return err
		}
		return mockSelfTest(*runDir)
	case "mock-gateway-e2e":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir, baseURL, patEnv := commonAPIFlags(flags)
		runID := flags.String("run-id", "", "run identifier")
		db := flags.String("db", "one-api.db", "SQLite database")
		redisURL := flags.String("redis-url", "", "test Redis URL")
		apply := flags.Bool("apply", false, "authorize Mock channel provisioning")
		if err := flags.Parse(args); err != nil {
			return err
		}
		pat, err := requiredEnvironment(*patEnv)
		if err != nil {
			return needsManualError{err.Error()}
		}
		return runMockGatewayE2E(*runDir, *runID, *baseURL, pat, *db, *redisURL, *apply)
	case "api-snapshot":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir, baseURL, patEnv := commonAPIFlags(flags)
		if err := flags.Parse(args); err != nil {
			return err
		}
		pat, err := requiredEnvironment(*patEnv)
		if err != nil {
			return needsManualError{err.Error()}
		}
		return snapshotAPI(*runDir, *baseURL, pat)
	case "apply-profile":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir, baseURL, patEnv := commonAPIFlags(flags)
		runID := flags.String("run-id", "", "run identifier")
		profile := flags.String("profile", "single-low", "profile")
		apply := flags.Bool("apply", false, "authorize mutation")
		if err := flags.Parse(args); err != nil {
			return err
		}
		pat, err := requiredEnvironment(*patEnv)
		if err != nil {
			return needsManualError{err.Error()}
		}
		return applyProfile(*runDir, *runID, *baseURL, pat, *profile, *apply)
	case "provision-groups":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir, baseURL, patEnv := commonAPIFlags(flags)
		runID := flags.String("run-id", "", "run identifier")
		apply := flags.Bool("apply", false, "authorize mutation")
		if err := flags.Parse(args); err != nil {
			return err
		}
		pat, err := requiredEnvironment(*patEnv)
		if err != nil {
			return needsManualError{err.Error()}
		}
		return mergeTestGroups(*runDir, *runID, *baseURL, pat, *apply)
	case "provision-tokens":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir, baseURL, patEnv := commonAPIFlags(flags)
		apply := flags.Bool("apply", false, "authorize mutation")
		if err := flags.Parse(args); err != nil {
			return err
		}
		pat, err := requiredEnvironment(*patEnv)
		if err != nil {
			return needsManualError{err.Error()}
		}
		_, err = provisionTokens(*runDir, *baseURL, pat, *apply)
		return err
	case "provision-loopback":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir, baseURL, patEnv := commonAPIFlags(flags)
		upstream := flags.String("upstream-base-url", "http://127.0.0.1:3000", "loopback target")
		apply := flags.Bool("apply", false, "authorize mutation")
		if err := flags.Parse(args); err != nil {
			return err
		}
		pat, err := requiredEnvironment(*patEnv)
		if err != nil {
			return needsManualError{err.Error()}
		}
		return provisionLoopbackChannel(*runDir, *baseURL, pat, *upstream, *apply)
	case "affinity-smoke":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir, baseURL, patEnv := commonAPIFlags(flags)
		runID := flags.String("run-id", "", "run identifier")
		db := flags.String("db", "one-api.db", "SQLite database")
		if err := flags.Parse(args); err != nil {
			return err
		}
		pat, err := requiredEnvironment(*patEnv)
		if err != nil {
			return needsManualError{err.Error()}
		}
		return runAffinitySmoke(*runDir, *runID, *baseURL, pat, *db)
	case "cache-migration":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir := flags.String("run-dir", "", "run directory")
		runID := flags.String("run-id", "", "run identifier")
		baseURL := flags.String("base-url", "http://127.0.0.1:3000", "service URL")
		db := flags.String("db", "one-api.db", "SQLite database")
		outerDB := flags.String("outer-db", "", "upper SQLite database when the request crosses a type 60 instance")
		redisURL := flags.String("redis-url", "", "test Redis URL")
		count := flags.Int("count", 13, "requests")
		if err := flags.Parse(args); err != nil {
			return err
		}
		return runCacheMigration(*runDir, *runID, *baseURL, *db, *outerDB, *redisURL, *count)
	case "dual-smoke":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir, upperURL, patEnv := commonAPIFlags(flags)
		runID := flags.String("run-id", "", "run identifier")
		lowerURL := flags.String("lower-base-url", "http://127.0.0.1:3001", "lower URL")
		lowerDB := flags.String("lower-db", "", "lower SQLite database")
		if err := flags.Parse(args); err != nil {
			return err
		}
		pat, err := requiredEnvironment(*patEnv)
		if err != nil {
			return needsManualError{err.Error()}
		}
		return runDualTopologySmoke(*runDir, *runID, *upperURL, *lowerURL, *lowerDB, pat)
	case "load":
		return loadCommand(args)
	case "hard-limit":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir := flags.String("run-dir", "", "run directory")
		runID := flags.String("run-id", "", "run identifier")
		outer := flags.String("outer-base-url", "http://127.0.0.1:3000", "upper URL")
		lower := flags.String("lower-base-url", "http://127.0.0.1:3001", "lower URL")
		db := flags.String("db", "", "lower SQLite database")
		redisURL := flags.String("redis-url", "", "test Redis URL")
		confirm := flags.String("confirm", "", "must equal run ID")
		if err := flags.Parse(args); err != nil {
			return err
		}
		if *confirm != *runID {
			return needsManualError{"live hard-limit confirmation is missing or does not match the run ID"}
		}
		return runHardLimitAndPost429(*runDir, *runID, *outer, *lower, *db, *redisURL)
	case "three-customer":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir := flags.String("run-dir", "", "run directory")
		runID := flags.String("run-id", "", "run identifier")
		baseURL := flags.String("base-url", "http://127.0.0.1:3000", "upper URL")
		db := flags.String("db", "", "lower SQLite database")
		redisURL := flags.String("redis-url", "", "test Redis URL")
		count := flags.Int("count", 4800, "total requests")
		duration := flags.Duration("duration", 58*time.Second, "duration")
		concurrency := flags.Int("concurrency", 256, "concurrency")
		confirm := flags.String("confirm", "", "must equal run ID")
		if err := flags.Parse(args); err != nil {
			return err
		}
		if *confirm != *runID {
			return needsManualError{"three-customer live confirmation is missing or does not match the run ID"}
		}
		summary, err := runThreeCustomerLoad(*runDir, *runID, *baseURL, *db, *redisURL, *count, *duration, *concurrency)
		if err == nil {
			fmt.Printf("sent=%d achieved_rpm=%.2f local429=%d upstream429=%d\n", summary.Sent, summary.AchievedRPM, summary.LocalGuard429, summary.UpstreamOrUnknown429)
		}
		return err
	case "redis-poison":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		redisURL := flags.String("redis-url", "", "Redis URL")
		channelID := flags.Int("channel-id", 0, "channel ID")
		apply := flags.Bool("apply", false, "authorize mutation")
		if err := flags.Parse(args); err != nil {
			return err
		}
		return poisonRPMKey(*redisURL, *channelID, *apply)
	case "redis-cleanup":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		redisURL := flags.String("redis-url", "", "Redis URL")
		channelIDs := flags.String("channel-ids", "", "comma-separated channel IDs")
		cooldown := flags.Bool("include-cooldown", false, "delete exact cooldown keys")
		if err := flags.Parse(args); err != nil {
			return err
		}
		ids, err := parseInts(*channelIDs)
		if err != nil {
			return err
		}
		return cleanupChannelRedis(*redisURL, ids, *cooldown)
	case "redis-snapshot":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir := flags.String("run-dir", "", "run directory")
		redisURL := flags.String("redis-url", "", "Redis URL")
		channelID := flags.Int("channel-id", 0, "channel ID")
		if err := flags.Parse(args); err != nil {
			return err
		}
		snapshot, err := snapshotChannelRedis(*redisURL, *channelID)
		if err != nil {
			return err
		}
		return appendRedisSnapshot(*runDir, snapshot)
	case "live-gate":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir := flags.String("run-dir", "", "run directory")
		runID := flags.String("run-id", "", "run identifier")
		if err := flags.Parse(args); err != nil {
			return err
		}
		if os.Getenv("OCG_TEST_LIVE_CONFIRM") != *runID {
			return needsManualError{fmt.Sprintf("set OCG_TEST_LIVE_CONFIRM=%s after reviewing budget and target accounts", *runID)}
		}
		budget, err := rebuildBudget(*runDir)
		if err != nil {
			return err
		}
		return writeJSONAtomic(filepath.Join(*runDir, "live-gate.json"), map[string]any{"confirmed_at": utcNow(), "run_id": *runID, "budget": budget})
	case "report":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		runDir := flags.String("run-dir", "", "run directory")
		if err := flags.Parse(args); err != nil {
			return err
		}
		return generateReport(*runDir)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func commonAPIFlags(flags *flag.FlagSet) (*string, *string, *string) {
	return flags.String("run-dir", "", "run directory"), flags.String("base-url", "http://127.0.0.1:3000", "service URL"), flags.String("pat-env", "OCG_TEST_ROOT_PAT", "environment variable holding root PAT")
}

func requiredEnvironment(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("environment variable %s is required", name)
	}
	return value, nil
}

func splitNonEmpty(value string) []string {
	result := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func parseInts(value string) ([]int, error) {
	items := splitNonEmpty(value)
	result := make([]int, 0, len(items))
	for _, item := range items {
		parsed, err := strconv.Atoi(item)
		if err != nil {
			return nil, err
		}
		result = append(result, parsed)
	}
	return result, nil
}

func loadCommand(args []string) error {
	flags := flag.NewFlagSet("load", flag.ContinueOnError)
	runDir := flags.String("run-dir", "", "run directory")
	runID := flags.String("run-id", "", "run identifier")
	scenario := flags.String("scenario", "manual-load", "scenario")
	baseURL := flags.String("base-url", "http://127.0.0.1:3000", "service URL")
	tokenNames := flags.String("token-names", "ocg-e2e-customer-1", "provisioned token names")
	count := flags.Int("count", 1, "request count")
	duration := flags.Duration("duration", time.Second, "duration")
	concurrency := flags.Int("concurrency", 1, "concurrency")
	promptKeys := flags.String("prompt-cache-keys", "", "comma-separated keys")
	stablePrefix := flags.Bool("stable-prefix", false, "send cacheable long prefix")
	real := flags.Bool("real", false, "count against live budget")
	stop429 := flags.Bool("stop-on-429", false, "stop scheduling after 429")
	if err := flags.Parse(args); err != nil {
		return err
	}
	tokens := make([]namedToken, 0)
	for _, name := range splitNonEmpty(*tokenNames) {
		value, err := loadProvisionedSecret(*runDir, name)
		if err != nil {
			return err
		}
		tokens = append(tokens, namedToken{Name: name, Value: value})
	}
	prefix := ""
	if *stablePrefix {
		prefix = strings.Repeat("Stable OpenCodeGo cache validation context. ", 900)
	}
	summary, err := runLoad(context.Background(), loadConfig{RunDir: *runDir, RunID: *runID, Scenario: *scenario, BaseURL: *baseURL, Tokens: tokens, Count: *count, Duration: *duration, Concurrency: *concurrency, Model: defaultModel, PromptCacheKeys: splitNonEmpty(*promptKeys), StablePrefix: prefix, MaxTokens: 1, RealUpstream: *real, StopOn429: *stop429})
	if err == nil {
		fmt.Printf("sent=%d achieved_rpm=%.2f\n", summary.Sent, summary.AchievedRPM)
	}
	return err
}
