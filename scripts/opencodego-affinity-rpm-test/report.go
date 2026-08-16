package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func generateReport(runDir string) error {
	state, err := loadState(runDir)
	if err != nil {
		return err
	}
	verdict := "PARTIAL"
	hasFailed := false
	hasBlocked := false
	allCriticalPassed := true
	critical := map[string]bool{
		"inventory": true, "code-validation": true, "mock-selftest": true, "redis-validation": true,
		"verify-channels": true, "mock-gateway-e2e": true, "affinity-smoke": true, "cache-migration-low-rpm": true,
		"dual-instance": true, "hard-limit": true, "post429-cache-migration": true,
		"three-customer-4800-rpm": true,
	}
	for name, step := range state.Steps {
		if step.Status == statusFailed {
			hasFailed = true
		}
		if step.Status == statusNeedsManual {
			hasBlocked = true
		}
		if critical[name] && step.Status != statusPassed {
			allCriticalPassed = false
		}
	}
	switch {
	case hasFailed:
		verdict = "FAIL"
	case hasBlocked:
		verdict = "BLOCKED"
	case allCriticalPassed:
		verdict = "PASS"
	}
	budget, _ := rebuildBudget(runDir)
	records, _ := readJSONLines[requestRecord](filepath.Join(runDir, "requests.ndjson"))
	if err := writeChannelTransitions(runDir, records); err != nil {
		return err
	}
	sanitizedEvents := make([]map[string]any, 0)
	for _, record := range records {
		if record.ErrorClassification == "" && record.Status < 400 {
			continue
		}
		sanitizedEvents = append(sanitizedEvents, map[string]any{
			"started_at": record.StartedAt, "scenario": record.Scenario,
			"attempt": record.Attempt, "sequence": record.Sequence, "status": record.Status,
			"request_id": record.RequestID, "channel_id": record.ChannelID,
			"classification": record.ErrorClassification,
			"preview":        sanitizePreview(record.ErrorPreview),
		})
	}
	if err := writeJSONAtomic(filepath.Join(runDir, "sanitized-logs.json"), sanitizedEvents); err != nil {
		return err
	}
	changes := make([]optionChange, 0)
	if err := readJSONFile(filepath.Join(runDir, "configuration-changes.json"), &changes); err == nil {
		latest := map[string]string{}
		for _, change := range changes {
			if change.ResponseSuccess {
				latest[change.Key] = change.TestValue
			}
		}
		if err := writeJSONAtomic(filepath.Join(runDir, "config-after.json"), map[string]any{"captured_at": utcNow(), "recorded_values": latest}); err != nil {
			return err
		}
	} else if os.IsNotExist(err) {
		if err := writeJSONAtomic(filepath.Join(runDir, "config-after.json"), map[string]any{"captured_at": utcNow(), "recorded_values": map[string]string{}}); err != nil {
			return err
		}
	} else {
		return err
	}
	statusCounts := map[int]int{}
	upstream429 := 0
	local429 := 0
	for _, record := range records {
		statusCounts[record.Status]++
		if record.Status == 429 {
			if record.LocalGuard429 {
				local429++
			} else {
				upstream429++
			}
		}
	}
	stepNames := append([]string(nil), orderedSteps...)
	var summary strings.Builder
	fmt.Fprintf(&summary, "# OpenCodeGo affinity/RPM test report\n\nVerdict: **%s**\n\n", verdict)
	fmt.Fprintf(&summary, "- Run ID: `%s`\n- Generated: `%s`\n- Real upstream requests: %d / %d\n- Estimated cost: $%.6f / $%.2f\n- Local guard 429: %d\n- Upstream or unknown 429: %d\n\n", state.RunID, utcNow(), budget.Requests, budget.MaxRequests, budget.EstimatedUSD, budget.MaxEstimatedUSD, local429, upstream429)
	if budget.CarryoverRequests > 0 || budget.CarryoverEstimatedUSD > 0 {
		fmt.Fprintf(&summary, "- Budget carryover: %d requests / $%.6f\n\n", budget.CarryoverRequests, budget.CarryoverEstimatedUSD)
	}
	summary.WriteString("## Step status\n\n| Step | Status | Attempt | Error |\n|---|---|---:|---|\n")
	for _, name := range stepNames {
		step := state.Steps[name]
		fmt.Fprintf(&summary, "| `%s` | %s | %d | %s |\n", name, step.Status, step.Attempt, strings.ReplaceAll(step.Error, "|", "\\|"))
	}
	summary.WriteString("\n## HTTP status counts\n\n")
	statuses := make([]int, 0, len(statusCounts))
	for status := range statusCounts {
		statuses = append(statuses, status)
	}
	sort.Ints(statuses)
	for _, status := range statuses {
		fmt.Fprintf(&summary, "- `%d`: %d\n", status, statusCounts[status])
	}
	if err := writeBytesAtomic(filepath.Join(runDir, "summary.md"), []byte(summary.String()), 0o600); err != nil {
		return err
	}
	var failures strings.Builder
	failures.WriteString("# Failures, blockers, and partial results\n\n")
	for _, name := range stepNames {
		step := state.Steps[name]
		if step.Status == statusPassed || step.Status == statusPending {
			continue
		}
		fmt.Fprintf(&failures, "- `%s`: **%s** — %s\n", name, step.Status, step.Error)
	}
	return writeBytesAtomic(filepath.Join(runDir, "failures.md"), []byte(failures.String()), 0o600)
}

func writeRollbackScript(runDir, databasePath, binaryPath string) error {
	backupDir := filepath.Join(runDir, "backup")
	content := fmt.Sprintf(`param([switch]$Confirm)
$ErrorActionPreference = 'Stop'
if (-not $Confirm) { throw 'Rollback overwrites the test database. Stop the exact upper/lower test PIDs, verify the paths below, then re-run with -Confirm.' }
$targetDb = '%s'
$backupDb = '%s'
$targetBinary = '%s'
$backupBinary = '%s'
if (-not (Test-Path -LiteralPath $backupDb)) { throw "Backup database not found: $backupDb" }
Copy-Item -LiteralPath $backupDb -Destination $targetDb -Force
if ($targetBinary -and (Test-Path -LiteralPath $backupBinary)) {
  Copy-Item -LiteralPath $backupBinary -Destination $targetBinary -Force
}
Write-Host 'Rollback files restored. Start the original service manually and verify /api/status.'
`, powerShellLiteral(databasePath), powerShellLiteral(filepath.Join(backupDir, "one-api.db")), powerShellLiteral(binaryPath), powerShellLiteral(filepath.Join(backupDir, "new-api.exe")))
	return writeBytesAtomic(filepath.Join(runDir, "rollback.ps1"), []byte(content), 0o600)
}

func powerShellLiteral(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func appendFailure(runDir, message string) {
	file, err := os.OpenFile(filepath.Join(runDir, "failures.md"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = fmt.Fprintf(file, "- %s — %s\n", utcNow(), message)
}
