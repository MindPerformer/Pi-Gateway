package accounts

import (
	"context"
	"errors"
	"net"
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
