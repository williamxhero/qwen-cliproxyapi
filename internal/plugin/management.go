package plugin

// Resource registration follows the MIT reference's native RPC envelope. The
// embedded page adapts the same CLI reading used by the generic quota provider.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"qwen-cliproxyapi/internal/config"
	"qwen-cliproxyapi/resources"
)

func (m *Manager) registerManagement(request []byte) ([]byte, error) {
	var req struct {
		Plugin           pluginapi.Metadata `json:"Plugin"`
		BasePath         string             `json:"BasePath"`
		ResourceBasePath string             `json:"ResourceBasePath"`
	}
	if json.Unmarshal(request, &req) != nil {
		return ErrEnvelope("invalid_request", "malformed management registration request body"), nil
	}
	return okEnvelope(struct {
		Routes []struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		} `json:"routes"`
		Resources []struct {
			Path        string `json:"path"`
			Menu        string `json:"menu"`
			Description string `json:"description"`
		} `json:"resources"`
	}{
		Routes: []struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		}{
			{Method: http.MethodGet, Path: "/plugins/" + pluginName + "/quota-info"},
			{Method: http.MethodPost, Path: "/plugins/" + pluginName + "/quota-usage"},
		},
		Resources: []struct {
			Path        string `json:"path"`
			Menu        string `json:"menu"`
			Description string `json:"description"`
		}{{Path: "/quota", Menu: "Qwen 额度", Description: "查看千问套餐、额度窗口与重置时间。"}},
	}), nil
}

func (m *Manager) handleManagement(request []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if json.Unmarshal(request, &req) != nil {
		return ErrEnvelope("invalid_request", "malformed management request body"), nil
	}
	resp, err := m.HandleManagement(context.Background(), req)
	if err != nil {
		return ErrEnvelope("management_failure", err.Error()), nil
	}
	return okEnvelope(resp), nil
}

func (m *Manager) HandleManagement(ctx context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	if req.Method == http.MethodGet && req.Path == "/v0/resource/plugins/"+pluginName+"/quota" {
		return pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": {"text/html; charset=utf-8"}},
			Body:       []byte(resources.QuotaPage),
		}, nil
	}
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()
	// Preserve the original metadata endpoint for existing consumers.
	if req.Method == http.MethodGet && req.Path == "/v0/management/plugins/"+pluginName+"/quota-info" {
		return quotaJSON(http.StatusOK, map[string]any{"provider": ProviderID, "display_name": cfg.DisplayName, "source": "command", "supports_reset": false, "fetch_endpoint": "/v0/management/quota/fetch"})
	}
	if req.Method != http.MethodPost || req.Path != "/v0/management/plugins/"+pluginName+"/quota-usage" {
		return quotaJSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	if len(req.Body) > 0 && json.Unmarshal(req.Body, &body) != nil {
		return quotaJSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}
	cards, err := m.quotaCredentials(ctx, cfg)
	if err != nil {
		return quotaJSON(http.StatusBadGateway, map[string]string{"error": boundedDiagnostic([]byte(redactSecrets(err.Error(), cfg, "")))})
	}
	if body.AuthIndex != "" {
		selected := make([]quotaCard, 0, 1)
		for _, card := range cards {
			if card.AuthIndex == body.AuthIndex {
				selected = append(selected, card)
				break
			}
		}
		if len(selected) == 0 {
			return quotaJSON(http.StatusNotFound, map[string]string{"error": "unknown auth_index"})
		}
		cards = selected
	}
	if len(cards) > 0 {
		// Console quota is account-scoped. One invocation gives every configured
		// credential the same account reading; API keys never enter CLI argv.
		raw, refreshErr := fetchCommandQuota(ctx, cfg, "")
		now := time.Now()
		for i := range cards {
			if refreshErr != nil {
				message := refreshErr.Error()
				cards[i].Error = &message
				continue
			}
			populateQuotaCard(&cards[i], raw, cfg, now)
		}
	}
	return quotaJSON(http.StatusOK, struct {
		Cards []quotaCard `json:"cards"`
	}{Cards: cards})
}

type quotaWindow struct {
	Window           string  `json:"window"`
	UsedPercent      float64 `json:"usedPercent"`
	RemainingPercent float64 `json:"remainingPercent"`
	ResetTime        string  `json:"resetTime,omitempty"`
	ResetsInDays     *int    `json:"resetsInDays,omitempty"`
}

type quotaCard struct {
	AuthIndex  string                  `json:"auth_index"`
	Label      string                  `json:"label"`
	Plan       string                  `json:"plan,omitempty"`
	PlanStatus string                  `json:"planStatus,omitempty"`
	PlanStart  string                  `json:"planStart,omitempty"`
	PlanEnd    string                  `json:"planEnd,omitempty"`
	DaysLeft   *int                    `json:"daysLeft,omitempty"`
	ObservedAt string                  `json:"observedAt,omitempty"`
	Windows    []quotaWindow           `json:"windows"`
	Metrics    []pluginapi.QuotaMetric `json:"metrics"`
	Error      *string                 `json:"error"`
}

func (m *Manager) quotaCredentials(ctx context.Context, cfg config.Config) ([]quotaCard, error) {
	cards := make([]quotaCard, 0, len(cfg.APIKeys))
	if len(cfg.APIKeys) == 0 {
		return cards, nil
	}
	if m.bridge == nil {
		return nil, fmt.Errorf("host credential list is unavailable")
	}
	entries, err := m.bridge.AuthList(ctx)
	if err != nil {
		return nil, err
	}
	for i, key := range cfg.APIKeys {
		digest := sha256.Sum256([]byte(key.Value))
		id := "qwen-key-" + hex.EncodeToString(digest[:])
		found := false
		for _, entry := range entries {
			if entry.Provider != ProviderID && entry.Type != ProviderID {
				continue
			}
			if entry.ID != id && entry.Name != id+".json" {
				continue
			}
			if strings.TrimSpace(entry.AuthIndex) == "" {
				return nil, fmt.Errorf("host credential index is unavailable")
			}
			cards = append(cards, quotaCard{
				AuthIndex: entry.AuthIndex,
				Label:     redactSecrets(accountLabel(cfg, key.Value, entry.Label, i), cfg, ""),
				Windows:   []quotaWindow{}, Metrics: []pluginapi.QuotaMetric{},
			})
			found = true
			break
		}
		if !found {
			return nil, fmt.Errorf("configured Qwen credential is not available in the host")
		}
	}
	return cards, nil
}

func populateQuotaCard(card *quotaCard, raw commandQuota, cfg config.Config, now time.Time) {
	safe := func(s string) string { return redactSecrets(s, cfg, "") }
	card.Plan, card.PlanStatus, card.ObservedAt = safe(raw.Plan), safe(raw.PlanStatus), safe(raw.ObservedAt)
	if _, err := time.Parse(time.RFC3339Nano, raw.PlanStart); err == nil {
		card.PlanStart = raw.PlanStart
	}
	if days := daysUntil(raw.PlanEnd, now); days != nil {
		card.PlanEnd, card.DaysLeft = raw.PlanEnd, days
	}
	for _, window := range raw.Windows {
		used := min(100.0, max(0.0, *window.UsedPercent))
		card.Windows = append(card.Windows, quotaWindow{
			Window: safe(window.Window), UsedPercent: used, RemainingPercent: 100 - used,
			ResetTime: window.ResetTime, ResetsInDays: daysUntil(window.ResetTime, now),
		})
	}
	for _, metric := range raw.Metrics {
		card.Metrics = append(card.Metrics, pluginapi.QuotaMetric{
			Key: safe(metric.Key), Label: safe(metric.Label), Value: *metric.Value,
			Unit: safe(metric.Unit), Currency: safe(metric.Currency), Format: metric.Format,
		})
	}
}

func daysUntil(timestamp string, now time.Time) *int {
	end, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return nil
	}
	// Whole seconds avoid time.Duration saturation for valid distant dates;
	// the remainder includes nanoseconds so exact day boundaries stay exact.
	seconds := end.Unix() - now.Unix()
	days := seconds / 86400
	if seconds%86400*int64(time.Second)+int64(end.Nanosecond()-now.Nanosecond()) > 0 {
		days++
	}
	value := int(days)
	return &value
}

func quotaJSON(status int, value any) (pluginapi.ManagementResponse, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return pluginapi.ManagementResponse{}, err
	}
	return pluginapi.ManagementResponse{StatusCode: status, Headers: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
}
