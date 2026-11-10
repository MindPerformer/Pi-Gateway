package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"pi-gateway/internal/pricing"
	"pi-gateway/internal/settings"
	"pi-gateway/internal/store"
)

// UsageRecalculateHandler keeps previews ephemeral: previewing never writes a
// ledger row. Durable estimates are individually unique by usage record ID.
type UsageRecalculateHandler struct {
	store    *store.Store
	provider pricing.Provider
	settings *settings.Holder
	mu       sync.Mutex
	previews map[string]*usageRecalculationPreview
}

func NewUsageRecalculateHandler(st *store.Store, provider pricing.Provider, holder *settings.Holder) *UsageRecalculateHandler {
	return &UsageRecalculateHandler{store: st, provider: provider, settings: holder, previews: make(map[string]*usageRecalculationPreview)}
}

type usageRecalculationRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Model            string `json:"model,omitempty"`
	AccountID        int64  `json:"account_id,omitempty"`
	StartAt          int64  `json:"start_at,omitempty"`
	EndAt            int64  `json:"end_at,omitempty"`
	Limit            int    `json:"limit,omitempty"`
	AfterID          int64  `json:"after_id,omitempty"`
	PreviewToken     string `json:"preview_token,omitempty"`
}

type usageRecalculationItem struct {
	ID           int64  `json:"id"`
	Model        string `json:"model"`
	AccountID    int64  `json:"account_id"`
	CostMicros   int64  `json:"cost_micros"`
	PriceVersion string `json:"price_version"`
}

type usageRecalculationResponse struct {
	Revision        int64                    `json:"revision"`
	PreviewToken    string                   `json:"preview_token"`
	Matched         int                      `json:"matched"`
	Eligible        int                      `json:"eligible"`
	AlreadyRepriced int                      `json:"already_repriced"`
	MissingUsage    int                      `json:"missing_usage"`
	Unpriced        int                      `json:"unpriced"`
	CostMicros      int64                    `json:"cost_micros"`
	HasMore         bool                     `json:"has_more"`
	NextAfterID     int64                    `json:"next_after_id"`
	Items           []usageRecalculationItem `json:"items"`
	EstimateKind    string                   `json:"estimate_kind"`
}

type usageRecalculationPreview struct {
	request   usageRecalculationRequest
	response  usageRecalculationResponse
	records   []store.UsageRepricingRecord
	updates   []store.UsageRepricingUpdate
	version   string
	overrides string
	expires   time.Time
}

func (h *UsageRecalculateHandler) snapshot() (*pricing.Snapshot, int64) {
	if h.provider == nil {
		return pricing.BuiltinSnapshot(), 0
	}
	revision := func() int64 {
		if p, ok := h.provider.(interface{ Revision() int64 }); ok {
			return p.Revision()
		}
		return 0
	}
	// A catalog replacement must not pair a new snapshot with an old revision.
	for i := 0; i < 3; i++ {
		before := revision()
		snap := h.provider.Snapshot()
		if snap == nil {
			snap = pricing.BuiltinSnapshot()
		}
		if before == revision() {
			return snap, before
		}
	}
	return nil, -1
}

func (h *UsageRecalculateHandler) overrides() (map[string]float64, string) {
	raw := "{}"
	if h.settings != nil {
		if v := h.settings.Get(); v != nil {
			raw = v.PricingOverridesJSON
		}
	}
	var values map[string]float64
	_ = json.Unmarshal([]byte(raw), &values)
	canonical, _ := json.Marshal(values)
	return values, string(canonical)
}

func decodeUsageRecalculation(w http.ResponseWriter, r *http.Request) (usageRecalculationRequest, bool) {
	var body usageRecalculationRequest
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return body, false
	}
	body.Model = strings.TrimSpace(body.Model)
	if body.AccountID < 0 || body.StartAt < 0 || body.EndAt < 0 || body.AfterID < 0 || body.Limit < 0 || body.Limit > 2000 || len(body.Model) > 256 || (body.EndAt > 0 && body.EndAt <= body.StartAt) {
		writeErr(w, http.StatusBadRequest, "invalid recalculation range or limit (maximum 2000)")
		return body, false
	}
	if body.Limit == 0 {
		body.Limit = 500
	}
	return body, true
}

func usageRecalculationFilter(body usageRecalculationRequest) store.UsageFilter {
	return store.UsageFilter{UsageRange: store.UsageRange{Start: body.StartAt, End: body.EndAt}, AccountID: body.AccountID, Model: body.Model, Limit: body.Limit}
}

func (h *UsageRecalculateHandler) validatePrice(p *usageRecalculationPreview) error {
	snap, revision := h.snapshot()
	_, overrides := h.overrides()
	if snap == nil || revision != p.request.ExpectedRevision || snap.Version() != p.version || overrides != p.overrides {
		return errors.New("prices changed since preview; create a new preview")
	}
	return nil
}

// Preview reports one bounded batch. A caller must explicitly advance the ID
// cursor to inspect more history; matched never pretends to be a global count.
func (h *UsageRecalculateHandler) Preview(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeUsageRecalculation(w, r)
	if !ok {
		return
	}
	snap, revision := h.snapshot()
	if snap == nil || body.ExpectedRevision != revision {
		writeErr(w, http.StatusConflict, "pricing revision changed; reload prices")
		return
	}
	overrides, overridesJSON := h.overrides()
	records, more, err := h.store.ListUsageRepricingRecords(r.Context(), usageRecalculationFilter(body), body.AfterID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	response := usageRecalculationResponse{Revision: revision, Matched: len(records), HasMore: more, Items: []usageRecalculationItem{}, EstimateKind: "current_price_historical_estimate"}
	updates := make([]store.UsageRepricingUpdate, 0, len(records))
	for _, record := range records {
		response.NextAfterID = record.ID
		if record.RepricedMicros != nil {
			response.AlreadyRepriced++
			continue
		}
		if record.InputTokens == nil || record.CachedTokens == nil || record.CacheWriteTokens == nil || record.OutputTokens == nil {
			response.MissingUsage++
			continue
		}
		tier := record.ServiceTier
		tierSource := "actual"
		if tier == "" {
			// A requested priority/flex tier is not evidence upstream actually
			// served it. Match live settlement's standard-price fallback.
			tier, tierSource = "standard", "default"
		}
		detail := snap.Explain(record.Model, tier, overrides, *record.InputTokens, *record.CachedTokens, *record.CacheWriteTokens, *record.OutputTokens)
		detail.TierSource = tierSource
		if detail.TotalCostMicros == nil {
			response.Unpriced++
			continue
		}
		cost := *detail.TotalCostMicros
		if cost < 0 || response.CostMicros > math.MaxInt64-cost {
			writeErr(w, http.StatusBadRequest, "recalculation total overflow")
			return
		}
		modelPrice, _ := snap.ResolveModel(record.Model)
		// Keep decimal strings and source metadata as well as the computed
		// breakdown. A version label alone cannot reconstruct old manual rates.
		raw, _ := json.Marshal(struct {
			Breakdown pricing.Breakdown  `json:"breakdown"`
			Model     pricing.ModelPrice `json:"model"`
			Overrides map[string]float64 `json:"overrides,omitempty"`
		}{detail, modelPrice, overrides})
		updates = append(updates, store.UsageRepricingUpdate{Record: record, CostMicros: cost, PriceVersion: snap.Version(), RateSnapshot: string(raw), CostSource: "historical_estimate:" + detail.PriceSource})
		response.Items = append(response.Items, usageRecalculationItem{ID: record.ID, Model: record.Model, AccountID: record.AccountID, CostMicros: cost, PriceVersion: snap.Version()})
		response.Eligible++
		response.CostMicros += cost
	}
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot create preview token")
		return
	}
	response.PreviewToken = hex.EncodeToString(token[:])
	body.PreviewToken = ""
	preview := &usageRecalculationPreview{request: body, response: response, records: records, updates: updates, version: snap.Version(), overrides: overridesJSON, expires: time.Now().Add(15 * time.Minute)}
	if err := h.validatePrice(preview); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err := r.Context().Err(); err != nil {
		return
	}
	h.mu.Lock()
	for key, p := range h.previews {
		if time.Now().After(p.expires) {
			delete(h.previews, key)
		}
	}
	if len(h.previews) >= 128 {
		h.mu.Unlock()
		writeErr(w, http.StatusTooManyRequests, "too many active previews; retry after previews expire")
		return
	}
	h.previews[response.PreviewToken] = preview
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, response)
}

// Apply rechecks scope, prices and original token data. Any changed eligible row
// aborts the whole batch; repeated confirmation of the same token is a no-op.
func (h *UsageRecalculateHandler) Apply(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeUsageRecalculation(w, r)
	if !ok {
		return
	}
	token := body.PreviewToken
	body.PreviewToken = ""
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.previews[token]
	if p == nil || time.Now().After(p.expires) {
		writeErr(w, http.StatusConflict, "preview expired or unavailable; create a new preview")
		return
	}
	if body != p.request {
		writeErr(w, http.StatusConflict, "preview scope changed; create a new preview")
		return
	}
	if err := h.validatePrice(p); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	ids := make([]int64, len(p.records))
	for i, record := range p.records {
		ids[i] = record.ID
	}
	current, err := h.store.GetUsageRepricingRecords(r.Context(), ids)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Permit precisely this token's own durable estimates for idempotent retries.
	for i := range current {
		if current[i].RepricedPreviewToken == token {
			current[i].RepricedMicros = nil
			current[i].RepricedPreviewToken = ""
		}
	}
	if !store.EqualUsageRepricingRecords(current, p.records) {
		writeErr(w, http.StatusConflict, store.ErrUsageRepricingConflict.Error())
		return
	}
	var result store.UsageRepricingResult
	apply := func() error {
		if err := h.validatePrice(p); err != nil {
			return err
		}
		var applyErr error
		result, applyErr = h.store.ApplyUsageRepricing(r.Context(), token, p.updates, func() error { return h.validatePrice(p) })
		return applyErr
	}
	// The live service holds its catalog mutation lock through commit, closing
	// the gap between revision validation and writing the historical estimate.
	if guarded, ok := h.provider.(interface {
		WithRevision(int64, func() error) error
	}); ok {
		err = guarded.WithRevision(p.request.ExpectedRevision, apply)
	} else {
		err = apply()
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrUsageRepricingConflict) || h.validatePrice(p) != nil {
			status = http.StatusConflict
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": p.response.Revision, "updated": result.Updated, "skipped": result.Skipped, "cost_micros": result.CostMicros, "has_more": p.response.HasMore, "next_after_id": p.response.NextAfterID, "estimate_kind": "current_price_historical_estimate"})
}
