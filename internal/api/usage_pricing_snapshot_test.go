package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"pi-gateway/internal/config"
	"pi-gateway/internal/piwire"
	"pi-gateway/internal/pricing"
	"pi-gateway/internal/settings"
	"pi-gateway/internal/store"
	"pi-gateway/internal/upstream"
)

type changingPriceProvider struct {
	current *pricing.Snapshot
	calls   int
}

func (p *changingPriceProvider) Snapshot() *pricing.Snapshot { p.calls++; return p.current }

func requestPriceSnapshot(t *testing.T, version, amount string) *pricing.Snapshot {
	t.Helper()
	s, err := pricing.NewSnapshot(version, []pricing.ModelPrice{
		{Model: "requested", Standard: &pricing.DecimalRates{Input: amount}},
		{Model: "actual", Standard: &pricing.DecimalRates{Input: amount}, Source: "external", SourceID: "source-1"},
		{Model: "compact", Standard: &pricing.DecimalRates{Input: amount}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestUsagePinsPricingAcrossModelChanges(t *testing.T) {
	provider := &changingPriceProvider{current: requestPriceSnapshot(t, "catalog-v1", "2")}
	s := New(Options{Pricing: provider, Settings: settings.New(nil, &config.Config{}), Logger: slog.Default()})
	tracker := s.newUsageTracker(nil, nil, "requested", "http", "http", "", "")
	provider.current = requestPriceSnapshot(t, "catalog-v2", "100")
	tracker.noteUsage(upstream.Usage{HasUsage: true, InputTokens: 100})
	for _, model := range []string{"actual", "compact"} {
		tracker.noteResponseMetadata("response.completed", map[string]any{"model": model})
		d := tracker.billingDetails()
		if d.Version != "catalog-v1" || d.CatalogVersion != "catalog-v1" || d.TotalCostMicros == nil || *d.TotalCostMicros != 200 {
			t.Fatalf("pricing changed mid-request: %+v", d)
		}
	}
	if provider.calls != 1 {
		t.Fatalf("provider read %d times, want one", provider.calls)
	}
	later := s.newUsageTracker(nil, nil, "requested", "http", "http", "", "")
	later.noteUsage(upstream.Usage{HasUsage: true, InputTokens: 100})
	if d := later.billingDetails(); d.Version != "catalog-v2" || d.TotalCostMicros == nil || *d.TotalCostMicros != 10000 {
		t.Fatalf("new request did not see publish: %+v", d)
	}
}

func TestUsagePersistsDynamicVersionAndMatchingBudget(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider := &changingPriceProvider{current: requestPriceSnapshot(t, "catalog-v7", "3")}
	s := New(Options{Store: db, Pricing: provider, Settings: settings.New(nil, &config.Config{}), Logger: slog.Default()})
	tracker := s.newUsageTracker(&store.APIKey{ID: 7}, nil, "actual", "http", "http", "", "")
	tracker.noteUsage(upstream.Usage{HasUsage: true, InputTokens: 100})
	s.startUsage(context.Background(), tracker)
	provider.current = requestPriceSnapshot(t, "catalog-v8", "100")
	tracker.finalize(context.Background(), store.OutcomeOK, "")
	rows, _, err := db.ListUsageRecords(t.Context(), store.UsageFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	row := rows[0]
	var details pricing.Breakdown
	if err := json.Unmarshal([]byte(row.BillingDetails), &details); err != nil {
		t.Fatal(err)
	}
	if row.PriceVersion != "catalog-v7" || row.CostMicros == nil || *row.CostMicros != 300 || details.Version != row.PriceVersion || details.TotalCostMicros == nil || *details.TotalCostMicros != *row.CostMicros || details.SourceID != "source-1" {
		t.Fatalf("inconsistent settled snapshot: row=%+v details=%+v", row, details)
	}
	tracker.finalize(context.Background(), store.OutcomeOK, "")
	windows, err := db.KeyBudgetStatus(t.Context(), 7, tracker.completedMS)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range windows {
		if w.UsedMicros != 300 {
			t.Fatalf("budget does not match settled snapshot or charged twice: %+v", windows)
		}
	}
}

func TestAccountRetryKeepsOriginalSnapshotAndOverrides(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider := &changingPriceProvider{current: requestPriceSnapshot(t, "before-retry", "2")}
	s := New(Options{Store: db, Pricing: provider, Settings: settings.New(nil, &config.Config{}), Logger: slog.Default()})
	first := s.newUsageTracker(nil, nil, "requested", "http", "http", "", "")
	first.overrides = map[string]float64{"actual": 2}
	p := &prepared{Account: &store.Account{ID: 1}, Usage: first, Built: &piwire.BuiltRequest{Model: "actual", JSON: []byte(`{"model":"actual"}`)}, ClientBody: map[string]any{}, RuleContext: map[string]any{}}
	provider.current = requestPriceSnapshot(t, "after-retry", "100")
	s.prepareRetryAccount(t.Context(), p, &store.Account{ID: 2}, func() {}, "retry")
	p.Usage.noteUsage(upstream.Usage{HasUsage: true, InputTokens: 100})
	d := p.Usage.billingDetails()
	if d.Version != "before-retry" || d.TotalCostMicros == nil || *d.TotalCostMicros != 400 || provider.calls != 1 {
		t.Fatalf("retry picked up changed pricing: calls=%d details=%+v", provider.calls, d)
	}
}

func TestUsageNilProviderSnapshotFallsBackToBuiltin(t *testing.T) {
	provider := &changingPriceProvider{}
	s := New(Options{Pricing: provider, Settings: settings.New(nil, &config.Config{}), Logger: slog.Default()})
	tracker := s.newUsageTracker(nil, nil, "gpt-6.1-terra", "http", "http", "", "")
	tracker.noteUsage(upstream.Usage{HasUsage: true, InputTokens: 100})
	d := tracker.billingDetails()
	if d.Version != pricing.Version || d.TotalCostMicros == nil || *d.TotalCostMicros != 100 {
		t.Fatalf("builtin fallback failed: %+v", d)
	}
}
