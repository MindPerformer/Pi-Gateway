package rules

import (
	"context"
	"fmt"
	"testing"
)

func BenchmarkCompileRules(b *testing.B) {
	for _, n := range []int{1, 100, 1000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			rs := make([]Rule, n)
			for i := range rs {
				rs[i] = rule(fmt.Sprintf("r-%d", i), act("json_set", map[string]any{"path": fmt.Sprintf("/v/%d", i), "value": i}))
				rs[i].OrderIndex = int64(i)
			}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, e := Compile(rs); e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}
func BenchmarkApplyRules(b *testing.B) {
	for _, n := range []int{1, 100, 1000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			rs := make([]Rule, n)
			for i := range rs {
				rs[i] = rule(fmt.Sprintf("r-%d", i))
				rs[i].OrderIndex = int64(i)
			}
			rs[n-1].Actions = []Action{act("json_set", map[string]any{"path": "/last", "value": n})}
			e, err := Compile(rs)
			if err != nil {
				b.Fatal(err)
			}
			in := &Input{Body: map[string]any{}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				res, e := e.Apply(context.Background(), PhaseRequest, in)
				if e != nil || !res.Changed {
					b.Fatal(e)
				}
			}
		})
	}
}

// BenchmarkApplyMutatingRules measures every rule performing a committed write,
// unlike BenchmarkApplyRules which measures scanning no-op rules plus the final write.
func BenchmarkApplyMutatingRules(b *testing.B) {
	for _, n := range []int{1, 100, 1000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			rs := make([]Rule, n)
			for i := range rs {
				rs[i] = rule(fmt.Sprintf("r-%d", i), act("json_set", map[string]any{"path": "/value", "value": i}))
				rs[i].OrderIndex = int64(i)
			}
			e, err := Compile(rs)
			if err != nil {
				b.Fatal(err)
			}
			in := &Input{Body: map[string]any{}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				res, err := e.Apply(context.Background(), PhaseRequest, in)
				if err != nil || !res.Changed {
					b.Fatal(err)
				}
			}
		})
	}
}
func BenchmarkApplyResponseEventNoRules(b *testing.B) {
	e, err := Compile(nil)
	if err != nil {
		b.Fatal(err)
	}
	body := map[string]any{"type": "response.output_text.delta", "delta": "hello"}
	in := &Input{Body: body, ClientBody: map[string]any{"history": make([]any, 1024)}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res, e := e.Apply(context.Background(), PhaseResponseEvent, in)
		if e != nil || res.Body == nil || res.Changed {
			b.Fatal(e)
		}
	}
}
