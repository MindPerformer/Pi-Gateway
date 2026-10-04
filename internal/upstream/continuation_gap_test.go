package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pi-gateway/internal/session"

	"github.com/gorilla/websocket"
)

// Fault injection wraps the actual bounded, serialized session.Store; records
// are never stored in a test map. Clearing replaces the real store atomically.
type switchableSessions struct {
	mu     sync.Mutex
	memory *session.Memory
	paused bool
	reads  atomic.Int64
	hits   atomic.Int64
}

func newSwitchableSessions(t *testing.T) *switchableSessions {
	t.Helper()
	s := &switchableSessions{memory: session.NewMemory(session.Options{})}
	t.Cleanup(func() { s.mu.Lock(); defer s.mu.Unlock(); _ = s.memory.Close() })
	return s
}
func (s *switchableSessions) Resolve(ctx context.Context, key int64, id string) (*session.Record, error) {
	s.reads.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paused {
		return nil, session.ErrUnavailable
	}
	r, err := s.memory.Resolve(ctx, key, id)
	if err == nil {
		s.hits.Add(1)
	}
	return r, err
}
func (s *switchableSessions) RecordCompleted(ctx context.Context, r session.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paused {
		return session.ErrUnavailable
	}
	return s.memory.RecordCompleted(ctx, r)
}
func (s *switchableSessions) Invalidate(ctx context.Context, i session.Invalidation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paused {
		return session.ErrUnavailable
	}
	return s.memory.Invalidate(ctx, i)
}
func (s *switchableSessions) pause(paused bool) { s.mu.Lock(); defer s.mu.Unlock(); s.paused = paused }
func (s *switchableSessions) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.memory.Close()
	s.memory = session.NewMemory(session.Options{})
}

func TestSharedHitRejectsLostSocketAndAllowsCompleteNewChain(t *testing.T) {
	for _, mode := range []string{"new-instance", "dead", "over-age"} {
		t.Run(mode, func(t *testing.T) {
			f := newContinuationFixture(t, nil)
			store := newSwitchableSessions(t)
			c := testClient(t, f.server.URL)
			c.cfg.Sessions = store
			req := continuationRequest(1, "scope")
			runContinuation(t, c, req)
			<-f.frames
			record, err := store.Resolve(context.Background(), 1, "r1")
			if err != nil {
				t.Fatalf("real session store did not commit: %v", err)
			}
			switch mode {
			case "new-instance":
				c = testClient(t, f.server.URL)
				c.cfg.Sessions = store
			case "dead", "over-age":
				c.pool.mu.Lock()
				conn := c.pool.entries[record.SessionID]
				if conn == nil {
					c.pool.mu.Unlock()
					t.Fatal("missing original socket")
				}
				if mode == "dead" {
					c.pool.removeLocked(record.SessionID, conn)
				} else {
					conn.createdAt = time.Now().Add(-2 * c.pool.maxAge)
				}
				c.pool.mu.Unlock()
			}
			hits := store.hits.Load()
			explicit := explicitRequest(t, req, "r1", []any{"old-delta"})
			explicit.Transport = "auto"
			if _, err := c.Stream(context.Background(), explicit, func(*Event) error { return nil }); !errors.Is(err, ErrContinuationUnavailable) {
				t.Fatalf("lost socket accepted shared hit: %v", err)
			}
			if store.hits.Load() != hits+1 {
				t.Fatal("test did not exercise a real shared record hit")
			}
			if f.requests.Load() != 1 || f.dials.Load() != 1 || f.httpCalls.Load() != 0 {
				t.Fatal("old delta dialed or forwarded")
			}
			fresh := continuationRequest(1, "scope")
			fresh.Body = []byte(`{"model":"m","input":["complete-history"]}`)
			runContinuation(t, c, fresh)
			frame := <-f.frames
			if _, previous := frame.body["previous_response_id"]; previous {
				t.Fatal("new chain included old parent")
			}
			if f.requests.Load() != 2 || f.dials.Load() != 2 || f.httpCalls.Load() != 0 {
				t.Fatalf("complete new chain counts: requests=%d dials=%d", f.requests.Load(), f.dials.Load())
			}
		})
	}
}

func TestActualSessionStorePauseClearResumeLocalLiveOnly(t *testing.T) {
	f := newContinuationFixture(t, nil)
	store := newSwitchableSessions(t)
	c := testClient(t, f.server.URL)
	c.cfg.Sessions = store
	req := continuationRequest(1, "scope")
	runContinuation(t, c, req)
	<-f.frames
	if _, err := store.Resolve(context.Background(), 1, "r1"); err != nil {
		t.Fatal(err)
	}
	store.pause(true)
	runContinuation(t, c, explicitRequest(t, req, "r1", []any{"two"}))
	<-f.frames
	if _, err := c.ResolveContinuation(context.Background(), 1, "r2"); err != nil {
		t.Fatalf("outage lost trusted local ready record: %v", err)
	}
	full := continuationRequest(1, "independent")
	full.Body = []byte(`{"model":"m","input":["complete"]}`)
	runContinuation(t, c, full)
	<-f.frames
	rejectUnknown := func() {
		t.Helper()
		before := f.requests.Load()
		dials := f.dials.Load()
		if _, err := c.Stream(context.Background(), explicitRequest(t, req, "unowned", []any{"not-forwarded"}), func(*Event) error { return nil }); !errors.Is(err, ErrContinuationUnavailable) {
			t.Fatalf("unknown continuation=%v", err)
		}
		if f.requests.Load() != before || f.dials.Load() != dials || f.httpCalls.Load() != 0 {
			t.Fatal("unknown response was forwarded")
		}
	}
	rejectUnknown()
	store.clear()
	store.pause(false)
	if _, err := store.Resolve(context.Background(), 1, "r1"); !errors.Is(err, session.ErrMiss) {
		t.Fatalf("store clear did not take effect: %v", err)
	}
	rejectUnknown()
	// Shared state was cleared, but the live socket's last delivered parent is trusted.
	runContinuation(t, c, explicitRequest(t, req, "r2", []any{"four"}))
	frame := <-f.frames
	if frame.connection != 1 {
		t.Fatal("local fallback did not use original live socket")
	}
	record, err := store.Resolve(context.Background(), 1, "r4")
	if err != nil {
		t.Fatalf("recovered store did not accept writes: %v", err)
	}
	c.pool.mu.Lock()
	c.pool.removeLocked(record.SessionID, c.pool.entries[record.SessionID])
	c.pool.mu.Unlock()
	hits := store.hits.Load()
	before := f.requests.Load()
	dials := f.dials.Load()
	if _, err := c.Stream(context.Background(), explicitRequest(t, req, "r4", []any{"dead"}), func(*Event) error { return nil }); !errors.Is(err, ErrContinuationUnavailable) {
		t.Fatalf("recovered store revived dead socket: %v", err)
	}
	if store.hits.Load() != hits+1 || f.requests.Load() != before || f.dials.Load() != dials {
		t.Fatal("dead socket shared hit was not rejected without send")
	}
	runContinuation(t, c, continuationRequest(1, "fresh"))
	<-f.frames
	if f.requests.Load() != before+1 {
		t.Fatal("fresh complete request was not executed once")
	}
}

func TestNonInputToolsInstructionsReasoningChangesDisableDelta(t *testing.T) {
	for _, field := range []string{"tools", "instructions", "reasoning"} {
		t.Run(field, func(t *testing.T) {
			f := newContinuationFixture(t, nil)
			c := testClient(t, f.server.URL)
			req := continuationRequest(1, "scope")
			original := map[string]any{"model": "m", "input": []any{"one"}, "tools": []any{map[string]any{"type": "function", "name": "before"}}, "instructions": "before", "reasoning": map[string]any{"effort": "low"}}
			req.Body = mustJSON(t, original)
			runContinuation(t, c, req)
			<-f.frames
			original["input"] = []any{"one", "answer", "two"}
			switch field {
			case "tools":
				original[field] = []any{map[string]any{"type": "function", "name": "after"}}
			case "instructions":
				original[field] = "after"
			case "reasoning":
				original[field] = map[string]any{"effort": "high"}
			}
			req.Body = mustJSON(t, original)
			runContinuation(t, c, req)
			frame := <-f.frames
			if _, delta := frame.body["previous_response_id"]; delta {
				t.Fatalf("changed %s reused old baseline", field)
			}
			if !reflect.DeepEqual(frame.body["input"], original["input"]) || !reflect.DeepEqual(frame.body[field], original[field]) {
				t.Fatalf("full request changed: %+v", frame.body)
			}
		})
	}
}

func TestContinuationToolCallAndResultOrderOnImplicitAndExplicitDelta(t *testing.T) {
	calls := []any{
		map[string]any{"type": "function_call", "call_id": "call_a", "name": "a", "arguments": "{}"},
		map[string]any{"type": "custom_tool_call", "call_id": "call_b", "name": "b", "input": "x"},
	}
	results := []any{
		map[string]any{"type": "function_call_output", "call_id": "call_a", "output": "A"},
		map[string]any{"type": "custom_tool_call_output", "call_id": "call_b", "output": "B"},
		map[string]any{"type": "message", "role": "user", "content": "next"},
	}
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "implicit", true: "explicit"}[explicit], func(t *testing.T) {
			frames := make(chan map[string]any, 2)
			var dials atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer ws.Close()
				dials.Add(1)
				for n := 0; n < 2; n++ {
					var frame map[string]any
					if ws.ReadJSON(&frame) != nil {
						return
					}
					frames <- frame
					output := []any{}
					id := "tools-next"
					if n == 0 {
						output = calls
						id = "tools-parent"
					}
					if ws.WriteJSON(map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": output}}) != nil {
						return
					}
				}
			}))
			defer server.Close()
			c := testClient(t, server.URL)
			req := continuationRequest(1, "tools")
			runContinuation(t, c, req)
			<-frames
			if explicit {
				req = explicitRequest(t, req, "tools-parent", results)
			} else {
				input := append([]any{"one"}, calls...)
				input = append(input, results...)
				req.Body = mustJSON(t, map[string]any{"model": "m", "input": input})
			}
			runContinuation(t, c, req)
			frame := <-frames
			if frame["previous_response_id"] != "tools-parent" || !reflect.DeepEqual(frame["input"], results) {
				t.Fatalf("tool order changed: %+v", frame)
			}
			if dials.Load() != 1 {
				t.Fatal("tool continuation redialed")
			}
		})
	}
}
