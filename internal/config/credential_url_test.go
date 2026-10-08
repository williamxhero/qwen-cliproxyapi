package config

import "testing"

func TestCredentialBaseURLValidationSharesConfigRules(t *testing.T) {
	for _, raw := range []string{"https://example.test/v1", "http://localhost:8399/v1"} {
		if err := ValidateCredentialBaseURL(raw); err != nil {
			t.Fatalf("valid credential URL rejected: %v", err)
		}
	}
	for _, raw := range []string{"", "/v1", "https:///v1", "https://:443/v1", "ftp://example.test", "https://user@example.test", "https://example.test/?", "https://example.test/?x=y", "https://example.test/#", "https://example.test/#fragment"} {
		if err := ValidateCredentialBaseURL(raw); err == nil {
			t.Fatalf("invalid credential URL accepted: %s", raw)
		}
	}
	if err := validateURL("base-url", "http://localhost/v1", false); err == nil {
		t.Fatal("config HTTP opt-in changed")
	}
}
