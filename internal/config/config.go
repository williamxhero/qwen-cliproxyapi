// Package config loads Qwen configuration. Parsing and validation idioms are
// adapted from the MIT opencode-go-cliproxyapi project (see NOTICE.md).
package config

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultBaseURL          = "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"
	DefaultModelPrefix      = "qwen"
	DefaultRefreshInterval  = 30 * time.Minute
	DefaultRequestTimeout   = 5 * time.Minute
	DefaultCommandTimeout   = 90 * time.Second
	DefaultMaxResponseBytes = int64(67108864)
	DefaultDisplayName      = "阿里云百炼额度（CLI）"
)

type ModelPrefix struct {
	Enabled bool
	Value   string
}
type APIKey struct {
	Name  string
	Value string
}
type Catalog struct {
	RefreshInterval       time.Duration
	StaleWhileUnavailable bool
}

// Protocol switches govern accepted client formats, not upstream routing.
type Protocols struct {
	ChatCompletions bool
	Messages        bool
}
type Config struct {
	BaseURL          string
	CatalogURL       string
	ModelPrefix      ModelPrefix
	APIKeys          []APIKey
	Catalog          Catalog
	Protocols        Protocols
	AllowHTTP        bool
	RequestTimeout   time.Duration
	MaxResponseBytes int64
	QuotaSource      string
	Command          string
	CommandArgs      []string
	CommandTimeout   time.Duration
	DisplayName      string
}

type rawConfig struct {
	BaseURL     *string `yaml:"base-url"`
	ModelPrefix struct {
		Enabled *bool   `yaml:"enabled"`
		Value   *string `yaml:"value"`
	} `yaml:"model-prefix"`
	APIKeys []struct {
		Name  string `yaml:"name"`
		Value string `yaml:"value"`
	} `yaml:"api-keys"`
	Catalog struct {
		RefreshInterval       *string `yaml:"refresh-interval"`
		StaleWhileUnavailable *bool   `yaml:"stale-while-unavailable"`
	} `yaml:"catalog"`
	Protocols struct {
		ChatCompletions *bool `yaml:"chat-completions"`
		Messages        *bool `yaml:"messages"`
	} `yaml:"protocols"`
	AllowHTTP        bool      `yaml:"allow-http"`
	RequestTimeout   *string   `yaml:"request-timeout"`
	MaxResponseBytes *int64    `yaml:"max-response-bytes"`
	QuotaSource      *string   `yaml:"quota-source"`
	Command          string    `yaml:"command"`
	CommandArgs      *[]string `yaml:"command-args"`
	CommandTimeout   *string   `yaml:"command-timeout"`
	DisplayName      *string   `yaml:"display-name"`
}

func Load(body []byte) (Config, error) {
	var raw rawConfig
	if err := yaml.Unmarshal(body, &raw); err != nil {
		if n := regexp.MustCompile(`line (\d+)`).FindStringSubmatch(err.Error()); n != nil {
			return Config{}, fmt.Errorf("decode config: invalid YAML structure near line %s", n[1])
		}
		return Config{}, fmt.Errorf("decode config: invalid YAML structure")
	}
	refresh, err := parseDuration("catalog.refresh-interval", raw.Catalog.RefreshInterval, DefaultRefreshInterval)
	if err != nil {
		return Config{}, err
	}
	timeout, err := parseDuration("request-timeout", raw.RequestTimeout, DefaultRequestTimeout)
	if err != nil {
		return Config{}, err
	}
	commandTimeout, err := parseDuration("command-timeout", raw.CommandTimeout, DefaultCommandTimeout)
	if err != nil {
		return Config{}, err
	}
	c := Config{
		BaseURL:     strings.TrimRight(orDefault(raw.BaseURL, DefaultBaseURL), "/"),
		ModelPrefix: ModelPrefix{Enabled: orDefault(raw.ModelPrefix.Enabled, true), Value: orDefault(raw.ModelPrefix.Value, DefaultModelPrefix)},
		Catalog:     Catalog{RefreshInterval: refresh, StaleWhileUnavailable: orDefault(raw.Catalog.StaleWhileUnavailable, true)},
		Protocols:   Protocols{ChatCompletions: orDefault(raw.Protocols.ChatCompletions, true), Messages: orDefault(raw.Protocols.Messages, true)},
		AllowHTTP:   raw.AllowHTTP, RequestTimeout: timeout, MaxResponseBytes: orDefault(raw.MaxResponseBytes, DefaultMaxResponseBytes),
		QuotaSource: orDefault(raw.QuotaSource, "command"), Command: strings.TrimSpace(raw.Command), CommandTimeout: commandTimeout, DisplayName: orDefault(raw.DisplayName, DefaultDisplayName),
		CommandArgs: append([]string(nil), orDefault(raw.CommandArgs, []string{"--json"})...),
	}
	c.CatalogURL = c.BaseURL + "/models"
	for _, k := range raw.APIKeys {
		c.APIKeys = append(c.APIKeys, APIKey{Name: strings.TrimSpace(k.Name), Value: strings.TrimSpace(os.ExpandEnv(k.Value))})
	}
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func PublicID(c Config, id string) string {
	if c.ModelPrefix.Enabled {
		return c.ModelPrefix.Value + "/" + id
	}
	return id
}

func (c Config) validate() error {
	if err := validateURL("base-url", c.BaseURL, c.AllowHTTP); err != nil {
		return err
	}
	if len(c.APIKeys) == 0 {
		return fmt.Errorf("api-keys: at least one key is required")
	}
	seen := map[string]bool{}
	for i, k := range c.APIKeys {
		if k.Value == "" {
			return fmt.Errorf("api-keys[%d].value: expanded to empty", i)
		}
		if strings.ContainsAny(k.Value, "\r\n\x00") {
			return fmt.Errorf("api-keys[%d].value: invalid control character", i)
		}
		if seen[k.Value] {
			return fmt.Errorf("api-keys: duplicate key values are not allowed")
		}
		seen[k.Value] = true
	}
	if c.ModelPrefix.Enabled && !validPrefix(c.ModelPrefix.Value) {
		return fmt.Errorf("model-prefix.value: invalid provider-ID characters")
	}
	if c.Catalog.RefreshInterval < time.Minute {
		return fmt.Errorf("catalog.refresh-interval: must be at least 1m")
	}
	if c.RequestTimeout <= 0 {
		return fmt.Errorf("request-timeout: must be positive")
	}
	if c.MaxResponseBytes <= 0 {
		return fmt.Errorf("max-response-bytes: must be positive")
	}
	if c.QuotaSource != "command" {
		return fmt.Errorf("quota-source: only command is supported")
	}
	if c.Command == "" {
		return fmt.Errorf("command: executable path is required")
	}
	if strings.ContainsRune(c.Command, '\x00') {
		return fmt.Errorf("command: invalid executable path")
	}
	for _, arg := range c.CommandArgs {
		if strings.ContainsRune(arg, '\x00') {
			return fmt.Errorf("command-args: invalid argument")
		}
	}
	if c.CommandTimeout <= 0 {
		return fmt.Errorf("command-timeout: must be positive")
	}
	if strings.TrimSpace(c.DisplayName) == "" {
		return fmt.Errorf("display-name: must not be empty")
	}
	return nil
}

func validateURL(name, raw string, allowHTTP bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme == "" {
		return fmt.Errorf("%s: invalid URL", name)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("%s: must not contain query, fragment, or userinfo", name)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowHTTP {
			return fmt.Errorf("%s: http scheme requires allow-http", name)
		}
	default:
		return fmt.Errorf("%s: unsupported scheme; use https", name)
	}
	return nil
}
func validPrefix(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if i == 0 && !alnum || !alnum && r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}
func parseDuration(name string, p *string, def time.Duration) (time.Duration, error) {
	if p == nil {
		return def, nil
	}
	d, err := time.ParseDuration(*p)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration", name)
	}
	return d, nil
}
func orDefault[T any](p *T, def T) T {
	if p != nil {
		return *p
	}
	return def
}
