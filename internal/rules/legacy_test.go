package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"pi-gateway/internal/middleware"
	"pi-gateway/internal/piwire"
)

func legacyInput() *piwire.OrderedMap {
	return piwire.NewOrderedMap().Set("model", "old").Set("instructions", "<environment_context>secret</environment_context> Answer").Set("input", []any{
		map[string]any{"type": "message", "content": []any{map[string]any{"type": "input_text", "text": "Hello <environment_context>secret</environment_context> world"}}},
		map[string]any{"type": "reasoning", "content": "reason"}, map[string]any{"type": "message", "content": "BLOCK\nME"}, "string match", map[string]any{"type": "metadata", "internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": []any{"environments.environment_context"}}, "content": []any{map[string]any{"text": "environment"}}},
	}).Set("tools", []any{map[string]any{"name": "remove"}, map[string]any{"name": "keep"}}).Set("reasoning", map[string]any{"effort": "low", "summary": "auto"}).Set("extra", true)
}
func compareLegacy(t testing.TB, regs []middleware.Registration, body *piwire.OrderedMap) {
	t.Helper()
	client := map[string]any{"metadata": map[string]any{"a": 1}, "nullable": nil, "extra": false}
	oldBody := mustClone(body).(*piwire.OrderedMap)
	oldReq := &middleware.Request{Body: oldBody, Model: "old", Meta: map[string]any{"client_body": mustClone(client)}}
	oldChain, e := middleware.NewChain(regs, middleware.Registry())
	if e != nil {
		t.Fatal(e)
	}
	oldResult, e := oldChain.Apply(context.Background(), oldReq)
	if e != nil {
		t.Fatal(e)
	}
	migrated, e := MigrateLegacy(regs)
	if e != nil {
		t.Fatal(e)
	}
	res, e := compileTest(t, migrated...).Apply(context.Background(), PhaseRequest, &Input{Body: body, Model: "old", ClientBody: client, Trace: true})
	if e != nil {
		t.Fatal(e)
	}
	assertJSON(t, res.Body, oldReq.Body)
	if res.Model != oldReq.Model || res.Blocked != oldResult.Dropped || res.Reason != oldResult.Reason {
		t.Fatalf("migration changed result old=%+v model=%q new=%+v", oldResult, oldReq.Model, res)
	}
	oldJSON, _ := json.Marshal(oldReq.Body)
	newJSON, _ := json.Marshal(res.Body)
	if string(oldJSON) != string(newJSON) {
		t.Fatalf("ordered wire changed\nold %s\nnew %s", oldJSON, newJSON)
	}
}
func onlyLegacy(name, config string) []middleware.Registration {
	regs := []middleware.Registration{}
	for n := range middleware.Registry() {
		regs = append(regs, middleware.Registration{Name: n, Enabled: n == name, Order: 100, Config: "{}"})
	}
	for i := range regs {
		if regs[i].Name == name {
			regs[i].Config = config
		}
	}
	return regs
}
func TestMigrateLegacyAllEightEquivalent(t *testing.T) {
	configs := map[string][]string{
		"drop_environment_context": {`{}`, `{"content_item_kind":"custom","also_strip_from_instructions":false}`},
		"drop_input_items":         {`{}`, `{"types":["reasoning"],"pattern":"block.me","target":"text","mode":"unknown"}`, `{"types":"reasoning, metadata","pattern":"match","target":"unsupported","mode":true}`, `{"types":[1,"reasoning",null]}`},
		"drop_tools":               {`{}`, `{"names":["remove"]}`, `{"names":"remove, keep"}`, `{"drop_all":true}`},
		"drop_fields":              {`{}`, `{"fields":["extra","tools",""]}`, `{"fields":"extra,not-present"}`},
		"rewrite_model":            {`{}`, `{"model":"new"}`, `{"model":"  "}`},
		"passthrough_fields":       {`{}`, `{"fields":["extra","metadata","nullable","", "absent"]}`},
		"set_reasoning":            {`{}`, `{"effort":"custom"}`, `{"summary":"concise"}`, `{"effort":null,"summary":4}`},
		"block_prompt":             {`{}`, `{"pattern":"block.me"}`, `{"pattern":"NO MATCH"}`, `{"pattern":"  "}`},
	}
	for name, items := range configs {
		for i, cfg := range items {
			t.Run(fmt.Sprintf("%s/%d", name, i), func(t *testing.T) { compareLegacy(t, onlyLegacy(name, cfg), legacyInput()) })
		}
	}
}
func TestMigrateDefaultsPartialOrderingAndNoops(t *testing.T) {
	compareLegacy(t, nil, legacyInput())
	compareLegacy(t, []middleware.Registration{{Name: "drop_environment_context", Enabled: false, Order: -5}, {Name: "rewrite_model", Enabled: true, Order: 1, Config: `{"model":"new"}`}, {Name: "drop_fields", Enabled: true, Order: 1, Config: `{"fields":["extra"]}`}}, legacyInput())
	rs, e := MigrateLegacy(nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(rs) != 8 {
		t.Fatal("missing registry defaults")
	}
	last := ""
	for i, r := range rs {
		if !r.Enabled || r.Priority != 100 || r.OrderIndex != int64(i) || r.ID != "legacy-"+r.Name || r.LegacyName != r.Name {
			t.Fatalf("wrong defaults %+v", r)
		}
		if r.Name < last {
			t.Fatal("wrong equal-priority order")
		}
		last = r.Name
		if r.Name == "block_prompt" || r.Name == "rewrite_model" {
			if len(r.Actions) != 0 || r.When.Op != "always" {
				t.Fatal("legacy noop changed")
			}
		}
	}
	// The old chain resolves duplicate registrations by the last row and ignores unknown names.
	compareLegacy(t, []middleware.Registration{{Name: "rewrite_model", Enabled: true, Config: `{"model":"discarded"}`}, {Name: "rewrite_model", Enabled: true, Order: 2, Config: `{"model":"last"}`}, {Name: "unknown", Enabled: true, Config: `invalid`}}, legacyInput())
}
func TestMigrateInvalidConfigsNeverSilentlyRepair(t *testing.T) {
	for _, reg := range []middleware.Registration{{Name: "block_prompt", Enabled: true, Config: `{"pattern":"["}`}, {Name: "drop_input_items", Enabled: false, Config: `{"pattern":"["}`}, {Name: "drop_tools", Config: `[`}, {Name: "drop_fields", Config: `{"fields":["model"]}`}} {
		if _, e := MigrateLegacy([]middleware.Registration{reg}); e == nil || !strings.Contains(e.Error(), reg.Name) {
			t.Fatalf("bad legacy config accepted: %+v %v", reg, e)
		}
	}
}
