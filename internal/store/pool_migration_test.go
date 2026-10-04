package store

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// This fixture deliberately spells out the old schema, independently of the
// production schema, so accidental regressions cannot change both together.
const legacyPoolSchema = `
CREATE TABLE accounts (
 id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL,email TEXT NOT NULL DEFAULT '',
 account_id TEXT NOT NULL DEFAULT '',plan_type TEXT NOT NULL DEFAULT '',access_token TEXT NOT NULL DEFAULT '',
 refresh_token TEXT NOT NULL DEFAULT '',id_token TEXT NOT NULL DEFAULT '',expires_at INTEGER NOT NULL DEFAULT 0,
 enabled INTEGER NOT NULL DEFAULT 1,weight INTEGER NOT NULL DEFAULT 1,concurrency INTEGER NOT NULL DEFAULT 3,
 proxy_url TEXT NOT NULL DEFAULT '',status TEXT NOT NULL DEFAULT 'unknown',last_error TEXT NOT NULL DEFAULT '',
 last_refresh_at INTEGER NOT NULL DEFAULT 0,last_used_at INTEGER NOT NULL DEFAULT 0,
 request_count INTEGER NOT NULL DEFAULT 0,error_count INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL
);
CREATE TABLE api_keys (
 id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL,key TEXT NOT NULL UNIQUE,enabled INTEGER NOT NULL DEFAULT 1,
 scope TEXT NOT NULL DEFAULT 'all',account_ids TEXT NOT NULL DEFAULT '[]',strategy TEXT NOT NULL DEFAULT '',
 transport TEXT NOT NULL DEFAULT '',last_used_at INTEGER NOT NULL DEFAULT 0,request_count INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL
);
INSERT INTO accounts(id,name,created_at,updated_at) VALUES (1,'one',10,20),(2,'two',10,20);
INSERT INTO api_keys(id,name,key,scope,account_ids,strategy,transport,last_used_at,request_count,created_at,updated_at)
 VALUES (1,'list','legacy-one','list','[2,1,2]','sticky','ws',30,7,10,20),
 (2,'same set','legacy-two','list','[1,2]','','',0,0,10,20),
 (3,'all','legacy-all','all','[1]','','',0,0,10,20),
 (4,'empty','legacy-empty','list','[]','','',0,0,10,20);
`

func storeTestPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")
	// Registered before database cleanup so handles close first. On Windows,
	// a successful file unlink can still leave the directory delete-pending;
	// retry removal of the whole private test directory, not just its files.
	t.Cleanup(func() {
		var err error
		for attempt := 0; attempt < 50; attempt++ {
			if err = os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Errorf("remove private SQLite test directory: %v", err)
	})
	return path
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(storeTestPath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func makeLegacyPool(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(legacyPoolSchema); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestOpenMigratesLegacyKeyGroupsIdempotently(t *testing.T) {
	path := storeTestPath(t)
	db := makeLegacyPool(t, path)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for pass := 0; pass < 2; pass++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		t.Cleanup(func() { _ = s.Close() })
		groups, err := s.ListAccountGroups(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(groups) != 1 {
			t.Fatalf("groups=%+v", groups)
		}
		g := groups[0]
		if g.Name != fmt.Sprintf("migrated:%x", sha1.Sum([]byte("1,2"))) || !reflect.DeepEqual(g.AccountIDs, []int64{1, 2}) || !g.Enabled {
			t.Fatalf("migrated group=%+v", g)
		}
		for _, id := range []int64{1, 2} {
			k, err := s.GetKey(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if k == nil || !reflect.DeepEqual(k.GroupIDs, []int64{g.ID}) {
				t.Fatalf("key %d=%+v", id, k)
			}
			if k.Label != "" || k.MaxConcurrency != 0 || k.RequestsPerMinute != 0 || k.DailyLimitUSD != 0 || k.WeeklyLimitUSD != 0 {
				t.Fatalf("key defaults=%+v", k)
			}
			if id == 1 && (k.Strategy != "sticky" || k.Transport != "ws" || k.LastUsedAt != 30 || k.RequestCount != 7 || k.CreatedAt != 10 || k.UpdatedAt != 20) {
				t.Fatalf("lost legacy data: %+v", k)
			}
		}
		for _, id := range []int64{3, 4} {
			ids, err := s.ListKeyGroupIDs(ctx, id)
			if err != nil || len(ids) != 0 {
				t.Fatalf("unscoped key %d: %v, %v", id, ids, err)
			}
		}
		checkNewPoolColumns(t, s.db)
		var count int
		if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND tbl_name='api_keys'`).Scan(&count); err != nil || count < 1 {
			t.Fatalf("key indexes: %d %v", count, err)
		}
		if _, err := s.db.Exec(`INSERT INTO api_keys(name,key,created_at,updated_at) VALUES ('duplicate','legacy-one',0,0)`); err == nil {
			t.Fatal("lost unique key constraint")
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	backups, err := filepath.Glob(path + ".bak-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup files=%v err=%v", backups, err)
	}
	backup, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var scope, ids string
	if err := backup.QueryRow(`SELECT scope,account_ids FROM api_keys WHERE id=1`).Scan(&scope, &ids); err != nil || scope != "list" || ids != "[2,1,2]" {
		t.Fatalf("backup not pre-migration: %s %s %v", scope, ids, err)
	}
	var newTables int
	if err := backup.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='account_groups'`).Scan(&newTables); err != nil || newTables != 0 {
		t.Fatalf("backup contains migrated schema: %d %v", newTables, err)
	}
}

func checkNewPoolColumns(t *testing.T, db *sql.DB) {
	t.Helper()
	for table, want := range map[string]map[string]string{
		"api_keys": {"label": "''", "max_concurrency": "0", "requests_per_minute": "0", "daily_limit_usd": "0", "weekly_limit_usd": "0"},
		"accounts": {"consecutive_failures": "0", "cooldown_until": "0", "cooldown_kind": "''", "last_started_at": "0", "ewma_first_output_ms": "0", "ewma_failure_rate_bp": "0"},
	} {
		rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var cid, notNull, pk int
			var name, typ string
			var dflt sql.NullString
			if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			if table == "api_keys" && (name == "scope" || name == "account_ids") {
				t.Errorf("legacy column survived: %s", name)
			}
			if v, ok := want[name]; ok {
				if notNull != 1 || !dflt.Valid || dflt.String != v {
					t.Errorf("%s.%s default=%v notNull=%d", table, name, dflt, notNull)
				}
				delete(want, name)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if len(want) != 0 {
			t.Fatalf("missing columns %s: %v", table, want)
		}
	}
	var nullable int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('usage_records') WHERE name IN ('input_tokens','cached_tokens','cache_write_tokens','output_tokens','reasoning_tokens','total_tokens','cost_micros') AND "notnull"=0`).Scan(&nullable); err != nil || nullable != 7 {
		t.Fatalf("nullable usage columns=%d err=%v", nullable, err)
	}
	var indexes int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name IN ('idx_usage_started','idx_usage_model','idx_usage_key','idx_usage_account','idx_usage_outcome')`).Scan(&indexes); err != nil || indexes != 5 {
		t.Fatalf("usage indexes=%d %v", indexes, err)
	}
}

func TestLegacyMigrationBackupIncludesWAL(t *testing.T) {
	path := storeTestPath(t)
	db := makeLegacyPool(t, path)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; UPDATE api_keys SET name='WAL-only' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	backups, _ := filepath.Glob(path + ".bak-*")
	if len(backups) != 1 {
		t.Fatalf("backups=%v", backups)
	}
	b, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var name string
	if err := b.QueryRow(`SELECT name FROM api_keys WHERE id=1`).Scan(&name); err != nil || name != "WAL-only" {
		t.Fatalf("backup lost WAL data: %q %v", name, err)
	}
}

func TestLegacyKeyMigrationRollsBackOnMalformedAccountIDs(t *testing.T) {
	path := storeTestPath(t)
	db := makeLegacyPool(t, path)
	if _, err := db.Exec(`UPDATE api_keys SET account_ids='broken' WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("malformed restriction must not become unrestricted")
	}
	check, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var scope string
	if err := check.QueryRow(`SELECT scope FROM api_keys WHERE id=1`).Scan(&scope); err != nil || scope != "list" {
		t.Fatalf("legacy table not preserved: %s %v", scope, err)
	}
	var n int
	if err := check.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='account_groups'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("migration transaction not rolled back: %d %v", n, err)
	}
}

func TestConcurrentLegacyKeyMigration(t *testing.T) {
	path := storeTestPath(t)
	db := makeLegacyPool(t, path)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Open(path)
			if err == nil {
				err = s.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(path + ".bak-*")
	if len(files) != 1 {
		t.Fatalf("backups=%v", files)
	}
}

func TestLegacyKeyMigrationPreservesSequence(t *testing.T) {
	path := storeTestPath(t)
	db := makeLegacyPool(t, path)
	if _, err := db.Exec(`INSERT INTO api_keys(id,name,key,created_at,updated_at) VALUES (100,'deleted','deleted',0,0); DELETE FROM api_keys WHERE id=100`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	k := &APIKey{Name: "new"}
	if err := s.CreateKey(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	if k.ID <= 100 {
		t.Fatalf("reused historical key ID %d", k.ID)
	}
}

func TestFreshStoreDoesNotBackup(t *testing.T) {
	path := storeTestPath(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(path + "*")
	for _, f := range files {
		if strings.Contains(f, ".bak-") {
			t.Fatalf("fresh database backed up: %s", f)
		}
	}
}
