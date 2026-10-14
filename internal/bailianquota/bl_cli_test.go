package bailianquota

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestCLIBLUnavailableIsFailClosed(t *testing.T) {
	var out, diagnostics bytes.Buffer
	code := RunCLI(context.Background(), []string{"--source", "bl", "--bl-path", filepath.Join(t.TempDir(), "missing-bl")}, &out, &diagnostics)
	var f Failure
	if code != 1 || json.Unmarshal(out.Bytes(), &f) != nil || f.Code != "bl_unavailable" {
		t.Fatalf("got exit %d, output %s; want bl_unavailable", code, out.String())
	}
	if bytes.Contains(out.Bytes(), []byte("usedPercent")) {
		t.Fatal("failed query emitted a reading")
	}
}
