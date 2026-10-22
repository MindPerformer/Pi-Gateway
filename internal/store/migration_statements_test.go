package store

import (
	"fmt"
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
