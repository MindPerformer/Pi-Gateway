package api

import (
	"context"
	"sync"
	"time"
)

// http.Server.Shutdown does not track hijacked connections. Keep their handlers
// alive until cancellation has released slots and finalized their usage records.
type wsLifecycle struct {
	mu      sync.Mutex
	active  map[string]context.CancelFunc
	closing bool
	done    chan struct{}
	wg      sync.WaitGroup
}

func (l *wsLifecycle) track(id string, cancel context.CancelFunc) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closing {
		return false
	}
	if l.active == nil {
		l.active = make(map[string]context.CancelFunc)
	}
	l.active[id] = cancel
	l.wg.Add(1)
	return true
}

func (l *wsLifecycle) untrack(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, present := l.active[id]; present {
		delete(l.active, id)
		l.wg.Done()
	}
}

func (l *wsLifecycle) shutdown(ctx context.Context) error {
	l.mu.Lock()
	if !l.closing {
		l.closing = true
		l.done = make(chan struct{})
		for _, cancel := range l.active {
			cancel()
		}
		// No Add is possible after closing, even for an upgrade racing shutdown.
		go func() {
			l.wg.Wait()
			close(l.done)
		}()
	}
	done := l.done
	l.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Shutdown cancels WebSockets and waits for their account/usage cleanup. It is
// independent of the HTTP listener and safe to invoke concurrently or repeatedly.
func (s *Server) Shutdown(ctx context.Context) error { return s.lifecycle.shutdown(ctx) }

// Close provides bounded cleanup for the application lifecycle.
func (s *Server) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil && s.logger != nil {
		s.logger.Warn("websocket shutdown deadline reached")
	}
}
