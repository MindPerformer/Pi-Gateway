package api

import (
	"net/http"
	"reflect"
	"testing"

	"pi-gateway/internal/rules"
)

// Inspect the actual upstream payload after rules and transport framing. This
// catches fields lost by either normalization, final enforcement or bridging.
func TestPreviewOptionsAcrossTransportsAndRules(t *testing.T) {
	for _, transport := range []string{"sse", "websocket", "websocket-cached", "auto-fallback-sse"} {
		for _, downstream := range []string{"http", "ws"} {
			// auto selects websocket-cached for downstream WS, without SSE fallback.
			if transport == "auto-fallback-sse" && downstream == "ws" {
				continue
			}
			t.Run(downstream+"/"+transport, func(t *testing.T) {
				h, observed, _ := unifiedHarness(t, transport, unifiedRulesEvents)
				publishUnifiedRules(t, h, unifiedRule("options", rules.PhaseRequest, 10,
					unifiedSet("edit-instructions", "/instructions", "from rule"),
					unifiedSet("edit-verbosity", "/text/verbosity", "low"),
					unifiedSet("unsupported", "/metadata", map[string]any{"must": "be dropped"})))
				request := `{"model":"gpt-5.5","input":"hello","stream":false,"store":true,"instructions":"from client","text":{"verbosity":"high","format":{"type":"text"}},"parallel_tool_calls":false,"future_option":{"flag":false,"nullable":null},"temperature":0.5}`
				if downstream == "ws" {
					events, _ := unifiedWSResponse(t, h, request)
					if unifiedObject(t, events[len(events)-1])["type"] != "response.completed" {
						t.Fatalf("request failed: %s", events[len(events)-1])
					}
				} else {
					response, body, _ := capturedHTTPResponse(t, h, request)
					if response.StatusCode != http.StatusOK {
						t.Fatalf("status=%d body=%s", response.StatusCode, body)
					}
				}
				_, raw := observed.get()
				wsUpstream := transport == "websocket" || transport == "websocket-cached"
				if wsUpstream {
					observed.mu.Lock()
					raw = append([]byte(nil), observed.wsFrame...)
					observed.mu.Unlock()
				}
				payload := unifiedObject(t, raw)
				if payload["instructions"] != "from rule" || payload["parallel_tool_calls"] != false || payload["stream"] != true || payload["store"] != false {
					t.Fatalf("client options or route constraints changed: %s", raw)
				}
				if !reflect.DeepEqual(payload["text"], map[string]any{"verbosity": "low", "format": map[string]any{"type": "text"}}) || !reflect.DeepEqual(payload["future_option"], map[string]any{"flag": false, "nullable": nil}) {
					t.Fatalf("nested options lost: %s", raw)
				}
				for _, field := range []string{"metadata", "temperature", "previous_response_id"} {
					if _, exists := payload[field]; exists {
						t.Errorf("unsupported HTTP field or rule-reintroduced field %s reached upstream: %s", field, raw)
					}
				}
				if wsUpstream && payload["type"] != "response.create" {
					t.Fatalf("missing WS envelope: %s", raw)
				}
				if !wsUpstream && payload["type"] != nil {
					t.Fatalf("WS envelope leaked into HTTP: %s", raw)
				}
			})
		}
	}
}
