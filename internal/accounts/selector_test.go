package accounts

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pi-gateway/internal/store"
)

func cand(id int64, weight, concurrency, inFlight int) Candidate {
	return Candidate{
		Account:  &store.Account{ID: id, Name: "acct", Weight: weight, Concurrency: concurrency},
		InFlight: inFlight,
	}
}

func ids(cs []Candidate) []int64 {
	out := make([]int64, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Account.ID)
	}
	return out
}

// The weight gate is the most surprising rule to port: only the highest-weight
// accounts are candidates at all, so a low-weight idle account never wins.
func TestWeightGateConsidersOnlyHighestWeight(t *testing.T) {
	now := time.Now()
	cs := []Candidate{cand(1, 1, 4, 0), cand(2, 3, 4, 3), cand(3, 2, 4, 0)}

	gate := selectAt(cs, StrategyRoundRobin, SmartWeights{}, nil, nil, now, 4)
	if got := ids(gate); len(got) != 1 || got[0] != 2 {
		t.Fatalf("weight gate = %v, want only the weight-3 account [2]", got)
	}

	open := selectAt(cs, StrategyRoundRobin, SmartWeights{PreferHigherWeight: true}, nil, nil, now, 4)
	if len(open) != 3 {
		t.Fatalf("prefer-higher-weight kept %v, want all three candidates", ids(open))
	}
}

func TestExcludedAndCoolingAccountsAreSkipped(t *testing.T) {
	now := time.Now()
	cs := []Candidate{cand(1, 1, 4, 0), cand(2, 1, 4, 0), cand(3, 1, 4, 0)}
	cs[2].CooldownUntil = now.Add(time.Minute).UnixMilli()

	got := selectAt(cs, StrategyRoundRobin, SmartWeights{PreferHigherWeight: true}, map[int64]bool{1: true}, nil, now, 4)
	if len(got) != 1 || got[0].Account.ID != 2 {
		t.Fatalf("candidates = %v, want only account 2 (1 excluded, 3 cooling)", ids(got))
	}

	// Once the cooldown expires the account is eligible again.
	cs[2].CooldownUntil = now.Add(-time.Second).UnixMilli()
	later := selectAt(cs, StrategyRoundRobin, SmartWeights{PreferHigherWeight: true}, nil, nil, now, 4)
	if len(later) != 3 {
		t.Fatalf("after cooldown = %v, want all three", ids(later))
	}
}

func TestSmartPrefersIdleAndHealthyAccount(t *testing.T) {
	now := time.Now()
	busy := cand(1, 1, 4, 4)
	busy.FailureRate = 0.5
	idle := cand(2, 1, 4, 0)

	got := selectAt([]Candidate{busy, idle}, StrategySmart, DefaultSmartWeights(), nil, nil, now, 4)
	if got[0].Account.ID != 2 {
		t.Fatalf("smart head = %d, want the idle/healthy account 2", got[0].Account.ID)
	}
}

// Within the tolerance band the head must rotate instead of pinning one account,
// which is what keeps a fleet of equal accounts from stampeding one of them.
func TestSmartRotatesWithinTolerance(t *testing.T) {
	now := time.Now()
	w := DefaultSmartWeights()
	w.ScoreTolerance = 10 // every candidate is within tolerance

	cs := []Candidate{cand(1, 1, 4, 0), cand(2, 1, 4, 0), cand(3, 1, 4, 0)}
	var cursor uint64
	seen := map[int64]bool{}
	for i := 0; i < 6; i++ {
		got := selectAt(cs, StrategySmart, w, nil, &cursor, now, 4)
		if len(got) != 3 {
			t.Fatalf("round %d: candidates = %v", i, ids(got))
		}
		seen[got[0].Account.ID] = true
	}
	if len(seen) < 3 {
		t.Fatalf("smart pinned the head to %v; want rotation across all three", seen)
	}
}

func TestQuotaResetPriorityOrdering(t *testing.T) {
	now := time.Now()
	noReset := cand(1, 1, 4, 0)
	sooner := cand(2, 1, 4, 0)
	sooner.QuotaResetAt = now.Add(time.Minute).UnixMilli()
	later := cand(3, 1, 4, 0)
	later.QuotaResetAt = now.Add(time.Hour).UnixMilli()
	later.InFlight = 1

	got := selectAt([]Candidate{noReset, later, sooner}, StrategyQuotaResetPriority, SmartWeights{}, nil, nil, now, 4)
	want := []int64{2, 3, 1} // soonest reset, then later reset, then no reset at all
	if len(got) != 3 {
		t.Fatalf("candidates = %v", ids(got))
	}
	for i, id := range want {
		if got[i].Account.ID != id {
			t.Fatalf("order = %v, want %v", ids(got), want)
		}
	}
}

func TestLeastInflightAndSingleStrategies(t *testing.T) {
	now := time.Now()
	cs := []Candidate{cand(1, 1, 8, 5), cand(2, 1, 8, 0), cand(3, 1, 8, 2)}

	least := selectAt(cs, StrategyLeastInflight, SmartWeights{}, nil, nil, now, 8)
	if got := ids(least); got[0] != 2 || got[1] != 3 || got[2] != 1 {
		t.Fatalf("least_inflight order = %v, want [2 3 1]", got)
	}

	single := selectAt(cs, StrategySingle, SmartWeights{}, nil, nil, now, 8)
	if len(single) != 1 {
		t.Fatalf("single kept %d candidates, want 1", len(single))
	}
}

func TestRoundRobinAdvancesCursor(t *testing.T) {
	now := time.Now()
	cs := []Candidate{cand(1, 1, 4, 0), cand(2, 1, 4, 0), cand(3, 1, 4, 0)}
	var cursor uint64
	heads := []int64{}
	for i := 0; i < 3; i++ {
		got := selectAt(cs, StrategyRoundRobin, SmartWeights{}, nil, &cursor, now, 4)
		heads = append(heads, got[0].Account.ID)
	}
	if heads[0] == heads[1] || heads[1] == heads[2] {
		t.Fatalf("round robin heads = %v, want three different accounts", heads)
	}
}

func newAccountsTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "accounts.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Cleanups run LIFO: close the store, then delete the SQLite side files, so
	// Windows can remove the TempDir afterwards.
	t.Cleanup(func() {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			_ = os.Remove(dbPath + suffix)
		}
	})
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// A 429 parks the account for at least the configured cooldown, and a later
// success clears the failure counter.
func TestRecordResultAppliesCooldownAndResetsFailures(t *testing.T) {
	st := newAccountsTestStore(t)
	ctx := context.Background()
	acct := &store.Account{Name: "a", AccountID: "acct-1", Enabled: true, Weight: 1, Concurrency: 1, Status: store.AccountStatusReady}
	if err := st.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	m := New(st, nil, nil, Options{})
	now := time.Now()

	// A Retry-After longer than the configured cooldown wins; a shorter one falls
	// back to the cooldown, so the account is never parked for less than the floor.
	if err := m.RecordResult(ctx, acct.ID, Result{Failed: true, StatusCode: 429, RetryAfter: 120 * time.Second}, now); err != nil {
		t.Fatal(err)
	}
	saved, err := st.GetAccount(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := now.Add(120 * time.Second).UnixMilli()
	if saved.CooldownUntil != want {
		t.Fatalf("cooldown_until = %d, want %d (retry-after wins over the default)", saved.CooldownUntil, want)
	}
	if saved.ConsecutiveFailures != 1 {
		t.Fatalf("consecutive_failures = %d, want 1", saved.ConsecutiveFailures)
	}
	if saved.EWMAFailureRateBP == 0 {
		t.Fatalf("ewma_failure_rate_bp = 0, want a failure sample")
	}

	// A shorter Retry-After must not shorten an existing cooldown: the update is
	// monotonic, so a second 429 cannot bring the account back early.
	if err := m.RecordResult(ctx, acct.ID, Result{Failed: true, StatusCode: 429, RetryAfter: time.Second}, now); err != nil {
		t.Fatal(err)
	}
	saved, _ = st.GetAccount(ctx, acct.ID)
	if saved.CooldownUntil != want {
		t.Fatalf("cooldown_until = %d, want it unchanged at %d", saved.CooldownUntil, want)
	}

	// A fresh account with no Retry-After gets the configured default floor.
	fresh := &store.Account{Name: "b", AccountID: "acct-2", Enabled: true, Weight: 1, Concurrency: 1, Status: store.AccountStatusReady}
	if err := st.CreateAccount(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if err := m.RecordResult(ctx, fresh.ID, Result{Failed: true, StatusCode: 429}, now); err != nil {
		t.Fatal(err)
	}
	fresh, _ = st.GetAccount(ctx, fresh.ID)
	def := now.Add(m.accountCooldown).UnixMilli()
	if fresh.CooldownUntil != def {
		t.Fatalf("cooldown_until = %d, want the default floor %d", fresh.CooldownUntil, def)
	}

	// Success clears the streak and records a latency sample.
	if err := m.RecordSuccess(ctx, saved, 250*time.Millisecond, now); err != nil {
		t.Fatal(err)
	}
	saved, _ = st.GetAccount(ctx, acct.ID)
	if saved.ConsecutiveFailures != 0 {
		t.Fatalf("consecutive_failures = %d, want 0 after success", saved.ConsecutiveFailures)
	}
	if saved.EWMAFirstOutputMS <= 0 {
		t.Fatalf("ewma_first_output_ms = %v, want the cold-start sample", saved.EWMAFirstOutputMS)
	}
}

func TestParseSmartWeightsFallsBackOnBadInput(t *testing.T) {
	if w, err := parseSmartWeights(""); err != nil || w != DefaultSmartWeights() {
		t.Fatalf("empty = %+v err=%v, want defaults", w, err)
	}
	if _, err := parseSmartWeights("not json"); err == nil {
		t.Fatal("expected an error for non-object input")
	}
	if _, err := parseSmartWeights(`{"load":-1}`); err == nil {
		t.Fatal("expected an error for a negative weight")
	}
	w, err := parseSmartWeights(`{"weights":[1,2,3,4,5,6]}`)
	if err != nil {
		t.Fatal(err)
	}
	if w.Load != 1 || w.Queue != 6 {
		t.Fatalf("array form parsed as %+v", w)
	}
	w, err = parseSmartWeights(`{"load":4,"quota":3}`)
	if err != nil {
		t.Fatal(err)
	}
	if w.Load != 4 || w.Quota != 3 || w.Health != DefaultSmartWeights().Health {
		t.Fatalf("partial object parsed as %+v", w)
	}
}
