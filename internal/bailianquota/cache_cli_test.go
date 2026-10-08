package bailianquota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A service-identity caller (session 0) cannot reach the browser, so it serves a
// reading that an interactive-session scheduled task refreshed. This path must
// never touch the network.
func TestCLICacheOnlyServesReadingWithoutNetwork(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	body := `{"source":"bsk","plan":"Token Plan 个人版 Standard","planStatus":"生效中","observedAt":"` +
		time.Now().In(chinaTime).Format(time.RFC3339) +
		`","windows":[{"window":"1month","usedPercent":100,"resetTime":"2026-10-18T00:00:00+08:00"}],"metrics":[],"notes":[]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := testClient()
	client.HTTP = doerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("network must not be used")
	})
	var out, diagnostics bytes.Buffer
	code := runCLI(context.Background(), []string{"--json", "--cache-only", "--cache-file", path, "--source", "cookie", "--cookie", "synthetic"},
		&out, &diagnostics, client)
	if code != 0 || calls != 0 {
		t.Fatal(code, calls, out.String(), diagnostics.String())
	}
	var result Result
	if json.Unmarshal(out.Bytes(), &result) != nil || result.Plan == "" || len(result.Windows) != 1 {
		t.Fatal(out.String())
	}
}

func TestCLICacheOnlyFailures(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.json")
	invalid := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(invalid, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "stale.json")
	if err := os.WriteFile(stale, []byte(`{"source":"bsk","plan":"p","observedAt":"2020-01-01T00:00:00+08:00","windows":[],"metrics":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args []string
		code string
	}{
		{[]string{"--cache-only"}, "invalid_arguments"},
		{[]string{"--cache-only", "--cache-file", missing}, "cache_unavailable"},
		{[]string{"--cache-only", "--cache-file", empty}, "cache_unavailable"},
		{[]string{"--cache-only", "--cache-file", invalid}, "cache_invalid"},
		{[]string{"--cache-only", "--cache-file", stale, "--max-age", "45m"}, "cache_stale"},
		{[]string{"--cache-file", missing, "--max-age", "-1s"}, "invalid_arguments"},
	}
	for _, tc := range cases {
		var out, diagnostics bytes.Buffer
		code := runCLI(context.Background(), tc.args, &out, &diagnostics, testClient())
		var failure Failure
		if code != 1 || json.Unmarshal(out.Bytes(), &failure) != nil || failure.Code != tc.code {
			t.Fatal(tc.args, code, out.String())
		}
	}
}

func TestCLIWritesCacheOnSuccessfulFetch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "cache.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	client := testClient()
	client.HTTP = doerFunc(func(*http.Request) (*http.Response, error) {
		return httpResponse(200, string(fixture(`{"per1MonthPercentage":0.5,"per1MonthResetTime":1792252800000}`))), nil
	})
	var out, diagnostics bytes.Buffer
	code := runCLI(context.Background(), []string{"--json", "--source", "cookie", "--cookie", "synthetic", "--cache-file", path},
		&out, &diagnostics, client)
	if code != 0 {
		t.Fatal(code, out.String(), diagnostics.String())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result Result
	if json.Unmarshal(body, &result) != nil || result.Source != "cookie" {
		t.Fatal(string(body))
	}
	// A failed write must not fail the fetch itself.
	var out2, diagnostics2 bytes.Buffer
	code = runCLI(context.Background(), []string{"--json", "--source", "cookie", "--cookie", "synthetic", "--cache-file", filepath.Join(dir, "missing-dir", "cache.json")},
		&out2, &diagnostics2, client)
	if code != 0 || !json.Valid(out2.Bytes()) {
		t.Fatal(code, out2.String())
	}
}
