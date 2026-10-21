package bailianquota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os/exec"
	"strings"
	"time"
)

// BLCommandRunner keeps executable selection and both diagnostic streams
// injectable. Raw CLI diagnostics must never be returned to the user.
type BLCommandRunner func(context.Context, string, ...string) ([]byte, []byte, error)

func runBL(ctx context.Context, executable string, args ...string) ([]byte, []byte, error) {
	path, err := exec.LookPath(executable)
	if err != nil {
		return nil, nil, failure("bl_unavailable", "cannot find or execute bl; configure --bl-path")
	}
	cmd, err := blCommand(ctx, path, args...)
	if err != nil {
		return nil, nil, failure("bl_unavailable", "cannot execute bl; configure a supported --bl-path")
	}
	var stdout, stderr boundedOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	if err != nil && cmd.ProcessState == nil {
		return nil, nil, failure("bl_unavailable", "cannot start bl; check its installation and permissions")
	}
	return stdout.Bytes(), stderr.Bytes(), err
}

func (c *Client) FetchBL(ctx context.Context, executable string, check bool) (Result, error) {
	if executable == "" {
		executable = "bl"
	}
	run := c.RunBL
	if run == nil {
		run = runBL
	}
	stdout, stderr, runErr := run(ctx, executable, "usage", "token-plan", "--output", "json")
	if ctx.Err() != nil || errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, context.Canceled) {
		return Result{}, failure("timeout", "bl operation was canceled or exceeded --timeout")
	}
	var known *Failure
	if errors.As(runErr, &known) {
		return Result{}, runErr
	}
	if len(stdout) > maxResponseBytes || len(stderr) > maxResponseBytes {
		return Result{}, failure("invalid_response", "bl output exceeds the response limit")
	}
	// bl v2 writes its error envelope to either stream, and on Windows the Node
	// launcher can abort during teardown after the payload has already been
	// written. The exit status alone is therefore not a reliable diagnostic.
	var coded exitCoder
	exitCode := 0
	if errors.As(runErr, &coded) {
		exitCode = coded.ExitCode()
	}
	stdoutCode, stdoutError := blError(stdout)
	stderrCode, stderrError := blError(stderr)
	code := stdoutCode
	if code == 0 {
		code = stderrCode
	}
	if code == 3 || exitCode == 3 {
		return Result{}, failure("not_logged_in", "bl console login is missing or expired; run bl auth login --console as the refresh user")
	}
	if code == 5 || exitCode == 5 {
		return Result{}, failure("timeout", "bl operation exceeded its deadline")
	}
	if stdoutError || stderrError {
		return Result{}, failure("invalid_response", "bl returned an unsuccessful quota response")
	}
	usage, valid := parseBLUsage(stdout)
	if !valid {
		return Result{}, failure("invalid_response", "bl returned invalid or missing monthly quota fields")
	}
	if runErr != nil && !isWindowsTeardownAbort(exitCode) {
		return Result{}, failure("invalid_response", "bl returned an unsuccessful quota response")
	}
	result := emptyResult("bl", c.Now())
	if !check {
		result.Windows = []Window{{Window: "1month", UsedPercent: *usage.Percentage * 100, ResetTime: time.UnixMilli(*usage.ResetTime).In(chinaTime).Format(time.RFC3339Nano)}}
	}
	return result, nil
}

// exitCoder keeps the exit-status mapping testable without constructing a real
// *exec.ExitError for a Windows status code.
type exitCoder interface {
	ExitCode() int
}

// The official Node launcher can abort while tearing down after it has already
// written its payload (libuv UV_HANDLE_CLOSING assertion, status 0xC0000409).
// Both the signed and unsigned spellings are accepted because Go reports the
// raw Windows status as an int.
func isWindowsTeardownAbort(code int) bool {
	return code == 3221226505 || code == -1073740791
}

type blUsageFields struct {
	Percentage *float64 `json:"per1MonthPercentage"`
	ResetTime  *int64   `json:"per1MonthResetTime"`
}

func parseBLUsage(stdout []byte) (blUsageFields, bool) {
	value := firstJSONValue(stdout)
	if value == nil {
		return blUsageFields{}, false
	}
	var usage blUsageFields
	if json.Unmarshal(value, &usage) != nil || usage.Percentage == nil || usage.ResetTime == nil {
		return blUsageFields{}, false
	}
	if math.IsNaN(*usage.Percentage) || math.IsInf(*usage.Percentage, 0) || *usage.Percentage < 0 || *usage.Percentage > 1 || *usage.ResetTime < 0 || *usage.ResetTime > 253402271999999 {
		return blUsageFields{}, false
	}
	return usage, true
}

// firstJSONValue returns the first complete JSON value when whatever follows it
// is only the known Windows teardown assertion. Any other trailing content is
// rejected, so concatenated payloads cannot be read as a valid reading.
func firstJSONValue(stream []byte) []byte {
	decoder := json.NewDecoder(bytes.NewReader(stream))
	var raw json.RawMessage
	if decoder.Decode(&raw) != nil {
		return nil
	}
	if !tolerableTrailer(stream[int(decoder.InputOffset()):]) {
		return nil
	}
	return raw
}

func tolerableTrailer(rest []byte) bool {
	for _, line := range strings.Split(string(rest), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "Assertion failed:") && !strings.Contains(line, "UV_HANDLE_CLOSING") {
			return false
		}
	}
	return true
}

func blError(body []byte) (int, bool) {
	value := firstJSONValue(body)
	if value == nil {
		return 0, false
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(value, &envelope) != nil {
		return 0, false
	}
	raw, present := envelope["error"]
	if !present {
		return 0, false
	}
	var diagnostic struct {
		Code int `json:"code"`
	}
	_ = json.Unmarshal(raw, &diagnostic)
	return diagnostic.Code, true
}
