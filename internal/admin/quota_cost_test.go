package admin

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"pi-gateway/internal/accounts"
	"pi-gateway/internal/pricing"
	"pi-gateway/internal/quota"
	"pi-gateway/internal/store"
)

func seedAccountCost(t *testing.T, db *store.Store, accountID, start, completed int64, cost *int64, outcome, sent string) {
	t.Helper()
	record := &store.UsageRecord{AccountID: accountID, StartedAt: start, Model: "gpt-6-sol", RequestKind: "compaction"}
	id, err := db.StartUsageRecord(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	if outcome == "running" {
		return
	}
	record.Outcome, record.UpstreamSendState, record.CompletedAt, record.CostMicros = outcome, sent, completed, cost
	if err := db.FinishUsageRecord(context.Background(), id, record); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaCostWindowBoundariesAndEstimate(t *testing.T) {
	db := newAdminTestStore(t)
	s := &Server{store: db}
	const start int64 = 1_800_000_000_000
	const asOf = start + 3_600_000
	window := quota.Window{LimitID: "codex", Role: "primary", UsedPercent: 25, ResetAt: start + 18_000_000, WindowSeconds: 18000}
	// Retained account total includes older and newer requests. The estimate only
	// includes requests started in this cycle and settled by its quota snapshot.
	seedAccountCost(t, db, 1, start-1, start, ptr(90_000_000), "succeeded", "sent")
	seedAccountCost(t, db, 1, start, start+100, ptr(4_000_000), "succeeded", "sent")
	seedAccountCost(t, db, 1, start+200, asOf, ptr(6_000_000), "cancelled", "sent")
	seedAccountCost(t, db, 1, asOf, asOf+100, ptr(70_000_000), "succeeded", "sent")
	seedAccountCost(t, db, 1, asOf-100, asOf+1, ptr(80_000_000), "succeeded", "sent")
	seedAccountCost(t, db, 2, start, start+1, ptr(30_000_000), "succeeded", "sent")
	seedAccountCost(t, db, 1, start+300, start+301, nil, "failed", "not_sent")
	seedAccountCost(t, db, 1, start+400, 0, nil, "running", "sent")
	for i := 0; i < 2; i++ {
		got := s.buildQuotaWindowCost(context.Background(), 1, window, asOf, asOf+1000)
		if got.Usage == nil || got.Usage.CostMicros != 10_000_000 || got.Usage.PricedRequests != 2 || got.Usage.UnpricedRequests != 0 || got.StartAt != start || got.AsOf != asOf || got.UnavailableReason != "" {
			t.Fatalf("window cost: %+v", got)
		}
		if got.EstimatedTotalUSD == nil || *got.EstimatedTotalUSD != 40 || got.EstimatedRemainingUSD == nil || *got.EstimatedRemainingUSD != 30 {
			t.Fatalf("estimate: %+v", got)
		}
	}
	all, err := db.AccountUsageCost(context.Background(), 1, store.UsageRange{}, 0)
	if err != nil || all.CostMicros != 250_000_000 || all.PricedRequests != 5 || all.UnpricedRequests != 0 {
		t.Fatalf("total: %+v %v", all, err)
	}
}

func TestQuotaCostUnavailableAndZero(t *testing.T) {
	const start int64 = 1_800_000_000_000
	const asOf = start + 3_600_000
	for _, tc := range []struct {
		name, want                string
		percent                   float64
		cost                      *int64
		seed                      bool
		asOf, now, reset, seconds int64
	}{
		{"zero cost", "", 25, ptr(0), true, asOf, asOf, start + 18_000_000, 18000},
		{"exhausted", "", 100, ptr(10_000_000), true, asOf, asOf, start + 18_000_000, 18000},
		{"unknown price", "unpriced", 25, nil, true, asOf, asOf, start + 18_000_000, 18000},
		{"no history", "no_usage", 25, nil, false, asOf, asOf, start + 18_000_000, 18000},
		{"zero percent", "no_consumption", 0, ptr(10), true, asOf, asOf, start + 18_000_000, 18000},
		{"invalid percent", "no_consumption", math.NaN(), ptr(10), true, asOf, asOf, start + 18_000_000, 18000},
		{"expired", "expired", 25, ptr(10), true, asOf, start + 18_000_000, start + 18_000_000, 18000},
		{"missing timestamp", "missing_snapshot", 25, ptr(10), true, 0, asOf, start + 18_000_000, 18000},
		{"previous cycle", "missing_snapshot", 25, ptr(10), true, start - 1, asOf, start + 18_000_000, 18000},
		{"future snapshot", "missing_snapshot", 25, ptr(10), true, asOf + 1, asOf, start + 18_000_000, 18000},
		{"missing duration", "missing_window", 25, ptr(10), true, asOf, asOf, start + 18_000_000, 0},
		{"overflow duration", "missing_window", 25, ptr(10), true, asOf, asOf, start + 18_000_000, math.MaxInt64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newAdminTestStore(t)
			if tc.seed {
				seedAccountCost(t, db, 1, start, start+100, tc.cost, "succeeded", "sent")
			}
			s := &Server{store: db}
			got := s.buildQuotaWindowCost(context.Background(), 1, quota.Window{LimitID: "codex", Role: "primary", UsedPercent: tc.percent, ResetAt: tc.reset, WindowSeconds: tc.seconds}, tc.asOf, tc.now)
			if got.UnavailableReason != tc.want {
				t.Fatalf("reason = %q, want %q", got.UnavailableReason, tc.want)
			}
			if tc.want != "" && (got.EstimatedTotalUSD != nil || got.EstimatedRemainingUSD != nil) {
				t.Fatalf("unexpected estimate: %+v", got)
			}
			if tc.name == "zero cost" && (got.EstimatedTotalUSD == nil || *got.EstimatedTotalUSD != 0) {
				t.Fatal("measured zero must remain zero")
			}
			if tc.name == "exhausted" && (got.EstimatedRemainingUSD == nil || *got.EstimatedRemainingUSD != 0) {
				t.Fatal("exhausted quota must have zero remaining value")
			}
		})
	}
}

func TestQuotaCostSnapshotTimestampAndModelLedger(t *testing.T) {
	db := newAdminTestStore(t)
	s := &Server{store: db, accounts: accounts.New(db, nil, nil, accounts.Options{})}
	now := time.Now().UnixMilli()
	asOf := now - 1000
	a := &store.Account{ID: 1, CodexRefreshToken: "linked", QuotaUpdatedAt: now, QuotaError: "refresh failed"}
	window := quota.Window{LimitID: "codex", Role: "secondary", Kind: "7d", UsedPercent: 25, ResetAt: now + 3600000, WindowSeconds: 604800}
	var total int64
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		rule, _, ok := pricing.Lookup(model, "", nil)
		if !ok {
			t.Fatalf("missing fixture price for %s", model)
		}
		cost, ok := pricing.Cost(rule, 1000000, 500000, 0, 10000)
		if !ok {
			t.Fatal("cannot price fixture")
		}
		total += cost
		seedAccountCost(t, db, a.ID, asOf-100, asOf-1, &cost, "succeeded", "sent")
	}
	seedAccountCost(t, db, a.ID, asOf+1, now-1, ptr(100000000), "succeeded", "sent")
	report := &quota.Report{FetchedAt: asOf, Windows: []quota.Window{window, {LimitID: "code_review", Role: "primary", UsedPercent: 25, ResetAt: window.ResetAt, WindowSeconds: window.WindowSeconds}}}
	setSnapshot := func() {
		raw, err := json.Marshal(quota.Snapshot{Report: report})
		if err != nil {
			t.Fatal(err)
		}
		a.QuotaJSON = string(raw)
	}
	setSnapshot()
	got := s.buildQuotaView(context.Background(), a)
	if len(got.WindowCosts) != 1 || got.WindowCosts[0].Usage.CostMicros != total || got.WindowCosts[0].AsOf != asOf {
		t.Fatalf("snapshot cost: %+v", got.WindowCosts)
	}
	if got.Cost == nil || got.Cost.CostMicros != total+100000000 {
		t.Fatalf("account cost: %+v", got.Cost)
	}
	report.FetchedAt = 0
	setSnapshot()
	got = s.buildQuotaView(context.Background(), a)
	if got.WindowCosts[0].UnavailableReason != "missing_snapshot" {
		t.Fatal("failed refresh time used as report timestamp")
	}
	a.QuotaError = ""
	setSnapshot()
	got = s.buildQuotaView(context.Background(), a)
	if got.WindowCosts[0].AsOf != now || got.WindowCosts[0].Usage.CostMicros != total+100000000 {
		t.Fatal("legacy successful snapshot did not use account timestamp")
	}
	a.CodexRefreshToken = ""
	got = s.buildQuotaView(context.Background(), a)
	if got.Report != nil || len(got.WindowCosts) != 0 || got.Cost == nil || got.Cost.CostMicros != total+100000000 {
		t.Fatal("unlinked accounts must keep model cost without stale quota estimates")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got = s.buildQuotaView(ctx, a)
	if got.Cost != nil {
		t.Fatal("failed cost query shown as measured zero")
	}
}
