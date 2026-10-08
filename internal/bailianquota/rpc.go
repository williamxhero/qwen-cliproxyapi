package bailianquota

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Call struct {
	Name string
	API  string
	Data map[string]any
}

func Calls() []Call {
	return []Call{
		{"usage", "zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/usage", map[string]any{}},
		{"subscription", "zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/subscription", map[string]any{"queryInstanceInfoRequest": map[string]any{"commodityCode": "sfm_tokenplansolo_public_cn"}}},
		{"addon", "zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/addon/summary", map[string]any{"commodityCode": "sfm_tokenplansoloaddon_public_cn"}},
		{"coding", "zeldaEasy.broadscope-bailian.codingPlan.queryCodingPlanInstanceInfoV2", map[string]any{"queryCodingPlanInstanceInfoRequest": map[string]any{"commodityCode": "sfm_codingplan_public_cn"}}},
	}
}

// NewRPCRequest is shared by browser and cookie transports. Only the URL's api
// parameter is dash encoded; the Api inside params retains its original name.
func NewRPCRequest(ctx context.Context, call Call) (*http.Request, error) {
	if ctx == nil || call.API == "" {
		return nil, failure("invalid_request", "cannot assemble console RPC")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, failure("invalid_request", "cannot generate console trace ID")
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	trace := fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
	data := make(map[string]any, len(call.Data)+1)
	for k, v := range call.Data {
		data[k] = v
	}
	data["cornerstoneParam"] = map[string]string{
		"feTraceId": trace,
		"feURL":     "https://bailian.console.aliyun.com/cn-beijing/subscription/token-plan/personal",
		"protocol":  "V2", "console": "ONE_CONSOLE", "productCode": "p_efm",
	}
	params, err := json.Marshal(map[string]any{"Api": call.API, "V": "1.0", "Data": data})
	if err != nil {
		return nil, failure("invalid_request", "cannot encode console RPC")
	}
	query := url.Values{"action": {"BroadScopeAspnGateway"}, "product": {"sfm_bailian"}, "api": {strings.NewReplacer(".", "-", "/", "-").Replace(call.API)}, "_v": {""}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, RPCURL+"?"+query.Encode(), strings.NewReader(url.Values{"params": {string(params)}}.Encode()))
	if err != nil {
		return nil, failure("invalid_request", "cannot assemble console RPC")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req, nil
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Client struct {
	Run           CommandRunner
	HTTP          HTTPDoer
	Now           func() time.Time
	cleanupBudget time.Duration
}

func NewClient() *Client {
	return &Client{Run: runBSK, HTTP: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Now: time.Now}
}

func (c *Client) FetchCookie(ctx context.Context, cookie string, check bool) (Result, error) {
	if strings.TrimSpace(cookie) == "" || strings.ContainsAny(cookie, "\r\n") {
		return Result{}, failure("invalid_cookie", "provide a non-empty cookie header without line breaks")
	}
	responses := make(map[string]json.RawMessage)
	calls := Calls()
	if check {
		calls = calls[:1]
	}
	for _, call := range calls {
		req, err := NewRPCRequest(ctx, call)
		if err != nil {
			return Result{}, err
		}
		req.Header.Set("Cookie", cookie)
		req.Header.Set("Origin", consoleOrigin)
		req.Header.Set("Referer", ConsoleURL)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if call.Name == "usage" {
				if ctx.Err() != nil {
					return Result{}, failure("timeout", "console request was canceled or exceeded --timeout")
				}
				return Result{}, failure("network_error", "cannot reach the Bailian console RPC")
			}
			if ctx.Err() != nil {
				break
			} // Optional enrichment cannot invalidate primary usage.
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || (resp.StatusCode >= 300 && resp.StatusCode < 400) {
			if call.Name == "usage" {
				return Result{}, failure("not_logged_in", "Bailian console login is missing or expired")
			}
			continue
		}
		if readErr != nil || len(body) > maxResponseBytes || resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if call.Name == "usage" {
				return Result{}, failure("http_error", fmt.Sprintf("console usage RPC returned HTTP %d or an unreadable response", resp.StatusCode))
			}
			continue
		}
		responses[call.Name] = body
		if call.Name == "usage" {
			if _, err := ParseResponses("cookie", responses, c.Now(), check); err != nil {
				return Result{}, err
			}
		}
	}
	return ParseResponses("cookie", responses, c.Now(), check)
}
