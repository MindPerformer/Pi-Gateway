package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"pi-gateway/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestAccountManagementPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("PI_GATEWAY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("PI_GATEWAY_TEST_POSTGRES_DSN is not set")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL test configuration")
	}
	admin := stdlib.OpenDB(*cfg)
	defer admin.Close()
	schema := "management_test_" + strings.TrimPrefix(store.GenerateKey(), "sk-pi-")
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	cfg.RuntimeParams["search_path"] = schema
	registered := stdlib.RegisterConnConfig(cfg)
	defer stdlib.UnregisterConnConfig(registered)
	st, err := store.OpenOptions(store.Options{Driver: "postgres", DSN: registered})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// Simulate a version 5 installation; the next open must add durable sessions.
	if _, err := st.ExecContext(t.Context(), `DROP TABLE admin_sessions`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ExecContext(t.Context(), `UPDATE schema_migrations SET version=5`); err != nil {
		t.Fatal(err)
	}
	second, err := store.OpenOptions(store.Options{Driver: "postgres", DSN: registered})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	server := managementServer(st)
	if err := server.BootstrapPassword(t.Context()); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	server.Routes(mux, nil)
	w := managementHTTP(t, mux, "POST", "/api/auth/login", "", `{"username":"admin","password":"test-password"}`, 200)
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	restarted := managementServer(second)
	if err := restarted.BootstrapPassword(t.Context()); err != nil {
		t.Fatal(err)
	}
	mux = http.NewServeMux()
	restarted.Routes(mux, nil)
	managementHTTP(t, mux, "GET", "/api/auth/me", login.Token, "", 200)
	input := accountBundle{Type: "pi-gateway-accounts", Version: 1, Accounts: []portableAccount{{Email: "pg@example.test", ChatGPT: &portableCredential{AccessToken: "main", RefreshToken: "refresh", ClientID: "issued-client"}, Codex: &portableCredential{AccessToken: "codex", RefreshToken: "codex-refresh", AccountID: "pg-codex"}}}}
	results := importDocument(t, restarted, "auto", input, 0)
	if len(results) != 1 || results[0].Status != "created" {
		t.Fatalf("import=%+v", results)
	}
	id := results[0].ID
	group := &store.AccountGroup{Name: "pg-extra", Enabled: true}
	if err := second.CreateAccountGroup(t.Context(), group); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		body := fmt.Sprintf(`{"ids":[%d],"action":"add_groups","group_ids":[%d]}`, id, group.ID)
		w = managementHTTP(t, mux, "POST", "/api/accounts/batch", login.Token, body, 200)
		if !strings.Contains(w.Body.String(), `"success"`) {
			t.Fatalf("batch=%s", w.Body.String())
		}
	}
	a, err := st.GetAccount(t.Context(), id)
	if err != nil || len(a.GroupIDs) != 1 || a.CodexRefreshToken != "codex-refresh" || a.AccessToken != "main" {
		t.Fatal("PostgreSQL account transfer or batch lost state")
	}
	w = managementHTTP(t, mux, "POST", "/api/accounts/export", login.Token, fmt.Sprintf(`{"ids":[%d]}`, id), 200)
	var backup accountBundle
	if err := json.Unmarshal(w.Body.Bytes(), &backup); err != nil || len(backup.Accounts) != 1 || backup.Accounts[0].Codex.AccountID != "pg-codex" {
		t.Fatal("PostgreSQL backup lost fields")
	}
	if strings.Contains(w.Body.String(), `"groups"`) || strings.Contains(w.Body.String(), `"enabled"`) || strings.Contains(w.Body.String(), `"name"`) {
		t.Fatal("PostgreSQL export contains configuration")
	}
	managementHTTP(t, mux, "POST", "/api/auth/logout", login.Token, "", 200)
	managementHTTP(t, mux, "GET", "/api/auth/me", login.Token, "", 401)
}
