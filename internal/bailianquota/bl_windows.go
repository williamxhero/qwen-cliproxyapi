package bailianquota

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func blCommand(ctx context.Context, path string, args ...string) (*exec.Cmd, error) {
	var cmd *exec.Cmd
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cmd", ".bat":
		// Official npm installs use bl.cmd. cmd.exe is needed only for this
		// shim; reject expansion/control characters and keep arguments fixed.
		if strings.ContainsAny(path, "\"\r\n&|<>^%!\x00") || strings.Join(args, " ") != "usage token-plan --output json" {
			return nil, errors.New("unsupported bl shim path or arguments")
		}
		processor := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
		cmd = exec.CommandContext(ctx, processor)
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /s /c ""` + path + `" usage token-plan --output json"`}
	default:
		cmd = exec.CommandContext(ctx, path, args...)
	}
	// Killing just the npm shim leaves node running with inherited pipes.
	// Terminate only this invocation's process tree within a bounded interval.
	cmd.Cancel = func() error {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		killer := filepath.Join(os.Getenv("SystemRoot"), "System32", "taskkill.exe")
		if err := exec.CommandContext(cleanup, killer, "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run(); err == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	return cmd, nil
}
