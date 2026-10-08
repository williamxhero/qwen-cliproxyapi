package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"qwen-cliproxyapi/internal/config"
)

// The record is saved by the host, using the same storage schema as configured
// keys. base_url survives host reloads in StorageJSON and auth.parse attributes.
type credentialRecord struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Label   string `json:"label"`
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url,omitempty"`
}

func (m *Manager) writeCredential(ctx context.Context, body []byte) (pluginapi.ManagementResponse, error) {
	var input struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		Name    string `json:"name"`
	}
	failure := func(status int, reason string) (pluginapi.ManagementResponse, error) {
		return quotaJSON(status, map[string]string{"error": reason})
	}
	if json.Unmarshal(body, &input) != nil {
		return failure(http.StatusBadRequest, "invalid request")
	}
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	if config.ValidateCredentialBaseURL(input.BaseURL) != nil {
		return failure(http.StatusBadRequest, "invalid base_url")
	}
	input.APIKey = strings.TrimSpace(input.APIKey)
	if input.APIKey == "" {
		return failure(http.StatusBadRequest, "api_key is required")
	}
	if strings.ContainsAny(input.APIKey, "\r\n\x00") {
		return failure(http.StatusBadRequest, "invalid api_key")
	}
	if strings.Contains(input.BaseURL, input.APIKey) {
		return failure(http.StatusBadRequest, "base_url must not contain api_key")
	}
	if m.bridge == nil {
		return failure(http.StatusServiceUnavailable, "host credential API is unavailable")
	}

	// Serialize list/check/save with reconfiguration and other form submissions.
	// Never reserve an in-memory credential before the host confirms persistence.
	m.lifeMu.Lock()
	defer m.lifeMu.Unlock()
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()
	digest := sha256.Sum256([]byte(input.APIKey + "\x00" + input.BaseURL))
	id := "qwen-key-" + hex.EncodeToString(digest[:])
	duplicate, err := m.credentialExists(ctx, cfg, input.APIKey, input.BaseURL)
	if err != nil {
		return failure(http.StatusBadGateway, "cannot inspect existing credentials")
	}
	if duplicate {
		return failure(http.StatusConflict, "credential already exists")
	}
	label := strings.TrimSpace(input.Name)
	if label == "" {
		// No alias: present the key masked as first4...last4 so operators can tell
		// credentials apart without the secret being readable.
		label = maskAPIKey(input.APIKey)
	}
	label = redactSecrets(label, cfg, input.APIKey)
	record, err := json.Marshal(credentialRecord{Type: ProviderID, ID: id, Label: label, APIKey: input.APIKey, BaseURL: input.BaseURL})
	if err != nil {
		return failure(http.StatusInternalServerError, "cannot encode credential")
	}
	if err := m.bridge.AuthSave(ctx, pluginapi.HostAuthSaveRequest{Name: id + ".json", JSON: record}); err != nil {
		// Host diagnostics may contain submitted data. Do not relay or log them.
		return failure(http.StatusBadGateway, "cannot save credential")
	}
	return quotaJSON(http.StatusOK, struct {
		OK    bool   `json:"ok"`
		ID    string `json:"id"`
		Label string `json:"label"`
	}{OK: true, ID: id, Label: label})
}

func (m *Manager) credentialExists(ctx context.Context, cfg config.Config, key, baseURL string) (bool, error) {
	entries, err := m.bridge.AuthList(ctx)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Provider != ProviderID && entry.Type != ProviderID {
			continue
		}
		// The list intentionally excludes keys. Read only host-reported plugin auth
		// files, as materializeAuthRecords already does for startup disk entries.
		if !filepath.IsAbs(entry.Path) {
			return false, fmt.Errorf("credential path unavailable")
		}
		raw, err := os.ReadFile(entry.Path)
		if err != nil {
			return false, fmt.Errorf("credential read failed")
		}
		var record credentialRecord
		if json.Unmarshal(raw, &record) != nil {
			return false, fmt.Errorf("invalid credential storage")
		}
		effectiveURL := record.BaseURL
		if effectiveURL == "" {
			effectiveURL = cfg.BaseURL
		}
		if strings.TrimSpace(record.APIKey) == key && strings.TrimRight(effectiveURL, "/") == baseURL {
			return true, nil
		}
	}
	return false, nil
}
