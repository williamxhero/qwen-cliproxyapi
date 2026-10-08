package config

import (
	"strings"
	"testing"
	"time"
)

const validYAML = "api-keys:\n  - value: dummy-plan-key\ncommand: quota-stub\n"

func TestConfigDefaultsAndEnvironmentExpansion(t *testing.T) {
	t.Setenv("QWEN_TEST_KEY", "dummy-expanded-key")
	c, err := Load([]byte("api-keys:\n  - value: '${QWEN_TEST_KEY}'\n    name: ' Personal '\ncommand: quota-stub\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKeys[0].Value != "dummy-expanded-key" || c.APIKeys[0].Name != "Personal" || c.BaseURL != DefaultBaseURL || c.CatalogURL != DefaultBaseURL+"/models" {
		t.Fatalf("unexpected config: %#v", c)
	}
	if !c.ModelPrefix.Enabled || c.ModelPrefix.Value != "qwen" || c.Catalog.RefreshInterval != 30*time.Minute || !c.Catalog.StaleWhileUnavailable || !c.Protocols.ChatCompletions || !c.Protocols.Messages || c.CommandTimeout != 90*time.Second || c.DisplayName != DefaultDisplayName || strings.Join(c.CommandArgs, ",") != "--json" {
		t.Fatalf("wrong defaults: %#v", c)
	}
}

func TestConfigExplicitValuesTakePrecedence(t *testing.T) {
	c, err := Load([]byte(validYAML + `base-url: http://localhost:8399/compatible-mode/v1/
allow-http: true
model-prefix: {enabled: false, value: custom}
catalog: {refresh-interval: 1m, stale-while-unavailable: false}
protocols: {chat-completions: false, messages: true}
request-timeout: 2s
command-timeout: 3s
command-args: []
max-response-bytes: 1024
display-name: Custom
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "http://localhost:8399/compatible-mode/v1" || c.ModelPrefix.Enabled || c.Protocols.ChatCompletions || !c.Protocols.Messages || c.Catalog.StaleWhileUnavailable || len(c.CommandArgs) != 0 || c.CommandTimeout != 3*time.Second || c.RequestTimeout != 2*time.Second || c.DisplayName != "Custom" || PublicID(c, "qwen3.8-max") != "qwen3.8-max" {
		t.Fatalf("wrong explicit config: %#v", c)
	}
	c.ModelPrefix.Enabled = true
	if PublicID(c, "qwen3.8-max") != "custom/qwen3.8-max" {
		t.Fatal("prefix override ignored")
	}
}

func TestConfigRejectsInvalidAndRedactsErrors(t *testing.T) {
	tests := map[string]string{
		"empty": "api-keys: []\ncommand: stub", "blank": "api-keys: [{value: '   '}]\ncommand: stub", "duplicates": "api-keys: [{value: dummy1}, {value: dummy1}]\ncommand: stub", "unset_env": "api-keys: [{value: '${QWEN_UNSET_TEST_KEY}'}]\ncommand: stub", "missing_command": "api-keys: [{value: dummy1}]",
		"malformed_key": "api-keys: [sk-sensitive-test123]\ncommand: stub", "malformed_yaml": "api-keys: [\ncommand: stub",
		"http": validYAML + "base-url: http://localhost/v1", "userinfo": validYAML + "base-url: https://user:sk-sensitive-test123@example.com/v1", "query": validYAML + "base-url: https://example.com/v1?key=sk-sensitive-test123", "fragment": validYAML + "base-url: https://example.com/v1#x", "ftp": validYAML + "base-url: ftp://example.com/v1", "invalid_url": validYAML + "base-url: 'https://%'",
		"refresh": validYAML + "catalog: {refresh-interval: 1s}", "bad_duration": validYAML + "request-timeout: secret", "timeout": validYAML + "request-timeout: 0s", "command_timeout": validYAML + "command-timeout: -1s", "response_limit": validYAML + "max-response-bytes: 0", "prefix": validYAML + "model-prefix: {value: '../bad'}", "quota_source": validYAML + "quota-source: api", "display": validYAML + "display-name: ''",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load([]byte(body))
			if err == nil {
				t.Fatal("accepted invalid config")
			}
			if strings.Contains(err.Error(), "sk-sensitive-test123") {
				t.Fatalf("leaked secret: %v", err)
			}
		})
	}
}
