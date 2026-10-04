package store

import (
	"context"
	"reflect"
	"testing"
)

func TestAccountGroupsAndKeyCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// Detect accidental nested queries that exhaust a single-connection pool.
	s.db.SetMaxOpenConns(1)
	a := &Account{Name: "a", Enabled: true}
	b := &Account{Name: "b", Enabled: true}
	disabled := &Account{Name: "disabled", Enabled: false}
	for _, account := range []*Account{a, b, disabled} {
		if err := s.CreateAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	g := &AccountGroup{Name: "first", Enabled: true, Notes: "notes", AccountIDs: []int64{a.ID, disabled.ID, a.ID}}
	if err := s.CreateAccountGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	other := &AccountGroup{Name: "second", Enabled: false, AccountIDs: []int64{b.ID}}
	if err := s.CreateAccountGroup(ctx, other); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAccountGroup(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "first" || got.Notes != "notes" || !reflect.DeepEqual(got.AccountIDs, []int64{a.ID, disabled.ID}) || got.CreatedAt == 0 || got.UpdatedAt == 0 {
		t.Fatalf("group=%+v", got)
	}
	k := &APIKey{Name: "key", Enabled: true, Label: "label", MaxConcurrency: 3, RequestsPerMinute: 7, DailyLimitUSD: 1.25, WeeklyLimitUSD: 8.5, Strategy: "smart", Transport: "ws", GroupIDs: []int64{g.ID, other.ID, g.ID}}
	if err := s.CreateKey(ctx, k); err != nil {
		t.Fatal(err)
	}
	checkKey := func(key *APIKey) {
		t.Helper()
		if key == nil || key.Label != k.Label || key.MaxConcurrency != k.MaxConcurrency || key.RequestsPerMinute != k.RequestsPerMinute || key.DailyLimitUSD != k.DailyLimitUSD || key.WeeklyLimitUSD != k.WeeklyLimitUSD || key.Strategy != k.Strategy || key.Transport != k.Transport || !reflect.DeepEqual(key.GroupIDs, uniqueIDs(k.GroupIDs)) {
			t.Fatalf("key=%+v want=%+v", key, k)
		}
	}
	key, err := s.GetKey(ctx, k.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkKey(key)
	key, err = s.GetKeyByValue(ctx, k.Key)
	if err != nil {
		t.Fatal(err)
	}
	checkKey(key)
	keys, err := s.ListKeys(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys=%+v %v", keys, err)
	}
	checkKey(keys[0])
	eligible, err := s.EligibleAccountsForKey(ctx, k)
	if err != nil || len(eligible) != 1 || eligible[0].ID != a.ID {
		t.Fatalf("eligible=%+v %v", eligible, err)
	}
	g.Enabled = false
	g.AccountIDs = nil
	if err := s.UpdateAccountGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	eligible, err = s.EligibleAccountsForKey(ctx, k)
	if err != nil || len(eligible) != 0 {
		t.Fatalf("disabled groups must not fall back to all: %+v %v", eligible, err)
	}
	got, err = s.GetAccountGroup(ctx, g.ID)
	if err != nil || len(got.AccountIDs) != 2 {
		t.Fatalf("disable deleted members: %+v %v", got, err)
	}
	g.Enabled = true
	if err := s.UpdateAccountGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountGroupAccounts(ctx, g.ID, []int64{b.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetAccountGroup(ctx, g.ID)
	if err != nil || !reflect.DeepEqual(got.AccountIDs, []int64{b.ID}) {
		t.Fatalf("replaced members=%+v %v", got, err)
	}
	if err := s.SetAccountGroupAccounts(ctx, g.ID, []int64{a.ID, 9999}); err == nil {
		t.Fatal("nonexistent account accepted")
	}
	got, err = s.GetAccountGroup(ctx, g.ID)
	if err != nil || !reflect.DeepEqual(got.AccountIDs, []int64{b.ID}) {
		t.Fatalf("failed replacement not rolled back: %+v %v", got, err)
	}
	if err := s.SetKeyGroups(ctx, k.ID, []int64{other.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKeyGroups(ctx, k.ID, []int64{g.ID, 9999}); err == nil {
		t.Fatal("nonexistent group accepted")
	}
	ids, err := s.ListKeyGroupIDs(ctx, k.ID)
	if err != nil || !reflect.DeepEqual(ids, []int64{other.ID}) {
		t.Fatalf("key bindings not rolled back: %v %v", ids, err)
	}
	k.GroupIDs = []int64{g.ID}
	k.Label = "edited"
	k.MaxConcurrency = 9
	k.RequestsPerMinute = 13
	k.DailyLimitUSD = 2.5
	k.WeeklyLimitUSD = 16
	if err := s.UpdateKey(ctx, k); err != nil {
		t.Fatal(err)
	}
	key, err = s.GetKey(ctx, k.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkKey(key)
	if err := s.MarkKeyUsed(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	key, err = s.GetKey(ctx, k.ID)
	if err != nil || key.RequestCount != 1 || key.LastUsedAt == 0 {
		t.Fatalf("key usage=%+v %v", key, err)
	}
	if err := s.SetKeyGroups(ctx, k.ID, nil); err != nil {
		t.Fatal(err)
	}
	eligible, err = s.EligibleAccountsForKey(ctx, k)
	if err != nil || len(eligible) != 2 {
		t.Fatalf("unbound key=%+v %v", eligible, err)
	}
	groups, err := s.ListAccountGroups(ctx)
	if err != nil || len(groups) != 2 {
		t.Fatalf("groups=%+v %v", groups, err)
	}
	if err := s.SetKeyGroups(ctx, k.ID, []int64{g.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccountGroup(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	ids, err = s.ListKeyGroupIDs(ctx, k.ID)
	if err != nil || len(ids) != 0 {
		t.Fatalf("dangling group binding=%v %v", ids, err)
	}
	got, err = s.GetAccountGroup(ctx, g.ID)
	if err != nil || got != nil {
		t.Fatalf("deleted group=%+v %v", got, err)
	}
	if err := s.SetKeyGroups(ctx, k.ID, []int64{other.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteKey(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	key, err = s.GetKey(ctx, k.ID)
	if err != nil || key != nil {
		t.Fatalf("deleted key=%+v %v", key, err)
	}
	key, err = s.GetKeyByValue(ctx, k.Key)
	if err != nil || key != nil {
		t.Fatalf("deleted key value=%+v %v", key, err)
	}
	ids, err = s.ListKeyGroupIDs(ctx, k.ID)
	if err != nil || len(ids) != 0 {
		t.Fatalf("dangling key binding=%v %v", ids, err)
	}
}

func TestAccountSchedulingStateReadback(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := &Account{Name: "healthy", Enabled: true}
	if err := s.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE accounts SET consecutive_failures=2,cooldown_until=123,cooldown_kind='rate_limit',last_started_at=100,ewma_first_output_ms=12.5,ewma_failure_rate_bp=2500 WHERE id=?`, a.ID); err != nil {
		t.Fatal(err)
	}
	// An administrative update from an older snapshot must not reset scheduling.
	if err := s.UpdateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAccount(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConsecutiveFailures != 2 || got.CooldownUntil != 123 || got.CooldownKind != "rate_limit" || got.LastStartedAt != 100 || got.EWMAFirstOutputMS != 12.5 || got.EWMAFailureRateBP != 2500 {
		t.Fatalf("scheduling state=%+v", got)
	}
}

func TestKeyAndGroupWritesAreAtomic(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	bad := &APIKey{Name: "bad", GroupIDs: []int64{999}}
	if err := s.CreateKey(ctx, bad); err == nil {
		t.Fatal("key with missing group created")
	}
	keys, err := s.ListKeys(ctx)
	if err != nil || len(keys) != 0 {
		t.Fatalf("partial key persisted: %+v %v", keys, err)
	}
	g := &AccountGroup{Name: "bad", AccountIDs: []int64{999}}
	if err := s.CreateAccountGroup(ctx, g); err == nil {
		t.Fatal("group with missing account created")
	}
	groups, err := s.ListAccountGroups(ctx)
	if err != nil || len(groups) != 0 {
		t.Fatalf("partial group persisted: %+v %v", groups, err)
	}
	k := &APIKey{Name: "original", Enabled: true}
	if err := s.CreateKey(ctx, k); err != nil {
		t.Fatal(err)
	}
	k.Name = "changed"
	k.GroupIDs = []int64{999}
	if err := s.UpdateKey(ctx, k); err == nil {
		t.Fatal("invalid binding update accepted")
	}
	got, err := s.GetKey(ctx, k.ID)
	if err != nil || got.Name != "original" {
		t.Fatalf("partial key update persisted: %+v %v", got, err)
	}
}
