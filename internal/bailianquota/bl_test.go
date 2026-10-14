package bailianquota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const blUsage = `{"per1MonthPercentage":1,"per1MonthResetTime":1792252800000}`

func blTestClient(t *testing.T, body string, runErr error) *Client {
	t.Helper()
	client := testClient()
	client.Run = func(context.Context, ...string) ([]byte, error) {
		t.Fatal("bl source contacted browser")
		return nil, nil
	}
	client.HTTP = doerFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("bl source contacted console directly")
		return nil, nil
	})
	client.RunBL = func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
		if !reflect.DeepEqual(args, []string{"usage", "token-plan", "--output", "json"}) {
			t.Fatal("incorrect bl arguments", args)
		}
		return []byte(body), nil, runErr
	}
	return client
}

func TestBLMappingAndOmittedFields(t *testing.T) {
	for _, tc := range []struct {
		percentage string
		want       float64
	}{{"0", 0}, {"0.375", 37.5}, {"1", 100}} {
		t.Run(tc.percentage, func(t *testing.T) {
			client := blTestClient(t, `{"per1MonthPercentage":`+tc.percentage+`,"per1MonthResetTime":1792252800123,"plan":"invented","metrics":[{"value":999}]}`, nil)
			result, err := client.FetchBL(context.Background(), "fake-bl", false)
			if err != nil || result.Source != "bl" || result.ObservedAt != observed.In(chinaTime).Format(time.RFC3339) || len(result.Windows) != 1 {
				t.Fatal(result, err)
			}
			window := result.Windows[0]
			if window.Window != "1month" || window.UsedPercent != tc.want || window.ResetTime != "2026-10-18T00:00:00.123+08:00" {
				t.Fatal(window)
			}
			body, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			assertBLOmitted(t, body)
		})
	}
	client := blTestClient(t, blUsage, nil)
	result, err := client.FetchBL(context.Background(), "", true)
	if err != nil || result.Source != "bl" || len(result.Windows) != 0 {
		t.Fatal("check must validate but not emit readings", result, err)
	}
}

func assertBLOmitted(t *testing.T, body []byte) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"plan", "planStatus", "metrics", "planStart", "planEnd", "daysLeft"} {
		if _, present := object[field]; present {
			t.Fatal("unsupported field emitted", field, string(body))
		}
	}
}

func TestBLInvalidResponsesFailClosed(t *testing.T) {
	for _, body := range []string{
		``, `not json private-secret`, `null`, `[]`, `{}`, blUsage + `{}`,
		`{"per1MonthPercentage":0.5}`, `{"per1MonthResetTime":1792252800000}`,
		`{"per1MonthPercentage":null,"per1MonthResetTime":1792252800000}`,
		`{"per1MonthPercentage":0.5,"per1MonthResetTime":null}`,
		`{"per1MonthPercentage":"0.5","per1MonthResetTime":1792252800000}`,
		`{"per1MonthPercentage":true,"per1MonthResetTime":1792252800000}`,
		`{"per1MonthPercentage":-0.01,"per1MonthResetTime":1792252800000}`,
		`{"per1MonthPercentage":1.01,"per1MonthResetTime":1792252800000}`,
		`{"per1MonthPercentage":1e999,"per1MonthResetTime":1792252800000}`,
		`{"per1MonthPercentage":0.5,"per1MonthResetTime":"1792252800000"}`,
		`{"per1MonthPercentage":0.5,"per1MonthResetTime":1792252800000.5}`,
		`{"per1MonthPercentage":0.5,"per1MonthResetTime":-1}`,
		`{"per1MonthPercentage":0.5,"per1MonthResetTime":9223372036854775807}`,
		`{"error":{"code":6,"message":"private-secret"},"per1MonthPercentage":1,"per1MonthResetTime":1792252800000}`,
		`{"error":null,"per1MonthPercentage":1,"per1MonthResetTime":1792252800000}`,
		strings.Repeat("x", maxResponseBytes+1),
	} {
		client := blTestClient(t, body, nil)
		var out, diagnostics bytes.Buffer
		if code := runCLI(context.Background(), []string{"--source", "bl", "--verbose"}, &out, &diagnostics, client); code != 1 {
			t.Fatal("invalid response accepted", code)
		}
		assertBLFailure(t, out.Bytes(), "invalid_response")
		if strings.Contains(out.String()+diagnostics.String(), "private-secret") {
			t.Fatal("raw diagnostics leaked")
		}
	}
}

func assertBLFailure(t *testing.T, body []byte, code string) {
	t.Helper()
	var object map[string]json.RawMessage
	var f Failure
	if json.Unmarshal(body, &object) != nil || json.Unmarshal(body, &f) != nil || f.Code != code || f.Message == "" || len(object) != 2 {
		t.Fatalf("want fail-closed %s, got %s", code, body)
	}
}

func TestBLStructuredErrorsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name, stdout, stderr, code string
		err                        error
		cancel                     bool
	}{
		{"missing", "", "", "bl_unavailable", failure("bl_unavailable", "cannot execute bl"), false},
		{"no_console", `{"error":{"code":3,"message":"No console access token found.","hint":"Run bl auth login --console private-secret"}}`, "", "not_logged_in", errors.New("private-secret"), false},
		{"expired", "", `{"error":{"code":3,"message":"Console session is not logged in or has expired."}}`, "not_logged_in", errors.New("private-secret"), false},
		{"auth_with_reading", `{"error":{"code":3},"per1MonthPercentage":1,"per1MonthResetTime":1792252800000}`, "", "not_logged_in", nil, false},
		{"bl_timeout", `{"error":{"code":5}}`, "", "timeout", errors.New("private-secret"), false},
		{"exit_with_reading", blUsage, "private-secret", "invalid_response", errors.New("private-secret"), false},
		{"stderr_error", blUsage, `{"error":{"code":6,"message":"private-secret"}}`, "invalid_response", nil, false},
		{"canceled", blUsage, "", "timeout", nil, true},
		{"deadline", blUsage, "", "timeout", context.DeadlineExceeded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := blTestClient(t, "", nil)
			client.RunBL = func(context.Context, string, ...string) ([]byte, []byte, error) {
				if tc.cancel {
					cancel()
				}
				return []byte(tc.stdout), []byte(tc.stderr), tc.err
			}
			var out, diagnostics bytes.Buffer
			if code := runCLI(ctx, []string{"--source", "bl", "--verbose"}, &out, &diagnostics, client); code != 1 {
				t.Fatal(code, out.String())
			}
			assertBLFailure(t, out.Bytes(), tc.code)
			if strings.Contains(out.String()+diagnostics.String(), "private-secret") {
				t.Fatal("raw failure leaked")
			}
		})
	}
}

func TestBLPathPrecedenceAndFlags(t *testing.T) {
	for _, tc := range []struct {
		env, flag, want string
	}{{"", "", "bl"}, {"env-bl", "", "env-bl"}, {"env-bl", "explicit-bl", "explicit-bl"}} {
		t.Setenv("BAILIAN_BL_PATH", tc.env)
		client := blTestClient(t, blUsage, nil)
		runner := client.RunBL
		client.RunBL = func(ctx context.Context, path string, args ...string) ([]byte, []byte, error) {
			if path != tc.want {
				t.Fatalf("path %q, want %q", path, tc.want)
			}
			return runner(ctx, path, args...)
		}
		args := []string{"--source", "bl", "--pretty"}
		if tc.flag != "" {
			args = append(args, "--bl-path", tc.flag)
		}
		var out, diagnostics bytes.Buffer
		if code := runCLI(context.Background(), args, &out, &diagnostics, client); code != 0 {
			t.Fatal(code, out.String())
		}
		assertBLOmitted(t, out.Bytes())
	}
	for _, args := range [][]string{
		{"--bl-path", "private-secret"}, {"--source", "cookie", "--bl-path", "private-secret"},
		{"--source", "bl", "--cookie", "private-secret"}, {"--source", "bl", "--cookie-file", "private-secret"},
		{"--source", "bl", "--browser", "private-secret"},
	} {
		var out, diagnostics bytes.Buffer
		if code := runCLI(context.Background(), args, &out, &diagnostics, testClient()); code != 1 {
			t.Fatal(code)
		}
		assertBLFailure(t, out.Bytes(), "invalid_arguments")
	}
}

func TestBLCacheRoundTripAgeAndFailedRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.json")
	client := blTestClient(t, blUsage, nil)
	var out, diagnostics bytes.Buffer
	if code := runCLI(context.Background(), []string{"--source", "bl", "--cache-file", path}, &out, &diagnostics, client); code != 0 {
		t.Fatal(code, out.String())
	}
	original, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, out.Bytes()) {
		t.Fatal("cache does not match live reading", err)
	}
	assertBLOmitted(t, original)
	client.RunBL = func(context.Context, string, ...string) ([]byte, []byte, error) {
		t.Fatal("cache-only ran bl")
		return nil, nil, nil
	}
	for _, tc := range []struct {
		age    time.Duration
		maxAge string
		code   string
	}{
		{0, "45m", ""}, {45 * time.Minute, "45m", ""},
		{45*time.Minute + time.Nanosecond, "45m", "stale_cache"},
		{46 * time.Minute, "1h", ""}, {2 * time.Minute, "1m", "stale_cache"},
		{-time.Second, "45m", "invalid_cache"},
	} {
		client.Now = func() time.Time { return observed.Add(tc.age) }
		out.Reset()
		code := runCLI(context.Background(), []string{"--source", "bl", "--bl-path", "nonexistent", "--cache-only", "--cache-file", path, "--max-age", tc.maxAge}, &out, &diagnostics, client)
		if tc.code == "" {
			if code != 0 || !bytes.Equal(original, out.Bytes()) {
				t.Fatal("cache roundtrip changed", code, out.String())
			}
			assertBLOmitted(t, out.Bytes())
		} else {
			if code != 1 {
				t.Fatal("invalid cache accepted")
			}
			assertBLFailure(t, out.Bytes(), tc.code)
		}
	}
	client = blTestClient(t, `{"error":{"code":3}}`, errors.New("private-secret"))
	out.Reset()
	if code := runCLI(context.Background(), []string{"--source", "bl", "--cache-file", path}, &out, &diagnostics, client); code != 1 {
		t.Fatal(code)
	}
	assertBLFailure(t, out.Bytes(), "not_logged_in")
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, unchanged) {
		t.Fatal("failed refresh or cache-only replaced last reading", err)
	}
}

func TestBLCachedUnsupportedMetricsCannotBecomeEmptySuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.json")
	body := `{"source":"bl","observedAt":"` + observed.In(chinaTime).Format(time.RFC3339) + `","windows":[],"metrics":[{"key":"balance","label":"Balance","value":1,"format":"number"}],"notes":[]}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	client := blTestClient(t, "", nil)
	client.RunBL = func(context.Context, string, ...string) ([]byte, []byte, error) {
		t.Fatal("cache-only ran bl")
		return nil, nil, nil
	}
	var out, diagnostics bytes.Buffer
	if code := runCLI(context.Background(), []string{"--source", "bl", "--cache-only", "--cache-file", path}, &out, &diagnostics, client); code != 1 {
		t.Fatal("unsupported cache emitted success without a reading", code, out.String())
	}
	assertBLFailure(t, out.Bytes(), "invalid_cache")
}

// Re-execute only this test as a fake CLI; no official bl or network is used.
func TestBLHelperProcess(t *testing.T) {
	mode := os.Getenv("BAILIAN_TEST_BL_HELPER")
	if mode == "" {
		return
	}
	args := os.Args
	if !reflect.DeepEqual(args[len(args)-4:], []string{"usage", "token-plan", "--output", "json"}) {
		os.Exit(2)
	}
	switch mode {
	case "ok":
		fmt.Fprint(os.Stdout, blUsage)
	case "auth":
		fmt.Fprint(os.Stderr, `{"error":{"code":3,"message":"private-secret"}}`)
		os.Exit(3)
	case "exit_auth":
		fmt.Fprint(os.Stderr, "private-secret")
		os.Exit(3)
	case "exit_timeout":
		os.Exit(5)
	case "failed":
		fmt.Fprint(os.Stdout, blUsage)
		os.Exit(1)
	case "large":
		fmt.Fprint(os.Stdout, strings.Repeat("x", maxResponseBytes+1))
	case "large_stderr":
		fmt.Fprint(os.Stdout, blUsage)
		fmt.Fprint(os.Stderr, strings.Repeat("x", maxResponseBytes+1))
	case "wait":
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

func TestBLRealSubprocessRunner(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ mode, code string }{
		{"ok", ""}, {"auth", "not_logged_in"}, {"exit_auth", "not_logged_in"},
		{"exit_timeout", "timeout"}, {"failed", "invalid_response"},
		{"large", "invalid_response"}, {"large_stderr", "invalid_response"}, {"wait", "timeout"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Setenv("BAILIAN_TEST_BL_HELPER", tc.mode)
			client := blTestClient(t, "", nil)
			client.RunBL = func(ctx context.Context, executable string, args ...string) ([]byte, []byte, error) {
				return runBL(ctx, executable, append([]string{"-test.run=^TestBLHelperProcess$", "--"}, args...)...)
			}
			args := []string{"--source", "bl", "--bl-path", path, "--timeout", "10s"}
			if tc.mode == "wait" {
				args[len(args)-1] = "200ms"
			}
			var out, diagnostics bytes.Buffer
			start := time.Now()
			code := runCLI(context.Background(), args, &out, &diagnostics, client)
			if tc.code == "" {
				if code != 0 || !bytes.Contains(out.Bytes(), []byte(`"usedPercent":100`)) || !bytes.Contains(out.Bytes(), []byte(`"resetTime":"2026-10-18T00:00:00+08:00"`)) {
					t.Fatal(code, out.String())
				}
				assertBLOmitted(t, out.Bytes())
			} else {
				if code != 1 {
					t.Fatal(code, out.String())
				}
				assertBLFailure(t, out.Bytes(), tc.code)
			}
			if strings.Contains(out.String()+diagnostics.String(), "private-secret") || time.Since(start) > 10*time.Second {
				t.Fatal("leaked diagnostics or unbounded subprocess")
			}
		})
	}
}
