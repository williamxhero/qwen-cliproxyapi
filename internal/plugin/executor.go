// Non-stream and stream execution paths (FR-005/FR-006/FR-007, arch §4/§5
// steps 5-8): resolve the public model ID against the catalog snapshot,
// translate the inbound payload to the record's upstream protocol, call
// upstream through the host HTTP callbacks, and translate back to the
// client protocol. Request authentication is selected by CPA.

package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"qwen-cliproxyapi/internal/adapter/chatcompletions"
	"qwen-cliproxyapi/internal/adapter/shared"
	"qwen-cliproxyapi/internal/catalog"
	"qwen-cliproxyapi/internal/config"
	"qwen-cliproxyapi/internal/errclass"
)

// executorRequest mirrors rpcExecutorRequest: the SDK embeds
// pluginapi.ExecutorRequest untagged, so its fields marshal under Go field
// names ("Model", "SourceFormat", "OriginalRequest", "Stream"). StreamID is
// the DOWNSTREAM host-allocated id for executor.execute_stream emissions;
// ids returned by DoStream are UPSTREAM and never interchangeable (§4).
type executorRequest struct {
	pluginapi.ExecutorRequest
	StreamID string `json:"stream_id,omitempty"`
}

// resolvedExecution carries everything both execution paths need after
// model/key resolution succeeded.
type resolvedExecution struct {
	cfg   config.Config
	rec   catalog.ModelRecord
	key   string
	tools shared.ResponseTools
}

// resolveExecution resolves the requested model against the snapshot and
// extracts the key selected by CPA. A non-nil second return is a ready-made
// failure envelope.
func (m *Manager) resolveExecution(req executorRequest) (*resolvedExecution, []byte) {
	m.mu.RLock()
	cfg, mgr := m.cfg, m.mgr
	m.mu.RUnlock()
	req.SourceFormat = normalizeSourceFormat(req.SourceFormat)
	if (req.SourceFormat == "openai" && !cfg.Protocols.ChatCompletions) || (req.SourceFormat == "claude" && !cfg.Protocols.Messages) {
		return nil, classEnvelope(&errclass.Error{Class: errclass.ClassUnsupported, Message: "client protocol disabled by config"})
	}
	if req.SourceFormat != "openai" && req.SourceFormat != "claude" {
		return nil, classEnvelope(&errclass.Error{Class: errclass.ClassUnsupported, Message: "unsupported client format"})
	}
	if req.AuthProvider != ProviderID {
		return nil, classEnvelope(&errclass.Error{Class: errclass.ClassAuth, Message: "selected auth provider is not qwen"})
	}
	key := strings.TrimSpace(req.AuthAttributes["api_key"])
	debugTrace("executor auth model=%s auth_id=%s provider=%s attr_api_key_present=%t attr_count=%d storage_json_bytes=%d", req.Model, req.AuthID, req.AuthProvider, key != "", len(req.AuthAttributes), len(req.StorageJSON))
	if key == "" {
		return nil, classEnvelope(&errclass.Error{Class: errclass.ClassAuth, Message: "selected auth has no api key"})
	}
	// Attributes are restored by auth.parse; storage is retained by the host
	// across reloads and also supports older host execution snapshots.
	baseURL := req.AuthAttributes["base_url"]
	if baseURL == "" && len(req.StorageJSON) > 0 {
		var record struct {
			BaseURL string `json:"base_url"`
		}
		if json.Unmarshal(req.StorageJSON, &record) != nil {
			return nil, classEnvelope(&errclass.Error{Class: errclass.ClassAuth, Message: "selected auth has invalid storage"})
		}
		baseURL = record.BaseURL
	}
	if baseURL != "" {
		if err := config.ValidateCredentialBaseURL(baseURL); err != nil {
			return nil, classEnvelope(&errclass.Error{Class: errclass.ClassAuth, Message: "selected auth has invalid base_url"})
		}
		cfg.BaseURL = strings.TrimRight(baseURL, "/")
	}
	var rec catalog.ModelRecord
	var found bool
	if mgr != nil && req.Model != "" {
		rec, found = mgr.Lookup(req.Model)
	}
	if !found {
		return nil, classEnvelope(&errclass.Error{
			Class:      errclass.ClassInvalidModel,
			Message:    "model not in routable catalog",
			StatusCode: http.StatusNotFound,
		})
	}
	return &resolvedExecution{cfg: cfg, rec: rec, key: key, tools: *shared.NewResponseTools()}, nil
}

// handleExecute implements executor.execute (non-stream). Stream-flagged
// requests are routed to the stream path instead of rejected.
func (m *Manager) handleExecute(request []byte) ([]byte, error) {
	var req executorRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return ErrEnvelope("invalid_request", "malformed executor request body"), nil
	}
	debugTrace("executor invoked model=%s source_format=%s stream=%t original_body_%s payload_%s", req.Model, req.SourceFormat, req.Stream, debugBodyMeta(req.OriginalRequest), debugBodyMeta(req.Payload))
	req.SourceFormat = normalizeSourceFormat(req.SourceFormat)
	if req.Stream {
		return m.executeStream(req)
	}
	res, failEnv := m.resolveExecution(req)
	if res == nil {
		return failEnv, nil
	}
	upstreamBody, eErr := buildUpstreamRequest(res.rec.Protocol, res.rec.UpstreamID, req.SourceFormat, req.OriginalRequest, res.rec.Thinking, &res.tools)
	if eErr != nil {
		return executionErrorEnvelope(eErr, res), nil
	}

	url := catalog.JoinUpstreamURL(res.cfg.BaseURL, res.rec.EndpointPath)
	debugTrace("executor resolved public_model=%s upstream_model=%s route=%s url=%s key_count=%d", req.Model, res.rec.UpstreamID, res.rec.Protocol, redactSecrets(url, res.cfg, res.key), len(res.cfg.APIKeys))
	debugTrace("executor sending non-stream url=%s body_len=%d", redactSecrets(url, res.cfg, res.key), len(upstreamBody))
	ctx, cancel := context.WithTimeout(context.Background(), res.cfg.RequestTimeout)
	defer cancel()
	resp, err := m.bridge.Do(ctx, pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     url,
		Headers: upstreamAuthHeaders(res.key),
		Body:    upstreamBody,
	})
	if err != nil {
		debugTrace("executor non-stream network error: %v", err)
		return executionErrorEnvelope(errclass.FromNetwork(err), res), nil
	}
	debugTrace("executor received non-stream status=%d body_len=%d", resp.StatusCode, len(resp.Body))
	if resp.StatusCode >= 400 {
		return executionErrorEnvelope(shared.UpstreamStatusError(resp.StatusCode, resp.Body), res), nil
	}
	// Parse/envelope guard only; true OOM prevention belongs to the host transport's byte cap.
	if int64(len(resp.Body)) > res.cfg.MaxResponseBytes {
		return classEnvelope(errclass.Translation("response exceeds max-response-bytes")), nil
	}
	converted, eErr := convertNonStream(res.rec.Protocol, req.SourceFormat, resp.StatusCode, resp.Body, &res.tools)
	if eErr != nil {
		return executionErrorEnvelope(eErr, res), nil
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: converted, Headers: resp.Headers}), nil
}

func buildUpstreamRequest(route catalog.Route, upstreamModel, sourceFormat string, sourceBody []byte, ts *pluginapi.ThinkingSupport, tools ...*shared.ResponseTools) ([]byte, *errclass.Error) {
	if route != catalog.RouteChatCompletions {
		return nil, errclass.Translation("unsupported upstream route")
	}
	return chatcompletions.BuildRequest(upstreamModel, sourceFormat, sourceBody, ts, tools...)
}

func upstreamAuthHeaders(key string) http.Header {
	h := chatcompletions.AuthHeaders(key)
	h.Set("Content-Type", "application/json")
	return h
}

// CPA v8 uses short names; canonical public names are accepted at the boundary.
func normalizeSourceFormat(format string) string {
	switch format {
	case "openai.chat_completions":
		return "openai"
	case "anthropic.messages":
		return "claude"
	}
	return format
}

// convertNonStream routes one upstream response to its adapter's uniform
// translator: every adapter owns status classification (>=400 → §7
// classified errors), native passthrough, and cross-format conversion for
// all client formats.
func convertNonStream(route catalog.Route, sourceFormat string, status int, body []byte, tools ...*shared.ResponseTools) ([]byte, *errclass.Error) {
	switch route {
	case catalog.RouteChatCompletions:
		return chatcompletions.ConvertNonStreamResponse(sourceFormat, status, body, tools...)
	}
	return nil, errclass.Translation("unsupported route")
}

// classEnvelope renders a classified failure as the wire error envelope;
// ToEnvelopeError redacts and propagates Retryable/HTTPStatus host-side.
func classEnvelope(e *errclass.Error) []byte {
	wire := errclass.ToEnvelopeError(e)
	out, _ := json.Marshal(pluginabi.Envelope{OK: false, Error: &wire})
	return out
}

// streamConverter is the common shape of the three adapters' stream
// converters: feed one upstream SSE chunk, get translated client events.
type streamConverter interface {
	Feed(chunk []byte) (events [][]byte, done bool, eErr *errclass.Error)
}

func newStreamConverter(route catalog.Route, sourceFormat string, tools ...*shared.ResponseTools) streamConverter {
	return chatcompletions.NewStreamConverter(sourceFormat, tools...)
}

// handleExecuteStream implements executor.execute_stream (FR-006, §7).
// It decodes once and delegates to executeStream so a stream-flagged
// request arriving via executor.execute is never parsed twice.
func (m *Manager) handleExecuteStream(request []byte) ([]byte, error) {
	var req executorRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return ErrEnvelope("invalid_request", "malformed executor request body"), nil
	}
	debugTrace("executor stream invoked model=%s source_format=%s stream=%t original_body_%s payload_%s", req.Model, req.SourceFormat, req.Stream, debugBodyMeta(req.OriginalRequest), debugBodyMeta(req.Payload))
	return m.executeStream(req)
}

// executeStream runs the already-decoded stream execution: pre-first-byte errors
// (invalid request, unroutable model, upstream HTTP >=400) return immediately as
// error envelopes so CPA can failover pre-emission.
// On success (upstream HTTP 200 OK), the reading and emitting pump loop runs in
// a background goroutine and executeStream returns okEnvelope immediately so the
// host can start draining chunks to the downstream client without buffer deadlocks.
func (m *Manager) executeStream(req executorRequest) ([]byte, error) {
	req.SourceFormat = normalizeSourceFormat(req.SourceFormat)
	if req.StreamID == "" {
		return ErrEnvelope("invalid_request", "downstream stream id is required"), nil
	}
	res, failEnv := m.resolveExecution(req)
	if res == nil {
		return failEnv, nil
	}
	upstreamBody, eErr := buildUpstreamRequest(res.rec.Protocol, res.rec.UpstreamID, req.SourceFormat, req.OriginalRequest, res.rec.Thinking, &res.tools)
	if eErr != nil {
		return executionErrorEnvelope(eErr, res), nil
	}

	// The executor RPC is authoritative even if OriginalRequest omitted stream.
	var streamBody map[string]json.RawMessage
	if json.Unmarshal(upstreamBody, &streamBody) != nil || streamBody == nil {
		return classEnvelope(errclass.Translation("invalid translated stream request")), nil
	}
	streamBody["stream"] = json.RawMessage("true")
	upstreamBody, _ = json.Marshal(streamBody)

	url := catalog.JoinUpstreamURL(res.cfg.BaseURL, res.rec.EndpointPath)
	debugTrace("executor sending stream url=%s body_len=%d", redactSecrets(url, res.cfg, res.key), len(upstreamBody))
	ctx, cancel := context.WithTimeout(context.Background(), res.cfg.RequestTimeout)
	defer cancel()
	st, _, id, err := m.bridge.DoStream(ctx, pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     url,
		Headers: upstreamAuthHeaders(res.key),
		Body:    upstreamBody,
	})
	debugTrace("executor stream DoStream status=%d upstreamID=%s err=%v", st, id, err)
	if err != nil {
		return executionErrorEnvelope(errclass.FromNetwork(err), res), nil
	}
	if st >= 400 {
		var body []byte
		if id != "" {
			watchdog := time.AfterFunc(res.cfg.RequestTimeout, func() {
				_ = m.bridge.StreamClose(id)
			})
			body, _, _, _ = m.bridge.StreamRead(id)
			watchdog.Stop()
			_ = m.bridge.StreamClose(id)
		}
		return executionErrorEnvelope(shared.UpstreamStatusError(st, body), res), nil
	}

	if st < 200 || st >= 300 || id == "" {
		if id != "" {
			_ = m.bridge.StreamClose(id)
		}
		return classEnvelope(errclass.Translation("invalid upstream stream response")), nil
	}

	downID := req.StreamID
	if m.bridge != nil {
		m.bridge.inFlight.Add(1)
	}
	go func() {
		if m.bridge != nil {
			defer m.bridge.inFlight.Done()
		}
		m.pumpStream(downID, id, res, req.SourceFormat)
	}()
	return okEnvelope(struct{}{}), nil
}

func (m *Manager) pumpStream(downID, upstreamID string, res *resolvedExecution, sourceFormat string) {
	var closeOnce sync.Once
	closeStreams := func(downErrMsg string) {
		closeOnce.Do(func() {
			_ = m.bridge.StreamClose(upstreamID)
			_ = m.bridge.StreamCloseDownstream(downID, downErrMsg)
		})
	}
	defer closeStreams("")

	var aborted atomic.Bool
	watchdog := time.AfterFunc(res.cfg.RequestTimeout, func() {
		defer func() {
			if r := recover(); r != nil && m.bridge != nil {
				_ = m.bridge.Log("error", "stream watchdog panicked", nil)
			}
		}()
		aborted.Store(true)
		_ = m.bridge.StreamClose(upstreamID)
	})
	defer watchdog.Stop()

	conv := newStreamConverter(res.rec.Protocol, sourceFormat, &res.tools)
	var (
		total          int64
		upstreamClosed bool
		convDone       bool
	)
	for {
		payload, readErrMsg, closed, err := m.bridge.StreamRead(upstreamID)
		debugTrace("executor stream read chunk_len=%d closed=%t readErrMsg=%q err=%v", len(payload), closed, readErrMsg, err)
		upstreamClosed = closed
		if aborted.Load() {
			closeStreams(errclass.Redact("stream exceeded request-timeout"))
			return
		}
		if err != nil {
			closeStreams(redactSecrets(err.Error(), res.cfg, res.key))
			return
		}
		if readErrMsg != "" {
			closeStreams(redactSecrets(readErrMsg, res.cfg, res.key))
			return
		}
		total += int64(len(payload))
		if total > res.cfg.MaxResponseBytes {
			closeStreams(errclass.Redact("stream exceeded max-response-bytes"))
			return
		}
		events, done, convErr := conv.Feed(payload)
		if convErr != nil {
			closeStreams(redactSecrets(convErr.Message, res.cfg, res.key))
			return
		}
		if emitErr := m.emitAll(downID, events); emitErr != nil {
			closeStreams(redactSecrets(emitErr.Error(), res.cfg, res.key))
			return
		}
		convDone = done
		if convDone || upstreamClosed {
			break
		}
	}
	debugTrace("executor stream loop end convDone=%t upstreamClosed=%t", convDone, upstreamClosed)
	if !convDone && upstreamClosed {
		if flusher, ok := conv.(interface{ Flush() [][]byte }); ok {
			flushed := flusher.Flush()
			if emitErr := m.emitAll(downID, flushed); emitErr != nil {
				closeStreams(redactSecrets(emitErr.Error(), res.cfg, res.key))
				return
			}
		}
	}
}

// emitAll feeds converted events downstream in order. StreamEmit is
// deadline-bounded (emitTimeout), so a host that stops draining the
// downstream stream surfaces here as an error; the pump loop treats that
// as a post-first-byte stream-fatal failure and runs fail(), whose
// Once-closer releases both streams.
func (m *Manager) emitAll(downStreamID string, events [][]byte) error {
	for _, evt := range events {
		if err := m.bridge.StreamEmit(downStreamID, evt); err != nil {
			return err
		}
	}
	return nil
}
