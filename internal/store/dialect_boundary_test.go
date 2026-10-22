package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

const boundaryTableSQL = `CREATE TABLE dialect_boundary_values (value TEXT PRIMARY KEY)`
const boundarySelectSQL = `SELECT value FROM dialect_boundary_values WHERE value=?`
const boundaryInsertSQL = `INSERT INTO dialect_boundary_values(value) VALUES (?)`

// Run through the shared SQLite/PostgreSQL backend contract so all six wrappers
// must preserve argument binding, even for values containing SQL and placeholders.
func exerciseStoreSQLBoundaries(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	_, err := s.ExecContext(ctx, boundaryTableSQL)
	requireDataNoError(t, err)
	const value = `'); DROP TABLE dialect_boundary_values; -- ? $1 "`
	_, err = s.ExecContext(ctx, boundaryInsertSQL, value)
	requireDataNoError(t, err)
	var got string
	requireDataNoError(t, s.QueryRowContext(ctx, boundarySelectSQL, value).Scan(&got))
	if got != value {
		t.Fatalf("store query row changed bound value: %q", got)
	}
	rows, err := s.QueryContext(ctx, boundarySelectSQL, value)
	requireDataNoError(t, err)
	assertBoundaryRows(t, rows, value)
	const replacement = `?'); DELETE FROM dialect_boundary_values; -- $2`
	err = s.withTransaction(ctx, nil, func(tx *storeTx) error {
		_, err = tx.ExecContext(ctx, `UPDATE dialect_boundary_values SET value=? WHERE value=?`, replacement, value)
		requireDataNoError(t, err)
		requireDataNoError(t, tx.QueryRowContext(ctx, boundarySelectSQL, replacement).Scan(&got))
		if got != replacement {
			t.Fatalf("transaction query row changed bound value: %q", got)
		}
		rows, err = tx.QueryContext(ctx, boundarySelectSQL, replacement)
		requireDataNoError(t, err)
		assertBoundaryRows(t, rows, replacement)
		return tx.Commit()
	})
	requireDataNoError(t, err)
	requireDataNoError(t, s.QueryRowContext(ctx, boundarySelectSQL, replacement).Scan(&got))
	if got != replacement {
		t.Fatalf("commit changed bound value: %q", got)
	}
	_, err = s.ExecContext(ctx, `DROP TABLE dialect_boundary_values`)
	requireDataNoError(t, err)
}

func assertBoundaryRows(t *testing.T, rows *sql.Rows, want string) {
	t.Helper()
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("missing bound value: %v", rows.Err())
	}
	var got string
	requireDataNoError(t, rows.Scan(&got))
	if got != want {
		t.Fatalf("query rows changed bound value: %q", got)
	}
	if rows.Next() {
		t.Fatal("bound predicate selected extra rows")
	}
	requireDataNoError(t, rows.Err())
}

func TestStoreTransactionOwnership(t *testing.T) {
	s := openTestStore(t)
	_, err := s.ExecContext(t.Context(), boundaryTableSQL)
	requireDataNoError(t, err)
	t.Run("early return", func(t *testing.T) { testStoreTransactionEarlyReturn(t, s) })
	t.Run("panic", func(t *testing.T) { testStoreTransactionPanic(t, s) })
	t.Run("canceled", func(t *testing.T) { testStoreTransactionCanceled(t, s) })
}

func testStoreTransactionEarlyReturn(t *testing.T, s *Store) {
	t.Helper()
	sentinel := errors.New("abort transaction")
	err := s.withTransaction(t.Context(), nil, func(tx *storeTx) error {
		if _, err := tx.ExecContext(t.Context(), boundaryInsertSQL, t.Name()); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("early return: %v", err)
	}
	assertBoundaryMissing(t, s)
}

func testStoreTransactionPanic(t *testing.T, s *Store) {
	t.Helper()
	sentinel := errors.New("panic transaction")
	func() {
		defer func() {
			if got := recover(); got != sentinel {
				t.Fatalf("panic changed: %v", got)
			}
		}()
		err := s.withTransaction(t.Context(), nil, func(tx *storeTx) error {
			_, err := tx.ExecContext(t.Context(), boundaryInsertSQL, t.Name())
			requireDataNoError(t, err)
			panic(sentinel)
		})
		t.Fatalf("callback panic was lost: %v", err)
	}()
	assertBoundaryMissing(t, s)
}

func testStoreTransactionCanceled(t *testing.T, s *Store) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := s.withTransaction(ctx, nil, func(tx *storeTx) error {
		_, err := tx.ExecContext(ctx, boundaryInsertSQL, t.Name())
		requireDataNoError(t, err)
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled callback error: %v", err)
	}
	assertBoundaryMissing(t, s)
	err = s.withTransaction(ctx, nil, func(tx *storeTx) error {
		t.Fatal("canceled transaction invoked callback")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquisition error: %v", err)
	}

}

func assertBoundaryMissing(t *testing.T, s *Store) {
	t.Helper()
	var got string
	if err := s.QueryRowContext(t.Context(), boundarySelectSQL, t.Name()).Scan(&got); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("uncommitted value escaped: %q err=%v", got, err)
	}
}

func TestStoreUncommittedSuccessRollsBack(t *testing.T) {
	s := openTestStore(t)
	_, err := s.ExecContext(t.Context(), boundaryTableSQL)
	requireDataNoError(t, err)
	err = s.withTransaction(t.Context(), nil, func(tx *storeTx) error {
		_, err := tx.ExecContext(t.Context(), boundaryInsertSQL, t.Name())
		return err
	})
	requireDataNoError(t, err)
	assertBoundaryMissing(t, s)
}

func TestRulePublicationDoesNotRetryCallback(t *testing.T) {
	s := openTestStore(t)
	sentinel := errors.New("database is locked")
	calls := 0
	err := s.withRulePublication(t.Context(), func(tx *storeTx) error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("callback replayed or error changed: calls=%d err=%v", calls, err)
	}
}

func TestStoreClosedDatabaseDoesNotInvokeTransaction(t *testing.T) {
	s := openTestStore(t)
	requireDataNoError(t, s.Close())
	err := s.withTransaction(t.Context(), nil, func(tx *storeTx) error {
		t.Fatal("failed acquisition invoked callback")
		return nil
	})
	if err == nil {
		t.Fatal("closed database acquired a transaction")
	}
}

// A real PostgreSQL deferred FK rejects Commit after the callback has produced a
// result. The lifetime wrapper must preserve both that result and the error.
func exerciseDeferredCommitFailure(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	_, err := s.ExecContext(ctx, `CREATE TABLE transaction_commit_parent (id INTEGER PRIMARY KEY)`)
	requireDataNoError(t, err)
	_, err = s.ExecContext(ctx, `CREATE TABLE transaction_commit_child (id INTEGER PRIMARY KEY,parent_id INTEGER REFERENCES transaction_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)`)
	requireDataNoError(t, err)
	var commitErr error
	result, err := withStoreTransaction(s, ctx, nil, func(tx *storeTx) (int, error) {
		_, err := tx.ExecContext(ctx, `INSERT INTO transaction_commit_child(id,parent_id) VALUES (1,999)`)
		requireDataNoError(t, err)
		commitErr = tx.Commit()
		return 123, commitErr
	})
	if commitErr == nil || !errors.Is(err, commitErr) || result != 123 {
		t.Fatalf("commit result/error changed: result=%d err=%v commit=%v", result, err, commitErr)
	}
	var count int
	requireDataNoError(t, s.QueryRowContext(ctx, `SELECT count(*) FROM transaction_commit_child`).Scan(&count))
	if count != 0 {
		t.Fatal("failed commit persisted a row")
	}
}

func exerciseReadOnlyTransactionOptions(t *testing.T, s *Store) {
	t.Helper()
	opts := &sql.TxOptions{ReadOnly: true}
	err := s.withTransaction(t.Context(), opts, func(tx *storeTx) error {
		var isolation string
		requireDataNoError(t, tx.QueryRowContext(t.Context(), `SHOW transaction_isolation`).Scan(&isolation))
		if isolation != "repeatable read" {
			t.Fatalf("read-only snapshot isolation changed: %s", isolation)
		}
		return tx.Commit()
	})
	requireDataNoError(t, err)
	if opts.Isolation != sql.LevelDefault || !opts.ReadOnly {
		t.Fatal("caller options were mutated")
	}
}

func TestTransactionBeginDiagnosticDoesNotWrapCallbackErrors(t *testing.T) {
	s := openTestStore(t)
	sentinel := errors.New("callback failure")
	err := s.withTransactionBeginContext(t.Context(), "begin fixture", func(tx *storeTx) error { return sentinel })
	if err != sentinel {
		t.Fatalf("callback error was relabeled: %v", err)
	}
	requireDataNoError(t, s.Close())
	err = s.withTransactionBeginContext(t.Context(), "begin fixture", func(tx *storeTx) error { t.Fatal("closed database invoked callback"); return nil })
	if err == nil || !strings.HasPrefix(err.Error(), "begin fixture: ") || errors.Unwrap(err) == nil {
		t.Fatalf("begin diagnostic/cause lost: %v", err)
	}
}
