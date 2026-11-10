package modelpricing

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"pi-gateway/internal/egress"
	"pi-gateway/internal/pricing"
	"pi-gateway/internal/store"
)

func testService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s, err := New(context.Background(), st, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return s, st
}
func testPrice(id, input string) pricing.ModelPrice {
	return pricing.ModelPrice{Model: id, ContextMode: "same", Standard: &pricing.DecimalRates{Input: input, CachedInput: "0.5", CacheWrite: "3", Output: "4"}}
}
func requirePrice(t *testing.T, snap *pricing.Snapshot, id string, want float64) {
	t.Helper()
	r, _, ok := snap.Lookup(id, "standard", nil)
	if !ok || r.Input != want {
		t.Fatalf("%s input=%v priced=%v want=%v", id, r.Input, ok, want)
	}
}
func testDocument(models ...pricing.ModelPrice) StandardPriceFile {
	return StandardPriceFile{1, "USD", "per_million_tokens", models}
}
func TestManualCASPersistenceImmutabilityAndBlockedReset(t *testing.T) {
	ctx := context.Background()
	s, st := testService(t)
	old := s.Snapshot()
	p := testPrice("vendor/precise-model", "1.125")
	p.Aliases = []string{"vendor/alias"}
	rev, err := s.SaveManual(ctx, 0, p, "tester")
	if err != nil || rev != 1 {
		t.Fatalf("save: %d %v", rev, err)
	}
	requirePrice(t, s.Snapshot(), p.Model, 1.125)
	if _, _, ok := old.Lookup(p.Model, "standard", nil); ok {
		t.Fatal("old snapshot changed")
	}
	p.Standard.Input = "999"
	p.Aliases[0] = "corrupted"
	if _, err = s.SaveManual(ctx, 0, testPrice(p.Model, "8"), "tester"); !errors.Is(err, ErrConflict) {
		t.Fatalf("CAS: %v", err)
	}
	rev, err = s.ChangeModel(ctx, rev, p.Model, "block", "", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.Snapshot().Lookup("vendor/precise-model", "standard", nil); ok {
		t.Fatal("block fell back")
	}
	rev, err = s.ChangeModel(ctx, rev, p.Model, "reset", "", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.Snapshot().Lookup(p.Model, "standard", nil); ok {
		t.Fatal("reset retained manual")
	}
	rev, err = s.SaveManual(ctx, rev, testPrice("vendor/second", "2"), "tester")
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(ctx, st, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Revision() != rev {
		t.Fatal("revision not persisted")
	}
	requirePrice(t, reloaded.Snapshot(), "vendor/second", 2)
	audit, err := st.ListAudit(ctx, 20)
	if err != nil || len(audit) != 4 {
		t.Fatalf("audit count=%d err=%v", len(audit), err)
	}
}
func TestTransactionFailureDoesNotPublishAndCASAcrossServices(t *testing.T) {
	ctx := context.Background()
	s, st := testService(t)
	other, err := New(ctx, st, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveManual(ctx, 0, testPrice("a", "1"), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err = other.SaveManual(ctx, 0, testPrice("b", "2"), "b"); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-service CAS: %v", err)
	}
	if other.Revision() != 1 {
		t.Fatal("conflict did not refresh persisted snapshot")
	}
	requirePrice(t, other.Snapshot(), "a", 1)
	before := s.Snapshot()
	_, err = st.ExecContext(ctx, `CREATE TRIGGER reject_pricing BEFORE INSERT ON pricing_models BEGIN SELECT RAISE(ABORT,'fixture failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveManual(ctx, 1, testPrice("x", "2"), "a"); err == nil {
		t.Fatal("expected transaction failure")
	}
	if s.Revision() != 1 || s.Snapshot() != before {
		t.Fatal("failed commit published")
	}
	records, err := st.LoadPricing(ctx)
	if err != nil || records.Revision != 1 {
		t.Fatalf("rollback revision=%d err=%v", records.Revision, err)
	}
}
func TestAliasesManualOwnershipAndImportStaleness(t *testing.T) {
	ctx := context.Background()
	s, _ := testService(t)
	p := testPrice("manual-model", "9")
	p.Aliases = []string{"gpt-5", "private-alias"}
	rev, err := s.SaveManual(ctx, 0, p, "actor")
	if err != nil {
		t.Fatal(err)
	}
	requirePrice(t, s.Snapshot(), "gpt-5", 9)
	source := Source{ID: "fixture", URL: "https://example.com/prices.json", Format: "standard"}
	rev, err = s.SaveSource(ctx, rev, source)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	candidate := makeRun(s.state, source, &fetched{Models: []pricing.ModelPrice{testPrice("private-alias", "2")}, Warnings: map[string][]string{}})
	s.mu.Unlock()
	if !candidate.Items[0].Locked {
		t.Fatal("external canonical stole a manual alias")
	}
	document := testDocument(testPrice("import/model", "2.5"))
	rev, run, err := s.Import(ctx, rev, document)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.Snapshot().Lookup("import/model", "standard", nil); ok {
		t.Fatal("preview changed prices")
	}
	document.Models[0].Standard.Input = "88"
	rev, err = s.Apply(ctx, rev, run.ID, []string{"import/model"}, "actor")
	if err != nil {
		t.Fatal(err)
	}
	requirePrice(t, s.Snapshot(), "import/model", 2.5)
	rev, run, err = s.Import(ctx, rev, testDocument(testPrice("future", "6")))
	if err != nil {
		t.Fatal(err)
	}
	rev, err = s.SaveManual(ctx, rev, testPrice("other", "1"), "actor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(ctx, rev, run.ID, []string{"future"}, "actor"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale run applied: %v", err)
	}
}
func TestHistoryRestoreAndRetention(t *testing.T) {
	ctx := context.Background()
	s, st := testService(t)
	rev, err := s.SaveManual(ctx, 0, testPrice("history", "1"), "actor")
	if err != nil {
		t.Fatal(err)
	}
	first := s.History()[0]
	rev, err = s.SaveManual(ctx, rev, testPrice("history", "2"), "actor")
	if err != nil {
		t.Fatal(err)
	}
	rev, err = s.Restore(ctx, rev, first.ID, "actor")
	if err != nil {
		t.Fatal(err)
	}
	requirePrice(t, s.Snapshot(), "history", 1)
	for i := 0; i < HistoryLimit+2; i++ {
		rev, err = s.SaveManual(ctx, rev, testPrice("history", fmt.Sprint(i)), "actor")
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(s.History()) != HistoryLimit {
		t.Fatalf("history unbounded: %d", len(s.History()))
	}
	records, err := st.LoadPricing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records.Versions) != VersionLimit {
		t.Fatalf("versions unbounded: %d", len(records.Versions))
	}
}
func fixtureSource(t *testing.T, handler http.HandlerFunc) (*Service, Source) {
	t.Helper()
	s, _ := testService(t)
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	s.factory = egress.NewFactory(egress.Options{RootCAs: roots})
	source := Source{ID: "fixture", URL: server.URL + "/prices", Format: "standard", Enabled: true, TrustedHosts: []string{"127.0.0.1"}, IntervalHours: 24}
	if _, err := s.SaveSource(context.Background(), 0, source); err != nil {
		t.Fatal(err)
	}
	return s, source
}
func TestHTTPSFixtureSyncConditionalFailureAndFollow(t *testing.T) {
	var mu sync.Mutex
	body := string(encode(testDocument(testPrice("followed", "2"))))
	failure := false
	sawConditional := false
	s, source := fixtureSource(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credentials leaked")
		}
		if failure {
			http.Error(w, "failure", 503)
			return
		}
		if r.Header.Get("If-None-Match") == "fixture-v1" {
			sawConditional = true
			w.WriteHeader(304)
			return
		}
		w.Header().Set("ETag", "fixture-v1")
		w.Write([]byte(body))
	})
	ctx := context.Background()
	rev, run, err := s.Sync(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "pending" || run.BaseRevision != rev || len(run.Items) != 1 {
		t.Fatalf("bad run: %+v", run)
	}
	rev, err = s.Apply(ctx, rev, run.ID, []string{"followed"}, "actor")
	if err != nil {
		t.Fatal(err)
	}
	requirePrice(t, s.Snapshot(), "followed", 2)
	rev, err = s.SaveManual(ctx, rev, testPrice("unrelated", "1"), "actor")
	if err != nil {
		t.Fatal(err)
	}
	rev, run, err = s.Sync(ctx, source.ID)
	if err != nil || run.Status != "pending" {
		t.Fatalf("conditional %v %+v", err, run)
	}
	rev, err = s.Apply(ctx, rev, run.ID, []string{"followed"}, "actor")
	if err != nil {
		t.Fatalf("304 candidate unusable: %v", err)
	}
	mu.Lock()
	if !sawConditional {
		t.Error("conditional header absent")
	}
	failure = true
	mu.Unlock()
	_, failed, err := s.Sync(ctx, source.ID)
	if !errors.Is(err, ErrExternal) || failed.Status != "failed" {
		t.Fatalf("failed sync: %+v %v", failed, err)
	}
	requirePrice(t, s.Snapshot(), "followed", 2)
	sources := s.Sources()
	if sources[0].LastError == "" || sources[0].NextAttemptAt == 0 {
		t.Fatal("failure backoff/status absent")
	}
	rev = s.Revision()
	rev, err = s.SaveManual(ctx, rev, testPrice("followed", "9"), "actor")
	if err != nil {
		t.Fatal(err)
	}
	rev, err = s.ChangeModel(ctx, rev, "followed", "reset", "", "actor")
	if err != nil {
		t.Fatal(err)
	}
	requirePrice(t, s.Snapshot(), "followed", 2)
}
func TestPublicNetworkGuardAndRedirectValidation(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.1.1.1", "169.254.169.254", "::1", "fc00::1", "100.64.0.1", "192.0.2.1"} {
		if publicIP(net.ParseIP(ip)) {
			t.Errorf("nonpublic address accepted: %s", ip)
		}
	}
	if !publicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public address rejected")
	}
	s, source := fixtureSource(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "https://localhost/secret", 302) })
	if _, err := s.TestSource(context.Background(), source.ID); !errors.Is(err, ErrExternal) {
		t.Fatalf("redirect trusted-host escape: %v", err)
	}
	source.TrustedHosts = nil
	if _, err := s.fetch(context.Background(), source, false); err == nil {
		t.Fatal("private target accepted without explicit trust")
	}
	source.ProxyID = 1
	if _, err := validateURL(source, "https://example.com"); err == nil {
		t.Fatal("proxy DNS validation falsely trusted")
	}
	for _, raw := range []string{"http://example.com", "https://user:secret@example.com", "file:///tmp/a", "https://example.com/#fragment"} {
		if _, err := validateURL(Source{}, raw); err == nil {
			t.Errorf("unsafe URL: %s", raw)
		}
	}
}
func TestModelsDevUsesOnlyOpenAIPreservesUnknownAndWarns(t *testing.T) {
	raw := []byte(`{"anthropic":{"models":{"unwanted":{"cost":{"input":1,"output":2}}}},"openai":{"models":{"vendor/model":{"name":"Fixture","cost":{"input":0.25,"output":2.5,"tiers":[{}]}}}}}`)
	result, err := parseModelsDev(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Models) != 1 || result.Models[0].Model != "vendor/model" {
		t.Fatal("provider filtering")
	}
	p := result.Models[0]
	if p.Standard.CachedInput != "" || p.Standard.CacheWrite != "" {
		t.Fatal("missing costs became free")
	}
	if p.Standard.Input != "0.25" || p.Standard.Output != "2.5" {
		t.Fatalf("wrong units: %+v", p.Standard)
	}
	warnings := strings.Join(result.Warnings[p.Model], " ")
	if !strings.Contains(warnings, "tiers") || !strings.Contains(warnings, "unknown") {
		t.Fatalf("missing warnings: %s", warnings)
	}
	if _, err = parseModelsDev([]byte(`{"openai":{"models":{"bad":{"cost":{"input":-1}}}}}`)); err == nil {
		t.Fatal("negative price accepted")
	}
	encoded, _ := json.Marshal(testDocument(p))
	var document StandardPriceFile
	if err = json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	if _, err = parseStandard(document); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentCASOnlyOneWinner(t *testing.T) {
	s, _ := testService(t)
	var wg sync.WaitGroup
	wins := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.SaveManual(context.Background(), 0, testPrice(fmt.Sprint(i), "1"), "actor")
			wins <- err == nil
		}(i)
	}
	wg.Wait()
	close(wins)
	count := 0
	for win := range wins {
		if win {
			count++
		}
	}
	if count != 1 || s.Revision() != 1 {
		t.Fatalf("CAS winners=%d revision=%d", count, s.Revision())
	}
}
