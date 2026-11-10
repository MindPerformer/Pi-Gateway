package pricing

import (
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

// DecimalRates stores USD per million tokens. Empty means unknown, not free.
type DecimalRates struct {
	Input       string `json:"input"`
	CachedInput string `json:"cached_input"`
	CacheWrite  string `json:"cache_write"`
	Output      string `json:"output"`
}

type ModelPrice struct {
	Model             string        `json:"model"`
	Name              string        `json:"name,omitempty"`
	Aliases           []string      `json:"aliases,omitempty"`
	Standard          *DecimalRates `json:"standard"`
	Flex              *DecimalRates `json:"flex"`
	Fast              *DecimalRates `json:"fast"`
	LongStandard      *DecimalRates `json:"long_standard"`
	LongFlex          *DecimalRates `json:"long_flex"`
	LongFast          *DecimalRates `json:"long_fast"`
	ContextMode       string        `json:"context_mode"`
	ContextThreshold  int64         `json:"context_threshold"`
	ContextComparison string        `json:"context_comparison"`
	Disabled          bool          `json:"disabled"`
	Source            string        `json:"source,omitempty"`
	SourceID          string        `json:"source_id,omitempty"`
	SourceURL         string        `json:"source_url,omitempty"`
	Note              string        `json:"note,omitempty"`
}

// Snapshot owns its catalog. Neither callers' input nor returned values alias it.
type Snapshot struct {
	version      string
	models       []ModelPrice
	index        map[string]int
	builtinIndex map[string]int
}

type Provider interface{ Snapshot() *Snapshot }

// parsePrice accepts plain nonnegative decimal strings with bounded precision.
func parsePrice(value string) (*big.Rat, bool) {
	if len(value) == 0 || len(value) > 37 {
		return nil, false
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || len(parts[0]) == 0 || len(parts[0]) > 18 {
		return nil, false
	}
	if len(parts) == 2 && (len(parts[1]) == 0 || len(parts[1]) > 18) {
		return nil, false
	}
	for _, part := range parts {
		for _, c := range part {
			if c < '0' || c > '9' {
				return nil, false
			}
		}
	}
	return new(big.Rat).SetString(value)
}

func ValidateModel(m ModelPrice) error {
	if normalizeModel(m.Model) == "" || len(m.Model) > 256 {
		return fmt.Errorf("invalid model identifier")
	}
	for _, alias := range m.Aliases {
		if normalizeModel(alias) == "" || len(alias) > 256 {
			return fmt.Errorf("invalid alias")
		}
	}
	for _, tier := range []*DecimalRates{m.Standard, m.Flex, m.Fast, m.LongStandard, m.LongFlex, m.LongFast} {
		if tier == nil {
			continue
		}
		for _, value := range []string{tier.Input, tier.CachedInput, tier.CacheWrite, tier.Output} {
			if value != "" {
				if _, ok := parsePrice(value); !ok {
					return fmt.Errorf("invalid decimal price %q", value)
				}
			}
		}
	}
	switch m.ContextMode {
	case "", "same":
	case "custom", "unpriced":
		if m.ContextThreshold <= 0 {
			return fmt.Errorf("context threshold must be positive")
		}
	default:
		return fmt.Errorf("invalid context mode %q", m.ContextMode)
	}
	if m.ContextThreshold < 0 {
		return fmt.Errorf("context threshold must not be negative")
	}
	if m.ContextComparison != "" && m.ContextComparison != "gte" && m.ContextComparison != "gt" {
		return fmt.Errorf("invalid context comparison")
	}
	if m.Source != "" && m.Source != "builtin" && m.Source != "manual" && m.Source != "external" {
		return fmt.Errorf("invalid price source")
	}
	return nil
}

func cloneModel(m ModelPrice) ModelPrice {
	m.Aliases = append([]string(nil), m.Aliases...)
	clone := func(r *DecimalRates) *DecimalRates {
		if r == nil {
			return nil
		}
		c := *r
		return &c
	}
	m.Standard, m.Flex, m.Fast = clone(m.Standard), clone(m.Flex), clone(m.Fast)
	m.LongStandard, m.LongFlex, m.LongFast = clone(m.LongStandard), clone(m.LongFlex), clone(m.LongFast)
	return m
}

func NewSnapshot(version string, models []ModelPrice) (*Snapshot, error) {
	s := &Snapshot{version: version, index: make(map[string]int), builtinIndex: make(map[string]int), models: make([]ModelPrice, len(models))}
	for i, m := range models {
		if err := ValidateModel(m); err != nil {
			return nil, fmt.Errorf("model %q: %w", m.Model, err)
		}
		m = cloneModel(m)
		if m.ContextMode == "" {
			m.ContextMode = "same"
		}
		if m.ContextComparison == "" {
			m.ContextComparison = "gte"
		}
		if m.Source == "" {
			m.Source = "manual"
		}
		s.models[i] = m
		for _, name := range append([]string{m.Model}, m.Aliases...) {
			key := strings.TrimSpace(name)
			if _, exists := s.index[key]; exists {
				return nil, fmt.Errorf("conflicting model or alias %q", name)
			}
			s.index[key] = i
			if _, builtin := pricingRules[m.Model]; builtin {
				normalized := normalizeModel(name)
				if _, exists := s.builtinIndex[normalized]; exists {
					return nil, fmt.Errorf("conflicting builtin alias %q", name)
				}
				s.builtinIndex[normalized] = i
			}
		}
	}
	// Aliases point directly to a model, never to another alias. Rejecting all
	// name collisions also rejects cycles such as A aliases B / B aliases A.
	return s, nil
}

func (s *Snapshot) Version() string {
	if s == nil {
		return Version
	}
	return s.version
}
func (s *Snapshot) Models() []ModelPrice {
	if s == nil {
		return nil
	}
	out := make([]ModelPrice, len(s.models))
	for i, m := range s.models {
		out[i] = cloneModel(m)
	}
	return out
}

// BuiltinModels includes every explicitly supported alias and all tier bands.
func BuiltinModels() []ModelPrice {
	out := make([]ModelPrice, 0, len(pricingRules))
	convert := func(r tokenRates, write int64) *DecimalRates {
		if r == (tokenRates{}) {
			return nil
		}
		v := r.rule(write)
		f := func(n float64) string { return strconv.FormatFloat(n, 'f', -1, 64) }
		return &DecimalRates{f(v.Input), f(v.CachedInput), f(v.CacheWrite), f(v.Output)}
	}
	for name, p := range pricingRules {
		m := ModelPrice{Model: name, Source: "builtin", ContextMode: "same", ContextComparison: "gte"}
		m.Standard, m.Flex, m.Fast = convert(p.standard, p.cacheWritePercent), convert(p.flex, p.cacheWritePercent), convert(p.fast, p.cacheWritePercent)
		m.LongStandard, m.LongFlex, m.LongFast = convert(p.longStandard, p.cacheWritePercent), convert(p.longFlex, p.cacheWritePercent), convert(p.longFast, p.cacheWritePercent)
		if p.unpricedLongContext {
			m.ContextMode = "unpriced"
		} else if p.longStandard != (tokenRates{}) {
			m.ContextMode = "custom"
		}
		if m.ContextMode != "same" {
			m.ContextThreshold = longContextThreshold
		}
		if p.contextComparison != "" {
			m.ContextComparison = p.contextComparison
		}
		for alias, canonical := range modelAliases {
			if canonical == name {
				m.Aliases = append(m.Aliases, alias)
			}
		}
		sort.Strings(m.Aliases)
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

var builtinSnapshot = func() *Snapshot {
	s, err := NewSnapshot(Version, BuiltinModels())
	if err != nil {
		panic(err)
	}
	return s
}()

func BuiltinSnapshot() *Snapshot { return builtinSnapshot }

func (s *Snapshot) find(model string) (ModelPrice, bool) {
	if s == nil {
		s = BuiltinSnapshot()
	}
	i, ok := s.index[strings.TrimSpace(model)]
	if !ok {
		i, ok = s.builtinIndex[normalizeModel(model)]
	}
	if !ok || s.models[i].Disabled {
		return ModelPrice{}, false
	}
	return s.models[i], true
}

// ResolveModel returns a detached model with the same exact alias resolution as billing.
func (s *Snapshot) ResolveModel(model string) (ModelPrice, bool) {
	m, ok := s.find(model)
	if !ok {
		return ModelPrice{}, false
	}
	return cloneModel(m), true
}

func decimalRule(r *DecimalRates) Rule {
	v := Rule{exactRates: &[4]*big.Rat{}}
	for i, value := range []string{r.Input, r.CachedInput, r.CacheWrite, r.Output} {
		if value != "" {
			v.exactRates[i], _ = parsePrice(value)
		}
	}
	fields := []*float64{&v.Input, &v.CachedInput, &v.CacheWrite, &v.Output}
	for i, value := range v.exactRates {
		if value != nil {
			*fields[i], _ = value.Float64()
		}
	}
	return v
}

// ruleDecimalRates preserves unknown fields and exact decimal display values.
func ruleDecimalRates(r Rule) *DecimalRates {
	out := &DecimalRates{}
	fields := []*string{&out.Input, &out.CachedInput, &out.CacheWrite, &out.Output}
	if r.exactRates == nil {
		for i, value := range []float64{r.Input, r.CachedInput, r.CacheWrite, r.Output} {
			*fields[i] = strconv.FormatFloat(value, 'f', -1, 64)
		}
		return out
	}
	for i, value := range r.exactRates {
		if value == nil {
			continue
		}
		// Valid decimal prices and float overrides have only 2/5 factors in
		// their denominator, so this many digits renders the exact product.
		d := new(big.Int).Set(value.Denom())
		scale := 0
		for _, factor := range []int64{2, 5} {
			count := 0
			for d.Cmp(big.NewInt(1)) > 0 {
				q, rem := new(big.Int), new(big.Int)
				q.QuoRem(d, big.NewInt(factor), rem)
				if rem.Sign() != 0 {
					break
				}
				d = q
				count++
			}
			if count > scale {
				scale = count
			}
		}
		text := value.FloatString(scale)
		if strings.Contains(text, ".") {
			text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
		}
		*fields[i] = text
	}
	return out
}

func (s *Snapshot) Lookup(model, tier string, overrides map[string]float64) (Rule, string, bool) {
	m, ok := s.find(model)
	if !ok {
		return Rule{}, "", false
	}
	selected, ok := normalizeTier(tier)
	if !ok {
		return Rule{}, "", false
	}
	short, long := m.Standard, m.LongStandard
	switch selected {
	case "flex":
		short, long = m.Flex, m.LongFlex
	case "fast":
		short, long = m.Fast, m.LongFast
	}
	if short == nil {
		return Rule{}, "", false
	}
	r := decimalRule(short)
	if m.ContextMode != "same" {
		r.LongContextThreshold, r.ContextComparison = m.ContextThreshold, m.ContextComparison
		if m.ContextMode == "unpriced" || long == nil {
			r.UnpricedLongContext = true
		} else {
			l := decimalRule(long)
			r.LongContext = &l
		}
	}
	keys := []string{model, strings.TrimSpace(model), m.Model}
	if _, builtin := pricingRules[m.Model]; builtin {
		keys = []string{model, normalizeModel(model), m.Model}
	}
	for _, key := range keys {
		if multiplier, found := overrides[key]; found {
			if !scaleRule(&r, multiplier) {
				return Rule{}, "", false
			}
			return r, "override", true
		}
	}
	return r, m.Source, true
}
