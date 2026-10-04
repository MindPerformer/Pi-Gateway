package accounts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"pi-gateway/internal/egress"

	"pi-gateway/internal/oauth"
	"pi-gateway/internal/store"
)

func TestCreateFromTokenRejectsCodexCredential(t *testing.T) {
	st := newAccountsTestStore(t)
	manager := New(st, oauth.NewClient(nil), egress.NewFactory(egress.Options{}), Options{})

	access := jwtForClaims(t, map[string]any{
		oauth.JWTClaimPath: map[string]any{"chatgpt_account_id": "codex-account"},
	})
	if _, err := manager.CreateFromToken(context.Background(), "codex", &oauth.Token{Access: access}, ""); err == nil {
		t.Fatal("Codex-shaped token was accepted as a ChatGPT credential")
	}
}

func TestCreateFromTokenAcceptsChatGPTCredential(t *testing.T) {
	st := newAccountsTestStore(t)
	manager := New(st, oauth.NewClient(nil), egress.NewFactory(egress.Options{}), Options{})

	account, err := manager.CreateFromToken(context.Background(), "chatgpt", &oauth.Token{
		Access:   "chatgpt-access",
		Email:    "user@example.com",
		ClientID: "issued-client-123",
	}, "")
	if err != nil {
		t.Fatalf("ChatGPT credential rejected: %v", err)
	}
	if account.AccountID != "chatgpt:user@example.com" {
		t.Fatalf("account id = %q, want chatgpt:user@example.com", account.AccountID)
	}
}

func TestRefreshMergesStatusWriteError(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/accounts.db")
	if err != nil {
		t.Fatal(err)
	}
	account := &store.Account{
		Name: "refresh-failure", AccountID: "chatgpt:refresh-failure",
		RefreshToken: "refresh-token", Enabled: true,
	}
	if err := st.CreateAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	manager := New(st, oauth.NewClient(nil), egress.NewFactory(egress.Options{}), Options{Logger: logger})

	err = manager.Refresh(context.Background(), account)
	if err == nil {
		t.Fatal("expected both a missing client id and a closed-store status error")
	}
	for _, message := range []string{"missing issued ChatGPT oauth_client_id", "recording failed refresh status", "database is closed"} {
		if !strings.Contains(err.Error(), message) || !strings.Contains(logs.String(), message) {
			t.Errorf("returned/logged error missing %q: returned=%v log=%s", message, err, logs.String())
		}
	}
	if account.Status != store.AccountStatusExpired || !strings.Contains(account.LastError, "missing issued ChatGPT") {
		t.Fatal("failed refresh did not update the in-memory status")
	}
}

func TestLinkCodexCredentialRequiresRefreshToken(t *testing.T) {
	st := newAccountsTestStore(t)
	manager := New(st, oauth.NewClient(nil), egress.NewFactory(egress.Options{}), Options{})
	account := &store.Account{Name: "quota", AccessToken: "chatgpt-access", RefreshToken: "chatgpt-refresh"}
	if err := st.CreateAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	for _, refresh := range []string{"", "  \t "} {
		err := manager.LinkCodexCredential(context.Background(), account, &oauth.Token{Access: "codex-access", Refresh: refresh, AccountID: "codex-account"})
		if err == nil || !strings.Contains(err.Error(), "missing a refresh token") {
			t.Fatalf("empty refresh token error = %v", err)
		}
		got, err := st.GetAccount(context.Background(), account.ID)
		if err != nil {
			t.Fatal(err)
		}
		if account.CodexLinked() || got.CodexLinked() || got.CodexAccessToken != "" {
			t.Fatal("account became Codex-linked after rejected binding")
		}
	}
	if err := manager.LinkCodexCredential(context.Background(), account, &oauth.Token{Access: "codex-access", Refresh: "codex-refresh", AccountID: "codex-account"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetAccount(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !account.CodexLinked() || !got.CodexLinked() || got.AccessToken != "chatgpt-access" || got.RefreshToken != "chatgpt-refresh" {
		t.Fatal("valid Codex binding must preserve the main credential and report linked")
	}
}

func jwtForClaims(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	encode := func(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }
	return encode(header) + "." + encode(body) + ".sig"
}
