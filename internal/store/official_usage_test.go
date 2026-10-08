package store

import (
	"encoding/base64"
	"errors"
	"testing"
)

func officialTestAccount(t *testing.T, s *Store, subject string) *Account {
	t.Helper()
	a := &Account{Name: subject, AccountID: subject, CodexAccountID: "team", CodexRefreshToken: "refresh-" + subject, CodexAccessToken: "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"`+subject+`"}`)) + ".sig"}
	if err := s.CreateAccount(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestOfficialUsageCorrectionFailureAndCredentialScope(t *testing.T) {
	s := openTestStore(t)
	a := officialTestAccount(t, s, "alice")
	b := officialTestAccount(t, s, "bob")
	alias := *a
	alias.ID = 999
	if OfficialUsageKey(a) != OfficialUsageKey(&alias) || OfficialUsageKey(a) == OfficialUsageKey(b) {
		t.Fatal("workspace/user deduplication broken")
	}
	rows := []OfficialUsageDay{{Day: "2026-01-01", Credits: floatPtr(25), TotalTokens: usageInt(100), Settled: true}, {Day: "2026-01-02", Credits: nil}}
	if err := s.SaveOfficialUsage(t.Context(), a, "2026-01-01", "2026-01-02", rows, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOfficialUsage(t.Context(), b, "2026-01-01", "2026-01-02", []OfficialUsageDay{{Day: "2026-01-01", Credits: floatPtr(50)}}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOfficialUsage(t.Context(), a, "2026-01-02", "2026-01-02", []OfficialUsageDay{{Day: "2026-01-02", Credits: floatPtr(5)}}, ""); err != nil {
		t.Fatal(err)
	}
	state, _ := s.OfficialUsageSyncFor(t.Context(), OfficialUsageKey(a))
	if err := s.SaveOfficialUsage(t.Context(), a, "2026-01-01", "2026-01-02", nil, "HTTP 429"); err != nil {
		t.Fatal(err)
	}
	after, _ := s.OfficialUsageSyncFor(t.Context(), OfficialUsageKey(a))
	if after.SyncedAt != state.SyncedAt || after.Error != "HTTP 429" {
		t.Fatalf("failed sync lost snapshot: %+v", after)
	}
	got, err := s.ListOfficialUsage(t.Context(), OfficialUsageKey(a), "2026-01-01", "2026-01-02")
	if err != nil || len(got) != 2 || *got[0].Credits != 5 || *got[1].Credits != 25 {
		t.Fatalf("correction/history: %+v %v", got, err)
	}
	if _, err := s.ExecContext(t.Context(), `UPDATE accounts SET codex_refresh_token='' WHERE id=?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOfficialUsage(t.Context(), a, "2026-01-01", "2026-01-02", nil, ""); !errors.Is(err, ErrCodexCredentialChanged) {
		t.Fatalf("late unlinked write: %v", err)
	}
}
