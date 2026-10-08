package bailianquota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"time"
)

type CommandRunner func(context.Context, ...string) ([]byte, error)

// boundedOutput prevents browser output from growing without limit. Stderr is
// discarded because browser/tool failures can contain sensitive page contents.
type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxResponseBytes {
		return 0, errors.New("browser output exceeds limit")
	}
	return b.Buffer.Write(p)
}

func runBSK(ctx context.Context, args ...string) ([]byte, error) {
	return runCommand(ctx, DefaultBSK, args...)
}

func runCommand(ctx context.Context, executable string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	var output boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		// Preserve a session ID emitted before failure so the caller can stop
		// that session. No caller relays this raw output into error messages.
		return output.Bytes(), failure("bsk_unavailable", "bsk command failed; check the daemon and browser extension")
	}
	return output.Bytes(), nil
}

func SelectBrowser(raw []byte, requested string) (string, error) {
	var browsers []struct {
		ID           string `json:"instance_id"`
		Label        string `json:"label"`
		Unresponsive bool   `json:"unresponsive"`
	}
	if json.Unmarshal(raw, &browsers) != nil {
		return "", failure("invalid_response", "bsk returned an invalid browser list")
	}
	if requested == "" {
		if len(browsers) > 0 && browsers[0].ID != "" && !browsers[0].Unresponsive {
			return browsers[0].ID, nil
		}
		return "", failure("no_browser", "no browser connected; open the browser with its bsk extension")
	}
	selected := ""
	for _, browser := range browsers {
		if browser.ID != "" && !browser.Unresponsive && (browser.ID == requested || browser.Label == requested) {
			if selected != "" {
				return "", failure("no_browser", "browser label is ambiguous; use its instance ID")
			}
			selected = browser.ID
		}
	}
	if selected == "" {
		return "", failure("no_browser", "requested browser is not connected or responsive")
	}
	return selected, nil
}

func (c *Client) FetchBrowser(ctx context.Context, browser string, check bool) (result Result, err error) {
	body, runErr := c.Run(ctx, "browsers", "--json")
	if runErr != nil {
		return Result{}, browserFailure(ctx, "bsk_unavailable", "cannot list connected browsers")
	}
	browser, err = SelectBrowser(body, browser)
	if err != nil {
		return Result{}, err
	}
	body, runErr = c.Run(ctx, "session", "start", "--json", "--browser", browser, "--no-focus")
	// If bsk returns a usable ID even alongside a process error, still clean it
	// up. Without an ID we must not stop any other process's session.
	var session struct {
		ID string `json:"session_id"`
	}
	_ = json.Unmarshal(body, &session)
	session.ID = strings.TrimSpace(session.ID)
	if session.ID != "" {
		defer func() {
			budget := c.cleanupBudget
			if budget <= 0 {
				budget = 10 * time.Second
			}
			cleanup, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			_, stopErr := c.Run(cleanup, "session", "stop", session.ID)
			if stopErr != nil {
				_, stopErr = c.Run(cleanup, "session", "stop", session.ID)
			}
			if stopErr != nil && err == nil {
				result = Result{}
				err = failure("cleanup_failed", "could not stop this invocation's browser session; inspect bsk session list")
			}
		}()
	}
	if runErr != nil || session.ID == "" {
		return Result{}, browserFailure(ctx, "session_failed", "bsk could not start an unfocused session")
	}
	if _, runErr = c.Run(ctx, "navigate", ConsoleURL, "--session", session.ID); runErr != nil {
		return Result{}, browserFailure(ctx, "navigation_failed", "cannot navigate to the Bailian console")
	}
	script, scriptErr := browserScript(check)
	if scriptErr != nil {
		return Result{}, scriptErr
	}
	body, runErr = c.Run(ctx, "evaluate", script, "--session", session.ID, "--json", "--timeout", "60s")
	if runErr != nil {
		return Result{}, browserFailure(ctx, "browser_failed", "Bailian console evaluation failed")
	}
	payload, decodeErr := evaluationValue(body)
	if decodeErr != nil {
		return Result{}, decodeErr
	}
	var responses map[string]json.RawMessage
	if json.Unmarshal(payload, &responses) != nil {
		return Result{}, failure("invalid_response", "browser returned invalid console JSON")
	}
	var code string
	_ = json.Unmarshal(responses["error"], &code)
	if code != "" {
		if code == "not_logged_in" {
			return Result{}, failure("not_logged_in", "Bailian console is not logged in; log in manually in the selected browser")
		}
		return Result{}, failure("console_error", "browser could not reach the Bailian console RPC")
	}
	return ParseResponses("bsk", responses, c.Now(), check)
}

func browserFailure(ctx context.Context, code, message string) error {
	if ctx.Err() != nil {
		return failure("timeout", "browser operation was canceled or exceeded --timeout")
	}
	return failure(code, message)
}

func evaluationValue(body []byte) ([]byte, error) {
	var encoded string
	if json.Unmarshal(body, &encoded) == nil {
		body = []byte(encoded)
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
		return nil, failure("invalid_response", "bsk evaluation returned invalid JSON")
	}
	// bsk versions emit an ok/value envelope, an ok/data envelope, or the
	// resolved value directly. Never confuse ok:false with process success.
	if raw, present := envelope["ok"]; present {
		var ok bool
		if json.Unmarshal(raw, &ok) != nil || !ok {
			return nil, failure("browser_failed", "bsk evaluation was unsuccessful")
		}
		body = envelope["value"]
		if len(body) == 0 {
			body = envelope["data"]
			var data map[string]json.RawMessage
			if json.Unmarshal(body, &data) == nil && len(data["value"]) != 0 {
				body = data["value"]
			}
		}
	}
	if len(body) == 0 || bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		return nil, failure("invalid_response", "bsk evaluation returned no value")
	}
	if json.Unmarshal(body, &encoded) == nil {
		body = []byte(encoded)
	}
	return body, nil
}

func browserScript(check bool) (string, error) {
	type browserRequest struct {
		Name string `json:"name"`
		URL  string `json:"url"`
		Body string `json:"body"`
	}
	calls := Calls()
	if check {
		calls = calls[:1]
	}
	requests := make([]browserRequest, 0, len(calls))
	for _, call := range calls {
		req, err := NewRPCRequest(context.Background(), call)
		if err != nil {
			return "", err
		}
		body, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return "", failure("invalid_request", "cannot assemble browser request")
		}
		requests = append(requests, browserRequest{call.Name, req.URL.String(), string(body)})
	}
	raw, err := json.Marshal(requests)
	if err != nil {
		return "", failure("invalid_request", "cannot assemble browser requests")
	}
	return `(async()=>{
const expected="bailian.console.aliyun.com";
const text=()=>document.body?.innerText||"";
const login=()=>location.hostname!==expected||/login|signin|passport/i.test(location.pathname)||/请先登录|账号登录|登录阿里云/.test(text())&&!/Token Plan|订阅管理|模型广场/.test(text());
const shell=()=>!!document.querySelector("#root,#app,[class*='console'],[class*='layout']")&&text().trim().length>0;
for(let n=0;n<30&&!login()&&!shell();n++)await new Promise(resolve=>setTimeout(resolve,200));
if(login()||!shell())return {error:"not_logged_in"};
const out={};
for(const req of ` + string(raw) + `){
const controller=new AbortController();
const timer=setTimeout(()=>controller.abort(),req.name==="usage"?30000:5000);
try{
if(new URL(req.url).origin!=="https://bailian-cs.console.aliyun.com")return {error:"console_error"};
const response=await fetch(req.url,{method:"POST",credentials:"include",redirect:"error",signal:controller.signal,headers:{"Content-Type":"application/x-www-form-urlencoded"},body:req.body});
if(!response.ok){if(req.name==="usage")return {error:response.status===401||response.status===403?"not_logged_in":"console_error"};continue;}
out[req.name]=await response.json();
}catch(e){if(req.name==="usage")return {error:"console_error"};}
finally{clearTimeout(timer);}
}
return out;
})()`, nil
}
