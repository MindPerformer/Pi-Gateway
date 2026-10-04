package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clearStorageEnv(t *testing.T) {
	t.Helper()
	for _, pair := range os.Environ() {
		name, _, _ := strings.Cut(pair, "=")
		if strings.HasPrefix(name, "PI_GATEWAY_REDIS_") || strings.HasPrefix(name, "PI_GATEWAY_DATABASE") || strings.HasPrefix(name, "PI_GATEWAY_UPSTREAM_WEBSOCKET_") || name == "PI_GATEWAY_PORT" {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestStorageDefaults(t *testing.T) {
	clearStorageEnv(t)
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	want := RedisConfig{Prefix: "pi-gateway:", SessionTTLSeconds: 14400, CommitTimeoutMS: 100, MaxEntries: 10000, MaxRecordBytes: 65536, MaxBytes: 67108864, CatalogTTLSeconds: 30}
	if cfg.Redis != want {
		t.Fatalf("unexpected Redis defaults: %+v", cfg.Redis)
	}
	if cfg.Data.MaxOpenConns != 8 || cfg.Data.MaxIdleConns != 8 || cfg.Data.ConnMaxLifetimeSeconds != 0 {
		t.Fatalf("unexpected pool defaults: %+v", cfg.Data)
	}
	if cfg.Upstream.WebsocketMaxBaselineBytes != 2<<20 || cfg.Upstream.WebsocketMaxTotalBaselineBytes != 64<<20 || cfg.Upstream.WebsocketMaxPoolConnections != 256 {
		t.Fatal("wrong websocket limits")
	}
}

func TestStorageEnvironmentOverrides(t *testing.T) {
	clearStorageEnv(t)
	values := map[string]string{
		"PI_GATEWAY_REDIS_ENABLED": "true", "PI_GATEWAY_REDIS_TLS": "true",
		"PI_GATEWAY_REDIS_URL": "rediss://cache:6379/2", "PI_GATEWAY_REDIS_PASSWORD": "secret", "PI_GATEWAY_REDIS_PREFIX": "test:",
		"PI_GATEWAY_REDIS_SESSION_TTL_SECONDS": "100", "PI_GATEWAY_REDIS_COMMIT_TIMEOUT_MS": "20",
		"PI_GATEWAY_REDIS_MAX_ENTRIES": "7", "PI_GATEWAY_REDIS_MAX_RECORD_BYTES": "2048", "PI_GATEWAY_REDIS_MAX_BYTES": "4096", "PI_GATEWAY_REDIS_CATALOG_TTL_SECONDS": "12",
		"PI_GATEWAY_DATABASE_MAX_OPEN_CONNS": "4", "PI_GATEWAY_DATABASE_MAX_IDLE_CONNS": "2", "PI_GATEWAY_DATABASE_CONN_MAX_LIFETIME_SECONDS": "90",
		"PI_GATEWAY_UPSTREAM_WEBSOCKET_MAX_BASELINE_BYTES": "1024", "PI_GATEWAY_UPSTREAM_WEBSOCKET_MAX_TOTAL_BASELINE_BYTES": "4096", "PI_GATEWAY_UPSTREAM_WEBSOCKET_MAX_POOL_CONNECTIONS": "3",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	want := RedisConfig{Enabled: true, TLS: true, URL: "rediss://cache:6379/2", Password: "secret", Prefix: "test:", SessionTTLSeconds: 100, CommitTimeoutMS: 20, MaxEntries: 7, MaxRecordBytes: 2048, MaxBytes: 4096, CatalogTTLSeconds: 12}
	if cfg.Redis != want {
		t.Fatal("Redis overrides not applied")
	}
	if cfg.Data.MaxOpenConns != 4 || cfg.Data.MaxIdleConns != 2 || cfg.Data.ConnMaxLifetimeSeconds != 90 {
		t.Fatal("pool overrides not applied")
	}
	if cfg.Upstream.WebsocketMaxBaselineBytes != 1024 || cfg.Upstream.WebsocketMaxTotalBaselineBytes != 4096 || cfg.Upstream.WebsocketMaxPoolConnections != 3 {
		t.Fatal("websocket overrides not applied")
	}
}

func TestStorageEnvironmentErrorsAreRedacted(t *testing.T) {
	for _, name := range []string{
		"PI_GATEWAY_REDIS_ENABLED", "PI_GATEWAY_REDIS_TLS", "PI_GATEWAY_PORT",
		"PI_GATEWAY_REDIS_SESSION_TTL_SECONDS", "PI_GATEWAY_REDIS_COMMIT_TIMEOUT_MS", "PI_GATEWAY_REDIS_MAX_ENTRIES", "PI_GATEWAY_REDIS_MAX_RECORD_BYTES", "PI_GATEWAY_REDIS_MAX_BYTES", "PI_GATEWAY_REDIS_CATALOG_TTL_SECONDS",
		"PI_GATEWAY_DATABASE_MAX_OPEN_CONNS", "PI_GATEWAY_DATABASE_MAX_IDLE_CONNS", "PI_GATEWAY_DATABASE_CONN_MAX_LIFETIME_SECONDS",
		"PI_GATEWAY_UPSTREAM_WEBSOCKET_MAX_BASELINE_BYTES", "PI_GATEWAY_UPSTREAM_WEBSOCKET_MAX_TOTAL_BASELINE_BYTES", "PI_GATEWAY_UPSTREAM_WEBSOCKET_MAX_POOL_CONNECTIONS",
	} {
		t.Run(name, func(t *testing.T) {
			clearStorageEnv(t)
			for _, invalid := range []string{"secret-value", ""} {
				t.Setenv(name, invalid)
				_, err := Load("")
				if err == nil || !strings.Contains(err.Error(), name) || strings.Contains(err.Error(), "secret-value") {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestStorageYAMLAndValidationErrorsAreRedacted(t *testing.T) {
	for _, raw := range []string{
		"redis:\n  enabled: secret-value\n",
		"redis:\n  max_entries: secret-value\n",
		"data:\n  dsn: [secret-value]\n",
		"redis:\n  enabled: true\n  url: 'redis://user:secret-value@host/not-a-db'\n",
		"redis:\n  url: 'https://user:secret-value@host'\n",
		"redis:\n  enabled: true\n",
		"redis:\n  session_ttl_seconds: 0\n",
		"redis:\n  commit_timeout_ms: -1\n",
		"redis:\n  max_entries: 0\n",
		"redis:\n  max_record_bytes: 65537\n",
		"redis:\n  max_bytes: 1\n",
		"redis:\n  catalog_ttl_seconds: 0\n",
		"data:\n  max_open_conns: -1\n",
		"data:\n  max_idle_conns: -1\n",
		"data:\n  conn_max_lifetime_seconds: 9223372036854775807\n",
		"upstream:\n  websocket_max_pool_connections: 0\n",
		"upstream:\n  websocket_max_total_baseline_bytes: 1\n",
	} {
		t.Run(raw, func(t *testing.T) {
			clearStorageEnv(t)
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("unexpected config error: %v", err)
			}
		})
	}
}

func TestStorageYAMLAndSecretEnvClearing(t *testing.T) {
	clearStorageEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("redis:\n  enabled: true\n  url: redis://cache:6379/0\n  password: yaml-secret\n  prefix: 'yaml:'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_GATEWAY_REDIS_PASSWORD", "")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Redis.Password != "" || cfg.Redis.Prefix != "yaml:" || !cfg.Redis.Enabled {
		t.Fatal("YAML/env precedence incorrect")
	}
}
