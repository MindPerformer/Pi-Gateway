package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestModelCatalogPersistsFailureAndConcurrentAttemptOrdering(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		// Windows can defer removal of SQLite's just-closed WAL files. Retry
		// only fixture cleanup, bounded to 200ms; never suppress final failure.
		var cleanupErr error
		for attempt := 0; attempt < 10; attempt++ {
			cleanupErr = os.RemoveAll(dir)
			if cleanupErr == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Errorf("remove catalog database fixture: %v", cleanupErr)
	})
	a := &Account{Name: "catalog", Enabled: true}
	if err := s.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	get := func() *ModelCatalog {
		t.Helper()
		got, err := s.GetAccountModelCatalog(ctx, a.ID)
		if err != nil || got == nil {
			t.Fatalf("catalog=%+v err=%v", got, err)
		}
		return got
	}
	if got := get(); got.Models == nil || len(got.Models) != 0 || got.FetchedAt != 0 {
		t.Fatalf("new account has fabricated catalog: %+v", got)
	}
	if got, err := s.GetAccountModelCatalog(ctx, a.ID+1); err != nil || got != nil {
		t.Fatalf("missing account catalog=%+v err=%v", got, err)
	}
	save := func(c *ModelCatalog) {
		t.Helper()
		if err := s.SaveAccountModelCatalog(ctx, a.ID, c); err != nil {
			t.Fatal(err)
		}
	}
	save(&ModelCatalog{Models: []CatalogModel{{ID: "model-real", Name: "Real model", Description: "live"}}, FetchedAt: 100, AttemptedAt: 90})
	save(&ModelCatalog{Models: []CatalogModel{{ID: "must-not-replace"}}, FetchedAt: 999, AttemptedAt: 110, Error: "HTTP 403"})
	if got := get(); len(got.Models) != 1 || got.Models[0].ID != "model-real" || got.FetchedAt != 100 || got.AttemptedAt != 110 || got.Error != "HTTP 403" {
		t.Fatalf("failure destroyed successful data: %+v", got)
	}
	// Older successful fetch can improve old data but cannot clear a newer error.
	save(&ModelCatalog{Models: []CatalogModel{{ID: "model-new", Name: "New"}}, FetchedAt: 105, AttemptedAt: 100})
	if got := get(); got.Models[0].ID != "model-new" || got.Error != "HTTP 403" || got.AttemptedAt != 110 {
		t.Fatalf("out-of-order success lost recent failure: %+v", got)
	}
	save(&ModelCatalog{Models: []CatalogModel{{ID: "stale"}}, FetchedAt: 99, AttemptedAt: 80})
	if got := get(); got.Models[0].ID != "model-new" {
		t.Fatal("stale success rolled catalog back")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := get(); got.Models[0].ID != "model-new" || got.Error != "HTTP 403" {
		t.Fatalf("snapshot did not persist across reopen: %+v", got)
	}
	// A genuine empty successful list is authoritative, unlike a failed fetch.
	save(&ModelCatalog{FetchedAt: 120, AttemptedAt: 120})
	if got := get(); got.Models == nil || len(got.Models) != 0 || got.Error != "" {
		t.Fatalf("empty success not stored: %+v", got)
	}
	if err := s.DeleteAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	save(&ModelCatalog{FetchedAt: 130, AttemptedAt: 130})
	var rows int
	if err := s.DB().QueryRow("SELECT COUNT(*) FROM account_model_catalog WHERE account_id=?", a.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("deleted account left/recreated catalog: rows=%d err=%v", rows, err)
	}
}

func TestModelCatalogManualMergeKeepsUpstreamPersistenceSeparate(t *testing.T) {
	ctx := context.Background()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	s.DB().SetMaxOpenConns(1)
	t.Cleanup(func() { _ = s.Close() })
	a := &Account{Name: "manual", Enabled: true, SupplementalModels: []string{" shared ", "manual-only", "shared", " "}}
	if err := s.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	get := func(want []CatalogModel, fetched, attempted int64, wantError string) *ModelCatalog {
		t.Helper()
		got, err := s.GetAccountModelCatalog(ctx, a.ID)
		if err != nil || got == nil {
			t.Fatalf("catalog=%+v err=%v", got, err)
		}
		if !reflect.DeepEqual(got.Models, want) || got.FetchedAt != fetched || got.AttemptedAt != attempted || got.Error != wantError {
			t.Fatalf("catalog=%+v want models=%+v fetched=%d attempted=%d error=%q", got, want, fetched, attempted, wantError)
		}
		return got
	}
	save := func(catalog *ModelCatalog) {
		t.Helper()
		if err := s.SaveAccountModelCatalog(ctx, a.ID, catalog); err != nil {
			t.Fatal(err)
		}
	}
	manual := []CatalogModel{NewManualCatalogModel("manual-only"), NewManualCatalogModel("shared")}
	get(manual, 0, 0, "")
	var rows int
	if err := s.DB().QueryRow("SELECT COUNT(*) FROM account_model_catalog").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("read fabricated upstream snapshot: rows=%d err=%v", rows, err)
	}
	save(&ModelCatalog{AttemptedAt: 90, Error: "initial failure"})
	get(manual, 0, 90, "initial failure")
	save(&ModelCatalog{Models: []CatalogModel{
		{ID: " shared ", Name: "Upstream name", Description: "upstream description"},
		{ID: "shared", Name: "Duplicate"}, {ID: "upstream-only", Name: "Upstream only"},
	}, FetchedAt: 100, AttemptedAt: 100})
	merged := []CatalogModel{
		{ID: "shared", Name: "Upstream name", Description: "upstream description", Source: "upstream"},
		{ID: "upstream-only", Name: "Upstream only", Source: "upstream"},
		NewManualCatalogModel("manual-only"),
	}
	snapshot := get(merged, 100, 100, "")
	// Even an accidental save of a merged read cannot persist artificial models.
	save(snapshot)
	var raw string
	if err := s.DB().QueryRow("SELECT models_json FROM account_model_catalog WHERE account_id=?", a.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var persisted []CatalogModel
	if err := json.Unmarshal([]byte(raw), &persisted); err != nil || len(persisted) != 2 {
		t.Fatalf("persisted=%s err=%v", raw, err)
	}
	for _, model := range persisted {
		if model.Source != "" || model.ID == "manual-only" {
			t.Fatalf("manual merge contaminated upstream snapshot: %s", raw)
		}
	}
	save(&ModelCatalog{Models: []CatalogModel{{ID: "failed-result"}}, FetchedAt: 999, AttemptedAt: 110, Error: "HTTP 403"})
	get(merged, 100, 110, "HTTP 403")
	// Upstream removal changes the duplicate's source back to manual.
	save(&ModelCatalog{FetchedAt: 120, AttemptedAt: 120})
	get(manual, 120, 120, "")
	if err := s.PatchAccountManagementFields(ctx, a.ID, AccountManagementPatch{SupplementalModels: []string{"shared"}}); err != nil {
		t.Fatal(err)
	}
	get(manual[1:], 120, 120, "")
	if err := s.PatchAccountManagementFields(ctx, a.ID, AccountManagementPatch{SupplementalModels: []string{}}); err != nil {
		t.Fatal(err)
	}
	get([]CatalogModel{}, 120, 120, "")
}

func TestManualCatalogModelReasoningDefaultsAreIndependent(t *testing.T) {
	first := NewManualCatalogModel("manual-one")
	second := NewManualCatalogModel("manual-two")
	if first.ID != "manual-one" || first.Name != first.ID || first.Source != "manual" {
		t.Fatalf("incorrect manual identity: %+v", first)
	}
	var levels []struct {
		Effort      string `json:"effort"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(first.Metadata["supported_reasoning_levels"], &levels); err != nil {
		t.Fatal(err)
	}
	var efforts []string
	for _, level := range levels {
		efforts = append(efforts, level.Effort)
		if level.Description == "" {
			t.Fatal("manual reasoning level has no description")
		}
	}
	if !reflect.DeepEqual(efforts, []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("manual reasoning levels: %v", efforts)
	}
	for key, want := range map[string]string{
		"default_reasoning_level":              `"medium"`,
		"supports_reasoning_summary_parameter": `true`,
		"default_reasoning_summary":            `"auto"`,
	} {
		if string(first.Metadata[key]) != want {
			t.Fatalf("manual %s=%s want %s", key, first.Metadata[key], want)
		}
	}
	if len(first.Metadata) != 4 {
		t.Fatalf("manual model fabricated additional capabilities: %v", first.Metadata)
	}
	// Defaults must not share either a mutable map or RawMessage backing arrays.
	first.Metadata["default_reasoning_level"][1] = 'X'
	delete(first.Metadata, "default_reasoning_summary")
	if string(second.Metadata["default_reasoning_level"]) != `"medium"` || string(second.Metadata["default_reasoning_summary"]) != `"auto"` {
		t.Fatalf("manual defaults alias across models: %v", second.Metadata)
	}
}

func TestModelCatalogMetadataPersistenceAndLegacyJSON(t *testing.T) {
	ctx := context.Background()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	s.DB().SetMaxOpenConns(1)
	t.Cleanup(func() { _ = s.Close() })
	a := &Account{Name: "metadata", SupplementalModels: []string{"rich", "manual"}}
	if err := s.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	// Write an actual old-format cache rather than producing it via the new type.
	const legacy = `[{"id":"legacy","name":"Legacy","description":"old cache"}]`
	if _, err := s.ExecContext(ctx, `INSERT INTO account_model_catalog(account_id,models_json,fetched_at,attempted_at,error) VALUES(?,?,?,?,?)`, a.ID, legacy, 10, 10, ""); err != nil {
		t.Fatal(err)
	}
	old, err := s.GetAccountModelCatalog(ctx, a.ID)
	if err != nil || old == nil || len(old.Models) != 3 || old.Models[0].ID != "legacy" || old.Models[0].Metadata != nil {
		t.Fatalf("legacy cache incompatible: %+v err=%v", old, err)
	}
	metadata := map[string]json.RawMessage{
		"id":                                   json.RawMessage(`" rich "`),
		"supported_reasoning_levels":           json.RawMessage(`[]`),
		"default_reasoning_level":              json.RawMessage(`null`),
		"supports_reasoning_summary_parameter": json.RawMessage(`false`),
		"context_window":                       json.RawMessage(`9007199254740993123456789`),
		"input_modalities":                     json.RawMessage(`["text","image"]`),
		"unknown":                              json.RawMessage(`{"nested":[null,false,[],{"large":18446744073709551617}]}`),
	}
	model := CatalogModel{ID: "rich", Name: "Rich", Metadata: metadata}
	if err := s.SaveAccountModelCatalog(ctx, a.ID, &ModelCatalog{Models: []CatalogModel{model}, FetchedAt: 20, AttemptedAt: 20}); err != nil {
		t.Fatal(err)
	}
	for _, failed := range []bool{false, true} {
		if failed {
			if err := s.SaveAccountModelCatalog(ctx, a.ID, &ModelCatalog{AttemptedAt: 30, Error: "HTTP 403"}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.GetAccountModelCatalog(ctx, a.ID)
		if err != nil || got == nil || len(got.Models) != 2 {
			t.Fatalf("metadata snapshot=%+v err=%v", got, err)
		}
		if !reflect.DeepEqual(got.Models[0].Metadata, metadata) || got.Models[0].Source != "upstream" || got.Models[1].Source != "manual" || got.FetchedAt != 20 {
			t.Fatalf("merge or persistence changed upstream metadata: %+v", got)
		}
		// A model also manually listed must not inherit any manual defaults.
		if _, exists := got.Models[0].Metadata["default_reasoning_summary"]; exists {
			t.Fatal("manual defaults contaminated upstream metadata")
		}
		if !failed {
			if err := s.SaveAccountModelCatalog(ctx, a.ID, got); err != nil {
				t.Fatal(err)
			}
		}
	}
	var persisted string
	if err := s.QueryRowContext(ctx, `SELECT models_json FROM account_model_catalog WHERE account_id=?`, a.ID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	var models []CatalogModel
	if err := json.Unmarshal([]byte(persisted), &models); err != nil || len(models) != 1 || models[0].Source != "" || !reflect.DeepEqual(models[0].Metadata, metadata) {
		t.Fatalf("persisted metadata/manual isolation: %s err=%v", persisted, err)
	}
}
