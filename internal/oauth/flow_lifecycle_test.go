package oauth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

type lifecycleRoundTripper func(*http.Request) (*http.Response, error)

func (f lifecycleRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func closeLifecycleManager(t *testing.T, manager *FlowManager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Errorf("close flow manager: %v", err)
	}
}

func TestFlowManagerCloseStopsPendingCleanupAndListener(t *testing.T) {
	manager := NewFlowManager(NewClient(nil), FlowOptions{CallbackHost: "127.0.0.1", CallbackPort: 0, Timeout: time.Hour, Logger: quietLogger()})
	t.Cleanup(func() { closeLifecycleManager(t, manager) })
	flow, err := manager.StartFlow(context.Background(), FlowKindCodex, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	address := manager.ln.Addr().String()
	manager.mu.Unlock()
	closeLifecycleManager(t, manager)
	if got := manager.Get(flow.ID); got == nil || got.Status != FlowFailed {
		t.Fatalf("pending flow not closed: %+v", got)
	}
	if _, err := manager.StartFlow(context.Background(), FlowKindCodex, nil); err == nil {
		t.Fatal("closed manager accepted another flow")
	}
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("callback listener survived Close")
	}
	// Close is idempotent; it must not close a channel twice or restart workers.
	closeLifecycleManager(t, manager)
}

func TestFlowManagerCloseCancelsAndJoinsTokenExchange(t *testing.T) {
	started := make(chan struct{})
	transportDone := make(chan struct{})
	client := NewClient(&http.Client{Transport: lifecycleRoundTripper(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		close(transportDone)
		return nil, r.Context().Err()
	})})
	manager := NewFlowManager(client, FlowOptions{CallbackHost: "127.0.0.1", CallbackPort: 0, Timeout: time.Hour, Logger: quietLogger(), OnComplete: func(*Flow, *Token) error {
		return errors.New("canceled exchange must never reach persistence")
	}})
	t.Cleanup(func() { closeLifecycleManager(t, manager) })
	flow, err := manager.StartFlow(context.Background(), FlowKindCodex, nil)
	if err != nil {
		t.Fatal(err)
	}
	submitDone := make(chan error, 1)
	go func() {
		_, err := manager.SubmitInput(context.Background(), flow.ID, "local-code")
		submitDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("token exchange did not start")
	}
	closeLifecycleManager(t, manager)
	select {
	case <-transportDone:
	default:
		t.Fatal("Close returned before token transport stopped")
	}
	manager.mu.Lock()
	active := len(manager.activeCancels)
	manager.mu.Unlock()
	if active != 0 {
		t.Fatalf("Close left %d active exchange(s)", active)
	}
	select {
	case err := <-submitDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("SubmitInput did not finish after Close")
	}
	if got := manager.Get(flow.ID); got == nil || got.Status != FlowFailed {
		t.Fatalf("canceled flow status = %+v", got)
	}
}
