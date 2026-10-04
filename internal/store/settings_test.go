package store

import (
	"context"
	"reflect"
	"testing"
)

func TestRuntimePoolSettingsDefaultsAndRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	defaults := Settings{DefaultModel: "original", DefaultStrategy: "sticky"}
	got, err := s.LoadSettings(ctx, &defaults)
	if err != nil {
		t.Fatal(err)
	}
	if got.RotationStrategy != "smart" || got.RequestIntervalMS != 0 || got.MaxWaitingPerKey != 0 || got.MaxWaitingPerAccount != 0 || got.ConcurrencyWaitTimeoutSeconds != 30 || got.AccountCooldownSeconds != 60 || got.MaxAttempts != 2 || got.UsageRetentionDays != 31 || got.SmartSchedulingJSON != "{}" || got.PricingOverridesJSON != "{}" {
		t.Fatalf("defaults=%+v", got)
	}
	if defaults.RotationStrategy != "" || got.DefaultModel != "original" || got.DefaultStrategy != "sticky" {
		t.Fatalf("caller defaults mutated: %+v %+v", defaults, got)
	}
	want := *got
	want.RotationStrategy = "round_robin"
	want.RequestIntervalMS = 125
	want.MaxWaitingPerKey = 5
	want.MaxWaitingPerAccount = 9
	want.ConcurrencyWaitTimeoutSeconds = 11
	want.AccountCooldownSeconds = 22
	want.MaxAttempts = 3
	want.UsageRetentionDays = 42
	want.SmartSchedulingJSON = `{"weights":[10,8,10,5,0,0]}`
	want.PricingOverridesJSON = `{"test":{"input":1}}`
	if err := s.SaveSettings(ctx, &want); err != nil {
		t.Fatal(err)
	}
	got, err = s.LoadSettings(ctx, &defaults)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("roundtrip got=%+v want=%+v", got, want)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM settings WHERE key IN (
 'settings.rotation_strategy','settings.request_interval_ms','settings.max_waiting_per_key','settings.max_waiting_per_account',
 'settings.concurrency_wait_timeout_seconds','settings.account_cooldown_seconds','settings.max_attempts','settings.usage_retention_days',
 'settings.smart_scheduling_json','settings.pricing_overrides_json')`).Scan(&n); err != nil || n != 10 {
		t.Fatalf("persisted runtime keys=%d %v", n, err)
	}
	want.ConcurrencyWaitTimeoutSeconds = 0
	want.AccountCooldownSeconds = 0
	want.MaxAttempts = 0
	want.UsageRetentionDays = 0
	if err := s.SaveSettings(ctx, &want); err != nil {
		t.Fatal(err)
	}
	got, err = s.LoadSettings(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConcurrencyWaitTimeoutSeconds != 0 || got.AccountCooldownSeconds != 0 || got.MaxAttempts != 0 || got.UsageRetentionDays != 0 {
		t.Fatalf("persisted zero overridden by defaults: %+v", got)
	}
}
