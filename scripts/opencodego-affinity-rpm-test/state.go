package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var orderedSteps = []string{
	"inventory",
	"code-validation",
	"mock-selftest",
	"redis-validation",
	"upgrade",
	"verify-channels",
	"api-snapshot",
	"provision-loopback",
	"mock-gateway-e2e",
	"affinity-smoke",
	"cache-migration-low-rpm",
	"redis-failopen",
	"dual-instance",
	"live-gate",
	"hard-limit",
	"post429-cache-migration",
	"three-customer-4800-rpm",
	"resume-tests",
	"report",
	"cleanup",
}

func initRun(runDir, runID string) error {
	if err := ensureRunDir(runDir); err != nil {
		return err
	}
	statePath := filepath.Join(runDir, "state.json")
	if _, err := os.Stat(statePath); err == nil {
		var existing runState
		if err := readJSONFile(statePath, &existing); err != nil {
			return err
		}
		if existing.RunID != runID {
			return fmt.Errorf("run directory belongs to %q, not %q", existing.RunID, runID)
		}
		return recoverInterruptedSteps(runDir, &existing)
	}
	now := utcNow()
	state := runState{Version: toolVersion, RunID: runID, CreatedAt: now, UpdatedAt: now, Steps: map[string]*stepState{}}
	for _, name := range orderedSteps {
		state.Steps[name] = &stepState{Step: name, Status: statusPending}
	}
	if err := writeJSONAtomic(statePath, state); err != nil {
		return err
	}
	budget := budgetState{MaxRequests: defaultMaxRequests, MaxEstimatedUSD: defaultMaxUSD}
	if err := writeJSONAtomic(filepath.Join(runDir, "budget.json"), budget); err != nil {
		return err
	}
	for _, name := range []string{"requests.ndjson", "rpm-per-second.csv", "channel-transitions.csv", "cache-transitions.csv", "failures.md"} {
		path := filepath.Join(runDir, name)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			return err
		}
	}
	if err := writeJSONAtomic(filepath.Join(runDir, "redis-snapshots.json"), []channelRedisSnapshot{}); err != nil {
		return err
	}
	return nil
}

func recoverInterruptedSteps(runDir string, state *runState) error {
	changed := false
	for _, step := range state.Steps {
		if step.Status == statusRunning {
			step.Status = statusAborted
			step.CompletedAt = utcNow()
			step.Error = "previous process ended while this step was running; high-rate scenarios must restart from a fresh window"
			changed = true
		}
	}
	if !changed {
		return nil
	}
	state.UpdatedAt = utcNow()
	return writeJSONAtomic(filepath.Join(runDir, "state.json"), state)
}

func loadState(runDir string) (*runState, error) {
	var state runState
	if err := readJSONFile(filepath.Join(runDir, "state.json"), &state); err != nil {
		return nil, err
	}
	if state.Version != toolVersion {
		return nil, fmt.Errorf("state version %s is not supported by tool version %s", state.Version, toolVersion)
	}
	return &state, nil
}

func updateStep(runDir, name string, status stepStatus, inputHash, errorMessage string, artifacts []string) error {
	state, err := loadState(runDir)
	if err != nil {
		return err
	}
	step, ok := state.Steps[name]
	if !ok {
		return fmt.Errorf("unknown step %q", name)
	}
	switch status {
	case statusRunning:
		if step.Status == statusPassed && inputHash != "" && step.InputHash == inputHash {
			return errStepAlreadyPassed
		}
		step.Attempt++
		step.StartedAt = utcNow()
		step.CompletedAt = ""
		step.Error = ""
		step.Artifacts = nil
	case statusPassed, statusFailed, statusAborted, statusNeedsManual, statusSkipped:
		step.CompletedAt = utcNow()
		step.Error = errorMessage
		step.Artifacts = append([]string(nil), artifacts...)
	default:
		return fmt.Errorf("unsupported step status %q", status)
	}
	step.Status = status
	if inputHash != "" {
		step.InputHash = inputHash
	}
	if errorMessage != "" && status == statusRunning {
		step.Error = errorMessage
	}
	if len(artifacts) > 0 && status == statusRunning {
		step.Artifacts = append([]string(nil), artifacts...)
	}
	state.UpdatedAt = utcNow()
	return writeJSONAtomic(filepath.Join(runDir, "state.json"), state)
}

var errStepAlreadyPassed = errors.New("step already passed with the same input")

func nextIncompleteStep(runDir string) (string, error) {
	state, err := loadState(runDir)
	if err != nil {
		return "", err
	}
	for _, name := range orderedSteps {
		if state.Steps[name].Status != statusPassed {
			return name, nil
		}
	}
	return "", nil
}
