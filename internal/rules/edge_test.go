package rules

import (
	"context"
	"strings"
	"testing"
)

func TestBoundaryRegressions(t *testing.T) {
	t.Run("null-rule-list", func(t *testing.T) {
		if _, e := ParseRules([]byte("null")); e == nil {
			t.Fatal("null list accepted")
		}
	})
	t.Run("pointer-depth", func(t *testing.T) {
		r := rule("deep", act("json_set", map[string]any{"path": strings.Repeat("/x", MaxDepth+1), "value": 1, "create_parents": true}))
		if _, e := Compile([]Rule{r}); e == nil {
			t.Fatal("unbounded pointer accepted")
		}
	})
	t.Run("result-depth", func(t *testing.T) {
		r := rule("deep", act("json_set", map[string]any{"path": strings.Repeat("/x", MaxDepth), "value": map[string]any{"too_deep": 1}, "create_parents": true}))
		res, e := compileTest(t, r).Apply(context.Background(), PhaseRequest, &Input{Body: map[string]any{}})
		if e == nil || res.Changed {
			t.Fatal("nested result budget not enforced")
		}
	})
	t.Run("keep-does-not-resolve-unused-value", func(t *testing.T) {
		r := rule("keep", act("json_set", map[string]any{"path": "/x", "if_exists": "keep", "value": ref("client", "/absent")}))
		res := applyTest(t, r, map[string]any{"x": nil})
		if res.Changed {
			t.Fatal("existing null not kept")
		}
	})
	t.Run("whitespace-text-pattern", func(t *testing.T) {
		r := rule("space", act("text_replace", map[string]any{"path": "/x", "pattern": " ", "replacement": "_"}))
		res := applyTest(t, r, map[string]any{"x": "a b"})
		if pathValue(t, res.Body, "/x") != "a_b" {
			t.Fatal("whitespace pattern rejected")
		}
	})
	t.Run("original-model-context", func(t *testing.T) {
		r := rule("original", act("json_set", map[string]any{"path": "/original", "value": ref("context", "/original_model")}))
		r.Phase = PhaseResponseBody
		res, e := compileTest(t, r).Apply(context.Background(), PhaseResponseBody, &Input{Body: map[string]any{}, Model: "current", Context: map[string]any{"original_model": "original"}})
		if e != nil || pathValue(t, res.Body, "/original") != "original" {
			t.Fatal("original model lost", e)
		}
	})
	t.Run("event-root-remains-object", func(t *testing.T) {
		r := rule("root", act("json_set", map[string]any{"path": "", "value": "invalid-event"}))
		r.Phase = PhaseResponseEvent
		res, e := compileTest(t, r).Apply(context.Background(), PhaseResponseEvent, &Input{Body: map[string]any{}, EventType: "response.output_text.delta"})
		if e == nil || res.Changed {
			t.Fatal("nonobject event accepted")
		}
	})
	t.Run("source-path-error", func(t *testing.T) {
		r := rule("source", act("json_transfer", map[string]any{"source": "context", "source_path": "/account_id", "target_path": "/x"}))
		_, e := Compile([]Rule{r})
		if ve, ok := e.(*ValidationError); !ok || ve.Path != "/0/actions/0/params/source_path" {
			t.Fatal(e)
		}
	})
	t.Run("catalog-phases-detached", func(t *testing.T) {
		c := Catalog()
		c.Actions[6].Phases[0] = "mutated"
		c.Conditions[0].Phases[0] = "mutated"
		c.ValueExpressions[0].Phases[0] = "mutated"
		compileTest(t, rule("valid", act("json_set", map[string]any{"path": "/v", "value": 1})))
		if Catalog().Phases[0] != PhaseRequest {
			t.Fatal("catalog mutated validator")
		}
	})
}
