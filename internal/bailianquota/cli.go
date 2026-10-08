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
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: bailian-quota [--json] [--pretty] [--timeout 90s] [--check] [--source bsk|cookie] [--browser ID] [--cookie HEADER | --cookie-file PATH] [--verbose]")
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
	if err = writeJSON(stdout, *pretty, result); err != nil {
		return 1
	}
	return 0
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
