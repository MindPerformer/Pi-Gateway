package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func catalogRevisionForTest(t *testing.T, s *Store) string {
	t.Helper()
	revision, err := s.CatalogRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

// Both real backends run this contract. The second Store has a separate SQL pool
// and observes changes immediately, without process-local invalidation calls.
func exerciseCatalogRevisions(t *testing.T, first, second *Store) {
	t.Helper()
	ctx := context.Background()
	previous := catalogRevisionForTest(t, first)
	seen := map[string]bool{previous: true}
	assertChanged := func() {
		t.Helper()
		next := catalogRevisionForTest(t, first)
		parsed, err := uuid.Parse(next)
		if err != nil || parsed.Version() != 4 || seen[next] {
			t.Fatalf("reused/non-random revision %q: %v", next, err)
		}
		if got := catalogRevisionForTest(t, second); got != next {
			t.Fatalf("cross-Store stale epoch: %q != %q", got, next)
		}
		seen[next] = true
		previous = next
	}
	assertUnchanged := func() {
		t.Helper()
		for _, s := range []*Store{first, second} {
			if next := catalogRevisionForTest(t, s); next != previous {
				t.Fatalf("unexpected revision change %q => %q", previous, next)
			}
		}
	}
	a := &Account{Name: "revision-account", Enabled: true, SupplementalModels: []string{"manual"}}
	if err := first.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	assertChanged()
	// Identical writes with identical millisecond timestamps must still get
	// unique epochs. Epoch generation does not derive from fetched/attempted time.
	catalog := &ModelCatalog{Models: []CatalogModel{{ID: "upstream"}}, FetchedAt: 100, AttemptedAt: 100}
	for i := 0; i < 20; i++ {
		if err := first.SaveAccountModelCatalog(ctx, a.ID, catalog); err != nil {
			t.Fatal(err)
		}
		assertChanged()
	}
	if err := second.SaveAccountModelCatalog(ctx, a.ID, &ModelCatalog{Error: "refresh failed", AttemptedAt: 110}); err != nil {
		t.Fatal(err)
	}
	assertChanged()
	if got, err := first.GetAccountModelCatalog(ctx, a.ID); err != nil || got.FetchedAt != 100 || got.Error != "refresh failed" || len(got.Models) != 2 {
		t.Fatalf("failed refresh lost metadata: %+v err=%v", got, err)
	}
	for _, supplemental := range [][]string{{"new-manual"}, {"new-manual"}, {}} {
		if err := first.PatchAccountManagementFields(ctx, a.ID, AccountManagementPatch{SupplementalModels: supplemental}); err != nil {
			t.Fatal(err)
		}
		assertChanged()
		got, err := second.GetAccountModelCatalog(ctx, a.ID)
		if err != nil || len(got.Models) != 1+len(supplemental) {
			t.Fatalf("cross-Store stale manual list: %+v %v", got, err)
		}
	}
	// Metadata, eligibility and credentials are read live by callers and do not
	// belong to the cache epoch. UpdateAccount historically excludes model lists.
	a.SupplementalModels = []string{"must-not-overwrite"}
	a.AccessToken = "rotated-test-token"
	a.Enabled = false
	if err := first.UpdateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	assertUnchanged()
	if got, err := second.GetAccountModelCatalog(ctx, a.ID); err != nil || len(got.Models) != 1 {
		t.Fatal("UpdateAccount changed supplemental-model semantics")
	}
	name := "revision-renamed"
	if err := first.PatchAccountManagementFields(ctx, a.ID, AccountManagementPatch{Name: &name, DisabledModels: []string{"blocked"}}); err != nil {
		t.Fatal(err)
	}
	assertUnchanged()
	if err := first.UpdateAccountManagementFields(ctx, a.ID, name, true, 1, 3, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := first.UpdateAccountCredentials(ctx, a.ID, "next-access", "next-refresh", 500, AccountStatusReady, ""); err != nil {
		t.Fatal(err)
	}
	if err := first.MarkAccountUsed(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := first.SetAccountStatus(ctx, a.ID, AccountStatusReady, ""); err != nil {
		t.Fatal(err)
	}
	assertUnchanged()
	// A failed group validation rolls back the preceding supplemental write.
	if err := first.PatchAccountManagementFields(ctx, a.ID, AccountManagementPatch{SupplementalModels: []string{"rollback"}, GroupIDs: []int64{-99999}}); err == nil {
		t.Fatal("invalid group accepted")
	}
	assertUnchanged()
	if got, err := second.GetAccountModelCatalog(ctx, a.ID); err != nil || len(got.Models) != 1 {
		t.Fatal("failed transaction leaked manual-model mutation")
	}
	if err := first.CreateAccount(ctx, &Account{Name: "failed-create", GroupIDs: []int64{-99999}}); err == nil {
		t.Fatal("invalid create accepted")
	}
	assertUnchanged()
	for _, invalid := range []*ModelCatalog{nil, {}, {FetchedAt: 1, Models: []CatalogModel{{ID: "invalid", Metadata: map[string]json.RawMessage{"bad": json.RawMessage(`not-json`)}}}}} {
		// Nil, absent timestamp and invalid raw metadata cannot change revision.
		if err := first.SaveAccountModelCatalog(ctx, a.ID, invalid); err == nil {
			t.Fatal("invalid catalog accepted")
		}
		assertUnchanged()
	}
	// A revision written within an uncommitted transaction is not yet visible,
	// and rolling that transaction back leaves the authoritative epoch unchanged.
	tx, err := first.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := changeCatalogRevision(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if got := catalogRevisionForTest(t, second); got != previous {
		t.Fatal("uncommitted revision escaped transaction")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertUnchanged()
	if err := second.DeleteAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	assertChanged()
	if got, err := first.GetAccountModelCatalog(ctx, a.ID); err != nil || got != nil {
		t.Fatal("deleted catalog still readable")
	}
	if err := first.DeleteAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := first.SaveAccountModelCatalog(ctx, a.ID, catalog); err != nil {
		t.Fatal(err)
	}
	assertUnchanged()
}

func TestCatalogRevisionDialectContract(t *testing.T) {
	ctx := context.Background()
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			s, observer := observedCatalogStore(t)
			s.driver = dialect
			if got := catalogRevisionForTest(t, s); got != "0" {
				t.Fatalf("initial revision=%q", got)
			}
			// The revision upsert is portable and executes with native ?/$n
			// binding; no fake SQL results are used by this offline contract.
			tx, err := s.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := changeCatalogRevision(ctx, tx); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			observer.reset()
			revision := catalogRevisionForTest(t, s)
			if _, err := uuid.Parse(revision); err != nil {
				t.Fatal(err)
			}
			reads := observer.snapshot()
			if len(reads) != 1 || len(reads[0].args) != 1 || reads[0].args[0].Value != catalogRevisionSetting {
				t.Fatalf("revision is not one authoritative read: %+v", reads)
			}
			want := "SELECT value FROM settings WHERE key=?"
			if dialect == "postgres" {
				want = "SELECT value FROM settings WHERE key=$1"
			}
			if reads[0].sql != want {
				t.Fatalf("revision dialect SQL=%q want %q", reads[0].sql, want)
			}
		})
	}
}

func TestCatalogRevisionCrossStoreAndRestart(t *testing.T) {
	path := storeTestPath(t)
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	for _, s := range []*Store{first, second} {
		if got := catalogRevisionForTest(t, s); got != "0" {
			t.Fatalf("initial epoch=%q", got)
		}
	}
	exerciseCatalogRevisions(t, first, second)
	before := catalogRevisionForTest(t, second)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if after := catalogRevisionForTest(t, reopened); after != before {
		t.Fatal("revision lost across restart")
	}
	if err := reopened.CreateAccount(context.Background(), &Account{Name: "after-restart"}); err != nil {
		t.Fatal(err)
	}
	if after := catalogRevisionForTest(t, reopened); after == before || after == "0" {
		t.Fatal("restart reused old cache revision")
	}
}

func TestCatalogRevisionWriteFailureRollsBackEveryMutation(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := &Account{Name: "before", SupplementalModels: []string{"manual"}}
	if err := s.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAccountModelCatalog(ctx, a.ID, &ModelCatalog{Models: []CatalogModel{{ID: "old"}}, FetchedAt: 1}); err != nil {
		t.Fatal(err)
	}
	beforeRevision := catalogRevisionForTest(t, s)
	beforeCatalog, err := s.GetAccountModelCatalog(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Fail the epoch write after the catalog/account SQL has already succeeded.
	if _, err := s.ExecContext(ctx, `CREATE TRIGGER reject_catalog_revision BEFORE UPDATE ON settings
		WHEN NEW.key='`+catalogRevisionSetting+`' BEGIN SELECT RAISE(ABORT,'injected epoch write failure'); END`); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name string
		run  func() error
	}{
		{"save", func() error {
			return s.SaveAccountModelCatalog(ctx, a.ID, &ModelCatalog{Models: []CatalogModel{{ID: "new"}}, FetchedAt: 2})
		}},
		{"manual", func() error {
			return s.PatchAccountManagementFields(ctx, a.ID, AccountManagementPatch{SupplementalModels: []string{"new-manual"}})
		}},
		{"create", func() error { return s.CreateAccount(ctx, &Account{Name: "must-rollback"}) }},
		{"delete", func() error { return s.DeleteAccount(ctx, a.ID) }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			if err := mutation.run(); err == nil {
				t.Fatal("mutation ignored failed epoch persistence")
			}
			if got := catalogRevisionForTest(t, s); got != beforeRevision {
				t.Fatal("failed mutation changed revision")
			}
			got, err := s.GetAccountModelCatalog(ctx, a.ID)
			if err != nil || !reflect.DeepEqual(got, beforeCatalog) {
				t.Fatalf("failed mutation leaked data: %+v %v", got, err)
			}
			var count int
			if err := s.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("failed create/delete was not atomic: %d %v", count, err)
			}
		})
	}
	if _, err := s.ExecContext(ctx, `DROP TRIGGER reject_catalog_revision`); err != nil {
		t.Fatal(err)
	}
	// Failure in a later delete statement also rolls back the earlier account
	// deletion, rather than merely clearing an in-process cache.
	if _, err := s.ExecContext(ctx, `CREATE TRIGGER reject_catalog_delete BEFORE DELETE ON account_model_catalog
		BEGIN SELECT RAISE(ABORT,'injected catalog delete failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccount(ctx, a.ID); err == nil {
		t.Fatal("partial delete failure ignored")
	}
	if got := catalogRevisionForTest(t, s); got != beforeRevision {
		t.Fatal("partial delete changed epoch")
	}
	if got, err := s.GetAccountModelCatalog(ctx, a.ID); err != nil || !reflect.DeepEqual(got, beforeCatalog) {
		t.Fatal("partial delete escaped rollback")
	}
}

func TestCatalogRevisionInitialWriteFailureAndCancellation(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.ExecContext(ctx, `CREATE TRIGGER reject_initial_revision BEFORE INSERT ON settings
		WHEN NEW.key='`+catalogRevisionSetting+`' BEGIN SELECT RAISE(ABORT,'injected initial epoch failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAccount(ctx, &Account{Name: "rollback"}); err == nil {
		t.Fatal("initial epoch error ignored")
	}
	if got := catalogRevisionForTest(t, s); got != "0" {
		t.Fatal("failed first mutation persisted epoch")
	}
	var count int
	if err := s.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("first mutation escaped rollback: %d %v", count, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if revision, err := s.CatalogRevision(canceled); !errors.Is(err, context.Canceled) || revision != "" {
		t.Fatalf("canceled revision read=%q %v", revision, err)
	}
	if out, err := s.GetAccountModelCatalogs(canceled, []int64{1}); !errors.Is(err, context.Canceled) || out != nil {
		t.Fatalf("canceled catalog read: %v %v", out, err)
	}
	if out, err := s.DisabledModelsForAccounts(canceled, []int64{1}, nil); !errors.Is(err, context.Canceled) || out != nil {
		t.Fatalf("canceled restriction read: %v %v", out, err)
	}
}
