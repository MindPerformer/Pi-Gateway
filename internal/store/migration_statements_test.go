package store

import (
	"fmt"
	"strings"
	"testing"
)

func TestAdditiveMigrationStatements(t *testing.T) {
	columns := additiveColumns()
	if len(additiveMigrationStatements) != len(columns) {
		t.Fatal("migration tables differ from canonical schema")
	}
	for table, definitions := range columns {
		t.Run(table, func(t *testing.T) { assertMigrationStatements(t, table, definitions) })
	}
}

func assertMigrationStatements(t *testing.T, table string, definitions map[string]string) {
	t.Helper()
	if len(additiveMigrationStatements[table]) != len(definitions) {
		t.Fatal("migration columns differ from canonical schema")
	}
	for column, definition := range definitions {
		statement, err := registeredAdditiveMigration(table, column)
		requireDataNoError(t, err)
		wantSQLite := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition)
		wantPostgres := fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s %s", table, column, postgresSchema(definition))
		if statement.sqlite != wantSQLite || statement.postgres != wantPostgres {
			t.Errorf("%s: static SQL differs from canonical migration", column)
		}
	}
}

func TestAdditiveMigrationRejectsUnregisteredNames(t *testing.T) {
	for _, names := range [][2]string{{"unknown", "label"}, {"api_keys", "unknown"}, {"api_keys; DROP TABLE accounts; --", "label"}, {"api_keys", "label TEXT; DROP TABLE accounts; --"}} {
		statement, err := registeredAdditiveMigration(names[0], names[1])
		if err == nil || statement.sqlite != "" || statement.postgres != "" {
			t.Fatalf("unregistered migration accepted: %q", names)
		}
	}
}

func TestAPIKeySchemaMatchesLegacyRebuild(t *testing.T) {
	s := openTestStore(t)
	_, err := s.db.ExecContext(t.Context(), legacyAPIKeyTableSQL)
	requireDataNoError(t, err)
	initial := sqliteTableDefinition(t, s, "api_keys")
	rebuilt := sqliteTableDefinition(t, s, "api_keys_new")
	if initial != rebuilt {
		t.Fatalf("legacy rebuild differs from initial schema:\ninitial: %s\nrebuilt: %s", initial, rebuilt)
	}
}

func sqliteTableDefinition(t *testing.T, s *Store, table string) string {
	t.Helper()
	var ddl string
	err := s.db.QueryRowContext(t.Context(), `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl)
	requireDataNoError(t, err)
	_, definition, ok := strings.Cut(ddl, "(")
	if !ok {
		t.Fatalf("table %q has no definition: %q", table, ddl)
	}
	return definition
}
