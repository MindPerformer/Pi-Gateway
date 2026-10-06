package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"pi-gateway/internal/capture"
	"pi-gateway/internal/rules"
	"pi-gateway/internal/rulescapture"
	"pi-gateway/internal/store"
)

func TestLiveRuleCheckpointsPreserveStageBoundaries(t *testing.T) {
	for _, transport := range []string{"sse", "websocket"} {
		for _, downstream := range []string{"aggregate", "sse", "ws"} {
			t.Run(transport+"/"+downstream, func(t *testing.T) {
				h, _, _ := unifiedHarness(t, transport, unifiedRulesEvents)
				requestRule := unifiedRule("request", rules.PhaseRequest, 1, unifiedSet("instructions", "/instructions", "after-request"))
				eventRule := unifiedEventRule("event", "response.completed", unifiedSet("marker", "/response/marker", "after-event"))
				bodyRule := unifiedRule("body", rules.PhaseResponseBody, 1, unifiedSet("marker", "/marker", "after-body"))
				publishUnifiedRules(t, h, requestRule, eventRule, bodyRule)
				var record *store.Capture
				if downstream == "aggregate" {
					response, body, c := capturedHTTPResponse(t, h, `{"model":"gpt-5.5","instructions":"before-request","input":"hello","stream":false}`)
					if response.StatusCode != http.StatusOK || unifiedObject(t, body)["marker"] != "after-body" {
						t.Fatalf("response=%s", body)
					}
					record = c
				} else if downstream == "sse" {
					_, _, record = capturedHTTPResponse(t, h, `{"model":"gpt-5.5","instructions":"before-request","input":"hello","stream":true}`)
				} else {
					_, record = unifiedWSResponse(t, h, `{"type":"response.create","model":"gpt-5.5","instructions":"before-request","input":"hello"}`)
				}
				sample, err := rulescapture.Request(record, h.dataPlane.settings.Get())
				if err != nil {
					t.Fatal(err)
				}
				if sample.Source != "checkpoint" || sample.Input.Context["request_path"] != "/v1/responses" {
					t.Fatalf("request checkpoint=%+v", sample)
				}
				raw, _ := json.Marshal(sample.Input.Body)
				client := sample.Input.ClientBody.(map[string]any)
				expected, err := rulescapture.NormalizeRequest(client, h.dataPlane.settings.Get(), "")
				if err != nil {
					t.Fatal(err)
				}
				if !equalCaptureJSON(raw, expected.JSON) || client["instructions"] != "before-request" {
					t.Fatalf("checkpoint was not the normalized pre-rule input: %s want=%s", raw, expected.JSON)
				}
				if sample.Input.ConditionTrace {
					t.Fatal("live execution enabled condition diagnostics")
				}
				contextFrames := captureFrames(record, "internal", capture.KindRuleContext)
				if len(contextFrames) != 1 {
					t.Fatalf("response context frames=%d", len(contextFrames))
				}
				frames := rulescapture.EventFrames(record)
				if len(frames) != len(unifiedRulesEvents) {
					t.Fatalf("duplicated events=%d", len(frames))
				}
				event, err := rulescapture.Event(record, frames[len(frames)-1])
				if err != nil {
					t.Fatal(err)
				}
				payload := event.Input.Body.(map[string]any)["response"].(map[string]any)
				if _, exists := payload["marker"]; exists {
					t.Fatal("raw event was replaced by a modified event")
				}
				if event.Input.Context["account_id"] == nil || event.Input.Context["original_model"] == nil || event.Input.Context["request_method"] == nil {
					t.Fatalf("missing recorded response facts: %v", event.Input.Context)
				}
				if downstream == "aggregate" {
					body, err := rulescapture.ResponseBody(context.Background(), record, nil)
					if err != nil {
						t.Fatal(err)
					}
					if body.Source != "checkpoint" || body.Input.Body.(map[string]any)["marker"] != "after-event" {
						t.Fatalf("body boundary=%+v", body)
					}
				} else {
					empty, _ := rules.Compile(nil)
					if _, err := rulescapture.ResponseBody(context.Background(), record, empty); err == nil {
						t.Fatal("streaming event masqueraded as aggregate")
					}
				}
			})
		}
	}
}

func TestBlockedRequestStillHasPreRuleCheckpoint(t *testing.T) {
	h, _, calls := unifiedHarness(t, "sse", unifiedRulesEvents)
	block := unifiedRule("block", rules.PhaseRequest, 1, unifiedAction("reject", "reject_request", map[string]any{"status": 403, "message": "no routing"}))
	publishUnifiedRules(t, h, block)
	h.dataPlane.accounts = nil
	response, _, record := capturedHTTPResponse(t, h, `{"model":"gpt-5.5","input":[],"stream":false}`)
	if response.StatusCode != 403 || calls.Load() != 0 || record.AccountID != 0 {
		t.Fatal("blocked request routed")
	}
	sample, err := rulescapture.Request(record, h.dataPlane.settings.Get())
	if err != nil {
		t.Fatal(err)
	}
	if sample.Source != "checkpoint" || sample.Input.Context["api_key_id"] == nil || sample.Input.Context["account_id"] != nil {
		t.Fatalf("blocked input=%+v", sample)
	}
}
