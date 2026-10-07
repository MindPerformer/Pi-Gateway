package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type evaluation struct {
	ctx           context.Context
	body          any
	client        any
	facts         map[string]any
	model         string
	originalModel string
	eventType     string
	item          any
	index         int
	hasItem       bool
	phase         string
	ruleID        string
	rulePath      string
	conditions    *conditionCollector
	unavailable   []string
	original      any
	vars          map[string]any
	steps         *int
	nestedTraces  *traceCollector
	traceRule     *compiledRule
	itemPath      string
	functions     map[string]*runtimeFragment
}

func (s *evaluation) source(source string) any {
	switch source {
	case "", "current":
		return s.body
	case "client":
		return s.client
	case "original":
		return s.original
	case "vars":
		return s.vars
	case "context":
		facts := make(map[string]any, len(s.facts)+5)
		for k, v := range s.facts {
			facts[k] = v
		}
		facts["model"] = s.model
		if !s.hasItem && (s.phase == PhaseRequest || s.phase == PhaseRequestFinalize) {
			if model, ok := objGet(s.body, "model"); ok {
				facts["model"] = model
			}
		}
		facts["original_model"] = s.originalModel
		if s.phase == PhaseResponseEvent || s.phase == PhaseResponseBody {
			facts["event_type"] = s.eventType
		} else if s.phase != PhaseUpstreamHeaders {
			delete(facts, "account_id")
			delete(facts, "upstream_protocol")
			delete(facts, "event_type")
		}
		if s.hasItem {
			facts["item_index"] = s.index
		} else {
			delete(facts, "item_index")
		}
		return facts
	case "item":
		if s.hasItem {
			return s.item
		}
	}
	return nil
}
func (s *evaluation) requireAvailable(source, path string) error {
	if len(s.unavailable) == 0 {
		return nil
	}
	if source == "" {
		source = "current"
	}
	selected := "/" + source + path
	for _, missing := range s.unavailable {
		if selected == missing || strings.HasPrefix(selected, missing+"/") || strings.HasPrefix(missing, selected+"/") {
			return &MissingInputError{Path: missing}
		}
	}
	return nil
}

func (s *evaluation) selectValue(source, path, encoding string) (any, bool, error) {
	if err := s.requireAvailable(source, path); err != nil {
		return nil, false, err
	}
	v, ok, e := pointerGet(s.source(source), path)
	if e != nil || !ok {
		return v, ok, e
	}
	if encoding == "json" {
		b, e := json.Marshal(v)
		return string(b), e == nil, e
	}
	return v, true, nil
}
func (s *evaluation) expression(v any) (any, bool, error) {
	if m, ok := v.(map[string]any); ok {
		if x, exists := m["$literal"]; exists {
			return x, true, nil
		}
		if x, exists := m["$ref"]; exists {
			r := x.(map[string]any)
			return s.selectValue(stringParam(r, "source"), stringParam(r, "path"), stringParam(r, "encoding"))
		}
		if x, exists := m["$expr"]; exists {
			return s.compute(x.(map[string]any))
		}
	}
	return v, true, nil
}
func (s *evaluation) requiredExpression(v any) (any, error) {
	x, ok, e := s.expression(v)
	if e != nil {
		return nil, e
	}
	if !ok {
		return nil, fmt.Errorf("referenced value does not exist")
	}
	return cloneJSON(x)
}
func (c *compiledCondition) matches(s *evaluation) (matched bool, err error) {
	if s.conditions != nil {
		defer func() { s.conditions.record(c, s, matched, err, false) }()
	}
	return c.evaluate(s)
}

func (c *compiledCondition) skip(s *evaluation) {
	if s.conditions == nil {
		return
	}
	s.conditions.record(c, s, false, nil, true)
	for _, child := range c.children {
		child.skip(s)
	}
}

func (c *compiledCondition) evaluate(s *evaluation) (bool, error) {
	if e := s.ctx.Err(); e != nil {
		return false, e
	}
	switch c.raw.Op {
	case "test":
		v, exists, err := s.expression(c.raw.Value)
		if err != nil || !exists {
			return false, err
		}
		b, ok := v.(bool)
		if !ok {
			return false, fmt.Errorf("test expression must produce boolean")
		}
		return b, nil
	case "always":
		return true, nil
	case "all":
		for i, x := range c.children {
			ok, e := x.matches(s)
			if e != nil || !ok {
				for _, skipped := range c.children[i+1:] {
					skipped.skip(s)
				}
				return ok, e
			}
		}
		return true, nil
	case "any":
		for i, x := range c.children {
			ok, e := x.matches(s)
			if e != nil || ok {
				for _, skipped := range c.children[i+1:] {
					skipped.skip(s)
				}
				return ok, e
			}
		}
		return false, nil
	case "not":
		ok, e := c.children[0].matches(s)
		return !ok, e
	}
	left, exists, e := s.selectValue(c.raw.Source, c.raw.Path, c.raw.Encoding)
	if e != nil {
		return false, e
	}
	if c.raw.Op == "exists" {
		return exists, nil
	}
	if c.raw.Op == "not_exists" {
		return !exists, nil
	}
	if !exists {
		return false, nil
	}
	right, exists, e := s.expression(c.raw.Value)
	if e != nil || !exists {
		return false, e
	}
	switch c.raw.Op {
	case "eq":
		return jsonEqual(left, right), nil
	case "ne":
		return !jsonEqual(left, right), nil
	case "type":
		return jsonType(left) == right, nil
	case "regex", "not_regex":
		str, ok := left.(string)
		if !ok {
			return false, nil
		}
		match := c.re.MatchString(str)
		if c.raw.Op == "not_regex" {
			match = !match
		}
		return match, nil
	case "gt", "gte", "lt", "lte":
		a, aok := number(left)
		b, bok := number(right)
		if !aok || !bok {
			return false, nil
		}
		switch c.raw.Op {
		case "gt":
			return a > b, nil
		case "gte":
			return a >= b, nil
		case "lt":
			return a < b, nil
		default:
			return a <= b, nil
		}
	case "starts_with", "ends_with":
		a, aok := left.(string)
		b, bok := right.(string)
		if !aok || !bok {
			return false, nil
		}
		if c.raw.Op == "starts_with" {
			return strings.HasPrefix(a, b), nil
		}
		return strings.HasSuffix(a, b), nil
	case "contains", "not_contains":
		match, valid := containsValue(left, right)
		if !valid {
			return false, nil
		}
		if c.raw.Op == "not_contains" {
			match = !match
		}
		return match, nil
	case "in", "not_in":
		a, ok := right.([]any)
		if !ok {
			return false, nil
		}
		match := false
		for _, x := range a {
			if jsonEqual(left, x) {
				match = true
				break
			}
		}
		if c.raw.Op == "not_in" {
			match = !match
		}
		return match, nil
	}
	return false, fmt.Errorf("unsupported condition")
}
func containsValue(container, value any) (bool, bool) {
	switch a := container.(type) {
	case string:
		b, ok := value.(string)
		if !ok {
			return false, false
		}
		return strings.Contains(a, b), true
	case []any:
		for _, x := range a {
			if jsonEqual(x, value) {
				return true, true
			}
		}
		return false, true
	}
	if _, ok := object(container); ok {
		k, ok := value.(string)
		if !ok {
			return false, false
		}
		_, exists := objGet(container, k)
		return exists, true
	}
	return false, false
}
