package modelpricing

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/google/uuid"
	"pi-gateway/internal/pricing"
)

func (s *Service) SaveSource(ctx context.Context, expected int64, source Source) (int64, error) {
	source.TrustedHosts = append([]string(nil), source.TrustedHosts...)
	if source.ID == "" {
		source.ID = uuid.NewString()
	}
	if err := validateSource(&source); err != nil {
		return s.Revision(), err
	}
	return s.mutate(ctx, expected, "source.save", func(d *state) error {
		if len(d.Sources) >= 100 {
			if _, ok := d.Sources[source.ID]; !ok {
				return fmt.Errorf("source limit reached")
			}
		}
		if old, ok := d.Sources[source.ID]; ok && sourceHash(old) == sourceHash(source) {
			source.LastAttemptAt = old.LastAttemptAt
			source.LastSuccessAt = old.LastSuccessAt
			source.LastError = old.LastError
			source.CandidateCount = old.CandidateCount
			source.ETag = old.ETag
			source.LastModified = old.LastModified
			source.Failures = old.Failures
			source.NextAttemptAt = old.NextAttemptAt
		} else {
			source.LastAttemptAt = 0
			source.LastSuccessAt = 0
			source.LastError = ""
			source.CandidateCount = 0
			source.ETag = ""
			source.LastModified = ""
			source.Failures = 0
			source.NextAttemptAt = 0
		}
		d.Sources[source.ID] = source
		return nil
	})
}
func (s *Service) DeleteSource(ctx context.Context, expected int64, id string) (int64, error) {
	return s.mutate(ctx, expected, "source.delete", func(d *state) error {
		if _, ok := d.Sources[id]; !ok {
			return ErrNotFound
		}
		delete(d.Sources, id)
		for _, m := range d.Models {
			delete(m.External, id)
			if m.Following == id {
				m.Following = ""
			}
		}
		return nil
	})
}
func (s *Service) TestSource(ctx context.Context, id string) (int, error) {
	s.mu.Lock()
	source, ok := s.state.Sources[id]
	s.mu.Unlock()
	if !ok {
		return 0, ErrNotFound
	}
	result, err := s.fetch(ctx, source, false)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrExternal, err)
	}
	return len(result.Models), nil
}
func riskWarnings(before *pricing.ModelPrice, after pricing.ModelPrice) []string {
	if before == nil {
		return nil
	}
	out := []string{}
	for name, pair := range map[string][2]*pricing.DecimalRates{"standard": {before.Standard, after.Standard}, "flex": {before.Flex, after.Flex}, "fast": {before.Fast, after.Fast}, "long_standard": {before.LongStandard, after.LongStandard}, "long_flex": {before.LongFlex, after.LongFlex}, "long_fast": {before.LongFast, after.LongFast}} {
		if pair[0] != nil && pair[1] == nil {
			out = append(out, "coverage removed: "+name)
		}
	}
	if before.ContextMode != after.ContextMode || before.ContextThreshold != after.ContextThreshold || before.ContextComparison != after.ContextComparison {
		out = append(out, "context policy changes")
	}
	if !reflect.DeepEqual(before.Aliases, after.Aliases) {
		out = append(out, "aliases change")
	}
	return out
}
func manualLock(d *state, m pricing.ModelPrice) bool {
	names := map[string]bool{m.Model: true}
	for _, a := range m.Aliases {
		names[a] = true
	}
	for _, current := range d.Models {
		if current.Blocked {
			if names[current.Model] {
				return true
			}
			for _, alias := range current.BlockedAliases {
				if names[alias] {
					return true
				}
			}
		}
		if current.Manual == nil {
			continue
		}
		if names[current.Model] {
			return true
		}
		for _, a := range current.Manual.Aliases {
			if names[a] {
				return true
			}
		}
	}
	return false
}
func makeRun(d *state, source Source, result *fetched) SyncRun {
	run := SyncRun{ID: uuid.NewString(), SourceID: source.ID, CreatedAt: time.Now().UnixMilli(), Status: "pending", BaseRevision: d.Revision, Items: []SyncItem{}, SourceHash: sourceHash(source)}
	for _, m := range result.Models {
		m.Source = "external"
		m.SourceID = source.ID
		m.SourceURL = source.URL
		if source.ID == "import" {
			m.Source = "manual"
			m.SourceID = "import"
			m.SourceURL = ""
		}
		before := effective(d, m.Model)
		warnings := append([]string{}, result.Warnings[m.Model]...)
		warnings = append(warnings, riskWarnings(before, m)...)
		sort.Strings(warnings)
		run.Items = append(run.Items, SyncItem{Model: m.Model, Before: before, After: m, Warnings: warnings, Locked: source.ID != "import" && manualLock(d, m)})
	}
	sort.Slice(run.Items, func(i, j int) bool { return run.Items[i].Model < run.Items[j].Model })
	run.Hash = hashValue(run.Items)
	return run
}
func (s *Service) Import(ctx context.Context, expected int64, document StandardPriceFile) (int64, SyncRun, error) {
	result, err := parseStandard(document)
	if err != nil {
		return s.Revision(), SyncRun{}, err
	}
	var run SyncRun
	rev, err := s.mutate(ctx, expected, "import.preview", func(d *state) error {
		run = makeRun(d, Source{ID: "import", Format: "standard"}, result)
		d.Runs = append(d.Runs, run)
		return nil
	})
	return rev, run, err
}
func (s *Service) Sync(ctx context.Context, id string) (int64, SyncRun, error) {
	s.mu.Lock()
	source, ok := s.state.Sources[id]
	if !ok {
		s.mu.Unlock()
		return s.Revision(), SyncRun{}, ErrNotFound
	}
	if s.active[id] {
		s.mu.Unlock()
		return s.Revision(), SyncRun{}, fmt.Errorf("%w: source synchronization already running", ErrConflict)
	}
	s.active[id] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.active, id); s.mu.Unlock() }()
	result, fetchErr := s.fetch(ctx, source, true)
	// A 304 must not strand a revision-bound candidate. Fetch the complete document
	// again without validators so a fresh preview can be rebased on the latest state.
	if fetchErr == nil && result.NotModified {
		result, fetchErr = s.fetch(ctx, source, false)
	}
	var run SyncRun
	var rev int64
	var err error
	for tries := 0; tries < 4; tries++ {
		rev, err = s.mutate(ctx, s.Revision(), "source.sync", func(d *state) error {
			current, exists := d.Sources[id]
			if !exists || sourceHash(current) != sourceHash(source) {
				return ErrConflict
			}
			current.LastAttemptAt = time.Now().UnixMilli()
			if fetchErr != nil {
				current.LastError = fetchErr.Error()
				current.Failures++
				backoff := time.Minute * time.Duration(1<<min(current.Failures, 10))
				current.NextAttemptAt = time.Now().Add(backoff).UnixMilli()
				run = SyncRun{ID: uuid.NewString(), SourceID: id, CreatedAt: current.LastAttemptAt, Status: "failed", Error: current.LastError, BaseRevision: d.Revision, Items: []SyncItem{}}
			} else {
				current.LastSuccessAt = current.LastAttemptAt
				current.LastError = ""
				current.Failures = 0
				current.NextAttemptAt = 0
				if result.NotModified {
					run = SyncRun{ID: uuid.NewString(), SourceID: id, CreatedAt: current.LastAttemptAt, Status: "not_modified", BaseRevision: d.Revision, Items: []SyncItem{}}
				} else {
					current.ETag = result.ETag
					current.LastModified = result.Modified
					run = makeRun(d, current, result)
					current.CandidateCount = len(run.Items)
				}
			}
			d.Sources[id] = current
			d.Runs = append(d.Runs, run)
			return nil
		})
		if !errors.Is(err, ErrConflict) {
			break
		}
		s.mu.Lock()
		latest, exists := s.state.Sources[id]
		s.mu.Unlock()
		if !exists || sourceHash(latest) != sourceHash(source) {
			break
		}
	}
	if err != nil {
		return rev, run, err
	}
	if fetchErr != nil {
		return rev, run, fmt.Errorf("%w: %v", ErrExternal, fetchErr)
	}
	s.mu.Lock()
	latestSource := s.state.Sources[id]
	s.mu.Unlock()
	if latestSource.AutoApply && latestSource.Enabled && run.Status == "pending" {
		selected := []string{}
		s.mu.Lock()
		for _, item := range run.Items {
			m := s.state.Models[item.Model]
			if m != nil && m.Following == id && m.Manual == nil && !m.Blocked && !item.Locked && len(item.Warnings) == 0 {
				selected = append(selected, item.Model)
			}
		}
		s.mu.Unlock()
		if len(selected) > 0 {
			applied, e := s.Apply(ctx, rev, run.ID, selected, "sync:auto")
			if e == nil {
				rev = applied
				for _, saved := range s.Runs() {
					if saved.ID == run.ID {
						run = saved
						break
					}
				}
			} else {
				s.logger.Warn("pricing auto apply skipped", "source", id, "error", e)
			}
		}
	}
	return rev, run, nil
}
func (s *Service) Apply(ctx context.Context, expected int64, id string, models []string, actor string) (int64, error) {
	if len(models) == 0 || len(models) > MaxModels {
		return s.Revision(), fmt.Errorf("select 1..%d models", MaxModels)
	}
	return s.mutate(ctx, expected, "candidate.apply", func(d *state) error {
		index := -1
		for i := range d.Runs {
			if d.Runs[i].ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			return ErrNotFound
		}
		run := &d.Runs[index]
		if run.Status != "pending" || run.BaseRevision != expected || run.Hash != hashValue(run.Items) {
			return ErrConflict
		}
		if run.SourceID != "import" {
			source, ok := d.Sources[run.SourceID]
			if !ok || run.SourceHash != sourceHash(source) {
				return ErrConflict
			}
			if actor == "sync:auto" && (!source.AutoApply || !source.Enabled) {
				return ErrConflict
			}
		}
		chosen := map[string]bool{}
		for _, m := range models {
			if chosen[m] {
				return fmt.Errorf("duplicate selected model")
			}
			chosen[m] = true
		}
		found := 0
		for i := range run.Items {
			item := &run.Items[i]
			if !chosen[item.Model] {
				continue
			}
			found++
			if item.Locked || (run.SourceID != "import" && manualLock(d, item.After)) {
				return fmt.Errorf("model %s is manually locked", item.Model)
			}
			if err := pricing.ValidateModel(item.After); err != nil {
				return err
			}
			before := effective(d, item.Model)
			m := modelRecord(d, item.Model)
			p := item.After
			if run.SourceID == "import" {
				m.Manual = &p
				m.Blocked = p.Disabled
			} else {
				m.External[run.SourceID] = p
				m.Following = run.SourceID
				m.Blocked = false
			}
			item.Selected = true
			addHistory(d, actor, "apply", item.Model, before, effective(d, item.Model))
		}
		if found != len(chosen) {
			return fmt.Errorf("selected model not found in candidate")
		}
		run.Status = "applied"
		return nil
	})
}
