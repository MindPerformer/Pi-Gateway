package rules

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestJSONPointerOperations(t *testing.T) {
	r := rule("p", act("json_set", map[string]any{"path": "/a~1b/~0x/", "value": []any{nil, 1}, "create_parents": true}), Action{ID: "append", Type: "json_set", Params: map[string]any{"path": "/a~1b/~0x//2", "value": 2}}, Action{ID: "keep", Type: "json_set", Params: map[string]any{"path": "/a~1b/~0x//0", "value": 42, "if_exists": "keep"}}, act("json_remove", map[string]any{"paths": []any{"/a~1b/~0x//1"}}))
	res := applyTest(t, r, map[string]any{})
	assertJSON(t, res.Body, map[string]any{"a/b": map[string]any{"~x": map[string]any{"": []any{nil, 2}}}})
	for _, path := range []string{"/a/01", "/a/-", "/a/-1", "/a/+1", "/a/999999999999999999999"} {
		t.Run(path, func(t *testing.T) {
			r := rule("bad", act("json_set", map[string]any{"path": path, "value": 1}))
			res, e := compileTest(t, r).Apply(context.Background(), PhaseRequest, &Input{Body: map[string]any{"a": []any{1}}, Trace: true})
			if e == nil || res.Changed {
				t.Fatalf("invalid array pointer accepted %s %v", path, e)
			}
		})
	}
	for _, p := range []map[string]any{{"path": "/a/2", "value": 1}, {"path": "/a/x", "value": 1, "create_parents": true}, {"path": "/missing/x", "value": 1}} {
		r := rule("bad", act("json_set", p))
		if _, e := compileTest(t, r).Apply(context.Background(), PhaseRequest, &Input{Body: map[string]any{"a": nil}}); e == nil {
			t.Fatalf("invalid set accepted %v", p)
		}
	}
	r = rule("root", act("json_set", map[string]any{"path": "", "value": map[string]any{"replaced": true}}))
	res = applyTest(t, r, map[string]any{"a": 1})
	assertJSON(t, res.Body, map[string]any{"replaced": true})
}
func TestTransferCopiesAndMoves(t *testing.T) {
	r := rule("transfer", act("json_transfer", map[string]any{"source_path": "/a/0", "target_path": "/a/2", "operation": "move"}))
	res := applyTest(t, r, map[string]any{"a": []any{"first", "second", "third"}})
	assertJSON(t, pathValue(t, res.Body, "/a"), []any{"second", "third", "first"})
	r = rule("copy", act("json_transfer", map[string]any{"source": "client", "source_path": "/source", "target_path": "/target", "overwrite": false, "create_parents": true}))
	client := map[string]any{"source": map[string]any{"v": 1}}
	res, e := compileTest(t, r).Apply(context.Background(), PhaseRequest, &Input{Body: map[string]any{}, ClientBody: client})
	if e != nil {
		t.Fatal(e)
	}
	obj := pathValue(t, res.Body, "/target").(map[string]any)
	obj["v"] = 9
	assertJSON(t, client, map[string]any{"source": map[string]any{"v": 1}})
	_, e = compileTest(t, r).Apply(context.Background(), PhaseRequest, &Input{Body: map[string]any{"target": nil}, ClientBody: client})
	if e == nil {
		t.Fatal("overwrite=false ignored")
	}
}
func TestResponseProtectionAtCompileAndRuntime(t *testing.T) {
	for _, phase := range []string{PhaseResponseEvent, PhaseResponseBody} {
		for _, path := range []string{"/type", "/response/id", "/usage/input_tokens", "/response/output/0/call_id", "/output/0/id", "/sequence_number", "/status", "/error/message"} {
			t.Run(phase+path, func(t *testing.T) {
				r := rule("protected", act("json_set", map[string]any{"path": path, "value": "x"}))
				r.Phase = phase
				if _, e := Compile([]Rule{r}); e == nil {
					t.Fatal("protected write compiled")
				}
			})
		}
	}
	body := map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp-1", "status": "completed", "usage": map[string]any{"input_tokens": 3}, "output": []any{map[string]any{"id": "item-1", "type": "function_call", "call_id": "call-1", "name": "tool", "arguments": "{}"}}}}
	for _, a := range []Action{act("json_remove", map[string]any{"paths": []any{"/response"}}), act("json_set", map[string]any{"path": "/response", "value": ref("client", "/replacement")}), act("array_filter", map[string]any{"path": "/response/output", "predicate": Condition{Op: "always"}}), act("json_set", map[string]any{"path": "/response/output/0/name", "value": "other-tool"})} {
		t.Run(a.Type+fmt.Sprint(a.Params["path"]), func(t *testing.T) {
			r := rule("p", a)
			r.Phase = PhaseResponseEvent
			r.OnError = OnErrorSkipRule
			before := mustClone(body)
			res, e := compileTest(t, r).Apply(context.Background(), PhaseResponseEvent, &Input{Body: body, ClientBody: map[string]any{"replacement": map[string]any{}}, Trace: true})
			if e != nil || res.Changed || res.Dropped {
				t.Fatalf("protected parent write escaped %v %+v", e, res)
			}
			assertJSON(t, res.Body, before)
			assertJSON(t, body, before)
			if res.Traces[0].Status != "skipped" || !res.Traces[0].RolledBack {
				t.Fatal(res.Traces)
			}
		})
	}
	r := rule("allowed", act("text_replace", map[string]any{"path": "/response/output/0/arguments", "pattern": "{}", "replacement": "{\"safe\":true}"}))
	r.Phase = PhaseResponseEvent
	res := applyTest(t, r, body)
	assertJSON(t, pathValue(t, res.Body, "/response/usage"), map[string]any{"input_tokens": 3})
	if !res.Changed {
		t.Fatal("business field modification was blocked")
	}
}
func TestDropEventSafetyAndPhaseIsolation(t *testing.T) {
	r := rule("drop", act("drop_event", map[string]any{}))
	r.Phase = PhaseResponseEvent
	e := compileTest(t, r)
	for _, kind := range []string{"error", "response.error", "response.completed", "response.failed", "response.incomplete", "response.output_item.added", "response.function_call_arguments.delta", "unknown", "response.output_text.done"} {
		res, err := e.Apply(context.Background(), PhaseResponseEvent, &Input{Body: map[string]any{"type": kind}})
		if err == nil || res.Dropped {
			t.Fatalf("unsafe event dropped %s", kind)
		}
	}
	for _, kind := range []string{"response.output_text.delta", "response.refusal.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta"} {
		res, err := e.Apply(context.Background(), PhaseResponseEvent, &Input{Body: map[string]any{"type": kind, "delta": "text"}})
		if err != nil || !res.Dropped {
			t.Fatalf("safe delta not dropped %s %v", kind, err)
		}
	}
	res, err := e.Apply(context.Background(), PhaseResponseBody, &Input{Body: map[string]any{}})
	if err != nil || res.Dropped {
		t.Fatal("phase leaked")
	}
	_, err = e.Apply(context.Background(), PhaseResponseEvent, &Input{Body: map[string]any{"type": "response.output_text.delta"}, EventType: "response.completed"})
	if err == nil {
		t.Fatal("event type mismatch accepted")
	}
}
func TestRequestCredentialAndModelProtection(t *testing.T) {
	r := rule("model", act("json_set", map[string]any{"path": "/model", "value": "new"}))
	res := applyTest(t, r, map[string]any{"model": "old"})
	if res.Model != "new" {
		t.Fatal("generic model update not synchronized")
	}
	r = rule("remove", act("json_remove", map[string]any{"paths": []any{"/model"}}))
	res, err := compileTest(t, r).Apply(context.Background(), PhaseRequest, &Input{Body: map[string]any{"model": "old"}, Model: "old"})
	if err == nil || res.Model != "old" || res.Changed {
		t.Fatal("model removal allowed")
	}
	r = rule("auth", act("json_set", map[string]any{"path": "", "value": map[string]any{"api_key": "bad"}}))
	res, err = compileTest(t, r).Apply(context.Background(), PhaseRequest, &Input{Body: map[string]any{}})
	if err == nil || res.Changed {
		t.Fatal("credential insertion allowed")
	}
}
func TestTraceBudgetsDiffAndRedaction(t *testing.T) {
	actions := []Action{}
	for i := 0; i < 30; i++ {
		actions = append(actions, Action{ID: fmt.Sprint(i), Type: "json_set", Params: map[string]any{"path": "/x", "value": strings.Repeat("x", 1000) + fmt.Sprint(i)}})
	}
	r := rule("trace", actions...)
	res, e := compileTest(t, r).Apply(context.Background(), PhaseRequest, &Input{Body: map[string]any{}, Trace: true, TraceMaxBytes: 1200})
	if e != nil {
		t.Fatal(e)
	}
	if len(res.Traces) > 2 || res.TraceOmitted == 0 || len(res.Traces)+res.TraceOmitted != 30 {
		t.Fatalf("unbounded traces %d omitted %d", len(res.Traces), res.TraceOmitted)
	}
	if !strings.HasSuffix(pathValue(t, res.Body, "/x").(string), "29") {
		t.Fatal("trace budget limited execution")
	}
	res, e = compileTest(t, r).Apply(context.Background(), PhaseRequest, &Input{Body: map[string]any{}, Trace: false})
	if e != nil || len(res.Traces) != 0 || res.TraceOmitted != 0 {
		t.Fatal("trace=false retained data")
	}
	r = rule("secret", act("json_set", map[string]any{"path": "/metadata", "value": map[string]any{"password": "sensitive", "safe": "ok"}}))
	res = applyTest(t, r, map[string]any{})
	if pathValue(t, res.Traces[0].Changes[0].After, "/password") != "[REDACTED]" {
		t.Fatal("secret trace leak")
	}
	if pathValue(t, res.Body, "/metadata/password") != "sensitive" {
		t.Fatal("trace sanitization altered execution")
	}
	before, after := map[string]any{}, map[string]any{}
	for i := 0; i < 500; i++ {
		after[fmt.Sprint(i)] = i
	}
	changes, omitted := structuralDiff(before, after)
	if len(changes) != MaxTraceChanges || omitted != 500-MaxTraceChanges {
		t.Fatalf("diff budget %d/%d", len(changes), omitted)
	}
}
