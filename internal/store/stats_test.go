package store

import (
	"context"
	"math"
	"testing"
	"time"
)

func usageInt(n int64) *int64 { return &n }

func finishTestUsage(t *testing.T, s *Store, r UsageRecord) int64 {
	t.Helper()
	outcome, completedAt := r.Outcome, r.CompletedAt
	id, err := s.StartUsageRecord(context.Background(), &r)
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != "running" || r.ID != id {
		t.Fatalf("start must force running: %+v", r)
	}
	r.Outcome, r.CompletedAt = outcome, completedAt
	if err := s.FinishUsageRecord(context.Background(), id, &r); err != nil {
		t.Fatal(err)
	}
	return id
}

func checkFloat(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s=%v, want %v", name, got, want)
	}
}

func TestUsageSummaryAndDimensionsFrozenMetrics(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	start := time.Date(2025, 1, 6, 0, 0, 0, 0, time.UTC).UnixMilli()
	data := []UsageRecord{
		{RequestID: "first", APIKeyID: 1, APIKeyName: "old key", AccountID: 10, AccountName: "old account", Model: "a", Outcome: "succeeded", InputTokens: usageInt(100), CachedTokens: usageInt(40), CacheWriteTokens: usageInt(5), OutputTokens: usageInt(20), ReasoningTokens: usageInt(2), TotalTokens: usageInt(120), LatencyMS: 1000, FirstTokenMS: 200, CostMicros: usageInt(11), StartedAt: start + 600000},
		{RequestID: "second", APIKeyID: 1, APIKeyName: "renamed key", AccountID: 11, AccountName: "other", Model: "a", Outcome: "succeeded", InputTokens: usageInt(0), CachedTokens: usageInt(0), OutputTokens: usageInt(10), TotalTokens: usageInt(10), LatencyMS: 200, FirstTokenMS: 200, CostMicros: usageInt(0), StartedAt: start + 1200000},
		{RequestID: "third", APIKeyID: 2, APIKeyName: "two", AccountID: 10, AccountName: "renamed account", Model: "a", Outcome: "failed", LatencyMS: 300, FirstTokenMS: 100, ErrorCode: "upstream_error", StartedAt: start + 4200000},
		{RequestID: "fourth", APIKeyID: 2, APIKeyName: "two", AccountID: 11, AccountName: "other", Model: "b", Outcome: "cancelled", InputTokens: usageInt(300), CachedTokens: usageInt(60), OutputTokens: usageInt(40), TotalTokens: usageInt(340), LatencyMS: 1000, FirstTokenMS: 0, CostMicros: usageInt(22), StartedAt: start + 86400000 + 600000},
		// cached_tokens>0 is a hit under the frozen contract even when input is NULL.
		{RequestID: "fifth", APIKeyID: 2, APIKeyName: "two", AccountID: 11, AccountName: "other", Model: "b", Outcome: "incomplete", CachedTokens: usageInt(5), OutputTokens: usageInt(100), TotalTokens: usageInt(100), LatencyMS: 100, FirstTokenMS: 200, StartedAt: start + 86400000 + 1200000},
	}
	for _, r := range data {
		finishTestUsage(t, s, r)
	}
	// Pagination is ignored for aggregations.
	f := UsageFilter{UsageRange: UsageRange{Start: start, End: start + 2*86400000}, Limit: 1, Offset: 2}
	u, err := s.UsageSummaryFor(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if u.RequestCount != 5 || u.SuccessCount != 2 || u.FailureCount != 1 || u.CancelledCount != 1 || u.IncompleteCount != 1 {
		t.Fatalf("outcome counts=%+v", u)
	}
	if u.InputTokens != 400 || u.CachedTokens != 105 || u.CacheWriteTokens != 5 || u.OutputTokens != 170 || u.ReasoningTokens != 2 || u.TotalTokens != 570 || u.CostMicros != 33 {
		t.Fatalf("sums=%+v", u)
	}
	if u.CacheEligibleRequests != 3 || u.CacheHitRequests != 3 {
		t.Fatalf("nullable cache counts=%+v", u)
	}
	checkFloat(t, "cache rate", float64(u.CachedTokens)/float64(u.InputTokens), .2625)
	checkFloat(t, "avg latency", u.AvgLatencyMS, 520)
	if u.TTFT != (UsagePercentiles{200, 200, 200}) {
		t.Errorf("TTFT=%+v", u.TTFT)
	}
	// Valid TPS samples are 20*1000/(1000-200)=25 and 40*1000/1000=40.
	// floor(2*ratio) selects 40 for all three frozen order statistics.
	if u.OutputTPS != (UsagePercentiles{40, 40, 40}) {
		t.Errorf("TPS=%+v", u.OutputTPS)
	}

	models, err := s.UsageByModel(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].Key != "a" || models[0].ID != 0 || models[0].RequestCount != 3 || models[0].SuccessCount != 2 || models[0].FailureCount != 1 {
		t.Fatalf("models=%+v", models)
	}
	checkFloat(t, "model a request share", models[0].Share, .6)
	checkFloat(t, "model b request share", models[1].Share, .4)
	checkFloat(t, "model a TPS", models[0].OutputTPS, 25)
	checkFloat(t, "model b TPS", models[1].OutputTPS, 40)
	keys, err := s.UsageByKey(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0].ID != 2 || keys[0].RequestCount != 3 || keys[1].ID != 1 || keys[1].RequestCount != 2 {
		t.Fatalf("keys must group by ID despite renaming: %+v", keys)
	}
	accounts, err := s.UsageByAccount(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 || accounts[0].ID != 11 || accounts[0].RequestCount != 3 || accounts[1].ID != 10 || accounts[1].RequestCount != 2 {
		t.Fatalf("accounts must group by ID despite renaming: %+v", accounts)
	}
	trend, err := s.UsageTrend(ctx, f, "day")
	if err != nil {
		t.Fatal(err)
	}
	if len(trend) != 2 || trend[0].BucketStart != start || trend[0].RequestCount != 3 || trend[1].BucketStart != start+86400000 || trend[1].RequestCount != 2 {
		t.Fatalf("daily trend=%+v", trend)
	}
	checkFloat(t, "daily first TPS", trend[0].OutputTPS, 25)
	checkFloat(t, "daily second TPS", trend[1].OutputTPS, 40)
	hours, err := s.UsageTrend(ctx, f, "hour")
	if err != nil {
		t.Fatal(err)
	}
	if len(hours) != 3 || hours[0].BucketStart != start || hours[1].BucketStart != start+3600000 || hours[2].BucketStart != start+86400000 {
		t.Fatalf("hourly trend=%+v", hours)
	}
	if _, err := s.UsageTrend(ctx, f, "minute"); err == nil {
		t.Error("invalid bucket accepted")
	}

	firstTwo := UsageFilter{UsageRange: UsageRange{Start: start + 600000, End: start + 4200000}}
	u, err = s.UsageSummaryFor(ctx, firstTwo)
	if err != nil {
		t.Fatal(err)
	}
	if u.RequestCount != 2 || u.OutputTPS != (UsagePercentiles{25, 25, 25}) {
		t.Fatalf("half-open interval or single-sample TPS: %+v", u)
	}
	rows, total, err := s.ListUsageRecords(ctx, UsageFilter{Limit: 1, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(rows) != 1 || rows[0].RequestID != "third" || rows[0].InputTokens != nil || rows[0].CostMicros != nil {
		t.Fatalf("nullable page=%+v total=%d", rows, total)
	}
	rows, total, err = s.ListUsageRecords(ctx, UsageFilter{Search: "SECOND"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].InputTokens == nil || *rows[0].InputTokens != 0 || rows[0].CostMicros == nil || *rows[0].CostMicros != 0 {
		t.Fatalf("zero-valued usage=%+v", rows)
	}
	for _, filter := range []UsageFilter{{APIKeyID: 1, AccountID: 11, Model: "a", Outcome: "succeeded"}, {Search: "upstream_error"}} {
		_, total, err := s.ListUsageRecords(ctx, filter)
		if err != nil || total != 1 {
			t.Fatalf("filter %+v: total=%d err=%v", filter, total, err)
		}
	}
}

func TestUsageEmptyStatisticsAndNullTPS(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u, err := s.UsageSummaryFor(ctx, UsageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if *u != (UsageSummary{}) {
		t.Fatalf("empty summary=%+v", u)
	}
	models, err := s.UsageByModel(ctx, UsageFilter{})
	if err != nil || len(models) != 0 {
		t.Fatalf("empty models=%v %v", models, err)
	}
	for i, r := range []UsageRecord{
		{OutputTokens: nil, LatencyMS: 100, FirstTokenMS: 1},
		{OutputTokens: usageInt(0), LatencyMS: 100, FirstTokenMS: 1},
		{OutputTokens: usageInt(1), LatencyMS: 100, FirstTokenMS: 100},
		{OutputTokens: usageInt(1), LatencyMS: 99, FirstTokenMS: 100},
	} {
		r.Outcome = "succeeded"
		r.StartedAt = int64(i + 1)
		finishTestUsage(t, s, r)
	}
	u, err = s.UsageSummaryFor(ctx, UsageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if u.OutputTPS != (UsagePercentiles{}) || u.CacheEligibleRequests != 0 {
		t.Fatalf("invalid sample counted: %+v", u)
	}
}

func TestUsageLifecycleReconcileAndRetention(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	old := UsageRecord{RequestID: "old", StartedAt: 100, Outcome: "succeeded", CompletedAt: 5}
	oldID, err := s.StartUsageRecord(ctx, &old)
	if err != nil {
		t.Fatal(err)
	}
	recent := UsageRecord{RequestID: "recent", StartedAt: 200}
	recentID, err := s.StartUsageRecord(ctx, &recent)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := s.ListUsageRecords(ctx, UsageFilter{Outcome: "running"})
	if err != nil || len(rows) != 2 {
		t.Fatalf("start rows=%+v %v", rows, err)
	}
	for _, r := range rows {
		if r.CompletedAt != 0 || r.InputTokens != nil {
			t.Fatalf("start wrote terminal values: %+v", r)
		}
	}
	n, err := s.ReconcileRunningUsageRecords(ctx, 200)
	if err != nil || n != 1 {
		t.Fatalf("reconcile=%d %v", n, err)
	}
	n, err = s.ReconcileRunningUsageRecords(ctx, 200)
	if err != nil || n != 0 {
		t.Fatalf("reconcile duplicate=%d %v", n, err)
	}
	rows, _, err = s.ListUsageRecords(ctx, UsageFilter{Outcome: "incomplete"})
	if err != nil || len(rows) != 1 || rows[0].ID != oldID || rows[0].CompletedAt <= 0 {
		t.Fatalf("orphan rows=%+v %v", rows, err)
	}
	if err := s.FinishUsageRecord(ctx, recentID, &UsageRecord{Outcome: "running"}); err == nil {
		t.Fatal("accepted nonterminal finish")
	}
	for i := 0; i < 3; i++ {
		finishTestUsage(t, s, UsageRecord{RequestID: "done", StartedAt: 10, CompletedAt: 50, Outcome: "succeeded"})
	}
	n, err = s.DeleteUsageRecordsBefore(ctx, 51, 1, 2)
	if err != nil || n != 2 {
		t.Fatalf("bounded delete=%d %v", n, err)
	}
	n, err = s.DeleteUsageRecordsBefore(ctx, 50, 10, 1)
	if err != nil || n != 0 {
		t.Fatalf("strict cutoff=%d %v", n, err)
	}
	n, err = s.DeleteUsageRecordsBefore(ctx, NowMS()+10000, 10, 1)
	if err != nil || n != 2 {
		t.Fatalf("delete completed=%d %v", n, err)
	}
	rows, total, err := s.ListUsageRecords(ctx, UsageFilter{})
	if err != nil || total != 1 || rows[0].ID != recentID || rows[0].Outcome != "running" {
		t.Fatalf("running row lost: %+v total=%d err=%v", rows, total, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.DeleteUsageRecordsBefore(cancelled, 1000, 1, 1); err == nil {
		t.Fatal("ignored cancellation")
	}
}
