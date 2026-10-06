package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"pi-gateway/internal/piwire"
	"pi-gateway/internal/upstream"
)

// Auto mode keeps the selected native account. Check its current account/group
// policies for the summary model before a second request, and account for the
// actual model used rather than the client's native compaction model.
func (s *Server) prepareSummaryModel(ctx context.Context, p *prepared, model string) error {
	denied, err := s.store.DisabledModelsForAccount(ctx, p.Account.ID, p.Key)
	if err != nil {
		return err
	}
	for _, blocked := range denied {
		if blocked == model {
			return &upstream.FailureError{Status: 403, Code: "model_disabled", Message: "The configured compaction model is disabled for the selected account or groups.", OperationDenied: true}
		}
	}
	// Once auto has selected the summary operation, retries must reserve accounts
	// for that model and send summaries directly. Response rules see the actual
	// model too, while the global mode remains unchanged.
	p.CompactionMode = "on"
	p.Built.Model = model
	p.Built.Body.Set("model", model)
	p.Built.JSON, err = json.Marshal(p.Built.Body)
	if err != nil {
		return err
	}
	if p.Usage != nil {
		p.Usage.model = model
		p.Usage.status = 0 // Summary headers supersede a native endpoint rejection.
	}
	p.RuleContext["model"] = model
	if p.Recorder != nil {
		p.Recorder.SetRoute(p.Account.ID, p.Account.Name, model, p.SessionID, p.Transport)
	}
	return nil
}

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
