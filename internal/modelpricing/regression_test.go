package modelpricing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestBlockedAliasTombstoneAndMissingFollow(t *testing.T) {
	ctx := context.Background()
	s, st := testService(t)
	p := testPrice("my-model", "9")
	p.Aliases = []string{"gpt-5"}
	rev, err := s.SaveManual(ctx, 0, p, "actor")
	if err != nil {
		t.Fatal(err)
	}
	requirePrice(t, s.Snapshot(), "gpt-5", 9)
	// An exact canonical override must not destroy the builtin's other aliases.
	requirePrice(t, s.Snapshot(), "gpt-5-2025-08-07", 1.25)
	rev, err = s.ChangeModel(ctx, rev, "my-model", "block", "", "actor")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.Snapshot().Lookup("gpt-5", "standard", nil); ok {
		t.Fatal("blocked manual alias fell back to builtin")
	}
	if _, _, ok := s.Snapshot().Lookup("my-model", "standard", nil); ok {
		t.Fatal("blocked canonical priced")
	}
	reloaded, err := New(ctx, st, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := reloaded.Snapshot().Lookup("gpt-5", "standard", nil); ok {
		t.Fatal("blocked aliases not persisted")
	}
	rev, err = s.ChangeModel(ctx, rev, "my-model", "reset", "", "actor")
	if err != nil {
		t.Fatal(err)
	}
	requirePrice(t, s.Snapshot(), "gpt-5", 1.25)
	rev, err = s.SaveSource(ctx, rev, Source{ID: "missing", URL: "https://example.com", Format: "standard"})
	if err != nil {
		t.Fatal(err)
	}
	rev, err = s.ChangeModel(ctx, rev, "gpt-5-2025-08-07", "follow", "missing", "actor")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.Snapshot().Lookup("gpt-5-2025-08-07", "standard", nil); ok {
		t.Fatal("missing followed price fell back to builtin alias")
	}
	requirePrice(t, s.Snapshot(), "gpt-5", 1.25)
	_, items, err := s.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range items {
		if m.Model == "gpt-5-2025-08-07" && (m.Status != "unpriced" || m.Effective != nil) {
			t.Fatalf("view differs from data plane: %+v", m)
		}
	}
}
func TestAutoApplyRechecksCurrentFlagsAfterFetch(t *testing.T) {
	var rate atomic.Int64
	rate.Store(2)
	var block atomic.Bool
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	s, source := fixtureSource(t, func(w http.ResponseWriter, r *http.Request) {
		if block.Swap(false) {
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Write(encode(testDocument(testPrice("auto-followed", fmt.Sprint(rate.Load())))))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rev, run, err := s.Sync(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	rev, err = s.Apply(ctx, rev, run.ID, []string{"auto-followed"}, "actor")
	if err != nil {
		t.Fatal(err)
	}
	source.AutoApply = true
	rev, err = s.SaveSource(ctx, rev, source)
	if err != nil {
		t.Fatal(err)
	}
	rate.Store(3)
	block.Store(true)
	type syncResult struct {
		run SyncRun
		err error
	}
	done := make(chan syncResult, 1)
	go func() { _, r, e := s.Sync(ctx, source.ID); done <- syncResult{r, e} }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("fetch did not start")
	}
	source.AutoApply = false
	source.Enabled = false
	if _, err = s.SaveSource(ctx, s.Revision(), source); err != nil {
		t.Fatal(err)
	}
	close(release)
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.run.Status != "pending" {
		t.Fatalf("disabled auto apply continued: %s", result.run.Status)
	}
	requirePrice(t, s.Snapshot(), "auto-followed", 2)
	source.Enabled = true
	source.AutoApply = true
	if _, err = s.SaveSource(ctx, s.Revision(), source); err != nil {
		t.Fatal(err)
	}
	rate.Store(4)
	_, run, err = s.Sync(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "applied" {
		t.Fatalf("safe following update not auto applied: %+v", run)
	}
	requirePrice(t, s.Snapshot(), "auto-followed", 4)
	if _, err = s.SaveManual(ctx, s.Revision(), testPrice("auto-followed", "9"), "actor"); err != nil {
		t.Fatal(err)
	}
	rate.Store(5)
	_, run, err = s.Sync(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !run.Items[0].Locked {
		t.Fatal("manual price not locked")
	}
	requirePrice(t, s.Snapshot(), "auto-followed", 9)
}
func TestCustomRetentionAndCandidateHashFence(t *testing.T) {
	ctx := context.Background()
	base, st := testService(t)
	_ = base
	s, err := New(ctx, st, Options{HistoryLimit: 2, RunLimit: 2, VersionLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err = s.SaveManual(ctx, s.Revision(), testPrice("bounded", fmt.Sprint(i)), "actor"); err != nil {
			t.Fatal(err)
		}
		if _, _, err = s.Import(ctx, s.Revision(), testDocument(testPrice("candidate", "1"))); err != nil {
			t.Fatal(err)
		}
	}
	records, err := st.LoadPricing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records.History) != 2 || len(records.Runs) != 2 || len(records.Versions) != 2 {
		t.Fatalf("retention not honored: %d/%d/%d", len(records.History), len(records.Runs), len(records.Versions))
	}
	run := s.Runs()[0]
	s.mu.Lock()
	for i := range s.state.Runs {
		if s.state.Runs[i].ID == run.ID {
			s.state.Runs[i].Items[0].After.Standard.Input = "999"
		}
	}
	s.mu.Unlock()
	if _, err = s.Apply(ctx, s.Revision(), run.ID, []string{"candidate"}, "actor"); !errors.Is(err, ErrConflict) {
		t.Fatalf("tampered hash applied: %v", err)
	}
}
