package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const credentialPath = "/v0/management/plugins/qwen-cliproxyapi/credentials"

func credentialPost(t *testing.T, m *Manager, body string) pluginapi.ManagementResponse {
	t.Helper()
	raw, err := m.HandleCall(pluginabi.MethodManagementHandle, mustJSON(pluginapi.ManagementRequest{Method: "POST", Path: credentialPath, Body: []byte(body)}))
	if err != nil {
		t.Fatal(err)
	}
	var resp pluginapi.ManagementResponse
	decodeResult(t, raw, &resp)
	if strings.Contains(string(resp.Body), "dummy-panel-key") {
		t.Fatal("response leaked API key")
	}
	return resp
}

func credentialHost(t *testing.T) (*Manager, *fakeCaller) {
	t.Helper()
	dir := t.TempDir()
	var mu sync.Mutex
	var entries []pluginapi.HostAuthFileEntry
	f := &fakeCaller{responder: func(method string, payload []byte) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case pluginabi.MethodHostAuthList:
			return hostOK(hostAuthListResponse{Files: entries}), nil
		case pluginabi.MethodHostAuthSave:
			var req pluginapi.HostAuthSaveRequest
			if err := json.Unmarshal(payload, &req); err != nil {
				return nil, err
			}
			path := filepath.Join(dir, req.Name)
			if err := os.WriteFile(path, req.JSON, 0600); err != nil {
				return nil, err
			}
			var record map[string]string
			_ = json.Unmarshal(req.JSON, &record)
			entries = append(entries, pluginapi.HostAuthFileEntry{ID: record["id"], Name: req.Name, Path: path, Provider: "qwen", Label: record["label"], AuthIndex: record["id"]})
			return hostOK(map[string]any{}), nil
		default:
			return hostOK(map[string]any{}), nil
		}
	}}
	m := NewManager(NewHostBridge(f.call))
	m.cfg = providerConfig(t)
	return m, f
}

func TestCredentialRouteHappyPathAndAuthRoundTrip(t *testing.T) {
	m, f := credentialHost(t)
	resp := credentialPost(t, m, `{"base_url":"https://example.test/v1/","api_key":" dummy-panel-key ","name":"Panel","unknown":"ignored"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("POST: %d %s", resp.StatusCode, resp.Body)
	}
	var result struct {
		OK        bool `json:"ok"`
		ID, Label string
	}
	_ = json.Unmarshal(resp.Body, &result)
	if !result.OK || !strings.HasPrefix(result.ID, "qwen-key-") || result.Label != "Panel" {
		t.Fatalf("wrong result: %s", resp.Body)
	}
	calls := f.callsOf(pluginabi.MethodHostAuthSave)
	if len(calls) != 1 {
		t.Fatal("not saved exactly once")
	}
	var saved pluginapi.HostAuthSaveRequest
	_ = json.Unmarshal(calls[0].payload, &saved)
	var record map[string]string
	_ = json.Unmarshal(saved.JSON, &record)
	if record["type"] != "qwen" || record["label"] != "Panel" || record["api_key"] != "dummy-panel-key" || record["base_url"] != "https://example.test/v1" || saved.Name != result.ID+".json" {
		t.Fatalf("wrong stored credential: %#v", record)
	}
	parsed, err := (authProvider{cfg: m.cfg}).ParseAuth(context.Background(), pluginapi.AuthParseRequest{Provider: "qwen", FileName: saved.Name, RawJSON: saved.JSON})
	if err != nil || !parsed.Handled || parsed.Auth.Attributes["base_url"] != record["base_url"] || parsed.Auth.Label != "Panel" || parsed.Auth.ID != saved.Name {
		t.Fatalf("roundtrip lost credential: %#v %v", parsed, err)
	}
	resp = credentialPost(t, m, `{"base_url":"https://example.test/v1","api_key":"dummy-panel-key","name":"Renamed"}`)
	if resp.StatusCode != 409 || len(f.callsOf(pluginabi.MethodHostAuthSave)) != 1 {
		t.Fatal("duplicate overwrote credential")
	}
	resp = credentialPost(t, m, `{"base_url":"http://other.test/v1","api_key":"dummy-panel-key"}`)
	if resp.StatusCode != 200 || !strings.Contains(string(resp.Body), `"label":"Qwen"`) {
		t.Fatalf("different base URL rejected: %s", resp.Body)
	}
	for _, call := range f.callsOf(pluginabi.MethodHostLog) {
		if strings.Contains(string(call.payload), "dummy-panel-key") {
			t.Fatal("log leaked key")
		}
	}
}

func TestCredentialRouteValidation(t *testing.T) {
	for name, body := range map[string]string{
		"missing_url":    `{"api_key":"dummy-panel-key"}`,
		"empty_url":      `{"base_url":"","api_key":"dummy-panel-key"}`,
		"relative":       `{"base_url":"/v1","api_key":"dummy-panel-key"}`,
		"missing_host":   `{"base_url":"https:///v1","api_key":"dummy-panel-key"}`,
		"empty_hostname": `{"base_url":"https://:443/v1","api_key":"dummy-panel-key"}`,
		"scheme":         `{"base_url":"ftp://example.test","api_key":"dummy-panel-key"}`,
		"userinfo":       `{"base_url":"https://dummy-panel-key@example.test","api_key":"dummy-panel-key"}`,
		"query":          `{"base_url":"https://example.test/?key=dummy-panel-key","api_key":"dummy-panel-key"}`,
		"empty_query":    `{"base_url":"https://example.test/?","api_key":"dummy-panel-key"}`,
		"fragment":       `{"base_url":"https://example.test/#dummy-panel-key","api_key":"dummy-panel-key"}`,
		"missing_key":    `{"base_url":"https://example.test"}`,
		"empty_key":      `{"base_url":"https://example.test","api_key":""}`,
		"whitespace_key": `{"base_url":"https://example.test","api_key":" \t "}`,
		"control_key":    `{"base_url":"https://example.test","api_key":"dummy-panel-key\nextra"}`,
		"malformed":      `{"api_key":"dummy-panel-key"`,
		"wrong_type":     `{"base_url":1,"api_key":"dummy-panel-key"}`,
		"null":           `null`,
	} {
		t.Run(name, func(t *testing.T) {
			m, f := credentialHost(t)
			resp := credentialPost(t, m, body)
			if resp.StatusCode != 400 || len(f.callsOf(pluginabi.MethodHostAuthSave)) != 0 {
				t.Fatalf("invalid body accepted: %d %s", resp.StatusCode, resp.Body)
			}
		})
	}
}

func TestCredentialRouteHostRejectionDoesNotPublishCredentialOrSecret(t *testing.T) {
	for _, method := range []string{pluginabi.MethodHostAuthList, pluginabi.MethodHostAuthSave} {
		t.Run(method, func(t *testing.T) {
			m, f := credentialHost(t)
			original := f.responder
			f.responder = func(called string, payload []byte) ([]byte, error) {
				if called == method {
					return hostErr("failure", "host failure dummy-panel-key"), nil
				}
				return original(called, payload)
			}
			resp := credentialPost(t, m, `{"base_url":"https://example.test/v1","api_key":"dummy-panel-key"}`)
			if resp.StatusCode != 502 || len(f.authFiles) != 0 {
				t.Fatalf("failure not atomic: %d %s", resp.StatusCode, resp.Body)
			}
			for _, call := range f.callsOf(pluginabi.MethodHostLog) {
				if strings.Contains(string(call.payload), "dummy-panel-key") {
					t.Fatal("secret logged")
				}
			}
		})
	}
	m := NewManager(nil)
	if resp := credentialPost(t, m, `{"base_url":"https://example.test","api_key":"dummy-panel-key"}`); resp.StatusCode != 503 {
		t.Fatal("missing host accepted")
	}
}

func TestCredentialRouteConcurrentDuplicate(t *testing.T) {
	m, f := credentialHost(t)
	var wg sync.WaitGroup
	statuses := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := credentialPost(t, m, `{"base_url":"https://example.test","api_key":"dummy-panel-key"}`)
			statuses <- resp.StatusCode
		}()
	}
	wg.Wait()
	close(statuses)
	success := 0
	for status := range statuses {
		if status == 200 {
			success++
		} else if status != 409 {
			t.Fatalf("status %d", status)
		}
	}
	if success != 1 || len(f.callsOf(pluginabi.MethodHostAuthSave)) != 1 {
		t.Fatal("concurrent duplicate saved")
	}
}

func TestCredentialRouteDuplicateOfConfiguredAndImportedKey(t *testing.T) {
	m, f := credentialHost(t)
	if err := m.materializeAuthRecords(context.Background(), m.cfg); err != nil {
		t.Fatal(err)
	}
	resp := credentialPost(t, m, fmt.Sprintf(`{"base_url":%q,"api_key":"dummy-config-key"}`, m.cfg.BaseURL))
	if resp.StatusCode != 409 || len(f.callsOf(pluginabi.MethodHostAuthSave)) != 1 {
		t.Fatalf("configured duplicate accepted: %s", resp.Body)
	}
}

func TestPluginDisplayMetadataAndEmbeddedLogo(t *testing.T) {
	var registration registrationResult
	decodeResult(t, registrationEnvelope(), &registration)
	if registration.Metadata.Name != "QWen Plan API Key" || registration.Metadata.Logo != "/v0/resource/plugins/qwen-cliproxyapi/logo.svg" {
		t.Fatalf("wrong metadata: %#v", registration.Metadata)
	}
	t.Chdir(t.TempDir())
	m := NewManager(nil)
	resp, err := m.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: registration.Metadata.Logo})
	if err != nil || resp.StatusCode != 200 || resp.Headers.Get("Content-Type") != "image/svg+xml" || len(resp.Body) > 600 || !strings.Contains(string(resp.Body), "<svg") {
		t.Fatalf("bad logo: %#v %v", resp, err)
	}
	for _, forbidden := range []string{"href=", "<script", "<image", "http://", "https://"} {
		if strings.Contains(strings.ReplaceAll(string(resp.Body), `xmlns="http://www.w3.org/2000/svg"`, ""), forbidden) {
			t.Fatalf("external/active logo content: %s", forbidden)
		}
	}
	raw, _ := m.HandleCall(pluginabi.MethodManagementRegister, []byte(`{}`))
	if !strings.Contains(string(raw), `"path":"/logo.svg"`) || !strings.Contains(string(raw), `"path":"/plugins/qwen-cliproxyapi/credentials"`) {
		t.Fatalf("routes not registered: %s", raw)
	}
}

func TestLoginStartManualFormMetadata(t *testing.T) {
	m := NewManager(nil)
	raw, _ := m.HandleCall(pluginabi.MethodAuthLoginStart, []byte(`{"Provider":"qwen"}`))
	var result pluginapi.AuthLoginStartResponse
	decodeResult(t, raw, &result)
	want := `{"auth_kind":"manual_api_key","submit_path":"/v0/management/plugins/qwen-cliproxyapi/credentials","submit_label":"添加凭证","fields":[{"name":"base_url","label":"Base URL","placeholder":"https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1","required":true},{"name":"api_key","label":"API Key","type":"password","required":true},{"name":"name","label":"凭证名称","required":false}]}`
	var wantValue, gotValue any
	_ = json.Unmarshal([]byte(want), &wantValue)
	_ = json.Unmarshal(mustJSON(result.Metadata), &gotValue)
	if string(mustJSON(gotValue)) != string(mustJSON(wantValue)) {
		t.Fatalf("wrong metadata: %s", mustJSON(result.Metadata))
	}
}

func TestExecutorCredentialBaseURLPrecedenceAndFallback(t *testing.T) {
	for name, storage := range map[string]string{"credential": `{"base_url":"https://credential.test/v1"}`, "legacy": `{"type":"qwen"}`} {
		t.Run(name, func(t *testing.T) {
			m, f := newExecutorManager(t, func(method string, payload []byte) ([]byte, error) {
				if method == pluginabi.MethodHostHTTPDo {
					return hostOK(pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"id":"test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)}), nil
				}
				return hostOK(map[string]any{}), nil
			})
			var req executorRequest
			_ = json.Unmarshal(executionBody("openai", `{"messages":[{"role":"user","content":"hi"}]}`, false), &req)
			req.StorageJSON = []byte(storage)
			raw, _ := m.HandleCall(pluginabi.MethodExecutorExecute, mustJSON(req))
			if !decodeEnv(t, raw).OK {
				t.Fatalf("execution failed: %s", raw)
			}
			calls := f.callsOf(pluginabi.MethodHostHTTPDo)
			var upstream pluginapi.HTTPRequest
			_ = json.Unmarshal(calls[0].payload, &upstream)
			want := m.cfg.BaseURL + "/chat/completions"
			if name == "credential" {
				want = "https://credential.test/v1/chat/completions"
			}
			if upstream.URL != want {
				t.Fatalf("URL %s want %s", upstream.URL, want)
			}
		})
	}
}
