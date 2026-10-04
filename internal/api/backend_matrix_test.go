package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"

	"pi-gateway/internal/session"
	"pi-gateway/internal/store"
)

// This is the CI entry point for the real service matrix. Supplying either
// service opts into the entire matrix: partial configuration and broken service
// protocols are errors, never skips or a fallback to in-process substitutes.
func TestBackendMatrixIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PI_GATEWAY_TEST_POSTGRES_DSN"))
	redisURL := strings.TrimSpace(os.Getenv("PI_GATEWAY_TEST_REDIS_URL"))
	if dsn == "" && redisURL == "" {
		t.Skip("real backend matrix requires PI_GATEWAY_TEST_POSTGRES_DSN and PI_GATEWAY_TEST_REDIS_URL")
	}
	if dsn == "" || redisURL == "" {
		t.Fatal("both PI_GATEWAY_TEST_POSTGRES_DSN and PI_GATEWAY_TEST_REDIS_URL must be configured")
	}
	for _, tc := range []struct {
		name, database string
		redis          bool
	}{
		{"sqlite-memory", "SQLite", false},
		{"sqlite-redis", "SQLite", true},
		{"postgres-memory", "PostgreSQL", false},
		{"postgres-redis", "PostgreSQL", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := matrixDatabase(t, tc.database, dsn)
			url := ""
			if tc.redis {
				url = redisURL
			}
			matrixContract(t, opts, url)
		})
	}
}

// Local feedback exercises the exact same contract, but intentionally has a
// distinct name: miniredis is not evidence of a real Redis integration run.
func TestBackendMatrixLocalContract(t *testing.T) {
	for _, cache := range []string{"memory", "miniredis"} {
		t.Run("SQLite+"+cache, func(t *testing.T) {
			url := ""
			if cache == "miniredis" {
				url = "redis://" + miniredis.RunT(t).Addr() + "/0"
			}
			matrixContract(t, matrixDatabase(t, "SQLite", ""), url)
		})
	}
}

func matrixNonce(t *testing.T) string {
	t.Helper()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(nonce[:])
}

func matrixDatabase(t *testing.T, database, dsn string) store.Options {
	t.Helper()
	if database == "SQLite" {
		dir := t.TempDir()
		// SQLite WAL handles may remain delete-pending briefly on Windows.
		t.Cleanup(func() {
			var err error
			for attempt := 0; attempt < 50; attempt++ {
				if err = os.RemoveAll(dir); err == nil {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Errorf("remove matrix SQLite directory: %v", err)
		})
		return store.Options{Driver: "sqlite", Path: filepath.Join(dir, "matrix.db")}
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL integration DSN")
	}
	cfg.ConnectTimeout = 5 * time.Second
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = make(map[string]string)
	}
	cfg.RuntimeParams["statement_timeout"] = "10000"
	cfg.RuntimeParams["lock_timeout"] = "5000"
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() {
		if err := admin.Close(); err != nil {
			t.Errorf("close PostgreSQL schema connection: %v", err)
		}
	})
	schema := "api_matrix_" + matrixNonce(t)
	quoted := pgx.Identifier{schema}.Sanitize()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatalf("create private PostgreSQL schema: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Never clean caller-owned tables or another schema, even on failure.
		if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("drop private PostgreSQL schema: %v", err)
		}
	})
	cfg.RuntimeParams["search_path"] = schema
	registered := stdlib.RegisterConnConfig(cfg)
	t.Cleanup(func() { stdlib.UnregisterConnConfig(registered) })
	return store.Options{Driver: "postgres", DSN: registered}
}

type matrixSession interface {
	session.Store
	Close() error
}

func matrixSessionFactory(t *testing.T, redisURL, deploymentID string) func() matrixSession {
	t.Helper()
	namespace := "pi-gateway:api-matrix:" + deploymentID + ":" + matrixNonce(t)
	opts := session.Options{Namespace: namespace, TTL: 2 * time.Minute, CommitTimeout: 3 * time.Second}
	if redisURL != "" {
		parsed, err := redis.ParseURL(redisURL)
		if err != nil {
			t.Fatal("invalid Redis integration URL")
		}
		parsed.DialTimeout = 3 * time.Second
		parsed.ReadTimeout = 3 * time.Second
		parsed.WriteTimeout = 3 * time.Second
		parsed.PoolTimeout = 3 * time.Second
		parsed.ContextTimeoutEnabled = true
		parsed.MaxRetries = -1
		// The cleanup client is separate from the owned session clients, which
		// are deliberately closed/reopened to test persistence across clients.
		cleanup := redis.NewClient(parsed)
		t.Cleanup(func() {
			defer func() {
				if err := cleanup.Close(); err != nil {
					t.Errorf("close Redis cleanup client: %v", err)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			prefix := namespace + ":"
			var cursor uint64
			var keys []string
			for {
				batch, next, err := cleanup.Scan(ctx, cursor, prefix+"*", 100).Result()
				if err != nil {
					t.Errorf("scan private Redis namespace: %v", err)
					return
				}
				for _, key := range batch {
					if !strings.HasPrefix(key, prefix) {
						t.Error("Redis cleanup escaped private namespace")
						return
					}
					keys = append(keys, key)
				}
				cursor = next
				if cursor == 0 {
					break
				}
			}
			if len(keys) != 0 {
				if err := cleanup.Del(ctx, keys...).Err(); err != nil {
					t.Errorf("delete private Redis keys: %v", err)
				}
			}
		})
	}
	return func() matrixSession {
		var sessions matrixSession
		if redisURL == "" {
			sessions = session.NewMemory(opts)
		} else {
			var err error
			sessions, err = session.NewRedis(session.RedisOptions{URL: redisURL, Options: opts})
			if err != nil {
				t.Fatalf("open Redis session store: %v", err)
			}
		}
		t.Cleanup(func() {
			if err := sessions.Close(); err != nil {
				t.Errorf("close matrix session store: %v", err)
			}
		})
		return sessions
	}
}

type matrixRows struct {
	keyID, accountID, groupID int64
	captureID                 int64
	deletedKeyID              int64
	deletedAccountID          int64
	deletedGroupID            int64
}

func matrixContract(t *testing.T, opts store.Options, redisURL string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenOptions(opts) // A fresh database/schema runs real migrations.
	if err != nil {
		t.Fatalf("initial migration: %v", err)
	}
	t.Cleanup(func() {
		if st != nil {
			if err := st.Close(); err != nil {
				t.Errorf("close matrix SQL store: %v", err)
			}
		}
	})
	deploymentID, err := st.DeploymentID(ctx)
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(deploymentID) {
		t.Fatalf("persist deployment UUID: %q %v", deploymentID, err)
	}
	var migrationVersion int64
	if opts.Driver == "postgres" {
		var schema string
		if err := st.DB().QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil || !strings.HasPrefix(schema, "api_matrix_") {
			t.Fatalf("PostgreSQL escaped private search_path: %q %v", schema, err)
		}
		var count int
		if err := st.QueryRowContext(ctx, "SELECT MAX(version),COUNT(*) FROM schema_migrations").Scan(&migrationVersion, &count); err != nil || migrationVersion <= 0 || count != 1 {
			t.Fatalf("initial PostgreSQL migration: version=%d count=%d error=%v", migrationVersion, count, err)
		}
	}
	reopen := func() {
		t.Helper()
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		st, err = store.OpenOptions(opts)
		if err != nil {
			t.Fatalf("reopen/migrate store: %v", err)
		}
		id, err := st.DeploymentID(ctx)
		if err != nil || id != deploymentID {
			t.Fatalf("deployment UUID changed across Open: %q %v", id, err)
		}
		if opts.Driver == "postgres" {
			var version int64
			var count int
			if err := st.QueryRowContext(ctx, "SELECT MAX(version),COUNT(*) FROM schema_migrations").Scan(&version, &count); err != nil || version != migrationVersion || count != 1 {
				t.Fatalf("migration was not idempotent: version=%d count=%d error=%v", version, count, err)
			}
		}
	}
	reopen()
	openSessions := matrixSessionFactory(t, redisURL, deploymentID)
	sessions := openSessions()
	var rows matrixRows
	if !t.Run("CRUD-HTTP-aggregation-SSE-ledger-capture", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, deltaSSE)
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, usageTerminalSSE)
		})
		h := newHarnessWithStore(t, handler, "sse", st, sessions)
		t.Cleanup(h.dataPlane.Close)
		rows = matrixCRUD(t, h)
		for _, stream := range []bool{false, true} {
			body := map[string]any{"model": "gpt-5.5", "input": []any{gatewayUser("matrix HTTP")}, "stream": stream}
			status, headers, raw := matrixHTTP(t, h, h.key, body)
			if status != http.StatusOK || matrixHTTPResponseID(t, headers, raw, stream) != "resp_usage_1" {
				t.Fatalf("HTTP stream=%t protocol response: %d %s", stream, status, raw)
			}
		}
		rows.captureID = matrixLedgerAndCapture(t, h, rows)
	}) {
		return
	}
	if !rowsValid(rows) {
		t.Fatal("matrix CRUD fixture did not finish")
	}
	var stale session.Record
	if !t.Run("WS-two-turns-reconnect-HTTP-bridge-current-policy", func(t *testing.T) {
		stale = matrixWSContract(t, st, sessions, rows)
	}) {
		return
	}
	// Restore genuine completed metadata after orderly shutdown to model the
	// stale cache left by a crash. This must NOT restore a live upstream socket.
	stale.Version = 0
	if err := sessions.RecordCompleted(ctx, stale); err != nil {
		t.Fatalf("persist stale crash metadata: %v", err)
	}
	if err := sessions.Close(); err != nil {
		t.Fatal(err)
	}
	reopen()
	restarted := openSessions()
	if redisURL != "" {
		got, err := restarted.Resolve(ctx, stale.KeyID, stale.ResponseID)
		if err != nil || got.ConnectionID != stale.ConnectionID || got.InstanceID != stale.InstanceID {
			t.Fatalf("Redis record not retained across owned client reopen: %+v %v", got, err)
		}
	} else {
		if _, err := restarted.Resolve(ctx, stale.KeyID, stale.ResponseID); !errors.Is(err, session.ErrMiss) {
			t.Fatalf("new memory store retained old process state: %v", err)
		}
		// Even if stale metadata is restored, memory is not a socket authority.
		if err := restarted.RecordCompleted(ctx, stale); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("reopen-data-retained-dead-connection-never-revived", func(t *testing.T) {
		matrixAssertRows(t, st, rows)
		b := newGatewayContinuationBackend()
		h := newHarnessWithStore(t, b, "websocket-cached", st, restarted)
		t.Cleanup(h.dataPlane.Close)
		for _, stream := range []bool{false, true} {
			body := gatewayCreate([]any{gatewayUser("cannot resurrect")}, stale.ResponseID, "same-client-session")
			body["stream"] = stream
			status, _, raw := matrixHTTP(t, h, h.key, body)
			if status != http.StatusBadRequest || !bytes.Contains(raw, []byte("previous_response_not_found")) {
				t.Fatalf("old connection accepted over HTTP: %d %s", status, raw)
			}
		}
		conn := dialGatewayContinuation(t, h, h.key)
		if code := gatewayErrorCode(t, gatewayExchange(t, conn, gatewayCreate([]any{gatewayUser("cannot resurrect")}, stale.ResponseID, "same-client-session"))); code != "previous_response_not_found" {
			t.Fatalf("old connection accepted over WS: %s", code)
		}
		if b.generations.Load() != 0 || b.connections.Load() != 0 || b.httpCalls.Load() != 0 {
			t.Fatal("restarted gateway replayed a dead connection-local record")
		}
		// Keep fake response IDs globally unique across the two gateway instances;
		// this mirrors provider-issued IDs and avoids idempotent cache collisions.
		b.generations.Add(100)
		id := gatewayResponseID(t, gatewayExchange(t, conn, gatewayCreate([]any{gatewayUser("fresh after restart")}, "", "same-client-session")))
		gatewayObservedTurn(t, b)
		fresh := matrixResolve(t, restarted, rows.keyID, id)
		if fresh.InstanceID == stale.InstanceID || fresh.ConnectionID == stale.ConnectionID {
			t.Fatal("new chain reused old instance/socket ownership")
		}
	})
}

func rowsValid(rows matrixRows) bool {
	return rows.keyID > 0 && rows.accountID > 0 && rows.groupID > 0 && rows.captureID > 0
}

func matrixCRUD(t *testing.T, h *testHarness) matrixRows {
	t.Helper()
	ctx := context.Background()
	key, err := h.store.GetKeyByValue(ctx, h.key)
	if err != nil || key == nil {
		t.Fatalf("read API key: %+v %v", key, err)
	}
	group := &store.AccountGroup{Name: "matrix-group", Enabled: true, AccountIDs: []int64{h.accountID}}
	if err := h.store.CreateAccountGroup(ctx, group); err != nil {
		t.Fatal(err)
	}
	group.Name, group.Notes = "matrix-group-updated", "retained after reopen"
	group.DisabledModels = []string{"blocked-model"}
	if err := h.store.UpdateAccountGroup(ctx, group); err != nil {
		t.Fatal(err)
	}
	key.Name, key.Label, key.GroupIDs = "matrix-key-updated", "matrix", []int64{group.ID}
	if err := h.store.UpdateKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	name := "matrix-account-updated"
	if err := h.store.PatchAccountManagementFields(ctx, h.accountID, store.AccountManagementPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	account := &store.Account{Name: "disposable", Enabled: false, Weight: 1, Concurrency: 1}
	if err := h.store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	account.Name = "disposable-updated"
	if err := h.store.UpdateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if got, err := h.store.GetAccount(ctx, account.ID); err != nil || got == nil || got.Name != account.Name {
		t.Fatalf("account CRUD read: %+v %v", got, err)
	}
	otherGroup := &store.AccountGroup{Name: "disposable-group", Enabled: true, AccountIDs: []int64{account.ID}}
	if err := h.store.CreateAccountGroup(ctx, otherGroup); err != nil {
		t.Fatal(err)
	}
	otherKey := &store.APIKey{Name: "disposable-key", Enabled: true, GroupIDs: []int64{otherGroup.ID}}
	if err := h.store.CreateKey(ctx, otherKey); err != nil {
		t.Fatal(err)
	}
	if keys, err := h.store.ListKeys(ctx); err != nil || len(keys) != 2 {
		t.Fatalf("list keys: count=%d err=%v", len(keys), err)
	}
	if accounts, err := h.store.ListAccounts(ctx); err != nil || len(accounts) != 2 {
		t.Fatalf("list accounts: count=%d err=%v", len(accounts), err)
	}
	if groups, err := h.store.ListAccountGroups(ctx); err != nil || len(groups) != 2 {
		t.Fatalf("list groups: count=%d err=%v", len(groups), err)
	}
	if err := h.store.DeleteKey(ctx, otherKey.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.store.DeleteAccountGroup(ctx, otherGroup.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.store.DeleteAccount(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	rows := matrixRows{keyID: key.ID, accountID: h.accountID, groupID: group.ID, deletedKeyID: otherKey.ID, deletedAccountID: account.ID, deletedGroupID: otherGroup.ID}
	matrixAssertRows(t, h.store, rows)
	return rows
}

func matrixAssertRows(t *testing.T, st *store.Store, rows matrixRows) {
	t.Helper()
	ctx := context.Background()
	key, err := st.GetKey(ctx, rows.keyID)
	if err != nil || key == nil || key.Name != "matrix-key-updated" || key.Label != "matrix" || !key.Enabled || !reflect.DeepEqual(key.GroupIDs, []int64{rows.groupID}) {
		t.Fatalf("retained API key: %+v %v", key, err)
	}
	account, err := st.GetAccount(ctx, rows.accountID)
	if err != nil || account == nil || account.Name != "matrix-account-updated" || !account.Enabled {
		t.Fatalf("retained account: %+v %v", account, err)
	}
	group, err := st.GetAccountGroup(ctx, rows.groupID)
	if err != nil || group == nil || group.Name != "matrix-group-updated" || group.Notes != "retained after reopen" || !group.Enabled || !reflect.DeepEqual(group.AccountIDs, []int64{rows.accountID}) || !reflect.DeepEqual(group.DisabledModels, []string{"blocked-model"}) {
		t.Fatalf("retained group: %+v %v", group, err)
	}
	if got, err := st.GetKey(ctx, rows.deletedKeyID); err != nil || got != nil {
		t.Fatalf("deleted key returned: %+v %v", got, err)
	}
	if got, err := st.GetAccount(ctx, rows.deletedAccountID); err != nil || got != nil {
		t.Fatalf("deleted account returned: %+v %v", got, err)
	}
	if got, err := st.GetAccountGroup(ctx, rows.deletedGroupID); err != nil || got != nil {
		t.Fatalf("deleted group returned: %+v %v", got, err)
	}
	if rows.captureID > 0 {
		capture, err := st.GetCapture(ctx, rows.captureID)
		if err != nil || capture == nil || capture.ResponseID != "resp_usage_1" || capture.PromptTokens != 100 || capture.CompletionTokens != 20 || len(capture.ResponseFrames) == 0 || capture.RequestBody == "" {
			t.Fatalf("retained capture payload: %+v %v", capture, err)
		}
		records, total, err := st.ListUsageRecords(ctx, store.UsageFilter{Model: "gpt-5.5", Outcome: "succeeded", Limit: 10})
		if err != nil || total != 2 || len(records) != 2 {
			t.Fatalf("retained HTTP usage ledger: total=%d rows=%d err=%v", total, len(records), err)
		}
	}
}

func matrixHTTP(t *testing.T, h *testHarness, key string, body map[string]any) (int, http.Header, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, h.server.URL+"/v1/responses", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Session_id", "same-client-session")
	client := &http.Client{Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Clone(), payload
}

func matrixHTTPResponseID(t *testing.T, headers http.Header, raw []byte, stream bool) string {
	t.Helper()
	if !stream {
		var response struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if !strings.Contains(headers.Get("Content-Type"), "application/json") || json.Unmarshal(raw, &response) != nil || response.Status != "completed" || response.ID == "" {
			t.Fatalf("invalid aggregated HTTP response: %s", raw)
		}
		return response.ID
	}
	if !strings.Contains(headers.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("SSE Content-Type = %q", headers.Get("Content-Type"))
	}
	var id string
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	for _, frame := range strings.Split(text, "\n\n") {
		var lines []string
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "data:") {
				lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		data := strings.TrimSpace(strings.Join(lines, "\n"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatalf("invalid SSE JSON event: %v", err)
		}
		if event["type"] == "response.completed" {
			id = gatewayResponseID(t, event)
		}
	}
	if id == "" {
		t.Fatalf("SSE has no completed response: %s", raw)
	}
	return id
}

func matrixLedgerAndCapture(t *testing.T, h *testHarness, rows matrixRows) int64 {
	t.Helper()
	var records []store.UsageRecord
	var captures []*store.Capture
	gatewayAwait(t, func() bool {
		var err error
		records, _, err = h.store.ListUsageRecords(context.Background(), store.UsageFilter{Model: "gpt-5.5", Outcome: "succeeded", Limit: 10})
		if err != nil {
			t.Fatalf("read protocol usage ledger: %v", err)
		}
		captures, _, err = h.store.ListCaptures(context.Background(), store.CaptureFilter{Limit: 10, IncludePayloads: true})
		if err != nil {
			t.Fatalf("read protocol captures: %v", err)
		}
		return len(records) == 2 && len(captures) == 2
	}, "HTTP handlers did not persist both ledger/capture rows")
	for _, rec := range records {
		if rec.APIKeyID != rows.keyID || rec.AccountID != rows.accountID || rec.APIKeyName != "matrix-key-updated" || rec.AccountName != "matrix-account-updated" || rec.InputTokens == nil || *rec.InputTokens != 100 || rec.OutputTokens == nil || *rec.OutputTokens != 20 || rec.TotalTokens == nil || *rec.TotalTokens != 120 || rec.CachedTokens == nil || *rec.CachedTokens != 80 || rec.CostMicros == nil || *rec.CostMicros <= 0 {
			t.Fatalf("protocol ledger attribution/usage/cost: %+v", rec)
		}
	}
	for _, capture := range captures {
		if capture.AccountID != rows.accountID || capture.Outcome != store.OutcomeOK || capture.Status != http.StatusOK || capture.ResponseID != "resp_usage_1" || capture.TotalTokens != 120 || capture.RequestBody == "" {
			t.Fatalf("protocol capture: %+v", capture)
		}
		terminal := false
		for _, frame := range capture.ResponseFrames {
			terminal = terminal || frame.Type == "response.completed"
		}
		if !terminal {
			t.Fatal("capture lost upstream terminal event")
		}
		for _, header := range capture.RequestHeaders {
			if strings.EqualFold(header.Name, "Authorization") && strings.Contains(header.Value, "Bearer "+h.key) {
				t.Fatal("capture persisted unmasked API key")
			}
		}
	}
	return captures[0].ID
}

func matrixResolve(t *testing.T, sessions session.Store, keyID int64, responseID string) *session.Record {
	t.Helper()
	var record *session.Record
	gatewayAwait(t, func() bool {
		var err error
		record, err = sessions.Resolve(context.Background(), keyID, responseID)
		if errors.Is(err, session.ErrMiss) {
			return false
		}
		if err != nil {
			t.Fatalf("read actual continuation backend: %v", err)
		}
		return record != nil
	}, "completed protocol response was not committed to continuation backend")
	if record.KeyID != keyID || record.Scope != session.ScopeConnectionLocal || record.ConnectionID == "" || record.InstanceID == "" {
		t.Fatalf("invalid connection-local metadata: %+v", record)
	}
	return record
}

func matrixWSContract(t *testing.T, st *store.Store, sessions matrixSession, rows matrixRows) session.Record {
	t.Helper()
	ctx := context.Background()
	b := newGatewayContinuationBackend()
	h := newHarnessWithStore(t, b, "websocket-cached", st, sessions)
	// Register after the harness so downstream cancellation/wait precedes SQL,
	// upstream and session closure, including a fatal assertion mid-protocol.
	t.Cleanup(h.dataPlane.Close)
	conn := dialGatewayContinuation(t, h, h.key)
	first := gatewayResponseID(t, gatewayExchange(t, conn, gatewayCreate([]any{gatewayUser("one")}, "", "same-client-session")))
	firstTurn := gatewayObservedTurn(t, b)
	matrixResolve(t, sessions, rows.keyID, first)
	second := gatewayResponseID(t, gatewayExchange(t, conn, gatewayCreate([]any{gatewayUser("two")}, first, "same-client-session")))
	secondTurn := gatewayObservedTurn(t, b)
	if first == second || secondTurn.connection != firstTurn.connection || secondTurn.body["previous_response_id"] != first {
		t.Fatal("two WS turns did not use the same upstream continuation")
	}
	matrixResolve(t, sessions, rows.keyID, second)
	foreign := &store.APIKey{Name: "foreign-key", Enabled: true, GroupIDs: []int64{rows.groupID}}
	if err := st.CreateKey(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	other := dialGatewayContinuation(t, h, foreign.Key)
	if code := gatewayErrorCode(t, gatewayExchange(t, other, gatewayCreate([]any{gatewayUser("steal")}, second, "same-client-session"))); code != "previous_response_not_found" {
		t.Fatalf("cross-key same-session WS request: %s", code)
	}
	if _, err := sessions.Resolve(ctx, foreign.ID, second); !errors.Is(err, session.ErrWrongOwner) {
		t.Fatalf("session backend failed owner binding: %v", err)
	}
	foreignBody := gatewayCreate([]any{gatewayUser("steal")}, second, "same-client-session")
	status, _, raw := matrixHTTP(t, h, foreign.Key, foreignBody)
	if status != http.StatusBadRequest || !bytes.Contains(raw, []byte("previous_response_not_found")) || b.generations.Load() != 2 {
		t.Fatalf("cross-key same-session HTTP request reached upstream: %d %s", status, raw)
	}
	_ = other.Close()
	_ = conn.Close()
	gatewayAwait(t, func() bool {
		h.dataPlane.lifecycle.mu.Lock()
		defer h.dataPlane.lifecycle.mu.Unlock()
		return len(h.dataPlane.lifecycle.active) == 0
	}, "disconnected WS handlers did not finish")
	reconnected := dialGatewayContinuation(t, h, h.key)
	previous := gatewayResponseID(t, gatewayExchange(t, reconnected, gatewayCreate([]any{gatewayUser("reconnect delta")}, second, "same-client-session")))
	resumed := gatewayObservedTurn(t, b)
	if resumed.connection != firstTurn.connection || resumed.body["previous_response_id"] != second || len(resumed.body["input"].([]any)) != 1 {
		t.Fatalf("explicit reconnect lost original socket/delta: %+v", resumed)
	}
	for _, stream := range []bool{false, true} {
		body := gatewayCreate([]any{gatewayUser("WS to HTTP")}, previous, "same-client-session")
		body["stream"] = stream
		status, headers, raw := matrixHTTP(t, h, h.key, body)
		if status != http.StatusOK {
			t.Fatalf("WS to HTTP continuation: %d %s", status, raw)
		}
		turn := gatewayObservedTurn(t, b)
		if turn.connection != firstTurn.connection || turn.body["previous_response_id"] != previous {
			t.Fatal("HTTP bridge did not keep original WS continuation")
		}
		previous = matrixHTTPResponseID(t, headers, raw, stream)
		matrixResolve(t, sessions, rows.keyID, previous)
	}
	last := gatewayResponseID(t, gatewayExchange(t, reconnected, gatewayCreate([]any{gatewayUser("HTTP to WS")}, previous, "same-client-session")))
	turn := gatewayObservedTurn(t, b)
	if turn.connection != firstTurn.connection || turn.body["previous_response_id"] != previous || b.connections.Load() != 1 || b.httpCalls.Load() != 0 {
		t.Fatal("HTTP to WS bridge opened a replacement or HTTP fallback")
	}
	matrixResolve(t, sessions, rows.keyID, last)
	_ = reconnected.Close()
	matrixCurrentPolicy(t, h, b, sessions, rows)
	// A fresh, completed chain supplies authentic ownership metadata for the
	// restart test, independent from chains invalidated by policy rejection.
	final := dialGatewayContinuation(t, h, h.key)
	id := gatewayResponseID(t, gatewayExchange(t, final, gatewayCreate([]any{gatewayUser("before restart")}, "", "same-client-session")))
	gatewayObservedTurn(t, b)
	return *matrixResolve(t, sessions, rows.keyID, id)
}

func matrixCurrentPolicy(t *testing.T, h *testHarness, b *gatewayContinuationBackend, sessions session.Store, rows matrixRows) {
	t.Helper()
	ctx := context.Background()
	for _, policy := range []string{"disabled-key", "disabled-account", "disabled-group", "group-model", "account-model"} {
		conn := dialGatewayContinuation(t, h, h.key)
		id := gatewayResponseID(t, gatewayExchange(t, conn, gatewayCreate([]any{gatewayUser("before " + policy)}, "", "")))
		gatewayObservedTurn(t, b)
		matrixResolve(t, sessions, rows.keyID, id)
		key, err := h.store.GetKey(ctx, rows.keyID)
		if err != nil || key == nil {
			t.Fatalf("policy key read: %v", err)
		}
		group, err := h.store.GetAccountGroup(ctx, rows.groupID)
		if err != nil || group == nil {
			t.Fatalf("policy group read: %v", err)
		}
		no, yes := false, true
		var restore func() error
		switch policy {
		case "disabled-key":
			key.Enabled = false
			err = h.store.UpdateKey(ctx, key)
			restore = func() error { key.Enabled = true; return h.store.UpdateKey(ctx, key) }
		case "disabled-account":
			err = h.store.PatchAccountManagementFields(ctx, rows.accountID, store.AccountManagementPatch{Enabled: &no})
			restore = func() error {
				return h.store.PatchAccountManagementFields(ctx, rows.accountID, store.AccountManagementPatch{Enabled: &yes})
			}
		case "disabled-group":
			group.Enabled = false
			err = h.store.UpdateAccountGroup(ctx, group)
			restore = func() error { group.Enabled = true; return h.store.UpdateAccountGroup(ctx, group) }
		case "group-model":
			group.DisabledModels = []string{"blocked-model", "test-model"}
			err = h.store.UpdateAccountGroup(ctx, group)
			restore = func() error {
				group.DisabledModels = []string{"blocked-model"}
				return h.store.UpdateAccountGroup(ctx, group)
			}
		case "account-model":
			err = h.store.PatchAccountManagementFields(ctx, rows.accountID, store.AccountManagementPatch{DisabledModels: []string{"test-model"}})
			restore = func() error {
				return h.store.PatchAccountManagementFields(ctx, rows.accountID, store.AccountManagementPatch{DisabledModels: []string{}})
			}
		}
		if err != nil {
			t.Fatalf("change current %s: %v", policy, err)
		}
		before := b.generations.Load()
		body := gatewayCreate([]any{gatewayUser("must reject changed policy")}, id, "")
		event := gatewayExchange(t, conn, body)
		gatewayErrorCode(t, event) // Authentication errors need not have a code.
		problem := event["error"].(map[string]any)
		if problem["message"] == nil || problem["type"] == nil {
			t.Fatalf("%s returned no structured WS rejection: %v", policy, event)
		}
		delete(body, "previous_response_id")
		status, _, raw := matrixHTTP(t, h, h.key, body)
		wantStatus := http.StatusServiceUnavailable
		if policy == "disabled-key" {
			wantStatus = http.StatusUnauthorized
		} else if strings.Contains(policy, "model") {
			wantStatus = http.StatusForbidden
			if !bytes.Contains(raw, []byte("model_disabled")) {
				t.Fatalf("current %s did not return model_disabled: %s", policy, raw)
			}
		}
		if status != wantStatus || b.generations.Load() != before {
			t.Fatalf("current %s bypassed HTTP/WS authorization: status=%d want=%d body=%s", policy, status, wantStatus, raw)
		}
		if err := restore(); err != nil {
			t.Fatalf("restore %s: %v", policy, err)
		}
		_ = conn.Close()
		gatewayAwait(t, func() bool { return h.dataPlane.accounts.Inflight(rows.accountID) == 0 }, "policy rejection leaked account slot")
	}
}
