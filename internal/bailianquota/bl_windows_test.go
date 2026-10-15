package bailianquota

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBLWindowsCMDShimAndPathLookup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "node tools")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "fake-bl.cmd")
	body := "@echo off\r\nif not \"%*\"==\"usage token-plan --output json\" exit /b 2\r\necho " + blUsage + "\r\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, executable := range []string{path, "fake-bl"} {
		var out, diagnostics bytes.Buffer
		code := RunCLI(context.Background(), []string{"--source", "bl", "--bl-path", executable}, &out, &diagnostics)
		if code != 0 || !strings.Contains(out.String(), `"usedPercent":100`) {
			t.Fatal("could not run npm-style shim with spaces", code, out.String())
		}
		assertBLOmitted(t, out.Bytes())
	}
}

func TestBLWindowsRejectsShimExpansionAndUnexecutableFiles(t *testing.T) {
	for _, name := range []string{"fake%bl.cmd", "fake!bl.cmd", "fake&bl.cmd", "invalid-bl.exe"} {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte("not an executable"), 0600); err != nil {
			t.Fatal(err)
		}
		var out, diagnostics bytes.Buffer
		if code := RunCLI(context.Background(), []string{"--source", "bl", "--bl-path", path}, &out, &diagnostics); code != 1 {
			t.Fatal(code, out.String())
		}
		assertBLFailure(t, out.Bytes(), "bl_unavailable")
	}
}

func TestBLWindowsShimChildDeadline(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fake-bl.cmd")
	body := "@echo off\r\n\"" + executable + "\" -test.run=^TestBLHelperProcess$ -- %*\r\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BAILIAN_TEST_BL_HELPER", "wait")
	var out, diagnostics bytes.Buffer
	if code := RunCLI(context.Background(), []string{"--source", "bl", "--bl-path", path, "--timeout", "200ms"}, &out, &diagnostics); code != 1 {
		t.Fatal(code, out.String())
	}
	assertBLFailure(t, out.Bytes(), "timeout")
}
