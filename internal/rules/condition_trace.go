package rules

import (
	"encoding/json"
	"strings"
)

type conditionCollector struct {
	traces                             []ConditionTrace
	omitted, bytes, maxBytes, maxCount int
}

func newConditionCollector(in *Input) *conditionCollector {
	if !in.ConditionTrace {
		return nil
	}
	bytes, count := in.TraceMaxBytes, in.MaxTraces
	if bytes <= 0 || bytes > 256<<10 {
		bytes = 256 << 10
	}
	if count <= 0 || count > 2048 {
		count = 2048
	}
	return &conditionCollector{maxBytes: bytes, maxCount: count}
}

func (c *conditionCollector) record(node *compiledCondition, s *evaluation, matched bool, err error, skipped bool) {
	if len(c.traces) >= c.maxCount || c.bytes >= c.maxBytes {
		c.omitted++
		return
	}
	t := ConditionTrace{RuleID: s.ruleID, Path: strings.TrimPrefix(node.path, s.rulePath), Status: "not_matched"}
	if skipped {
		t.Status = "skipped"
	} else if err != nil {
		t.Status = "error"
		t.Error = err.Error()
		if len(t.Error) > 1024 {
			t.Error = t.Error[:1024]
		}
	} else {
		t.Matched = &matched
		if matched {
			t.Status = "matched"
		}
	}
	if s.hasItem {
		index := s.index
		t.ItemIndex = &index
	}
	raw, _ := json.Marshal(t)
	if c.bytes+len(raw) > c.maxBytes {
		c.omitted++
		return
	}
	c.bytes += len(raw)
	c.traces = append(c.traces, t)
}
