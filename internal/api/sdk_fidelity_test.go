package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"pi-gateway/internal/upstream"
)

func TestPrepareSeparatesWireAndInternalSession(t *testing.T) {
	h := newHarness(t, sseUpstream(&upstreamCapture{}), "sse")
	cases := []struct {
		name, body, header, hint, want string
	}{
		{name: "absent", body: `{"model":"test","input":[]}`},
		{name: "body", body: `{"model":"test","input":[],"prompt_cache_key":"body-session"}`, want: "body-session"},
		{name: "body wins", body: `{"model":"test","input":[],"prompt_cache_key":"body-session"}`, header: "session_id", hint: "header-session", want: "body-session"},
		{name: "empty body key", body: `{"model":"test","input":[],"prompt_cache_key":""}`},
	}
	for _, header := range []string{"session_id", "session-id", "x-session-id", "x-client-request-id"} {
		cases = append(cases, struct{ name, body, header, hint, want string }{
			name: header, body: `{"model":"test","input":[]}`, header: header, hint: "hint-session", want: "hint-session",
		})
	}
	var generated []string
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			r.Header.Set("Authorization", "Bearer "+h.key)
			if tc.header != "" {
				r.Header.Set(tc.header, tc.hint)
			}
			p, apiErr := h.dataPlane.prepare(context.Background(), r, []byte(tc.body), "sse")
			if apiErr != nil {
				t.Fatal(apiErr)
			}
			defer p.Release()
			if p.WireSessionID != tc.want || p.SessionID == "" {
				t.Fatalf("wire/internal session = %q/%q, want wire %q and nonempty internal", p.WireSessionID, p.SessionID, tc.want)
			}
			if tc.want == "" {
				if _, err := uuid.Parse(p.SessionID); err != nil {
					t.Fatalf("generated internal session is not UUID: %v", err)
				}
				generated = append(generated, p.SessionID)
			}
			req, err := h.dataPlane.newUpstreamRequest(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			if req.SessionID != p.SessionID {
				t.Fatalf("pool session = %q, want internal %q", req.SessionID, p.SessionID)
			}
			for name, headers := range map[string]http.Header{"sse": req.SSEHeaders, "ws": req.WSHeaders} {
				if got := headers.Get("x-client-request-id"); got != tc.want {
					t.Errorf("%s x-client-request-id = %q, want %q", name, got, tc.want)
				}
				if tc.want == "" {
					for _, key := range []string{"session_id", "session-id", "x-client-request-id"} {
						if headers.Get(key) != "" {
							t.Errorf("%s leaked %s = %q", name, key, headers.Get(key))
						}
					}
				}
			}
			if got := req.SSEHeaders.Get("session_id"); got != tc.want {
				t.Errorf("SSE session_id = %q, want %q", got, tc.want)
			}
			var body map[string]any
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatal(err)
			}
			cacheKey, present := body["prompt_cache_key"]
			if tc.want == "" && present || tc.want != "" && cacheKey != tc.want {
				t.Errorf("prompt_cache_key = %#v (present %t), wire = %q", cacheKey, present, tc.want)
			}
		})
	}
	if len(generated) != 2 || generated[0] == generated[1] {
		t.Fatalf("independent no-session requests share pool key: %v", generated)
	}
}

func TestNewUpstreamRequestUsesConfiguredPlatformAndExplicitTimeout(t *testing.T) {
	h := newHarness(t, sseUpstream(&upstreamCapture{}), "sse")
	cfg := h.baseConfig
	cfg.Upstream.StainlessOS = "MacOS"
	cfg.Upstream.StainlessArch = "arm64"
	cfg.Upstream.StainlessRuntime = "node"
	cfg.Upstream.StainlessRuntimeVersion = "v22.14.0"
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	r.Header.Set("Authorization", "Bearer "+h.key)
	p, apiErr := h.dataPlane.prepare(context.Background(), r, []byte(`{"model":"test","input":[]}`), "sse")
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	defer p.Release()
	for _, timeout := range []int{0, -1, 45} {
		t.Run(strconv.Itoa(timeout), func(t *testing.T) {
			cfg.Upstream.RequestTimeoutSeconds = timeout
			req, err := h.dataPlane.newUpstreamRequest(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{
				"Accept":                      "application/json",
				"X-Stainless-OS":              "MacOS",
				"X-Stainless-Arch":            "arm64",
				"X-Stainless-Runtime":         "node",
				"X-Stainless-Runtime-Version": "v22.14.0",
			}
			for name, value := range want {
				if got := req.SSEHeaders.Get(name); got != value {
					t.Errorf("%s = %q, want %q", name, got, value)
				}
			}
			wantTimeout := ""
			if timeout > 0 {
				wantTimeout = strconv.Itoa(timeout)
			}
			if got := req.SSEHeaders.Get("X-Stainless-Timeout"); got != wantTimeout {
				t.Errorf("timeout = %q, want %q", got, wantTimeout)
			}
			if got := req.WSHeaders.Get("originator"); got != "pi" {
				t.Errorf("WS originator = %q, want pi", got)
			}
		})
	}
}

func testRetryHeaders() http.Header {
	return http.Header{
		"X-Should-Retry": {"false"},
		"Retry-After":    {"7"},
		"Retry-After-Ms": {"7000"},
		"X-Request-Id":   {"req-retry"},
	}
}

func assertRetryHeaders(t *testing.T, headers http.Header) {
	t.Helper()
	for name, values := range testRetryHeaders() {
		if got := headers.Get(name); got != values[0] {
			t.Errorf("%s = %q, want %q", name, got, values[0])
		}
	}
	for _, name := range []string{"Set-Cookie", "Authorization", "X-Upstream-Secret"} {
		if got := headers.Get(name); got != "" {
			t.Errorf("unsafe header %s = %q", name, got)
		}
	}
}

func TestHTTPErrorPreservesRetryHeadersWithoutCapture(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			t.Run(fmt.Sprintf("stream=%t/status=%d", stream, status), func(t *testing.T) {
				h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					for name, values := range testRetryHeaders() {
						w.Header()[name] = values
					}
					w.Header().Set("Set-Cookie", "session=secret")
					w.Header().Set("Authorization", "Bearer secret")
					w.Header().Set("X-Upstream-Secret", "secret")
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{"error":{"message":"retry later","code":"rate_limit_exceeded"}}`)
				}), "sse")
				rt := h.dataPlane.settings.Get()
				rt.CaptureEnabled = false
				if err := h.dataPlane.settings.Set(context.Background(), rt); err != nil {
					t.Fatal(err)
				}
				r, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":"test","input":[],"stream":%t}`, stream)))
				r.Header.Set("Authorization", "Bearer "+h.key)
				resp, err := http.DefaultClient.Do(r)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != status {
					t.Errorf("status = %d, want %d", resp.StatusCode, status)
				}
				assertRetryHeaders(t, resp.Header)
				var payload map[string]any
				if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				inner, _ := payload["error"].(map[string]any)
				if inner["request_id"] != "req-retry" {
					t.Errorf("missing error request_id: %#v", payload)
				}
			})
		}
	}
}

func TestSSEMidstreamFailureKeepsRetryMetadata(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, values := range testRetryHeaders() {
			w.Header()[name] = values
		}
		w.Header().Set("X-Upstream-Secret", "secret")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, deltaSSE)
		w.(http.Flusher).Flush()
		// EOF before a terminal event yields a local structured stream error.
	}), "sse")
	r, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/responses", strings.NewReader(`{"model":"test","input":[],"stream":true}`))
	r.Header.Set("Authorization", "Bearer "+h.key)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertRetryHeaders(t, resp.Header)
	reader := upstream.ParseSSE(resp.Body)
	if _, err := reader.Next(); err != nil {
		t.Fatal(err)
	}
	event, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if event.Type != "error" {
		t.Fatalf("event = %#v, want error", event)
	}
	assertRetryPayload(t, event.Data)
}

func assertRetryPayload(t *testing.T, payload map[string]any) {
	t.Helper()
	encoded, _ := json.Marshal(payload["headers"])
	var headers map[string]string
	if err := json.Unmarshal(encoded, &headers); err != nil {
		t.Fatalf("decode retry metadata: %v, payload=%#v", err, payload)
	}
	want := map[string]string{}
	for name, values := range testRetryHeaders() {
		want[strings.ToLower(name)] = values[0]
	}
	if !reflect.DeepEqual(headers, want) {
		t.Fatalf("retry metadata = %#v, want %#v", headers, want)
	}
}

func TestWebSocketErrorCarriesRetryMetadata(t *testing.T) {
	for _, upstreamEvent := range []bool{false, true} {
		t.Run(fmt.Sprintf("upstreamEvent=%t", upstreamEvent), func(t *testing.T) {
			h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for name, values := range testRetryHeaders() {
					w.Header()[name] = values
				}
				w.Header().Set("X-Upstream-Secret", "secret")
				if upstreamEvent {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"retry later\"}}\n\n")
				} else {
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = io.WriteString(w, `{"error":{"message":"retry later","code":"rate_limit_exceeded"}}`)
				}
			}), "sse")
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(h.server.URL, "http")+"/v1/responses", http.Header{"Authorization": {"Bearer " + h.key}})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteJSON(map[string]any{"type": "response.create", "model": "test", "input": []any{}}); err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := conn.ReadJSON(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["type"] != "error" {
				t.Fatalf("payload = %#v, want error", payload)
			}
			assertRetryPayload(t, payload)
		})
	}
}

func TestResponseHeaderSinkReplacesHeadersAcrossAttempts(t *testing.T) {
	p := &prepared{}
	sink := p.sink()
	first := testRetryHeaders()
	first.Set("Authorization", "secret")
	sink.OnResponseHeaders(429, first)
	first.Set("Retry-After", "mutated")
	if p.ResponseHeaders.Get("Retry-After") != "7" || p.ResponseHeaders.Get("Authorization") != "" {
		t.Fatalf("headers were not safely cloned: %#v", p.ResponseHeaders)
	}
	sink.OnResponseHeaders(200, http.Header{"X-Request-Id": {"next"}})
	if len(p.ResponseHeaders) != 1 || p.ResponseHeaders.Get("X-Request-Id") != "next" {
		t.Fatalf("stale retry headers survived fallback: %#v", p.ResponseHeaders)
	}
}

func TestCancellationStillMapsTo499WithRetryMetadata(t *testing.T) {
	got := errorFromUpstream(fmt.Errorf("wrapped: %w", context.Canceled), testRetryHeaders())
	if got.Status != 499 || got.Type != "cancelled" {
		t.Fatalf("cancel = %+v, want 499 cancelled", got)
	}
	for _, payload := range []map[string]any{streamErrorPayload(errors.New("network failed"), testRetryHeaders()), errorFramePayload(got)} {
		assertRetryPayload(t, payload)
	}
}

// Exercise the actual local route against a strict stub, never a real account.
func TestResponsesCurlExampleAndStringInput(t *testing.T) {
	for name, body := range map[string]string{
		"curl message array": `{"model":"test-model","input":[{"role":"user","content":[{"type":"input_text","text":"Hi"}]}],"stream":true,"store":false}`,
		"string shorthand":   `{"model":"test-model","input":"Hi","stream":true,"store":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			capture := &upstreamCapture{}
			h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, "read body", 400)
					return
				}
				var payload map[string]any
				if json.Unmarshal(raw, &payload) != nil {
					http.Error(w, "invalid JSON", 400)
					return
				}
				if _, ok := payload["input"].([]any); !ok {
					http.Error(w, "input must be an array", 400)
					return
				}
				r.Body = io.NopCloser(strings.NewReader(string(raw)))
				sseUpstream(capture)(w, r)
			}), "sse")
			req, err := http.NewRequest(http.MethodPost, h.server.URL+"/v1/responses", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+h.key)
			req.Header.Set("Content-Type", "application/json")
			resp, err := h.server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			out, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(out), "response.completed") {
				t.Fatalf("curl route: status=%d body=%s", resp.StatusCode, out)
			}
			_, raw := capture.get()
			var got, want map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"Hi"}]}]}`), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got["input"], want["input"]) || got["stream"] != true || got["store"] != false {
				t.Fatalf("unexpected upstream payload: %s", raw)
			}
		})
	}
}
