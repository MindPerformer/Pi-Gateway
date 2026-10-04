package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"pi-gateway/internal/store"
	"pi-gateway/internal/upstream"
)

// TestResolveTransportAccountProtocolWins locks in the pivot's core rule: an
// account's upstream_protocol decides the protocol used towards the backend, and
// the client's own protocol never overrides it.
func TestResolveTransportAccountProtocolWins(t *testing.T) {
	s := &Server{}
	rt := &store.Settings{UpstreamTransport: "sse"}

	cases := []struct {
		name    string
		account string
		client  string
		want    string
	}{
		// "ws" must never be passed through verbatim: the upstream client only
		// understands websocket / websocket-cached, so an un-normalised value
		// silently degrades to SSE.
		{"ws account, sse client", "ws", "sse", "websocket-cached"},
		{"ws account, ws client", "ws", "ws", "websocket-cached"},
		{"ws account, upper case", "WS", "sse", "websocket-cached"},
		{"sse account, ws client", "sse", "ws", "sse"},
		{"sse account, sse client", "sse", "sse", "sse"},
		// No account override: the global default (sse) applies, not the client.
		{"no account override, ws client", "", "ws", "sse"},
		{"no account override, sse client", "", "sse", "sse"},
		// Unknown values are ignored rather than forwarded.
		{"bogus account value, ws client", "banana", "ws", "sse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := &store.Account{UpstreamProtocol: tc.account}
			got := s.resolveTransport(rt, nil, account, tc.client)
			if got != tc.want {
				t.Errorf("resolveTransport(account=%q, client=%q) = %q, want %q",
					tc.account, tc.client, got, tc.want)
			}
		})
	}
}

// TestResolveTransportNilAccountUsesGlobalDefault verifies the no-account path and
// that "passthrough" remains an explicit opt-in that mirrors the client.
func TestErrorFromUpstreamPrefersHTTPStatusAndKeepsRequestID(t *testing.T) {
	err := &upstream.FailureError{Code: "account_blocked", Status: 403, Message: "denied", RequestID: "req-123"}
	got := errorFromUpstream(err)
	if got.Status != http.StatusForbidden || got.Code != "account_blocked" || got.RequestID != "req-123" {
		t.Fatalf("errorFromUpstream = %+v, want status 403/code/request id preserved", got)
	}
	payload := streamErrorPayload(err)
	inner, _ := payload["error"].(map[string]any)
	if inner["request_id"] != "req-123" {
		t.Fatalf("stream error payload request_id = %#v", inner["request_id"])
	}
}

func TestErrorFromUpstreamAssignsStableTransportCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{name: "transport", err: errors.New("dial failed"), code: "upstream_transport_error"},
		{name: "timeout", err: context.DeadlineExceeded, code: "upstream_timeout"},
		{name: "cancel", err: context.Canceled, code: "client_closed_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := errorFromUpstream(tc.err).Code; got != tc.code {
				t.Fatalf("code = %q, want %q", got, tc.code)
			}
		})
	}
}

type timeoutTestError struct{}

func (timeoutTestError) Error() string   { return "timed out" }
func (timeoutTestError) Timeout() bool   { return true }
func (timeoutTestError) Temporary() bool { return true }

func TestErrorFromUpstreamMapsIdleAndNetTimeouts(t *testing.T) {
	for _, err := range []error{fmt.Errorf("wrapped: %w", timeoutTestError{}), fmt.Errorf("wrapped: %w", context.DeadlineExceeded), fmt.Errorf("wrapped: %w", &upstream.IdleTimeoutError{})} {
		got := errorFromUpstream(err)
		if got.Status != http.StatusGatewayTimeout || got.Type != "timeout" {
			t.Fatalf("errorFromUpstream(%v) = %+v, want 504 timeout", err, got)
		}
	}
}

func TestWriteErrorForwardsOnlyRetryHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, &apiError{
		Status:  http.StatusTooManyRequests,
		Message: "slow down",
		Type:    "rate_limit_error",
		ResponseHeaders: http.Header{
			"X-Should-Retry": {"true"},
			"Retry-After":    {"3"},
			"Retry-After-Ms": {"3000"},
			"X-Request-Id":   {"req-1"},
			"Authorization":  {"must-not-leak"},
		},
	})
	for _, name := range []string{"X-Should-Retry", "Retry-After", "Retry-After-Ms", "X-Request-Id"} {
		if w.Header().Get(name) == "" {
			t.Errorf("missing retry header %s", name)
		}
	}
	if got := w.Header().Get("Authorization"); got != "" {
		t.Errorf("unsafe header leaked: Authorization=%q", got)
	}
}

func TestResolveTransportNilAccountUsesGlobalDefault(t *testing.T) {
	s := &Server{}

	if got := s.resolveTransport(&store.Settings{UpstreamTransport: "sse"}, nil, nil, "ws"); got != "sse" {
		t.Errorf("global sse with ws client = %q, want sse", got)
	}
	if got := s.resolveTransport(&store.Settings{UpstreamTransport: "passthrough"}, nil, nil, "ws"); got != "websocket" {
		t.Errorf("explicit passthrough with ws client = %q, want websocket", got)
	}
	if got := s.resolveTransport(&store.Settings{UpstreamTransport: "passthrough"}, nil, nil, "sse"); got != "sse" {
		t.Errorf("explicit passthrough with sse client = %q, want sse", got)
	}
	// A key-level transport overrides the global default.
	key := &store.APIKey{Transport: "auto"}
	if got := s.resolveTransport(&store.Settings{UpstreamTransport: "sse"}, key, nil, "ws"); got != "websocket-cached" {
		t.Errorf("key auto with ws client = %q, want websocket-cached", got)
	}
}
