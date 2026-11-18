package accounts

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pi-gateway/internal/egress"
	"pi-gateway/internal/oauth"
	"pi-gateway/internal/store"
)

func TestCredentialRefreshWaitHonorsCancellation(t *testing.T) {
	for _, kind := range []string{"primary_token", "primary_refresh", "codex_token"} {
		t.Run(kind, func(t *testing.T) {
			factory := egress.NewFactory(egress.Options{})
			var dials atomic.Int64
			factory.BaseDialer = func(context.Context, string, string) (net.Conn, error) {
				dials.Add(1)
				return nil, errors.New("test forbids token network requests")
			}
			manager := New(nil, oauth.NewClient(nil), factory, Options{})
			account := &store.Account{ID: 42, RefreshToken: "local-refresh", OAuthClientID: "local-issued", CodexRefreshToken: "local-codex-refresh", CodexAccountID: "local-codex-account"}
			lock := manager.refreshLock(account.ID)
			lock.Lock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var err error
				switch kind {
				case "primary_token":
					_, _, err = manager.EnsureFreshToken(ctx, account)
				case "primary_refresh":
					err = manager.Refresh(ctx, account)
				case "codex_token":
					_, _, _, err = manager.EnsureFreshCodexToken(ctx, account)
				}
				done <- err
			}()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					lock.Unlock()
					t.Fatalf("queued %s returned %v", kind, err)
				}
			case <-time.After(time.Second):
				lock.Unlock()
				<-done
				t.Fatal("canceled credential operation remained queued behind refresh lock")
			}
			if dials.Load() != 0 {
				lock.Unlock()
				t.Fatalf("canceled operation dialed network %d times", dials.Load())
			}
			// Cancellation must not unlock the currently running refresh or retain
			// ownership after that owner completes.
			if len(lock.token) != 1 {
				t.Fatal("canceled waiter changed current lock ownership")
			}
			lock.Unlock()
			if err := lock.LockContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			lock.Unlock()
		})
	}
}

func TestScheduledPrimaryRefreshCapsLegacyMargin(t *testing.T) {
	st := newAccountsTestStore(t)
	ctx := t.Context()
	factory := egress.NewFactory(egress.Options{})
	client, err := factory.HTTPClient("")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	client.Transport = refreshTestTransport(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		n := calls.Add(1)
		if want := fmt.Sprintf("refresh-%d", n-1); r.Form.Get("refresh_token") != want {
			return nil, fmt.Errorf("refresh used a stale credential on call %d", n)
		}
		body := fmt.Sprintf(`{"access_token":"opaque-%d","refresh_token":"refresh-%d","scope":%q,"expires_in":3600}`, n, n, oauth.ChatGPTDirectTokenScope)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})

	wallNow := time.Now().Truncate(time.Second)
	now := wallNow.Add(-56 * time.Minute)
	// Apply the legacy value through the persisted settings path.
	if err := st.SaveSettings(ctx, &store.Settings{RefreshMarginSecs: 3600}); err != nil {
		t.Fatal(err)
	}
	manager := New(st, oauth.NewClient(nil), factory, Options{Now: func() time.Time { return now }})
	if manager.currentRefreshMargin() != time.Hour {
		t.Fatal("legacy margin was not loaded")
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"iat":%d}`, now.Unix())))
	a := &store.Account{Name: "57-minute credential", AccessToken: "e30." + payload + ".sig", RefreshToken: "refresh-0", OAuthClientID: "issued-client", ExpiresAt: now.Add(57 * time.Minute).UnixMilli(), Enabled: true, Status: store.AccountStatusReady}
	if err := st.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		now = now.Add(30 * time.Second)
		manager.refreshDue(ctx, 1)
	}
	if calls.Load() != 0 {
		t.Fatalf("scheduled refresh touched a fresh 57-minute credential: %d calls", calls.Load())
	}

	// A near-expiry credential still rotates. Both paths must skip a second
	// refresh if they resume from a candidate selected before that rotation.
	now = wallNow
	stale, err := st.GetAccount(ctx, a.ID)
	if err != nil || stale == nil {
		t.Fatalf("load near-expiry account: %v", err)
	}
	manager.refreshDue(ctx, 1)
	if calls.Load() != 1 {
		t.Fatalf("scheduled refresh missed the near-expiry credential: %d calls", calls.Load())
	}
	now = time.Now().Add(30 * time.Second)
	if err := manager.refreshScheduled(ctx, stale.ID); err != nil {
		t.Fatal(err)
	}
	manager.refreshDue(ctx, 1)
	if calls.Load() != 1 {
		t.Fatalf("scheduler repeated a completed rotation: %d calls", calls.Load())
	}
	if got, _, err := manager.EnsureFreshToken(ctx, stale); err != nil || got != "opaque-1" || calls.Load() != 1 {
		t.Fatalf("stale on-demand snapshot: token=%q err=%v calls=%d", got, err, calls.Load())
	}

	latest, err := st.GetAccount(ctx, a.ID)
	if err != nil || latest == nil {
		t.Fatalf("load rotated account: %v", err)
	}
	if err := manager.Refresh(ctx, latest); err != nil || calls.Load() != 2 {
		t.Fatalf("manual refresh was not forced: %v, calls=%d", err, calls.Load())
	}
}

func TestPrimaryRefreshDueWithoutLifetimeEvidence(t *testing.T) {
	now := time.Now()
	a := &store.Account{Enabled: true, Status: store.AccountStatusReady, AccessToken: "opaque", RefreshToken: "refresh"}
	for _, tc := range []struct {
		name   string
		expiry int64
		due    bool
	}{
		{"unknown expiry", 0, true},
		{"expired", now.Add(-time.Second).UnixMilli(), true},
		{"inside configured margin", now.Add(57 * time.Minute).UnixMilli(), true},
		{"outside configured margin", now.Add(61 * time.Minute).UnixMilli(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a.ExpiresAt = tc.expiry
			if got := primaryRefreshDue(a, now, time.Hour); got != tc.due {
				t.Fatalf("primaryRefreshDue = %t, want %t", got, tc.due)
			}
		})
	}
}

type refreshTestTransport func(*http.Request) (*http.Response, error)

func (f refreshTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
