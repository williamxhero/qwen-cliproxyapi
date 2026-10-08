package plugin

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const quotaFixture = `{"source":"bsk","plan":"Token Plan 个人版 Standard","planStatus":"生效中","observedAt":"2026-10-08T00:00:00+08:00","windows":[{"window":"1month","usedPercent":100.0,"resetTime":"2026-10-18T00:00:00+08:00"}],"metrics":[{"key":"balance_available","label":"余额","value":0,"unit":"CNY","currency":"CNY","format":"currency"}],"notes":[]}`

// This subprocess is the command stub; no shell or platform-specific executable
// is needed. It is entered only when the parent's argument slice requests it.
func TestQuotaCommandHelper(t *testing.T) {
	mode := ""
	for i, arg := range os.Args {
		if arg == "--quota-stub" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
		}
	}
	if mode == "" {
		return
	}
	switch mode {
	case "success":
		fmt.Print(quotaFixture)
	case "failure":
		fmt.Print(`{"error":"ConsoleNeedLogin","message":"CLI says login required"}`)
		os.Exit(3)
	case "zero-error":
		fmt.Print(`{"error":"ConsoleNeedLogin","message":"CLI says login required"}`)
	case "secret":
		fmt.Print(`{"error":"login","message":"dummy-config-key dummy-selected-key Cookie: session=private-cookie; token=private-token sk-sp-private-key"}`)
		os.Exit(2)
	case "garbage":
		fmt.Print("CLI garbage diagnostic")
		os.Exit(0)
	case "empty":
		fmt.Print(`{"source":"bsk","windows":[],"metrics":[]}`)
	case "timeout":
		time.Sleep(10 * time.Second)
	case "stdout-overflow":
		fmt.Print(strings.Repeat("x", maxQuotaStdout+100))
	case "stderr-overflow":
		fmt.Fprint(os.Stderr, strings.Repeat("x", maxQuotaStderr+100))
	case "stderr":
		fmt.Fprint(os.Stderr, "CLI stderr diagnostic")
		os.Exit(2)
	case "args":
		if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "space ; & argument" {
			os.Exit(4)
		}
		fmt.Print(quotaFixture)
	}
	os.Exit(0)
}
func commandManager(t *testing.T, mode string) *Manager {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(nil)
	m.cfg = providerConfig(t)
	m.cfg.Command = executable
	m.cfg.CommandArgs = []string{"-test.run=^TestQuotaCommandHelper$", "--", "--quota-stub", mode}
	m.cfg.CommandTimeout = 3 * time.Second
	return m
}
func quotaRequest() pluginapi.QuotaFetchRequest {
	return pluginapi.QuotaFetchRequest{Provider: ProviderID, Attributes: map[string]string{"api_key": "dummy-selected-key"}}
}

func TestQuotaCommandSuccessFrozenContractAndArgumentSlice(t *testing.T) {
	for _, mode := range []string{"success", "args"} {
		t.Run(mode, func(t *testing.T) {
			m := commandManager(t, mode)
			if mode == "args" {
				m.cfg.CommandArgs = append(m.cfg.CommandArgs, "space ; & argument")
			}
			result, err := m.FetchQuota(context.Background(), quotaRequest())
			if err != nil {
				t.Fatal(err)
			}
			if result.Subscription.Plan != "Token Plan 个人版 Standard" || len(result.Groups) != 1 || result.Groups[0].DisplayName != m.cfg.DisplayName || result.Groups[0].Buckets[0].Window != "1month" || result.Groups[0].Buckets[0].RemainingFraction != 0 || result.Groups[0].Buckets[0].ResetTime != "2026-10-18T00:00:00+08:00" || len(result.Summary) != 1 || result.Summary[0].Currency != "CNY" || result.Summary[0].Value != 0 {
				t.Fatalf("mapping mismatch: %#v", result)
			}
		})
	}
}
func TestQuotaCommandFailuresReturnOwnCLIText(t *testing.T) {
	tests := map[string]string{"failure": "ConsoleNeedLogin: CLI says login required", "zero-error": "ConsoleNeedLogin: CLI says login required", "garbage": "CLI garbage diagnostic", "stderr": "CLI stderr diagnostic", "empty": "no windows or metrics"}
	for mode, want := range tests {
		t.Run(mode, func(t *testing.T) {
			m := commandManager(t, mode)
			_, err := m.FetchQuota(context.Background(), quotaRequest())
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("got %v, want %s", err, want)
			}
		})
	}
}
func TestQuotaCommandHardTimeoutAndBoundedOutput(t *testing.T) {
	for _, mode := range []string{"timeout", "stdout-overflow", "stderr-overflow"} {
		t.Run(mode, func(t *testing.T) {
			m := commandManager(t, mode)
			m.cfg.CommandTimeout = 100 * time.Millisecond
			start := time.Now()
			_, err := m.FetchQuota(context.Background(), quotaRequest())
			if err == nil {
				t.Fatal("expected command failure")
			}
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Fatalf("hard bound not honored: %v", elapsed)
			}
			if mode == "timeout" && !strings.Contains(err.Error(), "timed out") {
				t.Fatal(err)
			}
			if mode != "timeout" && !strings.Contains(err.Error(), "byte limit") {
				t.Fatal(err)
			}
		})
	}
}
func TestQuotaCommandSecretRedaction(t *testing.T) {
	m := commandManager(t, "secret")
	_, err := m.FetchQuota(context.Background(), quotaRequest())
	if err == nil {
		t.Fatal("expected CLI failure")
	}
	for _, secret := range []string{"dummy-config-key", "dummy-selected-key", "private-cookie", "private-token", "sk-sp-private-key"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("secret leaked: %v", err)
		}
	}
	if !strings.Contains(err.Error(), "login") {
		t.Fatal("lost CLI's own error")
	}
}
func TestQuotaMappingClampsPercentAndAllowsMetricsOnly(t *testing.T) {
	for _, sample := range []struct {
		percent   float64
		remaining float64
	}{{-20, 1}, {25, 0.75}, {150, 0}} {
		result, err := normalizeQuota([]byte(fmt.Sprintf(`{"windows":[{"window":"5h","usedPercent":%g}]}`, sample.percent)), "Qwen")
		if err != nil || result.Groups[0].Buckets[0].RemainingFraction != sample.remaining {
			t.Fatalf("clamp failed: %#v %v", result, err)
		}
	}
	result, err := normalizeQuota([]byte(`{"metrics":[{"key":"balance","label":"Balance","value":0,"format":"number"}]}`), "Qwen")
	if err != nil || len(result.Groups) != 0 || len(result.Summary) != 1 {
		t.Fatalf("metrics-only reading failed: %#v %v", result, err)
	}
}
func TestQuotaNoReadingHonestyAndMissingValues(t *testing.T) {
	tests := map[string]string{
		"empty": `{}`, "null": `null`, "garbage": `garbage`, "error": `{"error":"login"}`,
		"missing_percent": `{"windows":[{"window":"1month"}]}`, "null_percent": `{"windows":[{"window":"1month","usedPercent":null}]}`, "missing_window": `{"windows":[{"usedPercent":0}]}`, "percent_overflow": `{"windows":[{"window":"1month","usedPercent":1e999}]}`,
		"invalid_reset":        `{"windows":[{"window":"1month","usedPercent":0,"resetTime":"not a date"}]}`,
		"missing_metric_value": `{"metrics":[{"key":"balance","label":"Balance"}]}`, "null_metric_value": `{"metrics":[{"key":"balance","label":"Balance","value":null}]}`, "missing_metric_key": `{"metrics":[{"label":"Balance","value":0}]}`, "invalid_metric_format": `{"metrics":[{"key":"balance","label":"Balance","value":0,"format":"pie"}]}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := normalizeQuota([]byte(body), "Qwen")
			if err == nil {
				t.Fatal("invented reading from invalid contract")
			}
		})
	}
	result, err := normalizeQuota([]byte(`{"windows":[{"window":"1month","usedPercent":100}]}`), "Qwen")
	if err != nil || result.Groups[0].Buckets[0].ResetTime != "" {
		t.Fatal("missing reset should stay missing, not be invented")
	}
}
func TestQuotaSelectedCredentialAndConfigurationValidation(t *testing.T) {
	m := commandManager(t, "success")
	req := quotaRequest()
	req.Provider = "other"
	if _, err := m.FetchQuota(context.Background(), req); err == nil {
		t.Fatal("accepted foreign credential")
	}
	req = quotaRequest()
	req.Attributes = nil
	if _, err := m.FetchQuota(context.Background(), req); err == nil {
		t.Fatal("fell back to config key")
	}
	m.cfg.Command = ""
	if _, err := m.FetchQuota(context.Background(), quotaRequest()); err == nil {
		t.Fatal("accepted unconfigured command")
	}
	m = commandManager(t, "success")
	m.cfg.Command = "missing-quota-command-executable"
	if _, err := m.FetchQuota(context.Background(), quotaRequest()); err == nil {
		t.Fatal("nonexistent executable succeeded")
	}
}
func TestSecretRedactionArgumentSecretsAndBoundedDiagnostics(t *testing.T) {
	cfg := providerConfig(t)
	cfg.CommandArgs = []string{"--cookie", "opaque-cookie", "--token=opaque-token"}
	got := redactSecrets("opaque-cookie opaque-token Bearer unrelated-token api_key=field-secret", cfg, "")
	for _, s := range []string{"opaque-cookie", "opaque-token", "unrelated-token", "field-secret"} {
		if strings.Contains(got, s) {
			t.Fatalf("leaked %s: %s", s, got)
		}
	}
	if len(boundedDiagnostic([]byte(strings.Repeat("x", maxQuotaError+100)))) > maxQuotaError+20 {
		t.Fatal("diagnostics unbounded")
	}
}
