package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"pi-gateway/internal/capture"
	"pi-gateway/internal/store"
	"pi-gateway/internal/upstream"
)

func TestResponsesCaptureHTTPRejections(t *testing.T) {
	const upstreamBody = `{"error":{"message":"denied <upstream>","code":"policy_denied","upstream_only":true}}`
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden} {
		for _, stream := range []bool{true, false} {
			for _, includeHeaders := range []bool{true, false} {
				t.Run(fmt.Sprintf("status=%d/stream=%t/headers=%t", status, stream, includeHeaders), func(t *testing.T) {
					observed := &upstreamCapture{}
					h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, _ := io.ReadAll(r.Body)
						observed.set(r, body)
						setCaptureTestHeaders(w)
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(status)
						_, _ = io.WriteString(w, upstreamBody)
					}), "sse")
					h.dataPlane.cfg.Capture.IncludeHeaders = includeHeaders
					requestBody := fmt.Sprintf(`{"model":"test","input":"hello","stream":%t}`, stream)
					resp, body, record := capturedHTTPResponse(t, h, requestBody)
					if resp.StatusCode != status || record.Status != status || record.Outcome != store.OutcomeError {
						t.Fatalf("client/upstream status = %d/%d, outcome=%s", resp.StatusCode, record.Status, record.Outcome)
					}
					assertCapturedClientHeaders(t, record, resp, includeHeaders)
					assertCapturedJSONBody(t, record, body, "error")
					assertCapturedRequestHops(t, record, observed, requestBody, includeHeaders)
					frames := captureFrames(record, "in", upstream.KindHTTPError)
					if len(frames) != 1 || !equalCaptureJSON(captureFrameBytes(t, frames[0]), []byte(upstreamBody)) {
						t.Fatalf("upstream rejection body missing: %+v", frames)
					}
					if bytes.Contains(body, []byte("upstream_only")) || equalCaptureJSON(body, []byte(upstreamBody)) {
						t.Fatalf("client error was substituted with upstream payload: %s", body)
					}
					if !bytes.Contains(body, []byte(`"request_id":"req-capture"`)) {
						t.Fatalf("request id missing from client payload: %s", body)
					}
					if len(captureFrames(record, capture.DirClientOut, store.KindSSEEvent)) != 0 || record.ResponseText != "" {
						t.Fatal("HTTP rejection was recorded as a started SSE stream")
					}
				})
			}
		}
	}
}

func TestResponsesCaptureSuccess(t *testing.T) {
	for _, stream := range []bool{true, false} {
		for _, includeHeaders := range []bool{true, false} {
			t.Run(fmt.Sprintf("stream=%t/headers=%t", stream, includeHeaders), func(t *testing.T) {
				observed := &upstreamCapture{}
				h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					observed.set(r, body)
					setCaptureTestHeaders(w)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, deltaSSE+terminalSSE)
				}), "sse")
				h.dataPlane.cfg.Capture.IncludeHeaders = includeHeaders
				requestBody := fmt.Sprintf(`{"model":"test","input":"hello","stream":%t}`, stream)
				resp, body, record := capturedHTTPResponse(t, h, requestBody)
				if resp.StatusCode != http.StatusOK || record.Status != http.StatusOK || record.Outcome != store.OutcomeOK {
					t.Fatalf("client/upstream status = %d/%d, outcome=%s", resp.StatusCode, record.Status, record.Outcome)
				}
				assertCapturedClientHeaders(t, record, resp, includeHeaders)
				assertCapturedRequestHops(t, record, observed, requestBody, includeHeaders)
				if len(captureFrames(record, "in", store.KindSSEEvent)) != 2 {
					t.Fatal("upstream events were lost or duplicated")
				}
				if stream {
					if string(body) != deltaSSE+terminalSSE {
						t.Fatalf("stream changed: %s", body)
					}
					assertCapturedSSEBody(t, record, body, 2)
				} else {
					assertCapturedJSONBody(t, record, body, "response")
					var payload map[string]any
					if err := json.Unmarshal(body, &payload); err != nil || payload["id"] != "resp_test_1" || payload["response"] != nil {
						t.Fatalf("aggregated client payload changed: %s, err=%v", body, err)
					}
					if len(captureFrames(record, capture.DirClientOut, store.KindSSEEvent)) != 0 || record.ResponseText != "" {
						t.Fatal("aggregated response polluted the client SSE text")
					}
				}
			})
		}
	}
}

func TestResponsesCaptureEmptyUpstream(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				setCaptureTestHeaders(w)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
			}), "sse")
			resp, body, record := capturedHTTPResponse(t, h, fmt.Sprintf(`{"model":"test","input":[],"stream":%t}`, stream))
			if resp.StatusCode != http.StatusBadGateway || record.Status != http.StatusOK || record.Outcome != store.OutcomeError {
				t.Fatalf("client/upstream status = %d/%d, outcome=%s", resp.StatusCode, record.Status, record.Outcome)
			}
			assertCapturedClientHeaders(t, record, resp, true)
			assertCapturedJSONBody(t, record, body, "error")
			if len(captureFrames(record, "in", store.KindSSEEvent)) != 0 || record.ResponseText != "" {
				t.Fatal("empty upstream invented an event or client SSE text")
			}
		})
	}
}

func TestResponsesCaptureStreamErrorsNotDuplicated(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "local EOF error", body: deltaSSE},
		{name: "upstream error", body: deltaSSE + "data: {\"type\":\"error\",\"error\":{\"code\":\"server_error\",\"message\":\"failed\"}}\n\n"},
		{name: "upstream failed response", body: deltaSSE + "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failed\",\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"failed\"}}}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				setCaptureTestHeaders(w)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.body)
			}), "sse")
			resp, body, record := capturedHTTPResponse(t, h, `{"model":"test","input":[],"stream":true}`)
			if resp.StatusCode != http.StatusOK || record.Outcome != store.OutcomeError {
				t.Fatalf("committed stream status=%d outcome=%s", resp.StatusCode, record.Outcome)
			}
			assertCapturedClientHeaders(t, record, resp, true)
			assertCapturedSSEBody(t, record, body, 2)
		})
	}
}

func TestResponsesCaptureTokenRefreshFailure(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			var upstreamCalls, refreshCalls atomic.Int32
			h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamCalls.Add(1)
				http.Error(w, "unexpected upstream request", http.StatusInternalServerError)
			}), "sse")
			// Reject the refresh tunnel locally; this proxy never dials any host.
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				refreshCalls.Add(1)
				if r.Method != http.MethodConnect {
					t.Errorf("refresh proxy method = %s, want CONNECT", r.Method)
				}
				http.Error(w, "refresh blocked by local test proxy", http.StatusForbidden)
			}))
			t.Cleanup(proxy.Close)
			account, err := h.store.GetAccount(context.Background(), h.accountID)
			if err != nil {
				t.Fatal(err)
			}
			account.ExpiresAt = time.Now().Add(-time.Hour).UnixMilli()
			account.ProxyURL = proxy.URL
			account.OAuthClientID = "local-test-client"
			if err := h.store.UpdateAccount(context.Background(), account); err != nil {
				t.Fatal(err)
			}
			resp, body, record := capturedHTTPResponse(t, h, fmt.Sprintf(`{"model":"test","input":[],"stream":%t}`, stream))
			if resp.StatusCode != http.StatusBadGateway || record.Status != 0 || record.Outcome != store.OutcomeError {
				t.Fatalf("refresh failure client/upstream status=%d/%d outcome=%s body=%s", resp.StatusCode, record.Status, record.Outcome, body)
			}
			assertCapturedClientHeaders(t, record, resp, true)
			assertCapturedJSONBody(t, record, body, "error")
			if refreshCalls.Load() != 1 || upstreamCalls.Load() != 0 {
				t.Fatalf("refresh/upstream calls=%d/%d, want 1/0; body=%s", refreshCalls.Load(), upstreamCalls.Load(), body)
			}
			if record.RequestBody != "" || len(record.ResponseHeaders) != 0 || record.ResponseText != "" {
				t.Fatal("refresh failure invented upstream traffic or SSE text")
			}
			account, err = h.store.GetAccount(context.Background(), h.accountID)
			if err != nil || account.Status != store.AccountStatusExpired {
				t.Fatalf("refresh failure was not persisted: account=%+v err=%v", account, err)
			}
		})
	}
}

func TestResponsesCaptureWSTokenRefreshFailure(t *testing.T) {
	var upstreamCalls, refreshCalls atomic.Int32
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		http.Error(w, "unexpected upstream request", http.StatusInternalServerError)
	}), "sse")
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		if r.Method != http.MethodConnect {
			t.Errorf("refresh proxy method = %s, want CONNECT", r.Method)
		}
		http.Error(w, "refresh blocked by local test proxy", http.StatusForbidden)
	}))
	t.Cleanup(proxy.Close)
	account, err := h.store.GetAccount(context.Background(), h.accountID)
	if err != nil {
		t.Fatal(err)
	}
	account.ExpiresAt = time.Now().Add(-time.Hour).UnixMilli()
	account.ProxyURL = proxy.URL
	account.OAuthClientID = "local-test-client"
	if err := h.store.UpdateAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(h.server.URL, "http")+"/v1/responses", http.Header{"Authorization": {"Bearer " + h.key}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	const request = `{"type":"response.create","model":"test","input":[]}`
	if err := client.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	messageType, body, err := client.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.TextMessage || !bytes.Contains(body, []byte(`"type":"error"`)) {
		t.Fatalf("unexpected WS error reply: type=%d body=%s", messageType, body)
	}
	record := waitForResponseCapture(t, h)
	frames := captureFrames(record, capture.DirClientOut, store.KindWSFrame)
	if len(frames) != 1 || frames[0].Type != "error" || frames[0].Bytes != len(body) || !equalCaptureJSON(captureFrameBytes(t, frames[0]), body) {
		t.Fatalf("captured WS error differs from actual reply: frames=%+v body=%s", frames, body)
	}
	requestFrames := captureFrames(record, capture.DirClientIn, store.KindWSFrame)
	if len(requestFrames) != 1 || !equalCaptureJSON(captureFrameBytes(t, requestFrames[0]), []byte(request)) {
		t.Fatalf("original WS request missing: %+v", requestFrames)
	}
	if record.Status != 0 || record.Outcome != store.OutcomeError || record.ResponseText != "" || record.RequestBody != "" {
		t.Fatalf("WS refresh failure changed capture semantics: %+v", record)
	}
	if refreshCalls.Load() != 1 || upstreamCalls.Load() != 0 {
		t.Fatalf("refresh/upstream calls=%d/%d, want 1/0", refreshCalls.Load(), upstreamCalls.Load())
	}
	if len(captureFrames(record, capture.DirClientOut, store.KindHandshakeResponse)) != 0 {
		t.Fatal("per-exchange failure invented an HTTP handshake response")
	}
}

func TestCaptureJSONResponseUsesWriteErrorPayload(t *testing.T) {
	for _, apiErr := range []*apiError{nil, {
		Status: http.StatusForbidden, Message: "denied <request>", RequestID: "req-capture",
		ResponseHeaders: http.Header{"Retry-After": {"5"}, "Set-Cookie": {"upstream-secret"}},
	}} {
		want := httptest.NewRecorder()
		writeError(want, apiErr)
		rec := capture.New(nil, capture.Options{IncludeHeaders: true})
		got := httptest.NewRecorder()
		writeError(captureJSONResponse(got, rec, "error"), apiErr)
		if got.Code != want.Code || !reflect.DeepEqual(got.Result().Header, want.Result().Header) || got.Body.String() != want.Body.String() {
			t.Fatalf("capture changed writeError output: got=%+v want=%+v", got, want)
		}
		assertCapturedClientHeaders(t, rec.Capture(), got.Result(), true)
		assertCapturedJSONBody(t, rec.Capture(), got.Body.Bytes(), "error")
	}
}

func TestResponsesCaptureUnsupportedStreaming(t *testing.T) {
	rec := capture.New(nil, capture.Options{IncludeHeaders: true})
	response := httptest.NewRecorder()
	w := struct{ http.ResponseWriter }{response} // Deliberately no http.Flusher.
	outcome, err := (&Server{}).serveSSE(w, context.Background(), &prepared{Recorder: rec}, nil)
	if err == nil || outcome != store.OutcomeError || response.Code != http.StatusInternalServerError {
		t.Fatalf("unsupported streaming status=%d outcome=%s err=%v", response.Code, outcome, err)
	}
	assertCapturedClientHeaders(t, rec.Capture(), response.Result(), true)
	assertCapturedJSONBody(t, rec.Capture(), response.Body.Bytes(), "error")
}

func TestCaptureJSONResponseSnapshotsEncoderBuffer(t *testing.T) {
	rec := capture.New(nil, capture.Options{IncludeHeaders: true})
	response := httptest.NewRecorder()
	writer := captureJSONResponse(response, rec, "response")
	writer.WriteHeader(http.StatusOK)
	const payload = `{"ok":true}`
	raw := []byte(payload)
	if n, err := writer.Write(raw); n != len(raw) || err != nil {
		t.Fatalf("write: n=%d err=%v", n, err)
	}
	for i := range raw {
		raw[i] = 'x'
	}
	frames := captureFrames(rec.Capture(), capture.DirClientOut, store.KindHTTPResponse)
	if len(frames) != 1 || string(captureFrameBytes(t, frames[0])) != payload {
		t.Fatalf("captured payload borrowed the encoder buffer: %+v", frames)
	}
}

func TestCaptureJSONResponseRetainsOnlyWrittenBytes(t *testing.T) {
	rec := capture.New(nil, capture.Options{IncludeHeaders: true})
	w := &shortCaptureWriter{ResponseRecorder: httptest.NewRecorder()}
	writer := captureJSONResponse(w, rec, "response")
	writer.WriteHeader(http.StatusOK)
	raw := []byte(`{"ok":true}`)
	n, err := writer.Write(raw)
	if n != 5 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("writer result changed: n=%d err=%v", n, err)
	}
	for i := range raw {
		raw[i] = 'x'
	}
	frames := captureFrames(rec.Capture(), capture.DirClientOut, store.KindHTTPResponse)
	if len(frames) != 1 || string(captureFrameBytes(t, frames[0])) != `{"ok"` || frames[0].Bytes != 5 {
		t.Fatalf("capture retained unwritten bytes or borrowed buffer: %+v", frames)
	}
}

type shortCaptureWriter struct{ *httptest.ResponseRecorder }

func (w *shortCaptureWriter) Write(raw []byte) (int, error) {
	n, _ := w.ResponseRecorder.Write(raw[:5])
	return n, io.ErrClosedPipe
}

func setCaptureTestHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Request-Id", "req-capture")
	w.Header().Set("X-Should-Retry", "false")
	w.Header().Set("Retry-After", "7")
	w.Header().Add("Retry-After-Ms", "125")
	w.Header().Add("Retry-After-Ms", "250")
	w.Header().Set("Set-Cookie", "session=upstream-private-credential-123456789")
	w.Header().Set("Authorization", "Bearer upstream-private-credential-123456789")
	w.Header().Set("X-Upstream-Only", "not-forwarded")
}

func capturedHTTPResponse(t *testing.T, h *testHarness, requestBody string) (*http.Response, []byte, *store.Capture) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.server.URL+"/v1/responses", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Only", "original-client-value")
	resp, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return resp, body, waitForResponseCapture(t, h)
}

func waitForResponseCapture(t *testing.T, h *testHarness) *store.Capture {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		records, _, err := h.store.ListCaptures(context.Background(), store.CaptureFilter{Limit: 2, IncludePayloads: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(records) == 1 {
			return records[0]
		}
		if len(records) > 1 {
			t.Fatalf("request created %d captures", len(records))
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for request capture")
		case <-ticker.C:
		}
	}
}

func assertCapturedClientHeaders(t *testing.T, record *store.Capture, resp *http.Response, includeHeaders bool) {
	t.Helper()
	frames := captureFrames(record, capture.DirClientOut, store.KindHandshakeResponse)
	if len(frames) != 1 {
		t.Fatalf("client header frames=%d, want exactly one", len(frames))
	}
	var data struct {
		Status  int            `json:"status"`
		Headers []store.Header `json:"headers"`
	}
	raw := captureFrameBytes(t, frames[0])
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if data.Status != resp.StatusCode {
		t.Fatalf("captured client status=%d actual=%d", data.Status, resp.StatusCode)
	}
	if !includeHeaders {
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if _, ok := fields["headers"]; ok {
			t.Fatalf("disabled headers were recorded: %s", raw)
		}
		return
	}
	got := http.Header{}
	for _, header := range data.Headers {
		got.Add(header.Name, header.Value)
	}
	want := resp.Header.Clone()
	// net/http adds these transport headers after the application commits.
	want.Del("Date")
	want.Del("Content-Length")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("captured response headers=%v actual application headers=%v", got, want)
	}
	for _, name := range []string{"Set-Cookie", "Authorization", "X-Upstream-Only"} {
		if resp.Header.Get(name) != "" || got.Get(name) != "" {
			t.Fatalf("upstream-only header %s was forwarded", name)
		}
	}
	if record.Truncated {
		t.Fatal("small test response was unexpectedly truncated")
	}
}

func assertCapturedJSONBody(t *testing.T, record *store.Capture, body []byte, eventType string) {
	t.Helper()
	frames := captureFrames(record, capture.DirClientOut, store.KindHTTPResponse)
	if len(frames) != 1 || frames[0].Type != eventType || frames[0].Bytes != len(body) {
		t.Fatalf("client JSON frames=%+v, actual body=%s", frames, body)
	}
	// Persistence may compact RawMessage JSON; compare the JSON payload and keep
	// the original byte count (including Encoder's newline) as the wire metric.
	if !equalCaptureJSON(captureFrameBytes(t, frames[0]), body) {
		t.Fatalf("captured client payload=%s actual=%s", captureFrameBytes(t, frames[0]), body)
	}
	if record.ResponseText != "" {
		t.Fatal("JSON body changed SSE-only ResponseText")
	}
	var headerSeq int
	for _, frame := range captureFrames(record, capture.DirClientOut, store.KindHandshakeResponse) {
		headerSeq = frame.Seq
	}
	if frames[0].Seq <= headerSeq {
		t.Fatal("JSON body was captured before its response headers")
	}
}

func assertCapturedRequestHops(t *testing.T, record *store.Capture, observed *upstreamCapture, requestBody string, includeHeaders bool) {
	t.Helper()
	clientFrames := captureFrames(record, capture.DirClientIn, store.KindRequestBody)
	if len(clientFrames) != 1 || !equalCaptureJSON(captureFrameBytes(t, clientFrames[0]), []byte(requestBody)) {
		t.Fatalf("client request body changed: %+v", clientFrames)
	}
	actualHeaders, actualBody := observed.get()
	if record.RequestBody != string(actualBody) || equalCaptureJSON(actualBody, []byte(requestBody)) {
		t.Fatalf("upstream normalized request missing: recorded=%s actual=%s client=%s", record.RequestBody, actualBody, requestBody)
	}
	if actualHeaders.Get("X-Client-Only") != "" {
		t.Fatal("client-only header leaked to upstream")
	}
	if includeHeaders {
		if len(record.RequestHeaders) == 0 || len(record.ResponseHeaders) == 0 {
			t.Fatal("upstream hop headers missing")
		}
		for _, header := range record.RequestHeaders {
			if strings.EqualFold(header.Name, "Authorization") {
				if header.Value == actualHeaders.Get("Authorization") || !strings.Contains(header.Value, "...") {
					t.Fatalf("upstream credential was not redacted: %q", header.Value)
				}
			} else if !containsCaptureHeader(actualHeaders.Values(header.Name), header.Value) {
				t.Fatalf("captured upstream request header %s=%q differs from wire", header.Name, header.Value)
			}
		}
	} else if len(record.RequestHeaders) != 0 || len(record.ResponseHeaders) != 0 {
		t.Fatal("disabled upstream headers were recorded")
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{harnessClientKey, actualHeaders.Get("Authorization"), "upstream-private-credential-123456789"} {
		if secret != "" && bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("capture contains unredacted credential %q", secret)
		}
	}
}

const harnessClientKey = "sk-pi-test"

func containsCaptureHeader(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func assertCapturedSSEBody(t *testing.T, record *store.Capture, body []byte, count int) {
	t.Helper()
	frames := captureFrames(record, capture.DirClientOut, store.KindSSEEvent)
	if len(frames) != count || record.ResponseText != string(body) || len(captureFrames(record, capture.DirClientOut, store.KindHTTPResponse)) != 0 {
		t.Fatalf("client SSE duplicated or changed: frames=%d want=%d text=%q body=%q", len(frames), count, record.ResponseText, body)
	}
	reader := upstream.ParseSSE(bytes.NewReader(body))
	for _, frame := range frames {
		event, err := reader.Next()
		if err != nil || event.Type != frame.Type || !equalCaptureJSON(captureFrameBytes(t, frame), event.Raw) {
			t.Fatalf("captured SSE frame=%+v actual=%+v err=%v", frame, event, err)
		}
	}
	if _, err := reader.Next(); err != io.EOF {
		t.Fatalf("unexpected extra client event: %v", err)
	}
}

func captureFrames(record *store.Capture, dir, kind string) []store.Frame {
	var frames []store.Frame
	for _, frame := range record.ResponseFrames {
		if frame.Dir == dir && frame.Kind == kind {
			frames = append(frames, frame)
		}
	}
	return frames
}

func captureFrameBytes(t *testing.T, frame store.Frame) []byte {
	t.Helper()
	if frame.Data == nil {
		return []byte(frame.Text)
	}
	raw, err := json.Marshal(frame.Data)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func equalCaptureJSON(a, b []byte) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}
