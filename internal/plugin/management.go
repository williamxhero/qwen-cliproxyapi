package plugin

// Management registration follows the reference's native RPC shape. Quota is
// exposed through SDK v8's generic quota UI, not a second browser/credential API.
import (
	"encoding/json"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func (m *Manager) registerManagement(request []byte) ([]byte, error) {
	var req map[string]json.RawMessage
	if json.Unmarshal(request, &req) != nil {
		return ErrEnvelope("invalid_request", "malformed management registration request body"), nil
	}
	return okEnvelope(struct {
		Routes []struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		} `json:"routes"`
	}{Routes: []struct {
		Method string `json:"method"`
		Path   string `json:"path"`
	}{{Method: http.MethodGet, Path: "/plugins/" + pluginName + "/quota-info"}}}), nil
}

func (m *Manager) handleManagement(request []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if json.Unmarshal(request, &req) != nil {
		return ErrEnvelope("invalid_request", "malformed management request body"), nil
	}
	if req.Method != http.MethodGet || req.Path != "/v0/management/plugins/"+pluginName+"/quota-info" {
		return okEnvelope(pluginapi.ManagementResponse{StatusCode: http.StatusNotFound, Body: []byte(`{"error":"not found"}`)}), nil
	}
	m.mu.RLock()
	name := m.cfg.DisplayName
	m.mu.RUnlock()
	body, _ := json.Marshal(map[string]any{"provider": ProviderID, "display_name": name, "source": "command", "supports_reset": false, "fetch_endpoint": "/v0/management/quota/fetch"})
	return okEnvelope(pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": {"application/json"}}, Body: body}), nil
}
