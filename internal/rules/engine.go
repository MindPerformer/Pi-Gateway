package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Engine is an immutable snapshot, safe for concurrent request and response execution.
type Engine struct{ phases map[string][]compiledRule }

// Compile validates every rule (including disabled rules), snapshots all values and
// precompiles every regular expression. There is deliberately no rule-count limit.
func Compile(definitions []Rule) (*Engine, error) {
	e := &Engine{phases: map[string][]compiledRule{}}
	ids := map[string]bool{}
	for i, r := range definitions {
		path := fmt.Sprintf("/%d", i)
		if r.ID != "" && ids[r.ID] {
			return nil, invalid(path+"/id", "duplicate rule ID %q", r.ID)
		}
		ids[r.ID] = true
		if r.OnError == "" {
			r.OnError = OnErrorAbort
		}
		r.Actions = append([]Action{}, r.Actions...)
		for j := range r.Actions {
			if r.Actions[j].Params == nil {
				r.Actions[j].Params = map[string]any{}
			}
		}
		// A JSON round trip detaches recursively aliased slices/maps and retains explicit null.
		raw, err := json.Marshal(r)
		if err != nil {
			return nil, invalid(path, "invalid JSON rule: %v", err)
		}
		if len(raw) > MaxRuleBytes {
			return nil, invalid(path, "rule exceeds %d bytes", MaxRuleBytes)
		}
		copy, err := ParseRule(raw)
		if err != nil {
			return nil, prefixError(path, err)
		}
		cr, err := compileRule(copy, path)
		if err != nil {
			return nil, err
		}
		e.phases[r.Phase] = append(e.phases[r.Phase], cr)
	}
	for phase := range e.phases {
		sort.SliceStable(e.phases[phase], func(i, j int) bool {
			a, b := e.phases[phase][i].raw, e.phases[phase][j].raw
			if a.Priority != b.Priority {
				return a.Priority < b.Priority
			}
			if a.OrderIndex != b.OrderIndex {
				return a.OrderIndex < b.OrderIndex
			}
			return a.ID < b.ID
		})
	}
	return e, nil
}

// HasPhase reports whether an enabled rule exists in this phase (including no-op rules).
func (e *Engine) HasPhase(phase string) bool {
	if e == nil {
		return false
	}
	for _, r := range e.phases[phase] {
		if r.raw.Enabled {
			return true
		}
	}
	return false
}

// Apply evaluates a single phase. Each matched rule commits atomically; both abort
// and skip_rule roll back all actions of the failing rule, never prior committed rules.
func (e *Engine) Apply(ctx context.Context, phase string, in *Input) (Result, error) {
	if !contains(phases, phase) {
		return Result{}, invalid("/phase", "unknown phase")
	}
	if in == nil {
		return Result{}, invalid("/input", "input is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{Body: in.Body, Model: in.Model}, err
	}
	if !e.HasPhase(phase) {
		return Result{Body: in.Body, Model: in.Model}, nil
	}
	body, err := cloneJSON(in.Body)
	if err != nil {
		return Result{}, invalid("/body", "%v", err)
	}
	if err = ensurePayload(body); err != nil {
		return Result{}, invalid("/body", "%v", err)
	}
	client, err := cloneJSON(in.ClientBody)
	if err != nil {
		return Result{}, invalid("/client_body", "%v", err)
	}
	facts := map[string]any{}
	if in.Context != nil {
		c, err := cloneJSON(in.Context)
		if err != nil {
			return Result{}, invalid("/context", "%v", err)
		}
		facts = c.(map[string]any)
	}
	s := evaluation{ctx: ctx, body: body, client: client, facts: facts, model: in.Model, originalModel: in.Model, eventType: in.EventType, phase: phase}
	if phase == PhaseRequest {
		if _, ok := object(body); !ok {
			return Result{}, invalid("/body", "request body must be an object")
		}
		if m, ok := objGet(body, "model"); ok {
			model, ok := m.(string)
			if !ok || strings.TrimSpace(model) == "" {
				return Result{}, invalid("/body/model", "model must be a nonempty string")
			}
			if in.Model != "" && in.Model != model {
				return Result{}, invalid("/model", "does not match body model")
			}
			s.model = model
			s.originalModel = model
		}
	}
	if original, ok := facts["original_model"].(string); ok {
		s.originalModel = original
	}
	if phase == PhaseResponseEvent {
		if _, ok := object(body); !ok {
			return Result{}, invalid("/body", "response event must be an object")
		}
		if t, ok := objGet(body, "type"); ok {
			str, ok := t.(string)
			if !ok {
				return Result{}, invalid("/body/type", "event type must be a string")
			}
			if s.eventType == "" {
				s.eventType = str
			} else if s.eventType != str {
				return Result{}, invalid("/event_type", "does not match body type")
			}
		}
	}
	res := Result{Body: s.body, Model: s.model}
	collector := newTraceCollector(in)
	baseline := protectedProjection(body, phase)
	finish := func(err error) (Result, error) {
		res.Body = s.body
		res.Model = s.model
		res.Changed = !jsonEqual(in.Body, s.body) || in.Model != s.model
		res.Traces = collector.traces
		res.TraceOmitted = collector.omitted
		return res, err
	}
	if e == nil {
		return finish(nil)
	}
	for _, r := range e.phases[phase] {
		if err := ctx.Err(); err != nil {
			return finish(err)
		}
		if !r.raw.Enabled {
			continue
		}
		start := time.Now()
		matched, matchErr := r.when.matches(&s)
		if matchErr != nil {
			t := baseTrace(r, &s)
			t.Status = "error"
			t.Error = matchErr.Error()
			t.DurationNS = time.Since(start).Nanoseconds()
			collector.add(t)
			if ctx.Err() != nil || r.raw.OnError != OnErrorSkipRule {
				return finish(prefixError(r.path+"/when", matchErr))
			}
			continue
		}
		if !matched {
			t := baseTrace(r, &s)
			t.Status = "not_matched"
			t.DurationNS = time.Since(start).Nanoseconds()
			collector.add(t)
			continue
		}
		if len(r.actions) == 0 {
			t := baseTrace(r, &s)
			t.Matched = true
			t.Status = "no_change"
			t.DurationNS = time.Since(start).Nanoseconds()
			collector.add(t)
			if r.raw.StopAfterMatch {
				break
			}
			continue
		}
		candidate := s
		candidate.body, err = cloneJSON(s.body)
		if err != nil {
			return finish(err)
		}
		pending := newTraceCollector(in)
		pending.maxBytes = collector.remainingBytes()
		pending.maxTraces = collector.remainingCount()
		pending.enabled = collector.enabled
		var actionErr error
		var outcome terminal
		var errorPath string
		for i, a := range r.actions {
			actionStart := time.Now()
			var before any
			beforeModel := candidate.model
			if pending.canCollect() {
				before, err = cloneJSON(candidate.body)
				if err != nil {
					return finish(err)
				}
			}
			outcome, actionErr = a.execute(&candidate)
			if actionErr == nil {
				actionErr = ensurePayload(candidate.body)
			}
			if actionErr == nil {
				actionErr = compareProjection(baseline, protectedProjection(candidate.body, phase))
			}
			if actionErr == nil && phase == PhaseRequest {
				actionErr = syncModel(&candidate, &s)
			}
			if actionErr == nil && phase == PhaseResponseEvent {
				if _, ok := object(candidate.body); !ok {
					actionErr = fmt.Errorf("response event must remain an object")
				}
			}
			t := baseTrace(r, &candidate)
			t.ActionID = a.raw.ID
			t.ActionType = a.raw.Type
			t.ActionIndex = i
			t.Matched = true
			t.DurationNS = time.Since(actionStart).Nanoseconds()
			if actionErr != nil {
				t.Status = "error"
				t.Error = actionErr.Error()
				t.RolledBack = true
				pending.add(t)
				errorPath = a.path
				break
			}
			t.Status = "no_change"
			if pending.canCollect() {
				t.Changes, t.OmittedChanges = structuralDiff(before, candidate.body)
				if len(t.Changes) > 0 || t.OmittedChanges > 0 || beforeModel != candidate.model {
					t.Status = "changed"
				}
			}
			if outcome.blocked {
				t.Status = "blocked"
			}
			if outcome.dropped {
				t.Status = "dropped"
			}
			pending.add(t)
			if outcome.blocked || outcome.dropped {
				break
			}
		}
		if actionErr != nil {
			for i := range pending.traces {
				pending.traces[i].RolledBack = true
				pending.traces[i].Changes = nil
				pending.traces[i].OmittedChanges = 0
				if pending.traces[i].Status != "error" {
					pending.traces[i].Status = "rolled_back"
				} else if r.raw.OnError == OnErrorSkipRule {
					pending.traces[i].Status = "skipped"
				}
			}
			collector.merge(pending)
			if ctx.Err() != nil {
				return finish(ctx.Err())
			}
			if r.raw.OnError == OnErrorSkipRule {
				continue
			}
			return finish(prefixError(errorPath, actionErr))
		}
		s = candidate
		collector.merge(pending)
		if outcome.blocked {
			res.Blocked = true
			res.Status = outcome.status
			res.Reason = outcome.reason
			return finish(nil)
		}
		if outcome.dropped {
			res.Dropped = true
			return finish(nil)
		}
		if r.raw.StopAfterMatch {
			break
		}
	}
	return finish(nil)
}
func syncModel(s, previous *evaluation) error {
	if _, ok := object(s.body); !ok {
		return fmt.Errorf("request body must remain an object")
	}
	v, ok := objGet(s.body, "model")
	if !ok {
		if _, had := objGet(previous.body, "model"); had {
			return fmt.Errorf("request model cannot be removed")
		}
		return nil
	}
	str, ok := v.(string)
	if !ok || strings.TrimSpace(str) == "" {
		return fmt.Errorf("request model must be a nonempty string")
	}
	s.model = str
	return nil
}
func compareProjection(a, b map[string]any) error {
	for _, k := range keys(a) {
		v := a[k]
		w, ok := b[k]
		if !ok || !jsonEqual(v, w) {
			return fmt.Errorf("protected field %s was changed or removed", k)
		}
	}
	for _, k := range keys(b) {
		if _, ok := a[k]; !ok {
			return fmt.Errorf("protected field %s was added", k)
		}
	}
	return nil
}
func baseTrace(r compiledRule, s *evaluation) Trace {
	eventID, _ := s.facts["event_id"].(string)
	return Trace{Source: "rule", RuleID: r.raw.ID, RuleName: r.raw.Name, Revision: r.raw.Revision, Priority: r.raw.Priority, Phase: r.raw.Phase, ActionIndex: -1, EventType: s.eventType, EventID: eventID, Sequence: s.facts["sequence"]}
}
