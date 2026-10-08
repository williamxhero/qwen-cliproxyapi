package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestCredentialRouteImportedDuplicateAndSafeLabel(t *testing.T) {
	m, f := credentialHost(t)
	record := []byte(`{"type":"qwen","id":"imported","label":"Imported","api_key":"dummy-panel-key","base_url":"https://imported.test/v1"}`)
	if err := m.bridge.AuthSave(context.Background(), pluginapi.HostAuthSaveRequest{Name: "imported.json", JSON: record}); err != nil {
		t.Fatal(err)
	}
	resp := credentialPost(t, m, `{"base_url":"https://imported.test/v1/","api_key":"dummy-panel-key"}`)
	if resp.StatusCode != 409 || len(f.callsOf(pluginabi.MethodHostAuthSave)) != 1 {
		t.Fatalf("imported duplicate accepted: %s", resp.Body)
	}
	resp = credentialPost(t, m, `{"base_url":"https://other.test","api_key":"dummy-panel-key","name":"dummy-panel-key"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("POST failed: %s", resp.Body)
	}
	if strings.Contains(string(resp.Body), "dummy-panel-key") {
		t.Fatal("label echoed key")
	}
}

func TestCredentialRouteUnreadableOrInvalidExistingStorage(t *testing.T) {
	for name, path := range map[string]string{"relative": "relative.json", "missing": "C:/missing-qwen-test-file.json"} {
		t.Run(name, func(t *testing.T) {
			f := &fakeCaller{responder: func(string, []byte) ([]byte, error) {
				return hostOK(hostAuthListResponse{Files: []pluginapi.HostAuthFileEntry{{Provider: "qwen", Path: path}}}), nil
			}}
			m := NewManager(NewHostBridge(f.call))
			m.cfg = providerConfig(t)
			resp := credentialPost(t, m, `{"base_url":"https://example.test","api_key":"dummy-panel-key"}`)
			if resp.StatusCode != 502 || len(f.callsOf(pluginabi.MethodHostAuthSave)) != 0 {
				t.Fatalf("unsafe duplicate check: %s", resp.Body)
			}
		})
	}
	m, _ := credentialHost(t)
	if err := m.bridge.AuthSave(context.Background(), pluginapi.HostAuthSaveRequest{Name: "invalid.json", JSON: []byte(`{"type":"qwen","api_key":42}`)}); err != nil {
		t.Fatal(err)
	}
	if resp := credentialPost(t, m, `{"base_url":"https://example.test","api_key":"dummy-panel-key"}`); resp.StatusCode != 502 {
		t.Fatal("invalid stored credential ignored")
	}
}

func TestExecutorCredentialAttributesOverrideStorageAndRejectInvalidURL(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonstream", true: "stream"}[stream], func(t *testing.T) {
			m, _ := newExecutorManager(t, nil)
			var req executorRequest
			_ = json.Unmarshal(executionBody("openai", `{"messages":[]}`, stream), &req)
			req.AuthAttributes["base_url"] = "http://attribute.test/v1/"
			req.StorageJSON = []byte(`{"base_url":"https://storage.test/v1"}`)
			resolved, fail := m.resolveExecution(req)
			if resolved == nil || resolved.cfg.BaseURL != "http://attribute.test/v1" {
				t.Fatalf("attribute lost: %s", fail)
			}
			req.AuthAttributes["base_url"] = "https://user:dummy-selected-key@example.test"
			resolved, fail = m.resolveExecution(req)
			if resolved != nil || strings.Contains(string(fail), "dummy-selected-key") {
				t.Fatal("invalid URL accepted or key leaked")
			}
			delete(req.AuthAttributes, "base_url")
			req.StorageJSON = []byte(`{`)
			if resolved, _ = m.resolveExecution(req); resolved != nil {
				t.Fatal("malformed storage accepted")
			}
		})
	}
}

func TestAuthParseCredentialURLAndLabelSurviveRefresh(t *testing.T) {
	cfg := providerConfig(t)
	raw := []byte(`{"type":"qwen","id":"panel","label":"Panel label","api_key":"dummy-config-key","base_url":"https://panel.test/v1"}`)
	parsed, err := (authProvider{cfg: cfg}).ParseAuth(context.Background(), pluginapi.AuthParseRequest{Provider: "qwen", RawJSON: raw})
	if err != nil || parsed.Auth.Label != "Panel label" {
		t.Fatalf("panel label replaced: %#v %v", parsed, err)
	}
	refreshed, err := (authProvider{}).RefreshAuth(context.Background(), pluginapi.AuthRefreshRequest{AuthProvider: "qwen", AuthID: "panel", Attributes: parsed.Auth.Attributes, StorageJSON: parsed.Auth.StorageJSON})
	if err != nil || refreshed.Auth.Attributes["base_url"] != "https://panel.test/v1" || string(refreshed.Auth.StorageJSON) != string(raw) {
		t.Fatal("refresh lost URL")
	}
	if _, err := (authProvider{}).ParseAuth(context.Background(), pluginapi.AuthParseRequest{Provider: "qwen", RawJSON: []byte(`{"type":"qwen","api_key":"dummy-panel-key","base_url":"https://example.test/?key=dummy-panel-key"}`)}); err == nil || strings.Contains(err.Error(), "dummy-panel-key") {
		t.Fatal("invalid stored URL accepted or key leaked")
	}
}

func TestQuotaPageIncludesPanelCredential(t *testing.T) {
	m := managementCommandManager(t, "success")
	original := m.bridge
	entries, err := original.AuthList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entries = append(entries, pluginapi.HostAuthFileEntry{Provider: "qwen", ID: "panel-id", Label: "Panel", AuthIndex: "panel-index"})
	m.bridge = NewHostBridge(func(string, []byte) ([]byte, error) { return hostOK(hostAuthListResponse{Files: entries}), nil })
	resp, cards := managementUsage(t, m, `{"auth_index":"panel-index"}`)
	if resp.StatusCode != 200 || len(cards) != 1 || cards[0].Label != "Panel" || cards[0].Error != nil {
		t.Fatalf("panel credential missing quota: %s", resp.Body)
	}
}
