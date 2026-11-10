package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
func postgresPricingTestOptions(t *testing.T) Options {
	t.Helper()
	dsn := os.Getenv("PI_GATEWAY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("PI_GATEWAY_TEST_POSTGRES_DSN is not set")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL test configuration")
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = admin.Close() })
	schema := "pricing_test_" + strings.TrimPrefix(GenerateKey(), "sk-pi-")
	if _, err = admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Error(err)
		}
	})
	cfg.RuntimeParams["search_path"] = schema
	registered := stdlib.RegisterConnConfig(cfg)
	t.Cleanup(func() { stdlib.UnregisterConnConfig(registered) })
	return Options{Driver: "postgres", DSN: registered}
}

func TestPricingRecordsPostgres(t *testing.T) {
	s, err := OpenOptions(postgresPricingTestOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	exercisePricingRecords(t, s)
}

func TestPricingPostgresSchemaVersionAdvancesPastV7(t *testing.T) {
	// Version 7 was deployed before pricing tables existed. Leaving this at 7
	// makes migratePostgres return early even though pricing_state is absent.
	if postgresSchemaVersion <= 7 {
		t.Fatal("pricing tables require a PostgreSQL schema version newer than deployed version 7")
	}
}

func TestPricingSchemaUpgrade(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, existingPricing := range []bool{false, true} {
			name := backend + "/legacy_without_pricing"
			if existingPricing {
				name = backend + "/existing_pricing"
			}
			t.Run(name, func(t *testing.T) {
				opts := Options{Path: filepath.Join(t.TempDir(), "upgrade.sqlite")}
				if backend == "postgres" {
					opts = postgresPricingTestOptions(t)
				}
				exercisePricingSchemaUpgrade(t, opts, existingPricing)
			})
		}
	}
}

func exercisePricingSchemaUpgrade(t *testing.T, opts Options, existingPricing bool) {
	t.Helper()
	ctx := t.Context()
	first, err := OpenOptions(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	account := &Account{Name: "preserved", AccountID: "pricing-upgrade", AccessToken: "fixture-token", Enabled: true}
	if err := first.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	usageID := seedStoreRepricing(t, first)
	if existingPricing {
		exercisePricingRecords(t, first)
		if _, err := first.ApplyUsageRepricing(ctx, "upgrade-preview", storeRepricingUpdates(t, first), nil); err != nil {
			t.Fatal(err)
		}
	} else {
		// Match the pre-pricing release, including the version ledger that made
		// PostgreSQL previously skip all schema work on the next startup.
		for _, table := range []string{"pricing_state", "pricing_models", "pricing_sources", "pricing_runs", "pricing_history", "pricing_versions", "usage_repriced_costs"} {
			if _, err := first.ExecContext(ctx, `DROP TABLE `+table); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := first.LoadPricing(ctx); err == nil || !strings.Contains(err.Error(), "pricing_state") {
			t.Fatalf("legacy fixture did not reproduce missing pricing_state: %v", err)
		}
	}
	if opts.Driver == "postgres" {
		for _, statement := range []string{`DELETE FROM schema_migrations`, `INSERT INTO schema_migrations(version,applied_at) VALUES(7,1)`} {
			if _, err := first.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		s, err := OpenOptions(opts)
		if err != nil {
			t.Fatalf("upgrade/reopen %d: %v", pass, err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if !existingPricing && pass == 0 {
			exercisePricingRecords(t, s)
			if _, err := s.ApplyUsageRepricing(ctx, "upgrade-preview", storeRepricingUpdates(t, s), nil); err != nil {
				t.Fatal(err)
			}
		}
		records, err := s.LoadPricing(ctx)
		if err != nil || records.Revision != 1 || len(records.Models) != 1 || len(records.Sources) != 1 || len(records.Runs) != 1 || len(records.History) != 1 || len(records.Versions) != 1 {
			t.Fatalf("upgrade/reopen %d pricing: %+v err=%v", pass, records, err)
		}
		accounts, err := s.ListAccounts(ctx)
		if err != nil || len(accounts) != 1 || accounts[0].ID != account.ID || accounts[0].Name != account.Name || accounts[0].AccessToken != account.AccessToken {
			t.Fatalf("upgrade/reopen %d lost account data: err=%v", pass, err)
		}
		cost, err := s.AccountUsageCost(ctx, account.ID, UsageRange{}, 0)
		if err != nil || cost.CostMicros != 123 || cost.RepricedRequests != 1 || cost.UnpricedRequests != 0 {
			t.Fatalf("upgrade/reopen %d repriced cost: %+v err=%v", pass, cost, err)
		}
		var originalUnpriced bool
		var model string
		var input int64
		if err := s.QueryRowContext(ctx, `SELECT cost_micros IS NULL,model,input_tokens FROM usage_records WHERE id=?`, usageID).Scan(&originalUnpriced, &model, &input); err != nil || !originalUnpriced || model != "test-model" || input != 100 {
			t.Fatalf("upgrade/reopen %d changed original usage: unpriced=%t model=%q input=%d err=%v", pass, originalUnpriced, model, input, err)
		}
		if opts.Driver == "postgres" {
			var version, count int64
			if err := s.DB().QueryRow(`SELECT MAX(version),COUNT(*) FROM schema_migrations`).Scan(&version, &count); err != nil || version != postgresSchemaVersion || count != 2 {
				t.Fatalf("upgrade/reopen %d ledger: version=%d count=%d err=%v", pass, version, count, err)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
