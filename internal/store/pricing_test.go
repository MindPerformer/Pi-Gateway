package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func exercisePricingRecords(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	empty, err := s.LoadPricing(ctx)
	if err != nil || empty.Revision != 0 {
		t.Fatalf("initial pricing revision: %+v %v", empty, err)
	}
	records := &PricingRecords{Models: map[string]json.RawMessage{"model/with/slash": json.RawMessage(`{"model":"model/with/slash"}`)}, Sources: map[string]json.RawMessage{"source": json.RawMessage(`{"id":"source"}`)}, Runs: map[string]json.RawMessage{"run": json.RawMessage(`{"id":"run"}`)}, History: map[string]json.RawMessage{"history": json.RawMessage(`{"id":"history"}`)}, Versions: map[string]json.RawMessage{"1": json.RawMessage(`[]`)}}
	if err = s.SavePricing(ctx, 0, records, "fixture"); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadPricing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 1 || len(got.Models) != 1 || len(got.Sources) != 1 || len(got.Runs) != 1 || len(got.History) != 1 || len(got.Versions) != 1 {
		t.Fatalf("roundtrip: %+v", got)
	}
	if err = s.SavePricing(ctx, 0, records, "stale"); !errors.Is(err, ErrPricingRevision) {
		t.Fatalf("CAS not enforced: %v", err)
	}
	records.Models["broken"] = json.RawMessage(`not json`)
	if err = s.SavePricing(ctx, 1, records, "broken"); err == nil {
		t.Fatal("invalid record committed")
	}
	got, err = s.LoadPricing(ctx)
	if err != nil || got.Revision != 1 || len(got.Models) != 1 {
		t.Fatalf("rollback failed: %+v %v", got, err)
	}
	audit, err := s.ListAudit(ctx, 10)
	if err != nil || len(audit) != 1 || audit[0].Action != "pricing.fixture" {
		t.Fatalf("transactional audit: %+v %v", audit, err)
	}
}
func TestPricingRecordsSQLite(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	exercisePricingRecords(t, s)
}
func TestPricingRecordsPostgres(t *testing.T) {
	dsn := os.Getenv("PI_GATEWAY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("PI_GATEWAY_TEST_POSTGRES_DSN is not set")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	defer admin.Close()
	schema := "pricing_test_" + strings.TrimPrefix(GenerateKey(), "sk-pi-")
	if _, err = admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Error(err)
		}
	}()
	cfg.RuntimeParams["search_path"] = schema
	registered := stdlib.RegisterConnConfig(cfg)
	defer stdlib.UnregisterConnConfig(registered)
	s, err := OpenOptions(Options{Driver: "postgres", DSN: registered})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	exercisePricingRecords(t, s)
}
