package pricing

import "testing"

func TestExplainLongContextFast(t *testing.T) {
	for _, tc := range []struct {
		model      string
		multiplier float64
	}{
		{"gpt-6-sol", 2}, {"gpt-5.4", 2}, {"gpt-5.5", 2.5}, {"gpt-5.4-pro", 2.5}, {"gpt-5.5-pro", 2.5},
	} {
		d := Explain(tc.model, "priority", nil, 300000, 100000, 0, 10000)
		if !d.LongContext || d.ContextThreshold != 272000 || d.TotalCostMicros == nil || d.UnavailableReason != "" {
			t.Fatalf("%s: %+v", tc.model, d)
		}
		if d.ContextMultipliers.Input != 2 || d.ContextMultipliers.Output != 1.5 || d.TierMultipliers.Input != tc.multiplier || d.TierMultipliers.Output != tc.multiplier {
			t.Fatalf("%s multipliers: %+v %+v", tc.model, d.ContextMultipliers, d.TierMultipliers)
		}
		r, _, _ := Lookup(tc.model, "priority", nil)
		want, ok := Cost(r, 300000, 100000, 0, 10000)
		if !ok || want != *d.TotalCostMicros {
			t.Fatalf("breakdown and settlement differ: %+v", d)
		}
	}
}

func TestExplainContextAndTierBoundaries(t *testing.T) {
	short := Explain("gpt-5.4", "default", nil, 271999, 200000, 0, 1000)
	long := Explain("gpt-5.4", "priority", nil, 272000, 200000, 0, 1000)
	if short.LongContext || !long.LongContext || long.ContextRates.Input != 5 || long.EffectiveRates.Input != 10 || long.ContextRates.Output != 22.5 || long.EffectiveRates.Output != 45 {
		t.Fatalf("boundary prices: %+v %+v", short, long)
	}
	if short.TotalCostMicros == nil || long.TotalCostMicros == nil || *long.TotalCostMicros <= *short.TotalCostMicros {
		t.Fatal("tier/context did not increase cost")
	}
	unknown := Explain("gpt-5.6-cyber", "default", nil, 272000, 0, 0, 0)
	if unknown.TotalCostMicros != nil || unknown.UnavailableReason != "unpriced_context" {
		t.Fatalf("invented unknown long price: %+v", unknown)
	}
	zero := Explain("gpt-6-sol", "fast", map[string]float64{"gpt-6-sol": 0}, 300000, 100, 0, 0)
	if zero.TotalCostMicros == nil || *zero.TotalCostMicros != 0 {
		t.Fatal("zero override became unknown")
	}
	plain := Explain("gpt-5", "default", nil, 300000, 0, 0, 0)
	if plain.LongContext || plain.ContextMultipliers.Input != 1 {
		t.Fatal("model without long prices switched bands")
	}
}
