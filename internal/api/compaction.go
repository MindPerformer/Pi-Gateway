package api

import (
	"fmt"
	"strings"

	"pi-gateway/internal/piwire"
)

// Detect the operation before user rules run: dropping the trigger must not
// accidentally turn a compaction request into another model/tool execution.
func detectCompaction(body map[string]any, direct bool) (bool, *apiError) {
	compact := direct
	if input, ok := body["input"].([]any); ok {
		for i, raw := range input {
			item, _ := raw.(map[string]any)
			if item["type"] == "compaction_trigger" {
				if i != len(input)-1 {
					return false, compactInputError("compaction_trigger must be the final input item")
				}
				compact = true
			}
		}
	}
	if compact && body["previous_response_id"] != nil && body["previous_response_id"] != "" {
		return false, compactInputError("Compaction requires complete input without previous_response_id; send the full conversation window")
	}
	return compact, nil
}

func compactInputError(message string) *apiError {
	return &apiError{Status: 400, Message: message, Type: "invalid_request_error", Code: "invalid_compaction_request"}
}

// The compact endpoint has its own schema. In particular it does not accept
// the create endpoint's stream, store, tool_choice, tools or reasoning fields.
// Instructions are restored before policy evaluation, so rules can still edit
// or remove them. Never restore values from the original client after policy.
func compactRequestBody(body *piwire.OrderedMap) (*piwire.OrderedMap, error) {
	model, _ := body.Get("model")
	name, ok := model.(string)
	if !ok || strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("compaction requires a nonempty model")
	}
	previous, _ := body.Get("previous_response_id")
	if previous != nil && previous != "" {
		return nil, fmt.Errorf("compaction requires complete input without previous_response_id")
	}
	input, _ := body.Get("input")
	items, ok := input.([]any)
	if !ok {
		return nil, fmt.Errorf("compaction input must be an array of conversation items")
	}
	history := make([]any, 0, len(items))
	for i, raw := range items {
		item, _ := raw.(map[string]any)
		if item["type"] == "compaction_trigger" {
			if i != len(items)-1 {
				return nil, fmt.Errorf("compaction_trigger must be the final input item")
			}
			continue
		}
		history = append(history, raw)
	}
	out := piwire.NewOrderedMap()
	out.Set("model", name)
	out.Set("input", history)
	for _, key := range []string{"instructions", "prompt_cache_key", "prompt_cache_options"} {
		if value, exists := body.Get(key); exists && value != nil {
			out.Set(key, value)
		}
	}
	return out, nil
}
