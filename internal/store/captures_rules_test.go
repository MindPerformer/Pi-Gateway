package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCaptureRuleTraceRoundTripAndLightweightList(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	raw := json.RawMessage(`{"rule_id":"deleted-rule","rule_name":"historical name","status":"changed","changes":[{"path":"/text","before_exists":true,"after_exists":true,"before":null,"after":"new"}]}`)
	c := &Capture{AccountID: 0, Outcome: OutcomeError, RequestBody: "actual-request", ResponseText: "actual-response", RulesVersion: 42, RulesTraceTruncated: true, RulesTraceOmitted: 3, RuleTraces: []json.RawMessage{raw}, ResponseFrames: []Frame{{Seq: 2, Dir: "in", Kind: KindSSEEvent, RuleEventID: "event-2"}}}
	if err := s.InsertCapture(ctx, c, 10); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCapture(ctx, c.ID)
	if err != nil || got == nil {
		t.Fatalf("get capture: %+v %v", got, err)
	}
	if got.RulesVersion != 42 || !got.RulesTraceTruncated || got.RulesTraceOmitted != 3 || len(got.RuleTraces) != 1 || string(got.RuleTraces[0]) != string(raw) {
		t.Fatalf("lost trace metadata: %+v", got)
	}
	if len(got.ResponseFrames) != 1 || got.ResponseFrames[0].RuleEventID != "event-2" {
		t.Fatal("frame event association was not persisted")
	}
	rows, total, err := s.ListCaptures(ctx, CaptureFilter{})
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("list: total=%d rows=%d err=%v", total, len(rows), err)
	}
	if len(rows[0].RuleTraces) != 0 || rows[0].RuleTraces == nil || len(rows[0].ResponseFrames) != 0 || rows[0].RequestBody != "" || rows[0].ResponseText != "" {
		t.Fatal("summary listing loaded heavy payload")
	}
	if rows[0].RulesVersion != 42 || rows[0].RulesTraceOmitted != 3 || !rows[0].RulesTraceTruncated {
		t.Fatal("summary listing lost lightweight metadata")
	}
	// Export uses IncludePayloads and must retain independent rule provenance.
	export, _, err := s.ListCaptures(ctx, CaptureFilter{IncludePayloads: true})
	if err != nil || len(export) != 1 || len(export[0].RuleTraces) != 1 {
		t.Fatalf("export omitted rule sources: %+v %v", export, err)
	}
	encoded, err := json.Marshal(export[0])
	if err != nil || !strings.Contains(string(encoded), `"rule_traces":[{`) || strings.Contains(string(encoded), `"rule_traces":"`) {
		t.Fatalf("traces were encoded as a string: %s %v", encoded, err)
	}
}

func TestCaptureUnassignedRetentionFilterAndSafeClear(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	insert := func(accountID int64) *Capture {
		t.Helper()
		c := &Capture{AccountID: accountID, Model: "test", Outcome: OutcomeError}
		if err := s.InsertCapture(ctx, c, 2); err != nil {
			t.Fatal(err)
		}
		return c
	}
	assigned := insert(7)
	first := insert(0)
	insert(0)
	last := insert(0)
	if got, err := s.GetCapture(ctx, first.ID); err != nil || got != nil {
		t.Fatalf("unassigned history was not trimmed: %+v %v", got, err)
	}
	if got, err := s.GetCapture(ctx, assigned.ID); err != nil || got == nil {
		t.Fatalf("unassigned trim deleted assigned history: %+v %v", got, err)
	}
	rows, total, err := s.ListCaptures(ctx, CaptureFilter{Unassigned: true})
	if err != nil || total != 2 || len(rows) != 2 || rows[0].ID != last.ID {
		t.Fatalf("unassigned filter failed: %+v %d %v", rows, total, err)
	}
	for _, c := range rows {
		if c.AccountID != 0 {
			t.Fatal("assigned row escaped unassigned scope")
		}
	}
	// Explicit unassigned scope must never fall through to all rows.
	_, total, err = s.ListCaptures(ctx, CaptureFilter{Unassigned: true, AccountID: 7})
	if err != nil || total != 2 {
		t.Fatalf("conflicting filter escaped unassigned scope: %d %v", total, err)
	}
	deleted, err := s.ClearUnassignedCaptures(ctx)
	if err != nil || deleted != 2 {
		t.Fatalf("clear unassigned = %d %v", deleted, err)
	}
	if got, err := s.GetCapture(ctx, assigned.ID); err != nil || got == nil {
		t.Fatal("unassigned clear deleted assigned history")
	}
	rows, total, err = s.ListCaptures(ctx, CaptureFilter{})
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("remaining capture count = %d %v", total, err)
	}
	if rows[0].RuleTraces == nil {
		t.Fatal("empty historical trace must serialize to [], not null")
	}
	deleted, err = s.ClearCaptures(ctx, 0)
	if err != nil || deleted != 1 {
		t.Fatalf("historical clear-all behavior changed: %d %v", deleted, err)
	}
}

func TestCaptureRejectsInvalidTraceJSON(t *testing.T) {
	s := openTestStore(t)
	if err := s.InsertCapture(context.Background(), &Capture{RuleTraces: []json.RawMessage{json.RawMessage(`{"bad":`)}}, 0); err == nil {
		t.Fatal("invalid raw trace was stored")
	}
}
