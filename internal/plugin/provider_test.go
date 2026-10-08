package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"qwen-cliproxyapi/internal/catalog"
	"qwen-cliproxyapi/internal/config"
)

const providerYAML = "api-keys: [{value: dummy-config-key}]\ncommand: quota-stub\n"

func providerConfig(t *testing.T) config.Config {
	t.Helper()
	c, e := config.Load([]byte(providerYAML))
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func lifecycleBody(body string) []byte { return mustJSON(lifecycleRequest{ConfigYAML: []byte(body)}) }
func newExecutorManager(t *testing.T, responder RawCaller) (*Manager, *fakeCaller) {
	t.Helper()
	f := &fakeCaller{responder: responder}
	m := NewManager(NewHostBridge(f.call))
	m.cfg = providerConfig(t)
	m.mgr = catalog.New(m.cfg, nil)
	t.Cleanup(func() { _, _ = m.HandleCall(pluginabi.MethodPluginShutdown, nil) })
	return m, f
}
func executionBody(format, body string, stream bool) []byte {
	return mustJSON(executorRequest{ExecutorRequest: pluginapi.ExecutorRequest{AuthID: "selected", AuthProvider: ProviderID, AuthAttributes: map[string]string{"api_key": "dummy-selected-key"}, Model: "qwen/qwen3.8-max", SourceFormat: format, OriginalRequest: []byte(body), Stream: stream}, StreamID: "downstream"})
}

func TestProviderRegistrationAndCredentialMaterialization(t *testing.T) {
	f := &fakeCaller{responder: func(method string, _ []byte) ([]byte, error) {
		if method == pluginabi.MethodHostHTTPDo {
			return hostOK(pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[{"id":"qwen3.8-max"}]}`)}), nil
		}
		return hostOK(map[string]any{}), nil
	}}
	m := NewManager(NewHostBridge(f.call))
	defer m.HandleCall(pluginabi.MethodPluginShutdown, nil)
	yaml := providerYAML
	raw, _ := m.HandleCall(pluginabi.MethodPluginRegister, lifecycleBody(yaml))
	var registration registrationResult
	decodeResult(t, raw, &registration)
	cap := registration.Capabilities
	if !cap.AuthProvider || !cap.ModelProvider || !cap.Executor || !cap.QuotaProvider || !strings.Contains(strings.Join(cap.ExecutorInputFormats, ","), "anthropic.messages") || registration.Metadata.GitHubRepository != "https://github.com/williamxhero/qwen-cliproxyapi" {
		t.Fatalf("bad registration: %#v", registration)
	}
	calls := f.callsOf(pluginabi.MethodHostAuthSave)
	if len(calls) != 1 {
		t.Fatalf("auth save calls: %d", len(calls))
	}
	var saved pluginapi.HostAuthSaveRequest
	if err := json.Unmarshal(calls[0].payload, &saved); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("dummy-config-key"))
	id := "qwen-key-" + hex.EncodeToString(digest[:])
	var record map[string]string
	_ = json.Unmarshal(saved.JSON, &record)
	if saved.Name != id+".json" || record["id"] != id || record["label"] != "Qwen 1" || record["type"] != "qwen" {
		t.Fatalf("bad credential: %s %s", saved.Name, saved.JSON)
	}
	raw, _ = m.HandleCall(pluginabi.MethodPluginReconfigure, lifecycleBody(yaml))
	if !decodeEnv(t, raw).OK || len(f.callsOf(pluginabi.MethodHostAuthSave)) != 1 {
		t.Fatal("reconfigure overwrote existing credential")
	}
	raw, _ = m.HandleCall(pluginabi.MethodModelForAuth, []byte(`{}`))
	var models pluginapi.ModelResponse
	decodeResult(t, raw, &models)
	if models.Provider != "qwen" || len(models.Models) != 1 {
		t.Fatalf("models: %#v", models)
	}
}

func TestCredentialStableNamesOrderAndLabels(t *testing.T) {
	f := &fakeCaller{}
	m := NewManager(NewHostBridge(f.call))
	c := providerConfig(t)
	c.APIKeys = []config.APIKey{{Value: "dummy-a"}, {Value: "dummy-b", Name: "Work"}}
	if err := m.materializeAuthRecords(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	calls := f.callsOf(pluginabi.MethodHostAuthSave)
	for i, call := range calls {
		var saved pluginapi.HostAuthSaveRequest
		_ = json.Unmarshal(call.payload, &saved)
		var record map[string]string
		_ = json.Unmarshal(saved.JSON, &record)
		want := []string{"Qwen 1", "Work"}[i]
		if record["label"] != want || saved.Name != record["id"]+".json" {
			t.Fatalf("bad credential: %s", saved.JSON)
		}
	}
	c.APIKeys[0], c.APIKeys[1] = c.APIKeys[1], c.APIKeys[0]
	if err := m.materializeAuthRecords(context.Background(), c); err != nil || len(f.callsOf(pluginabi.MethodHostAuthSave)) != 2 {
		t.Fatal("reordered keys rematerialized")
	}
}

func TestCredentialExistingDiskIdentityAndSafeFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.json")
	if e := os.WriteFile(path, []byte(`{"type":"qwen","api_key":"dummy-config-key"}`), 0600); e != nil {
		t.Fatal(e)
	}
	f := &fakeCaller{responder: func(method string, _ []byte) ([]byte, error) {
		if method == pluginabi.MethodHostAuthList {
			return hostOK(hostAuthListResponse{Files: []pluginapi.HostAuthFileEntry{{Provider: "qwen", Path: path, Name: "old.json"}}}), nil
		}
		return hostOK(map[string]any{}), nil
	}}
	m := NewManager(NewHostBridge(f.call))
	if e := m.materializeAuthRecords(context.Background(), providerConfig(t)); e != nil || len(f.callsOf(pluginabi.MethodHostAuthSave)) != 0 {
		t.Fatalf("disk identity failed: %v", e)
	}
	f.responder = func(string, []byte) ([]byte, error) { return hostErr("save", "dummy-config-key"), nil }
	if e := m.materializeAuthRecords(context.Background(), providerConfig(t)); e == nil || strings.Contains(e.Error(), "dummy-config-key") {
		t.Fatalf("unsafe materialize failure: %v", e)
	}
}

func TestLifecycleFallbackReconfigureAndInvalidConfig(t *testing.T) {
	m := NewManager(nil)
	defer m.HandleCall(pluginabi.MethodPluginShutdown, nil)
	raw, _ := m.HandleCall(pluginabi.MethodPluginRegister, lifecycleBody(providerYAML))
	if !decodeEnv(t, raw).OK || len(m.mgr.Models()) != 3 {
		t.Fatal("startup fallback missing")
	}
	raw, _ = m.HandleCall(pluginabi.MethodPluginReconfigure, lifecycleBody(providerYAML+"model-prefix: {value: custom}\n"))
	if !decodeEnv(t, raw).OK {
		t.Fatal(string(raw))
	}
	if _, ok := m.mgr.Lookup("custom/qwen3.8-max"); !ok {
		t.Fatal("stale prefix not rebuilt")
	}
	raw, _ = m.HandleCall(pluginabi.MethodPluginReconfigure, lifecycleBody(providerYAML+"catalog: {stale-while-unavailable: false}\n"))
	if !decodeEnv(t, raw).OK || len(m.mgr.Models()) != 0 {
		t.Fatal("fail closed ignored")
	}
	raw, _ = m.HandleCall(pluginabi.MethodPluginReconfigure, lifecycleBody("api-keys: []"))
	if decodeEnv(t, raw).OK {
		t.Fatal("invalid config accepted")
	}
}

func TestExecutorSelectedKeyOpenAIAndCanonicalFormats(t *testing.T) {
	for _, format := range []string{"openai", "openai.chat_completions"} {
		t.Run(format, func(t *testing.T) {
			m, f := newExecutorManager(t, func(method string, payload []byte) ([]byte, error) {
				if method == pluginabi.MethodHostHTTPDo {
					var req hostHTTPReq
					_ = json.Unmarshal(payload, &req)
					if req.URL != config.DefaultBaseURL+"/chat/completions" || req.Headers.Get("Authorization") != "Bearer dummy-selected-key" || req.Headers.Get("Content-Type") != "application/json" {
						t.Fatalf("wrong execution request: %#v", req)
					}
					if !strings.Contains(string(req.Body), `"model":"qwen3.8-max"`) || !strings.Contains(string(req.Body), `"tools"`) || !strings.Contains(string(req.Body), `image_url`) {
						t.Fatalf("payload lost fields: %s", req.Body)
					}
					return hostOK(pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}), nil
				}
				return hostOK(map[string]any{}), nil
			})
			raw, _ := m.HandleCall(pluginabi.MethodExecutorExecute, executionBody(format, `{"model":"ignored","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}],"tools":[{"type":"function","function":{"name":"f"}}]}`, false))
			if !decodeEnv(t, raw).OK || len(f.callsOf(pluginabi.MethodHostHTTPDo)) != 1 {
				t.Fatalf("execute failed: %s", raw)
			}
		})
	}
}

func TestExecutorAnthropicToolsImagesAndResponse(t *testing.T) {
	for _, format := range []string{"claude", "anthropic.messages"} {
		t.Run(format, func(t *testing.T) {
			m, _ := newExecutorManager(t, func(method string, payload []byte) ([]byte, error) {
				if method == pluginabi.MethodHostHTTPDo {
					var req hostHTTPReq
					_ = json.Unmarshal(payload, &req)
					body := string(req.Body)
					for _, want := range []string{`image_url`, `data:image/png;base64,AAAA`, `tool_calls`, `tool_call_id`, `"tools"`, `"system"`} {
						if !strings.Contains(body, want) {
							t.Fatalf("missing %s: %s", want, body)
						}
					}
					return hostOK(pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"id":"r","model":"qwen3.8-max","choices":[{"message":{"content":"ok","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":2,"completion_tokens":3}}`)}), nil
				}
				return hostOK(map[string]any{}), nil
			})
			body := `{"system":"instructions","max_tokens":16,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]},{"role":"assistant","content":[{"type":"tool_use","id":"old","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"old","content":"result"}]}],"tools":[{"name":"f","input_schema":{"type":"object"}}]}`
			raw, _ := m.HandleCall(pluginabi.MethodExecutorExecute, executionBody(format, body, false))
			var response pluginapi.ExecutorResponse
			decodeResult(t, raw, &response)
			if !strings.Contains(string(response.Payload), `"type":"tool_use"`) || !strings.Contains(string(response.Payload), `"stop_reason":"tool_use"`) {
				t.Fatalf("bad response: %s", response.Payload)
			}
		})
	}
}

func TestExecutorErrorPropagationAndNoDefaultKey(t *testing.T) {
	m, _ := newExecutorManager(t, func(method string, _ []byte) ([]byte, error) {
		if method == pluginabi.MethodHostHTTPDo {
			return hostOK(pluginapi.HTTPResponse{StatusCode: 429, Body: []byte(`{"error":{"message":"quota exhausted dummy-selected-key"}}`)}), nil
		}
		return hostOK(map[string]any{}), nil
	})
	raw, _ := m.HandleCall(pluginabi.MethodExecutorExecute, executionBody("openai", `{"messages":[]}`, false))
	env := decodeEnv(t, raw)
	if env.OK || env.Error.HTTPStatus != 429 || !env.Error.Retryable || env.Error.Code != "quota_exhaustion" || strings.Contains(env.Error.Message, "dummy-selected-key") {
		t.Fatalf("error not propagated safely: %s", raw)
	}
	var req executorRequest
	_ = json.Unmarshal(executionBody("openai", `{}`, false), &req)
	req.AuthAttributes = nil
	raw, _ = m.HandleCall(pluginabi.MethodExecutorExecute, mustJSON(req))
	if e := decodeEnv(t, raw); e.OK || e.Error.Code != "auth_failure" {
		t.Fatal("fell back to configured key")
	}
	req.AuthAttributes = map[string]string{"api_key": "dummy-selected-key"}
	req.AuthProvider = "other"
	raw, _ = m.HandleCall(pluginabi.MethodExecutorExecute, mustJSON(req))
	if decodeEnv(t, raw).OK {
		t.Fatal("accepted foreign credential")
	}
}

func TestExecutorProtocolSwitchesDoNotAffectCatalog(t *testing.T) {
	m, _ := newExecutorManager(t, nil)
	m.cfg.Protocols.Messages = false
	raw, _ := m.HandleCall(pluginabi.MethodExecutorExecute, executionBody("claude", `{}`, false))
	if decodeEnv(t, raw).OK {
		t.Fatal("disabled messages accepted")
	}
	if len(m.mgr.Models()) != 3 {
		t.Fatal("client protocol switch removed models")
	}
	m.cfg.Protocols.ChatCompletions = false
	raw, _ = m.HandleCall(pluginabi.MethodExecutorExecute, executionBody("openai", `{}`, false))
	if decodeEnv(t, raw).OK {
		t.Fatal("disabled openai accepted")
	}
}

func TestExecutorStreamChunkMappingAndTools(t *testing.T) {
	for _, format := range []string{"openai", "anthropic.messages"} {
		t.Run(format, func(t *testing.T) {
			frames := []string{"data: {\"id\":\"r\",\"model\":\"qwen3.8-max\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"function\":{\"name\":\"f\",\"arguments\":\"{\"}}]}}]}\n\n", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n", "data: [DONE]\n\n"}
			i := 0
			m, f := newExecutorManager(t, func(method string, _ []byte) ([]byte, error) {
				switch method {
				case pluginabi.MethodHostHTTPDoStream:
					return hostOK(hostStreamStartResp{StatusCode: 200, StreamID: "upstream"}), nil
				case pluginabi.MethodHostHTTPStreamRead:
					if i < len(frames) {
						body := frames[i]
						i++
						return hostOK(hostStreamReadResp{Payload: []byte(body)}), nil
					}
					return hostOK(hostStreamReadResp{Done: true}), nil
				}
				return hostOK(map[string]any{}), nil
			})
			raw, _ := m.HandleCall(pluginabi.MethodExecutorExecuteStream, executionBody(format, `{"max_tokens":16,"messages":[],"stream":true}`, true))
			if !decodeEnv(t, raw).OK {
				t.Fatal(string(raw))
			}
			if !m.bridge.WaitForInFlight(2 * time.Second) {
				t.Fatal("stream did not drain")
			}
			emits := f.callsOf(pluginabi.MethodHostStreamEmit)
			if len(emits) == 0 {
				t.Fatal("stream emitted nothing")
			}
			output := ""
			for _, call := range emits {
				var msg struct {
					Payload  []byte `json:"payload"`
					StreamID string `json:"stream_id"`
				}
				_ = json.Unmarshal(call.payload, &msg)
				if msg.StreamID != "downstream" {
					t.Fatal("used upstream id for downstream emit")
				}
				output += string(msg.Payload)
			}
			if format == "anthropic.messages" && (!strings.Contains(output, "input_json_delta") || !strings.Contains(output, "message_stop") || !strings.Contains(output, "tool_use")) {
				t.Fatalf("bad mapped stream: %s", output)
			}
			if len(f.callsOf(pluginabi.MethodHostHTTPStreamClose)) != 1 || len(f.callsOf(pluginabi.MethodHostStreamClose)) != 1 {
				t.Fatal("streams not closed once")
			}
		})
	}
}

func TestExecutorNetworkErrorsAndMalformedRequests(t *testing.T) {
	m, _ := newExecutorManager(t, func(string, []byte) ([]byte, error) { return nil, errors.New("network failed dummy-selected-key") })
	raw, _ := m.HandleCall(pluginabi.MethodExecutorExecute, executionBody("openai", `{}`, false))
	if e := decodeEnv(t, raw); e.OK || e.Error.Code != "timeout_or_network_failure" || strings.Contains(e.Error.Message, "dummy-selected-key") {
		t.Fatalf("bad network error: %s", raw)
	}
	for _, method := range []string{pluginabi.MethodExecutorExecute, pluginabi.MethodExecutorExecuteStream} {
		raw, _ = m.HandleCall(method, []byte(`bad`))
		if decodeEnv(t, raw).OK {
			t.Fatal("malformed execution accepted")
		}
	}
	raw, _ = m.HandleCall(pluginabi.MethodExecutorExecute, executionBody("unknown", `{}`, false))
	if decodeEnv(t, raw).OK {
		t.Fatal("unknown format accepted")
	}
	var req executorRequest
	_ = json.Unmarshal(executionBody("openai", `{}`, false), &req)
	req.Model = "missing"
	raw, _ = m.HandleCall(pluginabi.MethodExecutorExecute, mustJSON(req))
	if e := decodeEnv(t, raw); e.OK || e.Error.HTTPStatus != 404 {
		t.Fatal("missing model not rejected")
	}
}

func TestProviderQuotaDescribeManagementAndFallbackMethods(t *testing.T) {
	m := NewManager(nil)
	m.cfg = providerConfig(t)
	raw, _ := m.HandleCall(pluginabi.MethodQuotaDescribe, nil)
	var description pluginapi.QuotaDescribeResponse
	decodeResult(t, raw, &description)
	if description.DisplayName != config.DefaultDisplayName || description.SupportsReset {
		t.Fatal("bad quota description")
	}
	for _, method := range []string{pluginabi.MethodQuotaReset, pluginabi.MethodExecutorCountTokens, pluginabi.MethodExecutorHTTPRequest, "unknown"} {
		raw, _ = m.HandleCall(method, nil)
		if decodeEnv(t, raw).OK {
			t.Fatalf("unsupported method succeeded: %s", method)
		}
	}
	raw, _ = m.HandleCall(pluginabi.MethodManagementRegister, []byte(`{}`))
	if !decodeEnv(t, raw).OK {
		t.Fatal(string(raw))
	}
	raw, _ = m.HandleCall(pluginabi.MethodManagementHandle, mustJSON(pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/management/plugins/qwen-cliproxyapi/quota-info"}))
	var response pluginapi.ManagementResponse
	decodeResult(t, raw, &response)
	if response.StatusCode != 200 || !strings.Contains(string(response.Body), "quota/fetch") {
		t.Fatal("management info missing")
	}
}
