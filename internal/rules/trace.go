package rules

import (
	"encoding/json"
	"fmt"
	"strings"
)

type traceCollector struct {
	traces    []Trace
	omitted   int
	enabled   bool
	maxBytes  int
	maxTraces int
	usedBytes int
}

func newTraceCollector(in *Input) *traceCollector {
	c := &traceCollector{enabled: in != nil && in.Trace, maxBytes: in.TraceMaxBytes, maxTraces: in.MaxTraces}
	if c.maxBytes <= 0 {
		c.maxBytes = 1 << 20
	}
	if c.maxTraces <= 0 {
		c.maxTraces = 4096
	}
	return c
}
func (c *traceCollector) remainingBytes() int { return c.maxBytes - c.usedBytes }
func (c *traceCollector) remainingCount() int { return c.maxTraces - len(c.traces) }
func (c *traceCollector) canCollect() bool {
	return c.enabled && len(c.traces) < c.maxTraces && c.usedBytes < c.maxBytes
}
func (c *traceCollector) add(t Trace) {
	if !c.enabled {
		return
	}
	if !c.canCollect() {
		c.omitted++
		return
	}
	t = boundTrace(t)
	b, _ := json.Marshal(t)
	if len(b) > c.maxBytes-c.usedBytes {
		c.omitted++
		return
	}
	c.traces = append(c.traces, t)
	c.usedBytes += len(b)
}
func (c *traceCollector) merge(other *traceCollector) {
	if !c.enabled || other == nil {
		return
	}
	for _, t := range other.traces {
		c.add(t)
	}
	c.omitted += other.omitted
}
func boundTrace(t Trace) Trace {
	for i := range t.Changes {
		change := &t.Changes[i]
		before, bt := boundValue(change.Before, change.Path)
		after, at := boundValue(change.After, change.Path)
		change.Before = before
		change.After = after
		change.Truncated = change.Truncated || bt || at
	}
	if len(t.Error) > 512 {
		t.Error = t.Error[:512]
	}
	return t
}
func sensitiveKey(k string) bool {
	k = strings.ToLower(k)
	return contains([]string{"authorization", "proxy-authorization", "api_key", "access_token", "refresh_token", "password", "secret", "cookie", "set-cookie"}, k)
}
func redact(v any) any {
	if m, ok := object(v); ok {
		out := map[string]any{}
		for k, x := range m {
			if sensitiveKey(k) {
				out[k] = "[REDACTED]"
			} else {
				out[k] = redact(x)
			}
		}
		return out
	}
	if a, ok := v.([]any); ok {
		out := make([]any, len(a))
		for i, x := range a {
			out[i] = redact(x)
		}
		return out
	}
	return v
}
func boundValue(v any, path string) (any, bool) {
	parts, _ := pointerSegments(path)
	for _, k := range parts {
		if sensitiveKey(k) {
			return "[REDACTED]", false
		}
	}
	b, e := json.Marshal(v)
	if e != nil {
		return "<unserializable>", true
	}
	if len(b) > MaxTraceValueBytes {
		return fmt.Sprintf("<omitted %d bytes>", len(b)), true
	}
	return redact(v), false
}
func structuralDiff(before, after any) ([]Change, int) {
	out := []Change{}
	omitted := 0
	add := func(c Change) {
		if len(out) >= MaxTraceChanges {
			omitted++
			return
		}
		out = append(out, c)
	}
	var walk func(any, any, string)
	walk = func(a, b any, path string) {
		if jsonEqual(a, b) {
			return
		}
		_, aok := object(a)
		_, bok := object(b)
		if aok && bok {
			seen := map[string]bool{}
			for _, k := range keys(a) {
				seen[k] = true
				av, _ := objGet(a, k)
				bv, be := objGet(b, k)
				p := joinPointer(path, k)
				if !be {
					add(Change{Path: p, Operation: "remove", BeforeExists: true, Before: av})
					continue
				}
				walk(av, bv, p)
			}
			for _, k := range keys(b) {
				if !seen[k] {
					bv, _ := objGet(b, k)
					add(Change{Path: joinPointer(path, k), Operation: "add", AfterExists: true, After: bv})
				}
			}
			return
		}
		aa, aok := a.([]any)
		bb, bok := b.([]any)
		if aok && bok {
			i, j := 0, 0
			for i < len(aa) && j < len(bb) {
				if jsonEqual(aa[i], bb[j]) {
					i++
					j++
					continue
				}
				// Align surviving messages after deletions/insertions instead of
				// reporting every following message as a replacement.
				left, right := -1, -1
				for k := i + 1; k < len(aa); k++ {
					if jsonEqual(aa[k], bb[j]) {
						left = k
						break
					}
				}
				for k := j + 1; k < len(bb); k++ {
					if jsonEqual(aa[i], bb[k]) {
						right = k
						break
					}
				}
				if left >= 0 && (right < 0 || left-i <= right-j) {
					for i < left {
						add(Change{Path: fmt.Sprintf("%s/%d", path, i), Operation: "remove", BeforeExists: true, Before: aa[i]})
						i++
					}
				} else if right >= 0 {
					for j < right {
						add(Change{Path: fmt.Sprintf("%s/%d", path, j), Operation: "add", AfterExists: true, After: bb[j]})
						j++
					}
				} else {
					walk(aa[i], bb[j], fmt.Sprintf("%s/%d", path, j))
					i++
					j++
				}
			}
			for ; i < len(aa); i++ {
				add(Change{Path: fmt.Sprintf("%s/%d", path, i), Operation: "remove", BeforeExists: true, Before: aa[i]})
			}
			for ; j < len(bb); j++ {
				add(Change{Path: fmt.Sprintf("%s/%d", path, j), Operation: "add", AfterExists: true, After: bb[j]})
			}
			return
		}
		add(Change{Path: path, Operation: "replace", BeforeExists: true, AfterExists: true, Before: a, After: b})
	}
	walk(before, after, "")
	return out, omitted
}
