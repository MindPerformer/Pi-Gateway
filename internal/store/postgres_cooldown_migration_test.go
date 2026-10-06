package store

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Version 3 was already recorded before cooldown/retry columns were added.
// Opening such a database must upgrade it rather than return early at version 3.
func TestPostgresV3CooldownMigration(t *testing.T) {
	dsn := os.Getenv("PI_GATEWAY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("PI_GATEWAY_TEST_POSTGRES_DSN is not set")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL test configuration")
	}
	admin := stdlib.OpenDB(*cfg)
	defer admin.Close()
	schema := "cooldown_test_" + strings.TrimPrefix(GenerateKey(), "sk-pi-")
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
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
	opts := Options{Driver: "postgres", DSN: registered}
	ctx := context.Background()
	first, err := OpenOptions(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	account := &Account{Name: "preserved", AccountID: "preserved-account", AccessToken: "fixture-token", Enabled: true}
	if err := first.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	group := &AccountGroup{Name: "preserved-group", Enabled: true, AccountIDs: []int64{account.ID}}
	if err := first.CreateAccountGroup(ctx, group); err != nil {
		t.Fatal(err)
	}
	// Emulate the deployed version 3 schema, preserving user data and membership.
	for _, statement := range []string{
		`ALTER TABLE accounts DROP COLUMN cooldown_429_seconds`,
		`ALTER TABLE account_groups DROP COLUMN switch_on_429`,
		`DELETE FROM schema_migrations`,
		`INSERT INTO schema_migrations(version,applied_at) VALUES(3,1)`,
	} {
		if _, err := first.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := first.ListAccounts(ctx); err == nil || !strings.Contains(err.Error(), "cooldown_429_seconds") {
		t.Fatalf("legacy fixture did not reproduce missing cooldown column: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		s, err := OpenOptions(opts)
		if err != nil {
			t.Fatalf("upgrade/reopen %d: %v", pass, err)
		}
		defer s.Close()
		accounts, err := s.ListAccounts(ctx)
		if err != nil || len(accounts) != 1 {
			t.Fatalf("upgraded account query: count=%d err=%v", len(accounts), err)
		}
		a := accounts[0]
		wantCooldown := -1
		if pass == 1 {
			wantCooldown = 75
		}
		if a.ID != account.ID || a.Name != account.Name || a.AccessToken != account.AccessToken || a.Cooldown429Seconds != wantCooldown || len(a.GroupIDs) != 1 || a.GroupIDs[0] != group.ID {
			t.Fatalf("upgrade/reopen changed account state: %+v", a)
		}
		groups, err := s.ListAccountGroups(ctx)
		if err != nil || len(groups) != 1 || groups[0].Name != group.Name || groups[0].SwitchOn429 != "inherit" {
			t.Fatalf("upgraded group query: %+v err=%v", groups, err)
		}
		var version, count int64
		if err := s.DB().QueryRow(`SELECT MAX(version),COUNT(*) FROM schema_migrations`).Scan(&version, &count); err != nil || version != postgresSchemaVersion || count != 2 {
			t.Fatalf("upgrade ledger version=%d count=%d err=%v", version, count, err)
		}
		if pass == 0 {
			value := 75
			if err := s.PatchAccountManagementFields(ctx, account.ID, AccountManagementPatch{Cooldown429Seconds: &value}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
