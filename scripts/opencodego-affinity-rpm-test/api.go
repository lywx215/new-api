package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	rootcommon "github.com/QuantumNous/new-api/common"
)

type apiClient struct {
	baseURL string
	pat     string
	client  *http.Client
}

func newAPIClient(baseURL, pat string) (*apiClient, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("base URL is required")
	}
	if strings.TrimSpace(pat) == "" {
		return nil, errors.New("temporary root PAT is required")
	}
	return &apiClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		pat:     strings.TrimSpace(pat),
		client:  &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (client *apiClient) request(ctx context.Context, method, path string, payload any) (*apiEnvelope, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := rootcommon.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+client.pat)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024))
	if err != nil {
		return nil, err
	}
	var envelope apiEnvelope
	if err := rootcommon.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("%s %s returned HTTP %d and invalid JSON: %s", method, path, response.StatusCode, sanitizePreview(string(data)))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &envelope, fmt.Errorf("%s %s returned HTTP %d: %s", method, path, response.StatusCode, envelope.Message)
	}
	if !envelope.Success {
		return &envelope, fmt.Errorf("%s %s failed: %s", method, path, envelope.Message)
	}
	return &envelope, nil
}

func (client *apiClient) options(ctx context.Context) (map[string]string, error) {
	envelope, err := client.request(ctx, http.MethodGet, "/api/option/", nil)
	if err != nil {
		return nil, err
	}
	var items []optionItem
	if err := rootcommon.Unmarshal(envelope.Data, &items); err != nil {
		return nil, err
	}
	result := make(map[string]string, len(items))
	for _, item := range items {
		result[item.Key] = item.Value
	}
	return result, nil
}

func (client *apiClient) updateOption(ctx context.Context, key, value string) (*apiEnvelope, error) {
	return client.request(ctx, http.MethodPut, "/api/option/", map[string]any{"key": key, "value": value})
}

func snapshotAPI(runDir, baseURL, pat string) error {
	client, err := newAPIClient(baseURL, pat)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	options, err := client.options(ctx)
	if err != nil {
		return err
	}
	channels, err := client.request(ctx, http.MethodGet, "/api/channel/?p=1&page_size=1000", nil)
	if err != nil {
		return err
	}
	affinity, err := client.request(ctx, http.MethodGet, "/api/option/channel_affinity_cache", nil)
	if err != nil {
		return err
	}
	snapshot := map[string]any{
		"captured_at": utcNow(),
		"base_url":    baseURL,
		"options":     options,
		"channels":    channels.Data,
		"affinity":    affinity.Data,
	}
	return writeJSONAtomic(filepath.Join(runDir, "api-snapshot.json"), snapshot)
}

type settingProfile struct {
	Name    string
	Values  map[string]string
	Reasons map[string]string
}

func profileByName(name string) (settingProfile, error) {
	commonValues := map[string]string{
		"channel_affinity_setting.enabled":                     "false",
		"channel_affinity_setting.use_prompt_cache_key":        "true",
		"channel_affinity_setting.use_opencode_session":        "true",
		"channel_affinity_setting.use_metadata_user_id":        "false",
		"channel_affinity_setting.generate_fallback_key":       "true",
		"channel_affinity_setting.max_source_bytes":            "32768",
		"channel_affinity_setting.affinity_ttl_seconds":        "3600",
		"channel_affinity_setting.switch_on_success":           "true",
		"channel_affinity_setting.keep_on_channel_disabled":    "false",
		"channel_affinity_setting.rate_limit_cooldown_seconds": "10",
		"ModelRequestRateLimitEnabled":                         "false",
	}
	set := func(values map[string]string) settingProfile {
		for key, value := range commonValues {
			values[key] = value
		}
		reasons := make(map[string]string, len(values))
		for key := range values {
			reasons[key] = "OpenCodeGo affinity/RPM resumable validation profile " + name
		}
		return settingProfile{Name: name, Values: values, Reasons: reasons}
	}
	switch name {
	case "single-low":
		return set(map[string]string{
			"channel_affinity_setting.accept_internal_key":   "true",
			"channel_affinity_setting.generate_internal_key": "true",
			"channel_affinity_setting.rpm_guard_enabled":     "true",
			"channel_affinity_setting.default_account_rpm":   "1",
			"channel_affinity_setting.account_burst":         "4",
		}), nil
	case "single-affinity":
		return set(map[string]string{
			"channel_affinity_setting.accept_internal_key":   "true",
			"channel_affinity_setting.generate_internal_key": "true",
			"channel_affinity_setting.rpm_guard_enabled":     "false",
			"channel_affinity_setting.default_account_rpm":   "1450",
			"channel_affinity_setting.account_burst":         "50",
		}), nil
	case "upper":
		return set(map[string]string{
			"channel_affinity_setting.accept_internal_key":   "false",
			"channel_affinity_setting.generate_internal_key": "true",
			"channel_affinity_setting.rpm_guard_enabled":     "false",
			"channel_affinity_setting.default_account_rpm":   "1450",
			"channel_affinity_setting.account_burst":         "50",
		}), nil
	case "lower", "production":
		return set(map[string]string{
			"channel_affinity_setting.accept_internal_key":   "true",
			"channel_affinity_setting.generate_internal_key": "false",
			"channel_affinity_setting.rpm_guard_enabled":     "true",
			"channel_affinity_setting.default_account_rpm":   "1450",
			"channel_affinity_setting.account_burst":         "50",
		}), nil
	case "lower-low":
		return set(map[string]string{
			"channel_affinity_setting.accept_internal_key":   "true",
			"channel_affinity_setting.generate_internal_key": "false",
			"channel_affinity_setting.rpm_guard_enabled":     "true",
			"channel_affinity_setting.default_account_rpm":   "1",
			"channel_affinity_setting.account_burst":         "4",
		}), nil
	default:
		return settingProfile{}, fmt.Errorf("unknown profile %q", name)
	}
}

func applyProfile(runDir, runID, baseURL, pat, profileName string, apply bool) error {
	if !apply {
		return errors.New("refusing to mutate settings without --apply")
	}
	client, err := newAPIClient(baseURL, pat)
	if err != nil {
		return err
	}
	profile, err := profileByName(profileName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	current, err := client.options(ctx)
	if err != nil {
		return err
	}
	keys := orderedProfileKeys(current, profile.Values)
	changes := make([]optionChange, 0)
	for _, key := range keys {
		target := profile.Values[key]
		before := current[key]
		if before == target {
			continue
		}
		envelope, updateErr := client.updateOption(ctx, key, target)
		change := optionChange{
			Key: key, Before: before, TestValue: target, ProductionValue: productionValue(key),
			Reason: profile.Reasons[key], Rollback: fmt.Sprintf("PUT /api/option/ %s=%s", key, before),
			ResponseSuccess: updateErr == nil, ChangedAt: utcNow(), RunID: runID,
		}
		if envelope != nil {
			change.ResponseMessage = envelope.Message
		}
		changes = append(changes, change)
		if updateErr != nil {
			_ = writeOptionChanges(runDir, changes)
			return updateErr
		}
		current[key] = target
	}
	return writeOptionChanges(runDir, changes)
}

func orderedProfileKeys(current, target map[string]string) []string {
	decreases := make([]string, 0)
	normal := make([]string, 0)
	increases := make([]string, 0)
	for key, value := range target {
		if current[key] == value {
			continue
		}
		if key == "channel_affinity_setting.default_account_rpm" || key == "channel_affinity_setting.account_burst" {
			oldNumber, oldErr := strconv.Atoi(current[key])
			newNumber, newErr := strconv.Atoi(value)
			if oldErr == nil && newErr == nil && newNumber < oldNumber {
				decreases = append(decreases, key)
			} else {
				increases = append(increases, key)
			}
			continue
		}
		normal = append(normal, key)
	}
	sort.Strings(decreases)
	sort.Strings(normal)
	sort.Strings(increases)
	return append(append(decreases, normal...), increases...)
}

func productionValue(key string) string {
	values := map[string]string{
		"channel_affinity_setting.enabled":                     "false",
		"channel_affinity_setting.accept_internal_key":         "lower=true, upper=false",
		"channel_affinity_setting.generate_internal_key":       "upper=true, lower=false",
		"channel_affinity_setting.use_prompt_cache_key":        "true",
		"channel_affinity_setting.use_opencode_session":        "true",
		"channel_affinity_setting.use_metadata_user_id":        "false",
		"channel_affinity_setting.generate_fallback_key":       "true",
		"channel_affinity_setting.max_source_bytes":            "32768",
		"channel_affinity_setting.affinity_ttl_seconds":        "3600",
		"channel_affinity_setting.switch_on_success":           "true",
		"channel_affinity_setting.keep_on_channel_disabled":    "false",
		"channel_affinity_setting.rpm_guard_enabled":           "lower=true, upper=false",
		"channel_affinity_setting.default_account_rpm":         "1450",
		"channel_affinity_setting.account_burst":               "50",
		"channel_affinity_setting.rate_limit_cooldown_seconds": "10",
		"ModelRequestRateLimitEnabled":                         "restore production value",
	}
	return values[key]
}

func writeOptionChanges(runDir string, newChanges []optionChange) error {
	path := filepath.Join(runDir, "configuration-changes.json")
	changes := make([]optionChange, 0)
	if _, err := os.Stat(path); err == nil {
		if err := readJSONFile(path, &changes); err != nil {
			return err
		}
	}
	changes = append(changes, newChanges...)
	if err := writeJSONAtomic(path, changes); err != nil {
		return err
	}
	if err := writeChangesCSV(filepath.Join(runDir, "configuration-changes.csv"), changes); err != nil {
		return err
	}
	return writeChangesMarkdown(filepath.Join(runDir, "configuration-changes.md"), changes)
}

func writeChangesCSV(path string, changes []optionChange) error {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	_ = writer.Write([]string{"key", "before", "test_value", "production_value", "reason", "restart_required", "rollback", "success", "message", "changed_at", "run_id"})
	for _, change := range changes {
		_ = writer.Write([]string{change.Key, change.Before, change.TestValue, change.ProductionValue, change.Reason, strconv.FormatBool(change.RestartRequired), change.Rollback, strconv.FormatBool(change.ResponseSuccess), change.ResponseMessage, change.ChangedAt, change.RunID})
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return err
	}
	return writeBytesAtomic(path, buffer.Bytes(), 0o600)
}

func writeChangesMarkdown(path string, changes []optionChange) error {
	var builder strings.Builder
	builder.WriteString("# Test configuration change ledger\n\n")
	builder.WriteString("| Setting | Before | Test value | Production guidance | Result | Changed at |\n")
	builder.WriteString("|---|---|---|---|---|---|\n")
	for _, change := range changes {
		result := "failed"
		if change.ResponseSuccess {
			result = "saved"
		}
		fmt.Fprintf(&builder, "| `%s` | `%s` | `%s` | %s | %s | %s |\n", change.Key, change.Before, change.TestValue, change.ProductionValue, result, change.ChangedAt)
	}
	return writeBytesAtomic(path, []byte(builder.String()), 0o600)
}

func mergeTestGroups(runDir, runID, baseURL, pat string, apply bool) error {
	if !apply {
		return errors.New("refusing to mutate group settings without --apply")
	}
	client, err := newAPIClient(baseURL, pat)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	options, err := client.options(ctx)
	if err != nil {
		return err
	}
	groups := []string{"ocg-affinity-upper-e2e", "ocg-affinity-lower-e2e", "ocg-hardlimit-a", "ocg-hardlimit-b", "ocg-hardlimit-c", "ocg-mock-upper", "ocg-mock-lower"}
	groupRatio := map[string]float64{}
	if raw := strings.TrimSpace(options["GroupRatio"]); raw != "" {
		if err := rootcommon.UnmarshalJsonStr(raw, &groupRatio); err != nil {
			return fmt.Errorf("parse GroupRatio: %w", err)
		}
	}
	usable := map[string]string{}
	if raw := strings.TrimSpace(options["UserUsableGroups"]); raw != "" {
		if err := rootcommon.UnmarshalJsonStr(raw, &usable); err != nil {
			return fmt.Errorf("parse UserUsableGroups: %w", err)
		}
	}
	for _, group := range groups {
		groupRatio[group] = 1
		usable[group] = "OpenCodeGo E2E test group"
	}
	ratioJSON, _ := rootcommon.Marshal(groupRatio)
	usableJSON, _ := rootcommon.Marshal(usable)
	changes := make([]optionChange, 0, 2)
	for _, update := range []struct{ key, value string }{{"GroupRatio", string(ratioJSON)}, {"UserUsableGroups", string(usableJSON)}} {
		envelope, updateErr := client.updateOption(ctx, update.key, update.value)
		change := optionChange{Key: update.key, Before: options[update.key], TestValue: update.value, ProductionValue: "merge only required production groups", Reason: "add isolated OpenCodeGo E2E groups without replacing existing groups", Rollback: "restore captured pre-test value", ResponseSuccess: updateErr == nil, ChangedAt: utcNow(), RunID: runID}
		if envelope != nil {
			change.ResponseMessage = envelope.Message
		}
		changes = append(changes, change)
		if updateErr != nil {
			_ = writeOptionChanges(runDir, changes)
			return updateErr
		}
	}
	return writeOptionChanges(runDir, changes)
}

type tokenListItem struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	Group          string `json:"group"`
	Status         int    `json:"status"`
	UnlimitedQuota bool   `json:"unlimited_quota"`
}

func provisionTokens(runDir, baseURL, pat string, apply bool) (map[string]string, error) {
	if !apply {
		return nil, errors.New("refusing to create tokens without --apply")
	}
	client, err := newAPIClient(baseURL, pat)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	desired := map[string]string{
		"ocg-e2e-customer-1":    "ocg-affinity-upper-e2e",
		"ocg-e2e-customer-2":    "ocg-affinity-upper-e2e",
		"ocg-e2e-customer-3":    "ocg-affinity-upper-e2e",
		"ocg-e2e-inner":         "ocg-affinity-lower-e2e",
		"ocg-e2e-hard-a":        "ocg-hardlimit-a",
		"ocg-e2e-hard-b":        "ocg-hardlimit-b",
		"ocg-e2e-hard-c":        "ocg-hardlimit-c",
		"ocg-e2e-mock-customer": "ocg-mock-upper",
		"ocg-e2e-mock-inner":    "ocg-mock-lower",
	}
	items, err := listTokens(ctx, client)
	if err != nil {
		return nil, err
	}
	byName := map[string]tokenListItem{}
	for _, item := range items {
		byName[item.Name] = item
	}
	for name, group := range desired {
		item, exists := byName[name]
		if !exists {
			payload := map[string]any{"name": name, "group": group, "expired_time": -1, "unlimited_quota": true, "remain_quota": 0, "model_limits_enabled": true, "model_limits": defaultModel, "status": 1}
			if _, err := client.request(ctx, http.MethodPost, "/api/token/", payload); err != nil {
				return nil, fmt.Errorf("create token %s: %w", name, err)
			}
			items, err = listTokens(ctx, client)
			if err != nil {
				return nil, err
			}
			for _, candidate := range items {
				if candidate.Name == name {
					item = candidate
					byName[name] = candidate
					break
				}
			}
		}
		if item.ID <= 0 {
			return nil, fmt.Errorf("token %s was not found after provisioning", name)
		}
		if item.Group != group {
			return nil, fmt.Errorf("existing token %s belongs to %s, expected %s; update it manually before resuming", name, item.Group, group)
		}
	}
	secrets := make(map[string]string, len(desired))
	secretsPath := filepath.Join(runDir, "secrets.local.json")
	if err := readJSONFile(secretsPath, &secrets); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for name := range desired {
		if strings.TrimSpace(secrets[name]) != "" {
			continue
		}
		item := byName[name]
		envelope, err := client.request(ctx, http.MethodPost, fmt.Sprintf("/api/token/%d/key", item.ID), nil)
		if err != nil {
			return nil, fmt.Errorf("read token %s key: %w", name, err)
		}
		var payload struct {
			Key string `json:"key"`
		}
		if err := rootcommon.Unmarshal(envelope.Data, &payload); err != nil {
			return nil, err
		}
		secrets[name] = payload.Key
	}
	if err := writeJSONAtomic(secretsPath, secrets); err != nil {
		return nil, err
	}
	return secrets, nil
}

func provisionMockGatewayChannels(runDir, baseURL, pat string, accountURLs []string, apply bool) ([]int, error) {
	if !apply {
		return nil, errors.New("refusing to provision Mock gateway channels without --apply")
	}
	if len(accountURLs) != 3 {
		return nil, fmt.Errorf("expected 3 Mock account URLs, got %d", len(accountURLs))
	}
	secrets := map[string]string{}
	if err := readJSONFile(filepath.Join(runDir, "secrets.local.json"), &secrets); err != nil {
		return nil, err
	}
	innerKey := secrets["ocg-e2e-mock-inner"]
	if innerKey == "" {
		return nil, errors.New("ocg-e2e-mock-inner token key is missing")
	}
	client, err := newAPIClient(baseURL, pat)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	listChannels := func() (map[string]channelInventory, error) {
		envelope, requestErr := client.request(ctx, http.MethodGet, "/api/channel/?p=1&page_size=1000", nil)
		if requestErr != nil {
			return nil, requestErr
		}
		var page struct {
			Items []channelInventory `json:"items"`
		}
		if err := rootcommon.Unmarshal(envelope.Data, &page); err != nil {
			return nil, err
		}
		result := make(map[string]channelInventory, len(page.Items))
		for _, channel := range page.Items {
			result[channel.Name] = channel
		}
		return result, nil
	}

	existing, err := listChannels()
	if err != nil {
		return nil, err
	}
	upsert := func(name string, channelType int, group, target, key string) error {
		payload := map[string]any{
			"type": channelType, "name": name, "status": 1, "group": group,
			"base_url": strings.TrimRight(target, "/"), "models": defaultModel,
			"weight": 1, "priority": 0, "auto_ban": 0, "settings": "{}",
		}
		if current, ok := existing[name]; ok {
			if current.Type != channelType || current.Group != group {
				return fmt.Errorf("existing Mock channel %s has unsafe type/group", name)
			}
			payload["id"] = current.ID
			// The channel PATCH contract rejects status changes; status has its own endpoint.
			delete(payload, "status")
			_, err := client.request(ctx, http.MethodPut, "/api/channel/", payload)
			return err
		}
		payload["key"] = key
		_, err := client.request(ctx, http.MethodPost, "/api/channel/", map[string]any{"mode": "single", "channel": payload})
		return err
	}
	for index, target := range accountURLs {
		if err := upsert(fmt.Sprintf("ocg-e2e-mock-account-%c", 'a'+index), 99, "ocg-mock-lower", target, "mock-key"); err != nil {
			return nil, err
		}
	}
	if err := upsert("ocg-e2e-mock-loopback", 60, "ocg-mock-upper", baseURL, innerKey); err != nil {
		return nil, err
	}
	existing, err = listChannels()
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, 3)
	for _, name := range []string{"ocg-e2e-mock-account-a", "ocg-e2e-mock-account-b", "ocg-e2e-mock-account-c"} {
		channel, ok := existing[name]
		if !ok || channel.ID <= 0 {
			return nil, fmt.Errorf("Mock channel %s was not found after provisioning", name)
		}
		ids = append(ids, channel.ID)
	}
	return ids, nil
}

func listTokens(ctx context.Context, client *apiClient) ([]tokenListItem, error) {
	envelope, err := client.request(ctx, http.MethodGet, "/api/token/?p=1&page_size=1000", nil)
	if err != nil {
		return nil, err
	}
	var page struct {
		Items []tokenListItem `json:"items"`
	}
	if err := rootcommon.Unmarshal(envelope.Data, &page); err != nil {
		return nil, err
	}
	return page.Items, nil
}

func provisionLoopbackChannel(runDir, baseURL, pat, upstreamBaseURL string, apply bool) error {
	secrets := map[string]string{}
	if err := readJSONFile(filepath.Join(runDir, "secrets.local.json"), &secrets); err != nil {
		return fmt.Errorf("load provisioned token secrets: %w", err)
	}
	innerKey := secrets["ocg-e2e-inner"]
	if innerKey == "" {
		return errors.New("ocg-e2e-inner token key is missing")
	}
	if !apply {
		return errors.New("refusing to create loopback channel without --apply")
	}
	client, err := newAPIClient(baseURL, pat)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	envelope, err := client.request(ctx, http.MethodGet, "/api/channel/?p=1&page_size=1000&type=60", nil)
	if err != nil {
		return err
	}
	var page struct {
		Items []struct {
			ID      int     `json:"id"`
			Name    string  `json:"name"`
			Type    int     `json:"type"`
			Group   string  `json:"group"`
			Models  string  `json:"models"`
			BaseURL *string `json:"base_url"`
		} `json:"items"`
	}
	if err := rootcommon.Unmarshal(envelope.Data, &page); err != nil {
		return err
	}
	for _, channel := range page.Items {
		if channel.Name != "ocg-e2e-loopback" {
			continue
		}
		if channel.Type != 60 || channel.Group != "ocg-affinity-upper-e2e" {
			return fmt.Errorf("existing ocg-e2e-loopback channel has unsafe type/group; fix it manually")
		}
		target := strings.TrimRight(upstreamBaseURL, "/")
		current := ""
		if channel.BaseURL != nil {
			current = strings.TrimRight(*channel.BaseURL, "/")
		}
		if current != target || channel.Models != defaultModel {
			_, err := client.request(ctx, http.MethodPut, "/api/channel/", map[string]any{
				"id": channel.ID, "type": 60, "name": channel.Name,
				"base_url": target, "models": defaultModel, "group": channel.Group,
			})
			return err
		}
		return nil
	}
	weight := 1
	priority := int64(0)
	autoBan := 0
	payload := map[string]any{
		"mode": "single",
		"channel": map[string]any{
			"type": 60, "key": innerKey, "name": "ocg-e2e-loopback", "status": 1,
			"weight": weight, "priority": priority, "auto_ban": autoBan,
			"base_url": strings.TrimRight(upstreamBaseURL, "/"), "models": defaultModel,
			"group": "ocg-affinity-upper-e2e", "settings": "{}",
		},
	}
	_, err = client.request(ctx, http.MethodPost, "/api/channel/", payload)
	return err
}
