package bailianquota

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const realUsageFixture = `{"code":"200","data":{"DataV2":{"ret":["SUCCESS::接口调用成功"],"data":{"msg":"Success.","code":"SUCCESS","data":{"per1MonthPercentage":1.0,"per1MonthResetTime":1792252800000},"requestId":"...","success":true}},"success":true,"httpStatus":200,"errorCode":"","api":"...","errorMsg":""},"httpStatusCode":"200","requestId":"...","successResponse":true}`

var observed = time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)

func fixture(data string) json.RawMessage {
	return json.RawMessage(`{"code":"200","data":{"DataV2":{"success":true,"data":{"code":"SUCCESS","success":true,"data":` + data + `}}},"successResponse":true}`)
}
func responses() map[string]json.RawMessage {
	return map[string]json.RawMessage{"usage": json.RawMessage(realUsageFixture)}
}
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var f *Failure
	if !errors.As(err, &f) || f.Code != code {
		t.Fatalf("error = %v; want %s", err, code)
	}
}

func TestRealUsageFixtureAndFrozenContract(t *testing.T) {
	input := responses()
	input["subscription"] = fixture(`{"specCode":"standard","status":"VALID","instanceCode":"private-instance","startTime":1789639884000,"endTime":1821196800000}`)
	input["addon"] = fixture(`{}`)
	input["coding"] = fixture(`{}`)
	result, err := ParseResponses("bsk", input, observed, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "bsk" || result.Plan != "Token Plan 个人版 Standard" || result.PlanStatus != "生效中" || result.ObservedAt != "2026-10-08T17:30:00+08:00" {
		t.Fatal(result)
	}
	if !reflect.DeepEqual(result.Windows, []Window{{"1month", 100, "2026-10-18T00:00:00+08:00"}}) {
		t.Fatal(result.Windows)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || len(object) != 10 {
		t.Fatal(string(raw))
	}
	for _, key := range []string{"source", "plan", "planStatus", "observedAt", "windows", "metrics", "notes"} {
		if _, exists := object[key]; !exists {
			t.Fatalf("missing %s", key)
		}
	}
	if string(object["metrics"]) != "[]" || strings.Contains(string(raw), "private-instance") {
		t.Fatal(string(raw))
	}
	var window map[string]any
	_ = json.Unmarshal(object["windows"], new([]Window))
	windowRaw, _ := json.Marshal(result.Windows[0])
	_ = json.Unmarshal(windowRaw, &window)
	if len(window) != 3 {
		t.Fatal(window)
	}
}

func TestSubscriptionPeriodFields(t *testing.T) {
	start := time.Date(2026, 9, 17, 18, 11, 24, 0, chinaTime)
	end := observed.Add(25 * time.Hour)
	for _, wrapper := range []string{"", "instanceInfo", "planInfo", "subscription", "instance", "queryInstanceInfoResponse", "instanceInfoList"} {
		t.Run(wrapper, func(t *testing.T) {
			data := map[string]any{"planName": "Actual Plan", "startTime": start.UnixMilli(), "endTime": end.UnixMilli()}
			var subscription any = data
			if wrapper == "instanceInfoList" {
				subscription = map[string]any{wrapper: []any{data}}
			} else if wrapper != "" {
				subscription = map[string]any{wrapper: data}
			}
			raw, _ := json.Marshal(subscription)
			input := responses()
			input["subscription"] = fixture(string(raw))
			result, err := ParseResponses("bsk", input, observed, false)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(result)
			var object map[string]any
			_ = json.Unmarshal(body, &object)
			if object["planStart"] != start.Format(time.RFC3339) || object["planEnd"] != end.In(chinaTime).Format(time.RFC3339) || object["daysLeft"] != float64(2) {
				t.Fatalf("period missing or incorrect: %s", body)
			}
		})
	}
}

func TestSubscriptionPeriodAbsentAndInvalidOmitted(t *testing.T) {
	for _, value := range []string{"", `null`, `"not-a-date"`, `"1792252800000"`, `false`, `0`, `-1`, `1792252800`, `1.5`, `1e30`, `253402300800000`} {
		t.Run(value, func(t *testing.T) {
			data := `{}`
			if value != "" {
				data = `{"planName":"Actual Plan","startTime":` + value + `,"endTime":` + value + `}`
			}
			input := responses()
			input["subscription"] = fixture(data)
			result, err := ParseResponses("bsk", input, observed, false)
			if err != nil || len(result.Windows) != 1 {
				t.Fatalf("optional invalid period affected usage: %+v %v", result, err)
			}
			body, _ := json.Marshal(result)
			var object map[string]json.RawMessage
			_ = json.Unmarshal(body, &object)
			for _, field := range []string{"planStart", "planEnd", "daysLeft"} {
				if _, present := object[field]; present {
					t.Fatalf("invented %s: %s", field, body)
				}
			}
		})
	}
}

func TestSubscriptionPeriodIndependentFieldsAndMilliseconds(t *testing.T) {
	for _, tc := range []struct {
		name, data, start, end string
		wantDays               *int
	}{
		{"start_only", `{"startTime":1789639884000}`, "2026-09-17T18:11:24+08:00", "", nil},
		{"invalid_end", `{"startTime":1789639884000,"endTime":"invalid"}`, "2026-09-17T18:11:24+08:00", "", nil},
		{"ambiguous_instances", `{"instanceInfoList":[{"startTime":1789639884000},{"startTime":1789639884000}]}`, "", "", nil},
		{"unrelated_timestamp", `{"unrelated":{"startTime":1789639884000}}`, "", "", nil},
		{"year_overflow", `{"startTime":253402300799999}`, "", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := responses()
			input["subscription"] = fixture(tc.data)
			got, err := ParseResponses("bsk", input, observed, false)
			if err != nil || got.PlanStart != tc.start || got.PlanEnd != tc.end || got.DaysLeft != tc.wantDays {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	end := observed.Add(24*time.Hour + time.Millisecond)
	data, _ := json.Marshal(map[string]any{"endTime": end.UnixMilli()})
	input := responses()
	input["subscription"] = fixture(string(data))
	result, err := ParseResponses("bsk", input, observed, false)
	if err != nil || result.PlanEnd != end.In(chinaTime).Format(time.RFC3339Nano) || result.DaysLeft == nil || *result.DaysLeft != 2 {
		t.Fatalf("millisecond period was truncated: %+v %v", result, err)
	}
}

func TestSubscriptionDaysLeftCeiling(t *testing.T) {
	end := time.Date(2027, 9, 18, 0, 0, 0, 0, chinaTime)
	for _, tc := range []struct {
		name string
		now  time.Time
		want int
	}{
		{"exact_day", end.Add(-24 * time.Hour), 1},
		{"partial_day", end.Add(-time.Hour), 1},
		{"just_over_day", end.Add(-24*time.Hour - time.Nanosecond), 2},
		{"just_under_day", end.Add(-24*time.Hour + time.Nanosecond), 1},
		{"expires_now", end, 0},
		{"expired_partial_day", end.Add(time.Hour), 0},
		{"expired_over_day", end.Add(25 * time.Hour), -1},
		{"far_future", time.Date(2026, 10, 8, 0, 0, 0, 0, chinaTime), 345},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := json.Marshal(map[string]any{"endTime": end.UnixMilli()})
			input := responses()
			input["subscription"] = fixture(string(data))
			result, err := ParseResponses("bsk", input, tc.now, false)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(result)
			var object map[string]any
			_ = json.Unmarshal(body, &object)
			if object["daysLeft"] != float64(tc.want) || object["planEnd"] != end.Format(time.RFC3339) {
				t.Fatalf("want daysLeft %d, got %s", tc.want, body)
			}
			if _, present := object["planStart"]; present {
				t.Fatalf("invented missing planStart: %s", body)
			}
		})
	}
}

func TestQuotaZeroFractionAndAbsentResetAreGenuine(t *testing.T) {
	for _, tc := range []struct {
		fraction string
		want     float64
	}{{"0", 0}, {"0.125", 12.5}, {"1", 100}} {
		result, err := ParseResponses("cookie", map[string]json.RawMessage{"usage": fixture(`{"per1MonthPercentage":` + tc.fraction + `}`)}, observed, false)
		if err != nil || len(result.Windows) != 1 || result.Windows[0].UsedPercent != tc.want || result.Windows[0].ResetTime != "" {
			t.Fatalf("%+v, %v", result, err)
		}
	}
}

func TestInvalidUsageNeverProducesAReading(t *testing.T) {
	for _, data := range []string{`{}`, `{"per1MonthPercentage":null}`, `{"per1MonthPercentage":"0.5"}`, `{"per1MonthPercentage":true}`, `{"per1MonthPercentage":-0.01}`, `{"per1MonthPercentage":1.01}`, `{"per1MonthPercentage":1e999}`} {
		t.Run(data, func(t *testing.T) {
			result, err := ParseResponses("bsk", map[string]json.RawMessage{"usage": fixture(data)}, observed, false)
			requireCode(t, err, "invalid_quota")
			if len(result.Windows) != 0 {
				t.Fatal("invalid reading escaped")
			}
		})
	}
}

func TestInvalidResetRejected(t *testing.T) {
	for _, reset := range []string{`"1792252800000"`, `0`, `-1`, `1792252800`, `1.5`, `1e30`, `253402300800000`, `false`} {
		_, err := ParseResponses("bsk", map[string]json.RawMessage{"usage": fixture(`{"per1MonthPercentage":0.5,"per1MonthResetTime":` + reset + `}`)}, observed, false)
		requireCode(t, err, "invalid_quota")
	}
}

func TestMissingOrMalformedPrimaryResponse(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`no JSON`), json.RawMessage(`{}`), append(json.RawMessage(realUsageFixture), []byte(` {}`)...)} {
		_, err := ParseResponses("bsk", map[string]json.RawMessage{"usage": raw}, observed, false)
		requireCode(t, err, "invalid_response")
	}
}

func TestNotLoggedInAndBusinessErrors(t *testing.T) {
	for _, raw := range []string{`{"code":"ConsoleNeedLogin","message":"Cookie=secret"}`, `{"data":{"DataV2":{"ret":["ConsoleNeedLogin::secret"]}}}`, `{"code":"200","data":{"errorCode":"NotLogin"}}`, `<!doctype html><a href="https://signin.aliyun.com/login">Sign in</a>`} {
		_, err := ParseResponses("bsk", map[string]json.RawMessage{"usage": json.RawMessage(raw)}, observed, false)
		requireCode(t, err, "not_logged_in")
		if strings.Contains(err.Error(), "secret") {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`{"code":"500"}`, `{"code":"200","successResponse":false}`, `{"data":{"DataV2":{"success":false}}}`, `{"data":{"DataV2":{"success":true,"errorCode":"InvalidAPI"}}}`, `{"data":{"DataV2":{"success":true,"data":{"success":false}}}}`, `{"data":{"DataV2":{"success":true,"data":{"code":"FAIL"}}}}`} {
		_, err := ParseResponses("cookie", map[string]json.RawMessage{"usage": json.RawMessage(raw)}, observed, false)
		requireCode(t, err, "rpc_error")
	}
}

func TestSubscriptionNamesStatusesAndUnknownValues(t *testing.T) {
	for _, tc := range []struct{ data, plan, status string }{
		{`{"planName":"Actual Plan","planStatus":"Active"}`, "Actual Plan", "Active"},
		{`{"instanceInfo":{"instanceName":"Nested Plan","instanceStatusName":"生效中"}}`, "Nested Plan", "生效中"},
		{`{"instanceInfoList":[{"specCode":"future-tier","status":"FUTURE_STATE"}]}`, "future-tier", "FUTURE_STATE"},
		{`{"instanceCode":"private-instance","startTime":1}`, "", ""},
	} {
		input := responses()
		input["subscription"] = fixture(tc.data)
		got, err := ParseResponses("bsk", input, observed, false)
		if err != nil || got.Plan != tc.plan || got.PlanStatus != tc.status {
			t.Fatalf("%+v %v", got, err)
		}
	}
}

func TestOptionalCodingErrorsNonfatalAndSecretSafe(t *testing.T) {
	input := responses()
	for _, name := range []string{"subscription", "addon", "coding"} {
		input[name] = json.RawMessage(`{"code":"ConsoleNeedLogin","message":"Cookie=private-cookie; Authorization=private-token"}`)
	}
	result, err := ParseResponses("bsk", input, observed, false)
	if err != nil || len(result.Windows) != 1 || result.Plan != "" || len(result.Metrics) != 0 {
		t.Fatalf("%+v %v", result, err)
	}
	body, _ := json.Marshal(result)
	if strings.Contains(string(body), "private") || strings.Contains(string(body), "Authorization") {
		t.Fatal(string(body))
	}
}

func TestAddonMetricsAndExpiredCodingDoNotReplaceToken(t *testing.T) {
	input := responses()
	input["addon"] = fixture(`{"remainingCredits":0,"totalCredits":12.5}`)
	input["coding"] = fixture(`{"codingPlanInstanceInfos":[{"instanceName":"Expired coding","status":"INVALID"}],"userId":"private-user"}`)
	result, err := ParseResponses("cookie", input, observed, false)
	if err != nil || len(result.Metrics) != 2 || result.Metrics[0].Value != 0 || result.Metrics[1].Value != 12.5 || len(result.Windows) != 1 || result.Plan != "" {
		t.Fatalf("%+v %v", result, err)
	}
	if result.Metrics[0].Key != "addon_remaining_credits" || result.Metrics[1].Key != "addon_total_credits" {
		t.Fatal(result.Metrics)
	}
	body, _ := json.Marshal(result)
	if strings.Contains(string(body), "private") || strings.Contains(string(body), "Expired coding") {
		t.Fatal(string(body))
	}
}

func TestInvalidAddonValuesAreOmitted(t *testing.T) {
	for _, value := range []string{`null`, `-1`, `"0"`, `true`, `1e999`} {
		input := responses()
		input["addon"] = fixture(`{"remainingCredits":` + value + `}`)
		result, err := ParseResponses("bsk", input, observed, false)
		if err != nil || len(result.Metrics) != 0 || len(result.Windows) != 1 {
			t.Fatalf("%+v %v", result, err)
		}
	}
}

func TestCheckRequiresLoginButNotQuotaFields(t *testing.T) {
	result, err := ParseResponses("cookie", map[string]json.RawMessage{"usage": fixture(`{}`)}, observed, true)
	if err != nil || len(result.Windows) != 0 || len(result.Metrics) != 0 || len(result.Notes) != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	_, err = ParseResponses("cookie", nil, observed, true)
	requireCode(t, err, "invalid_response")
}
