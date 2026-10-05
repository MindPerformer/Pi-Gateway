package rules

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"pi-gateway/internal/middleware"
)

// MigrateLegacy is a pure conversion of the old chain's effective configuration.
// Unknown registry names remain ignored, duplicate rows are last-wins, and missing
// rows use enabled=true/order=100 with an EMPTY config (not DefaultConfig JSON).
// The caller must preserve original configs and commit the resulting rules atomically.
func MigrateLegacy(registrations []middleware.Registration) ([]Rule, error) {
	state := map[string]middleware.Registration{}
	for _, r := range registrations {
		state[r.Name] = r
	}
	regs := make([]middleware.Registration, 0, len(oldRegistry))
	for name := range oldRegistry {
		r, exists := state[name]
		if !exists {
			r = middleware.Registration{Name: name, Enabled: true, Order: 100}
		}
		regs = append(regs, r)
	}
	sort.Slice(regs, func(i, j int) bool {
		if regs[i].Order != regs[j].Order {
			return regs[i].Order < regs[j].Order
		}
		return regs[i].Name < regs[j].Name
	})
	out := make([]Rule, 0, len(regs))
	for i, reg := range regs {
		cfg := map[string]any{}
		if strings.TrimSpace(reg.Config) != "" {
			if e := json.Unmarshal([]byte(reg.Config), &cfg); e != nil {
				return nil, invalid("/legacy/"+reg.Name+"/config", "invalid config JSON: %v", e)
			}
		}
		r := Rule{SchemaVersion: SchemaVersion, ID: "legacy-" + reg.Name, Name: reg.Name, Enabled: reg.Enabled, Priority: reg.Order, OrderIndex: int64(i), Phase: PhaseRequest, When: Condition{Op: "always"}, Actions: []Action{}, OnError: OnErrorAbort, LegacyName: reg.Name}
		add := func(kind string, p map[string]any) {
			r.Actions = append(r.Actions, Action{ID: "legacy-" + reg.Name + "-action", Type: kind, Params: p})
		}
		switch reg.Name {
		case "drop_environment_context":
			kind := stringParam(cfg, "content_item_kind")
			if kind == "" {
				kind = "environments.environment_context"
			}
			strip := true
			if b, ok := cfg["also_strip_from_instructions"].(bool); ok {
				strip = b
			}
			add(reg.Name, map[string]any{"content_item_kind": kind, "also_strip_from_instructions": strip})
		case "drop_input_items":
			add(reg.Name, map[string]any{"types": legacyStrings(cfg["types"]), "pattern": stringParam(cfg, "pattern"), "target": "json", "case_insensitive": true, "dot_all": true, "multiline": false})
		case "drop_tools":
			add(reg.Name, map[string]any{"names": legacyStrings(cfg["names"]), "drop_all": boolParam(cfg, "drop_all")})
		case "drop_fields":
			paths := []any{}
			for _, raw := range legacyStrings(cfg["fields"]) {
				f := raw.(string)
				if f == "" {
					continue
				}
				if f == "model" {
					return nil, invalid("/legacy/drop_fields/config/fields", "deleting model cannot preserve the unified routing invariant")
				}
				paths = append(paths, joinPointer("", f))
			}
			add("json_remove", map[string]any{"paths": paths, "on_missing": "ignore"})
		case "rewrite_model":
			model := stringParam(cfg, "model")
			if strings.TrimSpace(model) != "" {
				add(reg.Name, map[string]any{"model": model})
			}
		case "passthrough_fields":
			add(reg.Name, map[string]any{"fields": legacyStrings(cfg["fields"])})
		case "set_reasoning":
			add(reg.Name, map[string]any{"effort": stringParam(cfg, "effort"), "summary": stringParam(cfg, "summary")})
		case "block_prompt":
			pattern := stringParam(cfg, "pattern")
			if strings.TrimSpace(pattern) != "" {
				r.When = Condition{Op: "regex", Source: "current", Encoding: "json", Value: pattern, CaseInsensitive: true, DotAll: true}
				add("reject_request", map[string]any{"status": 403, "message": "request blocked by middleware policy"})
			}
		default:
			return nil, fmt.Errorf("legacy middleware %q has no converter", reg.Name)
		}
		if e := ValidateRule(r, "/legacy/"+reg.Name); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, nil
}
func legacyStrings(v any) []any {
	out := []any{}
	switch x := v.(type) {
	case []string:
		for _, s := range x {
			out = append(out, s)
		}
	case []any:
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
	case string:
		if x != "" {
			for _, s := range strings.Split(x, ",") {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}
