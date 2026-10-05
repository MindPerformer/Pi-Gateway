package rules

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAllConditions(t *testing.T) {
	body := map[string]any{"text": "Hello\nWorld", "n": 3, "nil": nil, "array": []any{"a", 2, nil}, "obj": map[string]any{"key": true}}
	cases := []struct {
		name string
		c    Condition
		want bool
	}{
		{"always", Condition{Op: "always"}, true},
		{"all", Condition{Op: "all", Conditions: []Condition{{Op: "exists", Path: "/text"}, {Op: "eq", Path: "/n", Value: 3}}}, true},
		{"any", Condition{Op: "any", Conditions: []Condition{{Op: "exists", Path: "/missing"}, {Op: "eq", Path: "/n", Value: 3}}}, true},
		{"not", Condition{Op: "not", Conditions: []Condition{{Op: "exists", Path: "/missing"}}}, true},
		{"eq", Condition{Op: "eq", Path: "/nil", Value: nil, ValuePresent: true}, true},
		{"ne", Condition{Op: "ne", Path: "/n", Value: "3"}, true},
		{"exists", Condition{Op: "exists", Path: "/nil"}, true},
		{"not_exists", Condition{Op: "not_exists", Path: "/missing"}, true},
		{"contains-text", Condition{Op: "contains", Path: "/text", Value: "Hello"}, true},
		{"contains-array", Condition{Op: "contains", Path: "/array", Value: 2}, true},
		{"contains-object", Condition{Op: "contains", Path: "/obj", Value: "key"}, true},
		{"not_contains", Condition{Op: "not_contains", Path: "/array", Value: "b"}, true},
		{"starts_with", Condition{Op: "starts_with", Path: "/text", Value: "Hello"}, true},
		{"ends_with", Condition{Op: "ends_with", Path: "/text", Value: "World"}, true},
		{"in", Condition{Op: "in", Path: "/n", Value: []any{2, 3, 4}}, true},
		{"not_in", Condition{Op: "not_in", Path: "/nil", Value: []any{1, 2}}, true},
		{"regex", Condition{Op: "regex", Path: "/text", Value: "^hello.world$", CaseInsensitive: true, DotAll: true}, true},
		{"not_regex", Condition{Op: "not_regex", Path: "/text", Value: "^hello"}, true},
		{"multiline", Condition{Op: "regex", Path: "/text", Value: "^World$", Multiline: true}, true},
		{"gt", Condition{Op: "gt", Path: "/n", Value: 2}, true},
		{"gte", Condition{Op: "gte", Path: "/n", Value: 3}, true},
		{"lt", Condition{Op: "lt", Path: "/n", Value: 4}, true},
		{"lte", Condition{Op: "lte", Path: "/n", Value: 3}, true},
		{"type", Condition{Op: "type", Path: "/array", Value: "array"}, true},
		{"missing-is-not-null", Condition{Op: "eq", Path: "/missing", Value: nil, ValuePresent: true}, false},
		{"missing-ne-is-false", Condition{Op: "ne", Path: "/missing", Value: nil, ValuePresent: true}, false},
		{"wrong-type-negation-false", Condition{Op: "not_contains", Path: "/n", Value: 3}, false},
		{"wrong-type-numeric-false", Condition{Op: "gt", Path: "/text", Value: 2}, false},
		{"json-encoding", Condition{Op: "regex", Path: "/obj", Encoding: "json", Value: `"key":true`}, true},
		{"text-not-serialized", Condition{Op: "regex", Path: "/obj", Value: `key`}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := rule("c", act("json_set", map[string]any{"path": "/matched", "value": true}))
			r.When = tc.c
			res := applyTest(t, r, body)
			_, got, _ := pointerGet(res.Body, "/matched")
			if got != tc.want {
				t.Fatalf("matched %v want %v", got, tc.want)
			}
		})
	}
}
func TestSourcesAndExpressions(t *testing.T) {
	r := rule("sources", act("json_set", map[string]any{"path": "/fromClient", "value": ref("client", "/original")}), Action{ID: "literal", Type: "json_set", Params: map[string]any{"path": "/literal", "value": map[string]any{"$literal": map[string]any{"$ref": "not evaluated"}}}}, Action{ID: "context", Type: "json_set", Params: map[string]any{"path": "/context", "value": ref("context", "/api_key_id")}})
	r.When = Condition{Op: "eq", Source: "client", Path: "/original", Value: ref("current", "/same")}
	res, e := compileTest(t, r).Apply(context.Background(), PhaseRequest, &Input{Body: map[string]any{"same": map[string]any{"v": 1}}, ClientBody: map[string]any{"original": map[string]any{"v": 1}}, Context: map[string]any{"api_key_id": 7}})
	if e != nil {
		t.Fatal(e)
	}
	assertJSON(t, pathValue(t, res.Body, "/fromClient"), map[string]any{"v": 1})
	assertJSON(t, pathValue(t, res.Body, "/literal"), map[string]any{"$ref": "not evaluated"})
	assertJSON(t, pathValue(t, res.Body, "/context"), 7)
	r.When = Condition{Op: "eq", Value: ref("client", "/absent")}
	res = applyTest(t, r, map[string]any{})
	if res.Changed {
		t.Fatal("missing reference matched")
	}
}
func TestNullConditionRoundTrip(t *testing.T) {
	raw := []byte(`{"op":"eq","path":"/x","value":null}`)
	var c Condition
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if !c.ValuePresent || c.Value != nil {
		t.Fatal("lost explicit null")
	}
	out, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if v, ok := m["value"]; !ok || v != nil {
		t.Fatal(string(out))
	}
	missing := Condition{Op: "eq", Path: "/x"}
	r := rule("missing")
	r.When = missing
	if _, err := Compile([]Rule{r}); err == nil {
		t.Fatal("absent comparison value accepted")
	}
}
func TestArrayPredicateReadsOriginalIndexAndCurrent(t *testing.T) {
	r := rule("filter", act("array_filter", map[string]any{"path": "/values", "predicate": Condition{Op: "all", Conditions: []Condition{{Op: "gt", Source: "item", Value: ref("current", "/limit")}, {Op: "in", Source: "context", Path: "/item_index", Value: []any{1, 3}}}}, "mode": "keep_matches"}))
	res := applyTest(t, r, map[string]any{"limit": 3, "values": []any{1, 5, 6, 7}})
	assertJSON(t, pathValue(t, res.Body, "/values"), []any{5, 7})
}
