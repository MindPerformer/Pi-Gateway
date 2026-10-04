package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// With no consumer running, the first frame must remain buffered while the
// second receives an explicit rejection. Consuming the first reopens the queue.
func TestReadWSMessagesPreservesFirstFrameBeforeConsumer(t *testing.T) {
	messages := make(chan wsReadEvent, 1)
	readerDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(readerDone)
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		_ = readWSMessages(r.Context(), conn, newWSFrameWriter(conn), messages)
	}))
	defer srv.Close()
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() {
		_ = client.Close()
		select {
		case <-readerDone:
		case <-time.After(5 * time.Second):
			t.Error("reader did not exit on disconnect")
		}
	}()
	first := `{"type":"response.create","event_id":"first"}`
	second := `{"type":"response.create","event_id":"second"}`
	for _, frame := range []string{first, second} {
		if err := client.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	var rejected struct {
		Type    string `json:"type"`
		EventID string `json:"event_id"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := client.ReadJSON(&rejected); err != nil {
		t.Fatalf("read rejection: %v", err)
	}
	if rejected.Type != "error" || rejected.EventID != "second" || rejected.Error.Code != "too_many_requests" {
		t.Fatalf("rejection = %+v", rejected)
	}
	select {
	case event := <-messages:
		if event.messageType != websocket.TextMessage || string(event.payload) != first {
			t.Fatalf("buffered first frame = %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first frame was dropped before the consumer ran")
	}
	third := `{"type":"response.create","event_id":"third"}`
	if err := client.WriteMessage(websocket.TextMessage, []byte(third)); err != nil {
		t.Fatalf("write after rejection: %v", err)
	}
	select {
	case event := <-messages:
		if string(event.payload) != third {
			t.Fatalf("frame after rejection = %s, want %s", event.payload, third)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader stopped after rejecting a frame")
	}
}

func TestWSFrameWriterCancellationWhileWaitingForLock(t *testing.T) {
	writer := newWSFrameWriter(nil)
	<-writer.mu // Simulate another goroutine holding the connection write lock.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		done <- writer.write(ctx, []byte(`{}`))
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("write waiting for lock = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		writer.mu <- struct{}{}
		t.Fatal("cancelled write remained blocked waiting for the connection lock")
	}
}

func TestWSFrameWriterSkipsCancelledContext(t *testing.T) {
	writer := newWSFrameWriter(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := writer.write(ctx, []byte(`{"type":"should_not_write"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("write after cancellation = %v, want context.Canceled", err)
	}
	if err := writer.writeJSON(ctx, map[string]string{"type": "should_not_write"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("writeJSON after cancellation = %v, want context.Canceled", err)
	}
}
