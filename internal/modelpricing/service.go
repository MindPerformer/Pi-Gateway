package modelpricing

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"pi-gateway/internal/egress"
	"pi-gateway/internal/pricing"
	"pi-gateway/internal/store"
)

type Options struct {
	Factory *egress.Factory
	Logger  *slog.Logger
	// Zero selects bounded defaults: 200 history entries, 50 runs, 50 catalog versions.
	HistoryLimit int
	RunLimit     int
	VersionLimit int
}
type Service struct {
	store        *store.Store
	factory      *egress.Factory
	logger       *slog.Logger
	mu           sync.Mutex
	state        *state
	snapshot     atomic.Pointer[pricing.Snapshot]
	historyLimit int
	runLimit     int
	versionLimit int
	start        sync.Once
	active       map[string]bool
}

func New(ctx context.Context, st *store.Store, opts Options) (*Service, error) {
	if st == nil {
		return nil, fmt.Errorf("pricing: store required")
	}
	if opts.Factory == nil {
		opts.Factory = egress.NewFactory(egress.Options{})
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	records, err := st.LoadPricing(ctx)
	if err != nil {
		return nil, err
	}
	data, err := decodeState(records)
	if err != nil {
		return nil, err
	}
	if opts.HistoryLimit == 0 {
		opts.HistoryLimit = HistoryLimit
	}
	if opts.RunLimit == 0 {
		opts.RunLimit = RunLimit
	}
	if opts.VersionLimit == 0 {
		opts.VersionLimit = VersionLimit
	}
	if opts.HistoryLimit < 1 || opts.HistoryLimit > 10000 || opts.RunLimit < 1 || opts.RunLimit > 500 || opts.VersionLimit < 1 || opts.VersionLimit > 500 {
		return nil, fmt.Errorf("invalid pricing retention limits")
	}
	s := &Service{store: st, factory: opts.Factory, logger: opts.Logger, state: data, active: map[string]bool{}, historyLimit: opts.HistoryLimit, runLimit: opts.RunLimit, versionLimit: opts.VersionLimit}
	snap, err := buildSnapshot(data)
	if err != nil {
		return nil, err
	}
	s.snapshot.Store(snap)
	return s, nil
}
func (s *Service) Snapshot() *pricing.Snapshot { return s.snapshot.Load() }
func (s *Service) Revision() int64 {
	v, _ := strconv.ParseInt(s.snapshot.Load().Version(), 10, 64)
	return v
}

// WithRevision fences administrative recalculation commits against price changes.
func (s *Service) WithRevision(expected int64, use func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Revision != expected {
		return ErrConflict
	}
	return use()
}

// Start never waits for the network; cached persisted prices are already published by New.
func (s *Service) Start(ctx context.Context) {
	s.start.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.refreshDue(ctx)
				}
			}
		}()
	})
}
func (s *Service) refreshDue(ctx context.Context) {
	for _, source := range s.Sources() {
		now := time.Now().UnixMilli()
		if !source.Enabled || source.NextAttemptAt > now {
			continue
		}
		if source.NextAttemptAt == 0 && source.LastAttemptAt > 0 && now-source.LastAttemptAt < int64(source.IntervalHours)*int64(time.Hour/time.Millisecond) {
			continue
		}
		if _, _, err := s.Sync(ctx, source.ID); err != nil {
			s.logger.Warn("pricing source refresh failed", "source", source.ID, "error", err)
		}
	}
}
func encode(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func encodeState(d *state) *store.PricingRecords {
	r := &store.PricingRecords{Revision: d.Revision, Models: map[string]json.RawMessage{}, Sources: map[string]json.RawMessage{}, Runs: map[string]json.RawMessage{}, History: map[string]json.RawMessage{}, Versions: d.Versions}
	for id, m := range d.Models {
		r.Models[id] = encode(m)
	}
	for id, v := range d.Sources {
		r.Sources[id] = encode(storedSource{v, v.ETag, v.LastModified, v.Failures, v.NextAttemptAt})
	}
	for _, v := range d.Runs {
		r.Runs[v.ID] = encode(v)
	}
	for _, v := range d.History {
		r.History[v.ID] = encode(v)
	}
	return r
}
func decodeState(r *store.PricingRecords) (*state, error) {
	d := &state{Revision: r.Revision, Models: map[string]*modelState{}, Sources: map[string]Source{}, Runs: []SyncRun{}, History: []HistoryEntry{}, Versions: r.Versions}
	if d.Versions == nil {
		d.Versions = map[string]json.RawMessage{}
	}
	for id, raw := range r.Models {
		var v modelState
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		d.Models[id] = &v
	}
	for id, raw := range r.Sources {
		var v storedSource
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		v.Source.ETag = v.ETagValue
		v.Source.LastModified = v.ModifiedValue
		v.Source.Failures = v.FailuresValue
		v.Source.NextAttemptAt = v.NextValue
		d.Sources[id] = v.Source
	}
	for _, raw := range r.Runs {
		var v SyncRun
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		d.Runs = append(d.Runs, v)
	}
	for _, raw := range r.History {
		var v HistoryEntry
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		d.History = append(d.History, v)
	}
	sort.Slice(d.Runs, func(i, j int) bool { return d.Runs[i].CreatedAt < d.Runs[j].CreatedAt })
	sort.Slice(d.History, func(i, j int) bool { return d.History[i].Revision < d.History[j].Revision })
	return d, nil
}
func copyState(d *state) *state {
	n, err := decodeState(encodeState(d))
	if err != nil {
		panic(err)
	}
	n.Versions = map[string]json.RawMessage{}
	for k, v := range d.Versions {
		n.Versions[k] = append(json.RawMessage(nil), v...)
	}
	return n
}
func modelRecord(d *state, id string) *modelState {
	m := d.Models[id]
	if m == nil {
		m = &modelState{Model: id, External: map[string]pricing.ModelPrice{}}
		d.Models[id] = m
	}
	if m.External == nil {
		m.External = map[string]pricing.ModelPrice{}
	}
	return m
}
func builtins() map[string]pricing.ModelPrice {
	out := map[string]pricing.ModelPrice{}
	for _, m := range pricing.BuiltinModels() {
		out[m.Model] = m
	}
	return out
}
func effective(d *state, id string) *pricing.ModelPrice {
	m := d.Models[id]
	if m != nil {
		if m.Blocked {
			return &pricing.ModelPrice{Model: id, Aliases: append([]string(nil), m.BlockedAliases...), Disabled: true, Source: "manual", Note: "Explicitly blocked"}
		}
		if m.Manual != nil {
			v := *m.Manual
			v.Source = "manual"
			return &v
		}
		if m.Following != "" {
			if p, ok := m.External[m.Following]; ok {
				p.Source = "external"
				p.SourceID = m.Following
				return &p
			}
			return &pricing.ModelPrice{Model: id, Disabled: true, Source: "external", SourceID: m.Following, Note: "Following source has no applied price"}
		}
	}
	if p, ok := builtins()[id]; ok {
		p.Source = "builtin"
		return &p
	}
	return nil
}
func buildSnapshot(d *state) (*pricing.Snapshot, error) {
	ids := map[string]bool{}
	for id := range builtins() {
		ids[id] = true
	}
	for id := range d.Models {
		ids[id] = true
	}
	type entry struct {
		price    pricing.ModelPrice
		priority int
	}
	entries := []entry{}
	for id := range ids {
		p := effective(d, id)
		if p == nil {
			continue
		}
		priority := 0
		if p.Source == "external" {
			priority = 1
		}
		if p.Source == "manual" {
			priority = 2
		}
		if p.Disabled {
			priority = 3
		}
		entries = append(entries, entry{*p, priority})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].priority != entries[j].priority {
			return entries[i].priority > entries[j].priority
		}
		return entries[i].price.Model < entries[j].price.Model
	})
	owners := map[string]entry{}
	out := []pricing.ModelPrice{}
	for _, e := range entries {
		if prior, exists := owners[e.price.Model]; exists {
			if prior.priority == e.priority {
				return nil, fmt.Errorf("model/alias collision: %s", e.price.Model)
			}
			// Shadow only the exact claimed name. Unclaimed aliases retain their original rates.
			remaining := []string{}
			for _, a := range e.price.Aliases {
				if _, claimed := owners[a]; !claimed {
					remaining = append(remaining, a)
				}
			}
			if len(remaining) == 0 {
				continue
			}
			e.price.Model = remaining[0]
			e.price.Aliases = remaining[1:]
		}
		aliases := []string{}
		for _, a := range e.price.Aliases {
			if prior, exists := owners[a]; exists {
				if prior.priority == e.priority {
					return nil, fmt.Errorf("alias collision: %s", a)
				}
				continue
			}
			aliases = append(aliases, a)
		}
		e.price.Aliases = aliases
		owners[e.price.Model] = e
		for _, a := range aliases {
			owners[a] = e
		}
		out = append(out, e.price)
	}
	return pricing.NewSnapshot(strconv.FormatInt(d.Revision, 10), out)
}
func (s *Service) mutate(ctx context.Context, expected int64, action string, fn func(*state) error) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expected != s.state.Revision {
		return s.state.Revision, ErrConflict
	}
	d := copyState(s.state)
	d.Revision++
	if err := fn(d); err != nil {
		return s.state.Revision, err
	}
	if len(d.Models) > MaxModels {
		return s.state.Revision, fmt.Errorf("too many models")
	}
	snap, err := buildSnapshot(d)
	if err != nil {
		return s.state.Revision, err
	}
	if len(d.History) > s.historyLimit {
		d.History = d.History[len(d.History)-s.historyLimit:]
	}
	if len(d.Runs) > s.runLimit {
		d.Runs = d.Runs[len(d.Runs)-s.runLimit:]
	}
	d.Versions[fmt.Sprintf("%020d", d.Revision)] = encode(snap.Models())
	if len(d.Versions) > s.versionLimit {
		keys := []string{}
		for k := range d.Versions {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys[:len(keys)-s.versionLimit] {
			delete(d.Versions, k)
		}
	}
	if err = s.store.SavePricing(ctx, expected, encodeState(d), action); err != nil {
		if err == ErrConflict {
			if latest, e := s.store.LoadPricing(ctx); e == nil {
				if fresh, e := decodeState(latest); e == nil {
					if current, e := buildSnapshot(fresh); e == nil {
						s.state = fresh
						s.snapshot.Store(current)

					}
				}
			}
		}
		return s.state.Revision, err
	}
	s.state = d
	s.snapshot.Store(snap)
	return d.Revision, nil
}
func addHistory(d *state, actor, action, model string, before, after *pricing.ModelPrice) {
	d.History = append(d.History, HistoryEntry{ID: uuid.NewString(), CreatedAt: time.Now().UnixMilli(), Actor: actor, Action: action, Model: model, Before: before, After: after, Revision: d.Revision})
}
func clonePrice(p pricing.ModelPrice) pricing.ModelPrice {
	var out pricing.ModelPrice
	_ = json.Unmarshal(encode(p), &out)
	return out
}
func (s *Service) SaveManual(ctx context.Context, expected int64, p pricing.ModelPrice, actor string) (int64, error) {
	p = clonePrice(p)
	if !validModelID(p.Model) {
		return s.Revision(), fmt.Errorf("invalid model id")
	}
	if err := pricing.ValidateModel(p); err != nil {
		return s.Revision(), err
	}
	p.Source = "manual"
	p.SourceID = ""
	return s.mutate(ctx, expected, "manual", func(d *state) error {
		before := effective(d, p.Model)
		m := modelRecord(d, p.Model)
		m.Manual = &p
		m.Blocked = false
		addHistory(d, actor, "manual", p.Model, before, effective(d, p.Model))
		return nil
	})
}
func validModelID(id string) bool {
	return strings.TrimSpace(id) == id && id != "" && len(id) <= 512 && !strings.ContainsAny(id, "\r\n\x00")
}
func (s *Service) ChangeModel(ctx context.Context, expected int64, id, action, source, actor string) (int64, error) {
	if !validModelID(id) {
		return s.Revision(), fmt.Errorf("invalid model id")
	}
	return s.mutate(ctx, expected, action, func(d *state) error {
		before := effective(d, id)
		m := modelRecord(d, id)
		switch action {
		case "block":
			m.Blocked = true
			if before != nil {
				m.BlockedAliases = append([]string(nil), before.Aliases...)
			}
		case "reset":
			m.Manual = nil
			m.Blocked = false
		case "follow":
			if _, ok := d.Sources[source]; !ok {
				return ErrNotFound
			}
			m.Following = source
			m.Manual = nil
			m.Blocked = false
		default:
			return fmt.Errorf("invalid action")
		}
		addHistory(d, actor, action, id, before, effective(d, id))
		return nil
	})
}
func (s *Service) Models(ctx context.Context) (int64, []ModelView, error) {
	s.mu.Lock()
	d := copyState(s.state)
	published := s.Snapshot()
	s.mu.Unlock()
	resolved := map[string]*pricing.ModelPrice{}
	for _, p := range published.Models() {
		v := p
		resolved[p.Model] = &v
		for _, alias := range p.Aliases {
			resolved[alias] = &v
		}
	}
	base := builtins()
	ids := map[string]string{}
	for id, p := range base {
		ids[id] = p.Name
	}
	for id := range d.Models {
		ids[id] = ""
	}
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return d.Revision, nil, err
	}
	accountIDs := make([]int64, 0, len(accounts))
	for _, a := range accounts {
		accountIDs = append(accountIDs, a.ID)
	}
	catalogs, err := s.store.GetAccountModelCatalogs(ctx, accountIDs)
	if err != nil {
		return d.Revision, nil, err
	}
	for _, c := range catalogs {
		for _, m := range c.Models {
			ids[m.ID] = m.Name
		}
	}
	out := []ModelView{}
	for id, name := range ids {
		v := ModelView{Model: id, Name: name, Effective: resolved[id], Status: "unpriced", Source: "unknown"}
		if p, ok := base[id]; ok {
			v.Builtin = &p
		}
		if m := d.Models[id]; m != nil {
			v.Manual = m.Manual
			v.FollowingSourceID = m.Following
			if p, ok := m.External[m.Following]; ok {
				v.External = &p
			}
			if m.Blocked {
				v.Status = "blocked"
				v.Source = "blocked"
				v.Effective = nil
			}
		}
		if v.Effective != nil {
			v.Source = v.Effective.Source
			if v.Effective.Disabled {
				if v.Source == "manual" {
					v.Status = "blocked"
				}
				v.Effective = nil
			} else if v.Effective.Standard != nil {
				v.Status = "priced"
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return d.Revision, out, nil
}
func (s *Service) Sources() []Source { _, items := s.SourcesView(); return items }
func (s *Service) SourcesView() (int64, []Source) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Source{}
	for _, v := range s.state.Sources {
		v.TrustedHosts = append([]string(nil), v.TrustedHosts...)
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return s.state.Revision, out
}
func (s *Service) Runs() []SyncRun { _, items := s.RunsView(); return items }
func (s *Service) RunsView() (int64, []SyncRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := copyState(s.state)
	sort.Slice(d.Runs, func(i, j int) bool { return d.Runs[i].BaseRevision > d.Runs[j].BaseRevision })
	return d.Revision, d.Runs
}
func (s *Service) History() []HistoryEntry { _, items := s.HistoryView(); return items }
func (s *Service) HistoryView() (int64, []HistoryEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := copyState(s.state)
	sort.Slice(d.History, func(i, j int) bool { return d.History[i].Revision > d.History[j].Revision })
	return d.Revision, d.History
}
func (s *Service) Restore(ctx context.Context, expected int64, id, actor string) (int64, error) {
	return s.mutate(ctx, expected, "restore", func(d *state) error {
		for _, h := range d.History {
			if h.ID != id {
				continue
			}
			if h.Model == "" {
				return fmt.Errorf("history is not a model change")
			}
			before := effective(d, h.Model)
			m := modelRecord(d, h.Model)
			m.Manual = h.After
			m.Blocked = h.After != nil && h.After.Disabled
			if m.Blocked {
				m.BlockedAliases = append([]string(nil), h.After.Aliases...)
			}
			if h.After == nil {
				m.Following = ""
				m.Manual = nil
			}
			addHistory(d, actor, "restore", h.Model, before, effective(d, h.Model))
			return nil
		}
		return ErrNotFound
	})
}
func (s *Service) Export() StandardPriceFile {
	return StandardPriceFile{1, "USD", "per_million_tokens", s.Snapshot().Models()}
}
