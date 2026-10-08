// Package bailianquota reads authenticated Bailian console quota without a plan API key.
package bailianquota

import (
	"fmt"
	"time"
)

const (
	DefaultBSK       = "C:/Users/will/.local/bin/bsk"
	ConsoleURL       = "https://bailian.console.aliyun.com/cn-beijing/?tab=model#/subscription/token-plan/personal"
	RPCURL           = "https://bailian-cs.console.aliyun.com/data/api.json"
	consoleOrigin    = "https://bailian.console.aliyun.com"
	maxResponseBytes = 4 << 20
)

// Result is the frozen stdout contract shared with the provider plugin.
// Empty readings are arrays, not null; only --check may succeed without a reading.
type Result struct {
	Source     string   `json:"source"`
	Plan       string   `json:"plan"`
	PlanStatus string   `json:"planStatus"`
	PlanStart  string   `json:"planStart,omitempty"`
	PlanEnd    string   `json:"planEnd,omitempty"`
	DaysLeft   *int     `json:"daysLeft,omitempty"`
	ObservedAt string   `json:"observedAt"`
	Windows    []Window `json:"windows"`
	Metrics    []Metric `json:"metrics"`
	Notes      []string `json:"notes"`
}

type Window struct {
	Window      string  `json:"window"`
	UsedPercent float64 `json:"usedPercent"`
	ResetTime   string  `json:"resetTime"`
}

type Metric struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Value    float64 `json:"value"`
	Unit     string  `json:"unit"`
	Currency string  `json:"currency,omitempty"`
	Format   string  `json:"format"`
}

// Failure messages are intentionally controlled: console/browser diagnostics can
// contain credentials and must never be relayed to stdout or stderr.
type Failure struct {
	Code    string `json:"error"`
	Message string `json:"message"`
}

func (e *Failure) Error() string         { return fmt.Sprintf("%s: %s", e.Code, e.Message) }
func failure(code, message string) error { return &Failure{Code: code, Message: message} }

var chinaTime = time.FixedZone("UTC+08:00", 8*60*60)

func emptyResult(source string, observed time.Time) Result {
	return Result{Source: source, ObservedAt: observed.In(chinaTime).Format(time.RFC3339), Windows: []Window{}, Metrics: []Metric{}, Notes: []string{}}
}
