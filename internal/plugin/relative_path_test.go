package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// The live host reports plugin-managed auth records with a path relative to its own
// working directory. Creating a credential (and rejecting a duplicate) must keep
// working instead of failing with "cannot inspect existing credentials".
func TestCredentialRouteToleratesRelativeHostPaths(t *testing.T) {
	dir, err := os.MkdirTemp(".", "relpath-qwen-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	absDir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	const existingKey = "sk-sp-existing-dummy"
	const baseURL = "https://dummy.invalid/v1"
	digest := sha256.Sum256([]byte(existingKey))
	id := "qwen-key-" + hex.EncodeToString(digest[:])
	record, _ := json.Marshal(map[string]string{
		"type": "qwen", "id": id, "api_key": existingKey, "base_url": baseURL, "label": "Existing",
	})
	abs := filepath.Join(absDir, "Existing.json")
	if err := os.WriteFile(abs, record, 0600); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(cwd, abs)
	if err != nil {
		t.Fatalf("cannot build a relative path: %v", err)
	}

	saves := 0
	f := &fakeCaller{responder: func(method string, _ []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodHostAuthList:
			return hostOK(hostAuthListResponse{Files: []pluginapi.HostAuthFileEntry{
				{Provider: "qwen", ID: id, Name: "Existing.json", Path: rel},
			}}), nil
		case pluginabi.MethodHostAuthSave:
			saves++
			return hostOK(map[string]any{}), nil
		}
		return hostOK(map[string]any{}), nil
	}}
	m := NewManager(NewHostBridge(f.call))
	m.cfg = providerConfig(t)

	// (a) a brand-new key is created even though an existing record has a relative path.
	resp := credentialPost(t, m, `{"base_url":"`+baseURL+`","api_key":"brand-new-dummy-key","name":"Panel"}`)
	if resp.StatusCode != 200 || saves != 1 {
		t.Fatalf("create with a relative host path failed: status=%d saves=%d body=%s", resp.StatusCode, saves, resp.Body)
	}

	// (b) the credential already on disk is still rejected as a duplicate.
	resp = credentialPost(t, m, `{"base_url":"`+baseURL+`","api_key":"`+existingKey+`"}`)
	if resp.StatusCode != 409 {
		t.Fatalf("duplicate not detected with a relative host path: status=%d body=%s", resp.StatusCode, resp.Body)
	}
	if saves != 1 {
		t.Fatalf("duplicate was persisted: saves=%d", saves)
	}
}
