package upstream

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
	"sync/atomic"
	"testing"
	"time"

	"pi-gateway/internal/piwire"

	"github.com/gorilla/websocket"
)

// The API Responses WebSocket envelope preserves business fields and their order,
// but must not inherit HTTP-only options from Pi's normalized SSE request body.
func TestBuildWSFrameMatchesResponsesAPIEnvelope(t *testing.T) {
	const piBody = `{"model":"gpt-5.1-codex","store":false,"stream":true,"instructions":"You are a helpful assistant.","input":[],"text":{"verbosity":"low"},"include":["reasoning.encrypted_content"],"tool_choice":"auto","parallel_tool_calls":true}`
	for _, tc := range []struct {
		name  string
		extra string
	}{
		{name: "no_session"},
		{name: "with_session", extra: `,"prompt_cache_key":"sess-1"`},
		{name: "cached_continuation", extra: `,"prompt_cache_key":"sess-1","previous_response_id":"resp-1"`},
		{name: "optional_fields", extra: `,"temperature":0.2,"service_tier":"priority","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"reasoning":{"effort":"high","summary":"auto"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(piBody[:len(piBody)-1] + tc.extra + "}")
			original := bytes.Clone(body)
			frame, err := buildWSFrame(body)
			reportTestFailure(t, err != nil, func() {
				t.Fatal(err)
			})
			var want, got map[string]any
			{
				err := json.Unmarshal(original, &want)
				reportTestFailure(t, err != nil, func() {
					t.Fatal(err)
				})
			}
			want["type"] = "response.create"
			delete(want, "stream")
			delete(want, "background")
			{
				err := json.Unmarshal(frame, &got)
				reportTestFailure(t, err != nil, func() {
					t.Fatal(err)
				})
			}
			// Assert the complete business-field set as well as every value.
			reportTestFailure(t, !reflect.DeepEqual(got, want), func() {
				t.Fatalf("Responses API envelope mismatch\n got: %#v\nwant: %#v", got, want)
			})
			reportTestFailure(t, got["store"] != false, func() {
				t.Fatalf("WS must retain store:false: %s", frame)
			})
			wantBody := bytes.Replace(original, []byte(`,"stream":true`), nil, 1)
			reportTestFailure(t, !bytes.Equal(frame, append([]byte(`{"type":"response.create",`), wantBody[1:]...)), func() {
				t.Fatalf("body field order changed: %s", frame)
			})
			reportTestFailure(t, !bytes.Equal(body, original), func() {
				t.Fatal("WS framing mutated the HTTP body")
			})
		})
	}
}

func TestBuildWSFramePreservesBuildRequestAndEnforcePiShape(t *testing.T) {
	for _, session := range []string{"", "wire-session"} {
		t.Run("session="+session, func(t *testing.T) {
			client := map[string]any{
				"model": "gpt-5.1-codex", "input": []any{}, "stream": false, "store": true,
				"instructions": "preserve client instructions", "text": map[string]any{"verbosity": "high"}, "parallel_tool_calls": false,
			}
			built, err := piwire.BuildRequest(client, piwire.BuildOptions{SessionID: session})
			reportTestFailure(t, err != nil, func() {
				t.Fatal(err)
			})
			// Keep middleware shape enforcement intact before transport framing.
			built.Body.Set("stream", false)
			built.Body.Set("store", true)
			built.Body.Set("temperature", 0.2)
			built.Body.Set("metadata", map[string]any{"unsupported": true})
			piwire.EnforcePiShape(built.Body)
			httpBody, err := json.Marshal(built.Body)
			reportTestFailure(t, err != nil, func() {
				t.Fatal(err)
			})
			reportTestFailure(t, !bytes.Equal(httpBody, built.JSON), func() {
				t.Fatalf("EnforcePiShape regression: %s != %s", httpBody, built.JSON)
			})
			frame, err := buildWSFrame(httpBody)
			reportTestFailure(t, err != nil, func() {
				t.Fatal(err)
			})
			want := map[string]any{"type": "response.create", "model": "gpt-5.1-codex", "input": []any{}, "store": false}
			for _, field := range []string{"instructions", "text", "parallel_tool_calls"} {
				want[field] = client[field]
			}
			if session != "" {
				want["prompt_cache_key"] = session
			}
			var got map[string]any
			{
				err := json.Unmarshal(frame, &got)
				reportTestFailure(t, err != nil, func() {
					t.Fatal(err)
				})
			}
			reportTestFailure(t, !reflect.DeepEqual(got, want), func() {
				t.Fatalf("gateway envelope mismatch\n got: %#v\nwant: %#v", got, want)
			})
			reportTestFailure(t, !bytes.Equal(httpBody, built.JSON), func() {
				t.Fatal("WS framing changed the HTTP SSE body")
			})
		})
	}
}

func TestSSEBodyRetainsStreamAfterWSFraming(t *testing.T) {
	built, err := piwire.BuildRequest(map[string]any{"model": "gpt-5.1-codex", "input": []any{}, "stream": false}, piwire.BuildOptions{})
	reportTestFailure(t, err != nil, func() {
		t.Fatal(err)
	})
	original := bytes.Clone(built.JSON)
	{
		_, err := buildWSFrame(built.JSON)
		reportTestFailure(t, err != nil, func() {
			t.Fatal(err)
		})
	}
	captured := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(r.Body)
		reportTestFailure(t, err != nil, func() {
			t.Errorf("read HTTP SSE body: %v", err)
		})
		captured <- body
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()
	c := testClient(t, server.URL)
	{
		_, err := c.Stream(context.Background(), &Request{Transport: "sse", Body: built.JSON}, func(*Event) error { return nil })
		reportTestFailure(t, err != nil, func() {
			t.Fatal(err)
		})
	}
	var sent []byte
	select {
	case sent = <-captured:
	default:
		t.Fatal("no HTTP SSE request captured")
	}
	reportTestFailure(t, !bytes.Equal(sent, original), func() {
		t.Fatalf("HTTP body changed\n got: %s\nwant: %s", sent, original)
	})
	var body map[string]any
	{
		err := json.Unmarshal(sent, &body)
		reportTestFailure(t, err != nil, func() {
			t.Fatal(err)
		})
	}
	reportTestFailure(t, body["stream"] != true, func() {
		t.Fatalf("HTTP SSE must retain stream:true: %s", sent)
	})
	{
		_, ok := body["type"]
		reportTestFailure(t, ok, func() {
			t.Fatalf("WS envelope type leaked into HTTP SSE body: %s", sent)
		})
	}
	// A pre-send handshake 503 permits auto fallback. It must use the original
	// HTTP body, not the filtered WS frame or an object mutated during framing.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := c.Stream(ctx, &Request{Transport: "auto", Body: built.JSON}, func(*Event) error { return nil })
	if err != nil || result == nil || result.Transport != "sse" || result.FallbackFrom != "websocket" {
		t.Fatalf("expected safe SSE fallback: result=%+v error=%v", result, err)
	}
	select {
	case sent := <-captured:
		if !bytes.Equal(sent, original) {
			t.Fatalf("fallback changed HTTP body\n got: %s\nwant: %s", sent, original)
		}
	default:
		t.Fatal("no fallback SSE request captured")
	}
}

// A strict Responses API peer must reject HTTP-only fields instead of silently
// accepting the legacy Codex envelope. The same request must still work as SSE.
func TestWSStrictResponsesPeerAcceptsNormalizedRequest(t *testing.T) {
	for _, transport := range []string{"websocket", "websocket-cached", "auto"} {
		t.Run(transport, func(t *testing.T) {
			frames := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !websocket.IsWebSocketUpgrade(r) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode SSE body: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if body["stream"] != true || body["store"] != false {
						t.Errorf("SSE lost required HTTP fields: %#v", body)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_sse\",\"status\":\"completed\"}}\n\n")
					return
				}
				socket, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					t.Errorf("upgrade: %v", err)
					return
				}
				defer socket.Close()
				var frame map[string]any
				if err := socket.ReadJSON(&frame); err != nil {
					t.Errorf("read WS frame: %v", err)
					return
				}
				frames <- frame
				for _, field := range []string{"stream", "background"} {
					if _, present := frame[field]; present {
						_ = socket.WriteJSON(map[string]any{
							"type": "error", "status": http.StatusBadRequest,
							"error": map[string]any{"type": "invalid_request_error", "code": "unknown_parameter", "param": field, "message": "Unknown parameter: '" + field + "'."},
						})
						return
					}
				}
				if frame["type"] != "response.create" || frame["store"] != false {
					t.Errorf("invalid WS envelope: %#v", frame)
					return
				}
				_ = socket.WriteJSON(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_ws", "status": "in_progress"}})
				_ = socket.WriteJSON(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_ws", "status": "completed", "output": []any{}}})
			}))
			defer server.Close()
			client := testClient(t, server.URL)
			built, err := piwire.BuildRequest(map[string]any{"model": "gpt-5.1-codex", "input": "hello"}, piwire.BuildOptions{})
			if err != nil {
				t.Fatal(err)
			}
			original := bytes.Clone(built.JSON)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := client.Stream(ctx, &Request{Transport: "sse", Body: built.JSON}, func(*Event) error { return nil }); err != nil {
				t.Fatalf("same request failed as SSE: %v", err)
			}
			if _, err := client.Stream(ctx, &Request{Transport: transport, Body: built.JSON}, func(*Event) error { return nil }); err != nil {
				t.Fatalf("strict Responses WS rejected normalized request: %v", err)
			}
			select {
			case frame := <-frames:
				if frame["model"] != "gpt-5.1-codex" {
					t.Fatalf("model changed: %#v", frame)
				}
			default:
				t.Fatal("no WS request received; SSE fallback must not hide protocol errors")
			}
			if !bytes.Equal(built.JSON, original) {
				t.Fatal("WS framing changed the original HTTP body")
			}
		})
	}
}

func TestBuildWSFrameFiltersOnlyTopLevelTransportFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "empty", want: `{"type":"response.create"}`},
		{name: "whitespace", body: " \n\t ", want: `{"type":"response.create"}`},
		{name: "empty_object", body: " { \n } ", want: `{"type":"response.create"}`},
		{name: "only_transport_fields", body: `{"stream":true,"background":false}`, want: `{"type":"response.create"}`},
		{name: "false_and_null", body: `{"stream":false,"background":null,"store":false,"generate":false,"extension":null}`, want: `{"type":"response.create","store":false,"generate":false,"extension":null}`},
		{name: "transport_values", body: `{"stream":{"nested":true},"background":[1],"model":"m"}`, want: `{"type":"response.create","model":"m"}`},
		{name: "escaped_key", body: `{"str\u0065am":true,"model":"m"}`, want: `{"type":"response.create","model":"m"}`},
		{name: "reserved_type", body: `{"model":"m","type":"response.cancel","input":[],"type":null}`, want: `{"type":"response.create","model":"m","input":[]}`},
		{name: "nested_fields", body: `{"model":"m","stream":true,"input":[{"text":"stream,background,type","stream":false}],"tools":[{"parameters":{"properties":{"stream":{"type":"boolean"},"background":{"type":"string"}}}}],"extension":{"z":1,"a":2}}`, want: `{"type":"response.create","model":"m","input":[{"text":"stream,background,type","stream":false}],"tools":[{"parameters":{"properties":{"stream":{"type":"boolean"},"background":{"type":"string"}}}}],"extension":{"z":1,"a":2}}`},
		{name: "number_precision", body: `{"stream":true,"extension":9007199254740993,"nested":{"z":1e100,"a":-0},"previous_response_id":"resp_1"}`, want: `{"type":"response.create","extension":9007199254740993,"nested":{"z":1e100,"a":-0},"previous_response_id":"resp_1"}`},
		{name: "duplicate_transport_fields", body: `{"stream":true,"model":"m","stream":false,"background":false,"background":true}`, want: `{"type":"response.create","model":"m"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			original := bytes.Clone(body)
			got, err := buildWSFrame(body)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("WS frame\n got: %s\nwant: %s", got, tc.want)
			}
			if !bytes.Equal(body, original) {
				t.Fatal("framing mutated the source body")
			}
		})
	}
}

func TestBuildWSFrameRejectsInvalidObjects(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `1`, `"object"`, `true`, `{`, `{"stream":true,}`, `{"input":]}`, `{} {}`, `{} trailing`, `{"model":"m"} null`} {
		t.Run(body, func(t *testing.T) {
			if frame, err := buildWSFrame([]byte(body)); err == nil {
				t.Fatalf("invalid request produced frame: %s", frame)
			}
		})
	}
}

type wsDiagnosticSink struct {
	NopSink
	status  int
	kind    string
	inbound []byte
}

func (s *wsDiagnosticSink) OnResponseHeaders(status int, _ http.Header) {
	s.status = status
}

func (s *wsDiagnosticSink) OnFrame(direction, kind, _ string, raw []byte, _ map[string]any) {
	if direction == "in" {
		s.kind, s.inbound = kind, bytes.Clone(raw)
	}
}

func TestWSInvalidRequestPreservesDiagnosticsWithoutFallback(t *testing.T) {
	const payload = `{"type":"error","status":400,"request_id":"req-invalid","error":{"type":"invalid_request_error","code":"unknown_parameter","param":"unsupported_option","message":"Unknown parameter: 'unsupported_option'."}}`
	for _, phase := range []string{"handshake", "first_frame"} {
		t.Run(phase, func(t *testing.T) {
			var handshakes, creates, posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts.Add(1)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				handshakes.Add(1)
				if phase == "handshake" {
					w.Header().Set("X-Request-Id", "req-invalid")
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, payload)
					return
				}
				socket, err := (&websocket.Upgrader{}).Upgrade(w, r, http.Header{"X-Request-Id": []string{"req-invalid"}})
				if err != nil {
					t.Errorf("upgrade: %v", err)
					return
				}
				defer socket.Close()
				if _, _, err := socket.ReadMessage(); err != nil {
					t.Errorf("read create: %v", err)
					return
				}
				creates.Add(1)
				_ = socket.WriteMessage(websocket.TextMessage, []byte(payload))
			}))
			defer server.Close()
			client := testClient(t, server.URL)
			sink := &wsDiagnosticSink{}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var delivered [][]byte
			_, err := client.Stream(ctx, &Request{Transport: "auto", Body: []byte(`{"model":"test","stream":true,"store":false,"unsupported_option":true}`), Sink: sink}, func(event *Event) error {
				delivered = append(delivered, bytes.Clone(event.Raw))
				return nil
			})
			var failure *FailureError
			if !errors.As(err, &failure) {
				t.Fatalf("expected structured upstream failure, got %v", err)
			}
			if failure.Status != 400 || failure.Code != "unknown_parameter" || failure.Message != "Unknown parameter: 'unsupported_option'." || failure.RequestID != "req-invalid" || string(failure.Payload) != payload {
				t.Fatalf("lost invalid-request diagnostics: %+v", failure)
			}
			if string(sink.inbound) != payload {
				t.Fatalf("capture lost original status/code/message/param: %s", sink.inbound)
			}
			var transport *transportError
			if !errors.As(err, &transport) || transport.requestNotSent != (phase == "handshake") {
				t.Fatalf("incorrect send phase classification: %v", err)
			}
			wantStatus, wantKind, wantCreates := 400, KindHTTPError, int32(0)
			if phase == "first_frame" {
				wantStatus, wantKind, wantCreates = 101, "ws_frame", 1
				if len(delivered) != 1 || string(delivered[0]) != payload {
					t.Fatalf("error frame was altered or delivered more than once: %q", delivered)
				}
			} else if len(delivered) != 0 {
				t.Fatalf("HTTP handshake rejection was treated as a WS frame: %q", delivered)
			}
			if sink.status != wantStatus || sink.kind != wantKind || creates.Load() != wantCreates || handshakes.Load() != 1 || posts.Load() != 0 {
				t.Fatalf("unexpected retry or phase: status=%d kind=%s creates=%d handshakes=%d posts=%d", sink.status, sink.kind, creates.Load(), handshakes.Load(), posts.Load())
			}
		})
	}
}
