package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"pi-gateway/internal/egress"
	"pi-gateway/internal/store"
)

func TestDailyUsageAccountQueryAndNullableCounts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/analytics/daily-workspace-usage-counts" || r.URL.Query().Get("start_date") != "2026-01-01" || r.URL.Query().Get("end_date") != "2026-01-02" || r.URL.Query().Get("workspace_user") != "true" || r.URL.Query().Get("group_by") != "day" {
			t.Errorf("URL=%s", r.URL)
		}
		if r.Header.Get("chatgpt-account-id") != "codex-account" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("wrong credential scope")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"group_by":"day","data":[{"date":"2026-01-01","totals":{"credits":25,"text_total_tokens":100,"text_output_tokens":20}},{"date":"2026-01-02","totals":{"credits":0}}]}`))
	}))
	defer server.Close()
	c := New(Config{BaseURL: server.URL + "/backend-api", OfficialBaseURL: server.URL + "/backend-api"}, egress.NewFactory(egress.Options{}), nil)
	rows, err := c.FetchDailyUsage(context.Background(), &store.Account{AccountID: "model-account", CodexAccountID: "codex-account"}, "test-token", "2026-01-01", "2026-01-02")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Credits == nil || *rows[0].Credits != 25 || !rows[0].Settled || rows[1].Credits == nil || *rows[1].Credits != 0 || rows[1].TotalTokens != nil || rows[1].Settled {
		t.Fatalf("rows=%+v", rows)
	}
}

func TestDailyUsageRejectsMalformedAndErrorResponses(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":null}`, `{"data":[{"date":"bad"}]}`, `{"data":[{"date":"2026-01-01","totals":{"credits":-1}}]}`, `{"data":[{"date":"2026-01-01","totals":{"text_output_tokens":-1}}]}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer server.Close()
			c := New(Config{BaseURL: server.URL, OfficialBaseURL: server.URL}, egress.NewFactory(egress.Options{}), nil)
			if _, err := c.FetchDailyUsage(t.Context(), &store.Account{CodexAccountID: "test"}, "test", "2026-01-01", "2026-01-02"); err == nil {
				t.Fatal("accepted invalid data")
			}
		})
	}
}
