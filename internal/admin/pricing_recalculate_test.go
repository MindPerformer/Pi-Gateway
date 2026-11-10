package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"pi-gateway/internal/config"
	"pi-gateway/internal/pricing"
	"pi-gateway/internal/quota"
	"pi-gateway/internal/settings"
	"pi-gateway/internal/store"
)

type recalculateTestProvider struct {
	snap     *pricing.Snapshot
	revision int64
	guarded  int
}

func (p *recalculateTestProvider) Snapshot() *pricing.Snapshot { return p.snap }
func (p *recalculateTestProvider) Revision() int64             { return p.revision }
func (p *recalculateTestProvider) WithRevision(expected int64, use func() error) error {
	if expected != p.revision {
		return store.ErrUsageRepricingConflict
	}
	p.guarded++
	return use()
}

func newRecalculateTestProvider(t *testing.T) *recalculateTestProvider {
	t.Helper()
	snap, err := pricing.NewSnapshot("recalculate-test-v1", []pricing.ModelPrice{{Model: "test-model", Standard: &pricing.DecimalRates{Input: "2", CachedInput: "1", CacheWrite: "3", Output: "4"}, Source: "manual"}})
	if err != nil {
		t.Fatal(err)
	}
	return &recalculateTestProvider{snap: snap, revision: 7}
}

func newRecalculateTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedRecalculateUsage(t *testing.T, st *store.Store, r store.UsageRecord) int64 {
	t.Helper()
	if r.Model == "" {
		r.Model = "test-model"
	}
	if r.StartedAt == 0 {
		r.StartedAt = 1000
	}
	if r.CompletedAt == 0 {
		r.CompletedAt = r.StartedAt + 1
	}
	if r.AccountID == 0 {
		r.AccountID = 1
	}
	if r.Outcome == "" {
		r.Outcome = "succeeded"
	}
	if r.UpstreamSendState == "" {
		r.UpstreamSendState = "sent"
	}
	outcome, completed := r.Outcome, r.CompletedAt
	id, err := st.StartUsageRecord(t.Context(), &r)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "running" {
		r.Outcome, r.CompletedAt = outcome, completed
		if err := st.FinishUsageRecord(t.Context(), id, &r); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func completeRecalculateUsage() store.UsageRecord {
	return store.UsageRecord{InputTokens: ptr(1_000_000), CachedTokens: ptr(100_000), CacheWriteTokens: ptr(50_000), OutputTokens: ptr(100_000), BillingDetails: `{"version":"original","unavailable_reason":"unknown_model"}`}
}

func callRecalculate(t *testing.T, fn http.HandlerFunc, body usageRecalculationRequest) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	fn(w, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	return w.Code, w.Body.Bytes()
}

func previewRecalculate(t *testing.T, h *UsageRecalculateHandler, body usageRecalculationRequest) usageRecalculationResponse {
	t.Helper()
	status, raw := callRecalculate(t, h.Preview, body)
	if status != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", status, raw)
	}
	var response usageRecalculationResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestRecalculatePreviewApplyQuotaAndOriginalLedgers(t *testing.T) {
	st := newRecalculateTestStore(t)
	provider := newRecalculateTestProvider(t)
	h := NewUsageRecalculateHandler(st, provider, nil)
	const start int64 = 1_800_000_000_000
	r := completeRecalculateUsage()
	r.StartedAt = start
	r.CompletedAt = start + 10
	id := seedRecalculateUsage(t, st, r)
	zero := completeRecalculateUsage()
	zero.StartedAt = start + 20
	zero.CostMicros = ptr(0)
	zero.CostSource = "original-zero"
	zero.PriceVersion = "old"
	seedRecalculateUsage(t, st, zero)
	missing := completeRecalculateUsage()
	missing.StartedAt = start + 30
	missing.OutputTokens = nil
	seedRecalculateUsage(t, st, missing)
	unknown := completeRecalculateUsage()
	unknown.StartedAt = start + 40
	unknown.Model = "unknown-model"
	seedRecalculateUsage(t, st, unknown)
	failed := completeRecalculateUsage()
	failed.Outcome = "failed"
	failed.UpstreamSendState = "not_sent"
	seedRecalculateUsage(t, st, failed)
	running := completeRecalculateUsage()
	running.Outcome = "running"
	seedRecalculateUsage(t, st, running)
	// Snapshot-boundary records may be repriced but must not enter this quota view.
	later := completeRecalculateUsage()
	later.StartedAt = start + 100
	later.CompletedAt = start + 501
	seedRecalculateUsage(t, st, later)
	_, err := st.ExecContext(t.Context(), `INSERT INTO key_budget_windows(api_key_id,period,window_start,used_micros) VALUES(1,'daily',?,12345)`, start)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.ExecContext(t.Context(), `INSERT INTO key_charge_events(request_id,api_key_id,cost_micros,created_at) VALUES('existing',1,12345,?)`, start)
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := st.ListUsageRecords(t.Context(), store.UsageFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	body := usageRecalculationRequest{ExpectedRevision: 7, AccountID: 1, StartAt: start, EndAt: start + 1000}
	p := previewRecalculate(t, h, body)
	if p.Matched != 4 || p.Eligible != 2 || p.MissingUsage != 1 || p.Unpriced != 1 || p.HasMore {
		t.Fatalf("preview %+v", p)
	}
	var count int
	if err := st.QueryRowContext(t.Context(), `SELECT count(*) FROM usage_repriced_costs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("preview wrote ledger: %d %v", count, err)
	}
	expected := provider.snap.Explain("test-model", "", nil, *r.InputTokens, *r.CachedTokens, *r.CacheWriteTokens, *r.OutputTokens)
	if p.CostMicros != 2**expected.TotalCostMicros {
		t.Fatalf("preview total=%d detail=%+v", p.CostMicros, expected)
	}
	s := &Server{store: st}
	window := quota.Window{LimitID: "codex", UsedPercent: 25, ResetAt: start + 18_000_000, WindowSeconds: 18000}
	before := s.buildQuotaWindowCost(t.Context(), 1, window, start+500, start+1000)
	if !before.Partial || before.Usage.CostMicros != 0 {
		t.Fatalf("before=%+v", before)
	}
	body.PreviewToken = p.PreviewToken
	status, raw := callRecalculate(t, h.Apply, body)
	if status != http.StatusOK {
		t.Fatalf("apply %d %s", status, raw)
	}
	var result struct {
		Updated, Skipped int
		CostMicros       int64 `json:"cost_micros"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if provider.guarded != 1 {
		t.Fatal("confirmation did not guard the pricing revision through commit")
	}
	if result.Updated != 2 || result.CostMicros != p.CostMicros {
		t.Fatalf("result=%s", raw)
	}
	after := s.buildQuotaWindowCost(t.Context(), 1, window, start+500, start+1000)
	if after.Usage.CostMicros != *expected.TotalCostMicros || after.Usage.RepricedRequests != 1 || after.Usage.RepricedMicros != *expected.TotalCostMicros || after.Usage.UnpricedRequests != 2 || !after.Partial || after.Unknown {
		t.Fatalf("after=%+v usage=%+v", after, after.Usage)
	}
	if after.EstimatedTotalUSD == nil || *after.EstimatedTotalUSD != float64(*expected.TotalCostMicros)/1e6*4 {
		t.Fatalf("quota estimate=%+v", after)
	}
	final, _, err := st.ListUsageRecords(t.Context(), store.UsageFilter{Limit: 100})
	if err != nil || !reflect.DeepEqual(original, final) {
		t.Fatalf("original ledger mutated: %v", err)
	}
	var budget, charge int64
	if err := st.QueryRowContext(t.Context(), `SELECT used_micros FROM key_budget_windows WHERE api_key_id=1 AND period='daily'`).Scan(&budget); err != nil {
		t.Fatal(err)
	}
	if err := st.QueryRowContext(t.Context(), `SELECT sum(cost_micros) FROM key_charge_events`).Scan(&charge); err != nil {
		t.Fatal(err)
	}
	if budget != 12345 || charge != 12345 {
		t.Fatalf("budget changed: %d %d", budget, charge)
	}
	var version, snapshot, source string
	if err := st.QueryRowContext(t.Context(), `SELECT price_version,rate_snapshot,cost_source FROM usage_repriced_costs WHERE usage_record_id=?`, id).Scan(&version, &snapshot, &source); err != nil {
		t.Fatal(err)
	}
	if version != provider.snap.Version() || snapshot == "" || source != "historical_estimate:manual" {
		t.Fatalf("missing provenance %q %q %q", version, snapshot, source)
	}
	status, raw = callRecalculate(t, h.Apply, body)
	if status != http.StatusOK {
		t.Fatalf("repeat apply %d %s", status, raw)
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Updated != 0 || result.Skipped != 2 || result.CostMicros != 0 {
		t.Fatalf("non-idempotent %s", raw)
	}
	again := previewRecalculate(t, h, usageRecalculationRequest{ExpectedRevision: 7, AccountID: 1, StartAt: start, EndAt: start + 1000})
	if again.AlreadyRepriced != 2 || again.Eligible != 0 {
		t.Fatalf("already repriced %+v", again)
	}
}

func TestRecalculateStalePreviewAndAtomicConflict(t *testing.T) {
	for _, change := range []string{"revision", "snapshot", "tokens", "settled", "deleted", "scope", "expired"} {
		t.Run(change, func(t *testing.T) {
			st := newRecalculateTestStore(t)
			provider := newRecalculateTestProvider(t)
			h := NewUsageRecalculateHandler(st, provider, nil)
			seedRecalculateUsage(t, st, completeRecalculateUsage())
			id := seedRecalculateUsage(t, st, completeRecalculateUsage())
			body := usageRecalculationRequest{ExpectedRevision: 7}
			p := previewRecalculate(t, h, body)
			body.PreviewToken = p.PreviewToken
			switch change {
			case "revision":
				provider.revision++
			case "snapshot":
				provider.snap, _ = pricing.NewSnapshot("new-version", provider.snap.Models())
			case "tokens":
				_, _ = st.ExecContext(t.Context(), `UPDATE usage_records SET input_tokens=input_tokens+1 WHERE id=?`, id)
			case "settled":
				_, _ = st.ExecContext(t.Context(), `UPDATE usage_records SET cost_micros=0 WHERE id=?`, id)
			case "deleted":
				_, _ = st.ExecContext(t.Context(), `DELETE FROM usage_records WHERE id=?`, id)
			case "scope":
				body.AccountID = 2
			case "expired":
				h.previews[p.PreviewToken].expires = h.previews[p.PreviewToken].expires.Add(-16 * 60 * 1e9)
			}
			status, raw := callRecalculate(t, h.Apply, body)
			if status != http.StatusConflict {
				t.Fatalf("status=%d %s", status, raw)
			}
			var count int
			if err := st.QueryRowContext(t.Context(), `SELECT count(*) FROM usage_repriced_costs`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial write count=%d err=%v", count, err)
			}
		})
	}
}

func TestRecalculateBoundedCursorAndCancellation(t *testing.T) {
	st := newRecalculateTestStore(t)
	h := NewUsageRecalculateHandler(st, newRecalculateTestProvider(t), nil)
	first := seedRecalculateUsage(t, st, completeRecalculateUsage())
	second := seedRecalculateUsage(t, st, completeRecalculateUsage())
	body := usageRecalculationRequest{ExpectedRevision: 7, Limit: 1}
	p := previewRecalculate(t, h, body)
	if p.Matched != 1 || p.Eligible != 1 || !p.HasMore || p.NextAfterID != first {
		t.Fatalf("first page %+v", p)
	}
	body.PreviewToken = p.PreviewToken
	status, raw := callRecalculate(t, h.Apply, body)
	if status != 200 {
		t.Fatalf("apply %d %s", status, raw)
	}
	body.PreviewToken = ""
	body.AfterID = p.NextAfterID
	p = previewRecalculate(t, h, body)
	if p.Matched != 1 || p.HasMore || p.NextAfterID != second {
		t.Fatalf("second page %+v", p)
	}
	body.PreviewToken = p.PreviewToken
	rawBody, _ := json.Marshal(body)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	w := httptest.NewRecorder()
	h.Apply(w, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(rawBody)).WithContext(ctx))
	var count int
	if err := st.QueryRowContext(t.Context(), `SELECT count(*) FROM usage_repriced_costs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("cancellation wrote: %d %v", count, err)
	}
	for _, bad := range []usageRecalculationRequest{{ExpectedRevision: 7, Limit: 2001}, {ExpectedRevision: 7, StartAt: 20, EndAt: 10}, {ExpectedRevision: 7, AccountID: -1}} {
		status, _ := callRecalculate(t, h.Preview, bad)
		if status != 400 {
			t.Fatalf("invalid input accepted: %+v status=%d", bad, status)
		}
	}
}

func TestRecalculateRestartPersistenceAndCascade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repricing.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	provider := newRecalculateTestProvider(t)
	h := NewUsageRecalculateHandler(st, provider, nil)
	id := seedRecalculateUsage(t, st, completeRecalculateUsage())
	body := usageRecalculationRequest{ExpectedRevision: 7}
	p := previewRecalculate(t, h, body)
	body.PreviewToken = p.PreviewToken
	status, raw := callRecalculate(t, h.Apply, body)
	if status != 200 {
		t.Fatalf("apply %d %s", status, raw)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cost, err := st.AccountUsageCost(t.Context(), 1, store.UsageRange{}, 0)
	if err != nil || cost.RepricedRequests != 1 || cost.CostMicros != p.CostMicros {
		t.Fatalf("restart cost=%+v err=%v", cost, err)
	}
	if _, err := st.ExecContext(t.Context(), `DELETE FROM usage_records WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := st.QueryRowContext(t.Context(), `SELECT count(*) FROM usage_repriced_costs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan estimate %d %v", count, err)
	}
}

func TestRecalculateUnconfirmedRequestedTierUsesStandard(t *testing.T) {
	st := newRecalculateTestStore(t)
	provider := newRecalculateTestProvider(t)
	models := provider.snap.Models()
	models[0].Fast = &pricing.DecimalRates{Input: "20", CachedInput: "10", CacheWrite: "30", Output: "40"}
	models[0].Flex = &pricing.DecimalRates{Input: "1", CachedInput: "0.5", CacheWrite: "1.5", Output: "2"}
	var err error
	provider.snap, err = pricing.NewSnapshot("all-tier-prices", models)
	if err != nil {
		t.Fatal(err)
	}
	h := NewUsageRecalculateHandler(st, provider, nil)
	for _, requested := range []string{"priority", "flex"} {
		r := completeRecalculateUsage()
		r.RequestedServiceTier = requested
		seedRecalculateUsage(t, st, r)
	}
	r := completeRecalculateUsage()
	standard := provider.snap.Explain("test-model", "standard", nil, *r.InputTokens, *r.CachedTokens, *r.CacheWriteTokens, *r.OutputTokens)
	body := usageRecalculationRequest{ExpectedRevision: 7}
	p := previewRecalculate(t, h, body)
	if p.Eligible != 2 || p.CostMicros != 2**standard.TotalCostMicros {
		t.Fatalf("unconfirmed request tier affected cost: %+v", p)
	}
	body.PreviewToken = p.PreviewToken
	status, raw := callRecalculate(t, h.Apply, body)
	if status != http.StatusOK {
		t.Fatalf("apply %d %s", status, raw)
	}
	var snapshot string
	if err := st.QueryRowContext(t.Context(), `SELECT rate_snapshot FROM usage_repriced_costs ORDER BY usage_record_id LIMIT 1`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Breakdown pricing.Breakdown `json:"breakdown"`
	}
	if err := json.Unmarshal([]byte(snapshot), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Breakdown.Tier != "standard" || stored.Breakdown.TierSource != "default" {
		t.Fatalf("tier provenance %+v", stored.Breakdown)
	}
}

func TestRecalculateConcurrentNewRequestsDoNotInvalidateBatch(t *testing.T) {
	st := newRecalculateTestStore(t)
	h := NewUsageRecalculateHandler(st, newRecalculateTestProvider(t), nil)
	seedRecalculateUsage(t, st, completeRecalculateUsage())
	body := usageRecalculationRequest{ExpectedRevision: 7}
	p := previewRecalculate(t, h, body)
	seedRecalculateUsage(t, st, completeRecalculateUsage())
	body.PreviewToken = p.PreviewToken
	for i := 0; i < 2; i++ {
		status, raw := callRecalculate(t, h.Apply, body)
		if status != http.StatusOK {
			t.Fatalf("new unrelated request invalidated preview/retry: %d %s", status, raw)
		}
	}
	var count int
	if err := st.QueryRowContext(t.Context(), `SELECT count(*) FROM usage_repriced_costs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("preview scope changed: count=%d err=%v", count, err)
	}
}

func TestRecalculateMissingUsageAndUnsentFailures(t *testing.T) {
	st := newRecalculateTestStore(t)
	h := NewUsageRecalculateHandler(st, newRecalculateTestProvider(t), nil)
	for _, field := range []string{"input", "cached", "write", "output"} {
		r := completeRecalculateUsage()
		switch field {
		case "input":
			r.InputTokens = nil
		case "cached":
			r.CachedTokens = nil
		case "write":
			r.CacheWriteTokens = nil
		case "output":
			r.OutputTokens = nil
		}
		seedRecalculateUsage(t, st, r)
	}
	for _, outcome := range []string{"failed", "running"} {
		r := completeRecalculateUsage()
		r.Outcome = outcome
		r.UpstreamSendState = "not_sent"
		seedRecalculateUsage(t, st, r)
	}
	p := previewRecalculate(t, h, usageRecalculationRequest{ExpectedRevision: 7})
	if p.Matched != 4 || p.MissingUsage != 4 || p.Eligible != 0 || p.Unpriced != 0 {
		t.Fatalf("usage completeness %+v", p)
	}
}

func TestRecalculateLegacyMultiplierAndStaleSettings(t *testing.T) {
	st := newRecalculateTestStore(t)
	provider := newRecalculateTestProvider(t)
	holder := settings.New(st, &config.Config{})
	if err := holder.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	current := holder.Get()
	current.PricingOverridesJSON = `{"test-model":2}`
	if err := holder.Set(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	h := NewUsageRecalculateHandler(st, provider, holder)
	r := completeRecalculateUsage()
	seedRecalculateUsage(t, st, r)
	body := usageRecalculationRequest{ExpectedRevision: 7}
	p := previewRecalculate(t, h, body)
	base := provider.snap.Explain("test-model", "", nil, *r.InputTokens, *r.CachedTokens, *r.CacheWriteTokens, *r.OutputTokens)
	if p.CostMicros != 2**base.TotalCostMicros {
		t.Fatalf("legacy multiplier ignored: %+v", p)
	}
	current = holder.Get()
	current.PricingOverridesJSON = `{"test-model":3}`
	if err := holder.Set(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	body.PreviewToken = p.PreviewToken
	status, raw := callRecalculate(t, h.Apply, body)
	if status != http.StatusConflict {
		t.Fatalf("stale multiplier accepted: %d %s", status, raw)
	}
}
