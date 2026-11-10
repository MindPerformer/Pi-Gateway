package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pi-gateway/internal/config"
	"pi-gateway/internal/modelpricing"
	"pi-gateway/internal/pricing"
	"pi-gateway/internal/settings"
	"pi-gateway/internal/store"
)

func newPricingHTTP(t *testing.T) (*Server, *http.ServeMux) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Default()
	service, err := modelpricing.New(ctx, st, modelpricing.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SetSecret(ctx, adminPasswordHashKey, "fixture-hash"); err != nil {
		t.Fatal(err)
	}
	if err = st.CreateAdminSession(ctx, "fixture-token", cfg.Admin.Username, "fixture-hash", time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Config: cfg, Store: st, Pricing: service, Settings: settings.New(st, cfg)})
	mux := http.NewServeMux()
	server.Routes(mux, http.NotFoundHandler())
	return server, mux
}
func pricingRequest(t *testing.T, mux http.Handler, method, path string, body any, auth bool) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	switch v := body.(type) {
	case string:
		raw = []byte(v)
	case nil:
		raw = []byte("{}")
	default:
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if auth {
		r.Header.Set("Authorization", "Bearer fixture-token")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}
func TestPricingRoutesRequireAuth(t *testing.T) {
	_, mux := newPricingHTTP(t)
	for _, route := range []string{"GET /api/pricing/models", "POST /api/pricing/models", "POST /api/pricing/models/reset", "POST /api/pricing/models/block", "POST /api/pricing/models/follow", "GET /api/pricing/sources", "POST /api/pricing/sources", "POST /api/pricing/sources/x/delete", "POST /api/pricing/sources/x/test", "POST /api/pricing/sources/x/sync", "GET /api/pricing/sync-runs", "POST /api/pricing/sync-runs/x/apply", "GET /api/pricing/export", "POST /api/pricing/import", "POST /api/pricing/preview", "GET /api/pricing/history", "POST /api/pricing/history/x/restore", "POST /api/pricing/recalculate/preview", "POST /api/pricing/recalculate"} {
		parts := strings.SplitN(route, " ", 2)
		w := pricingRequest(t, mux, parts[0], parts[1], nil, false)
		if w.Code != 401 {
			t.Errorf("%s unauth status=%d body=%s", route, w.Code, w.Body.String())
		}
	}
}
func TestPricingHTTPManualPreviewConflictExportAndHistory(t *testing.T) {
	s, mux := newPricingHTTP(t)
	price := pricing.ModelPrice{Model: "vendor/model/with/slashes", Standard: &pricing.DecimalRates{Input: "2", CachedInput: "1", CacheWrite: "3", Output: "4"}, ContextMode: "same"}
	w := pricingRequest(t, mux, "POST", "/api/pricing/models", map[string]any{"expected_revision": 0, "price": price}, true)
	if w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body.String())
	}
	w = pricingRequest(t, mux, "POST", "/api/pricing/models", map[string]any{"expected_revision": 0, "price": price}, true)
	if w.Code != 409 {
		t.Fatalf("stale status=%d", w.Code)
	}
	w = pricingRequest(t, mux, "POST", "/api/pricing/preview", map[string]any{"model": price.Model, "tier": "standard", "input_tokens": 1000000, "output_tokens": 0}, true)
	if w.Code != 200 {
		t.Fatalf("preview %d %s", w.Code, w.Body.String())
	}
	var preview struct {
		Billing pricing.Breakdown `json:"billing"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Billing.TotalCostMicros == nil || *preview.Billing.TotalCostMicros != 2000000 {
		t.Fatalf("billing=%+v", preview.Billing)
	}
	w = pricingRequest(t, mux, "GET", "/api/pricing/models", nil, true)
	var models struct {
		Revision int64                    `json:"revision"`
		Items    []modelpricing.ModelView `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &models); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range models.Items {
		if m.Model == price.Model {
			found = m.Status == "priced" && m.Manual != nil
		}
	}
	if !found || models.Revision != 1 {
		t.Fatal("saved model missing")
	}
	w = pricingRequest(t, mux, "GET", "/api/pricing/export", nil, true)
	var document modelpricing.StandardPriceFile
	if err := json.Unmarshal(w.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion != 1 || document.Unit != "per_million_tokens" || document.Currency != "USD" {
		t.Fatal("invalid export contract")
	}
	if len(s.pricing.History()) != 1 {
		t.Fatal("missing history")
	}
	for _, body := range []string{`{"price":{"model":"missing-revision"}}`, `{"expected_revision":null,"price":{"model":"null-revision"}}`, `{"expected_revision":-1,"price":{"model":"negative-revision"}}`, `{"expected_revision":1} {}`} {
		w = pricingRequest(t, mux, "POST", "/api/pricing/models", body, true)
		if w.Code != 400 {
			t.Errorf("invalid body status=%d body=%s", w.Code, w.Body.String())
		}
	}
}
func TestPricingLargeImportPreviewApplyAndExternalFailure(t *testing.T) {
	s, mux := newPricingHTTP(t)
	p := pricing.ModelPrice{Model: "large-import", Note: strings.Repeat("n", 2<<20), Standard: &pricing.DecimalRates{Input: "1", CachedInput: "0", CacheWrite: "0", Output: "2"}}
	document := modelpricing.StandardPriceFile{SchemaVersion: 1, Currency: "USD", Unit: "per_million_tokens", Models: []pricing.ModelPrice{p}}
	w := pricingRequest(t, mux, "POST", "/api/pricing/import", map[string]any{"expected_revision": 0, "document": document}, true)
	if w.Code != 200 {
		t.Fatalf("2MiB import failed %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Revision int64                `json:"revision"`
		Run      modelpricing.SyncRun `json:"run"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.pricing.Snapshot().Lookup(p.Model, "standard", nil); ok {
		t.Fatal("import bypassed preview")
	}
	w = pricingRequest(t, mux, "POST", "/api/pricing/sync-runs/"+response.Run.ID+"/apply", map[string]any{"expected_revision": response.Revision, "models": []string{p.Model}}, true)
	if w.Code != 200 {
		t.Fatalf("apply %d %s", w.Code, w.Body.String())
	}
	if _, _, ok := s.pricing.Snapshot().Lookup(p.Model, "standard", nil); !ok {
		t.Fatal("import not applied")
	}
	source := modelpricing.Source{ID: "private", URL: "https://127.0.0.1/prices", Format: "standard", Enabled: true}
	rev, err := s.pricing.SaveSource(context.Background(), s.pricing.Revision(), source)
	if err != nil {
		t.Fatal(err)
	}
	w = pricingRequest(t, mux, "POST", "/api/pricing/sources/private/test", nil, true)
	if w.Code != 502 {
		t.Fatalf("private network status=%d", w.Code)
	}
	w = pricingRequest(t, mux, "POST", "/api/pricing/sources/private/sync", nil, true)
	if w.Code != 502 {
		t.Fatalf("failed source status=%d", w.Code)
	}
	if s.pricing.Revision() != rev+1 {
		t.Fatal("sync attempt not persisted")
	}
	w = pricingRequest(t, mux, "POST", "/api/pricing/import", fmt.Sprintf(`{"expected_revision":%d,"document":{"schema_version":1,"currency":"USD","unit":"per_million_tokens","models":[{"model":"oversize","note":"%s"}]}}`, s.pricing.Revision(), strings.Repeat("x", modelpricing.MaxDocumentBytes)), true)
	if w.Code != 413 {
		t.Fatalf("oversize status=%d body=%s", w.Code, w.Body.String())
	}
	s.pricingImports <- struct{}{}
	s.pricingImports <- struct{}{}
	w = pricingRequest(t, mux, "POST", "/api/pricing/import", nil, true)
	<-s.pricingImports
	<-s.pricingImports
	if w.Code != 429 {
		t.Fatalf("import concurrency not bounded: %d", w.Code)
	}
	s.mu.Lock()
	s.pricingImportBudget = 0
	s.pricingImportRefill = time.Now()
	s.mu.Unlock()
	w = pricingRequest(t, mux, "POST", "/api/pricing/import", nil, true)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("import frequency not bounded: %d", w.Code)
	}
}
