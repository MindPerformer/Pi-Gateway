package rules

import (
	"fmt"
)

// Compatibility recipes are data builders only. The executor never dispatches
// domain actions or calls the old middleware registry.
func isLegacyAction(t string) bool {
	return contains([]string{"rewrite_model", "drop_environment_context", "drop_input_items", "drop_tools", "passthrough_fields", "set_reasoning"}, t)
}
func refExpr(source, path string) any {
	return map[string]any{"$ref": map[string]any{"source": source, "path": path}}
}
func expr(op string, args ...any) any {
	return map[string]any{"$expr": map[string]any{"op": op, "args": args}}
}
func step(id, t string, p map[string]any) Action { return Action{ID: id, Type: t, Params: p} }
func setStep(id, path string, v any) Action {
	return step(id, "json_set", map[string]any{"path": path, "value": v, "create_parents": true})
}
func removeStep(id string, paths ...any) Action {
	return step(id, "json_remove", map[string]any{"paths": paths})
}
func testExpr(v any) Condition              { return Condition{Op: "test", Value: v} }
func cond(op, path string, v any) Condition { return Condition{Op: op, Path: path, Value: v} }
func all(cs ...Condition) Condition         { return Condition{Op: "all", Conditions: cs} }
func anyCond(cs ...Condition) Condition     { return Condition{Op: "any", Conditions: cs} }
func branch(id string, c Condition, then []Action, otherwise ...Action) Action {
	return step(id, "if", map[string]any{"predicate": c, "then": append([]Action{}, then...), "else": append([]Action{}, otherwise...)})
}
func nonemptyString(v any) any {
	return expr("and", expr("eq", expr("type", v), "string"), expr("ne", v, ""))
}
func emptyValue(v any) any { return expr("or", expr("eq", v, ""), expr("eq", v, []any{})) }

func expandLegacyAction(a Action) ([]Action, error) {
	p := a.Params
	id := a.ID
	switch a.Type {
	case "rewrite_model":
		return []Action{setStep(id+"-set", "/model", p["model"])}, nil
	case "set_reasoning":
		out := []Action{}
		for _, key := range []string{"effort", "summary"} {
			if v := stringParam(p, key); v != "" {
				out = append(out, setStep(id+"-"+key, "/reasoning/"+key, v))
			}
		}
		return []Action{branch(id+"-ensure", Condition{Op: "not", Conditions: []Condition{cond("type", "/reasoning", "object")}}, []Action{setStep(id+"-object", "/reasoning", map[string]any{})}), step(id+"-values", "sequence", map[string]any{"steps": out})}, nil
	case "passthrough_fields":
		out := []Action{}
		for i, v := range legacyStrings(p["fields"]) {
			key := v.(string)
			if key == "" {
				continue
			}
			path := joinPointer("", key)
			c := all(Condition{Op: "not_exists", Path: path}, Condition{Op: "exists", Source: "client", Path: path}, Condition{Op: "ne", Source: "client", Path: path, ValuePresent: true})
			out = append(out, branch(fmt.Sprintf("%s-%d", id, i), c, []Action{setStep(fmt.Sprintf("%s-copy-%d", id, i), path, refExpr("client", path))}))
		}
		return out, nil
	case "drop_input_items":
		types := legacyStrings(p["types"])
		pattern := stringParam(p, "pattern")
		parts := []Condition{}
		steps := []Action{}
		if len(types) > 0 {
			parts = append(parts, Condition{Op: "in", Source: "item", Path: "/type", Value: types})
		}
		if pattern != "" {
			c := Condition{Op: "regex", Source: "item", Value: pattern, CaseInsensitive: boolParam(p, "case_insensitive"), DotAll: boolParam(p, "dot_all"), Multiline: boolParam(p, "multiline")}
			if p["target"] == "text" {
				c.Source = "vars"
				c.Path = "/visible"
				steps = append(steps, step("save-item", "let", map[string]any{"name": "saved_item", "value": refExpr("current", "")}),
					step("visible-walk", "walk", map[string]any{"path": "", "steps": []Action{
						branch("visible-object", cond("type", "", "object"), []Action{setStep("object-text", "", expr("coalesce", refExpr("current", "/text"), refExpr("current", "/content"), ""))}),
						branch("visible-array", cond("type", "", "array"), []Action{setStep("joined-parts", "", expr("join", refExpr("current", ""), "\n"))}),
						branch("visible-scalar", Condition{Op: "not", Conditions: []Condition{cond("type", "", "string")}}, []Action{setStep("empty-text", "", "")}),
					}}), step("visible-value", "let", map[string]any{"name": "visible", "value": refExpr("current", "")}), setStep("restore-item", "", refExpr("vars", "/saved_item")))
			} else {
				c.Encoding = "json"
				steps = append(steps, branch("raw-string", cond("type", "", "string"), []Action{step("raw-value", "let", map[string]any{"name": "raw", "value": refExpr("current", "")})}, step("json-value", "let", map[string]any{"name": "raw", "value": expr("json_stringify", refExpr("current", ""))})))
				c.Source = "vars"
				c.Path = "/raw"
				c.Encoding = ""
			}
			parts = append(parts, c)
		}
		if len(parts) == 0 {
			return []Action{}, nil
		}
		return []Action{branch(id+"-array", cond("type", "/input", "array"), []Action{step(id+"-filter", "for_each", map[string]any{"path": "/input", "steps": steps, "keep": Condition{Op: "not", Conditions: []Condition{anyCond(parts...)}}})})}, nil
	case "drop_environment_context":
		return environmentRecipe(id, p), nil
	case "drop_tools":
		return toolsRecipe(id, p), nil
	}
	return nil, fmt.Errorf("unknown compatibility action %q", a.Type)
}

func cleanText(v any) any {
	return expr("trim", expr("regex_replace", v, "(?is)<environment_context>.*?</environment_context>", ""))
}
func environmentRecipe(id string, p map[string]any) []Action {
	kind := stringParam(p, "content_item_kind")
	if kind == "" {
		kind = "environments.environment_context"
	}
	strip := true
	if b, ok := p["also_strip_from_instructions"].(bool); ok {
		strip = b
	}
	clean := func(key string) Action {
		path := joinPointer("", key)
		return branch("clean-"+key, all(cond("type", path, "string"), cond("regex", path, "(?is)<environment_context>.*?</environment_context>")), []Action{setStep("replace-"+key, path, cleanText(refExpr("current", path)))})
	}
	meta := "/internal_chat_message_metadata_passthrough/content_item_kinds"
	keepPart := testExpr(expr("and", expr("ne", expr("coalesce", expr("index", refExpr("vars", "/kinds"), refExpr("context", "/item_index")), ""), kind), expr("not", expr("and", expr("eq", expr("type", refExpr("original", "/text")), "string"), expr("contains", expr("coalesce", refExpr("original", "/text"), ""), "<environment_context>"), expr("eq", expr("coalesce", refExpr("current", "/text"), "nontext"), "")))))
	arraySteps := []Action{
		step("kinds", "let", map[string]any{"name": "kinds", "value": expr("coalesce", refExpr("current", meta), []any{})}),
		step("original-count", "let", map[string]any{"name": "original_count", "value": expr("length", refExpr("current", "/content"))}),
		step("parts", "for_each", map[string]any{"path": "/content", "steps": []Action{clean("text")}, "keep": keepPart}),
		branch("metadata-alignment", testExpr(expr("and", expr("exists", refExpr("current", meta)), expr("ne", expr("length", refExpr("current", meta)), expr("length", refExpr("current", "/content"))))), []Action{removeStep("remove-misaligned", "/internal_chat_message_metadata_passthrough")}),
	}
	messageSteps := []Action{branch("content-array", cond("type", "/content", "array"), arraySteps, clean("content"))}
	keepMessage := testExpr(expr("not", expr("or", expr("or", expr("eq", expr("coalesce", refExpr("current", "/content"), "absent"), []any{}), expr("and", expr("eq", expr("coalesce", refExpr("current", "/content"), "absent"), ""), expr("ne", expr("coalesce", refExpr("original", "/content"), ""), ""))), expr("and", expr("ne", expr("coalesce", refExpr("current", meta), []any{}), []any{}), expr("eq", expr("coalesce", refExpr("current", meta), []any{}), expr("array", kind))))))
	out := []Action{branch(id+"-input", cond("type", "/input", "array"), []Action{step("messages", "for_each", map[string]any{"path": "/input", "steps": messageSteps, "keep": keepMessage})}, clean("input"))}
	if strip {
		out = append(out, clean("instructions"))
	}
	return out
}

func toolsRecipe(id string, p map[string]any) []Action {
	if boolParam(p, "drop_all") {
		return []Action{removeStep(id+"-all", "/tools")}
	}
	names := legacyStrings(p["names"])
	types := legacyStrings(p["types"])
	if len(names) == 0 && len(types) == 0 {
		return []Action{}
	}
	match := expr("or", expr("contains", names, expr("coalesce", refExpr("current", "/name"), "")), expr("contains", types, expr("coalesce", refExpr("current", "/type"), "")))
	keep := testExpr(expr("and", expr("not", match), expr("not", expr("and", expr("eq", expr("coalesce", refExpr("current", "/type"), ""), "namespace"), expr("eq", expr("coalesce", refExpr("current", "/tools"), "absent"), []any{})))))
	// walk handles nested namespace arrays without recursive fragment calls.
	walk := step(id+"-tree", "walk", map[string]any{"path": "/tools", "predicate": cond("type", "", "array"), "steps": []Action{step(id+"-items", "for_each", map[string]any{"path": "", "steps": []Action{}, "keep": keep})}})
	choice := expr("coalesce", refExpr("current", "/tool_choice"), "")
	choiceMatch := expr("or", expr("contains", types, expr("coalesce", refExpr("current", "/tool_choice/type"), choice)), expr("contains", names, expr("coalesce", refExpr("current", "/tool_choice/name"), "")), expr("and", expr("eq", choice, "required"), expr("eq", expr("coalesce", refExpr("current", "/tools"), []any{}), []any{})))
	return []Action{branch(id+"-has-tools", cond("type", "/tools", "array"), []Action{walk, branch(id+"-empty", cond("eq", "/tools", []any{}), []Action{removeStep(id+"-remove", "/tools")}), branch(id+"-choice", testExpr(choiceMatch), []Action{removeStep(id+"-clear-choice", "/tool_choice")})})}
}

// ExpandRule exposes the same primitive tree the compiler executes. Saving this
// representation removes all dependence on domain compatibility actions.
func ExpandRule(r Rule) (Rule, error) {
	var expand func([]Action) ([]Action, error)
	expand = func(actions []Action) ([]Action, error) {
		out := []Action{}
		for _, a := range actions {
			if isLegacyAction(a.Type) {
				steps, err := expandLegacyAction(a)
				if err != nil {
					return nil, err
				}
				steps, err = expand(steps)
				if err != nil {
					return nil, err
				}
				a = step(a.ID, "sequence", map[string]any{"steps": steps})
			} else {
				for _, key := range []string{"steps", "then", "else"} {
					if v, ok := a.Params[key]; ok {
						steps, err := decodeActions(v, "/params/"+key)
						if err != nil {
							return nil, err
						}
						steps, err = expand(steps)
						if err != nil {
							return nil, err
						}
						a.Params[key] = steps
					}
				}
				if a.Type == "scope" {
					if functions, ok := a.Params["functions"].(map[string]any); ok {
						for name, value := range functions {
							body, err := decodeActions(value, joinPointer("/params/functions", name))
							if err != nil {
								return nil, err
							}
							body, err = expand(body)
							if err != nil {
								return nil, err
							}
							functions[name] = body
						}
					}
				}
			}
			out = append(out, a)
		}
		return out, nil
	}
	actions, err := expand(r.Actions)
	r.Actions = actions
	r.SchemaVersion = SchemaVersion
	return r, err
}
