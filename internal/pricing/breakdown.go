package pricing

// Version is stored with the settled bill, so future price changes do not
// rewrite historical explanations.
const Version = "model-pricing-v3"

type Rates struct {
	Input       float64 `json:"input"`
	CachedInput float64 `json:"cached_input"`
	CacheWrite  float64 `json:"cache_write"`
	Output      float64 `json:"output"`
}

type Breakdown struct {
	Version               string        `json:"version"`
	CatalogVersion        string        `json:"catalog_version"`
	CalculationVersion    string        `json:"calculation_version"`
	SourceID              string        `json:"source_id,omitempty"`
	SourceURL             string        `json:"source_url,omitempty"`
	Model                 string        `json:"model,omitempty"`
	ContextComparison     string        `json:"context_comparison"`
	Tier                  string        `json:"tier"`
	TierSource            string        `json:"tier_source"`
	PriceSource           string        `json:"price_source"`
	UnavailableReason     string        `json:"unavailable_reason,omitempty"`
	LongContext           bool          `json:"long_context"`
	ContextThreshold      int64         `json:"context_threshold"`
	BaseRates             *Rates        `json:"base_rates"`
	ContextRates          *Rates        `json:"context_rates"`
	EffectiveRates        *Rates        `json:"effective_rates"`
	BaseDecimalRates      *DecimalRates `json:"base_decimal_rates,omitempty"`
	ContextDecimalRates   *DecimalRates `json:"context_decimal_rates,omitempty"`
	EffectiveDecimalRates *DecimalRates `json:"effective_decimal_rates,omitempty"`
	ContextMultipliers    *Rates        `json:"context_multipliers"`
	TierMultipliers       *Rates        `json:"tier_multipliers"`
	BaseCostMicros        *int64        `json:"base_cost_micros"`
	ContextCostMicros     *int64        `json:"context_cost_micros"`
	TotalCostMicros       *int64        `json:"total_cost_micros"`
}

func rates(r Rule) *Rates { return &Rates{r.Input, r.CachedInput, r.CacheWrite, r.Output} }
func ratios(a, b Rule) *Rates {
	ratio := func(x, y float64) float64 {
		if y == 0 {
			return 1
		}
		return x / y
	}
	return &Rates{ratio(a.Input, b.Input), ratio(a.CachedInput, b.CachedInput), ratio(a.CacheWrite, b.CacheWrite), ratio(a.Output, b.Output)}
}

func contextRule(r Rule, input int64) (Rule, bool) {
	if isLongContext(r, input) {
		if r.UnpricedLongContext {
			return Rule{}, false
		}
		if r.LongContext != nil {
			return *r.LongContext, true
		}
	}
	return r, true
}

// Explain uses the same integer settlement as Cost. Each stage selects all
// segment prices; a long input changes output prices too, and a service tier
// may have its own prices instead of one common multiplier.
func Explain(model, tier string, overrides map[string]float64, input, cached, write, output int64) Breakdown {
	return BuiltinSnapshot().Explain(model, tier, overrides, input, cached, write, output)
}

func isLongContext(r Rule, input int64) bool {
	return r.LongContextThreshold > 0 && (input > r.LongContextThreshold || (input == r.LongContextThreshold && r.ContextComparison != "gt"))
}

func missingUsedRates(r Rule, input, cached, write, output int64) bool {
	if r.exactRates == nil {
		return false
	}
	uncached := input - min(input, cached)
	uncached -= min(uncached, write)
	for i, tokens := range []int64{uncached, cached, write, output} {
		if tokens > 0 && r.exactRates[i] == nil {
			return true
		}
	}
	return false
}

func (s *Snapshot) Explain(model, tier string, overrides map[string]float64, input, cached, write, output int64) Breakdown {
	if s == nil {
		s = BuiltinSnapshot()
	}
	d := Breakdown{Version: s.Version(), CatalogVersion: s.Version(), CalculationVersion: Version, Tier: tier, ContextComparison: "gte"}
	m, found := s.find(model)
	if !found {
		d.UnavailableReason = "unknown_model"
		return d
	}
	d.Model, d.SourceID, d.SourceURL = m.Model, m.SourceID, m.SourceURL
	d.PriceSource, d.ContextComparison = m.Source, m.ContextComparison
	if m.ContextMode != "same" {
		d.ContextThreshold = m.ContextThreshold
	}
	var context Rule
	var haveContext bool
	if base, _, ok := s.Lookup(model, "standard", overrides); ok {
		d.BaseRates, d.BaseDecimalRates = rates(base), ruleDecimalRates(base)
		baseShort := base
		baseShort.LongContextThreshold = 0
		if cost, ok := Cost(baseShort, input, cached, write, output); ok {
			d.BaseCostMicros = &cost
		}
		context, haveContext = contextRule(base, input)
		if haveContext {
			d.ContextRates, d.ContextMultipliers = rates(context), ratios(context, baseShort)
			d.ContextDecimalRates = ruleDecimalRates(context)
			if cost, ok := Cost(context, input, cached, write, output); ok {
				d.ContextCostMicros = &cost
			}
		}
	}
	selected, source, ok := s.Lookup(model, tier, overrides)
	if !ok {
		d.UnavailableReason = "unpriced_tier"
		return d
	}
	d.PriceSource = source
	d.Tier, _ = normalizeTier(tier)
	d.LongContext = isLongContext(selected, input)
	effective, ok := contextRule(selected, input)
	if !ok {
		d.UnavailableReason = "unpriced_context"
		return d
	}
	d.EffectiveRates, d.EffectiveDecimalRates = rates(effective), ruleDecimalRates(effective)
	if haveContext {
		d.TierMultipliers = ratios(effective, context)
	}
	if cost, ok := Cost(selected, input, cached, write, output); ok {
		d.TotalCostMicros = &cost
	} else if input >= 0 && cached >= 0 && write >= 0 && output >= 0 && missingUsedRates(effective, input, cached, write, output) {
		d.UnavailableReason = "unpriced_rates"
	} else {
		d.UnavailableReason = "invalid_usage"
	}
	return d
}
