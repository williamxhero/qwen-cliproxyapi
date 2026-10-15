//go:build !windows

package bailianquota

import (
	"context"
	"os/exec"
)

func blCommand(ctx context.Context, path string, args ...string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, path, args...), nil
}
