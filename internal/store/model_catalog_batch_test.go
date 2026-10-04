package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"modernc.org/sqlite"
)

// catalogSQLObserver wraps only a test driver's reads; production has no counters
// or observer API. The same real SQLite engine accepts both ? and $n bindings,
// allowing offline execution of the actual PostgreSQL-rebound read statements.
// This is a placeholder contract test, not a replacement for real PostgreSQL CI.
type catalogSQLObserver struct {
	mu      sync.Mutex
	queries []catalogObservedQuery
}

type catalogObservedQuery struct {
	sql  string
	args []driver.NamedValue
}

func (o *catalogSQLObserver) reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.queries = nil
}

func (o *catalogSQLObserver) snapshot() []catalogObservedQuery {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]catalogObservedQuery(nil), o.queries...)
}

type catalogObserverConnector struct {
	observer *catalogSQLObserver
	driver   sqlite.Driver
}

func (c *catalogObserverConnector) Driver() driver.Driver { return &c.driver }
func (c *catalogObserverConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(":memory:")
	if err != nil {
		return nil, err
	}
	return &catalogObservedConn{Conn: conn, observer: c.observer}, nil
}

type catalogObservedConn struct {
	driver.Conn
	observer *catalogSQLObserver
}

func (c *catalogObservedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c *catalogObservedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}
func (c *catalogObservedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.observer.mu.Lock()
	c.observer.queries = append(c.observer.queries, catalogObservedQuery{query, append([]driver.NamedValue(nil), args...)})
	c.observer.mu.Unlock()
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func observedCatalogStore(t *testing.T) (*Store, *catalogSQLObserver) {
	t.Helper()
	observer := &catalogSQLObserver{}
	db := sql.OpenDB(&catalogObserverConnector{observer: observer})
	db.SetMaxOpenConns(1)
	s := &Store{db: db, driver: "sqlite"}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	observer.reset()
	return s, observer
}

func TestAccountModelBatchReadCountAndDialectContract(t *testing.T) {
	ctx := context.Background()
	s, observer := observedCatalogStore(t)
	const count = accountModelBatchSize*2 + 1
	// Fixture insertion is intentionally batched in a transaction and excluded
	// from observed read counts, so large boundary tests stay inexpensive.
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for id := 1; id <= count; id++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO accounts(id,name,created_at,updated_at) VALUES(?,?,0,0)`, id, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			s.driver = dialect
			for _, n := range []int{0, 1, 20, 100, accountModelBatchSize, accountModelBatchSize + 1, count} {
				t.Run(fmt.Sprint(n), func(t *testing.T) {
					ids := make([]int64, 0, n*2)
					for id := n; id > 0; id-- {
						ids = append(ids, int64(id), int64(id))
					}
					original := append([]int64{}, ids...)
					for _, restrictions := range []bool{false, true} {
						observer.reset()
						if restrictions {
							got, err := s.DisabledModelsForAccounts(ctx, ids, &APIKey{ID: 42})
							if err != nil || got == nil || len(got) != n {
								t.Fatalf("restrictions size=%d err=%v", len(got), err)
							}
						} else {
							got, err := s.GetAccountModelCatalogs(ctx, ids)
							if err != nil || got == nil || len(got) != n {
								t.Fatalf("catalog size=%d err=%v", len(got), err)
							}
						}
						queries := observer.snapshot()
						if len(queries) != (n+accountModelBatchSize-1)/accountModelBatchSize {
							t.Fatalf("N+1 regression: n=%d reads=%d", n, len(queries))
						}
						for batch, query := range queries {
							args := min(accountModelBatchSize, n-batch*accountModelBatchSize)
							if restrictions {
								args++
							}
							if len(query.args) != args || args > 501 {
								t.Fatalf("unbounded/wrong arguments: %d want %d", len(query.args), args)
							}
							if strings.Contains(query.sql, "access_token") || strings.Contains(query.sql, "refresh_token") {
								t.Fatal("catalog/policy read fetched account credentials")
							}
							if dialect == "postgres" {
								if strings.Contains(query.sql, "?") || !strings.Contains(query.sql, fmt.Sprintf("$%d", args)) {
									t.Fatalf("bad PostgreSQL bindings: %s", query.sql)
								}
							} else if strings.Count(query.sql, "?") != args {
								t.Fatalf("bad SQLite bindings: %s", query.sql)
							}
							for i, arg := range query.args {
								want := int64(batch*accountModelBatchSize + i + 1)
								if restrictions && i == len(query.args)-1 {
									want = 42
								}
								if arg.Ordinal != i+1 || arg.Value != want {
									t.Fatalf("unstable/wrong arguments: %+v want %d", arg, want)
								}
							}
						}
					}
					if !reflect.DeepEqual(ids, original) {
						t.Fatal("caller IDs mutated")
					}
				})
			}
		})
	}
	s.driver = "sqlite"
	for _, restrictions := range []bool{false, true} {
		observer.reset()
		for id := int64(1); id <= 100; id++ {
			if restrictions {
				_, err = s.DisabledModelsForAccount(ctx, id, nil)
			} else {
				_, err = s.GetAccountModelCatalog(ctx, id)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if got := len(observer.snapshot()); got != 100 {
			t.Fatalf("individual SQL baseline=%d want 100", got)
		}
	}
	allIDs := make([]int64, count)
	for i := range allIDs {
		allIDs[i] = int64(i + 1)
	}
	// A corrupt policy/catalog in batch three must discard both good batches.
	if _, err := s.ExecContext(ctx, `UPDATE accounts SET disabled_models='null', supplemental_models='null' WHERE id=?`, count); err != nil {
		t.Fatal(err)
	}
	if out, err := s.GetAccountModelCatalogs(ctx, allIDs); err == nil || out != nil {
		t.Fatal("late catalog failure returned partial results")
	}
	if out, err := s.DisabledModelsForAccounts(ctx, allIDs, nil); err == nil || out != nil {
		t.Fatal("late policy failure returned partial results")
	}
	if _, err := s.ExecContext(ctx, `DELETE FROM accounts WHERE id=?`, count); err != nil {
		t.Fatal(err)
	}
	if out, err := s.DisabledModelsForAccounts(ctx, allIDs, nil); !errors.Is(err, sql.ErrNoRows) || out != nil {
		t.Fatal("late unknown ID returned partial restrictions")
	}
}

// Shared contract is also run by the optional real PostgreSQL integration test.
func exerciseAccountModelBatches(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	a := &Account{Name: "batch-rich", Enabled: true, SupplementalModels: []string{"shared", "manual"}, DisabledModels: []string{"account-blocked", "shared"}}
	b := &Account{Name: "batch-empty", Enabled: true}
	for _, account := range []*Account{a, b} {
		if err := s.CreateAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	metadata := map[string]json.RawMessage{
		"large":  json.RawMessage(`9007199254740993123456789`),
		"nested": json.RawMessage(`{"x":[null,false,[],{"huge":18446744073709551617}]}`),
		"empty":  json.RawMessage(`[]`), "null": json.RawMessage(`null`),
		"false": json.RawMessage(`false`), "sensitive_unknown": json.RawMessage(`"preserved-not-sanitized-by-store"`),
	}
	if err := s.SaveAccountModelCatalog(ctx, a.ID, &ModelCatalog{Models: []CatalogModel{
		{ID: " shared ", Name: "Rich", Description: "Complete", Metadata: metadata},
		{ID: "shared", Name: "duplicate"}, {ID: "", Name: "empty"},
	}, FetchedAt: 10, AttemptedAt: 11}); err != nil {
		t.Fatal(err)
	}
	missing := max(a.ID, b.ID) + 999999
	ids := []int64{b.ID, missing, a.ID, a.ID, 0, -1}
	got, err := s.GetAccountModelCatalogs(ctx, ids)
	if err != nil || len(got) != 2 || got[missing] != nil {
		t.Fatalf("catalog unknown/duplicate semantics: %+v err=%v", got, err)
	}
	for _, account := range []*Account{a, b} {
		one, err := s.GetAccountModelCatalog(ctx, account.ID)
		if err != nil || !reflect.DeepEqual(one, got[account.ID]) {
			t.Fatalf("single/batch mismatch: %+v %+v err=%v", one, got[account.ID], err)
		}
	}
	if !reflect.DeepEqual(got[a.ID].Models[0].Metadata, metadata) || len(got[a.ID].Models) != 2 || !reflect.DeepEqual(got[a.ID].Models[1], NewManualCatalogModel("manual")) {
		t.Fatalf("metadata/manual defaults changed: %+v", got[a.ID])
	}
	if got[b.ID].Models == nil || got[b.ID].FetchedAt != 0 || len(got[b.ID].Models) != 0 {
		t.Fatal("empty account fabricated fetched models")
	}
	member := &AccountGroup{Name: "batch-member", Enabled: true, AccountIDs: []int64{a.ID}, DisabledModels: []string{"membership-blocked", "shared"}}
	keyGroup := &AccountGroup{Name: "batch-key", Enabled: true, DisabledModels: []string{"key-blocked", "shared"}}
	disabled := &AccountGroup{Name: "batch-disabled", Enabled: false, AccountIDs: []int64{a.ID, b.ID}, DisabledModels: []string{"ignored"}}
	for _, group := range []*AccountGroup{member, keyGroup, disabled} {
		if err := s.CreateAccountGroup(ctx, group); err != nil {
			t.Fatal(err)
		}
	}
	key := &APIKey{Name: "batch-key", GroupIDs: []int64{member.ID}}
	if err := s.CreateKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	// Leave key.GroupIDs stale while changing its authoritative database bindings.
	if err := s.SetKeyGroups(ctx, key.ID, []int64{keyGroup.ID, disabled.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecContext(ctx, `UPDATE account_groups SET disabled_models='not-json' WHERE id=?`, disabled.ID); err != nil {
		t.Fatal(err)
	}
	assertRestrictions := func(k *APIKey, wantA, wantB []string) {
		t.Helper()
		out, err := s.DisabledModelsForAccounts(ctx, []int64{b.ID, a.ID, a.ID}, k)
		if err != nil || len(out) != 2 || !reflect.DeepEqual(out[a.ID], wantA) || !reflect.DeepEqual(out[b.ID], wantB) {
			t.Fatalf("restrictions=%+v err=%v want=%v/%v", out, err, wantA, wantB)
		}
		for _, account := range []*Account{a, b} {
			one, err := s.DisabledModelsForAccount(ctx, account.ID, k)
			if err != nil || !reflect.DeepEqual(one, out[account.ID]) {
				t.Fatalf("single/batch restrictions mismatch: %v %v err=%v", one, out[account.ID], err)
			}
		}
	}
	assertRestrictions(key, []string{"account-blocked", "key-blocked", "membership-blocked", "shared"}, []string{"key-blocked", "shared"})
	assertRestrictions(nil, []string{"account-blocked", "membership-blocked", "shared"}, []string{})
	if err := s.SetKeyGroups(ctx, key.ID, []int64{}); err != nil {
		t.Fatal(err)
	}
	assertRestrictions(key, []string{"account-blocked", "membership-blocked", "shared"}, []string{})
	if err := s.SetAccountGroupAccounts(ctx, member.ID, []int64{b.ID}); err != nil {
		t.Fatal(err)
	}
	assertRestrictions(key, []string{"account-blocked", "shared"}, []string{"membership-blocked", "shared"})
	for _, id := range []int64{missing, 0, -1} {
		if out, err := s.DisabledModelsForAccounts(ctx, []int64{a.ID, id}, key); !errors.Is(err, sql.ErrNoRows) || out != nil {
			t.Fatalf("unknown ID failed open: %v %v", out, err)
		}
		if out, err := s.DisabledModelsForAccount(ctx, id, key); !errors.Is(err, sql.ErrNoRows) || out != nil {
			t.Fatalf("legacy missing semantics changed: %v %v", out, err)
		}
	}
	// All applicable policy sources must fail closed. Disabled groups above are
	// ignored even when malformed; newly enabled groups must fail immediately.
	if err := s.SetKeyGroups(ctx, key.ID, []int64{keyGroup.ID}); err != nil {
		t.Fatal(err)
	}
	for _, source := range []struct {
		table string
		id    int64
	}{{"accounts", a.ID}, {"account_groups", member.ID}, {"account_groups", keyGroup.ID}} {
		for _, malformed := range []string{"not-json", "null", `{}`, `[123]`, `[null]`} {
			query := "UPDATE " + source.table + " SET disabled_models=? WHERE id=?"
			if _, err := s.ExecContext(ctx, query, malformed, source.id); err != nil {
				t.Fatal(err)
			}
			if out, err := s.DisabledModelsForAccounts(ctx, []int64{a.ID, b.ID}, key); err == nil || out != nil {
				t.Fatalf("malformed %s %q failed open: %v %v", source.table, malformed, out, err)
			}
			if _, err := s.ExecContext(ctx, query, `[]`, source.id); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, field := range []struct{ table, column, idColumn string }{
		{"account_model_catalog", "models_json", "account_id"}, {"accounts", "supplemental_models", "id"},
	} {
		query := "UPDATE " + field.table + " SET " + field.column + "=? WHERE " + field.idColumn + "=?"
		if _, err := s.ExecContext(ctx, query, "not-json", a.ID); err != nil {
			t.Fatal(err)
		}
		if out, err := s.GetAccountModelCatalogs(ctx, []int64{a.ID, b.ID}); err == nil || out != nil {
			t.Fatalf("malformed catalog returned partial/success: %v %v", out, err)
		}
		if _, err := s.ExecContext(ctx, query, "[]", a.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAccountModelBatchSemantics(t *testing.T) {
	exerciseAccountModelBatches(t, openTestStore(t))
}

func TestAccountModelBatchEmptyInputAndDatabaseFailures(t *testing.T) {
	s := openTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, ids := range [][]int64{nil, {}} {
		if out, err := s.GetAccountModelCatalogs(ctx, ids); err != nil || out == nil || len(out) != 0 {
			t.Fatalf("empty catalog unexpectedly read DB: %v %v", out, err)
		}
		if out, err := s.DisabledModelsForAccounts(ctx, ids, nil); err != nil || out == nil || len(out) != 0 {
			t.Fatalf("empty policy unexpectedly read DB: %v %v", out, err)
		}
	}
	if out, err := s.GetAccountModelCatalogs(ctx, []int64{1}); err == nil || out != nil {
		t.Fatal("catalog DB error swallowed")
	}
	if out, err := s.DisabledModelsForAccounts(ctx, []int64{1}, nil); err == nil || out != nil {
		t.Fatal("policy DB error swallowed")
	}
	if revision, err := s.CatalogRevision(ctx); err == nil || revision != "" {
		t.Fatal("revision DB error silently defaulted")
	}
}
