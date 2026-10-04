package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func checkDeploymentIdentity(t *testing.T, opts Options) string {
	t.Helper()
	first, err := OpenOptions(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenOptions(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	const workers = 24
	ids := make(chan string, workers)
	errors := make(chan error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			st := first
			if i%2 != 0 {
				st = second
			}
			id, err := st.DeploymentID(context.Background())
			if err != nil {
				errors <- err
				return
			}
			ids <- id
		}(i)
	}
	close(start)
	wg.Wait()
	close(ids)
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	var want string
	count := 0
	for id := range ids {
		count++
		if !deploymentIDPattern.MatchString(id) {
			t.Errorf("invalid UUID: %q", id)
		}
		if want == "" {
			want = id
		}
		if id != want {
			t.Errorf("concurrent UUID changed: %q != %q", id, want)
		}
	}
	if count != workers {
		t.Fatalf("only %d successful identities", count)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenOptions(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.DeploymentID(context.Background())
	if err != nil || got != want {
		t.Fatalf("restart identity = %q, %v; want %q", got, err, want)
	}
	return want
}

func TestDeploymentIDConcurrentAndRestartStable(t *testing.T) {
	one := checkDeploymentIdentity(t, Options{Path: filepath.Join(t.TempDir(), "one.db")})
	two := checkDeploymentIdentity(t, Options{Path: filepath.Join(t.TempDir(), "two.db")})
	if one == two {
		t.Fatal("independent databases share a deployment identity")
	}
}

func TestDeploymentIDRejectsCorruptionAndCancellation(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.DeploymentID(ctx); err == nil {
		t.Fatal("canceled request succeeded")
	}
	if _, err := st.ExecContext(context.Background(), `INSERT INTO settings(key,value,updated_at) VALUES(?,?,?)`, deploymentIDKey, "invalid-secret-value", NowMS()); err != nil {
		t.Fatal(err)
	}
	if id, err := st.DeploymentID(context.Background()); err == nil || id != "" || err.Error() != "store: invalid persisted deployment identity" {
		t.Fatalf("corrupt identity was accepted or exposed: %q %v", id, err)
	}
	var value string
	if err := st.QueryRowContext(context.Background(), `SELECT value FROM settings WHERE key=?`, deploymentIDKey).Scan(&value); err != nil || value != "invalid-secret-value" {
		t.Fatal("invalid identity was silently rotated")
	}
}

func TestDeploymentIDPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("PI_GATEWAY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("PI_GATEWAY_TEST_POSTGRES_DSN is not set")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL test DSN")
	}
	admin := stdlib.OpenDB(*cfg)
	defer admin.Close()
	schema := "deployment_test_" + GenerateKey()[6:]
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Error(err)
		}
	}()
	cfg.RuntimeParams["search_path"] = schema
	registered := stdlib.RegisterConnConfig(cfg)
	defer stdlib.UnregisterConnConfig(registered)
	checkDeploymentIdentity(t, Options{Driver: "postgres", DSN: registered})
}
