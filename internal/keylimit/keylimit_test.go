package keylimit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pi-gateway/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// Cleanups run LIFO: the store closes first, then the SQLite side files are
	// removed. Windows refuses to delete the TempDir while -wal/-shm remain.
	t.Cleanup(func() {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			_ = os.Remove(dbPath + suffix)
		}
	})
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestLimitsFromKeyConvertsUSDToMicros(t *testing.T) {
	lim := LimitsFromKey(&store.APIKey{
		MaxConcurrency:    2,
		RequestsPerMinute: 30,
		DailyLimitUSD:     1.5,
		WeeklyLimitUSD:    10,
	})
	if lim.MaxConcurrency != 2 || lim.RequestsPerMinute != 30 {
		t.Fatalf("limits = %+v", lim)
	}
	if lim.DailyLimitMicros != 1_500_000 {
		t.Fatalf("daily micros = %d, want 1500000", lim.DailyLimitMicros)
	}
	if lim.WeeklyLimitMicros != 10_000_000 {
		t.Fatalf("weekly micros = %d, want 10000000", lim.WeeklyLimitMicros)
	}
	if got := LimitsFromKey(nil); got != (Limits{}) {
		t.Fatalf("nil key limits = %+v, want zero", got)
	}
}

func TestZeroLimitsMeanUnlimited(t *testing.T) {
	l := NewLimiter(nil)
	now := time.Now()
	var releases []func()
	for i := 0; i < 50; i++ {
		dec, release, err := l.Admit(context.Background(), 1, Limits{}, now)
		if err != nil {
			t.Fatal(err)
		}
		if !dec.Allowed {
			t.Fatalf("admit %d denied: %+v", i, dec)
		}
		releases = append(releases, release)
	}
	if got := l.Active(1); got != 50 {
		t.Fatalf("active = %d, want 50", got)
	}
	for _, r := range releases {
		r()
	}
	if got := l.Active(1); got != 0 {
		t.Fatalf("active after release = %d, want 0", got)
	}
}

func TestConcurrencyLimitRejectsAndRecovers(t *testing.T) {
	l := NewLimiter(nil)
	now := time.Now()
	lim := Limits{MaxConcurrency: 1}

	dec, release, err := l.Admit(context.Background(), 7, lim, now)
	if err != nil || !dec.Allowed {
		t.Fatalf("first admit: %+v err=%v", dec, err)
	}

	dec, release2, err := l.Admit(context.Background(), 7, lim, now)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Allowed || dec.Reason != "concurrency" {
		t.Fatalf("second admit = %+v, want concurrency denial", dec)
	}
	if dec.RetryAfter <= 0 {
		t.Fatalf("retry after = %v, want a positive hint", dec.RetryAfter)
	}
	// A denied request must not leave a slot behind.
	if got := l.Active(7); got != 1 {
		t.Fatalf("active = %d, want 1", got)
	}
	release2() // safe no-op on a denial

	release()
	dec, release3, err := l.Admit(context.Background(), 7, lim, now)
	if err != nil || !dec.Allowed {
		t.Fatalf("admit after release: %+v err=%v", dec, err)
	}
	release3()
}

func TestReleaseIsIdempotent(t *testing.T) {
	l := NewLimiter(nil)
	dec, release, err := l.Admit(context.Background(), 3, Limits{MaxConcurrency: 2}, time.Now())
	if err != nil || !dec.Allowed {
		t.Fatalf("admit: %+v err=%v", dec, err)
	}
	release()
	release()
	release()
	if got := l.Active(3); got != 0 {
		t.Fatalf("active = %d, want 0 (never negative)", got)
	}
}

func TestPerKeyIsolation(t *testing.T) {
	l := NewLimiter(nil)
	now := time.Now()
	if dec, _, _ := l.Admit(context.Background(), 1, Limits{MaxConcurrency: 1}, now); !dec.Allowed {
		t.Fatal("key 1 should be admitted")
	}
	if dec, _, _ := l.Admit(context.Background(), 2, Limits{MaxConcurrency: 1}, now); !dec.Allowed {
		t.Fatal("key 2 has its own slot and should be admitted")
	}
}

func TestRequestsPerMinuteWindow(t *testing.T) {
	l := NewLimiter(nil)
	now := time.Now()
	lim := Limits{RequestsPerMinute: 2}

	for i := 0; i < 2; i++ {
		dec, release, err := l.Admit(context.Background(), 9, lim, now)
		if err != nil || !dec.Allowed {
			t.Fatalf("admit %d: %+v err=%v", i, dec, err)
		}
		release()
	}

	dec, release, err := l.Admit(context.Background(), 9, lim, now)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Allowed || dec.Reason != "rpm" {
		t.Fatalf("third admit = %+v, want rpm denial", dec)
	}
	if dec.RetryAfter <= 0 || dec.RetryAfter > time.Minute {
		t.Fatalf("retry after = %v, want (0,1m]", dec.RetryAfter)
	}
	release()

	// Once the window slides past the first two requests the key is admitted again.
	later := now.Add(time.Minute + time.Second)
	dec, release, err = l.Admit(context.Background(), 9, lim, later)
	if err != nil || !dec.Allowed {
		t.Fatalf("admit after window: %+v err=%v", dec, err)
	}
	release()
}

func TestBudgetLimitRejectsAndRollsBackSlot(t *testing.T) {
	st := newTestStore(t)
	l := NewLimiter(st)
	ctx := context.Background()
	now := time.Now()

	// Consume the whole daily budget through an idempotent settle.
	if _, err := st.SettleKeyCharge(ctx, "req-1", 5, 1_000_000, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}

	lim := Limits{MaxConcurrency: 1, RequestsPerMinute: 1, DailyLimitMicros: 1_000_000}
	dec, release, err := l.Admit(ctx, 5, lim, now)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Allowed || dec.Reason != "budget" {
		t.Fatalf("admit = %+v, want budget denial", dec)
	}
	if dec.RetryAfter <= 0 {
		t.Fatalf("retry after = %v, want time until the next window", dec.RetryAfter)
	}
	release()

	// The denied attempt must not hold a slot or an RPM slot.
	if got := l.Active(5); got != 0 {
		t.Fatalf("active = %d, want 0 after budget denial", got)
	}
	dec, release, err = l.Admit(ctx, 5, Limits{MaxConcurrency: 1, RequestsPerMinute: 1}, now)
	if err != nil || !dec.Allowed {
		t.Fatalf("admit without budget: %+v err=%v", dec, err)
	}
	release()
}

func TestBudgetAllowsBelowLimit(t *testing.T) {
	st := newTestStore(t)
	l := NewLimiter(st)
	ctx := context.Background()
	now := time.Now()

	if _, err := st.SettleKeyCharge(ctx, "req-low", 6, 999_999, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	dec, release, err := l.Admit(ctx, 6, Limits{DailyLimitMicros: 1_000_000}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Allowed {
		t.Fatalf("admit = %+v, want allowed below the limit", dec)
	}
	release()
}
