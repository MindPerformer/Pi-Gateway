package rules

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestStrictParseErrors(t *testing.T) {
	base := `{"schema_version":1,"name":"test","phase":"request","when":{"op":"always"},"actions":[]}`
	tests := []struct{ name, raw, path string }{
		{"unknown", strings.Replace(base, `"actions":[]`, `"actions":[],"extra":1`, 1), "/extra"},
		{"duplicate", strings.Replace(base, `"name":"test"`, `"name":"test","name":"other"`, 1), "/name"},
		{"version", strings.Replace(base, `"schema_version":1`, `"schema_version":2`, 1), "/schema_version"},
		{"phase", strings.Replace(base, `"request"`, `"responses"`, 1), "/phase"},
		{"null-enabled", strings.Replace(base, `"actions":[]`, `"actions":[],"enabled":null`, 1), "/enabled"},
		{"float-priority", strings.Replace(base, `"actions":[]`, `"actions":[],"priority":1.5`, 1), "/priority"},
		{"priority-bound", strings.Replace(base, `"actions":[]`, `"actions":[],"priority":2147483648`, 1), "/priority"},
		{"unsafe", strings.Replace(base, `"actions":[]`, `"actions":[],"revision":9007199254740993`, 1), "/revision"},
		{"unsafe-exponent", strings.Replace(base, `"actions":[]`, `"actions":[],"revision":9.007199254740993e15`, 1), "/revision"},
		{"infinity", strings.Replace(base, `"actions":[]`, `"actions":[],"priority":1e309`, 1), "/priority"},
		{"missing-when", strings.Replace(base, `"when":{"op":"always"},`, "", 1), "/when"},
		{"null-actions", strings.Replace(base, `"actions":[]`, `"actions":null`, 1), "/actions"},
		{"unknown-condition", strings.Replace(base, `{"op":"always"}`, `{"op":"always","hidden":false}`, 1), "/when/hidden"},
		{"unused-condition-field", strings.Replace(base, `{"op":"always"}`, `{"op":"always","dot_all":false}`, 1), "/when/dot_all"},
		{"null-path", strings.Replace(base, `{"op":"always"}`, `{"op":"exists","path":null}`, 1), "/when/path"},
		{"empty-group", strings.Replace(base, `{"op":"always"}`, `{"op":"all","conditions":[]}`, 1), "/when/conditions"},
		{"wrong-not-arity", strings.Replace(base, `{"op":"always"}`, `{"op":"not","conditions":[{"op":"always"},{"op":"always"}]}`, 1), "/when/conditions"},
		{"nested-unknown", strings.Replace(base, `{"op":"always"}`, `{"op":"all","conditions":[{"op":"exists","bogus":true}]}`, 1), "/when/conditions/0/bogus"},
		{"action-unknown", strings.Replace(base, `"actions":[]`, `"actions":[{"id":"x","type":"json_set","params":{"path":"/x","value":1},"x":1}]`, 1), "/actions/0/x"},
		{"param-unknown", strings.Replace(base, `"actions":[]`, `"actions":[{"id":"x","type":"json_set","params":{"path":"/x","value":1,"hidden":false}}]`, 1), "/actions/0/params/hidden"},
		{"null-param", strings.Replace(base, `"actions":[]`, `"actions":[{"id":"x","type":"json_set","params":{"path":"/x","value":1,"create_parents":null}}]`, 1), "/actions/0/params/create_parents"},
		{"unsafe-value", strings.Replace(base, `"actions":[]`, `"actions":[{"id":"x","type":"json_set","params":{"path":"/x","value":{"nested":[9007199254740993]}}}]`, 1), "/actions/0/params/value/nested/0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseRule([]byte(tt.raw))
			ve, ok := err.(*ValidationError)
			if !ok || ve.Path != tt.path || ve.Message == "" {
				t.Fatalf("got %T %v want path %s", err, err, tt.path)
			}
		})
	}
	for _, raw := range []string{base + ` {}`, base + ` garbage`, "null", "[]"} {
		if _, e := ParseRule([]byte(raw)); e == nil {
			t.Fatal("invalid JSON accepted", raw)
		}
	}
	good, err := ParseRule([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if !good.Enabled || good.Priority != 100 || good.OnError != "abort" {
		t.Fatal("wrong defaults", good)
	}
	if _, err := ParseRules([]byte("[" + base + "," + strings.Replace(base, `"request"`, `"bad"`, 1) + "]")); err == nil || err.(*ValidationError).Path != "/1/phase" {
		t.Fatal(err)
	}
}
func TestCompileSemanticValidation(t *testing.T) {
	cases := []struct {
		name string
		r    Rule
		path string
	}{
		{"unknown-action", rule("x", act("wat", nil)), "/0/actions/0/type"},
		{"wrong-phase", func() Rule {
			r := rule("x", act("rewrite_model", map[string]any{"model": "m"}))
			r.Phase = PhaseResponseBody
			return r
		}(), "/0/actions/0/type"},
		{"blank-model", rule("x", act("rewrite_model", map[string]any{"model": "  "})), "/0/actions/0/params/model"},
		{"missing-value", rule("x", act("json_set", map[string]any{"path": "/x"})), "/0/actions/0/params/value"},
		{"bad-pointer", rule("x", act("json_set", map[string]any{"path": "x", "value": 1})), "/0/actions/0/params/path"},
		{"bad-escape", rule("x", act("json_set", map[string]any{"path": "/a~2b", "value": 1})), "/0/actions/0/params/path"},
		{"remove-root", rule("x", act("json_remove", map[string]any{"paths": []any{""}})), "/0/actions/0/params/paths/0"},
		{"move-client", rule("x", act("json_transfer", map[string]any{"source": "client", "source_path": "/x", "target_path": "/y", "operation": "move"})), "/0/actions/0/params/source"},
		{"move-child", rule("x", act("json_transfer", map[string]any{"source_path": "/x", "target_path": "/x/y", "operation": "move"})), "/0/actions/0/params/target_path"},
		{"merge-scalar", rule("x", act("json_merge", map[string]any{"path": "", "value": 3})), "/0/actions/0/params/value"},
		{"index-missing", rule("x", act("array_insert", map[string]any{"path": "/a", "values": []any{}, "position": "index"})), "/0/actions/0/params/index"},
		{"unused-index", rule("x", act("array_insert", map[string]any{"path": "/a", "values": []any{}, "index": 1})), "/0/actions/0/params/index"},
		{"regex-invalid", rule("x", act("text_replace", map[string]any{"path": "/x", "match": "regex", "pattern": "["})), "/0/actions/0/params/pattern"},
		{"literal-flags", rule("x", act("text_replace", map[string]any{"path": "/x", "pattern": "a", "dot_all": true})), "/0/actions/0/params/dot_all"},
		{"status-invalid", rule("x", act("reject_request", map[string]any{"status": 500})), "/0/actions/0/params/status"},
		{"null-array", rule("x", act("drop_tools", map[string]any{"names": nil})), "/0/actions/0/params/names"},
		{"ref-unknown", rule("x", act("json_set", map[string]any{"path": "/x", "value": map[string]any{"$ref": map[string]any{"source": "client", "wat": 1}}})), "/0/actions/0/params/value/$ref/wat"},
		{"ref-ambiguous", rule("x", act("json_set", map[string]any{"path": "/x", "value": map[string]any{"$ref": map[string]any{}, "x": 1}})), "/0/actions/0/params/value"},
		{"account-in-request", rule("x", act("json_set", map[string]any{"path": "/x", "value": ref("context", "/account_id")})), "/0/actions/0/params/value/$ref/path"},
		{"item-outside-filter", rule("x", act("json_set", map[string]any{"path": "/x", "value": ref("item", "")})), "/0/actions/0/params/value/$ref/source"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile([]Rule{tc.r})
			ve, ok := err.(*ValidationError)
			if !ok || ve.Path != tc.path {
				t.Fatalf("got %v want %s", err, tc.path)
			}
		})
	}
	r := rule("nan", act("json_set", map[string]any{"path": "/x", "value": math.NaN()}))
	if _, e := Compile([]Rule{r}); e == nil {
		t.Fatal("NaN accepted")
	}
	r = rule("duplicate", act("drop_tools", map[string]any{}), act("drop_tools", map[string]any{}))
	if _, e := Compile([]Rule{r}); e == nil {
		t.Fatal("duplicate action IDs accepted")
	}
	if _, e := Compile([]Rule{rule("same"), rule("same")}); e == nil {
		t.Fatal("duplicate rule IDs accepted")
	}
}
func TestExpressionBudgetsAndSafeNumbers(t *testing.T) {
	c := Condition{Op: "always"}
	for i := 0; i < MaxDepth+1; i++ {
		c = Condition{Op: "not", Conditions: []Condition{c}}
	}
	r := rule("deep")
	r.When = c
	if _, e := Compile([]Rule{r}); e == nil {
		t.Fatal("deep condition accepted")
	}
	conditions := make([]Condition, MaxConditionNodes+1)
	for i := range conditions {
		conditions[i] = Condition{Op: "always"}
	}
	r.When = Condition{Op: "all", Conditions: conditions}
	if _, e := Compile([]Rule{r}); e == nil {
		t.Fatal("unbounded condition nodes")
	}
	for _, v := range []any{nil, true, false, "", []any{1, nil, "x"}, map[string]any{"x": nil}, float64(0.25), MaxSafeInteger, -MaxSafeInteger} {
		r := rule("literal", act("json_set", map[string]any{"path": "/value", "value": v}))
		b, e := json.Marshal(r)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = ParseRule(b); e != nil {
			t.Fatalf("valid literal %v: %v", v, e)
		}
	}
	for _, n := range []string{"9007199254740992", "9007199254740992.0", "9.007199254740992e15", "-9007199254740992", "1e999"} {
		r := rule("n", act("json_set", map[string]any{"path": "/v", "value": "REPLACE"}))
		raw, _ := json.Marshal(r)
		raw = []byte(strings.Replace(string(raw), `"REPLACE"`, n, 1))
		if _, e := ParseRule(raw); e == nil {
			t.Fatal("unsafe number accepted", n)
		}
	}
	t.Log(fmt.Sprintf("budgets: depth=%d nodes=%d actions=%d", MaxDepth, MaxConditionNodes, MaxActionsPerRule))
}
