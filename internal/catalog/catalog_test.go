package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"qwen-cliproxyapi/internal/config"
)

type hostFunc func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)

func (f hostFunc) Do(ctx context.Context, r pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return f(ctx, r)
}
func catalogConfig(t *testing.T) config.Config {
	t.Helper()
	c, err := config.Load([]byte("api-keys: [{value: dummy-catalog-key}]\ncommand: stub\n"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCatalogStartsEmptyAndHonorsExplicitPrefix(t *testing.T) {
	c := catalogConfig(t)
	if c.ModelPrefix.Enabled {
		t.Fatal("model prefix should be disabled by default")
	}
	m := New(c, nil)
	if len(m.Models()) != 0 {
		t.Fatal("catalog must stay empty before a successful discovery")
	}
	c.ModelPrefix = config.ModelPrefix{Enabled: true, Value: "custom"}
	m = New(c, nil)
	m.swap([]rawModel{{ID: "deepseek-v4.1-flash"}})
	if got := m.Models()[0].PublicID; got != "custom/deepseek-v4.1-flash" {
		t.Fatalf("explicit prefix ignored: %s", got)
	}
}
func TestCatalogDiscoveryAuthenticatedAndChatOnly(t *testing.T) {
	c := catalogConfig(t)
	ids := []string{"qwen3.7-max", "qwen3.7-plus", "qwen3.6-flash", "glm-5.2", "deepseek-v4-pro", "wan2.7-image", "wan2.7-image-pro", "qwen-audio-3.0-tts-plus", "deepseek-v4-flash-0731", "qwen3.8-max", "qwen-audio-3.0-realtime-plus", "deepseek-v4-pro-0813", "qwen3.8-flash", "deepseek-v4.1-flash", "glm-5.3", "auto", "decision-model-preview", "wan3.0-video", "deepseek-v4.1-flash-1017"}
	models := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		models = append(models, map[string]any{"id": id})
	}
	models[0]["display_name"] = "Qwen 3.7 Max"
	models[0]["context_length"] = 128000
	models[0]["max_output_tokens"] = 8192
	models[0]["input_modalities"] = []string{"text", "image"}
	models[0]["thinking"] = map[string]any{"min": 1, "max": 100, "zero_allowed": true, "dynamic_allowed": true, "levels": []string{"low"}}
	body, err := json.Marshal(map[string]any{"data": models})
	if err != nil {
		t.Fatal(err)
	}
	m := New(c, hostFunc(func(_ context.Context, r pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		if r.Method != "GET" || r.URL != c.BaseURL+"/models" || r.Headers.Get("Authorization") != "Bearer selected-discovery-key" {
			t.Fatalf("bad discovery request: %#v", r)
		}
		return pluginapi.HTTPResponse{StatusCode: 200, Body: body}, nil
	}))
	if err := m.Refresh(context.Background(), "selected-discovery-key"); err != nil {
		t.Fatal(err)
	}
	records := m.Models()
	if len(records) != len(ids) || records[0].PublicID != "qwen3.7-max" || records[0].Protocol != RouteChatCompletions || records[0].ContextLimit != 128000 || len(records[0].InputModes) != 2 || records[0].Thinking == nil || !records[0].Thinking.ZeroAllowed {
		t.Fatalf("bad catalog: %#v", records)
	}
	if _, ok := m.Lookup("deepseek-v4.1-flash"); !ok {
		t.Fatal("non-Qwen upstream model missing")
	}
	for _, rec := range records {
		if strings.HasPrefix(rec.PublicID, "qwen/") {
			t.Fatalf("default model ID has provider prefix: %s", rec.PublicID)
		}
	}
	if got := JoinUpstreamURL(c.BaseURL, records[0].EndpointPath); got != c.BaseURL+"/chat/completions" {
		t.Fatalf("wrong upstream URL: %s", got)
	}
}
func TestCatalogStalePolicyAndHonestFailures(t *testing.T) {
	for _, stale := range []bool{true, false} {
		t.Run(map[bool]string{true: "stale", false: "fail_closed"}[stale], func(t *testing.T) {
			c := catalogConfig(t)
			c.Catalog.StaleWhileUnavailable = stale
			body := `{"data":[{"id":"discovered"}]}`
			status := 200
			network := false
			m := New(c, hostFunc(func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
				if network {
					return pluginapi.HTTPResponse{}, errors.New("sk-do-not-echo")
				}
				return pluginapi.HTTPResponse{StatusCode: status, Body: []byte(body)}, nil
			}))
			if err := m.Refresh(context.Background(), "dummy"); err != nil {
				t.Fatal(err)
			}
			network = true
			err := m.Refresh(context.Background(), "dummy")
			if err == nil || strings.Contains(err.Error(), "sk-do-not-echo") {
				t.Fatalf("failure not safe: %v", err)
			}
			_, ok := m.Lookup("discovered")
			if ok != stale {
				t.Fatal("incorrect stale policy")
			}
			network = false
			for _, invalid := range []string{`bad`, `{}`, `{"data":{}}`} {
				body = invalid
				if err := m.Refresh(context.Background(), "dummy"); err == nil {
					t.Fatal("accepted invalid response")
				}
			}
			status = 401
			if err := m.Refresh(context.Background(), "dummy"); err == nil {
				t.Fatal("accepted unauthorized catalog")
			}
			status = 200
			body = `{"data":[]}`
			if err := m.Refresh(context.Background(), "dummy"); err != nil || len(m.Models()) != 0 {
				t.Fatal("successful empty catalog not respected")
			}
		})
	}
}
func TestCatalogSeedRebuildsPrefixAndDeduplicates(t *testing.T) {
	c := catalogConfig(t)
	c.ModelPrefix.Enabled = true
	old := New(c, nil)
	old.swap([]rawModel{{ID: "a"}, {ID: "a"}, {ID: "qwen/a"}, {}})
	if len(old.Models()) != 1 || len(old.Warnings()) == 0 || len(old.Unsupported()) != 2 {
		t.Fatalf("dedup/collision diagnostics missing: %#v", old.Models())
	}
	c.ModelPrefix.Value = "custom"
	fresh := New(c, nil)
	fresh.SeedFrom(old)
	if _, ok := fresh.Lookup("custom/a"); !ok {
		t.Fatal("seed did not rebuild prefix")
	}
	if _, ok := fresh.Lookup("qwen/a"); !ok {
		t.Fatal("seed did not reconsider old collision")
	}
}
func TestCatalogNilClientHonorsStalePolicy(t *testing.T) {
	c := catalogConfig(t)
	m := New(c, nil)
	if err := m.Refresh(context.Background(), "dummy"); err == nil || len(m.Models()) != 0 {
		t.Fatal("nil client must not publish an incomplete fallback catalog")
	}
}
