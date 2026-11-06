package upstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pi-gateway/internal/egress"

	"github.com/gorilla/websocket"
)

func awaitPoolCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("pool condition was not reached")
		case <-tick.C:
		}
	}
}
func assertNoPoolResources(t *testing.T, c *Client) {
	t.Helper()
	c.pool.mu.Lock()
	defer c.pool.mu.Unlock()
	reportTestFailure(t, len(c.pool.entries) != 0 || len(c.pool.slots) != 0 || c.pool.reservations != 0 || c.pool.totalBaselineBytes != 0, func() {
		t.Fatalf("leaked entries=%d slots=%d reservations=%d baseline=%d", len(c.pool.entries), len(c.pool.slots), c.pool.reservations, c.pool.totalBaselineBytes)
	})
}
func awaitStreamError(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not unblock")
		return nil
	}
}

func TestLivePoolSixtyFourWaitersRejectOverflowAndCancelOrCloseCleanly(t *testing.T) {
	for _, mode := range []string{"cancel", "close"} {
		t.Run(mode, func(t *testing.T) { runLivePoolSixtyFourWaitersRejectOverflowAndCancelOrCloseCleanlyCase(t, &mode) })
	}
}

// This wrapper observes real TCP writes; it never injects a write error or blocks
// artificially. A small TCP send buffer plus an upstream which stops reading
// causes genuine socket backpressure in Gorilla's response.create WriteMessage.
type observedTCPWrite struct {
	net.Conn
	started   chan struct{}
	once      sync.Once
	active    atomic.Bool
	completed atomic.Int64
}

func (c *observedTCPWrite) Write(p []byte) (int, error) {
	if bytes.HasPrefix(p, []byte("GET ")) {
		return c.Conn.Write(p)
	}
	c.active.Store(true)
	c.once.Do(func() { close(c.started) })
	n, err := c.Conn.Write(p)
	c.completed.Add(1)
	c.active.Store(false)
	return n, err
}

func TestActualBlockedUpstreamSocketWriteCanceledClosedOrAgedWithoutReplay(t *testing.T) {
	for _, mode := range []string{"cancel", "close", "age"} {
		t.Run(mode, func(t *testing.T) {
			runActualBlockedUpstreamSocketWriteCanceledClosedOrAgedWithoutReplayCase(t, &mode)
		})
	}
}

func TestSlowConsumerActualSocketWriteReleasesLeaseAfterCancelCloseOrAge(t *testing.T) {
	for _, mode := range []string{"cancel", "close", "age"} {
		t.Run(mode, func(t *testing.T) {
			f := newContinuationFixture(t, func(ws *websocket.Conn, _, _ int64) bool {
				return ws.WriteJSON(map[string]any{"type": "response.output_text.delta", "delta": "one"}) == nil
			})
			c := testClient(t, f.server.URL)
			if mode == "age" {
				c.pool.maxAge = 40 * time.Millisecond
			}
			// The downstream peer never reads. This is a real net.Conn write, not a sleep
			// in the callback. The callback owns its downstream write deadline/context.
			writer, reader := net.Pipe()
			defer writer.Close()
			defer reader.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stop := context.AfterFunc(ctx, func() { _ = writer.Close() })
			defer stop()
			entered := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				_, err := c.Stream(ctx, continuationRequest(1, "slow"), func(e *Event) error {
					_ = writer.SetWriteDeadline(time.Now().Add(150 * time.Millisecond))
					close(entered)
					_, err := writer.Write(e.Raw)
					return err
				})
				result <- err
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("callback did not reach blocked downstream write")
			}
			select {
			case err := <-result:
				t.Fatalf("downstream Write did not block: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			if mode == "cancel" {
				cancel()
			} else if mode == "close" {
				c.Close()
			}
			{
				err := awaitStreamError(t, result)
				reportTestFailure(t, err == nil, func() {
					t.Fatal("blocked downstream write succeeded")
				})
			}
			assertNoPoolResources(t, c)
			{
				_, err := c.ResolveContinuation(context.Background(), 1, "r1")
				reportTestFailure(t, !errors.Is(err, ErrContinuationUnavailable), func() {
					t.Fatal("undelivered terminal became ready")
				})
			}
			reportTestFailure(t, f.requests.Load() != 1 || f.dials.Load() != 1 || f.httpCalls.Load() != 0, func() {
				t.Fatal("slow consumer triggered upstream retry")
			})
		})
	}
}

// runLivePoolSixtyFourWaitersRejectOverflowAndCancelOrCloseCleanlyCase exercises one isolated test phase with the original shared state.
func runLivePoolSixtyFourWaitersRejectOverflowAndCancelOrCloseCleanlyCase(t *testing.T, capturedMode *string) {
	t.Helper()

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	f := newContinuationFixture(t, func(_ *websocket.Conn, _, n int64) bool {
		if n == 1 {
			close(entered)
			<-release
		}
		return true
	})
	c := testClient(t, f.server.URL)
	c.pool.idleTimeout = 5 * time.Second
	ownerCtx, ownerCancel := context.WithCancel(context.Background())
	defer ownerCancel()
	req := continuationRequest(1, "busy")
	owner := make(chan error, 1)
	go func() { _, err := c.Stream(ownerCtx, req, func(*Event) error { return nil }); owner <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("owner did not reach upstream")
	}
	waiterCtx, waiterCancel := context.WithCancel(context.Background())
	defer waiterCancel()
	results := make(chan error, maxPoolWaitersPerKey)
	for i := 0; i < maxPoolWaitersPerKey; i++ {
		go func() { _, err := c.Stream(waiterCtx, req, func(*Event) error { return nil }); results <- err }()
	}
	key := c.requestPoolKey(req)
	awaitPoolCondition(t, func() bool {
		c.pool.mu.Lock()
		defer c.pool.mu.Unlock()
		s := c.pool.slots[key]
		return s != nil && len(s.waiters) == maxPoolWaitersPerKey
	})
	{
		_, err := c.Stream(context.Background(), req, func(*Event) error { return nil })
		reportTestFailure(t, !errors.Is(err, ErrPoolBusy), func() {
			t.Fatalf("65th waiter=%v", err)
		})
	}
	reportTestFailure(t, f.dials.Load() != 1 || f.requests.Load() != 1, func() {
		t.Fatal("queued request redialed or generated")
	})
	if (*capturedMode) == "cancel" {
		waiterCancel()
	} else {
		c.Close()
	}
	for i := 0; i < maxPoolWaitersPerKey; i++ {
		err := awaitStreamError(t, results)
		reportTestFailure(t, err == nil, func() {
			t.Fatal("canceled waiter succeeded")
		})
		reportTestFailure(t, (*capturedMode) == "cancel" && !errors.Is(err, context.Canceled), func() {
			t.Fatalf("waiter cancel=%v", err)
		})
	}
	if (*capturedMode) == "cancel" {
		c.pool.mu.Lock()
		s := c.pool.slots[key]
		valid := s != nil && len(s.waiters) == 0 && s.refs == 1 && s.busy
		c.pool.mu.Unlock()
		reportTestFailure(t, !valid, func() {
			t.Fatal("canceling queue changed owner lease or leaked waiter references")
		})
		ownerCancel()
	}
	{
		err := awaitStreamError(t, owner)
		reportTestFailure(t, err == nil, func() {
			t.Fatal("active owner was not interrupted")
		})
	}
	assertNoPoolResources(t, c)
	once.Do(func() { close(release) })
	reportTestFailure(t, f.dials.Load() != 1 || f.requests.Load() != 1 || f.httpCalls.Load() != 0, func() {
		t.Fatal("shutdown replayed queued requests")
	})
}

// runActualBlockedUpstreamSocketWriteCanceledClosedOrAgedWithoutReplayCase exercises one isolated test phase with the original shared state.
func runActualBlockedUpstreamSocketWriteCanceledClosedOrAgedWithoutReplayCase(t *testing.T, capturedMode *string) {
	t.Helper()

	release := make(chan struct{})
	var releaseOnce sync.Once
	var dials, posts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveActualBlockedUpstreamSocketWriteCanceledClosedOrAgedWithoutReplayRequest(w, r, &release, &dials, &posts)
	}))
	observed := make(chan *observedTCPWrite, 1)
	factory := egress.NewFactory(egress.Options{})
	factory.BaseDialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return verifyActualBlockedUpstreamSocketWriteCanceledClosedOrAgedWithoutReplay(ctx, network, addr, &observed)
	}
	c := New(Config{Pool: true, WSURL: "ws" + strings.TrimPrefix(server.URL, "http"), SSEURL: server.URL, IdleTimeout: time.Second}, factory)
	if (*capturedMode) == "age" {
		c.pool.maxAge = 500 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	req := continuationRequest(1, "large")
	req.Transport = "auto"
	req.Body = []byte(`{"model":"m","input":["` + strings.Repeat("x", 8<<20) + `"]}`)
	result := make(chan error, 1)
	finished := make(chan struct{})
	defer func() {
		cancel()
		c.Close()
		// Unblock the hijacked peer on every exit, including t.Fatal.
		// server.Close does not manage hijacked WebSocket connections.
		releaseOnce.Do(func() { close(release) })
		server.Close()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("stream goroutine did not exit during cleanup")
		}
	}()
	// Preparing and framing 8 MiB of JSON under -race is CPU work, not a
	// blocked socket. Give setup a separate bounded budget; retain the original
	// backpressure proof, connection age and post-cancellation deadlines below.
	setup, stopSetup := context.WithTimeout(ctx, 10*time.Second)
	defer stopSetup()
	started := time.Now()
	go func() {
		defer close(finished)
		_, err := c.Stream(ctx, req, func(*Event) error { return nil })
		result <- err
	}()
	wire, err := awaitSocketWriteSetup(setup, observed, result)
	if err != nil {
		t.Fatal(err)
	}
	stopSetup()
	t.Logf("request preparation, dial and first socket write: %s", time.Since(started))
	// Prove a real Write remains in progress while completed-write count is stable.
	deadline := time.Now().Add(300 * time.Millisecond)
	blocked := false
	for time.Now().Before(deadline) {
		count := wire.completed.Load()
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case err := <-result:
			timer.Stop()
			t.Fatalf("request ended before write backpressure proof: %v", err)
		case <-timer.C:
		}
		if wire.active.Load() && wire.completed.Load() == count {
			blocked = true
			break
		}
	}
	reportTestFailure(t, !blocked, func() {
		t.Fatal("TCP peer did not exert write backpressure")
	})
	if (*capturedMode) == "cancel" {
		cancel()
	} else if (*capturedMode) == "close" {
		c.Close()
	}
	err = awaitStreamError(t, result)
	var transport *transportError
	reportTestFailure(t, err == nil || !errors.As(err, &transport) || transport.requestNotSent, func() {
		t.Fatalf("blocked write lost send boundary: %v", err)
	})
	reportTestFailure(t, wire.active.Load(), func() {
		t.Fatal("TCP Write remained blocked after stream return")
	})
	assertNoPoolResources(t, c)
	reportTestFailure(t, dials.Load() != 1 || posts.Load() != 0, func() {
		t.Fatalf("partial write replayed: dials=%d posts=%d", dials.Load(), posts.Load())
	})
	releaseOnce.Do(func() { close(release) })
}

// serveActualBlockedUpstreamSocketWriteCanceledClosedOrAgedWithoutReplayRequest exercises one isolated test phase with the original shared state.
func serveActualBlockedUpstreamSocketWriteCanceledClosedOrAgedWithoutReplayRequest(w http.ResponseWriter, r *http.Request, capturedRelease *chan struct{}, capturedDials *atomic.Int64, capturedPosts *atomic.Int64) {
	if !websocket.IsWebSocketUpgrade(r) {
		(*capturedPosts).Add(1)
		http.Error(w, "unexpected HTTP retry", 500)
		return
	}
	ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	(*capturedDials).Add(1)
	if tcp, ok := ws.UnderlyingConn().(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(1024)
	}
	<-(*capturedRelease) // deliberately never read the WebSocket request body
}

// verifyActualBlockedUpstreamSocketWriteCanceledClosedOrAgedWithoutReplay exercises one isolated test phase with the original shared state.
func verifyActualBlockedUpstreamSocketWriteCanceledClosedOrAgedWithoutReplay(ctx context.Context, network, addr string, capturedObserved *chan *observedTCPWrite) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetWriteBuffer(1024)
	}
	wrapped := &observedTCPWrite{Conn: conn, started: make(chan struct{})}
	(*capturedObserved) <- wrapped
	return wrapped, nil
}

// awaitSocketWriteSetup distinguishes slow request preparation from an early
// stream failure. One context bounds both setup phases without changing the
// deadlines that verify a blocked write is actually interrupted.
func awaitSocketWriteSetup(ctx context.Context, observed <-chan *observedTCPWrite, result <-chan error) (*observedTCPWrite, error) {
	var wire *observedTCPWrite
	select {
	case wire = <-observed:
	case err := <-result:
		return nil, fmt.Errorf("stream ended before dialing: %v", err)
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for request preparation and dial: %w", ctx.Err())
	}
	select {
	case <-wire.started:
		return wire, nil
	case err := <-result:
		return nil, fmt.Errorf("stream ended before first socket write: %v", err)
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for first socket write: %w", ctx.Err())
	}
}

func TestAwaitSocketWriteSetupReportsPhaseAndEarlyFailure(t *testing.T) {
	for _, tc := range []struct {
		name, phase, want string
	}{
		{"ready", "write", ""},
		{"failure_before_dial", "dial", "stream ended before dialing: setup failure"},
		{"failure_before_write", "write", "stream ended before first socket write: setup failure"},
		{"cancellation_before_dial", "dial", "waiting for request preparation and dial: context canceled"},
		{"cancellation_before_write", "write", "waiting for first socket write: context canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			observed := make(chan *observedTCPWrite)
			result := make(chan error, 1)
			wire := &observedTCPWrite{started: make(chan struct{})}
			producerDone := make(chan struct{})
			signal := func() {
				if tc.want == "" {
					close(wire.started)
				} else if strings.HasPrefix(tc.name, "cancellation") {
					cancel()
				} else {
					result <- errors.New("setup failure")
				}
			}
			if tc.phase == "dial" {
				signal()
				close(producerDone)
			} else {
				go func() {
					defer close(producerDone)
					select {
					case observed <- wire:
						signal()
					case <-ctx.Done():
					}
				}()
			}
			got, err := awaitSocketWriteSetup(ctx, observed, result)
			if tc.want == "" {
				if err != nil || got != wire {
					t.Fatalf("ready write not returned: wire=%p error=%v", got, err)
				}
			} else if err == nil || err.Error() != tc.want || got != nil {
				t.Fatalf("got wire=%p error=%v; want %s", got, err, tc.want)
			}
			select {
			case <-producerDone:
			case <-time.After(time.Second):
				t.Fatal("setup signal producer did not exit")
			}
		})
	}
}
