package bailianquota

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }
func httpResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func testClient() *Client { c := NewClient(); c.Now = func() time.Time { return observed }; return c }

func TestRPCRequestAssembly(t *testing.T) {
	for _, call := range Calls() {
		t.Run(call.Name, func(t *testing.T) {
			req, err := NewRPCRequest(context.Background(), call)
			if err != nil {
				t.Fatal(err)
			}
			if req.Method != "POST" || req.URL.Scheme+"://"+req.URL.Host+req.URL.Path != RPCURL || req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Fatal(req)
			}
			wantQuery := url.Values{"action": {"BroadScopeAspnGateway"}, "product": {"sfm_bailian"}, "api": {strings.NewReplacer(".", "-", "/", "-").Replace(call.API)}, "_v": {""}}
			if !reflect.DeepEqual(req.URL.Query(), wantQuery) {
				t.Fatal(req.URL.Query())
			}
			raw, _ := io.ReadAll(req.Body)
			if !strings.HasPrefix(string(raw), "params=") {
				t.Fatal(string(raw))
			}
			form, err := url.ParseQuery(string(raw))
			if err != nil || len(form) != 1 {
				t.Fatal(form, err)
			}
			var params struct {
				API     string         `json:"Api"`
				Version string         `json:"V"`
				Data    map[string]any `json:"Data"`
			}
			if json.Unmarshal([]byte(form.Get("params")), &params) != nil || params.API != call.API || params.Version != "1.0" {
				t.Fatal(params)
			}
			cornerstone, ok := params.Data["cornerstoneParam"].(map[string]any)
			if !ok || len(cornerstone) != 5 || cornerstone["feURL"] != "https://bailian.console.aliyun.com/cn-beijing/subscription/token-plan/personal" || cornerstone["protocol"] != "V2" || cornerstone["console"] != "ONE_CONSOLE" || cornerstone["productCode"] != "p_efm" {
				t.Fatal(cornerstone)
			}
			if !regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`).MatchString(cornerstone["feTraceId"].(string)) {
				t.Fatal(cornerstone)
			}
			delete(params.Data, "cornerstoneParam")
			if !reflect.DeepEqual(params.Data, call.Data) {
				t.Fatal(params.Data)
			}
			if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
				t.Fatal("credentials in assembly")
			}
		})
	}
	if Calls()[1].Data["queryInstanceInfoRequest"].(map[string]any)["commodityCode"] != "sfm_tokenplansolo_public_cn" || Calls()[3].Data["queryCodingPlanInstanceInfoRequest"].(map[string]any)["commodityCode"] != "sfm_codingplan_public_cn" {
		t.Fatal("incorrect commodities")
	}
}

func TestRPCAssemblyIsIndependentAndRejectsInvalidInput(t *testing.T) {
	call := Calls()[0]
	first, _ := NewRPCRequest(context.Background(), call)
	second, _ := NewRPCRequest(context.Background(), call)
	a, _ := io.ReadAll(first.Body)
	b, _ := io.ReadAll(second.Body)
	if string(a) == string(b) || len(call.Data) != 0 {
		t.Fatal("trace repeated or call mutated")
	}
	for _, input := range []Call{{}, {API: "test", Data: map[string]any{"bad": make(chan int)}}} {
		_, err := NewRPCRequest(context.Background(), input)
		requireCode(t, err, "invalid_request")
	}
	_, err := NewRPCRequest(nil, call)
	requireCode(t, err, "invalid_request")
}

func TestCookieFetchUsesSameRequestsAndOptionalErrorsAreNonfatal(t *testing.T) {
	client := testClient()
	calls := 0
	client.HTTP = doerFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "bailian-cs.console.aliyun.com" || req.Header.Get("Cookie") != "session=synthetic" || req.Header.Get("Origin") != consoleOrigin {
			t.Fatal(req.Header, req.URL)
		}
		if strings.HasSuffix(req.URL.Query().Get("api"), "-usage") {
			return httpResponse(200, realUsageFixture), nil
		}
		if strings.HasSuffix(req.URL.Query().Get("api"), "-subscription") {
			return httpResponse(200, string(fixture(`{"specCode":"standard","status":"VALID"}`))), nil
		}
		return httpResponse(500, `Cookie=private`), nil
	})
	result, err := client.FetchCookie(context.Background(), "session=synthetic", false)
	if err != nil || calls != 4 || result.Plan != "Token Plan 个人版 Standard" || len(result.Windows) != 1 {
		t.Fatalf("%+v %v calls=%d", result, err, calls)
	}
}

func TestCookieValidationCheckAndPrimaryErrors(t *testing.T) {
	client := testClient()
	for _, cookie := range []string{"", " ", "session=x\r\nsecret=y"} {
		_, err := client.FetchCookie(context.Background(), cookie, false)
		requireCode(t, err, "invalid_cookie")
	}
	client.HTTP = doerFunc(func(req *http.Request) (*http.Response, error) { return httpResponse(200, string(fixture(`{}`))), nil })
	result, err := client.FetchCookie(context.Background(), "session=synthetic", true)
	if err != nil || len(result.Windows) != 0 {
		t.Fatal(result, err)
	}
	for _, tc := range []struct {
		status     int
		body, code string
	}{{401, "private", "not_logged_in"}, {403, "private", "not_logged_in"}, {302, "private", "not_logged_in"}, {500, "private", "http_error"}, {200, `{"code":"ConsoleNeedLogin","message":"private"}`, "not_logged_in"}, {200, "garbage", "invalid_response"}, {200, strings.Repeat("x", maxResponseBytes+1), "http_error"}} {
		client.HTTP = doerFunc(func(*http.Request) (*http.Response, error) { return httpResponse(tc.status, tc.body), nil })
		_, err := client.FetchCookie(context.Background(), "session=synthetic", false)
		requireCode(t, err, tc.code)
		if strings.Contains(err.Error(), "private") {
			t.Fatal(err)
		}
	}
	client.HTTP = doerFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("Cookie=private") })
	_, err = client.FetchCookie(context.Background(), "session=synthetic", false)
	requireCode(t, err, "network_error")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.FetchCookie(ctx, "session=synthetic", false)
	requireCode(t, err, "timeout")
}

func TestOptionalTimeoutKeepsGenuineTokenReading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := testClient()
	calls := 0
	client.HTTP = doerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return httpResponse(200, realUsageFixture), nil
		}
		cancel()
		return nil, context.Canceled
	})
	result, err := client.FetchCookie(ctx, "session=synthetic", false)
	if err != nil || len(result.Windows) != 1 || calls != 2 {
		t.Fatal(result, err, calls)
	}
}

func TestCookieRedirectNeverLeaksToOtherOrigin(t *testing.T) {
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		t.Error("redirect followed with cookie", r.Header.Get("Cookie"))
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer origin.Close()
	client := testClient()
	underlying := client.HTTP.(*http.Client)
	client.HTTP = doerFunc(func(req *http.Request) (*http.Response, error) {
		// Rewrite only inside this test; production cannot override RPCURL.
		u, _ := url.Parse(origin.URL)
		req.URL.Scheme = u.Scheme
		req.URL.Host = u.Host
		return underlying.Do(req)
	})
	_, err := client.FetchCookie(context.Background(), "session=synthetic", false)
	requireCode(t, err, "not_logged_in")
	if hits != 0 {
		t.Fatal("redirect was followed")
	}
}
