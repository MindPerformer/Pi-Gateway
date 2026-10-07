package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"pi-gateway/internal/pricing"
	"pi-gateway/internal/store"
	"pi-gateway/internal/upstream"
)

func TestUsageBillsActualServiceTier(t *testing.T) {
	for _, tc := range []struct {
		name, requested, returned, wantTier string
		wantInput                           float64
	}{
		{"downgraded", "priority", "default", "standard", 2},
		{"confirmed fast", "priority", "priority", "fast", 4},
		{"upstream upgrade", "default", "fast", "fast", 4},
		{"missing confirmation", "priority", "", "standard", 2},
		{"flex", "auto", "flex", "flex", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := &usageTracker{model: "gpt-6-sol", tier: tc.requested, usage: upstream.Usage{HasUsage: true, InputTokens: 1000, OutputTokens: 100}}
			if tc.returned != "" {
				tracker.noteResponseMetadata("response.completed", map[string]any{"response": map[string]any{"service_tier": tc.returned}})
			}
			d := tracker.billingDetails()
			if d.Tier != tc.wantTier || d.EffectiveRates.Input != tc.wantInput || d.TotalCostMicros == nil {
				t.Fatalf("actual tier pricing: %+v", d)
			}
			cost, _ := tracker.cost()
			if cost == nil || *cost != *d.TotalCostMicros {
				t.Fatal("cost differs from detail")
			}
		})
	}
}

func TestUsageMetadataAndBillingSnapshotPersistence(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tracker := &usageTracker{store: db, model: "alias", requestID: "billing", startedMS: time.Now().UnixMilli(), usage: upstream.Usage{HasUsage: true, InputTokens: 300000, CachedTokens: 200000, OutputTokens: 1000}}
	tracker.noteRequestMetadata([]byte(`{"model":"alias","reasoning":{"effort":"xhigh"},"service_tier":"priority"}`))
	id, err := db.StartUsageRecord(t.Context(), &store.UsageRecord{RequestID: "billing", Model: "alias", ReasoningEffort: tracker.reasoningEffort, RequestedServiceTier: tracker.tier})
	if err != nil {
		t.Fatal(err)
	}
	tracker.rowID = id
	sink := &usageSink{Sink: upstream.NopSink{}, t: tracker}
	sink.OnFrame("in", "sse_event", "response.created", nil, map[string]any{"response": map[string]any{"service_tier": "priority", "model": "gpt-5.5"}})
	sink.OnFrame("in", "sse_event", "response.completed", nil, map[string]any{"response": map[string]any{"service_tier": "default", "model": "gpt-6-sol"}})
	tracker.finalize(context.Background(), store.OutcomeOK, "")
	rows, _, err := db.ListUsageRecords(t.Context(), store.UsageFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("records %v %v", rows, err)
	}
	row := rows[0]
	if row.ReasoningEffort != "xhigh" || row.RequestedServiceTier != "priority" || row.ServiceTier != "default" || row.Model != "gpt-6-sol" || row.PriceVersion != pricing.Version {
		t.Fatalf("metadata %+v", row)
	}
	var d pricing.Breakdown
	if err := json.Unmarshal([]byte(row.BillingDetails), &d); err != nil {
		t.Fatal(err)
	}
	if !d.LongContext || d.Tier != "standard" || d.TotalCostMicros == nil || row.CostMicros == nil || *d.TotalCostMicros != *row.CostMicros {
		t.Fatalf("stored bill %+v", d)
	}
	if d.ContextMultipliers.Input != 2 || d.ContextMultipliers.Output != 1.5 || d.TierMultipliers.Input != 1 {
		t.Fatal("incorrect stored rates")
	}
}

func TestUsageNoTokensRemainsUnpriced(t *testing.T) {
	tracker := &usageTracker{model: "gpt-6-sol", actualTier: "priority"}
	d := tracker.billingDetails()
	if d.TotalCostMicros != nil || d.BaseCostMicros != nil || d.UnavailableReason != "missing_usage" {
		t.Fatalf("missing usage became zero: %+v", d)
	}
	tracker.noteRequestMetadata([]byte(`{"reasoning":{"effort":"low"}}`))
	if tracker.reasoningEffort != "low" || tracker.tier != "" {
		t.Fatal("wrong request metadata")
	}
	tracker.noteRequestMetadata([]byte(`{"input":[]}`))
	if tracker.reasoningEffort != "" {
		t.Fatal("removed reasoning was restored")
	}
}
