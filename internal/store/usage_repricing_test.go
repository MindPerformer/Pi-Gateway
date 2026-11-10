package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func seedStoreRepricing(t *testing.T, s *Store) int64 {
	t.Helper()
	input, cached, write, output := int64(100), int64(10), int64(0), int64(20)
	r := &UsageRecord{AccountID: 1, Model: "test-model", StartedAt: 100, InputTokens: &input, CachedTokens: &cached, CacheWriteTokens: &write, OutputTokens: &output}
	id, err := s.StartUsageRecord(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.Outcome = "succeeded"
	r.UpstreamSendState = "sent"
	r.CompletedAt = 200
	if err := s.FinishUsageRecord(t.Context(), id, r); err != nil {
		t.Fatal(err)
	}
	return id
}

func storeRepricingUpdates(t *testing.T, s *Store) []UsageRepricingUpdate {
	t.Helper()
	records, _, err := s.ListUsageRepricingRecords(t.Context(), UsageFilter{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	updates := make([]UsageRepricingUpdate, 0, len(records))
	for _, r := range records {
		updates = append(updates, UsageRepricingUpdate{Record: r, CostMicros: 123, PriceVersion: "test-v1", RateSnapshot: `{"input":2}`, CostSource: "historical_estimate:manual"})
	}
	return updates
}

func TestUsageRepricingAtomicCASAndValidationRollback(t *testing.T) {
	for _, change := range []string{"tokens", "known_zero", "deleted", "validation"} {
		t.Run(change, func(t *testing.T) {
			s, err := Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			seedStoreRepricing(t, s)
			second := seedStoreRepricing(t, s)
			updates := storeRepricingUpdates(t, s)
			var validate func() error
			switch change {
			case "tokens":
				_, err = s.ExecContext(t.Context(), `UPDATE usage_records SET output_tokens=21 WHERE id=?`, second)
			case "known_zero":
				_, err = s.ExecContext(t.Context(), `UPDATE usage_records SET cost_micros=0 WHERE id=?`, second)
			case "deleted":
				_, err = s.ExecContext(t.Context(), `DELETE FROM usage_records WHERE id=?`, second)
			case "validation":
				validate = func() error { return errors.New("price changed") }
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.ApplyUsageRepricing(t.Context(), "preview-1", updates, validate)
			if err == nil || result.Updated != 0 {
				t.Fatalf("expected rollback, got %+v %v", result, err)
			}
			var count int
			if err := s.QueryRowContext(t.Context(), `SELECT count(*) FROM usage_repriced_costs`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial transaction leaked: count=%d err=%v", count, err)
			}
		})
	}
}

func TestUsageRepricingOriginalCostPrecedenceAndWindows(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := seedStoreRepricing(t, s)
	updates := storeRepricingUpdates(t, s)
	result, err := s.ApplyUsageRepricing(t.Context(), "preview-1", updates, nil)
	if err != nil || result.Updated != 1 {
		t.Fatalf("apply %+v %v", result, err)
	}
	for _, tc := range []struct {
		start, end, before int64
		want               int64
	}{{0, 0, 0, 123}, {100, 101, 200, 123}, {101, 0, 0, 0}, {0, 100, 0, 0}, {100, 101, 199, 0}} {
		cost, err := s.AccountUsageCost(t.Context(), 1, UsageRange{Start: tc.start, End: tc.end}, tc.before)
		if err != nil || cost.CostMicros != tc.want {
			t.Fatalf("window %+v: %+v %v", tc, cost, err)
		}
	}
	// Later authoritative settlement must override the historical estimate,
	// including a legitimate zero, without deleting that estimate's provenance.
	if _, err := s.ExecContext(t.Context(), `UPDATE usage_records SET cost_micros=0 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	cost, err := s.AccountUsageCost(t.Context(), 1, UsageRange{}, 0)
	if err != nil || cost.CostMicros != 0 || cost.PricedRequests != 1 || cost.RepricedRequests != 0 || cost.RepricedMicros != 0 || cost.UnpricedRequests != 0 {
		t.Fatalf("zero precedence %+v %v", cost, err)
	}
	if _, err := s.ApplyUsageRepricing(t.Context(), "preview-1", updates, nil); !errors.Is(err, ErrUsageRepricingConflict) {
		t.Fatalf("cannot overwrite known zero: %v", err)
	}
}

func TestUsageRepricingSchemaReopenAndForeignKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repricing.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			if err := s.Close(); err != nil {
				t.Errorf("close reopened store: %v", err)
			}
			if stats := s.DB().Stats(); stats.OpenConnections != 0 || stats.InUse != 0 {
				t.Errorf("connections remain after close: %+v", stats)
			}
		}
	}()
	id := seedStoreRepricing(t, s)
	updates := storeRepricingUpdates(t, s)
	if _, err := s.ApplyUsageRepricing(t.Context(), "preview-1", updates, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ApplyUsageRepricing(t.Context(), "preview-1", updates, nil)
	if err != nil || result.Skipped != 1 || result.Updated != 0 {
		t.Fatalf("durable idempotence %+v %v", result, err)
	}
	if _, err := s.ApplyUsageRepricing(t.Context(), "preview-2", updates, nil); !errors.Is(err, ErrUsageRepricingConflict) {
		t.Fatalf("other preview replaced estimate: %v", err)
	}
	if _, err := s.ExecContext(t.Context(), `DELETE FROM usage_records WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.QueryRowContext(t.Context(), `SELECT count(*) FROM usage_repriced_costs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cascade count=%d err=%v", count, err)
	}
}
