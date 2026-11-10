package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"pi-gateway/internal/modelpricing"
	"pi-gateway/internal/pricing"
)

func (s *Server) pricingRoutes(mux *http.ServeMux) {
	routes := map[string]http.HandlerFunc{
		"GET /api/pricing/models":                s.handlePricingModels,
		"POST /api/pricing/models":               s.handlePricingSave,
		"POST /api/pricing/models/reset":         s.handlePricingReset,
		"POST /api/pricing/models/block":         s.handlePricingBlock,
		"POST /api/pricing/models/follow":        s.handlePricingFollow,
		"GET /api/pricing/sources":               s.handlePricingSources,
		"POST /api/pricing/sources":              s.handlePricingSourceSave,
		"POST /api/pricing/sources/{id}/delete":  s.handlePricingSourceDelete,
		"POST /api/pricing/sources/{id}/test":    s.handlePricingSourceTest,
		"POST /api/pricing/sources/{id}/sync":    s.handlePricingSourceSync,
		"GET /api/pricing/sync-runs":             s.handlePricingRuns,
		"POST /api/pricing/sync-runs/{id}/apply": s.handlePricingApply,
		"GET /api/pricing/export":                s.handlePricingExport,
		"POST /api/pricing/import":               s.handlePricingImport,
		"POST /api/pricing/preview":              s.handlePricingPreview,
		"GET /api/pricing/history":               s.handlePricingHistory,
		"POST /api/pricing/history/{id}/restore": s.handlePricingRestore,
	}
	for pattern, handler := range routes {
		mux.HandleFunc(pattern, s.requireAuth(s.pricingAvailable(handler)))
	}
	var provider pricing.Provider
	if s.pricing != nil {
		provider = s.pricing
	}
	recalculate := NewUsageRecalculateHandler(s.store, provider, s.settings)
	mux.HandleFunc("POST /api/pricing/recalculate/preview", s.requireAuth(recalculate.Preview))
	mux.HandleFunc("POST /api/pricing/recalculate", s.requireAuth(recalculate.Apply))
}
func (s *Server) pricingAvailable(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.pricing == nil {
			writeErr(w, http.StatusServiceUnavailable, "pricing service unavailable")
			return
		}
		next(w, r)
	}
}
func pricingDecode(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			writeErr(w, http.StatusRequestEntityTooLarge, "pricing document too large")
		} else {
			writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		}
		return false
	}
	if r.URL.Path != "/api/pricing/preview" {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal(raw, &envelope)
		revision, ok := envelope["expected_revision"]
		var value int64
		if !ok || string(revision) == "null" || json.Unmarshal(revision, &value) != nil || value < 0 {
			writeErr(w, 400, "expected_revision must be a nonnegative integer")
			return false
		}
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeErr(w, http.StatusBadRequest, "request must contain exactly one JSON document")
		return false
	}
	return true
}
func pricingError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, modelpricing.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, modelpricing.ErrExternal):
		status = http.StatusBadGateway
	case errors.Is(err, modelpricing.ErrNotFound):
		status = http.StatusNotFound
	}
	writeErr(w, status, err.Error())
}
func (s *Server) pricingActor() string {
	if s.cfg != nil && s.cfg.Admin.Username != "" {
		return s.cfg.Admin.Username
	}
	return "admin"
}
func pricingRevision(w http.ResponseWriter, revision int64, err error) {
	if err != nil {
		pricingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision})
}
func (s *Server) pricingOverrides() map[string]float64 {
	var values map[string]float64
	if s.settings != nil {
		if v := s.settings.Get(); v != nil {
			_ = json.Unmarshal([]byte(v.PricingOverridesJSON), &values)
		}
	}
	return values
}
func (s *Server) handlePricingModels(w http.ResponseWriter, r *http.Request) {
	rev, items, err := s.pricing.Models(r.Context())
	if err != nil {
		pricingError(w, err)
		return
	}
	overrides := s.pricingOverrides()
	for i := range items {
		if value, ok := overrides[items[i].Model]; ok {
			v := value
			items[i].LegacyMultiplier = &v
		}
	}
	writeJSON(w, 200, map[string]any{"revision": rev, "items": items})
}
func (s *Server) handlePricingSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedRevision int64              `json:"expected_revision"`
		Price            pricing.ModelPrice `json:"price"`
	}
	if !pricingDecode(w, r, &body, 1<<20) {
		return
	}
	rev, err := s.pricing.SaveManual(r.Context(), body.ExpectedRevision, body.Price, s.pricingActor())
	pricingRevision(w, rev, err)
}
func (s *Server) pricingModelAction(w http.ResponseWriter, r *http.Request, action string) {
	var body struct {
		ExpectedRevision int64  `json:"expected_revision"`
		Model            string `json:"model"`
		SourceID         string `json:"source_id"`
	}
	if !pricingDecode(w, r, &body, 1<<20) {
		return
	}
	rev, err := s.pricing.ChangeModel(r.Context(), body.ExpectedRevision, body.Model, action, body.SourceID, s.pricingActor())
	pricingRevision(w, rev, err)
}
func (s *Server) handlePricingReset(w http.ResponseWriter, r *http.Request) {
	s.pricingModelAction(w, r, "reset")
}
func (s *Server) handlePricingBlock(w http.ResponseWriter, r *http.Request) {
	s.pricingModelAction(w, r, "block")
}
func (s *Server) handlePricingFollow(w http.ResponseWriter, r *http.Request) {
	s.pricingModelAction(w, r, "follow")
}
func (s *Server) handlePricingSources(w http.ResponseWriter, r *http.Request) {
	revision, items := s.pricing.SourcesView()
	writeJSON(w, 200, map[string]any{"revision": revision, "items": items})
}
func (s *Server) handlePricingSourceSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedRevision int64               `json:"expected_revision"`
		Source           modelpricing.Source `json:"source"`
	}
	if !pricingDecode(w, r, &body, 1<<20) {
		return
	}
	rev, err := s.pricing.SaveSource(r.Context(), body.ExpectedRevision, body.Source)
	pricingRevision(w, rev, err)
}
func (s *Server) handlePricingSourceDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !pricingDecode(w, r, &body, 1<<20) {
		return
	}
	rev, err := s.pricing.DeleteSource(r.Context(), body.ExpectedRevision, r.PathValue("id"))
	pricingRevision(w, rev, err)
}
func (s *Server) handlePricingSourceTest(w http.ResponseWriter, r *http.Request) {
	count, err := s.pricing.TestSource(r.Context(), r.PathValue("id"))
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, modelpricing.ErrNotFound) {
			status = 404
		}
		writeJSON(w, status, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "count": count})
}
func (s *Server) handlePricingSourceSync(w http.ResponseWriter, r *http.Request) {
	rev, run, err := s.pricing.Sync(r.Context(), r.PathValue("id"))
	if err != nil {
		pricingError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revision": rev, "run": run})
}
func (s *Server) handlePricingRuns(w http.ResponseWriter, r *http.Request) {
	revision, items := s.pricing.RunsView()
	writeJSON(w, 200, map[string]any{"revision": revision, "items": items})
}
func (s *Server) handlePricingApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedRevision int64    `json:"expected_revision"`
		Models           []string `json:"models"`
	}
	if !pricingDecode(w, r, &body, 1<<20) {
		return
	}
	rev, err := s.pricing.Apply(r.Context(), body.ExpectedRevision, r.PathValue("id"), body.Models, s.pricingActor())
	pricingRevision(w, rev, err)
}
func (s *Server) handlePricingExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Disposition", `attachment; filename="model-prices.json"`)
	writeJSON(w, 200, s.pricing.Export())
}
func (s *Server) handlePricingImport(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.pricingImports == nil {
		s.pricingImports = make(chan struct{}, 2)
	}
	gate := s.pricingImports
	s.mu.Unlock()
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	default:
		writeErr(w, http.StatusTooManyRequests, "too many concurrent pricing imports")
		return
	}
	// Administrative imports are CPU/IO-heavy: burst four, refill one every 15s.
	s.mu.Lock()
	now := time.Now()
	if s.pricingImportRefill.IsZero() {
		s.pricingImportBudget = 4
	} else {
		s.pricingImportBudget = min(4, s.pricingImportBudget+now.Sub(s.pricingImportRefill).Seconds()/15)
	}
	s.pricingImportRefill = now
	allowed := s.pricingImportBudget >= 1
	if allowed {
		s.pricingImportBudget--
	}
	s.mu.Unlock()
	if !allowed {
		w.Header().Set("Retry-After", "15")
		writeErr(w, http.StatusTooManyRequests, "pricing import rate limit exceeded")
		return
	}
	var body struct {
		ExpectedRevision int64                          `json:"expected_revision"`
		Document         modelpricing.StandardPriceFile `json:"document"`
	}
	if !pricingDecode(w, r, &body, modelpricing.MaxDocumentBytes) {
		return
	}
	rev, run, err := s.pricing.Import(r.Context(), body.ExpectedRevision, body.Document)
	if err != nil {
		pricingError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revision": rev, "run": run})
}
func (s *Server) handlePricingPreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Price  *pricing.ModelPrice `json:"price"`
		Model  string              `json:"model"`
		Tier   string              `json:"tier"`
		Input  int64               `json:"input_tokens"`
		Cached int64               `json:"cached_tokens"`
		Write  int64               `json:"cache_write_tokens"`
		Output int64               `json:"output_tokens"`
	}
	if !pricingDecode(w, r, &body, 1<<20) {
		return
	}
	if strings.TrimSpace(body.Model) == "" || body.Input < 0 || body.Cached < 0 || body.Write < 0 || body.Output < 0 {
		writeErr(w, 400, "model and nonnegative token counts required")
		return
	}
	snap := s.pricing.Snapshot()
	if body.Price != nil {
		var err error
		snap, err = pricing.NewSnapshot(fmt.Sprintf("preview:%d", s.pricing.Revision()), []pricing.ModelPrice{*body.Price})
		if err != nil {
			pricingError(w, err)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"billing": snap.Explain(body.Model, body.Tier, s.pricingOverrides(), body.Input, body.Cached, body.Write, body.Output)})
}
func (s *Server) handlePricingHistory(w http.ResponseWriter, r *http.Request) {
	revision, items := s.pricing.HistoryView()
	writeJSON(w, 200, map[string]any{"revision": revision, "items": items})
}
func (s *Server) handlePricingRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !pricingDecode(w, r, &body, 1<<20) {
		return
	}
	rev, err := s.pricing.Restore(r.Context(), body.ExpectedRevision, r.PathValue("id"), s.pricingActor())
	pricingRevision(w, rev, err)
}
