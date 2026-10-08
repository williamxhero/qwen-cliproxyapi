package bailianquota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIFlagsPrettyVerboseAndCheckContract(t *testing.T) {
	client := testClient()
	calls := 0
	client.HTTP = doerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return httpResponse(200, string(fixture(`{}`))), nil
	})
	var out, diagnostics bytes.Buffer
	code := runCLI(context.Background(), []string{"--json", "--pretty", "--source", "cookie", "--cookie", "session=synthetic", "--check", "--verbose"}, &out, &diagnostics, client)
	if code != 0 || calls != 1 || !strings.Contains(out.String(), "\n  \"source\"") || !strings.Contains(diagnostics.String(), "cookie") || strings.Contains(diagnostics.String(), "synthetic") {
		t.Fatal(code, out.String(), diagnostics.String())
	}
	var result Result
	if json.Unmarshal(out.Bytes(), &result) != nil || len(result.Windows) != 0 || result.Source != "cookie" {
		t.Fatal(out.String())
	}
}

func TestCLIArgumentErrorsAlwaysJSONAndNeverEchoSecrets(t *testing.T) {
	for _, args := range [][]string{{"--unknown=private-secret"}, {"--timeout", "private-secret"}, {"--timeout", "0s"}, {"--source", "private-secret"}, {"private-secret"}, {"--cookie", "private-secret"}, {"--source", "cookie", "--cookie", "private-secret", "--cookie-file", "private-secret"}, {"--source", "cookie", "--browser", "private-secret"}, {"--source", "cookie"}} {
		var out, diagnostics bytes.Buffer
		code := runCLI(context.Background(), args, &out, &diagnostics, testClient())
		var f Failure
		if code != 1 || json.Unmarshal(out.Bytes(), &f) != nil || f.Code == "" || f.Message == "" || strings.Contains(out.String()+diagnostics.String(), "private-secret") {
			t.Fatal(args, code, out.String(), diagnostics.String())
		}
	}
}

func TestCLICookieFileBOMAndWhitespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookie.txt")
	body := append([]byte{0xef, 0xbb, 0xbf}, []byte("session=synthetic\r\n")...)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	client := testClient()
	client.HTTP = doerFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Cookie") != "session=synthetic" {
			t.Fatal("file cookie malformed")
		}
		return httpResponse(200, realUsageFixture), nil
	})
	var out, diagnostics bytes.Buffer
	if code := runCLI(context.Background(), []string{"--source", "cookie", "--cookie-file", path}, &out, &diagnostics, client); code != 0 {
		t.Fatal(code, out.String())
	}
}

func TestCLICookieFileErrorsSafe(t *testing.T) {
	large := filepath.Join(t.TempDir(), "large.txt")
	if err := os.WriteFile(large, bytes.Repeat([]byte("x"), 65537), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "private-secret-missing"), large} {
		var out, diagnostics bytes.Buffer
		if code := runCLI(context.Background(), []string{"--source", "cookie", "--cookie-file", path}, &out, &diagnostics, testClient()); code != 1 {
			t.Fatal(code)
		}
		var f Failure
		_ = json.Unmarshal(out.Bytes(), &f)
		if f.Code != "cookie_file_error" || strings.Contains(out.String(), path) {
			t.Fatal(out.String())
		}
	}
}

func TestCLIHelpAndUnknownErrorOutput(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if code := runCLI(context.Background(), []string{"--help"}, &out, &diagnostics, testClient()); code != 0 || out.Len() != 0 || !strings.Contains(diagnostics.String(), "Usage") {
		t.Fatal(code, out.String(), diagnostics.String())
	}
	if code := writeError(&out, false, errors.New("private-secret")); code != 1 || strings.Contains(out.String(), "private-secret") {
		t.Fatal(out.String())
	}
	var f Failure
	_ = json.Unmarshal(out.Bytes(), &f)
	if f.Code != "internal_error" {
		t.Fatal(f)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("writer failed") }
func TestJSONWriterFailure(t *testing.T) {
	if err := writeJSON(failingWriter{}, false, emptyResult("cookie", observed)); err == nil {
		t.Fatal("writer failure suppressed")
	}
}
