package bailianquota

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// ParseResponses validates the required Token Plan reading. Optional RPC failures
// cannot turn a genuine reading into an error or produce invented balances.
func ParseResponses(source string, responses map[string]json.RawMessage, observed time.Time, check bool) (Result, error) {
	result := emptyResult(source, observed)
	usage, err := rpcData(responses["usage"])
	if err != nil {
		return Result{}, err
	}
	if check {
		result.Notes = append(result.Notes, "Console login and RPC reachability verified (--check); no quota reading emitted.")
		return result, nil
	}
	month, err := parseWindow(usage, "1month", "per1MonthPercentage", "per1MonthResetTime")
	if err != nil {
		return Result{}, err
	}
	result.Windows = append(result.Windows, month)
	if subscription, err := rpcData(responses["subscription"]); err == nil {
		result.Plan = findText(subscription, []string{"planName", "packageName", "instanceName", "commodityName", "specificationName", "productName"})
		if result.Plan == "" {
			result.Plan = findText(subscription, []string{"specCode"})
			if result.Plan == "standard" {
				result.Plan = "Token Plan 个人版 Standard"
			}
		}
		result.PlanStatus = findText(subscription, []string{"planStatus", "instanceStatusName", "statusName", "instanceStatus", "status"})
		if result.PlanStatus == "VALID" {
			result.PlanStatus = "生效中"
		}
		if result.Plan == "" {
			result.Notes = append(result.Notes, "Subscription returned no recognized plan name; plan is omitted rather than inferred.")
		}
	} else {
		result.Notes = append(result.Notes, "Subscription details unavailable; Token Plan usage is still valid.")
	}
	if addon, err := rpcData(responses["addon"]); err == nil {
		for _, field := range []struct{ key, label string }{{"remainingCredits", "加购包剩余额度"}, {"totalCredits", "加购包总额度"}} {
			if raw, present := addon[field.key]; present {
				number, numeric := raw.(json.Number)
				value, numberErr := number.Float64()
				if !numeric || numberErr != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
					result.Notes = append(result.Notes, "Invalid optional add-on amount omitted.")
					continue
				}
				key := "addon_remaining_credits"
				if field.key == "totalCredits" {
					key = "addon_total_credits"
				}
				result.Metrics = append(result.Metrics, Metric{Key: key, Label: field.label, Value: value, Unit: "credits", Format: "number"})
			}
		}
	} else {
		result.Notes = append(result.Notes, "Optional add-on details unavailable.")
	}
	if _, err := rpcData(responses["coding"]); err != nil {
		result.Notes = append(result.Notes, "Optional Coding Plan details unavailable; Token Plan usage is unaffected.")
	} else {
		// Coding response quota schemas are not verified by the frozen fixture.
		// Calling this API must not imply fabricated request windows.
		result.Notes = append(result.Notes, "Coding Plan response received; unverified quota fields are not reported.")
	}
	return result, nil
}

func parseWindow(data map[string]any, name, percentageKey, resetKey string) (Window, error) {
	used, ok := data[percentageKey].(json.Number)
	if !ok {
		return Window{}, failure("invalid_quota", "Token Plan usage is missing a numeric monthly percentage")
	}
	fraction, err := used.Float64()
	if err != nil || math.IsNaN(fraction) || math.IsInf(fraction, 0) || fraction < 0 || fraction > 1 {
		return Window{}, failure("invalid_quota", "Token Plan monthly percentage must be between 0 and 1")
	}
	if data[resetKey] == nil {
		return Window{Window: name, UsedPercent: fraction * 100}, nil
	}
	reset, ok := data[resetKey].(json.Number)
	if !ok {
		return Window{}, failure("invalid_quota", "Token Plan reset epoch must be numeric when present")
	}
	millis, err := strconv.ParseInt(string(reset), 10, 64)
	// Reject seconds, fractional timestamps and implausible epochs rather than
	// silently presenting a 1970 reset or overflowing time conversion.
	if err != nil || millis < 946684800000 || millis > 253402300799999 {
		return Window{}, failure("invalid_quota", "Token Plan reset time must be a valid millisecond epoch")
	}
	return Window{Window: name, UsedPercent: fraction * 100, ResetTime: time.UnixMilli(millis).In(chinaTime).Format(time.RFC3339)}, nil
}

func rpcData(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, failure("invalid_response", "console RPC returned no response")
	}
	lower := bytes.ToLower(bytes.TrimSpace(raw))
	if bytes.HasPrefix(lower, []byte("<")) && (bytes.Contains(lower, []byte("signin.aliyun.com")) || bytes.Contains(lower, []byte("/login")) || bytes.Contains(lower, []byte("请先登录"))) {
		return nil, failure("not_logged_in", "Bailian console redirected to a login page")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var root map[string]any
	if decoder.Decode(&root) != nil || root == nil {
		return nil, failure("invalid_response", "console RPC returned invalid JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, failure("invalid_response", "console RPC returned trailing data")
	}
	if hasLoginError(root) {
		return nil, failure("not_logged_in", "Bailian console login is missing or expired; log in manually or replace the cookie")
	}
	if code := scalarText(root["code"]); code != "" && code != "200" && code != "SUCCESS" {
		return nil, failure("rpc_error", "console RPC reported an error")
	}
	if success, present := root["successResponse"].(bool); present && !success {
		return nil, failure("rpc_error", "console RPC reported an error")
	}
	outer, _ := root["data"].(map[string]any)
	v2, _ := outer["DataV2"].(map[string]any)
	if v2 == nil {
		return nil, failure("invalid_response", "console RPC is missing DataV2")
	}
	gatewaySuccess, confirmed := v2["success"].(bool)
	if !confirmed {
		gatewaySuccess, confirmed = outer["success"].(bool)
	}
	if !confirmed || !gatewaySuccess {
		return nil, failure("rpc_error", "console gateway did not confirm success")
	}
	if code := scalarText(v2["errorCode"]); code != "" {
		return nil, failure("rpc_error", "console gateway reported an error")
	}
	if code := scalarText(outer["errorCode"]); code != "" {
		return nil, failure("rpc_error", "console gateway reported an error")
	}
	business, _ := v2["data"].(map[string]any)
	if business == nil {
		return nil, failure("invalid_response", "console RPC is missing its business response")
	}
	if success, present := business["success"].(bool); present && !success {
		return nil, failure("rpc_error", "console business RPC reported an error")
	}
	if code := scalarText(business["code"]); code != "" && code != "SUCCESS" && code != "200" {
		return nil, failure("rpc_error", "console business RPC reported an error")
	}
	data, _ := business["data"].(map[string]any)
	if data == nil {
		return nil, failure("invalid_response", "console RPC returned no object data")
	}
	return data, nil
}

func hasLoginError(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if key == "code" || key == "errorCode" || key == "ret" || key == "message" || key == "msg" || key == "errorMsg" {
				if hasLoginError(child) {
					return true
				}
			} else if _, ok := child.(map[string]any); ok && hasLoginError(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if hasLoginError(child) {
				return true
			}
		}
	case string:
		text := strings.ToLower(v)
		return strings.Contains(text, "consoleneedlogin") || strings.Contains(text, "notlogin") || strings.Contains(text, "needlogin") || strings.Contains(text, "nologin") || strings.Contains(text, "未登录") || strings.Contains(text, "请先登录")
	}
	return false
}

func scalarText(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	if number, ok := value.(json.Number); ok {
		return string(number)
	}
	return ""
}

// Prefer explicit fields at the current level, then known instance wrappers.
// Do not hunt arbitrary nested strings or synthesize a tier/status from dates.
func findText(data map[string]any, fields []string) string {
	for _, field := range fields {
		if text, ok := data[field].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	for _, wrapper := range []string{"instanceInfo", "planInfo", "subscription", "instance", "queryInstanceInfoResponse", "instanceInfoList"} {
		switch nested := data[wrapper].(type) {
		case map[string]any:
			if text := findText(nested, fields); text != "" {
				return text
			}
		case []any:
			if len(nested) == 1 {
				if object, ok := nested[0].(map[string]any); ok {
					if text := findText(object, fields); text != "" {
						return text
					}
				}
			}
		}
	}
	return ""
}
