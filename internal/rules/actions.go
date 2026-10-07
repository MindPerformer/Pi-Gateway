package rules

import (
	"encoding/json"
	"fmt"
	"strings"

	"pi-gateway/internal/middleware"
)

type terminal struct {
	blocked bool
	status  int
	reason  string
	dropped bool
}

var oldRegistry = middleware.Registry()

func (a compiledAction) execute(s *evaluation) (terminal, error) {
	if e := s.ctx.Err(); e != nil {
		return terminal{}, e
	}
	p := a.raw.Params
	path := stringParam(p, "path")
	switch a.raw.Type {
	case "sequence", "if", "for_each", "walk", "scope", "call", "let":
		return a.executeFlow(s)
	case "json_set":
		if p["if_exists"] == "keep" {
			if _, exists, e := s.selectValue("current", path, ""); e != nil {
				return terminal{}, e
			} else if exists {
				return terminal{}, nil
			}
		}
		v, e := s.requiredExpression(p["value"])
		if e != nil {
			return terminal{}, e
		}
		s.body, e = pointerSet(s.body, path, v, boolParam(p, "create_parents"), p["if_exists"] == "keep")
		return terminal{}, e
	case "json_remove":
		for _, raw := range p["paths"].([]any) {
			if err := s.requireAvailable("current", raw.(string)); err != nil {
				return terminal{}, err
			}
			var e error
			s.body, e = pointerRemove(s.body, raw.(string), p["on_missing"] == "error")
			if e != nil {
				return terminal{}, e
			}
		}
		return terminal{}, nil
	case "json_merge":
		incoming, e := s.requiredExpression(p["value"])
		if e != nil {
			return terminal{}, e
		}
		if _, ok := object(incoming); !ok {
			return terminal{}, fmt.Errorf("merge value must be an object")
		}
		target, exists, e := s.selectValue("current", path, "")
		if e != nil {
			return terminal{}, e
		}
		if !exists {
			if !boolParam(p, "create_parents") {
				return terminal{}, fmt.Errorf("merge target does not exist")
			}
			target = map[string]any{}
		}
		if _, ok := object(target); !ok {
			return terminal{}, fmt.Errorf("merge target must be an object")
		}
		mergeObjects(target, incoming, p["mode"] == "deep", p["array_mode"] == "append")
		s.body, e = pointerSet(s.body, path, target, boolParam(p, "create_parents"), false)
		return terminal{}, e
	case "json_transfer":
		return terminal{}, a.transfer(s)
	case "array_insert":
		v, exists, e := s.selectValue("current", path, "")
		if e != nil {
			return terminal{}, e
		}
		if !exists {
			if !boolParam(p, "create_if_missing") {
				return terminal{}, fmt.Errorf("array does not exist")
			}
			v = []any{}
		}
		arr, ok := v.([]any)
		if !ok {
			return terminal{}, fmt.Errorf("target must be an array")
		}
		values := make([]any, 0, len(p["values"].([]any)))
		for _, x := range p["values"].([]any) {
			v, e := s.requiredExpression(x)
			if e != nil {
				return terminal{}, e
			}
			values = append(values, v)
		}
		index := len(arr)
		if p["position"] == "prepend" {
			index = 0
		}
		if p["position"] == "index" {
			n, _ := number(p["index"])
			if n > float64(len(arr)) {
				return terminal{}, fmt.Errorf("index exceeds array length")
			}
			index = int(n)
		}
		out := make([]any, 0, len(arr)+len(values))
		out = append(out, arr[:index]...)
		out = append(out, values...)
		out = append(out, arr[index:]...)
		s.body, e = pointerSet(s.body, path, out, boolParam(p, "create_if_missing"), false)
		return terminal{}, e
	case "array_filter":
		v, exists, e := s.selectValue("current", path, "")
		if e != nil {
			return terminal{}, e
		}
		if !exists {
			return terminal{}, onMissing(p)
		}
		arr, ok := v.([]any)
		if !ok {
			return terminal{}, fmt.Errorf("target must be an array")
		}
		kept := make([]any, 0, len(arr))
		for i, item := range arr {
			nested := *s
			nested.item = item
			nested.index = i
			nested.hasItem = true
			match, e := a.predicate.matches(&nested)
			if e != nil {
				return terminal{}, e
			}
			if match == (p["mode"] == "keep_matches") {
				kept = append(kept, item)
			}
		}
		s.body, e = pointerSet(s.body, path, kept, false, false)
		return terminal{}, e
	case "text_replace":
		v, exists, e := s.selectValue("current", path, "")
		if e != nil {
			return terminal{}, e
		}
		if !exists {
			return terminal{}, onMissing(p)
		}
		text, ok := v.(string)
		if !ok {
			return terminal{}, fmt.Errorf("text target must be a string")
		}
		pattern := stringParam(p, "pattern")
		replacement := stringParam(p, "replacement")
		updated := text
		if p["match"] == "regex" {
			if boolParam(p, "replace_all") {
				updated = a.re.ReplaceAllString(text, replacement)
			} else if match := a.re.FindStringSubmatchIndex(text); match != nil {
				updated = text[:match[0]] + string(a.re.ExpandString(nil, replacement, text, match)) + text[match[1]:]
			}
		} else {
			n := 1
			if boolParam(p, "replace_all") {
				n = -1
			}
			updated = strings.Replace(text, pattern, replacement, n)
		}
		s.body, e = pointerSet(s.body, path, updated, false, false)
		return terminal{}, e
	case "reject_request":
		status, _ := number(p["status"])
		return terminal{blocked: true, status: int(status), reason: stringParam(p, "message")}, nil
	case "drop_event":
		if !droppableEvent(s.eventType, s.body) {
			return terminal{}, fmt.Errorf("event %q cannot be dropped: terminal, error and tool associations are protected", s.eventType)
		}
		return terminal{dropped: true}, nil
	}
	return terminal{}, fmt.Errorf("unsupported action %q", a.raw.Type)
}
func onMissing(p map[string]any) error {
	if p["on_missing"] == "error" {
		return fmt.Errorf("path does not exist")
	}
	return nil
}
func (a compiledAction) dropInput(s *evaluation) error {
	p := a.raw.Params
	v, exists, e := pointerGet(s.body, "/input")
	if e != nil || !exists {
		return e
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	types := p["types"].([]any)
	kept := make([]any, 0, len(arr))
	for _, item := range arr {
		if e := s.ctx.Err(); e != nil {
			return e
		}
		drop := false
		if m, ok := object(item); ok {
			typ, _ := m["type"].(string)
			if typ != "" {
				for _, t := range types {
					if typ == t {
						drop = true
						break
					}
				}
			}
			if !drop && a.re != nil {
				var text string
				if p["target"] == "text" {
					text = visibleText(item)
				} else {
					b, e := json.Marshal(item)
					if e != nil {
						return e
					}
					text = string(b)
				}
				drop = a.re.MatchString(text)
			}
		} else if str, ok := item.(string); ok && a.re != nil {
			drop = a.re.MatchString(str)
		}
		if !drop {
			kept = append(kept, item)
		}
	}
	if len(kept) != len(arr) {
		s.body, e = pointerSet(s.body, "/input", kept, false, false)
	}
	return e
}
func visibleText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		parts := make([]string, 0, len(x))
		for _, v := range x {
			parts = append(parts, visibleText(v))
		}
		return strings.Join(parts, "\n")
	}
	if m, ok := object(v); ok {
		if t, ok := m["text"].(string); ok {
			return t
		}
		if c, ok := m["content"]; ok {
			return visibleText(c)
		}
	}
	return ""
}
func mergeObjects(target, incoming any, deep, appendArrays bool) {
	for _, k := range keys(incoming) {
		v, _ := objGet(incoming, k)
		old, exists := objGet(target, k)
		if exists {
			if oa, ok := old.([]any); ok && appendArrays {
				if va, ok := v.([]any); ok {
					objSet(target, k, append(oa, va...))
					continue
				}
			}
			if deep {
				_, a := object(old)
				_, b := object(v)
				if a && b {
					mergeObjects(old, v, deep, appendArrays)
					continue
				}
			}
		}
		objSet(target, k, v)
	}
}
func (a compiledAction) transfer(s *evaluation) error {
	p := a.raw.Params
	source := stringParam(p, "source")
	sp := stringParam(p, "source_path")
	tp := stringParam(p, "target_path")
	v, exists, e := s.selectValue(source, sp, "value")
	if e != nil {
		return e
	}
	if !exists {
		return onMissing(p)
	}
	if source == "current" && sp == tp {
		return nil
	}
	if _, found, e := s.selectValue("current", tp, ""); e != nil {
		return e
	} else if found && !boolParam(p, "overwrite") {
		return fmt.Errorf("target already exists")
	}
	v, e = cloneJSON(v)
	if e != nil {
		return e
	}
	if p["operation"] == "move" {
		s.body, e = pointerRemove(s.body, sp, true)
		if e != nil {
			return e
		}
	}
	s.body, e = pointerSet(s.body, tp, v, boolParam(p, "create_parents"), false)
	return e
}
