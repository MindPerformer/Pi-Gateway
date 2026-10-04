package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDatabaseConfiguration(t *testing.T) {
	t.Setenv("PI_GATEWAY_DATABASE_DRIVER", "")
	t.Setenv("PI_GATEWAY_DATABASE_DSN", "")
	t.Setenv("PI_GATEWAY_DATABASE", "")
	cfg, err := Load("")
	if err != nil || cfg.Data.Driver != "sqlite" || cfg.Data.Database == "" {
		t.Fatalf("defaults=%+v err=%v", cfg, err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("data:\n  driver: PostgreSQL\n  dsn: postgres://localhost/gateway\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// An explicitly empty environment DSN clears YAML, so required DSNs fail closed.
	if _, err := Load(path); err == nil {
		t.Fatal("empty PostgreSQL DSN accepted")
	}
	t.Setenv("PI_GATEWAY_DATABASE_DSN", "postgres://localhost/override")
	cfg, err = Load(path)
	if err != nil || cfg.Data.Driver != "postgres" || cfg.Data.DSN != "postgres://localhost/override" {
		t.Fatalf("environment=%+v err=%v", cfg, err)
	}
	t.Setenv("PI_GATEWAY_DATABASE_DRIVER", "sqlite")
	t.Setenv("PI_GATEWAY_DATABASE_DSN", "")
	t.Setenv("PI_GATEWAY_DATABASE", "legacy.db")
	cfg, err = Load(path)
	if err != nil || cfg.Data.Driver != "sqlite" || cfg.Data.Database != "legacy.db" {
		t.Fatalf("legacy=%+v err=%v", cfg, err)
	}
}

func TestDatabaseValidation(t *testing.T) {
	for _, tc := range []struct {
		driver, dsn, path string
		valid             bool
	}{
		{"sqlite", "", "database.db", true},
		{"sqlite", ":memory:", "", true},
		{"sqlite", "", "", false},
		{"postgres", "postgres://localhost/gateway", "", true},
		{"pgx", "host=localhost dbname=gateway", "", true},
		{"postgres", "", "database.db", false},
		{"mysql", "localhost", "", false},
	} {
		cfg := Default()
		cfg.Data = DataConfig{Driver: tc.driver, DSN: tc.dsn, Database: tc.path}
		if err := cfg.Validate(); (err == nil) != tc.valid {
			t.Errorf("driver=%s valid=%v err=%v", tc.driver, tc.valid, err)
		}
	}
}
