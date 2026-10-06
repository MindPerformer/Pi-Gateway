package rulesruntime

import (
	"context"
	"testing"

	"pi-gateway/internal/rules"
	"pi-gateway/internal/store"
)

func TestDefaultImageExclusionIsAnEditableRule(t *testing.T) {
	ctx := context.Background()
	rows, err := ConvertLegacy(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := DecodeSnapshot(&store.RuleSetSnapshot{Rules: rows})
	if err != nil {
		t.Fatal(err)
	}
	var filter rules.Rule
	for _, definition := range definitions {
		if definition.ID == "default-drop-image-generation" {
			filter = definition
		}
	}
	if !filter.Enabled || len(filter.Actions) != 1 || filter.Actions[0].Type != "drop_tools" {
		t.Fatalf("missing default rule: %+v", filter)
	}
	image := map[string]any{"type": "image_generation"}
	input := &rules.Input{Body: map[string]any{"tools": []any{image, map[string]any{"type": "namespace", "name": "images", "tools": []any{image}}, map[string]any{"type": "function", "name": "image_generation"}}, "tool_choice": image}, Trace: true}
	engine, err := rules.Compile([]rules.Rule{filter})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Apply(ctx, rules.PhaseRequest, input)
	if err != nil {
		t.Fatal(err)
	}
	body := result.Body.(map[string]any)
	if len(body["tools"].([]any)) != 1 || body["tool_choice"] != nil || len(result.Traces) == 0 {
		t.Fatalf("exclusion failed: %+v", result)
	}
	filter.Enabled = false
	engine, err = rules.Compile([]rules.Rule{filter})
	if err != nil {
		t.Fatal(err)
	}
	result, err = engine.Apply(ctx, rules.PhaseRequest, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Body.(map[string]any)["tools"].([]any)) != 3 {
		t.Fatal("disabled rule still filtered tools")
	}
}
