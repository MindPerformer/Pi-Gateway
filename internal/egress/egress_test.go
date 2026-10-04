package egress

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPResponseHeaderTimeoutOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts Options
		want time.Duration
	}{
		{"default", Options{}, 15 * time.Second},
		{"connect-default", Options{ConnectTimeout: 2 * time.Second}, 2 * time.Second},
		{"explicit", Options{ConnectTimeout: time.Second, ResponseHeaderTimeout: 3 * time.Second}, 3 * time.Second},
		{"negative-default", Options{ResponseHeaderTimeout: -time.Second}, 15 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factory := NewFactory(tc.opts)
			client, err := factory.HTTPClient(Direct)
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			transport, ok := client.Transport.(*http.Transport)
			if !ok || transport.ResponseHeaderTimeout != tc.want || client.Timeout != 0 {
				t.Fatalf("transport=%+v client timeout=%s want header timeout=%s and no total timeout", transport, client.Timeout, tc.want)
			}
			cached, err := factory.HTTPClient(Direct)
			if err != nil || cached != client {
				t.Fatalf("client cache mismatch: %v", err)
			}
		})
	}
}

func TestHTTPResponseHeaderTimeoutDoesNotLimitStreamingBody(t *testing.T) {
	const headerTimeout = 100 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		timer := time.NewTimer(3 * headerTimeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			fmt.Fprint(w, "data: healthy\n\n")
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	client, err := NewFactory(Options{ResponseHeaderTimeout: headerTimeout}).HTTPClient(Direct)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "data: healthy\n\n" || time.Since(started) < 3*headerTimeout {
		t.Fatalf("healthy long stream failed: body=%q err=%v elapsed=%s", body, err, time.Since(started))
	}
}
