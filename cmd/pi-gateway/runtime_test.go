package main

import (
	"context"
	"errors"
	"flag"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"pi-gateway/internal/config"
	"pi-gateway/internal/session"
)

func TestHealthcheckURL(t *testing.T) {
	for host, want := range map[string]string{
		"":          "http://127.0.0.1:8317/healthz",
		"0.0.0.0":   "http://127.0.0.1:8317/healthz",
		"127.0.0.1": "http://127.0.0.1:8317/healthz",
		"::":        "http://[::1]:8317/healthz",
		"[::]":      "http://[::1]:8317/healthz",
		"::1":       "http://[::1]:8317/healthz",
		"localhost": "http://localhost:8317/healthz",
	} {
		got, err := healthcheckURL(host, 8317)
		if err != nil || got != want {
			t.Errorf("host=%q got=%q err=%v want=%q", host, got, err, want)
		}
	}
	for _, port := range []int{-1, 0, 65536} {
		if _, err := healthcheckURL("localhost", port); err == nil {
			t.Errorf("accepted port %d", port)
		}
	}
	if _, err := healthcheckURL("user:secret@host", 8317); err == nil {
		t.Fatal("accepted URL credentials in host")
	}
}

func TestCheckHealthStatusAndNoRedirect(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNoContent, http.StatusFound, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/healthz" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(status)
			}))
			defer srv.Close()
			addr := srv.Listener.Addr().(*net.TCPAddr)
			err := checkHealth(context.Background(), addr.IP.String(), addr.Port)
			if (err == nil) != (status >= 200 && status < 300) {
				t.Fatalf("status %d: %v", status, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("health probe followed redirect: %d requests", calls.Load())
			}
		})
	}
}

func TestCheckHealthRespectsContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	addr := srv.Listener.Addr().(*net.TCPAddr)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := checkHealth(ctx, addr.IP.String(), addr.Port); err == nil {
		t.Fatal("timed out probe succeeded")
	}
	if time.Since(started) > time.Second {
		t.Fatal("probe exceeded caller deadline")
	}
}

func TestHealthcheckRunDoesNotOpenDatabase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	addr := srv.Listener.Addr().(*net.TCPAddr)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Unreachable PostgreSQL would fail immediately if healthcheck opened it.
	if err := os.WriteFile(path, []byte("data:\n  driver: postgres\n  dsn: postgres://localhost:1/unreachable?connect_timeout=1\nserver:\n  port: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_GATEWAY_DATABASE_DRIVER", "postgres")
	t.Setenv("PI_GATEWAY_DATABASE_DSN", "postgres://localhost:1/unreachable?connect_timeout=1")
	t.Setenv("PI_GATEWAY_REDIS_ENABLED", "false")
	t.Setenv("PI_GATEWAY_REDIS_URL", "")
	oldArgs, oldFlags := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args = oldArgs; flag.CommandLine = oldFlags })
	os.Args = []string{"pi-gateway", "--config", path, "--healthcheck", "--host", addr.IP.String(), "--port", strconv.Itoa(addr.Port)}
	flag.CommandLine = flag.NewFlagSet("healthcheck-test", flag.ContinueOnError)
	if err := run(); err != nil {
		t.Fatalf("healthcheck must use CLI endpoint without opening DB: %v", err)
	}
}

func TestVersionDoesNotLoadInvalidConfig(t *testing.T) {
	oldArgs, oldFlags := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args = oldArgs; flag.CommandLine = oldFlags })
	t.Setenv("PI_GATEWAY_REDIS_ENABLED", "invalid-secret")
	os.Args = []string{"pi-gateway", "--version"}
	flag.CommandLine = flag.NewFlagSet("version-test", flag.ContinueOnError)
	if err := run(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionStoreMemoryLimitsAndClose(t *testing.T) {
	cfg := config.Default()
	cfg.Redis.MaxEntries = 1
	st, err := newSessionStore(cfg, "deployment-a")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, ok := st.(*session.Memory); !ok {
		t.Fatal("default backend is not memory")
	}
	record := session.Record{ResponseID: "response-1", KeyID: 1, AccountID: 1, SessionID: "session", InstanceID: "instance", ConnectionID: "connection", IdentityHash: "identity", Provider: "openai", Scope: session.ScopeConnectionLocal}
	if err := st.RecordCompleted(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	record.ResponseID = "response-2"
	if err := st.RecordCompleted(context.Background(), record); !errors.Is(err, session.ErrUnavailable) {
		t.Fatalf("memory entry cap ignored: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Resolve(context.Background(), 1, "response-1"); !errors.Is(err, session.ErrUnavailable) {
		t.Fatalf("closed store usable: %v", err)
	}
}

func TestSessionStoreUnavailableRedisDoesNotFallBack(t *testing.T) {
	cfg := config.Default()
	cfg.Redis.Enabled = true
	cfg.Redis.URL = "redis://127.0.0.1:1/0"
	cfg.Redis.CommitTimeoutMS = 20
	st, err := newSessionStore(cfg, "deployment-a")
	if err != nil {
		t.Fatalf("optional unavailable Redis blocked startup: %v", err)
	}
	defer st.Close()
	if _, ok := st.(*session.Redis); !ok {
		t.Fatal("unavailable Redis was replaced with memory")
	}
	if _, err := st.Resolve(context.Background(), 1, "unknown-response"); !errors.Is(err, session.ErrUnavailable) {
		t.Fatalf("unavailability became a miss: %v", err)
	}
}

func TestSessionStoreConfigurationFailsClosed(t *testing.T) {
	cfg := config.Default()
	cfg.Redis.Enabled = true
	cfg.Redis.URL = "redis://user:secret@host/invalid-db"
	if st, err := newSessionStore(cfg, "deployment-a"); err == nil || st != nil {
		t.Fatal("invalid Redis config accepted")
	}
	cfg = config.Default()
	if st, err := newSessionStore(cfg, ""); err == nil || st != nil {
		t.Fatal("missing deployment identity accepted")
	}
}
