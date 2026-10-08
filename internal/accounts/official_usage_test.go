package accounts

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"pi-gateway/internal/egress"
	"pi-gateway/internal/quota"
	"pi-gateway/internal/store"
)

func TestOfficialUsageInitialBackfillRollingSyncAndFailure(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &store.Account{Name: "test", AccountID: "test", CodexAccountID: "workspace", CodexAccessToken: "test-access", CodexRefreshToken: "test-refresh", CodexExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	if err := db.CreateAccount(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		days := 84
		if call > 1 {
			days = 7
		}
		start := time.Now().UTC().AddDate(0, 0, -days+1).Format(time.DateOnly)
		if r.URL.Query().Get("start_date") != start {
			t.Errorf("start=%s, want %s", r.URL.Query().Get("start_date"), start)
		}
		if fail.Load() {
			w.WriteHeader(429)
			return
		}
		w.Write([]byte(`{"group_by":"day","data":[{"date":"` + time.Now().UTC().Format(time.DateOnly) + `","totals":{"credits":25,"text_total_tokens":100}}]}`))
	}))
	defer server.Close()
	factory := egress.NewFactory(egress.Options{})
	m := New(db, nil, factory, Options{})
	m.SetQuotaClient(quota.New(quota.Config{BaseURL: server.URL + "/backend-api", OfficialBaseURL: server.URL + "/backend-api"}, factory, nil))
	if err := m.RefreshOfficialUsage(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := m.RefreshOfficialUsage(t.Context(), a); err != nil || calls.Load() != 1 {
		t.Fatal("debounce failed", err)
	}
	key := store.OfficialUsageKey(a)
	if _, err := db.ExecContext(t.Context(), `UPDATE official_usage_sync SET attempted_at=0 WHERE workspace_id=?`, key); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	if err := m.RefreshOfficialUsage(t.Context(), a); err == nil {
		t.Fatal("lost sync error")
	}
	state, err := db.OfficialUsageSyncFor(t.Context(), key)
	if err != nil || state.SyncedAt == 0 || state.Error == "" {
		t.Fatalf("failed sync state %+v %v", state, err)
	}
	rows, err := db.ListOfficialUsage(t.Context(), key, "2020-01-01", "2099-01-01")
	if err != nil || len(rows) != 1 || *rows[0].Credits != 25 {
		t.Fatal("failed sync discarded history", err)
	}
}
