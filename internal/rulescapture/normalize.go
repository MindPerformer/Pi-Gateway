// Package rulescapture builds offline samples without routing, credentials or writes.
package rulescapture

import (
	"encoding/json"
	"fmt"

	"pi-gateway/internal/piwire"
	"pi-gateway/internal/store"
)

// NormalizeRequest is shared with live admission, before any request rules.
func NormalizeRequest(client map[string]any, rt *store.Settings, sessionHint string) (*piwire.BuiltRequest, error) {
	return piwire.BuildRequest(client, piwire.BuildOptions{DefaultModel: rt.DefaultModel, ModelMappings: rt.ModelMappings, DefaultReasoningEffort: rt.ReasoningEffort, DefaultReasoningSummary: rt.ReasoningSummary, SessionID: sessionHint})
}

func UnwrapWS(payload []byte) (map[string]any, error) {
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("frame is not valid JSON: %w", err)
	}
	if decoded == nil {
		return nil, fmt.Errorf("frame must be a JSON object")
	}
	if t, ok := decoded["type"].(string); ok {
		if t != "response.create" && t != "" {
			return nil, fmt.Errorf("unsupported frame type %q (expected response.create)", t)
		}
		delete(decoded, "type")
	}
	return decoded, nil
}

// AggregatePayload mirrors the live stream:false response envelope extraction.
func AggregatePayload(terminal map[string]any) map[string]any {
	if response, ok := terminal["response"].(map[string]any); ok {
		return response
	}
	return terminal
}
