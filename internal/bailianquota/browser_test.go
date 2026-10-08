package bailianquota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBrowserSelection(t *testing.T) {
	raw := []byte(`[{"instance_id":"first","label":"work"},{"instance_id":"second","label":"personal"}]`)
	for _, tc := range []struct{ requested, want string }{{"", "first"}, {"second", "second"}, {"personal", "second"}} {
		got, err := SelectBrowser(raw, tc.requested)
		if err != nil || got != tc.want {
			t.Fatal(got, err)
		}
	}
	for _, raw := range []string{`[]`, `[{"instance_id":"first","unresponsive":true}]`, `[{"instance_id":""}]`} {
		_, err := SelectBrowser([]byte(raw), "")
		requireCode(t, err, "no_browser")
	}
	_, err := SelectBrowser(raw, "missing")
	requireCode(t, err, "no_browser")
	_, err = SelectBrowser([]byte(`garbage`), "")
	requireCode(t, err, "invalid_response")
	_, err = SelectBrowser([]byte(`[{"instance_id":"a","label":"same"},{"instance_id":"b","label":"same"}]`), "same")
	requireCode(t, err, "no_browser")
}

func browserPayload() []byte {
	payload, _ := json.Marshal(responses())
	body, _ := json.Marshal(map[string]any{"ok": true, "value": json.RawMessage(payload)})
	return body
}

func TestBrowserNoFocusFlowAndCleanup(t *testing.T) {
	client := testClient()
	var calls [][]string
	client.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{}, args...))
		switch args[0] {
		case "browsers":
			return []byte(`[{"instance_id":"selected"}]`), nil
		case "session":
			if args[1] == "start" {
				return []byte(`{"session_id":"ours"}`), nil
			}
			return []byte(`{}`), nil
		case "navigate":
			return nil, nil
		case "evaluate":
			return browserPayload(), nil
		}
		return nil, errors.New("unexpected call")
	}
	result, err := client.FetchBrowser(context.Background(), "selected", false)
	if err != nil || len(result.Windows) != 1 || len(calls) != 5 {
		t.Fatal(result, err, calls)
	}
	if !reflect.DeepEqual(calls[1], []string{"session", "start", "--json", "--browser", "selected", "--no-focus"}) || !reflect.DeepEqual(calls[2], []string{"navigate", ConsoleURL, "--session", "ours"}) || !reflect.DeepEqual(calls[4], []string{"session", "stop", "ours"}) {
		t.Fatal(calls)
	}
	if calls[3][0] != "evaluate" || !reflect.DeepEqual(calls[3][2:], []string{"--session", "ours", "--json", "--timeout", "60s"}) {
		t.Fatal(calls[3])
	}
}

func TestBrowserFailureCleanupUsesFreshContext(t *testing.T) {
	for _, failStage := range []string{"navigate", "evaluate", "decode", "login", "timeout"} {
		t.Run(failStage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := testClient()
			stopped := false
			client.Run = func(commandCtx context.Context, args ...string) ([]byte, error) {
				if args[0] == "browsers" {
					return []byte(`[{"instance_id":"first"}]`), nil
				}
				if args[0] == "session" && args[1] == "start" {
					return []byte(`{"session_id":"ours"}`), nil
				}
				if args[0] == "session" && args[1] == "stop" {
					stopped = true
					if commandCtx.Err() != nil {
						t.Fatal("cleanup reused canceled work context")
					}
					if _, ok := commandCtx.Deadline(); !ok {
						t.Fatal("unbounded cleanup")
					}
					return nil, nil
				}
				if args[0] == "navigate" {
					if failStage == "navigate" {
						return nil, errors.New("private-token")
					}
					return nil, nil
				}
				switch failStage {
				case "timeout":
					cancel()
					return nil, ctx.Err()
				case "evaluate":
					return nil, errors.New("private-cookie")
				case "decode":
					return []byte(`{"ok":false,"error":"Cookie=private"}`), nil
				case "login":
					return []byte(`{"ok":true,"value":{"error":"not_logged_in","message":"private"}}`), nil
				}
				return nil, errors.New("unexpected")
			}
			_, err := client.FetchBrowser(ctx, "", false)
			if err == nil || !stopped || strings.Contains(err.Error(), "private") {
				t.Fatal(err, stopped)
			}
			if failStage == "timeout" {
				requireCode(t, err, "timeout")
			}
		})
	}
}

func TestSessionStartFailureWithIDStillCleansUp(t *testing.T) {
	client := testClient()
	stopped := false
	client.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "browsers" {
			return []byte(`[{"instance_id":"first"}]`), nil
		}
		if args[1] == "stop" {
			stopped = true
			return nil, nil
		}
		return []byte(`{"session_id":"ours"}`), errors.New("private")
	}
	_, err := client.FetchBrowser(context.Background(), "", false)
	requireCode(t, err, "session_failed")
	if !stopped {
		t.Fatal("session orphaned")
	}
}

func TestNoBrowserAndBadSessionDoNotStopUnrelatedSessions(t *testing.T) {
	for _, stage := range []string{"browsers_failed", "no_browser", "bad_session"} {
		client := testClient()
		stopped := false
		client.Run = func(ctx context.Context, args ...string) ([]byte, error) {
			if args[0] == "browsers" {
				if stage == "browsers_failed" {
					return nil, errors.New("private")
				}
				if stage == "no_browser" {
					return []byte(`[]`), nil
				}
				return []byte(`[{"instance_id":"first"}]`), nil
			}
			if args[1] == "stop" {
				stopped = true
			}
			return []byte(`{}`), nil
		}
		_, err := client.FetchBrowser(context.Background(), "", false)
		if err == nil || stopped {
			t.Fatal(err, stopped)
		}
	}
}

func TestCleanupFailureRetriesAndReturnsError(t *testing.T) {
	client := testClient()
	stops := 0
	client.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "browsers":
			return []byte(`[{"instance_id":"first"}]`), nil
		case "session":
			if args[1] == "start" {
				return []byte(`{"session_id":"ours"}`), nil
			}
			stops++
			return nil, errors.New("private")
		case "navigate":
			return nil, nil
		default:
			return browserPayload(), nil
		}
	}
	result, err := client.FetchBrowser(context.Background(), "", false)
	requireCode(t, err, "cleanup_failed")
	if stops != 2 || len(result.Windows) != 0 {
		t.Fatal(stops, result)
	}
}

func TestBrowserScriptIsAsyncFixedOriginAndCheckOnly(t *testing.T) {
	full, err := browserScript(false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"(async()=>", `credentials:"include"`, `redirect:"error"`, `new URL(req.url).origin`, "not_logged_in", "!shell()", `req.name==="usage"?30000:5000`, "signal:controller.signal", "clearTimeout(timer)", url.QueryEscape(Calls()[0].API)} {
		if !strings.Contains(full, want) {
			t.Fatalf("missing %q", want)
		}
	}
	for _, forbidden := range []string{"document.cookie", "localStorage", "sessionStorage", "Authorization", "console.log"} {
		if strings.Contains(full, forbidden) {
			t.Fatal(forbidden)
		}
	}
	check, err := browserScript(true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(check, url.QueryEscape(Calls()[1].API)) || !strings.Contains(check, url.QueryEscape(Calls()[0].API)) {
		t.Fatal("check requested enrichment")
	}
}

func TestEvaluationEnvelopeAndEncodedValue(t *testing.T) {
	payload := `{"usage":{}}`
	encoded, _ := json.Marshal(payload)
	for _, body := range [][]byte{[]byte(payload), encoded, []byte(`{"ok":true,"value":` + payload + `}`), []byte(`{"ok":true,"value":` + string(encoded) + `}`), []byte(`{"ok":true,"data":` + payload + `}`), []byte(`{"ok":true,"data":{"value":` + payload + `}}`)} {
		got, err := evaluationValue(body)
		if err != nil || string(got) != payload {
			t.Fatal(string(got), err)
		}
	}
	for _, body := range []string{"garbage", `{"ok":false,"error":"private"}`, `{"ok":true}`} {
		_, err := evaluationValue([]byte(body))
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal(err)
		}
	}
}

func TestBoundedBrowserOutput(t *testing.T) {
	var output boundedOutput
	if _, err := output.Write([]byte("small")); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write(bytes.Repeat([]byte("x"), maxResponseBytes)); err == nil {
		t.Fatal("unbounded output")
	}
}

func TestCommandHelperProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--quota-test-helper" {
		return
	}
	if os.Args[len(os.Args)-1] == "wait" {
		time.Sleep(10 * time.Second)
		return
	}
	fmt.Fprint(os.Stdout, `{"session_id":"ours"}`)
	fmt.Fprint(os.Stderr, "Cookie=private-secret")
	if os.Args[len(os.Args)-1] == "fail" {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestNativeRunnerPreservesSessionIDOnErrorAndSanitizesDiagnostics(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"success", "fail"} {
		body, runErr := runCommand(context.Background(), executable, "-test.run=^TestCommandHelperProcess$", "--", "--quota-test-helper", mode)
		if string(body) != `{"session_id":"ours"}` || strings.Contains(string(body), "private-secret") {
			t.Fatal(string(body), runErr)
		}
		if mode == "success" && runErr != nil {
			t.Fatal(runErr)
		}
		if mode == "fail" {
			requireCode(t, runErr, "bsk_unavailable")
			if strings.Contains(runErr.Error(), "private-secret") {
				t.Fatal(runErr)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = runCommand(ctx, executable, "-test.run=^TestCommandHelperProcess$", "--", "--quota-test-helper", "wait")
	requireCode(t, err, "bsk_unavailable")
}

func TestCleanupTimeoutIsBoundedAndRetried(t *testing.T) {
	client := testClient()
	client.cleanupBudget = 10 * time.Millisecond
	stops := 0
	client.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "browsers":
			return []byte(`[{"instance_id":"first"}]`), nil
		case "session":
			if args[1] == "start" {
				return []byte(`{"session_id":"ours"}`), nil
			}
			stops++
			<-ctx.Done()
			return nil, ctx.Err()
		case "navigate":
			return nil, nil
		default:
			return browserPayload(), nil
		}
	}
	_, err := client.FetchBrowser(context.Background(), "", false)
	requireCode(t, err, "cleanup_failed")
	if stops != 2 {
		t.Fatal(stops)
	}
}

func TestCLITimeoutReservesCleanupBeforeHostDeadline(t *testing.T) {
	client := testClient()
	stopped := false
	client.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "browsers" {
			return []byte(`[{"instance_id":"first"}]`), nil
		}
		if args[0] == "session" {
			if args[1] == "start" {
				return []byte(`{"session_id":"ours"}`), nil
			}
			stopped = true
			if ctx.Err() != nil {
				t.Fatal("cleanup already expired")
			}
			return nil, nil
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 100*time.Millisecond {
			t.Fatal("work deadline did not reserve cleanup time")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	var stdout, stderr bytes.Buffer
	code := runCLI(context.Background(), []string{"--timeout", "150ms"}, &stdout, &stderr, client)
	if code != 1 || !stopped {
		t.Fatal(code, stopped, stdout.String())
	}
	var f Failure
	_ = json.Unmarshal(stdout.Bytes(), &f)
	if f.Code != "timeout" {
		t.Fatal(f)
	}
}
