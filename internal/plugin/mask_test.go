package plugin

import (
	"testing"

	"qwen-cliproxyapi/internal/config"
)

// The panel shows a credential's label; without an alias that label is the key
// masked as first4...last4, so operators can still tell two credentials apart.
func TestMaskAPIKeyFormat(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"a":                   "...",
		"ab":                  "a...b",
		"dummy-a":             "d...a",
		"dummy-key":           "du...ey",
		"sk-sp-abc":           "sk...bc",
		"dummy-panel-key":     "dumm...-key",
		"sk-sp-0123456789abc": "sk-s...9abc",
	}
	for in, want := range cases {
		if got := maskAPIKey(in); got != want {
			t.Fatalf("maskAPIKey(%q) = %q, want %q", in, got, want)
		}
	}
	// A mask must never be long enough to be a usable secret.
	long := "sk-sp-0123456789abcdefghij"
	if masked := maskAPIKey(long); len(masked) > 11 {
		t.Fatalf("mask too long: %q", masked)
	}
}

// An alias always wins; only a credential without one falls back to the mask.
func TestAccountLabelAliasBeatsMask(t *testing.T) {
	cfg := providerConfig(t)
	cfg.APIKeys = []config.APIKey{{Value: "dummy-config-key", Name: "Work"}}
	if got := accountLabel(cfg, "dummy-config-key", "", 0); got != "Work" {
		t.Fatalf("alias should win: %q", got)
	}
	cfg.APIKeys = []config.APIKey{{Value: "dummy-config-key"}}
	if got, want := accountLabel(cfg, "dummy-config-key", "", 0), maskAPIKey("dummy-config-key"); got != want {
		t.Fatalf("missing alias should fall back to mask: %q want %q", got, want)
	}
	// A label we generated ourselves before this rule existed is not an alias.
	if got, want := accountLabel(cfg, "dummy-config-key", "Qwen 3", 2), maskAPIKey("dummy-config-key"); got != want {
		t.Fatalf("stale default label should be replaced: %q want %q", got, want)
	}
	// A user-chosen label survives re-materialisation.
	if got := accountLabel(cfg, "dummy-config-key", "我的订阅", 0); got != "我的订阅" {
		t.Fatalf("user label should survive: %q", got)
	}
}
