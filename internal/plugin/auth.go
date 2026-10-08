package plugin

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"qwen-cliproxyapi/internal/config"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type authProvider struct{ cfg config.Config }

var _ pluginapi.AuthProvider = authProvider{}

func (authProvider) Identifier() string { return ProviderID }

func (p authProvider) ParseAuth(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	debugTrace("auth parse request provider=%s file=%s raw_bytes=%d", req.Provider, req.FileName, len(req.RawJSON))
	var raw struct {
		Type     string `json:"type"`
		Provider string `json:"provider"`
		ID       string `json:"id"`
		Label    string `json:"label"`
		APIKey   string `json:"api_key"`
		BaseURL  string `json:"base_url"`
	}
	if err := json.Unmarshal(req.RawJSON, &raw); err != nil {
		if req.Provider == ProviderID {
			return pluginapi.AuthParseResponse{}, fmt.Errorf("qwen auth record has invalid JSON")
		}
		return pluginapi.AuthParseResponse{}, nil
	}
	if (req.Provider != "" && req.Provider != ProviderID) || (raw.Type != ProviderID && raw.Provider != ProviderID) {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	if strings.TrimSpace(raw.APIKey) == "" {
		return pluginapi.AuthParseResponse{}, fmt.Errorf("qwen auth record has no api key")
	}
	if raw.ID == "" {
		raw.ID = req.FileName
	}
	debugTrace("auth parse handled provider=%s file=%s id=%s api_key_present=%t api_key_length=%d", req.Provider, req.FileName, raw.ID, strings.TrimSpace(raw.APIKey) != "", len(raw.APIKey))
	attributes := map[string]string{"api_key": raw.APIKey}
	if raw.BaseURL != "" {
		if err := config.ValidateCredentialBaseURL(raw.BaseURL); err != nil {
			return pluginapi.AuthParseResponse{}, fmt.Errorf("qwen auth record has invalid base_url")
		}
		attributes["base_url"] = raw.BaseURL
		// host.auth.save initially registers the filename as its runtime ID.
		// Match it on watcher parsing to update, not duplicate, panel records.
		// Legacy configured credentials retain their existing bare-hash IDs.
		if req.FileName != "" {
			raw.ID = req.FileName
		}
	}
	label := accountLabel(p.cfg, raw.APIKey, raw.Label, 0)
	if raw.BaseURL != "" && strings.TrimSpace(raw.Label) != "" {
		label = raw.Label
	}
	return pluginapi.AuthParseResponse{Handled: true, Auth: pluginapi.AuthData{
		Provider: ProviderID, ID: raw.ID, FileName: req.FileName, Label: label, StorageJSON: req.RawJSON,
		Attributes: attributes,
	}}, nil
}

func (authProvider) StartLogin(context.Context, pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	// The host requires a state even for a manual form; this is not OAuth.
	return pluginapi.AuthLoginStartResponse{Provider: ProviderID, State: rand.Text(), ExpiresAt: time.Now().Add(10 * time.Minute), Metadata: map[string]any{
		"auth_kind":    "manual_api_key",
		"submit_path":  "/v0/management/plugins/" + pluginName + "/credentials",
		"submit_label": "添加凭证",
		"fields": []map[string]any{
			{"name": "base_url", "label": "Base URL", "placeholder": config.DefaultBaseURL, "required": true},
			{"name": "api_key", "label": "API Key", "type": "password", "required": true},
			{"name": "name", "label": "凭证名称", "required": false},
		},
	}}, nil
}

func (authProvider) PollLogin(context.Context, pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	return pluginapi.AuthLoginPollResponse{}, fmt.Errorf("qwen login is unsupported; configure a manual api key")
}

func (authProvider) RefreshAuth(_ context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	debugTrace("auth refresh request provider=%s id=%s storage_json_bytes=%d attr_names=%v metadata_names=%v", req.AuthProvider, req.AuthID, len(req.StorageJSON), mapKeys(req.Attributes), mapKeys(req.Metadata))
	if req.AuthProvider != ProviderID || strings.TrimSpace(req.Attributes["api_key"]) == "" {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("qwen auth refresh requires a selected API key credential")
	}
	return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{Provider: req.AuthProvider, ID: req.AuthID, StorageJSON: req.StorageJSON, Metadata: req.Metadata, Attributes: req.Attributes}}, nil
}

func mapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

// Labels are presentation only. Stable auth IDs and stored host metadata are preserved.
func accountLabel(cfg config.Config, key, existing string, fallbackIndex int) string {
	index := fallbackIndex
	for i, entry := range cfg.APIKeys {
		if entry.Value == key {
			if name := strings.TrimSpace(entry.Name); name != "" {
				return name
			}
			index = i
			break
		}
	}
	label := strings.TrimSpace(existing)
	if label != "" && !strings.HasPrefix(label, "Qwen credential ") && !strings.HasPrefix(label, "qwen-key-") {
		return label
	}
	return fmt.Sprintf("Qwen %d", index+1)
}
