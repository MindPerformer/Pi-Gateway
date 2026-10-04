package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSettleKeyChargeIdempotentAndBudgetWindowsRoll(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	monday := time.Date(2025, 1, 6, 23, 59, 0, 0, time.UTC).UnixMilli()
	dailyStart := time.Date(2025, 1, 6, 0, 0, 0, 0, time.UTC).UnixMilli()
	settled, err := s.SettleKeyCharge(ctx, "request", 7, 123, monday)
	if err != nil || !settled {
		t.Fatalf("first charge=%v %v", settled, err)
	}
	settled, err = s.SettleKeyCharge(ctx, "request", 7, 999, monday)
	if err != nil || settled {
		t.Fatalf("duplicate charge=%v %v", settled, err)
	}
	settled, err = s.SettleKeyCharge(ctx, "request", 8, 999, monday)
	if err != nil || settled {
		t.Fatalf("request ID must be global: %v %v", settled, err)
	}
	windows, err := s.KeyBudgetStatus(ctx, 7, monday)
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 2 || windows[0] != (KeyBudgetWindow{"daily", dailyStart, 123}) || windows[1] != (KeyBudgetWindow{"weekly", dailyStart, 123}) {
		t.Fatalf("initial windows=%+v", windows)
	}
	tuesday := monday + 120000
	windows, err = s.KeyBudgetStatus(ctx, 7, tuesday)
	if err != nil {
		t.Fatal(err)
	}
	if windows[0] != (KeyBudgetWindow{"daily", dailyStart + 86400000, 0}) || windows[1] != (KeyBudgetWindow{"weekly", dailyStart, 123}) {
		t.Fatalf("rolled day=%+v", windows)
	}
	settled, err = s.SettleKeyCharge(ctx, "request", 7, 999, tuesday)
	if err != nil || settled {
		t.Fatalf("duplicate after rollover=%v %v", settled, err)
	}
	settled, err = s.SettleKeyCharge(ctx, "request-two", 7, 77, tuesday)
	if err != nil || !settled {
		t.Fatalf("new day charge=%v %v", settled, err)
	}
	windows, err = s.KeyBudgetStatus(ctx, 7, tuesday)
	if err != nil || windows[0].UsedMicros != 77 || windows[1].UsedMicros != 200 {
		t.Fatalf("second charge windows=%+v %v", windows, err)
	}
	nextMonday := dailyStart + 7*86400000
	windows, err = s.KeyBudgetStatus(ctx, 7, nextMonday)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range windows {
		if w.WindowStart != nextMonday || w.UsedMicros != 0 {
			t.Fatalf("rolled week=%+v", windows)
		}
	}
}

func TestResetKeyBudgetPreservesChargeIdempotency(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := NowMS()
	if ok, err := s.SettleKeyCharge(ctx, "one", 1, 10, now); err != nil || !ok {
		t.Fatalf("charge=%v %v", ok, err)
	}
	if err := s.ResetKeyBudget(ctx, 1, "daily", now); err != nil {
		t.Fatal(err)
	}
	ws, err := s.KeyBudgetStatus(ctx, 1, now)
	if err != nil || ws[0].UsedMicros != 0 || ws[1].UsedMicros != 10 {
		t.Fatalf("daily reset=%+v %v", ws, err)
	}
	if ok, err := s.SettleKeyCharge(ctx, "one", 1, 10, now); err != nil || ok {
		t.Fatalf("recharged after reset=%v %v", ok, err)
	}
	if err := s.ResetKeyBudget(ctx, 1, "weekly", now); err != nil {
		t.Fatal(err)
	}
	ws, err = s.KeyBudgetStatus(ctx, 1, now)
	if err != nil || ws[0].UsedMicros != 0 || ws[1].UsedMicros != 0 {
		t.Fatalf("weekly reset=%+v %v", ws, err)
	}
	if err := s.ResetKeyBudget(ctx, 1, "invalid", now); err == nil {
		t.Fatal("invalid period accepted")
	}
	if _, err := s.SettleKeyCharge(ctx, "negative", 1, -1, now); err == nil {
		t.Fatal("negative charge accepted")
	}
	if _, err := s.SettleKeyCharge(ctx, "", 1, 1, now); err == nil {
		t.Fatal("empty request ID accepted")
	}
}

func TestConcurrentKeyChargeSettlement(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := NowMS()
	const count = 12
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.SettleKeyCharge(ctx, fmt.Sprintf("request-%d", i/2), 1, 10, now)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	ws, err := s.KeyBudgetStatus(ctx, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		if w.UsedMicros != count/2*10 {
			t.Fatalf("lost/duplicate charge: %+v", ws)
		}
	}
}
