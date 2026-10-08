package bailianquota

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
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
	verbose := flags.Bool("verbose", false, "write sanitized progress messages to stderr")
	cacheFile := flags.String("cache-file", "", "path to write a successful reading to, or to read a reading from with --cache-only")
	cacheOnly := flags.Bool("cache-only", false, "print the cached reading from --cache-file without contacting the browser")
	maxAge := flags.Duration("max-age", 0, "with --cache-only, reject a reading observed longer ago than this (0 disables)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: bailian-quota [--json] [--pretty] [--timeout 90s] [--check] [--source bsk|cookie] [--browser ID] [--cookie HEADER | --cookie-file PATH] [--cache-file PATH [--cache-only] [--max-age 45m]] [--verbose]")
		fmt.Fprintln(stderr, "bsk uses the already-logged-in browser without focus. cookie is the session-0 service fallback; keep its file outside this repository.")
		fmt.Fprintln(stderr, "--cache-only serves a reading produced earlier (e.g. by a scheduled task in the interactive session) when the caller cannot reach the browser.")
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
	if *cacheOnly && *cacheFile == "" {
		return writeError(stdout, *pretty, failure("invalid_arguments", "--cache-only requires --cache-file"))
	}
	if *maxAge < 0 {
		return writeError(stdout, *pretty, failure("invalid_arguments", "--max-age must not be negative"))
	}
	if *cacheOnly {
		return printCachedReading(stdout, *pretty, *cacheFile, *maxAge)
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
		if cacheErr := writeCachedReading(*cacheFile, result); cacheErr != nil && *verbose {
			fmt.Fprintln(stderr, "cache write failed; the reading is still returned")
		}
	}
	if err = writeJSON(stdout, *pretty, result); err != nil {
		return 1
	}
	return 0
}

// printCachedReading serves a reading produced earlier by another invocation. It
// never reaches the network, so a service-identity caller (session 0, no browser)
// can still report quota that an interactive-session scheduled task refreshed.
func printCachedReading(out io.Writer, pretty bool, path string, maxAge time.Duration) int {
	body, err := os.ReadFile(path)
	if err != nil || len(body) == 0 {
		return writeError(out, pretty, failure("cache_unavailable", "no cached reading is available at the configured path"))
	}
	var reading Result
	if err = json.Unmarshal(body, &reading); err != nil {
		return writeError(out, pretty, failure("cache_invalid", "the cached reading is not a readable reading"))
	}
	if maxAge > 0 {
		observed, parseErr := time.Parse(time.RFC3339, reading.ObservedAt)
		if parseErr != nil {
			return writeError(out, pretty, failure("cache_invalid", "the cached reading has no usable observation time"))
		}
		if age := time.Since(observed); age > maxAge {
			return writeError(out, pretty, failure("cache_stale", fmt.Sprintf("the cached reading is %d minutes old; the refresh task is not keeping up", int(age.Minutes()))))
		}
	}
	if err = writeJSON(out, pretty, reading); err != nil {
		return 1
	}
	return 0
}

// writeCachedReading persists a successful reading for --cache-only callers.
func writeCachedReading(path string, reading Result) error {
	body, err := json.Marshal(reading)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o600)
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
