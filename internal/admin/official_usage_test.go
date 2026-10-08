package admin

import (
	"encoding/base64"
	"testing"

	"pi-gateway/internal/store"
)

func TestOfficialUsagePeriodAndUserDeduplication(t *testing.T) {
	db := newAdminTestStore(t)
	create := func(name, user string) *store.Account {
		a := &store.Account{Name: name, AccountID: name, CodexAccountID: "team", CodexRefreshToken: "refresh", CodexAccessToken: "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"`+user+`"}`)) + ".sig"}
		if err := db.CreateAccount(t.Context(), a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	alice := create("alice", "user-a")
	create("alice-copy", "user-a")
	bob := create("bob", "user-b")
	credits := 25.0
	for _, a := range []*store.Account{alice, bob} {
		if err := db.SaveOfficialUsage(t.Context(), a, "2026-01-01", "2026-01-02", []store.OfficialUsageDay{{Day: "2026-01-01", Credits: &credits, Settled: true}, {Day: "2026-01-02", Credits: nil}}, ""); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{store: db}
	code, body := getJSON(t, s, s.handleOfficialUsage, "/api/usage/official?start_date=2026-01-01&end_date=2026-01-01")
	if code != 200 || body["total_credits"] != 50.0 || body["equivalent_usd"] != 2.0 || body["workspace_count"] != 2.0 || len(body["items"].([]any)) != 2 {
		t.Fatalf("deduplicated period: %d %+v", code, body)
	}
	code, body = getJSON(t, s, s.handleOfficialUsage, "/api/usage/official?start_date=2026-01-01&end_date=2026-01-02&account_id="+itoa(bob.ID))
	if code != 200 || body["total_credits"] != 25.0 || body["missing_credit_days"] != 1.0 {
		t.Fatalf("filtered partial period: %d %+v", code, body)
	}
	for _, query := range []string{"days=0", "start_date=bad&end_date=2026-01-01", "start_date=2026-01-02&end_date=2026-01-01", "start_date=2020-01-01&end_date=2026-01-01", "account_id=-1"} {
		if code, _ := getJSON(t, s, s.handleOfficialUsage, "/api/usage/official?"+query); code != 400 {
			t.Fatalf("invalid %s returned %d", query, code)
		}
	}
	_, body = getJSON(t, s, s.handleOfficialUsage, "/api/usage/official?start_date=2026-01-03&end_date=2026-01-03")
	if body["total_credits"] != nil || body["equivalent_usd"] != nil {
		t.Fatal("empty period fabricated consumption")
	}
}
