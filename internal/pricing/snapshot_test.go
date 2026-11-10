package pricing

import (
	"math"
	"strings"
	"testing"
)

func mustSnapshot(t *testing.T, models ...ModelPrice) *Snapshot {
	t.Helper()
	s, err := NewSnapshot("test-v1", models)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDecimalValidation(t *testing.T) {
	for _, value := range []string{"", "0", "0.000000000000000001", "999999999999999999.999999999999999999", "00.01", "1.25"} {
		if err := ValidateModel(ModelPrice{Model: "m", Standard: &DecimalRates{Input: value}}); err != nil {
			t.Errorf("valid %q: %v", value, err)
		}
	}
	for _, value := range []string{"-1", "+1", "NaN", "Infinity", "1e3", " 1", "1 ", ".5", "1.", "1/2", "1.0000000000000000001", strings.Repeat("1", 19)} {
		if err := ValidateModel(ModelPrice{Model: "m", Standard: &DecimalRates{Input: value}}); err == nil {
			t.Errorf("accepted invalid %q", value)
		}
	}
	for _, m := range []ModelPrice{{}, {Model: "m", ContextMode: "invalid"}, {Model: "m", ContextMode: "custom"}, {Model: "m", ContextComparison: "eq"}, {Model: "m", ContextThreshold: -1}} {
		if ValidateModel(m) == nil {
			t.Errorf("accepted invalid model %+v", m)
		}
	}
}

func TestSnapshotMissingRatesAreNotFree(t *testing.T) {
	s := mustSnapshot(t, ModelPrice{Model: "m", Standard: &DecimalRates{Input: "1", Output: "0"}})
	for _, tc := range []struct {
		input, cached, write, output int64
		known                        bool
		cost                         int64
	}{
		{10, 0, 0, 2, true, 10}, {10, 2, 0, 0, false, 0}, {10, 0, 1, 0, false, 0}, {0, 0, 0, 100, true, 0}, {0, 0, 0, 0, true, 0},
	} {
		d := s.Explain("m", "", nil, tc.input, tc.cached, tc.write, tc.output)
		if (d.TotalCostMicros != nil) != tc.known {
			t.Fatalf("unexpected availability: %+v", d)
		}
		if tc.known && *d.TotalCostMicros != tc.cost {
			t.Fatalf("unexpected cost: %+v", d)
		}
		if !tc.known && d.UnavailableReason != "unpriced_rates" {
			t.Fatalf("missing reason: %+v", d)
		}
	}
	d := s.Explain("m", "", map[string]float64{"m": 0}, 10, 1, 0, 0)
	if d.TotalCostMicros != nil {
		t.Fatal("zero override must not invent an unknown cache rate")
	}
	known := s.Explain("m", "", nil, 10, 0, 0, 1)
	if known.EffectiveDecimalRates == nil || known.EffectiveDecimalRates.CachedInput != "" || known.EffectiveDecimalRates.Output != "0" || known.CalculationVersion != Version {
		t.Fatalf("unknown/free display or algorithm version lost: %+v", known)
	}
	flexOnly := mustSnapshot(t, ModelPrice{Model: "m", Flex: &DecimalRates{Output: "1"}})
	d = flexOnly.Explain("m", "flex", nil, 0, 0, 0, 10)
	if d.TotalCostMicros == nil || *d.TotalCostMicros != 10 {
		t.Fatalf("missing unused standard tier poisoned flex: %+v", d)
	}
}

func TestSnapshotPreciseDecimalAndFinalOverride(t *testing.T) {
	s := mustSnapshot(t, ModelPrice{Model: "m", Standard: &DecimalRates{Input: "0.500000000000000001", CachedInput: "0.2", CacheWrite: "0.3", Output: "0.4"}})
	r, _, _ := s.Lookup("m", "", nil)
	// Float conversion loses the final digit; exact decimal multiplication must not.
	requireCost(t, r, 1000000000000000001, 0, 0, 0, 500000000000000002)
	r, source, ok := s.Lookup("m", "", map[string]float64{"m": 0.1})
	if !ok || source != "override" {
		t.Fatal("missing override")
	}
	requireCost(t, r, 1000000000000000001, 0, 0, 0, 50000000000000000)
	s = mustSnapshot(t, ModelPrice{Model: "m", Standard: &DecimalRates{Input: "0.1", CachedInput: "0.2", CacheWrite: "0.3", Output: "0.4"}})
	r, _, _ = s.Lookup("m", "", nil)
	requireCost(t, r, 3, 1, 1, 1, 1)
	for _, f := range []float64{-1, math.NaN(), math.Inf(1)} {
		if _, _, ok := s.Lookup("m", "", map[string]float64{"m": f}); ok {
			t.Fatal("accepted invalid multiplier")
		}
	}
}

func TestSnapshotImmutableAndProvenance(t *testing.T) {
	m := ModelPrice{Model: "m", Aliases: []string{"alias"}, Standard: &DecimalRates{Input: "1"}, LongStandard: &DecimalRates{Input: "2"}, ContextMode: "custom", ContextThreshold: 10, Source: "external", SourceID: "source-1", SourceURL: "https://example.invalid/pricing"}
	s := mustSnapshot(t, m)
	m.Standard.Input, m.Aliases[0] = "999", "bad"
	returned := s.Models()
	returned[0].Standard.Input, returned[0].LongStandard.Input, returned[0].Aliases[0] = "888", "888", "bad"
	r, _, _ := s.Lookup("alias", "", nil)
	r.LongContext.Input = 777
	d := s.Explain("alias", "", nil, 10, 0, 0, 0)
	if d.TotalCostMicros == nil || *d.TotalCostMicros != 20 || d.Model != "m" || d.Version != "test-v1" || d.CatalogVersion != "test-v1" || d.SourceID != "source-1" || d.SourceURL != m.SourceURL || d.PriceSource != "external" {
		t.Fatalf("mutation or provenance error: %+v", d)
	}
	models := BuiltinModels()
	models[0].Standard.Input = "999999"
	if BuiltinModels()[0].Standard.Input == "999999" {
		t.Fatal("builtin model mutation leaked")
	}
}

func TestSnapshotExactNamesAliasesDisabledAndCycles(t *testing.T) {
	s := mustSnapshot(t,
		ModelPrice{Model: "openai/foo", Standard: &DecimalRates{Input: "1"}},
		ModelPrice{Model: "other/foo", Aliases: []string{"other-alias"}, Standard: &DecimalRates{Input: "2"}},
		ModelPrice{Model: "blocked", Disabled: true, Standard: &DecimalRates{Input: "1"}},
	)
	for _, tc := range []struct {
		model string
		want  int64
	}{{"openai/foo", 1}, {"other/foo", 2}, {"other-alias", 2}} {
		r, _, ok := s.Lookup(tc.model, "", nil)
		if !ok {
			t.Fatal(tc.model)
		}
		requireCost(t, r, 1, 0, 0, 0, tc.want)
	}
	for _, model := range []string{"foo", "vendor/foo", "blocked", "other/foo-future", "vendor/other-alias"} {
		if _, _, ok := s.Lookup(model, "", nil); ok {
			t.Errorf("unexpected match %s", model)
		}
	}
	for _, models := range [][]ModelPrice{
		{{Model: "a"}, {Model: "a"}},
		{{Model: "a", Aliases: []string{"a"}}},
		{{Model: "a", Aliases: []string{"b"}}, {Model: "b", Aliases: []string{"a"}}},
		{{Model: "a", Aliases: []string{"x"}}, {Model: "b", Aliases: []string{"x"}}},
	} {
		if _, err := NewSnapshot("v", models); err == nil {
			t.Fatal("accepted conflicting aliases")
		}
	}
}

func TestBuiltinCanonicalOverridesRetainNormalizationAndDisable(t *testing.T) {
	for _, source := range []string{"manual", "external"} {
		s := mustSnapshot(t,
			ModelPrice{Model: "gpt-5", Aliases: []string{"gpt-5-2025-08-07"}, Source: source, Standard: &DecimalRates{Input: "3"}},
			ModelPrice{Model: "other/gpt-5", Standard: &DecimalRates{Input: "7"}},
		)
		for _, name := range []string{"openai/gpt-5", " /OPENAI/GPT-5 ", "openai/gpt-5-2025-08-07"} {
			r, gotSource, ok := s.Lookup(name, "", nil)
			if !ok || gotSource != source {
				t.Fatalf("canonical override no longer normalizes: %s", name)
			}
			requireCost(t, r, 1, 0, 0, 0, 3)
		}
		r, _, ok := s.Lookup("other/gpt-5", "", nil)
		if !ok {
			t.Fatal("explicit namespace lost")
		}
		requireCost(t, r, 1, 0, 0, 0, 7)
	}
	s := mustSnapshot(t, ModelPrice{Model: "gpt-5", Disabled: true, Source: "manual", Standard: &DecimalRates{Input: "1"}})
	for _, name := range []string{"gpt-5", "OPENAI/GPT-5"} {
		if _, _, ok := s.Lookup(name, "", nil); ok {
			t.Fatal("disabled builtin became priced")
		}
	}
}

func TestTerraStrictContextBoundary(t *testing.T) {
	for _, tc := range []struct {
		tier   string
		factor float64
	}{{"standard", 1}, {"flex", 0.5}, {"fast", 2}} {
		for _, input := range []int64{271999, 272000, 272001} {
			d := Explain("gpt-6.1-terra", tc.tier, nil, input, 10000, 10000, 100)
			if d.TotalCostMicros == nil || d.LongContext != (input > 272000) || d.ContextComparison != "gt" {
				t.Fatalf("boundary failed: %+v", d)
			}
			r, _, _ := Lookup("gpt-6.1-terra", tc.tier, nil)
			want, ok := Cost(r, input, 10000, 10000, 100)
			if !ok || want != *d.TotalCostMicros {
				t.Fatalf("settlement differs: %+v", d)
			}
			in, cache, write, out := 1.0, 0.05, 1.25, 5.0
			if input > 272000 {
				in, cache, write, out = 2, 0.1, 2.5, 7.5
			}
			if *d.EffectiveRates != (Rates{in * tc.factor, cache * tc.factor, write * tc.factor, out * tc.factor}) {
				t.Fatalf("wrong Terra rates: %+v", d.EffectiveRates)
			}
		}
	}
}
