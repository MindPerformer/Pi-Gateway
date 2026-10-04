package accounts

import (
	"context"
	"errors"
	"testing"

	"pi-gateway/internal/store"
)

func TestCodexCASPreventsUnlinkResurrection(t *testing.T) {
	st := newAccountsTestStore(t)

	account := &store.Account{
		Name:              "quota",
		AccountID:         "primary",
		AccessToken:       "chat-token",
		RefreshToken:      "chat-refresh",
		CodexAccessToken:  "codex-old",
		CodexRefreshToken: "codex-refresh-old",
		CodexAccountID:    "codex-account-old",
		Enabled:           true,
	}
	if err := st.CreateAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}

	if err := st.SaveCodexCredential(context.Background(), account.ID, account.CodexAccessToken, account.CodexRefreshToken, "", 123, account.CodexAccountID); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAccountQuotaIfCurrent(context.Background(), account.ID, account.CodexRefreshToken, `{"linked":true}`, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.ClearCodexCredential(context.Background(), account.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateCodexCredentialsIfCurrent(context.Background(), account.ID, "codex-refresh-old", "codex-new", "codex-refresh-new", 123, "codex-account-new"); !errors.Is(err, store.ErrCodexCredentialChanged) {
		t.Fatalf("stale Codex refresh error = %v, want ErrCodexCredentialChanged", err)
	}
	if err := st.SaveAccountQuotaIfCurrent(context.Background(), account.ID, "codex-refresh-old", `{"stale":true}`, "", ""); !errors.Is(err, store.ErrCodexCredentialChanged) {
		t.Fatalf("stale quota write error = %v, want ErrCodexCredentialChanged", err)
	}

	got, err := st.GetAccount(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CodexRefreshToken != "" || got.CodexAccessToken != "" || got.CodexAccountID != "" || got.QuotaJSON != "" {
		t.Fatalf("unlink was resurrected: %+v", got)
	}
}

func TestCodexCASRejectsEmptyExpectedRefresh(t *testing.T) {
	st := newAccountsTestStore(t)
	account := &store.Account{Name: "unlinked", AccountID: "primary"}
	if err := st.CreateAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"", "  "} {
		if err := st.UpdateCodexCredentialsIfCurrent(context.Background(), account.ID, expected, "codex-access", "codex-refresh", 123, "codex-account"); !errors.Is(err, store.ErrCodexCredentialChanged) {
			t.Fatalf("empty expected refresh allowed a credential write: %v", err)
		}
		if err := st.SaveAccountQuotaIfCurrent(context.Background(), account.ID, expected, `{"stale":true}`, "", ""); !errors.Is(err, store.ErrCodexCredentialChanged) {
			t.Fatalf("empty expected refresh allowed a quota write: %v", err)
		}
	}
}

func TestCodexCASRejectsOldCredentialAfterRelink(t *testing.T) {
	st := newAccountsTestStore(t)

	account := &store.Account{Name: "quota", AccountID: "primary", Enabled: true}
	if err := st.CreateAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCodexCredential(context.Background(), account.ID, "codex-new", "codex-refresh-new", "", 100, "codex-account-new"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAccountQuotaIfCurrent(context.Background(), account.ID, "codex-refresh-old", `{"stale":true}`, "", ""); !errors.Is(err, store.ErrCodexCredentialChanged) {
		t.Fatalf("old credential quota write error = %v, want ErrCodexCredentialChanged", err)
	}
	if err := st.UpdateCodexCredentialsIfCurrent(context.Background(), account.ID, "codex-refresh-old", "stale-access", "stale-refresh", 123, "stale-account"); !errors.Is(err, store.ErrCodexCredentialChanged) {
		t.Fatalf("old credential refresh error = %v, want ErrCodexCredentialChanged", err)
	}
	got, err := st.GetAccount(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CodexRefreshToken != "codex-refresh-new" || got.QuotaJSON != "" {
		t.Fatalf("old credential changed relinked account: %+v", got)
	}
}
