package bailianquota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLIFlagsPrettyVerboseAndCheckContract(t *testing.T) {
	client := testClient()
	calls := 0
	client.HTTP = doerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return httpResponse(200, string(fixture(`{}`))), nil
	})
	var out, diagnostics bytes.Buffer
	code := runCLI(context.Background(), []string{"--json", "--pretty", "--source", "cookie", "--cookie", "session=synthetic", "--check", "--verbose"}, &out, &diagnostics, client)
	if code != 0 || calls != 1 || !strings.Contains(out.String(), "\n  \"source\"") || !strings.Contains(diagnostics.String(), "cookie") || strings.Contains(diagnostics.String(), "synthetic") {
		t.Fatal(code, out.String(), diagnostics.String())
	}
	var result Result
	if json.Unmarshal(out.Bytes(), &result) != nil || len(result.Windows) != 0 || result.Source != "cookie" {
		t.Fatal(out.String())
	}
}

func TestCLIArgumentErrorsAlwaysJSONAndNeverEchoSecrets(t *testing.T) {
	for _, args := range [][]string{{"--unknown=private-secret"}, {"--timeout", "private-secret"}, {"--timeout", "0s"}, {"--source", "private-secret"}, {"private-secret"}, {"--cookie", "private-secret"}, {"--source", "cookie", "--cookie", "private-secret", "--cookie-file", "private-secret"}, {"--source", "cookie", "--browser", "private-secret"}, {"--source", "cookie"}} {
		var out, diagnostics bytes.Buffer
		code := runCLI(context.Background(), args, &out, &diagnostics, testClient())
		var f Failure
		if code != 1 || json.Unmarshal(out.Bytes(), &f) != nil || f.Code == "" || f.Message == "" || strings.Contains(out.String()+diagnostics.String(), "private-secret") {
			t.Fatal(args, code, out.String(), diagnostics.String())
		}
	}
}

func TestCLICookieFileBOMAndWhitespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookie.txt")
	body := append([]byte{0xef, 0xbb, 0xbf}, []byte("session=synthetic\r\n")...)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	client := testClient()
	client.HTTP = doerFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Cookie") != "session=synthetic" {
			t.Fatal("file cookie malformed")
		}
		return httpResponse(200, realUsageFixture), nil
	})
	var out, diagnostics bytes.Buffer
	if code := runCLI(context.Background(), []string{"--source", "cookie", "--cookie-file", path}, &out, &diagnostics, client); code != 0 {
		t.Fatal(code, out.String())
	}
}

func TestCLICookieFileErrorsSafe(t *testing.T) {
	large := filepath.Join(t.TempDir(), "large.txt")
	if err := os.WriteFile(large, bytes.Repeat([]byte("x"), 65537), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "private-secret-missing"), large} {
		var out, diagnostics bytes.Buffer
		if code := runCLI(context.Background(), []string{"--source", "cookie", "--cookie-file", path}, &out, &diagnostics, testClient()); code != 1 {
			t.Fatal(code)
		}
		var f Failure
		_ = json.Unmarshal(out.Bytes(), &f)
		if f.Code != "cookie_file_error" || strings.Contains(out.String(), path) {
			t.Fatal(out.String())
		}
	}
}

func TestCLICacheOnlyPeriodRoundTripAndRecalculation(t *testing.T) {
	input := responses()
	start := observed.Add(-24 * time.Hour)
	end := observed.Add(24*time.Hour + time.Minute)
	subscription, _ := json.Marshal(map[string]any{"planName": "Actual Plan", "status": "VALID", "startTime": start.UnixMilli(), "endTime": end.UnixMilli()})
	input["subscription"] = fixture(string(subscription))
	input["addon"] = fixture(`{"remainingCredits":0,"totalCredits":12.5}`)
	reading, err := ParseResponses("bsk", input, observed, false)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "quota.json")
	body, _ := json.Marshal(reading)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	client := testClient()
	client.Now = func() time.Time { return observed.Add(2 * time.Minute) }
	client.Run = func(context.Context, ...string) ([]byte, error) {
		t.Fatal("cache-only contacted browser")
		return nil, nil
	}
	client.HTTP = doerFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("cache-only contacted console")
		return nil, nil
	})
	var out, diagnostics bytes.Buffer
	if code := runCLI(context.Background(), []string{"--json", "--cache-only", "--cache-file", path}, &out, &diagnostics, client); code != 0 {
		t.Fatalf("cache-only failed: %d %s", code, out.String())
	}
	var got, want map[string]any
	_ = json.Unmarshal(out.Bytes(), &got)
	_ = json.Unmarshal(body, &want)
	want["daysLeft"] = float64(1)
	wantBody, _ := json.Marshal(want)
	gotBody, _ := json.Marshal(got)
	if !bytes.Equal(gotBody, wantBody) {
		t.Fatalf("cached contract changed: got %s want %s", gotBody, wantBody)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(unchanged, body) {
		t.Fatal("cache-only changed the persisted reading", err)
	}
}

func TestCLICacheOnlyFailuresAreHonestAndOffline(t *testing.T) {
	valid, _ := json.Marshal(emptyResult("bsk", observed))
	for _, tc := range []struct {
		name, body string
		now        time.Time
		code       string
	}{
		{"missing", "", observed, "cache_file_error"},
		{"malformed", `{"private-secret":`, observed, "invalid_cache"},
		{"trailing", realUsageFixture + `{}`, observed, "invalid_cache"},
		{"empty_reading", string(valid), observed, "invalid_cache"},
		{"invalid_observed", `{"source":"bsk","observedAt":"invalid","windows":[{"window":"1month","usedPercent":100}]}`, observed, "invalid_cache"},
		{"stale", `{"source":"bsk","observedAt":"2026-10-08T17:30:00+08:00","windows":[{"window":"1month","usedPercent":100}]}`, observed.Add(46 * time.Minute), "stale_cache"},
		{"future", `{"source":"bsk","observedAt":"2026-10-08T17:30:00+08:00","windows":[{"window":"1month","usedPercent":100}]}`, observed.Add(-time.Minute), "invalid_cache"},
		{"invalid_percentage", `{"source":"bsk","observedAt":"2026-10-08T17:30:00+08:00","windows":[{"window":"1month","usedPercent":101}]}`, observed, "invalid_cache"},
		{"missing_percentage", `{"source":"bsk","observedAt":"2026-10-08T17:30:00+08:00","windows":[{"window":"1month"}]}`, observed, "invalid_cache"},
		{"null_percentage", `{"source":"bsk","observedAt":"2026-10-08T17:30:00+08:00","windows":[{"window":"1month","usedPercent":null}]}`, observed, "invalid_cache"},
		{"missing_metric_value", `{"source":"bsk","observedAt":"2026-10-08T17:30:00+08:00","metrics":[{"key":"balance_available"}]}`, observed, "invalid_cache"},
		{"structured_error_with_reading", `{"source":"bsk","observedAt":"2026-10-08T17:30:00+08:00","windows":[{"window":"1month","usedPercent":100}],"error":"failed","message":"private-secret"}`, observed, "invalid_cache"},
		{"error_message_with_reading", `{"source":"bsk","observedAt":"2026-10-08T17:30:00+08:00","windows":[{"window":"1month","usedPercent":100}],"message":"private-secret"}`, observed, "invalid_cache"},
		{"invalid_reset", `{"source":"bsk","observedAt":"2026-10-08T17:30:00+08:00","windows":[{"window":"1month","usedPercent":100,"resetTime":"invalid"}]}`, observed, "invalid_cache"},
		{"missing_metric_label", `{"source":"bsk","observedAt":"2026-10-08T17:30:00+08:00","metrics":[{"key":"balance_available","value":0}]}`, observed, "invalid_cache"},
		{"invalid_metric_format", `{"source":"bsk","observedAt":"2026-10-08T17:30:00+08:00","metrics":[{"key":"balance_available","label":"Balance","value":0,"format":"invalid"}]}`, observed, "invalid_cache"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "quota.json")
			if tc.body != "" {
				if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			client := testClient()
			client.Now = func() time.Time { return tc.now }
			client.Run = func(context.Context, ...string) ([]byte, error) { t.Fatal("browser called"); return nil, nil }
			var out, diagnostics bytes.Buffer
			if code := runCLI(context.Background(), []string{"--cache-only", "--cache-file", path}, &out, &diagnostics, client); code != 1 {
				t.Fatal(code, out.String())
			}
			var f Failure
			if json.Unmarshal(out.Bytes(), &f) != nil || f.Code != tc.code || strings.Contains(out.String(), "private-secret") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestCLICacheWriteAndOfflineRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.json")
	if err := os.WriteFile(path, []byte("previous reading"), 0600); err != nil {
		t.Fatal(err)
	}
	client := testClient()
	client.HTTP = doerFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Query().Get("api"), "-usage") {
			return httpResponse(200, realUsageFixture), nil
		}
		if strings.HasSuffix(req.URL.Query().Get("api"), "-subscription") {
			return httpResponse(200, string(fixture(`{"specCode":"standard","status":"VALID","startTime":1789639884000,"endTime":1821196800000}`))), nil
		}
		return httpResponse(200, string(fixture(`{}`))), nil
	})
	var out, diagnostics bytes.Buffer
	if code := runCLI(context.Background(), []string{"--source", "cookie", "--cookie", "session=synthetic", "--cache-file", path}, &out, &diagnostics, client); code != 0 {
		t.Fatal(code, out.String())
	}
	cached, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(cached, out.Bytes()) || bytes.Contains(cached, []byte("synthetic")) {
		t.Fatalf("cache write did not preserve contract: %s %v", cached, err)
	}
	out.Reset()
	client.HTTP = doerFunc(func(*http.Request) (*http.Response, error) { t.Fatal("cache-only fetched"); return nil, nil })
	if code := runCLI(context.Background(), []string{"--cache-only", "--cache-file", path}, &out, &diagnostics, client); code != 0 || !bytes.Equal(cached, out.Bytes()) {
		t.Fatal(code, out.String())
	}
	artifacts, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".bailian-quota-*"))
	if err != nil || len(artifacts) != 0 {
		t.Fatal("temporary cache files left behind", artifacts, err)
	}
}

func TestCLIFailedRefreshPreservesCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.json")
	original := []byte("previous reading")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	client := testClient()
	client.HTTP = doerFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("private-secret") })
	var out, diagnostics bytes.Buffer
	if code := runCLI(context.Background(), []string{"--source", "cookie", "--cookie", "session=synthetic", "--cache-file", path}, &out, &diagnostics, client); code != 1 {
		t.Fatal(code, out.String())
	}
	cached, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(cached, original) || strings.Contains(out.String(), "private-secret") {
		t.Fatal("failed refresh changed cache", err, out.String())
	}
}

func TestCLICacheOnlyLegacyMissingAndInvalidPeriod(t *testing.T) {
	for _, period := range []string{"", `,"planStart":"invalid","planEnd":"not-a-date","daysLeft":999`, `,"planStart":"2026-09-17T18:11:24+08:00"`} {
		path := filepath.Join(t.TempDir(), "quota.json")
		body := `{"source":"bsk","observedAt":"2026-10-08T17:30:00+08:00","windows":[{"window":"1month","usedPercent":100}]` + period + `}`
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		var out, diagnostics bytes.Buffer
		if code := runCLI(context.Background(), []string{"--cache-only", "--cache-file", path}, &out, &diagnostics, testClient()); code != 0 {
			t.Fatal(code, out.String())
		}
		var object map[string]json.RawMessage
		_ = json.Unmarshal(out.Bytes(), &object)
		if _, present := object["daysLeft"]; present {
			t.Fatal("invented daysLeft without parseable end", out.String())
		}
		if period == "" || strings.Contains(period, "invalid") {
			for _, field := range []string{"planStart", "planEnd"} {
				if _, present := object[field]; present {
					t.Fatal("invented missing cached period", out.String())
				}
			}
		}
	}
}

func TestCLICacheAgeBoundaryAndResponseLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.json")
	body := `{"source":"cookie","observedAt":"2026-10-08T17:30:00+08:00","metrics":[{"key":"balance_available","label":"Balance","value":0,"format":"currency"}]}`
	if err := os.WriteFile(path, append([]byte{0xef, 0xbb, 0xbf}, []byte(body)...), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		age    time.Duration
		maxAge string
		want   int
	}{
		{45 * time.Minute, "45m", 0},
		{45*time.Minute + time.Nanosecond, "45m", 1},
		{46 * time.Minute, "1h", 0},
		{time.Minute + time.Nanosecond, "1m", 1},
	} {
		client := testClient()
		client.Now = func() time.Time { return observed.Add(tc.age) }
		var out, diagnostics bytes.Buffer
		if code := runCLI(context.Background(), []string{"--cache-only", "--cache-file", path, "--max-age", tc.maxAge}, &out, &diagnostics, client); code != tc.want {
			t.Fatal(tc, code, out.String())
		}
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxResponseBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := readCache(path, observed, time.Hour)
	requireCode(t, err, "cache_file_error")
}

func TestCLICacheArgumentAndWriteErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--cache-only"}, {"--max-age", "0s"}, {"--max-age", "-1s"},
		{"--cache-only", "--cache-file", "private-secret", "--check"},
		{"--cache-only", "--cache-file", "private-secret", "--browser", "id"},
		{"--cache-only", "--cache-file", "private-secret", "--source", "cookie", "--cookie", "session=synthetic"},
		{"--check", "--cache-file", "private-secret"},
	} {
		var out, diagnostics bytes.Buffer
		if code := runCLI(context.Background(), args, &out, &diagnostics, testClient()); code != 1 {
			t.Fatal(code, out.String())
		}
		var f Failure
		_ = json.Unmarshal(out.Bytes(), &f)
		if f.Code != "invalid_arguments" || strings.Contains(out.String(), "private-secret") {
			t.Fatal(out.String())
		}
	}
	path := filepath.Join(t.TempDir(), "missing", "private-secret.json")
	if err := writeCache(path, emptyResult("bsk", observed)); err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatal("write failure not safely reported", err)
	}
	// A directory cannot be replaced by a cache file; the temporary file must go.
	directory := t.TempDir()
	if err := writeCache(directory, emptyResult("bsk", observed)); err == nil {
		t.Fatal("cache replaced a directory")
	}
	artifacts, err := filepath.Glob(filepath.Join(filepath.Dir(directory), ".bailian-quota-*"))
	if err != nil || len(artifacts) != 0 {
		t.Fatal("failed write left temporary files", artifacts, err)
	}
}

func TestCLIHelpAndUnknownErrorOutput(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if code := runCLI(context.Background(), []string{"--help"}, &out, &diagnostics, testClient()); code != 0 || out.Len() != 0 || !strings.Contains(diagnostics.String(), "Usage") {
		t.Fatal(code, out.String(), diagnostics.String())
	}
	if code := writeError(&out, false, errors.New("private-secret")); code != 1 || strings.Contains(out.String(), "private-secret") {
		t.Fatal(out.String())
	}
	var f Failure
	_ = json.Unmarshal(out.Bytes(), &f)
	if f.Code != "internal_error" {
		t.Fatal(f)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("writer failed") }
func TestJSONWriterFailure(t *testing.T) {
	if err := writeJSON(failingWriter{}, false, emptyResult("cookie", observed)); err == nil {
		t.Fatal("writer failure suppressed")
	}
}
