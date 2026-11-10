package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"pi-gateway/internal/accounts"
	"pi-gateway/internal/admin"
	"pi-gateway/internal/api"
	"pi-gateway/internal/config"
	"pi-gateway/internal/egress"
	"pi-gateway/internal/modelpricing"
	"pi-gateway/internal/oauth"
	"pi-gateway/internal/pricing"
	"pi-gateway/internal/settings"
	"pi-gateway/internal/store"
	"pi-gateway/internal/upstream"
)

// Exercise the production dependency graph: an authenticated admin edit must
// change actual Responses settlement without rewriting an earlier unknown bill.
func TestManagedPricingAndHistoricalEstimateIntegration(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pricing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"pricing_test\",\"model\":\"dynamic-pricing-test\",\"status\":\"completed\",\"service_tier\":\"default\",\"usage\":{\"input_tokens\":100,\"output_tokens\":20,\"total_tokens\":120,\"input_tokens_details\":{\"cached_tokens\":0}}}}\n\n")
	}))
	defer up.Close()
	cfg := config.Default()
	cfg.Admin.Password = "pricing-integration-password"
	cfg.Upstream.BaseURL = up.URL + "/backend-api"
	cfg.Upstream.Transport = "sse"
	cfg.Upstream.SSEZstd = false
	holder := settings.New(db, cfg)
	if err := holder.Load(ctx); err != nil {
		t.Fatal(err)
	}
	factory := egress.NewFactory(egress.Options{ConnectTimeout: time.Second})
	service, err := modelpricing.New(ctx, db, modelpricing.Options{Factory: factory, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	account := &store.Account{Name: "pricing-integration", AccountID: "pricing-account", AccessToken: "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"pricing-account"}}`)) + ".", RefreshToken: "fixture", ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), Enabled: true, Weight: 1, Concurrency: 3, Status: store.AccountStatusReady}
	if err := db.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	key := &store.APIKey{Name: "pricing-integration", Key: "sk-pricing-integration", Enabled: true}
	if err := db.CreateKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	manager := accounts.New(db, oauth.NewClient(nil), factory, accounts.Options{Logger: logger})
	manager.SetSettings(holder.Get())
	client := upstream.New(upstream.Config{SSEURL: cfg.UpstreamSSEURL(), WSURL: cfg.UpstreamWSURL(), Originator: "pi", Logger: logger}, factory)
	defer client.Close()
	data := api.New(api.Options{Config: cfg, Store: db, Accounts: manager, Upstream: client, Settings: holder, Pricing: service, Logger: logger})
	defer data.Close()
	control := admin.New(admin.Options{Config: cfg, Store: db, Accounts: manager, Settings: holder, Upstream: client, Factory: factory, Pricing: service, Logger: logger})
	if err := control.BootstrapPassword(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	data.Routes(mux)
	control.Routes(mux, nil)
	server := httptest.NewServer(mux)
	defer server.Close()
	httpClient := &http.Client{Timeout: 10 * time.Second}
	call := func(path, token string, body any, result any) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		payload, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", path, res.StatusCode, payload)
		}
		if result != nil {
			if err := json.Unmarshal(payload, result); err != nil {
				t.Fatalf("%s: %v: %s", path, err, payload)
			}
		}
	}
	response := map[string]any{"model": "dynamic-pricing-test", "input": "hello", "stream": true}
	call("/v1/responses", key.Key, response, nil)
	var firstID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM usage_records WHERE outcome<>'running' ORDER BY id LIMIT 1`).Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	var missing bool
	if err := db.QueryRowContext(ctx, `SELECT cost_micros IS NULL FROM usage_records WHERE id=?`, firstID).Scan(&missing); err != nil || !missing {
		t.Fatalf("expected original unknown cost: %v, %v", missing, err)
	}
	var login struct {
		Token string `json:"token"`
	}
	call("/api/auth/login", "", map[string]any{"username": cfg.Admin.Username, "password": cfg.Admin.Password}, &login)
	if login.Token == "" {
		t.Fatal("empty admin token")
	}
	price := pricing.ModelPrice{Model: "dynamic-pricing-test", Standard: &pricing.DecimalRates{Input: "1", CachedInput: "0", CacheWrite: "0", Output: "2"}, ContextMode: "same", ContextComparison: "gte"}
	call("/api/pricing/models", login.Token, map[string]any{"expected_revision": service.Revision(), "price": price}, nil)
	call("/v1/responses", key.Key, response, nil)
	var amount int64
	var version string
	if err := db.QueryRowContext(ctx, `SELECT cost_micros,price_version FROM usage_records ORDER BY id DESC LIMIT 1`).Scan(&amount, &version); err != nil {
		t.Fatal(err)
	}
	if amount != 140 || version != service.Snapshot().Version() {
		t.Fatalf("new request settlement = %d, %q; want 140, %q", amount, version, service.Snapshot().Version())
	}
	filter := map[string]any{"expected_revision": service.Revision(), "model": price.Model, "account_id": account.ID}
	var preview struct {
		Token      string `json:"preview_token"`
		Eligible   int    `json:"eligible"`
		CostMicros int64  `json:"cost_micros"`
	}
	call("/api/pricing/recalculate/preview", login.Token, filter, &preview)
	if preview.Eligible != 1 || preview.CostMicros != 140 || preview.Token == "" {
		t.Fatalf("recalculation preview = %+v", preview)
	}
	filter["preview_token"] = preview.Token
	call("/api/pricing/recalculate", login.Token, filter, nil)
	cost, err := db.AccountUsageCost(ctx, account.ID, store.UsageRange{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if cost.CostMicros != 280 || cost.UnpricedRequests != 0 {
		t.Fatalf("estimate after recalculation = %+v", cost)
	}
	if err := db.QueryRowContext(ctx, `SELECT cost_micros IS NULL FROM usage_records WHERE id=?`, firstID).Scan(&missing); err != nil || !missing {
		t.Fatalf("recalculation rewrote original bill: %v, %v", missing, err)
	}
	call("/api/pricing/recalculate", login.Token, filter, nil)
	budget, err := db.KeyBudgetStatus(ctx, key.ID, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	for _, window := range budget {
		if window.UsedMicros != 140 {
			t.Fatalf("historical estimate changed %s API key budget: %+v", window.Period, budget)
		}
	}
	cost, err = db.AccountUsageCost(ctx, account.ID, store.UsageRange{}, 0)
	if err != nil || cost.CostMicros != 280 {
		t.Fatalf("repeated recalculation changed estimate: %+v, %v", cost, err)
	}
	reopened, err := modelpricing.New(ctx, db, modelpricing.Options{Factory: factory, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	bill := reopened.Snapshot().Explain(price.Model, "standard", nil, 100, 0, 0, 20)
	if bill.TotalCostMicros == nil || *bill.TotalCostMicros != 140 {
		t.Fatalf("reloaded prices = %+v", bill)
	}
}
