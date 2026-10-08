package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const maxQuotaStdout = 1 << 20
const maxQuotaStderr = 16 << 10
const maxQuotaError = 4096

// boundedOutput bounds memory even when a command continuously writes. Overflow
// cancels the process; WaitDelay bounds inherited pipes held by subprocesses.
type boundedOutput struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := b.limit - b.buf.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.overflow = true
		b.cancel()
	}
	_, _ = b.buf.Write(p)
	return n, nil
}
func (b *boundedOutput) snapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...), b.overflow
}

// FetchQuota never passes the host-selected plan API key to the CLI. The CLI
// reads console login state; it cannot prove the browser account owns this key.
func (m *Manager) FetchQuota(ctx context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
	empty := pluginapi.QuotaFetchResponse{}
	if req.Provider != ProviderID {
		return empty, fmt.Errorf("unsupported quota provider")
	}
	key := strings.TrimSpace(req.Attributes["api_key"])
	if key == "" {
		return empty, fmt.Errorf("quota credential has no API key")
	}
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()
	if cfg.QuotaSource != "command" || cfg.Command == "" || cfg.CommandTimeout <= 0 {
		return empty, fmt.Errorf("quota command is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.CommandTimeout)
	defer cancel()
	stdout := &boundedOutput{limit: maxQuotaStdout, cancel: cancel}
	stderr := &boundedOutput{limit: maxQuotaStderr, cancel: cancel}
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.CommandArgs...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = 100 * time.Millisecond
	runErr := cmd.Run()
	out, outOverflow := stdout.snapshot()
	errOut, errOverflow := stderr.snapshot()
	safeError := func(message string) (pluginapi.QuotaFetchResponse, error) {
		return empty, fmt.Errorf("%s", boundedDiagnostic([]byte(redactSecrets(message, cfg, key))))
	}
	if outOverflow || errOverflow {
		return safeError("quota command output exceeds byte limit")
	}
	if ctx.Err() != nil {
		return safeError("quota command timed out or was canceled")
	}
	if runErr != nil {
		return safeError(commandErrorText(out, errOut, "quota command failed"))
	}
	result, err := normalizeQuota(out, cfg.DisplayName)
	if err != nil {
		return safeError(commandErrorText(out, errOut, err.Error()))
	}
	if result.Subscription != nil {
		result.Subscription.Plan = redactSecrets(result.Subscription.Plan, cfg, key)
	}
	for i := range result.Groups {
		result.Groups[i].DisplayName = redactSecrets(result.Groups[i].DisplayName, cfg, key)
		for j := range result.Groups[i].Buckets {
			result.Groups[i].Buckets[j].Window = redactSecrets(result.Groups[i].Buckets[j].Window, cfg, key)
		}
	}
	for i := range result.Summary {
		metric := &result.Summary[i]
		metric.Key = redactSecrets(metric.Key, cfg, key)
		metric.Label = redactSecrets(metric.Label, cfg, key)
		metric.Unit = redactSecrets(metric.Unit, cfg, key)
		metric.Currency = redactSecrets(metric.Currency, cfg, key)
	}
	return result, nil
}

// Preserve the CLI's own structured errors on both zero and nonzero exits.
// Non-JSON diagnostics are returned only through the redaction/bounding edge.
func commandErrorText(stdout, stderr []byte, fallback string) string {
	var failure struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(stdout, &failure) == nil {
		if failure.Message != "" {
			if failure.Error != "" {
				return failure.Error + ": " + failure.Message
			}
			return failure.Message
		}
		if failure.Error != "" {
			return failure.Error
		}
		if len(bytes.TrimSpace(stderr)) > 0 {
			return strings.TrimSpace(string(stderr))
		}
		return fallback
	}
	if len(bytes.TrimSpace(stdout)) > 0 {
		return strings.TrimSpace(string(stdout))
	}
	if len(bytes.TrimSpace(stderr)) > 0 {
		return strings.TrimSpace(string(stderr))
	}
	return fallback
}
func boundedDiagnostic(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > maxQuotaError {
		s = s[:maxQuotaError] + " [truncated]"
	}
	return s
}

type commandQuota struct {
	Source     string `json:"source"`
	Plan       string `json:"plan"`
	PlanStatus string `json:"planStatus"`
	ObservedAt string `json:"observedAt"`
	Windows    []struct {
		Window      string   `json:"window"`
		UsedPercent *float64 `json:"usedPercent"`
		ResetTime   string   `json:"resetTime"`
	} `json:"windows"`
	Metrics []struct {
		Key      string   `json:"key"`
		Label    string   `json:"label"`
		Value    *float64 `json:"value"`
		Unit     string   `json:"unit"`
		Currency string   `json:"currency"`
		Format   string   `json:"format"`
	} `json:"metrics"`
	Notes   []string `json:"notes"`
	Error   string   `json:"error"`
	Message string   `json:"message"`
}

func normalizeQuota(body []byte, displayName string) (pluginapi.QuotaFetchResponse, error) {
	empty := pluginapi.QuotaFetchResponse{}
	var raw commandQuota
	if json.Unmarshal(body, &raw) != nil {
		return empty, fmt.Errorf("quota command stdout is not valid contract JSON")
	}
	if raw.Error != "" || raw.Message != "" {
		return empty, fmt.Errorf("quota command returned an error")
	}
	if len(raw.Windows) == 0 && len(raw.Metrics) == 0 {
		return empty, fmt.Errorf("quota command returned no windows or metrics")
	}
	result := pluginapi.QuotaFetchResponse{Subscription: &pluginapi.QuotaSubscription{Plan: raw.Plan}}
	buckets := make([]pluginapi.QuotaBucket, 0, len(raw.Windows))
	for _, window := range raw.Windows {
		if strings.TrimSpace(window.Window) == "" || window.UsedPercent == nil || math.IsNaN(*window.UsedPercent) || math.IsInf(*window.UsedPercent, 0) {
			return empty, fmt.Errorf("quota window is invalid")
		}
		if window.ResetTime != "" {
			if _, err := time.Parse(time.RFC3339Nano, window.ResetTime); err != nil {
				return empty, fmt.Errorf("quota reset time is invalid")
			}
		}
		percent := min(100.0, max(0.0, *window.UsedPercent))
		buckets = append(buckets, pluginapi.QuotaBucket{Window: window.Window, RemainingFraction: 1 - percent/100, ResetTime: window.ResetTime})
	}
	if len(buckets) > 0 {
		result.Groups = []pluginapi.QuotaGroup{{DisplayName: displayName, Buckets: buckets}}
	}
	for _, metric := range raw.Metrics {
		if strings.TrimSpace(metric.Key) == "" || strings.TrimSpace(metric.Label) == "" || metric.Value == nil || math.IsNaN(*metric.Value) || math.IsInf(*metric.Value, 0) {
			return empty, fmt.Errorf("quota metric is invalid")
		}
		if metric.Format != "" && metric.Format != "number" && metric.Format != "currency" {
			return empty, fmt.Errorf("quota metric format is invalid")
		}
		result.Summary = append(result.Summary, pluginapi.QuotaMetric{Key: metric.Key, Label: metric.Label, Value: *metric.Value, Unit: metric.Unit, Format: metric.Format, Currency: metric.Currency})
	}
	return result, nil
}
