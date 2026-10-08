package catalog

import (
	"context"
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

func TestCatalogBuiltinFallbackAndPrefix(t *testing.T) {
	c := catalogConfig(t)
	for _, prefix := range []bool{true, false} {
		c.ModelPrefix.Enabled = prefix
		m := New(c, nil)
		if len(m.Models()) != 3 {
			t.Fatal("missing built-in fallback")
		}
		for _, id := range BuiltinFallbackIDs {
			r, ok := m.Lookup(config.PublicID(c, id))
			if !ok || r.UpstreamID != id || r.Protocol != RouteChatCompletions {
				t.Fatalf("fallback lookup: %#v", r)
			}
		}
	}
}
func TestCatalogDiscoveryAuthenticatedAndChatOnly(t *testing.T) {
	c := catalogConfig(t)
	m := New(c, hostFunc(func(_ context.Context, r pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		if r.Method != "GET" || r.URL != c.BaseURL+"/models" || r.Headers.Get("Authorization") != "Bearer selected-discovery-key" {
			t.Fatalf("bad discovery request: %#v", r)
		}
		return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[{"id":"custom-model","display_name":"Custom","context_length":128000,"max_output_tokens":8192,"input_modalities":["text","image"],"thinking":{"min":1,"max":100,"zero_allowed":true,"dynamic_allowed":true,"levels":["low"]}}]}`)}, nil
	}))
	if err := m.Refresh(context.Background(), "selected-discovery-key"); err != nil {
		t.Fatal(err)
	}
	records := m.Models()
	if len(records) != 1 || records[0].PublicID != "qwen/custom-model" || records[0].Protocol != RouteChatCompletions || records[0].ContextLimit != 128000 || len(records[0].InputModes) != 2 || !records[0].Thinking.ZeroAllowed {
		t.Fatalf("bad catalog: %#v", records)
	}
	if _, ok := m.Lookup("qwen/qwen3.8-max"); ok {
		t.Fatal("fallback merged into successful catalog")
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
	if err := m.Refresh(context.Background(), "dummy"); err == nil || len(m.Models()) != 3 {
		t.Fatal("nil client should retain initial fallback")
	}
}
