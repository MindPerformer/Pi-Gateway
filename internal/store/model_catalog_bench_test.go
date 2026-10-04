package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func benchmarkAccountModelFixture(b *testing.B, n int) (*Store, []int64, *APIKey) {
	b.Helper()
	s, err := Open(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		a := &Account{Name: fmt.Sprintf("bench-%d", i), Enabled: true, SupplementalModels: []string{"manual"}, DisabledModels: []string{"account-blocked"}}
		if err := s.CreateAccount(ctx, a); err != nil {
			b.Fatal(err)
		}
		ids = append(ids, a.ID)
		if err := s.SaveAccountModelCatalog(ctx, a.ID, &ModelCatalog{
			Models: []CatalogModel{{ID: "upstream", Name: "Upstream", Metadata: map[string]json.RawMessage{
				"context_window": json.RawMessage(`9007199254740993123456789`),
				"capabilities":   json.RawMessage(`{"images":true,"tools":false,"unknown":[null,[]]}`),
			}}}, FetchedAt: 100, AttemptedAt: 100,
		}); err != nil {
			b.Fatal(err)
		}
	}
	group := &AccountGroup{Name: "bench-membership", Enabled: true, AccountIDs: ids, DisabledModels: []string{"group-blocked", "shared"}}
	if err := s.CreateAccountGroup(ctx, group); err != nil {
		b.Fatal(err)
	}
	keyGroup := &AccountGroup{Name: "bench-key", Enabled: true, DisabledModels: []string{"key-blocked", "shared"}}
	if err := s.CreateAccountGroup(ctx, keyGroup); err != nil {
		b.Fatal(err)
	}
	key := &APIKey{Name: "bench-key", GroupIDs: []int64{keyGroup.ID}}
	if err := s.CreateKey(ctx, key); err != nil {
		b.Fatal(err)
	}
	return s, ids, key
}

// SQL-reads/op counts SELECT/WITH statements. The formula is verified against
// the real-driver observer in TestAccountModelBatchReadCountAndDialectContract.
func BenchmarkAccountModelReads(b *testing.B) {
	ctx := context.Background()
	for _, n := range []int{1, 20, 100} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			s, ids, key := benchmarkAccountModelFixture(b, n)
			b.Run("catalog/individual", func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64(n), "SQL-reads/op")
				for i := 0; i < b.N; i++ {
					for _, id := range ids {
						if _, err := s.GetAccountModelCatalog(ctx, id); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
			b.Run("catalog/batch", func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64((n+accountModelBatchSize-1)/accountModelBatchSize), "SQL-reads/op")
				for i := 0; i < b.N; i++ {
					if _, err := s.GetAccountModelCatalogs(ctx, ids); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("restrictions/individual", func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64(n), "SQL-reads/op")
				for i := 0; i < b.N; i++ {
					for _, id := range ids {
						if _, err := s.DisabledModelsForAccount(ctx, id, key); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
			b.Run("restrictions/batch", func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64((n+accountModelBatchSize-1)/accountModelBatchSize), "SQL-reads/op")
				for i := 0; i < b.N; i++ {
					if _, err := s.DisabledModelsForAccounts(ctx, ids, key); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
