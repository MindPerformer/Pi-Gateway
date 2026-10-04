package oauth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
}

// TestFinishMarksFlowFailedWhenPersistenceFails verifies a sign-in is never
// reported as successful when the credential was not actually stored.
func TestFinishMarksFlowFailedWhenPersistenceFails(t *testing.T) {
	m := NewFlowManager(nil, FlowOptions{
		Logger:     quietLogger(),
		OnComplete: func(*Flow, *Token) error { return errors.New("store write failed") },
	})
	flow := &Flow{ID: "f1", Kind: FlowKindCodex, Status: FlowExchanging}
	m.flows[flow.ID] = flow

	m.finish(flow, &Token{Access: "a", Email: "u@example.com"}, nil)

	if flow.Status != FlowFailed {
		t.Errorf("status = %q, want %q", flow.Status, FlowFailed)
	}
	if flow.Error == "" {
		t.Errorf("failed flow must carry the reason")
	}
}

// TestFinishMarksFlowCompletedOnSuccess verifies the happy path.
func TestFinishMarksFlowCompletedOnSuccess(t *testing.T) {
	persisted := false
	m := NewFlowManager(nil, FlowOptions{
		Logger:     quietLogger(),
		OnComplete: func(*Flow, *Token) error { persisted = true; return nil },
	})
	flow := &Flow{ID: "f1", Kind: FlowKindChatGPT, Status: FlowExchanging}
	m.flows[flow.ID] = flow

	m.finish(flow, &Token{Access: "a", Email: "u@example.com", PlanType: "plus"}, nil)

	if flow.Status != FlowCompleted {
		t.Errorf("status = %q, want %q", flow.Status, FlowCompleted)
	}
	if !persisted {
		t.Errorf("OnComplete was not invoked")
	}
	if flow.Email != "u@example.com" || flow.PlanType != "plus" {
		t.Errorf("token metadata not copied onto the flow: %+v", flow)
	}
}

// TestFinishWithoutOnCompleteStillCompletes verifies a nil callback is harmless.
func TestFinishWithoutOnCompleteStillCompletes(t *testing.T) {
	m := NewFlowManager(nil, FlowOptions{Logger: quietLogger()})
	flow := &Flow{ID: "f1", Kind: FlowKindChatGPT, Status: FlowExchanging}
	m.flows[flow.ID] = flow

	m.finish(flow, &Token{Access: "a"}, nil)

	if flow.Status != FlowCompleted {
		t.Errorf("status = %q, want %q", flow.Status, FlowCompleted)
	}
}

// TestFailByStateMarksPendingFlowFailed verifies a refused callback releases the
// flow instead of leaving it pending (which would hold the callback listener).
func TestFailByStateMarksPendingFlowFailed(t *testing.T) {
	m := NewFlowManager(nil, FlowOptions{Logger: quietLogger()})
	flow := &Flow{ID: "f1", Kind: FlowKindChatGPT, Status: FlowPending, state: "state-1"}
	m.flows[flow.ID] = flow

	m.failByState("state-1", "authorization was refused")

	if flow.Status != FlowFailed {
		t.Errorf("status = %q, want %q", flow.Status, FlowFailed)
	}
	if flow.Error != "authorization was refused" {
		t.Errorf("error = %q", flow.Error)
	}
}

// TestFailByStateIgnoresUnknownOrEmptyState verifies failByState cannot touch an
// unrelated flow.
func TestFailByStateIgnoresUnknownOrEmptyState(t *testing.T) {
	m := NewFlowManager(nil, FlowOptions{Logger: quietLogger()})
	flow := &Flow{ID: "f1", Kind: FlowKindChatGPT, Status: FlowPending, state: "state-1"}
	m.flows[flow.ID] = flow

	m.failByState("", "empty state must be ignored")
	m.failByState("other", "unknown state must be ignored")

	if flow.Status != FlowPending {
		t.Errorf("unrelated flow was mutated: %q", flow.Status)
	}
}

// TestGetReturnsSnapshot verifies Get cannot expose the live flow to concurrent
// readers (the callback goroutine keeps mutating it).
func TestGetReturnsSnapshot(t *testing.T) {
	m := NewFlowManager(nil, FlowOptions{Logger: quietLogger()})
	flow := &Flow{ID: "f1", Kind: FlowKindChatGPT, Status: FlowPending, Meta: map[string]string{"name": "before"}}
	m.flows[flow.ID] = flow

	snapshot := m.Get("f1")
	if snapshot == nil {
		t.Fatalf("Get returned nil for a known flow")
	}
	snapshot.Status = FlowCompleted
	snapshot.Error = "mutated by caller"
	snapshot.Meta["name"] = "mutated"

	if flow.Status != FlowPending || flow.Error != "" || flow.Meta["name"] != "before" {
		t.Errorf("Get exposed live flow state: status=%q error=%q meta=%v", flow.Status, flow.Error, flow.Meta)
	}
}

func TestStartFlowReturnsSnapshot(t *testing.T) {
	m := NewFlowManager(NewClient(nil), FlowOptions{
		CallbackHost: "127.0.0.1",
		CallbackPort: 0,
		Timeout:      time.Second,
		Logger:       quietLogger(),
	})
	flow, err := m.StartFlow(context.Background(), FlowKindCodex, map[string]string{"name": "before"})
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	flow.Status = FlowCompleted
	flow.Meta["name"] = "mutated"
	got := m.Get(flow.ID)
	if got == nil || got.Status != FlowPending || got.Meta["name"] != "before" {
		t.Fatalf("StartFlow returned live state: %+v", got)
	}
	m.mu.Lock()
	if live := m.flows[flow.ID]; live != nil {
		live.Status = FlowFailed
	}
	m.mu.Unlock()
	m.maybeStopCallbackServer()
}

func TestSubmitInputRejectsTerminalFlowsWithSnapshots(t *testing.T) {
	for _, status := range []string{FlowCompleted, FlowFailed} {
		m := NewFlowManager(nil, FlowOptions{Logger: quietLogger()})
		flow := &Flow{ID: "terminal-" + status, Kind: FlowKindCodex, Status: status}
		m.flows[flow.ID] = flow
		got, err := m.SubmitInput(context.Background(), flow.ID, "code")
		if err == nil {
			t.Fatalf("status %q: expected terminal flow error", status)
		}
		if got == nil || got.Status != status {
			t.Fatalf("status %q: got %+v, err=%v", status, got, err)
		}
		got.Status = FlowPending
		if flow.Status != status {
			t.Errorf("status %q: returned flow was live", status)
		}
	}
}

func TestFinishDoesNotResurrectDeletedFlow(t *testing.T) {
	m := NewFlowManager(nil, FlowOptions{Logger: quietLogger()})
	flow := &Flow{ID: "deleted", Kind: FlowKindCodex, Status: FlowExchanging}
	m.flows[flow.ID] = flow
	delete(m.flows, flow.ID)
	m.finish(flow, &Token{Access: "a"}, nil)
	if flow.Status != FlowExchanging {
		t.Fatalf("deleted flow was mutated to %q", flow.Status)
	}
}
