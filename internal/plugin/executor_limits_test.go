package plugin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestExecutorStreamForcesSSEAndRejectsMissingStream(t *testing.T) {
	m, _ := newExecutorManager(t, func(method string, body []byte) ([]byte, error) {
		if method == pluginabi.MethodHostHTTPDoStream {
			var req hostHTTPReq
			_ = json.Unmarshal(body, &req)
			if !strings.Contains(string(req.Body), `"stream":true`) {
				t.Errorf("upstream stream not forced: %s", req.Body)
			}
			return hostOK(hostStreamStartResp{StatusCode: 200}), nil
		}
		return hostOK(map[string]any{}), nil
	})
	raw, _ := m.HandleCall(pluginabi.MethodExecutorExecuteStream, executionBody("openai", `{"messages":[]}`, true))
	if decodeEnv(t, raw).OK {
		t.Fatal("missing upstream stream id accepted")
	}
	var req executorRequest
	_ = json.Unmarshal(executionBody("openai", `{}`, true), &req)
	req.StreamID = ""
	raw, _ = m.HandleCall(pluginabi.MethodExecutorExecuteStream, mustJSON(req))
	if decodeEnv(t, raw).OK {
		t.Fatal("missing downstream stream id accepted")
	}
}
func TestExecutorNonStreamResponseByteLimit(t *testing.T) {
	m, _ := newExecutorManager(t, func(string, []byte) ([]byte, error) {
		return hostOK(pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(strings.Repeat("x", 100))}), nil
	})
	m.cfg.MaxResponseBytes = 10
	raw, _ := m.HandleCall(pluginabi.MethodExecutorExecute, executionBody("openai", `{}`, false))
	if e := decodeEnv(t, raw); e.OK || !strings.Contains(e.Error.Message, "max-response-bytes") {
		t.Fatal("response limit ignored")
	}
}
func TestExecutorStreamFailurePropagationAndByteLimit(t *testing.T) {
	for _, mode := range []string{"status", "reported-error", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			m, f := newExecutorManager(t, func(method string, _ []byte) ([]byte, error) {
				switch method {
				case pluginabi.MethodHostHTTPDoStream:
					status := 200
					if mode == "status" {
						status = 429
					}
					return hostOK(hostStreamStartResp{StatusCode: status, StreamID: "upstream"}), nil
				case pluginabi.MethodHostHTTPStreamRead:
					switch mode {
					case "status":
						return hostOK(hostStreamReadResp{Payload: []byte(`{"error":{"message":"quota exhausted"}}`), Done: true}), nil
					case "reported-error":
						return hostOK(hostStreamReadResp{Error: "upstream failed dummy-selected-key", Done: true}), nil
					case "oversize":
						return hostOK(hostStreamReadResp{Payload: []byte(strings.Repeat("x", 100)), Done: true}), nil
					}
				}
				return hostOK(map[string]any{}), nil
			})
			if mode == "oversize" {
				m.cfg.MaxResponseBytes = 10
			}
			raw, _ := m.HandleCall(pluginabi.MethodExecutorExecuteStream, executionBody("openai", `{}`, true))
			if mode == "status" {
				e := decodeEnv(t, raw)
				if e.OK || e.Error.HTTPStatus != 429 {
					t.Fatalf("stream status lost: %s", raw)
				}
				return
			}
			if !decodeEnv(t, raw).OK || !m.bridge.WaitForInFlight(time.Second) {
				t.Fatal("stream pump failed")
			}
			calls := f.callsOf(pluginabi.MethodHostStreamClose)
			if len(calls) != 1 {
				t.Fatal("downstream was not closed")
			}
			var closeReq struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(calls[0].payload, &closeReq)
			if closeReq.Error == "" || strings.Contains(closeReq.Error, "dummy-selected-key") {
				t.Fatalf("unsafe stream failure: %s", calls[0].payload)
			}
		})
	}
}
