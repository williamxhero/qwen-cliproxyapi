package bailianquota

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os/exec"
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
	// bl v2 uses code/exit status 3 for missing or expired console login,
	// and 5 for timeout. Errors can be printed to either stream.
	var exitErr *exec.ExitError
	exitCode := 0
	if errors.As(runErr, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	stdoutCode, stdoutError := blError(stdout)
	stderrCode, stderrError := blError(stderr)
	if exitCode == 3 || stdoutCode == 3 || stderrCode == 3 {
		return Result{}, failure("not_logged_in", "bl console login is missing or expired; run bl auth login --console as the refresh user")
	}
	if exitCode == 5 || stdoutCode == 5 || stderrCode == 5 {
		return Result{}, failure("timeout", "bl operation exceeded its deadline")
	}
	if runErr != nil || stdoutError || stderrError {
		return Result{}, failure("invalid_response", "bl returned an unsuccessful quota response")
	}
	var usage struct {
		Percentage *float64 `json:"per1MonthPercentage"`
		ResetTime  *int64   `json:"per1MonthResetTime"`
	}
	if json.Unmarshal(stdout, &usage) != nil || usage.Percentage == nil || usage.ResetTime == nil || math.IsNaN(*usage.Percentage) || math.IsInf(*usage.Percentage, 0) || *usage.Percentage < 0 || *usage.Percentage > 1 || *usage.ResetTime < 0 || *usage.ResetTime > 253402271999999 {
		return Result{}, failure("invalid_response", "bl returned invalid or missing monthly quota fields")
	}
	result := emptyResult("bl", c.Now())
	if !check {
		result.Windows = []Window{{Window: "1month", UsedPercent: *usage.Percentage * 100, ResetTime: time.UnixMilli(*usage.ResetTime).In(chinaTime).Format(time.RFC3339Nano)}}
	}
	return result, nil
}

func blError(body []byte) (int, bool) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
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
