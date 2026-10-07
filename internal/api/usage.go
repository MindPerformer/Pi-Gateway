package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"pi-gateway/internal/keylimit"
	"pi-gateway/internal/pricing"
	"pi-gateway/internal/store"
	"pi-gateway/internal/upstream"
)

// usageTracker accumulates one request's facts for the usage_records ledger.
//
// It is deliberately independent of diagnostic capture: capture is an opt-in,
// size-capped debug sample that is trimmed per account, while the ledger is the
// billing/statistics record and must not be dropped or truncated.
type usageTracker struct {
	store  *store.Store
	logger *slog.Logger

	rowID     int64
	requestID string
	startedMS int64

	// upstreamStartMS is stamped when the upstream request is built. The
	// reference implementation starts its TTFT clock at the same point, so the
	// gateway's own admission/selection work is excluded from TTFT.
	upstreamStartMS int64
	headersMS       int64
	firstEventMS    int64
	firstTokenMS    int64
	completedMS     int64

	usage     upstream.Usage
	status    int
	sendState string

	accountID         int64
	accountName       string
	keyID             int64
	keyName           string
	model             string
	clientTransport   string
	requestKind       string
	upstreamTransport string
	sessionID         string
	tier              string
	actualTier        string
	reasoningEffort   string
	overrides         map[string]float64
}

// usageSink feeds the tracker while preserving every capture callback.
type usageSink struct {
	upstream.Sink
	t *usageTracker
}

func (s *usageSink) OnRequestHeaders(url string, h http.Header) {
	s.t.noteUpstreamStart(time.Now())
	s.Sink.OnRequestHeaders(url, h)
}

func (s *usageSink) OnResponseHeaders(status int, h http.Header) {
	s.t.noteHeaders(status, time.Now())
	s.Sink.OnResponseHeaders(status, h)
}

func (s *usageSink) OnFrame(dir, kind, eventType string, raw []byte, parsed map[string]any) {
	if dir == "in" {
		s.t.noteFrame(eventType, time.Now())
		s.t.noteResponseMetadata(eventType, parsed)
	}
	s.Sink.OnFrame(dir, kind, eventType, raw, parsed)
}

func (s *usageSink) OnUsage(u upstream.Usage) {
	s.t.noteUsage(u)
	s.Sink.OnUsage(u)
}

func (s *Server) newUsageTracker(key *store.APIKey, account *store.Account, model, clientTransport, upstreamTransport, sessionID, tier string) *usageTracker {
	t := &usageTracker{
		store:             s.store,
		logger:            s.logger,
		requestID:         uuid.NewString(),
		startedMS:         time.Now().UnixMilli(),
		model:             model,
		clientTransport:   clientTransport,
		requestKind:       "generation",
		upstreamTransport: upstreamTransport,
		sessionID:         sessionID,
		tier:              tier,
		overrides:         s.pricingOverrides(),
	}
	if key != nil {
		t.keyID = key.ID
		t.keyName = key.Name
	}
	if account != nil {
		t.accountID = account.ID
		t.accountName = account.Name
	}
	return t
}

// startUsage inserts the running row so an in-flight request is visible and a
// crash leaves a reconcilable trace. Failures are logged, never fatal.
func (s *Server) startUsage(ctx context.Context, t *usageTracker) {
	if t == nil || s.store == nil {
		return
	}
	rec := &store.UsageRecord{
		RequestID:            t.requestID,
		APIKeyID:             t.keyID,
		APIKeyName:           t.keyName,
		AccountID:            t.accountID,
		AccountName:          t.accountName,
		Model:                t.model,
		ClientTransport:      t.clientTransport,
		RequestKind:          t.requestKind,
		ReasoningEffort:      t.reasoningEffort,
		RequestedServiceTier: t.tier,
		UpstreamTransport:    t.upstreamTransport,
		Outcome:              "running",
		UpstreamSendState:    "not_sent",
		SessionID:            t.sessionID,
		StartedAt:            t.startedMS,
	}
	insCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	id, err := s.store.StartUsageRecord(insCtx, rec)
	if err != nil {
		s.logger.Warn("starting usage record failed", "error", err)
		return
	}
	t.rowID = id
}

func (t *usageTracker) noteUpstreamStart(now time.Time) {
	if t.upstreamStartMS == 0 {
		t.upstreamStartMS = now.UnixMilli()
	}
}

func (t *usageTracker) noteHeaders(status int, now time.Time) {
	t.markSent()
	if t.status == 0 {
		t.status = status
	}
	if t.upstreamStartMS != 0 && t.headersMS == 0 {
		if d := now.UnixMilli() - t.upstreamStartMS; d > 0 {
			t.headersMS = d
		}
	}
}

// noteFrame stamps the first event and, separately, the first token.
//
// The reference implementation defines TTFT as the first *output* event, not
// the first event: response.created arrives before any token, so treating it as
// the first token would overstate TTFT for every request.
func (t *usageTracker) noteFrame(eventType string, now time.Time) {
	if t.upstreamStartMS == 0 {
		return
	}
	elapsed := now.UnixMilli() - t.upstreamStartMS
	if elapsed < 1 {
		// The reference implementation clamps every timing sample to at least
		// 1ms; without it a sub-millisecond first token would be recorded as 0,
		// which is indistinguishable from "never observed".
		elapsed = 1
	}
	if t.firstEventMS == 0 && eventType != "" {
		t.firstEventMS = elapsed
	}
	if eventType != "" {
		t.markSent()
	}
	if t.firstTokenMS == 0 && isOutputDelta(eventType) {
		t.firstTokenMS = elapsed
	}
}

func isOutputDelta(eventType string) bool {
	if !strings.HasPrefix(eventType, "response.") {
		return false
	}
	return strings.Contains(eventType, ".delta")
}

func (t *usageTracker) noteUsage(u upstream.Usage) {
	if u.HasUsage {
		t.usage = u
	}
}

func (t *usageTracker) noteRequestMetadata(raw []byte) {
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return
	}
	t.tier = stringField(body, "service_tier")
	reasoning, _ := body["reasoning"].(map[string]any)
	t.reasoningEffort = stringField(reasoning, "effort")
}

func (t *usageTracker) noteResponseMetadata(eventType string, payload map[string]any) {
	if !strings.HasPrefix(eventType, "response.") {
		return
	}
	response, nested := payload["response"].(map[string]any)
	if !nested {
		response = payload
	}
	if tier := stringField(response, "service_tier"); tier != "" {
		t.actualTier = strings.ToLower(tier)
	}
	if reasoning, ok := response["reasoning"].(map[string]any); ok {
		if effort := stringField(reasoning, "effort"); effort != "" {
			t.reasoningEffort = effort
		}
	}
	// The observed model supersedes aliases and request-side routing names.
	if model := stringField(response, "model"); model != "" {
		t.model = model
	}
}

// markSent records that the upstream request definitely left the gateway. It is
// what allows a retry to be refused for a request that may already have run.
//
// The signal is "the upstream answered", which is the earliest point this layer
// can prove the request was actually transmitted: the request body is written
// and a response (headers or frame) came back. A pure transport failure before
// any response keeps the state at not_sent.
func (t *usageTracker) markSent() {
	if t.sendState != "sent" {
		t.sendState = "sent"
	}
}

// composeRelease runs each release exactly once, in order.
func composeRelease(fns ...func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, fn := range fns {
				if fn != nil {
					fn()
				}
			}
		})
	}
}

// admissionMessage renders a client-readable reason for a denied request.
func admissionMessage(dec keylimit.Decision) string {
	switch dec.Reason {
	case "concurrency":
		return "this API key is at its concurrency limit"
	case "rpm":
		return "this API key exceeded its requests-per-minute limit"
	case "budget":
		return "this API key exceeded its spending budget"
	default:
		return "this API key is not allowed to make this request"
	}
}

// retryAfterHeaders advertises when the client may try again, in whole seconds.
func retryAfterHeaders(d time.Duration) http.Header {
	h := http.Header{}
	if d <= 0 {
		return h
	}
	secs := int(d / time.Second)
	if d%time.Second != 0 {
		secs++
	}
	if secs < 1 {
		secs = 1
	}
	h.Set("Retry-After", strconv.Itoa(secs))
	return h
}

// pricingOverrides parses the operator-supplied multiplier table. A malformed
// value is ignored with a warning rather than failing the request.
func (s *Server) pricingOverrides() map[string]float64 {
	rt := s.settings.Get()
	raw := strings.TrimSpace(rt.PricingOverridesJSON)
	if raw == "" || raw == "{}" {
		return nil
	}
	out := map[string]float64{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		s.logger.Warn("ignoring invalid pricing_overrides_json", "error", err)
		return nil
	}
	return out
}

// cost returns the settled amount and its provenance. A model with no usable
// price stores NULL rather than a fabricated zero.
func (t *usageTracker) cost() (*int64, string) {
	d := t.billingDetails()
	if d.TotalCostMicros == nil {
		return nil, "unknown"
	}
	return d.TotalCostMicros, d.PriceSource
}

func (t *usageTracker) billingDetails() pricing.Breakdown {
	tier := t.actualTier
	source := "upstream"
	if tier == "" {
		// Missing confirmation must never enable a requested Fast surcharge.
		tier = "standard"
		source = "default"
	}
	d := pricing.Explain(t.model, tier, t.overrides, t.usage.InputTokens, t.usage.CachedTokens, t.usage.CacheWriteTokens, t.usage.OutputTokens)
	d.TierSource = source
	if !t.usage.HasUsage {
		d.BaseCostMicros = nil
		d.ContextCostMicros = nil
		d.TotalCostMicros = nil
		d.UnavailableReason = "missing_usage"
	}
	return d
}

// finalize writes the terminal row and settles the key budget by request id, so
// a retried settle can never double-charge.
func (t *usageTracker) finalize(ctx context.Context, outcome string, errText string) {
	if t == nil || t.store == nil {
		return
	}
	t.completedMS = time.Now().UnixMilli()
	latency := t.completedMS - t.startedMS
	if latency < 0 {
		latency = 0
	}

	details := t.billingDetails()
	costMicros, costSource := details.TotalCostMicros, details.PriceSource
	if costMicros == nil {
		costSource = "unknown"
	}
	detailsJSON, _ := json.Marshal(details)
	sendState := t.sendState
	if sendState == "" {
		sendState = "not_sent"
	}

	rec := &store.UsageRecord{
		RequestID:            t.requestID,
		APIKeyID:             t.keyID,
		APIKeyName:           t.keyName,
		AccountID:            t.accountID,
		AccountName:          t.accountName,
		Model:                t.model,
		ClientTransport:      t.clientTransport,
		RequestKind:          t.requestKind,
		UpstreamTransport:    t.upstreamTransport,
		Outcome:              usageOutcome(outcome),
		StatusCode:           t.status,
		ErrorMessage:         errText,
		UpstreamSendState:    sendState,
		HeadersMS:            t.headersMS,
		FirstEventMS:         t.firstEventMS,
		FirstTokenMS:         t.firstTokenMS,
		LatencyMS:            latency,
		CostMicros:           costMicros,
		CostSource:           costSource,
		ReasoningEffort:      t.reasoningEffort,
		RequestedServiceTier: t.tier,
		ServiceTier:          t.actualTier,
		BillingDetails:       string(detailsJSON),
		PriceVersion:         pricing.Version,
		SessionID:            t.sessionID,
		CompletedAt:          t.completedMS,
	}
	if t.usage.HasUsage {
		rec.InputTokens = ptrInt(t.usage.InputTokens)
		rec.CachedTokens = ptrInt(t.usage.CachedTokens)
		rec.CacheWriteTokens = ptrInt(t.usage.CacheWriteTokens)
		rec.OutputTokens = ptrInt(t.usage.OutputTokens)
		rec.ReasoningTokens = ptrInt(t.usage.ReasoningTokens)
		rec.TotalTokens = ptrInt(t.usage.TotalTokens)
	}

	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if t.rowID != 0 {
		if err := t.store.FinishUsageRecord(wctx, t.rowID, rec); err != nil {
			t.logger.Warn("finalising usage record failed", "error", err)
		}
	}
	if costMicros != nil && t.keyID != 0 {
		if _, err := t.store.SettleKeyCharge(wctx, t.requestID, t.keyID, *costMicros, t.completedMS); err != nil {
			t.logger.Warn("settling key charge failed", "error", err)
		}
	}
}

func usageOutcome(outcome string) string {
	switch outcome {
	case store.OutcomeOK:
		return "succeeded"
	case store.OutcomePending:
		return "running"
	case store.OutcomeAborted:
		return "cancelled"
	default:
		return "failed"
	}
}

func ptrInt(v int64) *int64 { return &v }

// stringField reads a top-level string field, ignoring any other type.
func stringField(body map[string]any, key string) string {
	v, ok := body[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}
