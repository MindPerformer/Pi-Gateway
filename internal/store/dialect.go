package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Driver returns the normalized backend name (sqlite or postgres).
func (s *Store) Driver() string { return s.driver }

// Rebind translates portable store SQL to the selected backend. This is a thin
// dialect for this package's SQL, not a general SQLite-to-PostgreSQL translator.
// Callers using DB directly must supply native SQL or explicitly call Rebind.
func (s *Store) Rebind(query string) string {
	if s.driver != "postgres" {
		return query
	}
	query = strings.ReplaceAll(query, accountColumns, postgresAccountColumns())
	query = postgresExpressions.Replace(query)
	for _, ratio := range []string{"0.5", "0.9", "0.95"} {
		query = strings.ReplaceAll(query, "CAST(count(*)*"+ratio+" AS INTEGER)", "CAST(FLOOR(count(*)*"+ratio+") AS BIGINT)")
	}
	query = strings.ReplaceAll(query, "CAST(AVG(NULLIF(duration_ms,0)) AS INTEGER)", "CAST(TRUNC(AVG(NULLIF(duration_ms,0))) AS BIGINT)")
	return postgresPlaceholders(query)
}

var postgresExpressions = strings.NewReplacer(
	"instr(", "strpos(",
	"max(latency_ms-first_token_ms,1)", "GREATEST(latency_ms-first_token_ms,1)",
	"MAX(account_model_catalog.fetched_at,excluded.fetched_at)", "GREATEST(account_model_catalog.fetched_at,excluded.fetched_at)",
	"MAX(account_model_catalog.attempted_at,excluded.attempted_at)", "GREATEST(account_model_catalog.attempted_at,excluded.attempted_at)",
	"MAX(cooldown_until,?)", "GREATEST(cooldown_until,?)",
)

func postgresAccountColumns() string {
	prefix := accountColumns[:strings.Index(accountColumns, "(SELECT COALESCE(json_group_array")]
	return prefix + `(SELECT COALESCE(json_agg(group_id ORDER BY group_id), '[]'::json)::text
 FROM account_group_accounts WHERE account_id=accounts.id),
 (SELECT COALESCE(json_agg(json_build_object('name',g.name,'models',g.disabled_models::json)), '[]'::json)::text
 FROM account_groups g JOIN account_group_accounts ga ON ga.group_id=g.id
 WHERE ga.account_id=accounts.id AND g.enabled=1)`
}

// Preserve quoted strings/identifiers and comments, including PostgreSQL dollar
// quotes. A literal JSON ? operator can be escaped as ?? in portable SQL.
func postgresPlaceholders(query string) string {
	var out strings.Builder
	n := 0
	for i := 0; i < len(query); {
		start := i
		switch {
		case query[i] == '\'' || query[i] == '"':
			quote := query[i]
			i++
			for i < len(query) {
				if query[i] == quote {
					i++
					if i < len(query) && query[i] == quote {
						i++
						continue
					}
					break
				}
				i++
			}
		case strings.HasPrefix(query[i:], "--"):
			for i < len(query) && query[i] != '\n' {
				i++
			}
		case strings.HasPrefix(query[i:], "/*"):
			i += 2
			depth := 1
			for i < len(query) && depth > 0 {
				if strings.HasPrefix(query[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(query[i:], "*/") {
					depth--
					i += 2
				} else {
					i++
				}
			}
		case query[i] == '$':
			end := i + 1
			for end < len(query) && ((query[end] >= 'a' && query[end] <= 'z') || (query[end] >= 'A' && query[end] <= 'Z') || query[end] == '_' || (end > i+1 && query[end] >= '0' && query[end] <= '9')) {
				end++
			}
			if end < len(query) && query[end] == '$' {
				tag := query[i : end+1]
				if close := strings.Index(query[end+1:], tag); close >= 0 {
					i = end + 1 + close + len(tag)
				} else {
					i++
				}
			} else {
				i++
			}
		case query[i] == '?':
			if i+1 < len(query) && query[i+1] == '?' {
				out.WriteByte('?')
				i += 2
			} else {
				n++
				out.WriteByte('$')
				out.WriteString(strconv.Itoa(n))
				i++
			}
			continue
		default:
			i++
		}
		out.WriteString(query[start:i])
	}
	return out.String()
}

// ExecContext is the dialect-aware escape hatch for production callers.
func (s *Store) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.Rebind(query), args...)
}
func (s *Store) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, s.Rebind(query), args...)
}
func (s *Store) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, s.Rebind(query), args...)
}

type storeTx struct {
	*sql.Tx
	store *Store
}

func (s *Store) BeginTx(ctx context.Context, opts *sql.TxOptions) (*storeTx, error) {
	// PostgreSQL READ COMMITTED gives each statement a different snapshot;
	// read-only count/page transactions require the same snapshot as SQLite.
	if s.driver == "postgres" && opts != nil && opts.ReadOnly && opts.Isolation == sql.LevelDefault {
		copied := *opts
		copied.Isolation = sql.LevelRepeatableRead
		opts = &copied
	}
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &storeTx{Tx: tx, store: s}, nil
}
func (tx *storeTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.Tx.ExecContext(ctx, tx.store.Rebind(query), args...)
}
func (tx *storeTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.Tx.QueryContext(ctx, tx.store.Rebind(query), args...)
}
func (tx *storeTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.Tx.QueryRowContext(ctx, tx.store.Rebind(query), args...)
}

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) insertID(ctx context.Context, executor sqlExecutor, query string, args ...any) (int64, error) {
	if s.driver == "postgres" {
		var id int64
		err := executor.QueryRowContext(ctx, query+" RETURNING id", args...).Scan(&id)
		return id, err
	}
	result, err := executor.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func postgresSchema(sql string) string {
	return strings.NewReplacer("INTEGER PRIMARY KEY AUTOINCREMENT", "BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY", "INTEGER", "BIGINT", "REAL", "DOUBLE PRECISION").Replace(sql)
}

// postgresConnectionError deliberately discards the original error and its
// chain: pgx parsing and connection errors can include the complete secret DSN.
func postgresConnectionError(phase string, err error) error {
	category := "connection failed"
	var parseErr *pgconn.ParseConfigError
	switch {
	case errors.As(err, &parseErr):
		category = "invalid connection configuration"
	case errors.Is(err, context.DeadlineExceeded):
		category = "connection timeout"
	case errors.Is(err, context.Canceled):
		category = "connection canceled"
	}
	return fmt.Errorf("store: PostgreSQL %s: %s", phase, category)
}

const postgresSchemaVersion int64 = 7

const postgresVersionSchema = `CREATE TABLE IF NOT EXISTS schema_migrations (
 version BIGINT PRIMARY KEY CHECK (version > 0),
 applied_at BIGINT NOT NULL
)`

// checkPostgresSchemaVersion runs under the migration advisory lock and rejects
// forward-incompatible databases before any application schema is changed.
func checkPostgresSchemaVersion(ctx context.Context, tx *sql.Tx) (int64, error) {
	if _, err := tx.ExecContext(ctx, postgresVersionSchema); err != nil {
		return 0, err
	}
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, err
	}
	if version > postgresSchemaVersion {
		return 0, fmt.Errorf("store: PostgreSQL schema version %d is newer than supported version %d", version, postgresSchemaVersion)
	}
	return version, nil
}

func recordPostgresSchemaVersion(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES ($1,$2)`, postgresSchemaVersion, NowMS())
	if err != nil {
		return fmt.Errorf("store: record PostgreSQL schema version: %w", err)
	}
	return nil
}

func (s *Store) migratePostgres() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return postgresConnectionError("begin migration", err)
	}
	defer tx.Rollback()
	// Transaction-scoped lock serializes migrations across gateway processes.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(1885955943)`); err != nil {
		return err
	}
	version, err := checkPostgresSchemaVersion(ctx, tx)
	if err != nil {
		return err
	}
	if version == postgresSchemaVersion {
		return tx.Commit()
	}
	// Version 1 adopts unversioned PostgreSQL databases and creates fresh ones.
	// Version 2 adds per-source model catalogs; version 3 adds independent rules,
	// publication metadata and rule capture fields. Version 4 adds per-account
	// 429 cooldown overrides and account-group retry policy. Version 5 adds request
	// reasoning, actual service tier and settled billing details. Version 6 adds
	// durable admin sessions. Version 7 adds official daily usage snapshots. Replaying the
	// idempotent schema and additive columns upgrades all prior versions.
	// Execute statements individually: pgx extended protocol rejects multi-command prepares.
	for _, statement := range strings.Split(postgresSchema(schemaSQL), ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("store: PostgreSQL schema: %w", err)
		}
	}
	for table, columns := range additiveColumns() {
		for name, definition := range columns {
			statement := fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s %s", table, name, postgresSchema(definition))
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("store: add %s.%s: %w", table, name, err)
			}
		}
	}
	for _, statement := range postgresProxyBindingSchema {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("store: PostgreSQL proxy bindings: %w", err)
		}
	}
	if err := recordPostgresSchemaVersion(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

var postgresProxyBindingSchema = []string{
	`CREATE INDEX IF NOT EXISTS idx_accounts_proxy_id ON accounts(proxy_id)`,
	`CREATE OR REPLACE FUNCTION pi_account_proxy_sync() RETURNS trigger LANGUAGE plpgsql AS $proxy$
 BEGIN
  IF NEW.proxy_id IS NOT NULL THEN
   SELECT url INTO NEW.proxy_url FROM proxies WHERE id=NEW.proxy_id FOR SHARE;
  END IF;
  RETURN NEW;
 END; $proxy$`,
	`DROP TRIGGER IF EXISTS account_proxy_insert ON accounts`,
	`CREATE TRIGGER account_proxy_insert BEFORE INSERT ON accounts FOR EACH ROW EXECUTE FUNCTION pi_account_proxy_sync()`,
	`DROP TRIGGER IF EXISTS account_proxy_update ON accounts`,
	`CREATE TRIGGER account_proxy_update BEFORE UPDATE OF proxy_id, proxy_url ON accounts FOR EACH ROW EXECUTE FUNCTION pi_account_proxy_sync()`,
	`CREATE OR REPLACE FUNCTION pi_proxy_url_sync() RETURNS trigger LANGUAGE plpgsql AS $proxy$
 BEGIN
  IF OLD.url IS DISTINCT FROM NEW.url THEN
   UPDATE accounts SET proxy_url=NEW.url, updated_at=NEW.updated_at WHERE proxy_id=NEW.id;
  END IF;
  RETURN NEW;
 END; $proxy$`,
	`DROP TRIGGER IF EXISTS proxy_url_update ON proxies`,
	`CREATE TRIGGER proxy_url_update AFTER UPDATE OF url ON proxies FOR EACH ROW EXECUTE FUNCTION pi_proxy_url_sync()`,
}
