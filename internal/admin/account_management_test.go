package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"pi-gateway/internal/accounts"
	"pi-gateway/internal/config"
	"pi-gateway/internal/oauth"
	"pi-gateway/internal/store"
)

func managementServer(st *store.Store) *Server {
	return New(Options{Store: st, Config: &config.Config{Admin: config.AdminConfig{Username: "admin", Password: "test-password", SessionTTLMinute: 60}}, Accounts: accounts.New(st, nil, nil, accounts.Options{})})
}

func managementHTTP(t *testing.T, handler http.Handler, method, path, token, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, w.Code, want, w.Body.String())
	}
	return w
}

func TestAdminSessionSurvivesRestartAndRevocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := managementServer(st)
	if err := s.BootstrapPassword(t.Context()); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Routes(mux, nil)
	login := func(handler http.Handler) string {
		w := managementHTTP(t, handler, "POST", "/api/auth/login", "", `{"username":"admin","password":"test-password"}`, 200)
		var result struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Token
	}
	token := login(mux)
	var stored string
	if err := st.QueryRowContext(t.Context(), `SELECT token_hash FROM admin_sessions`).Scan(&stored); err != nil || stored == token || len(stored) != 64 {
		t.Fatalf("raw token persisted or missing digest: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s = managementServer(st)
	if err := s.BootstrapPassword(t.Context()); err != nil {
		t.Fatal(err)
	}
	mux = http.NewServeMux()
	s.Routes(mux, nil)
	managementHTTP(t, mux, "GET", "/api/auth/me", token, "", 200)
	s.cfg.Admin.Username = "renamed-admin"
	managementHTTP(t, mux, "GET", "/api/auth/me", token, "", 401)
	s.cfg.Admin.Username = "admin"
	// Query-token downloads have the same revocation behavior as bearer clients.
	managementHTTP(t, mux, "POST", "/api/auth/logout?token="+token, "", "", 200)
	managementHTTP(t, mux, "GET", "/api/auth/me", token, "", 401)
	token = login(mux)
	second := login(mux)
	managementHTTP(t, mux, "POST", "/api/settings/password", token, `{"current_password":"test-password","new_password":"replacement-password"}`, 200)
	managementHTTP(t, mux, "GET", "/api/auth/me", token, "", 401)
	managementHTTP(t, mux, "GET", "/api/auth/me", second, "", 401)
	currentHash, err := st.GetSecret(t.Context(), adminPasswordHashKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAdminSession(t.Context(), "expired", "admin", currentHash, time.Now().Add(-time.Second).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	managementHTTP(t, mux, "GET", "/api/auth/me", "expired", "", 401)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	managementHTTP(t, mux, "GET", "/api/auth/me", "nonempty", "", 503)
}

func operationResults(t *testing.T, raw json.RawMessage) []accountOperationResult {
	t.Helper()
	var results []accountOperationResult
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	return results
}

func TestAccountBatchPreservesCredentialsAndMemberships(t *testing.T) {
	st := newAdminTestStore(t)
	s := managementServer(st)
	ctx := t.Context()
	a := &store.Account{Name: "one", Email: "one@example.test", AccessToken: "main-private", RefreshToken: "refresh-private", Enabled: true, Status: store.AccountStatusBanned, DisabledModels: []string{"blocked"}}
	if err := st.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	first := &store.AccountGroup{Name: "first", Enabled: true, AccountIDs: []int64{a.ID}}
	second := &store.AccountGroup{Name: "second", Enabled: true}
	for _, g := range []*store.AccountGroup{first, second} {
		if err := st.CreateAccountGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.ExecContext(ctx, `UPDATE accounts SET request_count=11,error_count=3,cooldown_until=9999999999999,consecutive_failures=5 WHERE id=?`, a.ID); err != nil {
		t.Fatal(err)
	}
	request := func(action string, extra string) []accountOperationResult {
		out := policyRequest(t, s.handleBatchAccounts, "POST", 0, fmt.Sprintf(`{"ids":[%d,%d,999999],"action":%q%s}`, a.ID, a.ID, action, extra), 200)
		if strings.Contains(string(out["results"]), "private") {
			t.Fatal("credential leaked")
		}
		results := operationResults(t, out["results"])
		if len(results) != 2 || results[0].Status != "success" || results[1].Status != "failed" {
			t.Fatalf("results=%+v", results)
		}
		return results
	}
	request("add_groups", fmt.Sprintf(`,"group_ids":[%d]`, second.ID))
	request("add_groups", fmt.Sprintf(`,"group_ids":[%d]`, second.ID))
	request("disable", "")
	request("recover", "")
	fresh, err := st.GetAccount(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Enabled || fresh.AccessToken != a.AccessToken || fresh.RefreshToken != a.RefreshToken || fresh.RequestCount != 11 || fresh.ErrorCount != 3 || fresh.Status != store.AccountStatusReady || fresh.CooldownUntil != 0 || fresh.ConsecutiveFailures != 0 || !reflect.DeepEqual(fresh.GroupIDs, []int64{first.ID, second.ID}) || !reflect.DeepEqual(fresh.DisabledModels, a.DisabledModels) {
		t.Fatal("batch changed unrelated state")
	}
	request("enable", "")
	policyRequest(t, s.handleBatchAccounts, "POST", 0, fmt.Sprintf(`{"ids":[%d],"action":"add_groups","group_ids":[999999]}`, a.ID), 400)
}

func importDocument(t *testing.T, s *Server, format string, data any, target int64) []accountOperationResult {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"format": format, "data": data, "target_account_id": target})
	if err != nil {
		t.Fatal(err)
	}
	out := policyRequest(t, s.handleImportAccounts, "POST", 0, string(payload), 200)
	return operationResults(t, out["results"])
}

func TestAccountCredentialRoundTripAndDuplicatePreservation(t *testing.T) {
	first := newAdminTestStore(t)
	s := managementServer(first)
	a := &store.Account{Name: "backup", Email: "backup@example.test", AccountID: "chatgpt:backup@example.test", PlanType: "plus", AccessToken: "main-private", RefreshToken: "main-refresh", IDToken: "main-id", OAuthClientID: "issued-client", ExpiresAt: 1791360000000, Enabled: false, Weight: 7, Concurrency: 5, ProxyURL: "http://user:password@127.0.0.1:8080", UpstreamProtocol: "ws", CodexAccessToken: "codex-private", CodexRefreshToken: "codex-refresh", CodexIDToken: "codex-id", CodexAccountID: "codex-identity", CodexExpiresAt: 1791360000000, DisabledModels: []string{"blocked"}, SupplementalModels: []string{"manual"}}
	if err := first.CreateAccount(t.Context(), a, 123); err != nil {
		t.Fatal(err)
	}
	g := &store.AccountGroup{Name: "backup-group", Enabled: false, Notes: "backup notes", AccountIDs: []int64{a.ID}, DisabledModels: []string{"group-blocked"}, SwitchOn429: "disabled"}
	if err := first.CreateAccountGroup(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/accounts/export", strings.NewReader(fmt.Sprintf(`{"ids":[%d]}`, a.ID)))
	w := httptest.NewRecorder()
	s.handleExportAccounts(w, r)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("export status=%d", w.Code)
	}
	var bundle accountBundle
	if err := json.Unmarshal(w.Body.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Accounts) != 1 || bundle.Accounts[0].ChatGPT.RefreshToken != a.RefreshToken || bundle.Accounts[0].Codex.RefreshToken != a.CodexRefreshToken {
		t.Fatal("export lost credentials")
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if len(document) != 4 {
		t.Fatal("export contains unexpected top-level fields")
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(document["accounts"], &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries[0]) != 3 || entries[0]["email"] == nil || entries[0]["chatgpt"] == nil || entries[0]["codex"] == nil {
		t.Fatal("export must contain only email and credentials")
	}
	// Even legacy or foreign configuration with invalid field types is ignored.
	legacy := map[string]any{}
	if err := json.Unmarshal(w.Body.Bytes(), &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["groups"] = "ignored malformed configuration"
	entry := legacy["accounts"].([]any)[0].(map[string]any)
	for _, key := range []string{"name", "account_id", "plan_type", "enabled", "weight", "concurrency", "proxy_url", "upstream_protocol", "cooldown_429_seconds", "groups", "disabled_models", "supplemental_models"} {
		entry[key] = map[string]any{"ignored": true}
	}
	second := newAdminTestStore(t)
	s = managementServer(second)
	result := importDocument(t, s, "auto", legacy, 0)
	if len(result) != 1 || result[0].Status != "created" {
		t.Fatalf("results=%+v", result)
	}
	restored, err := second.GetAccount(t.Context(), result[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.AccessToken != a.AccessToken || restored.RefreshToken != a.RefreshToken || restored.CodexAccessToken != a.CodexAccessToken || restored.CodexRefreshToken != a.CodexRefreshToken || restored.CodexAccountID != a.CodexAccountID || !restored.Enabled || restored.Name != a.Email || restored.PlanType != "" || restored.ProxyURL != "" || restored.Weight != 1 || restored.Concurrency != 3 || restored.UpstreamProtocol != "" || restored.Cooldown429Seconds != -1 || len(restored.GroupIDs) != 0 || len(restored.DisabledModels) != 0 || len(restored.SupplementalModels) != 0 {
		t.Fatal("credential round trip changed credentials or imported configuration")
	}
	groups, err := second.ListAccountGroups(t.Context())
	if err != nil || len(groups) != 0 {
		t.Fatal("credential import created groups")
	}
	if err := second.UpdateAccountCredentials(t.Context(), restored.ID, "rotated", "new-refresh", 1791360000000, store.AccountStatusReady, ""); err != nil {
		t.Fatal(err)
	}
	result = importDocument(t, s, "auto", bundle, 0)
	if result[0].Status != "skipped" {
		t.Fatalf("duplicate=%+v", result)
	}
	restored, _ = second.GetAccount(t.Context(), restored.ID)
	if restored.AccessToken != "rotated" {
		t.Fatal("duplicate backup overwrote a live credential")
	}
	list := policyRequest(t, s.handleListAccounts, "GET", 0, "", 200)
	if strings.Contains(string(list["accounts"]), "codex-private") || strings.Contains(string(list["accounts"]), "new-refresh") {
		t.Fatal("normal list exposes credentials")
	}
}

func TestExternalCodexImportsAndChatGPTAttachment(t *testing.T) {
	st := newAdminTestStore(t)
	s := managementServer(st)
	cli := map[string]any{"type": "codex", "name": map[string]any{"ignored": true}, "plan_type": map[string]any{"ignored": true}, "weight": 99, "proxy_url": "http://ignored:secret@127.0.0.1:9999", "groups": []string{"ignored"}, "email": "external@example.test", "account_id": "external-id", "access_token": "codex-access", "refresh_token": "codex-refresh", "expired": "2026-10-08T08:00:00Z"}
	results := importDocument(t, s, "auto", []any{cli}, 0)
	if results[0].Status != "created" {
		t.Fatalf("results=%+v", results)
	}
	a, err := st.GetAccount(t.Context(), results[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != a.Email || a.PlanType != "" || a.Weight != 1 || a.Concurrency != 3 || a.ProxyURL != "" || len(a.GroupIDs) != 0 || a.Enabled || a.AccessToken != "" || !a.CodexLinked() || a.CodexExpiresAt != time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatal("external credential routed to wrong slot or expiry")
	}
	out := policyRequest(t, s.handleBatchAccounts, "POST", 0, fmt.Sprintf(`{"ids":[%d],"action":"enable"}`, a.ID), 200)
	if operationResults(t, out["results"])[0].Status != "failed" {
		t.Fatal("credential-only account enabled")
	}
	policyRequest(t, s.handleUpdateAccount, "PATCH", a.ID, `{"enabled":true}`, 400)
	tok := &oauth.Token{Access: "chatgpt-access", Refresh: "chatgpt-refresh", ClientID: "issued-client", Email: a.Email, ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.accounts.LinkChatGPTCredential(t.Context(), a, tok); err != nil {
		t.Fatal(err)
	}
	a, _ = st.GetAccount(t.Context(), a.ID)
	if !a.Enabled || a.CodexRefreshToken != "codex-refresh" || a.AccessToken != "chatgpt-access" {
		t.Fatal("ChatGPT attachment lost Codex or failed to activate")
	}
	if _, err := st.ExecContext(t.Context(), `UPDATE accounts SET request_count=8,status=? WHERE id=?`, store.AccountStatusBanned, a.ID); err != nil {
		t.Fatal(err)
	}
	sub := map[string]any{"type": "sub2api-data", "version": 1, "accounts": []any{
		map[string]any{"name": "same", "platform": "openai", "type": "oauth", "credentials": map[string]any{"access_token": "new-codex", "refresh_token": "new-codex-refresh", "chatgpt_account_id": "external-id", "email": a.Email, "expires_at": "1791446400"}},
		map[string]any{"name": "wrong", "platform": "anthropic", "type": "oauth", "credentials": map[string]any{"refresh_token": "private-invalid"}},
	}}
	results = importDocument(t, s, "auto", map[string]any{"data": sub}, 0)
	if results[0].Status != "linked" || results[1].Status != "failed" {
		t.Fatalf("sub=%+v", results)
	}
	a, _ = st.GetAccount(t.Context(), a.ID)
	if a.AccessToken != "chatgpt-access" || a.RefreshToken != "chatgpt-refresh" || a.CodexRefreshToken != "new-codex-refresh" || a.CodexExpiresAt != 1791446400000 || a.RequestCount != 8 || a.Status != store.AccountStatusBanned {
		t.Fatal("Codex import changed primary credential or state")
	}
	cli["account_id"] = "wrong-identity"
	results = importDocument(t, s, "cliproxyapi", cli, a.ID)
	if results[0].Status != "failed" {
		t.Fatal("identity mismatch accepted")
	}
	cli["account_id"] = "external-id"
	cli["email"] = "other@example.test"
	results = importDocument(t, s, "cliproxyapi", cli, a.ID)
	if results[0].Status != "failed" {
		t.Fatal("email mismatch accepted")
	}
	if err := s.accounts.LinkChatGPTCredential(context.Background(), a, &oauth.Token{Access: "x", Refresh: "y", ClientID: "issued", Email: "other@example.test"}); err == nil {
		t.Fatal("ChatGPT identity mismatch accepted")
	}
}

func TestAccountImportValidationAndTokenMetadata(t *testing.T) {
	st := newAdminTestStore(t)
	s := managementServer(st)
	claims, _ := json.Marshal(map[string]any{"email": "claims@example.test", oauth.JWTClaimPath: map[string]any{"chatgpt_account_id": "claims-id", "chatgpt_plan_type": "pro"}})
	idToken := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".fixture"
	claims, _ = json.Marshal(map[string]any{"exp": 1791446400, oauth.JWTClaimPath: map[string]any{"chatgpt_account_id": "claims-id"}})
	access := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".fixture"
	src := map[string]any{"platform": "openai", "type": "oauth", "credentials": map[string]any{"id_token": idToken, "access_token": access, "refresh_token": "refresh"}}
	results := importDocument(t, s, "auto", src, 0)
	if results[0].Status != "created" {
		t.Fatalf("claims=%+v", results)
	}
	a, _ := st.GetAccount(t.Context(), results[0].ID)
	if a.Email != "claims@example.test" || a.CodexAccountID != "claims-id" || a.PlanType != "pro" || a.CodexExpiresAt != 1791446400000 {
		t.Fatal("JWT metadata fallback failed")
	}
	nativeStore := newAdminTestStore(t)
	nativeServer := managementServer(nativeStore)
	native := accountBundle{Type: "pi-gateway-accounts", Version: 1, Accounts: []portableAccount{{Codex: &portableCredential{AccessToken: access, RefreshToken: "refresh", IDToken: idToken, AccountID: "claims-id", ExpiresAt: 1791446400000}}}}
	nativeResults := importDocument(t, nativeServer, "auto", native, 0)
	if nativeResults[0].Status != "created" {
		t.Fatalf("native metadata import=%+v", nativeResults)
	}
	nativeAccount, err := nativeStore.GetAccount(t.Context(), nativeResults[0].ID)
	if err != nil || nativeAccount.Email != "claims@example.test" || nativeAccount.PlanType != "pro" || nativeAccount.Enabled {
		t.Fatal("native credential metadata fallback failed")
	}
	cases := []string{
		`{"format":"pi-gateway","data":{"type":"pi-gateway-accounts","version":2,"accounts":[]}}`,
		`{"format":"auto","data":[]}`,
		`{"format":"cliproxyapi","target_account_id":1,"data":[{"type":"codex"},{"type":"codex"}]}`,
		`{"format":"sub2api","data":{"type":"sub2api-data","version":2,"accounts":[]}}`,
		`{"format":"auto","data":null}`,
		`{"format":"auto","data":{}} {}`,
	}
	for _, body := range cases {
		policyRequest(t, s.handleImportAccounts, "POST", 0, body, 400)
	}
	for _, raw := range []string{`{"type":"codex","access_token":"secret","refresh_token":"r","account_id":"claims-id","email":"claims@example.test"}`, `{"type":"codex","access_token":3}`} {
		// A valid existing match remains singular; bad fields report per-record failure.
		results = importDocument(t, s, "cliproxyapi", json.RawMessage(raw), 0)
		if strings.Contains(results[0].Error, "secret") {
			t.Fatal("credential in error")
		}
	}
	for _, item := range []struct {
		raw  string
		want int64
	}{{`"2026-10-08T08:00:00Z"`, 1791446400000}, {`1791446400`, 1791446400000}, {`"1791446400000"`, 1791446400000}, {`null`, 0}} {
		if got := parseCredentialExpiry(json.RawMessage(item.raw)); got != item.want {
			t.Fatalf("expiry=%d want=%d", got, item.want)
		}
	}
}
