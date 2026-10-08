package bailianquota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RunCLI writes exactly one JSON value to stdout on success or failure. Verbose
// diagnostics go only to stderr and contain no command output or credentials.
func RunCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runCLI(ctx, args, stdout, stderr, NewClient())
}

func runCLI(ctx context.Context, args []string, stdout, stderr io.Writer, client *Client) int {
	flags := flag.NewFlagSet("bailian-quota", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonOutput := flags.Bool("json", true, "print the frozen JSON contract (default)")
	pretty := flags.Bool("pretty", false, "indent JSON output")
	timeout := flags.Duration("timeout", 90*time.Second, "overall fetch deadline (cleanup has a separate deadline)")
	check := flags.Bool("check", false, "verify console login and RPC reachability only")
	source := flags.String("source", "bsk", "source: bsk or cookie")
	browser := flags.String("browser", "", "connected browser instance ID or unique label")
	cookie := flags.String("cookie", "", "console cookie header (prefer --cookie-file to avoid argv exposure)")
	cookieFile := flags.String("cookie-file", "", "path to a file containing the console cookie header")
	cacheOnly := flags.Bool("cache-only", false, "read cached quota without contacting the browser or console")
	cacheFile := flags.String("cache-file", "", "quota cache path; successful live readings are saved here")
	maxAge := flags.Duration("max-age", 45*time.Minute, "maximum cached reading age")
	verbose := flags.Bool("verbose", false, "write sanitized progress messages to stderr")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: bailian-quota [--json] [--pretty] [--timeout 90s] [--check] [--source bsk|cookie] [--browser ID] [--cookie HEADER | --cookie-file PATH] [--cache-file PATH] [--cache-only] [--max-age 45m] [--verbose]")
		fmt.Fprintln(stderr, "bsk uses the already-logged-in browser without focus. cookie is the session-0 service fallback; keep its file outside this repository.")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.Usage()
			return 0
		}
		return writeError(stdout, *pretty, failure("invalid_arguments", "invalid flag or flag value; use --help"))
	}
	_ = jsonOutput // JSON remains the contract even when explicitly set to false.
	if flags.NArg() != 0 || *timeout <= 0 || (*source != "bsk" && *source != "cookie") {
		return writeError(stdout, *pretty, failure("invalid_arguments", "choose --source bsk|cookie, a positive --timeout, and no positional arguments"))
	}
	if (*cookie != "" && *cookieFile != "") || (*source == "bsk" && (*cookie != "" || *cookieFile != "")) || (*source == "cookie" && *browser != "") {
		return writeError(stdout, *pretty, failure("invalid_arguments", "cookie flags require --source cookie and are mutually exclusive; --browser requires bsk"))
	}
	if *maxAge <= 0 || (*cacheOnly && (*cacheFile == "" || *check || *cookie != "" || *cookieFile != "" || *browser != "")) || (*check && *cacheFile != "") {
		return writeError(stdout, *pretty, failure("invalid_arguments", "use a positive --max-age; --cache-only requires --cache-file and cannot query login or use browser/cookie flags; --check cannot write a cache"))
	}
	if *cacheOnly {
		result, err := readCache(*cacheFile, client.Now(), *maxAge)
		if err != nil {
			return writeError(stdout, *pretty, err)
		}
		if writeJSON(stdout, *pretty, result) != nil {
			return 1
		}
		return 0
	}
	if *cookieFile != "" {
		file, err := os.Open(*cookieFile)
		if err != nil {
			return writeError(stdout, *pretty, failure("cookie_file_error", "cannot open the supplied cookie file"))
		}
		body, readErr := io.ReadAll(io.LimitReader(file, 65537))
		file.Close()
		if readErr != nil || len(body) > 65536 {
			return writeError(stdout, *pretty, failure("cookie_file_error", "cookie file is unreadable or exceeds 64 KiB"))
		}
		*cookie = strings.TrimSpace(strings.TrimPrefix(string(body), string([]byte{0xef, 0xbb, 0xbf})))
	}
	if *verbose {
		fmt.Fprintln(stderr, "Reading Bailian console using", *source, "(credentials and raw diagnostics are suppressed).")
	}
	workTimeout := *timeout
	if *source == "bsk" {
		// The host can kill the process at --timeout. Reserve a bounded cleanup
		// interval plus a small output margin before that external deadline.
		copyClient := *client
		copyClient.cleanupBudget = max(time.Nanosecond, min(10*time.Second, *timeout/3))
		workTimeout -= copyClient.cleanupBudget + min(2*time.Second, *timeout/10)
		client = &copyClient
	}
	fetchCtx, cancel := context.WithTimeout(ctx, workTimeout)
	defer cancel()
	var result Result
	var err error
	if *source == "cookie" {
		result, err = client.FetchCookie(fetchCtx, *cookie, *check)
	} else {
		result, err = client.FetchBrowser(fetchCtx, *browser, *check)
	}
	if err != nil {
		return writeError(stdout, *pretty, err)
	}
	if *cacheFile != "" {
		if err := writeCache(*cacheFile, result); err != nil {
			return writeError(stdout, *pretty, err)
		}
	}
	if err = writeJSON(stdout, *pretty, result); err != nil {
		return 1
	}
	return 0
}

func readCache(path string, now time.Time, maxAge time.Duration) (Result, error) {
	file, err := os.Open(path)
	if err != nil {
		return Result{}, failure("cache_file_error", "cannot open the supplied quota cache")
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return Result{}, failure("cache_file_error", "quota cache is unreadable or exceeds the response limit")
	}
	body = bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})
	var result Result
	if json.Unmarshal(body, &result) != nil || result.Source == "" || len(result.Windows)+len(result.Metrics) == 0 {
		return Result{}, failure("invalid_cache", "quota cache is malformed or contains no reading")
	}
	observed, err := time.Parse(time.RFC3339, result.ObservedAt)
	if err != nil || observed.After(now) {
		return Result{}, failure("invalid_cache", "quota cache has an invalid or future observation time")
	}
	if now.Sub(observed) > maxAge {
		return Result{}, failure("stale_cache", "quota cache exceeds --max-age; refresh it before reading")
	}
	// Missing/null numeric fields must not become invented zero readings.
	var numbers struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Windows []struct {
			UsedPercent *float64 `json:"usedPercent"`
		} `json:"windows"`
		Metrics []struct {
			Value *float64 `json:"value"`
		} `json:"metrics"`
	}
	if json.Unmarshal(body, &numbers) != nil || numbers.Error != "" || numbers.Message != "" {
		return Result{}, failure("invalid_cache", "quota cache contains a failed query instead of a reading")
	}
	for i, window := range result.Windows {
		if numbers.Windows[i].UsedPercent == nil || strings.TrimSpace(window.Window) == "" || math.IsNaN(window.UsedPercent) || math.IsInf(window.UsedPercent, 0) || window.UsedPercent < 0 || window.UsedPercent > 100 {
			return Result{}, failure("invalid_cache", "quota cache contains an invalid window")
		}
		if window.ResetTime != "" {
			if _, err := time.Parse(time.RFC3339Nano, window.ResetTime); err != nil {
				return Result{}, failure("invalid_cache", "quota cache contains an invalid reset time")
			}
		}
	}
	for i, metric := range result.Metrics {
		if numbers.Metrics[i].Value == nil || strings.TrimSpace(metric.Key) == "" || strings.TrimSpace(metric.Label) == "" || math.IsNaN(metric.Value) || math.IsInf(metric.Value, 0) {
			return Result{}, failure("invalid_cache", "quota cache contains an invalid metric")
		}
		if metric.Format != "" && metric.Format != "number" && metric.Format != "currency" {
			return Result{}, failure("invalid_cache", "quota cache contains an invalid metric format")
		}
	}
	if result.Windows == nil {
		result.Windows = []Window{}
	}
	if result.Metrics == nil {
		result.Metrics = []Metric{}
	}
	if result.Notes == nil {
		result.Notes = []string{}
	}
	if _, err := time.Parse(time.RFC3339Nano, result.PlanStart); err != nil {
		result.PlanStart = ""
	}
	if _, err := time.Parse(time.RFC3339Nano, result.PlanEnd); err != nil {
		result.PlanEnd = ""
	}
	result.updateDaysLeft(now)
	return result, nil
}

func writeCache(path string, result Result) error {
	// Replace only after a complete successful write, so a failed refresh cannot
	// truncate the last reading used by a service running in another session.
	file, err := os.CreateTemp(filepath.Dir(path), ".bailian-quota-*")
	if err != nil {
		return failure("cache_file_error", "cannot create the supplied quota cache")
	}
	defer os.Remove(file.Name())
	if err := writeJSON(file, false, result); err != nil {
		file.Close()
		return failure("cache_file_error", "cannot write the supplied quota cache")
	}
	if err := file.Close(); err != nil {
		return failure("cache_file_error", "cannot close the supplied quota cache")
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return failure("cache_file_error", "cannot replace the supplied quota cache")
	}
	return nil
}

func writeError(out io.Writer, pretty bool, err error) int {
	var known *Failure
	if !errors.As(err, &known) {
		known = &Failure{Code: "internal_error", Message: "quota query failed"}
	}
	_ = writeJSON(out, pretty, known)
	return 1
}

func writeJSON(out io.Writer, pretty bool, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	if pretty {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(value)
}
