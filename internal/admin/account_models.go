package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"pi-gateway/internal/accounts"
	"pi-gateway/internal/piwire"
	"pi-gateway/internal/upstream"
)

const (
	maxModelTestMessage = 16 << 10
	maxModelTestOutput  = 64 << 10
	maxModelTestWire    = 2 << 20
)

var (
	errModelTestComplete   = errors.New("diagnostic response completed")
	errModelTestIncomplete = errors.New("upstream response was not completed")
	errModelTestFailure    = errors.New("upstream reported a model error")
	errModelTestOutput     = errors.New("model test output exceeds size limit")
	errModelTestWire       = errors.New("model test stream exceeds size limit")
)

type accountModelTestResult struct {
	OK                    bool   `json:"ok"`
	Model                 string `json:"model"`
	Transport             string `json:"transport"`
	Output                string `json:"output"`
	LatencyMS             int64  `json:"latency_ms"`
	FirstTokenMS          *int64 `json:"first_token_ms"`
	Error                 string `json:"error,omitempty"`
	UpstreamStatus        int    `json:"upstream_status,omitempty"`
	UpstreamBody          string `json:"upstream_body,omitempty"`
	UpstreamBodyTruncated bool   `json:"upstream_body_truncated,omitempty"`
	UpstreamEvent         string `json:"upstream_event,omitempty"`
}

// handleAccountModels only reads a local snapshot. Opening the test dialog can
// never generate model traffic or probe an account's proxy.
func (s *Server) handleAccountModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	id, err := pathInt(r, "id")
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid account id")
		return
	}
	catalog, err := s.store.GetAccountModelCatalog(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not read account model catalog")
		return
	}
	if catalog == nil {
		writeErr(w, http.StatusNotFound, "account not found")
		return
	}
	writeJSON(w, http.StatusOK, catalog)
}

func (s *Server) handleRefreshAccountModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	id, err := pathInt(r, "id")
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid account id")
		return
	}
	account, err := s.store.GetAccount(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not load account")
		return
	}
	if account == nil {
		writeErr(w, http.StatusNotFound, "account not found")
		return
	}
	if s.accounts == nil || s.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, "account model service is unavailable")
		return
	}
	userAgent := ""
	if s.settings != nil {
		userAgent = s.settings.Get().UserAgent
	}
	catalog, refreshErr := s.accounts.RefreshModelCatalog(r.Context(), account, s.cfg, userAgent)
	if catalog == nil {
		status, message := http.StatusInternalServerError, "could not refresh account model catalog"
		if errors.Is(refreshErr, accounts.ErrModelCatalogAccountMissing) {
			status, message = http.StatusNotFound, "account no longer exists"
		}
		// A failed write must not return an uncommitted snapshot that the UI
		// could mistake for the new stored catalog or for an empty success.
		writeErr(w, status, message)
		return
	}
	status := http.StatusOK
	if refreshErr != nil {
		status = http.StatusBadGateway
		if errors.Is(refreshErr, accounts.ErrModelCatalogSuperseded) {
			status = http.StatusConflict
		}
		if catalog.Error == "" {
			catalog.Error = refreshErr.Error() // Only classified, credential-free errors.
		}
	}
	s.recordModelDiagnostic(r.Context(), "account.models_refreshed", map[string]any{
		"account_id": id, "ok": refreshErr == nil, "partial": refreshErr == nil && catalog.Error != "",
		"model_count": len(catalog.Models), "error": catalog.Error,
	})
	writeJSON(w, status, catalog)
}

func (s *Server) handleTestAccountModel(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	result := accountModelTestResult{}
	redactor := &modelTestRedactor{}
	var accountID int64
	upstreamStarted := false
	finish := func(status int, message string) {
		result.Error, _ = redactor.bounded(message, maxModelTestDiagnostic, result.UpstreamBodyTruncated)
		result.Model, _ = redactor.bounded(result.Model, 256, false)
		result.LatencyMS = time.Since(started).Milliseconds()
		auditError := result.Error
		if upstreamStarted {
			// Detailed messages can themselves contain the entire upstream body.
			// Keep diagnostics in the response only, never in persisted audit data.
			auditError = ""
			if !result.OK {
				auditError = http.StatusText(status)
			}
		}
		s.recordModelDiagnostic(r.Context(), "account.model_test", map[string]any{
			"account_id": accountID, "model": result.Model, "transport": result.Transport,
			"ok": result.OK, "latency_ms": result.LatencyMS, "error": auditError,
		})
		writeJSON(w, status, result)
	}
	if r.Method != http.MethodPost {
		finish(http.StatusMethodNotAllowed, "POST required")
		return
	}
	var err error
	accountID, err = pathInt(r, "id")
	if err != nil || accountID <= 0 {
		finish(http.StatusBadRequest, "invalid account id")
		return
	}
	var body struct {
		Model   string `json:"model"`
		Message string `json:"message"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		finish(http.StatusBadRequest, "invalid model test JSON body")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		finish(http.StatusBadRequest, "model test body must contain one JSON object")
		return
	}
	result.Model = strings.TrimSpace(body.Model)
	if result.Model == "" || len(result.Model) > 256 || strings.ContainsAny(result.Model, "\r\n\x00") {
		result.Model = ""
		finish(http.StatusBadRequest, "a valid model is required")
		return
	}
	if strings.TrimSpace(body.Message) == "" || len(body.Message) > maxModelTestMessage {
		finish(http.StatusBadRequest, "message is required and must not exceed 16384 bytes")
		return
	}
	if s.accounts == nil || s.upstream == nil || s.cfg == nil {
		finish(http.StatusServiceUnavailable, "account model service is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), accounts.ModelOperationTimeout(s.cfg, 90*time.Second))
	defer cancel()
	account, err := s.store.GetAccount(ctx, accountID)
	if err != nil {
		finish(http.StatusInternalServerError, "could not load account")
		return
	}
	if account == nil {
		finish(http.StatusNotFound, "account not found")
		return
	}
	redactor.addAccount(account)
	disabled, err := s.store.DisabledModelsForAccount(ctx, accountID, nil)
	if err != nil {
		finish(http.StatusInternalServerError, "could not check account model restrictions")
		return
	}
	for _, model := range disabled {
		if model == result.Model {
			finish(http.StatusForbidden, "model is disabled for this account or its groups")
			return
		}
	}
	// This reservation never rotates/falls back to a different account and also
	// rechecks current policy, health, enabled state, cooldown and concurrency.
	account, release, err := s.accounts.AcquireSpecific(ctx, accountID, result.Model)
	if err != nil {
		if ctx.Err() != nil {
			finish(http.StatusRequestTimeout, modelTestSafeError(ctx.Err()))
		} else {
			finish(http.StatusConflict, "account is unavailable, busy, or the model is disabled")
		}
		return
	}
	defer release()
	redactor.addAccount(account)
	userAgent, reasoningEffort, reasoningSummary := "", s.cfg.Reasoning.DefaultEffort, s.cfg.Reasoning.DefaultSummary
	globalTransport := s.cfg.Upstream.Transport
	if s.settings != nil {
		rt := s.settings.Get()
		userAgent, globalTransport = rt.UserAgent, rt.UpstreamTransport
		reasoningEffort, reasoningSummary = rt.ReasoningEffort, rt.ReasoningSummary
	}
	result.Transport = diagnosticTransport(account.UpstreamProtocol, globalTransport)
	// Test the chosen catalog identifier, not an API-key alias. Never substitute
	// an empty message/default model or apply unrelated model mappings.
	built, err := piwire.BuildRequest(map[string]any{
		"model": result.Model,
		"input": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": body.Message},
		}}},
	}, piwire.BuildOptions{DefaultReasoningEffort: reasoningEffort, DefaultReasoningSummary: reasoningSummary})
	if err != nil {
		finish(http.StatusBadRequest, "could not build model test request")
		return
	}
	token, upstreamAccountID, err := s.accounts.EnsureFreshToken(ctx, account)
	// Retain both pre-refresh and newly issued credentials for redaction.
	redactor.addAccount(account)
	redactor.add(token)
	if err != nil {
		if ctx.Err() != nil {
			finish(http.StatusRequestTimeout, modelTestSafeError(ctx.Err()))
		} else {
			finish(http.StatusBadGateway, "could not obtain the account's primary ChatGPT credential")
		}
		return
	}
	// Credential rotation can wait behind a background refresh. Recheck policy
	// after that wait so a restriction change cannot escape via the old snapshot.
	disabled, err = s.store.DisabledModelsForAccount(ctx, accountID, nil)
	if err != nil {
		finish(http.StatusInternalServerError, "could not check account model restrictions")
		return
	}
	for _, model := range disabled {
		if model == result.Model {
			finish(http.StatusForbidden, "model is disabled for this account or its groups")
			return
		}
	}
	if !account.Enabled || account.CooldownUntil > time.Now().UnixMilli() || account.Status == "expired" || account.Status == "invalid" || account.Status == "banned" || account.Status == "quota_exhausted" {
		finish(http.StatusConflict, "account is unavailable")
		return
	}
	result.Transport = diagnosticTransport(account.UpstreamProtocol, globalTransport)
	session := "admin-test-" + uuid.NewString()
	headers := accounts.ModelHeaderOptions(s.cfg, userAgent, token, upstreamAccountID, session)
	req := &upstream.Request{
		ProxyURL: account.ProxyURL, SSEHeaders: piwire.BuildSSEHeaders(headers), WSHeaders: piwire.BuildWSHeaders(headers),
		Body: built.JSON, SessionID: session, AccountID: account.ID, Transport: result.Transport,
	}
	collector := modelTestCollector{started: started, result: &result}
	req.Sink = &collector
	upstreamStarted = true
	streamResult, streamErr := s.upstream.Stream(ctx, req, collector.onEvent)
	if streamResult != nil && streamResult.Transport != "" {
		if streamResult.Transport != "websocket" || result.Transport != "websocket-cached" {
			result.Transport = streamResult.Transport
		}
	}
	result.Output, _ = redactor.bounded(collector.output.String(), maxModelTestOutput, errors.Is(streamErr, errModelTestOutput))
	if !collector.completed || !errors.Is(streamErr, errModelTestComplete) {
		collector.diagnostics(redactor, streamErr)
	}
	if ctx.Err() != nil {
		finish(http.StatusRequestTimeout, modelTestSafeError(ctx.Err()))
		return
	}
	// The private completion sentinel deliberately closes the single-use WS
	// session instead of keeping diagnostic connections/conversations in the pool.
	if !collector.completed || !errors.Is(streamErr, errModelTestComplete) {
		finish(http.StatusBadGateway, modelTestDiagnosticError(streamErr, result.UpstreamBody))
		return
	}
	result.OK = true
	finish(http.StatusOK, "")
}

func diagnosticTransport(account, global string) string {
	switch strings.ToLower(strings.TrimSpace(account)) {
	case "ws", "websocket", "websocket-cached":
		return "websocket-cached"
	case "sse":
		return "sse"
	}
	switch strings.ToLower(strings.TrimSpace(global)) {
	case "websocket", "websocket-cached", "auto":
		return strings.ToLower(strings.TrimSpace(global))
	default: // The management HTTP caller uses SSE for passthrough.
		return "sse"
	}
}

type modelTestCollector struct {
	upstream.NopSink // Deliberately ignores request headers and bodies.
	started          time.Time
	result           *accountModelTestResult
	output           strings.Builder
	wireBytes        int
	events           int
	completed        bool
	lastRaw          []byte
	lastType         string
	failedRaw        []byte
	failedType       string
	bodyTruncated    bool
}

func (c *modelTestCollector) OnResponseHeaders(status int, _ http.Header) {
	// Only an observed HTTP response may populate this field. A status inside
	// an error event is not the transport's HTTP status (SSE failures are 200).
	c.result.UpstreamStatus = status
	c.failedRaw, c.lastRaw = nil, nil
	c.failedType, c.lastType, c.bodyTruncated = "", "", false
}

func (c *modelTestCollector) OnFrame(dir, kind, eventType string, raw []byte, _ map[string]any) {
	if dir != "in" {
		return
	}
	switch kind {
	case upstream.KindHTTPError:
		c.failedRaw, c.failedType = raw, ""
		c.bodyTruncated = eventType == "error_body_truncated"
	case "sse_event", "ws_frame", "ws_binary":
		// Sink observations precede callback normalization, so untyped errors
		// retain their actual received body rather than a synthesized type:error.
		c.lastRaw, c.lastType = raw, eventType
		if kind == "sse_event" {
			c.result.Transport = "sse"
		}
	case "ws_close":
		c.failedRaw, c.failedType, c.bodyTruncated = raw, "", false
	}
}

func (c *modelTestCollector) failEvent(event *upstream.Event, err error) error {
	c.failedRaw, c.failedType = event.Raw, event.Type
	if c.lastRaw != nil {
		c.failedRaw, c.failedType = c.lastRaw, c.lastType
	}
	if event.SSEEvent != "" {
		c.failedType = event.SSEEvent
	}
	c.bodyTruncated = false
	return err
}

func (c *modelTestCollector) diagnostics(redactor *modelTestRedactor, err error) {
	var failure *upstream.FailureError
	if len(c.failedRaw) == 0 && errors.As(err, &failure) {
		c.failedRaw = failure.Payload
	}
	c.result.UpstreamBody, c.result.UpstreamBodyTruncated = redactor.bounded(string(c.failedRaw), maxModelTestDiagnostic, c.bodyTruncated)
	c.result.UpstreamEvent, _ = redactor.bounded(c.failedType, 256, false)
}

func (c *modelTestCollector) appendText(text string) error {
	if text == "" {
		return nil
	}
	if c.result.FirstTokenMS == nil {
		ms := time.Since(c.started).Milliseconds()
		c.result.FirstTokenMS = &ms
	}
	remaining := maxModelTestOutput - c.output.Len()
	if len(text) > remaining {
		// Keep this bounded wire event until redaction has run. Cutting a token
		// here would leak its prefix when the partial output is returned.
		c.output.WriteString(text)
		return errModelTestOutput
	}
	c.output.WriteString(text)
	return nil
}

func (c *modelTestCollector) onEvent(event *upstream.Event) error {
	c.wireBytes += len(event.Raw)
	c.events++
	if c.wireBytes > maxModelTestWire || c.events > 4096 {
		return errModelTestWire
	}
	if failure := upstream.FailureFromEvent(event); failure != nil {
		return c.failEvent(event, failure)
	}
	response, _ := event.Data["response"].(map[string]any)
	if response != nil {
		if e := response["error"]; e != nil && e != false && e != "" {
			return c.failEvent(event, errModelTestFailure)
		}
	}
	switch event.Type {
	case "response.output_text.delta", "response.refusal.delta":
		text, _ := event.Data["delta"].(string)
		return c.appendText(text)
	case upstream.EventResponseFailed, upstream.EventError:
		return c.failEvent(event, errModelTestFailure)
	case upstream.EventResponseIncomplete:
		return c.failEvent(event, errModelTestIncomplete)
	case upstream.EventResponseCompleted, upstream.EventResponseDone:
		status, _ := response["status"].(string)
		if response == nil || status != "completed" {
			return c.failEvent(event, errModelTestIncomplete)
		}
		if actual, ok := response["model"].(string); ok && strings.TrimSpace(actual) != "" && len(actual) <= 256 && !strings.ContainsAny(actual, "\r\n\x00") {
			c.result.Model = actual
		}
		if c.output.Len() == 0 {
			items, _ := response["output"].([]any)
			for _, item := range items {
				object, _ := item.(map[string]any)
				content, _ := object["content"].([]any)
				for _, part := range content {
					block, _ := part.(map[string]any)
					field := "text"
					if block["type"] == "refusal" {
						field = "refusal"
					}
					text, _ := block[field].(string)
					if err := c.appendText(text); err != nil {
						return err
					}
				}
			}
		}
		c.completed = true
		return errModelTestComplete
	}
	return nil
}

func modelTestSafeError(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "model test canceled"
	case errors.Is(err, context.DeadlineExceeded), upstream.IsIdleTimeoutError(err):
		return "model test timed out"
	case errors.Is(err, errModelTestOutput):
		return errModelTestOutput.Error()
	case errors.Is(err, errModelTestWire):
		return errModelTestWire.Error()
	case errors.Is(err, errModelTestFailure):
		return errModelTestFailure.Error()
	case errors.Is(err, errModelTestIncomplete), err == nil:
		return errModelTestIncomplete.Error()
	}
	var failure *upstream.FailureError
	if errors.As(err, &failure) && failure.Status >= 100 && failure.Status <= 599 {
		return fmt.Sprintf("model test upstream returned HTTP %d", failure.Status)
	}
	return "model test upstream connection failed or stream ended before completion"
}

func (s *Server) recordModelDiagnostic(ctx context.Context, action string, detail any) {
	raw, _ := json.Marshal(detail)
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	// Diagnostics are audit-only: no API-key usage record, charge or fake cost.
	_ = s.store.RecordAudit(auditCtx, action, string(raw))
}
