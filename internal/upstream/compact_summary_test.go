package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pi-gateway/internal/egress"
)

func TestSummaryCompactionFallbackAndExpansion(t *testing.T) {
	for _, direct := range []bool{true, false} {
		t.Run(map[bool]string{true: "direct", false: "trigger"}[direct], func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if strings.HasSuffix(r.URL.Path, "/compact") {
					w.WriteHeader(403)
					io.WriteString(w, `{"error":{"message":"This ChatPass credential is not authorized for the requested operation."}}`)
					return
				}
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if body["model"] != "test" || body["stream"] != true || body["store"] != false || body["tools"] != nil || body["previous_response_id"] != nil {
					t.Errorf("bad summary request: %+v", body)
				}
				if !strings.Contains(body["instructions"].(string), "Do not answer the task") {
					t.Error("missing summary guard")
				}
				items := body["input"].([]any)
				if len(items) != 3 || items[0].(map[string]any)["role"] != "user" || items[1].(map[string]any)["role"] != "assistant" {
					t.Error("history not preserved")
				}
				io.WriteString(w, `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Keep the user goal and remaining work."}]}],"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120}}}`+"\n\n")
			}))
			defer server.Close()
			key := bytes.Repeat([]byte{42}, 32)
			c := New(Config{SSEURL: server.URL + "/responses", CompactionKey: key}, egress.NewFactory(egress.Options{}))
			defer c.Close()
			req := &Request{Compact: true, CompactDirect: direct, ClientKeyID: 7, Body: []byte(`{"model":"test","instructions":"task instructions","input":[{"role":"user","content":"goal"},{"role":"assistant","content":"progress"}]}`)}
			var terminal map[string]any
			_, err := c.Stream(context.Background(), req, func(e *Event) error {
				if e.Type == "response.output_text.delta" {
					t.Error("summary leaked as answer")
				}
				if e.Type == EventResponseCompleted {
					terminal = e.Data["response"].(map[string]any)
				}
				return nil
			})
			if err != nil || calls != 2 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if direct && terminal["object"] != "response.compaction" {
				t.Fatalf("bad envelope: %+v", terminal)
			}
			item := terminal["output"].([]any)[0].(map[string]any)
			encrypted := item["encrypted_content"].(string)
			if strings.Contains(encrypted, "remaining work") || !strings.HasPrefix(encrypted, summaryPrefix) {
				t.Fatal("summary not encrypted")
			}
			next, _ := json.Marshal(map[string]any{"model": "test", "input": []any{item, map[string]any{"role": "user", "content": "continue"}}})
			// A fresh client with the same persisted key can read the item.
			reopened := New(Config{CompactionKey: key}, egress.NewFactory(egress.Options{}))
			defer reopened.Close()
			expanded, err := reopened.ExpandCompactionBody(next, 7)
			if err != nil || !bytes.Contains(expanded, []byte("remaining work")) || bytes.Contains(expanded, []byte(summaryPrefix)) {
				t.Fatalf("expansion=%s err=%v", expanded, err)
			}
			if _, err := reopened.ExpandCompactionBody(next, 8); err == nil {
				t.Fatal("another API key decrypted summary")
			}
			item["encrypted_content"] = summaryPrefix + "invalid"
			tampered, _ := json.Marshal(map[string]any{"input": []any{item}})
			if _, err := reopened.ExpandCompactionBody(tampered, 7); err == nil {
				t.Fatal("accepted tampered item")
			}
		})
	}
}

func TestCompactionFallbackScope(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 405, 429, 500, 501} {
		f := &FailureError{Status: status, Message: "unrelated failure"}
		want := status == 404 || status == 405 || status == 501
		if compactFallbackAllowed(f) != want {
			t.Errorf("status=%d", status)
		}
	}
}

func TestSummaryCompactionEmptyOrFailedOutput(t *testing.T) {
	for _, terminal := range []string{
		`{"type":"response.completed","response":{"status":"completed","output":[]}}`,
		`{"type":"response.incomplete","response":{"status":"incomplete","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}]}}`,
		`{"type":"response.failed","response":{"status":"failed","error":{"code":"usage_limit_reached","message":"quota exhausted"}}}`,
	} {
		t.Run(terminal, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/compact") {
					w.WriteHeader(404)
					return
				}
				io.WriteString(w, "data: "+terminal+"\n\n")
			}))
			defer server.Close()
			c := testClient(t, server.URL+"/responses")
			events := 0
			_, err := c.Stream(context.Background(), &Request{Compact: true, Body: []byte(`{"model":"test","input":[]}`)}, func(*Event) error { events++; return nil })
			if err == nil || events != 0 {
				t.Fatalf("events=%d err=%v", events, err)
			}
		})
	}
}
