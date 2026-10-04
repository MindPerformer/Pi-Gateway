package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"pi-gateway/internal/accounts"
	"pi-gateway/internal/config"
	"pi-gateway/internal/egress"
	"pi-gateway/internal/oauth"
	"pi-gateway/internal/session"
	"pi-gateway/internal/settings"
	"pi-gateway/internal/store"
	"pi-gateway/internal/upstream"
)

// upstreamCapture records what the fake backend received.
type upstreamCapture struct {
	mu      sync.Mutex
	headers http.Header
	body    []byte
	// wsFrame records the WebSocket response.create envelope when used.
	wsFrame []byte
	// method/path of the last request.
	method string
	path   string
}

func (c *upstreamCapture) set(r *http.Request, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.headers = r.Header.Clone()
	c.body = body
	c.method = r.Method
	c.path = r.URL.Path
}

func (c *upstreamCapture) setWS(frame []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wsFrame = frame
}

func (c *upstreamCapture) get() (http.Header, []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.headers, c.body
}

func (c *upstreamCapture) getWS() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.wsFrame...)
}

// terminalResponse is a minimal but realistic terminal event.
const terminalSSE = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":11,\"output_tokens\":7,\"total_tokens\":18,\"output_tokens_details\":{\"reasoning_tokens\":3}}}}\n\n"

const deltaSSE = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"

// sseUpstream is a fake ChatGPT Codex backend speaking SSE.
func sseUpstream(cap *upstreamCapture) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cap.set(r, body)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "req-from-upstream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte(deltaSSE))
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = w.Write([]byte(terminalSSE))
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// wsUpstream is a fake backend that upgrades to a WebSocket and answers each
// response.create frame with the same event sequence.
func wsUpstream(t *testing.T, cap *upstreamCapture) http.HandlerFunc {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return func(w http.ResponseWriter, r *http.Request) {
		cap.set(r, nil)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("fake upstream upgrade failed: %v", err)
			return
		}
		defer conn.Close()
		_, frame, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("fake upstream read failed: %v", err)
			return
		}
		cap.setWS(frame)

		for _, payload := range []string{
			`{"type":"response.created","response":{"id":"resp_ws_1"}}`,
			`{"type":"response.output_text.delta","delta":"hello"}`,
			`{"type":"response.completed","response":{"id":"resp_ws_1","status":"completed","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`,
		} {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
				return
			}
		}
	}
}

// queuedWSUpstream blocks until the test has observed every expected rejection.
func queuedWSUpstream(t *testing.T, started chan<- struct{}, release <-chan struct{}) http.HandlerFunc {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var first sync.Once
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("queued fake upstream upgrade failed: %v", err)
			return
		}
		defer conn.Close()
		var request struct {
			ID string `json:"prompt_cache_key"`
		}
		if err := conn.ReadJSON(&request); err != nil {
			t.Errorf("read queued request: %v", err)
			return
		}
		first.Do(func() { close(started) })
		<-release
		for _, eventType := range []string{"response.created", "response.completed"} {
			payload := map[string]any{
				"type":     eventType,
				"response": map[string]any{"id": request.ID, "status": "completed"},
			}
			if err := conn.WriteJSON(payload); err != nil {
				return
			}
		}
	}
}

// testHarness wires a Server against a fake backend.
type testHarness struct {
	server     *httptest.Server
	dataPlane  *Server
	store      *store.Store
	key        string
	accountID  int64
	upstream   *httptest.Server
	capture    *upstreamCapture
	baseConfig *config.Config
}

func newHarness(t *testing.T, upstreamHandler http.Handler, transport string) *testHarness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// Cleanups run LIFO, so the store is closed before the private SQLite
	// directory is removed. This retry is needed on Windows for WAL handles.
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
	t.Cleanup(func() { _ = st.Close() })
	return newHarnessWithStore(t, upstreamHandler, transport, st, nil)
}

// newHarnessWithStore keeps the API wiring identical while allowing integration
// tests to inject an independently opened SQLite/PostgreSQL store and session
// backend. Reopened stores reuse the named fixture without overwriting it. The
// caller owns the store/session lifecycle.
func newHarnessWithStore(t *testing.T, upstreamHandler http.Handler, transport string, st *store.Store, sessions session.Store) *testHarness {
	t.Helper()
	ctx := context.Background()
	if st == nil {
		t.Fatal("nil test store")
	}

	upstreamSrv := httptest.NewServer(upstreamHandler)
	t.Cleanup(upstreamSrv.Close)

	account, err := st.FindAccountByAccountID(ctx, "acct-123")
	if err != nil {
		t.Fatalf("read fixture account: %v", err)
	}
	if account == nil {
		// The fake JWT permits account-id extraction, without contacting OAuth.
		account = &store.Account{
			Name: "test-account", AccountID: "acct-123",
			AccessToken: fakeJWT(t, "acct-123"), RefreshToken: "refresh-token",
			ExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli(),
			Enabled:   true, Weight: 1, Concurrency: 3, Status: store.AccountStatusReady,
		}
		if err := st.CreateAccount(ctx, account); err != nil {
			t.Fatalf("create account: %v", err)
		}
	}

	key, err := st.GetKeyByValue(ctx, "sk-pi-test")
	if err != nil {
		t.Fatalf("read fixture key: %v", err)
	}
	if key == nil {
		// No groups: this key may use every account.
		key = &store.APIKey{Name: "test-key", Key: "sk-pi-test", Enabled: true}
		if err := st.CreateKey(ctx, key); err != nil {
			t.Fatalf("create key: %v", err)
		}
	}

	cfg := config.Default()
	cfg.Upstream.BaseURL = upstreamSrv.URL + "/backend-api"
	cfg.Upstream.Transport = transport
	// Keep the wire assertion focused on headers/body rather than compression.
	cfg.Upstream.SSEZstd = false
	cfg.Logging.Level = "error"

	factory := egress.NewFactory(egress.Options{ConnectTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second})
	oauthClient := oauth.NewClient(nil)
	affinity, _ := sessions.(session.AffinityStore)
	catalogBackend, _ := sessions.(session.CatalogCache)
	accountsMgr := accounts.New(st, oauthClient, factory, accounts.Options{
		Logger: discardLogger(), Affinity: affinity,
		AffinityIdentity: session.Fingerprint("gateway-upstream", cfg.UpstreamSSEURL()+"\x00"+cfg.UpstreamWSURL()+"\x00"+cfg.Upstream.Originator),
		AffinityTimeout:  3 * time.Second,
	})
	upstreamCfg := upstream.Config{
		SSEURL:     cfg.UpstreamSSEURL(),
		WSURL:      cfg.UpstreamWSURL(),
		Originator: "pi",
		Zstd:       false,
		Pool:       true,
		Sessions:   sessions,
		Logger:     discardLogger(),
	}
	if sessions != nil {
		// Real service CI can take longer than the production best-effort cache
		// deadline; still bound the test and require every completed write.
		upstreamCfg.SessionCommitTimeout = 3 * time.Second
	}
	upstreamClient := upstream.New(upstreamCfg, factory)
	t.Cleanup(upstreamClient.Close)

	holder := settings.New(st, cfg)
	if err := holder.Load(ctx); err != nil {
		t.Fatalf("load settings: %v", err)
	}

	dataPlane := New(Options{
		Config:         cfg,
		Store:          st,
		Accounts:       accountsMgr,
		Upstream:       upstreamClient,
		Settings:       holder,
		Logger:         discardLogger(),
		CatalogCache:   catalogBackend,
		CatalogTTL:     30 * time.Second,
		CatalogTimeout: 3 * time.Second,
	})

	mux := http.NewServeMux()
	dataPlane.Routes(mux)
	var handlers sync.WaitGroup
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		srv.Close()
		// httptest.Server does not wait for hijacked WebSocket handlers. They
		// must finish persisting captures before the store/session owner closes.
		handlers.Wait()
	})

	return &testHarness{
		server:     srv,
		dataPlane:  dataPlane,
		store:      st,
		key:        key.Key,
		accountID:  account.ID,
		upstream:   upstreamSrv,
		capture:    &upstreamCapture{},
		baseConfig: cfg,
	}
}

// fakeJWT builds an unsigned JWT carrying the chatgpt_account_id claim.
func fakeJWT(t *testing.T, accountID string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"` + accountID + `"},"exp":4102444800}`))
	return header + "." + payload + ".sig"
}

// discardLogger returns a logger that swallows output during tests.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// ---- tests ----

// TestSSERequestReproducesPiWireShape is the core fidelity test: it asserts the
// gateway sends exactly what Pi sends.
func TestSSERequestReproducesPiWireShape(t *testing.T) {
	cap := &upstreamCapture{}
	h := newHarness(t, sseUpstream(cap), "passthrough")

	body := `{
		"model": "gpt-5.1-codex",
		"instructions": "You are Pi.",
		"input": [{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"stream": true,
		"prompt_cache_key": "session-abc",
		"reasoning": {"effort":"medium","summary":"auto"},
		"metadata": {"should":"be dropped"},
		"max_output_tokens": 999
	}`

	req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+h.key)
	req.Header.Set("Content-Type", "application/json")
	// Client headers that must NOT leak upstream.
	req.Header.Set("User-Agent", "evil-client/9.9")
	req.Header.Set("originator", "evil")
	req.Header.Set("x-client-secret", "leak-me")
	req.Header.Set("chatgpt-account-id", "attacker-account")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, raw)
	}
	_, _ = io.ReadAll(resp.Body)

	headers, sentBody := cap.get()

	// ---- header assertions (Pi's exact set on the Sign in with ChatGPT route) ----
	// The ChatGPT route sends no originator/OpenAI-Beta/chatgpt-account-id header.
	for _, absent := range []string{"originator", "OpenAI-Beta", "chatgpt-account-id"} {
		if got := headers.Get(absent); got != "" {
			t.Errorf("%s = %q, want it absent on the ChatGPT route", absent, got)
		}
	}
	if got := headers.Get("User-Agent"); !strings.HasPrefix(got, "pi (") {
		t.Errorf("User-Agent = %q, want a pi (...) value", got)
	}
	if got := headers.Get("accept"); got != "application/json" {
		t.Errorf("accept = %q", got)
	}
	if got := headers.Get("content-type"); got != "application/json" {
		t.Errorf("content-type = %q", got)
	}
	// The SDK's platform fingerprint headers must reach the wire, with the
	// defaults from the example config.
	for name, want := range map[string]string{
		"X-Stainless-Lang":            "js",
		"X-Stainless-Package-Version": "7.19.0",
		"X-Stainless-OS":              "Linux",
		"X-Stainless-Arch":            "x64",
		"X-Stainless-Runtime":         "node",
		"X-Stainless-Runtime-Version": "v24.21.0",
		"X-Stainless-Retry-Count":     "0",
	} {
		if got := headers.Get(name); got != want {
			t.Errorf("%s = %q, want %q on the wire", name, got, want)
		}
	}
	if got := headers.Get("X-Stainless-Timeout"); got != "" {
		t.Errorf("X-Stainless-Timeout = %q, want it absent without an explicit timeout", got)
	}
	if got := headers.Get("session_id"); got != "session-abc" {
		t.Errorf("session_id = %q, want the client's prompt_cache_key", got)
	}
	if got := headers.Get("x-client-request-id"); got != "session-abc" {
		t.Errorf("x-client-request-id = %q", got)
	}
	if !strings.HasPrefix(headers.Get("Authorization"), "Bearer ") {
		t.Errorf("Authorization missing bearer token")
	}

	// ---- no client header may leak ----
	for _, leaked := range []string{"x-client-secret", "evil", "attacker-account"} {
		for name, values := range headers {
			for _, v := range values {
				if strings.Contains(strings.ToLower(v), leaked) || strings.EqualFold(name, leaked) {
					t.Errorf("client header leaked upstream: %s: %s", name, v)
				}
			}
		}
	}
	if got := headers.Get("User-Agent"); strings.Contains(got, "evil-client") {
		t.Errorf("client User-Agent leaked: %q", got)
	}

	// ---- body assertions ----
	var sent map[string]any
	if err := json.Unmarshal(sentBody, &sent); err != nil {
		t.Fatalf("upstream body is not JSON: %v", err)
	}
	if sent["store"] != false {
		t.Errorf("store = %v, want false", sent["store"])
	}
	if sent["stream"] != true {
		t.Errorf("stream = %v, want true", sent["stream"])
	}
	include, ok := sent["include"].([]any)
	if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v, want [reasoning.encrypted_content]", sent["include"])
	}
	reasoning, _ := sent["reasoning"].(map[string]any)
	if reasoning["effort"] != "medium" {
		t.Errorf("reasoning.effort = %v, want medium", reasoning["effort"])
	}
	// Sign in with ChatGPT rejects these, and Pi never sends them on this route.
	for _, dropped := range []string{"metadata", "max_output_tokens", "temperature", "instructions", "text", "parallel_tool_calls", "tool_choice"} {
		if _, exists := sent[dropped]; exists {
			t.Errorf("%s was forwarded upstream; it must be dropped", dropped)
		}
	}

	// Pi serialises in a fixed key order; verify the prefix matches byte for byte.
	prefix := `{"model":"gpt-5.1-codex","input":`
	if !strings.HasPrefix(string(sentBody), prefix) {
		t.Errorf("request body key order differs from Pi:\n got: %s\nwant prefix: %s", string(sentBody), prefix)
	}
	if !strings.Contains(string(sentBody), `"stream":true,"prompt_cache_key":"session-abc","store":false`) {
		t.Errorf("request body is not in Pi's field order: %s", string(sentBody))
	}
}

func multilineSSEUpstream(cap *upstreamCapture) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cap.set(r, body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		multiline := `{
  "type": "response.output_text.delta",
  "delta": "hello"
}`
		_, _ = w.Write([]byte("data: " + strings.ReplaceAll(multiline, "\n", "\ndata: ") + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = w.Write([]byte(terminalSSE))
	}
}

func TestSSEMultilineJSONRemainsParseable(t *testing.T) {
	cap := &upstreamCapture{}
	h := newHarness(t, multilineSSEUpstream(cap), "passthrough")
	req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-5.1-codex","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer "+h.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, raw)
	}

	reader := upstream.ParseSSE(strings.NewReader(string(raw)))
	event, err := reader.Next()
	if err != nil {
		t.Fatalf("parse relayed SSE: %v; body=%s", err, raw)
	}
	if event.Type != "response.output_text.delta" || event.Data["delta"] != "hello" {
		t.Fatalf("event = %#v, want equivalent multiline JSON event", event.Data)
	}
}

// TestCaptureRecordsHeadersAndFrames verifies the analysis payload.
func TestCaptureRecordsHeadersAndFrames(t *testing.T) {
	cap := &upstreamCapture{}
	h := newHarness(t, sseUpstream(cap), "passthrough")

	req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/responses",
		strings.NewReader(`{"model":"gpt-5.1-codex","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer "+h.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	// Give the asynchronous capture write a moment to land.
	var captures []*store.Capture
	for i := 0; i < 50; i++ {
		captures, _, err = h.store.ListCaptures(context.Background(), store.CaptureFilter{Limit: 10, IncludePayloads: true})
		if err == nil && len(captures) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(captures) == 0 {
		t.Fatal("no capture was recorded")
	}

	c := captures[0]
	if c.AccountID != h.accountID {
		t.Errorf("capture account = %d, want %d", c.AccountID, h.accountID)
	}
	if c.ClientTransport != "sse" || c.UpstreamTransport != "sse" {
		t.Errorf("transports = %s/%s, want sse/sse", c.ClientTransport, c.UpstreamTransport)
	}
	if c.Status != http.StatusOK {
		t.Errorf("capture status = %d", c.Status)
	}
	if c.Outcome != store.OutcomeOK {
		t.Errorf("capture outcome = %q, want ok", c.Outcome)
	}
	if c.ResponseID != "resp_test_1" {
		t.Errorf("response id = %q", c.ResponseID)
	}
	if c.PromptTokens != 11 || c.CompletionTokens != 7 || c.TotalTokens != 18 {
		t.Errorf("usage = %d/%d/%d, want 11/7/18", c.PromptTokens, c.CompletionTokens, c.TotalTokens)
	}

	// Request headers must be recorded, with the bearer token masked.
	if len(c.RequestHeaders) == 0 {
		t.Error("request headers were not captured")
	}
	sawAccept, sawMaskedAuth := false, false
	for _, hd := range c.RequestHeaders {
		switch strings.ToLower(hd.Name) {
		case "accept":
			sawAccept = hd.Value == "application/json"
		case "authorization":
			sawMaskedAuth = strings.Contains(hd.Value, "...")
		}
	}
	if !sawAccept {
		t.Error("captured request headers are missing accept: application/json")
	}
	if !sawMaskedAuth {
		t.Error("captured Authorization header was not masked")
	}
	if len(c.ResponseHeaders) == 0 {
		t.Error("response headers were not captured")
	}

	// Frames must contain every incoming event.
	//
	// For the SSE transport the outbound direction lives in the dedicated
	// RequestHeaders/RequestBody fields (the bytes sent are exactly that body), so
	// only the inbound side appears here. The WebSocket transport additionally
	// records an outbound frame because its envelope differs from the body; that
	// is covered by TestWebSocketCaptureIsBidirectional.
	var inbound int
	var sawTerminal bool
	for _, f := range c.ResponseFrames {
		if f.Dir == "in" {
			inbound++
		}
		if f.Type == "response.completed" {
			sawTerminal = true
		}
	}
	if inbound < 2 {
		t.Errorf("inbound frames = %d, want at least 2", inbound)
	}
	if !sawTerminal {
		t.Error("terminal event was not captured")
	}
	if c.RequestBody == "" {
		t.Error("request body was not captured")
	}
	if !strings.Contains(c.RequestBody, `"store":false`) {
		t.Errorf("captured request body is not the Pi-shaped payload: %s", c.RequestBody)
	}
	if !strings.Contains(c.ResponseText, "response.completed") {
		t.Error("raw SSE text was not captured")
	}
}

// TestWebSocketCaptureIsBidirectional verifies that a WebSocket exchange records
// both directions, which is what makes the full round trip replayable.
func TestWebSocketCaptureIsBidirectional(t *testing.T) {
	cap := &upstreamCapture{}
	h := newHarness(t, wsUpstream(t, cap), "passthrough")

	wsURL := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/v1/responses"
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.Dial(wsURL, http.Header{"Authorization": []string{"Bearer " + h.key}})
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage,
		[]byte(`{"type":"response.create","model":"gpt-5.1-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read failed: %v", err)
		}
		var ev map[string]any
		if err := json.Unmarshal(payload, &ev); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if ev["type"] == "response.completed" {
			break
		}
	}

	var captures []*store.Capture
	for i := 0; i < 50; i++ {
		captures, _, err = h.store.ListCaptures(context.Background(), store.CaptureFilter{Limit: 5, IncludePayloads: true})
		if err == nil && len(captures) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(captures) == 0 {
		t.Fatal("no capture recorded for the websocket exchange")
	}
	c := captures[0]

	var outbound, inbound int
	var outboundEnvelope map[string]any
	for _, f := range c.ResponseFrames {
		switch f.Dir {
		case "out":
			outbound++
			if f.Kind == "ws_frame" {
				// JSON frames are stored as parsed objects, not strings.
				outboundEnvelope, _ = f.Data.(map[string]any)
			}
		case "in":
			inbound++
		}
	}
	if outbound == 0 {
		t.Error("no outbound frame captured for the websocket exchange")
	}
	if inbound == 0 {
		t.Error("no inbound frame captured for the websocket exchange")
	}
	if c.ClientTransport != "ws" {
		t.Errorf("client transport = %q, want ws", c.ClientTransport)
	}
	if c.RequestBody == "" {
		t.Error("outbound frame bytes were not captured")
	}
	if outboundEnvelope == nil {
		t.Fatal("captured outbound WS frame envelope is missing")
	}
	if outboundEnvelope["type"] != "response.create" || outboundEnvelope["model"] != "gpt-5.1-codex" {
		t.Fatalf("captured outbound frame envelope = %#v", outboundEnvelope)
	}
	var sentEnvelope map[string]any
	if err := json.Unmarshal(cap.getWS(), &sentEnvelope); err != nil {
		t.Fatalf("decode actual upstream envelope: %v", err)
	}
	if !reflect.DeepEqual(outboundEnvelope, sentEnvelope) {
		t.Fatalf("captured envelope = %#v, upstream received %#v", outboundEnvelope, sentEnvelope)
	}
}

// TestWebSocketQueueOverflowReturnsExplicitErrors verifies that burst requests
// are either handled or rejected with a structured too_many_requests frame.
func TestWebSocketQueueOverflowReturnsExplicitErrors(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	h := newHarness(t, queuedWSUpstream(t, started, release), "passthrough")
	var releaseOnce sync.Once
	releaseUpstream := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseUpstream()
	wsURL := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/v1/responses"
	conn, _, err := (&websocket.Dialer{HandshakeTimeout: 5 * time.Second}).Dial(wsURL, http.Header{"Authorization": []string{"Bearer " + h.key}})
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	const sent = 6
	writeFrame := func(i int) {
		t.Helper()
		frame := fmt.Sprintf(`{"type":"response.create","event_id":"burst-%d","prompt_cache_key":"burst-%d","model":"gpt-5.1-codex","input":"hi"}`, i, i)
		if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatalf("write frame %d: %v", i, err)
		}
	}
	writeFrame(0)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream did not receive the first request")
	}
	for i := 1; i < sent; i++ {
		writeFrame(i)
	}
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	readFrame := func() map[string]any {
		t.Helper()
		var frame map[string]any
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatalf("read response: %v", err)
		}
		return frame
	}
	// Keep the first response blocked until every excess request is rejected:
	// one active + one buffered + (sent-2) rejected, regardless of scheduling.
	settled := make(map[string]string)
	for i := 2; i < sent; i++ {
		frame := readFrame()
		inner, _ := frame["error"].(map[string]any)
		wantID := fmt.Sprintf("burst-%d", i)
		if frame["type"] != "error" || frame["status"] != float64(http.StatusTooManyRequests) || inner["code"] != "too_many_requests" || frame["event_id"] != wantID {
			t.Fatalf("rejection for %s = %#v", wantID, frame)
		}
		settled[wantID] = "rejected"
	}
	releaseUpstream()
	readCompleted := func(wantID string) {
		t.Helper()
		for _, wantType := range []string{"response.created", "response.completed"} {
			frame := readFrame()
			response, _ := frame["response"].(map[string]any)
			if frame["type"] != wantType || response["id"] != wantID {
				t.Fatalf("response for %s = %#v, want %s", wantID, frame, wantType)
			}
		}
	}
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("burst-%d", i)
		readCompleted(id)
		settled[id] = "completed"
	}
	if len(settled) != sent {
		t.Fatalf("settled requests = %#v, want all %d unique IDs", settled, sent)
	}
	// Rejection does not terminate the reader or poison future exchanges.
	writeFrame(sent)
	readCompleted(fmt.Sprintf("burst-%d", sent))
}

// TestSSEClientUsesAccountWebSocketUpstream verifies the client/upstream protocol
// matrix: an SSE client still reaches a WS backend when the account says ws.
func TestSSEClientUsesAccountWebSocketUpstream(t *testing.T) {
	cap := &upstreamCapture{}
	h := newHarness(t, wsUpstream(t, cap), "sse")
	account, err := h.store.GetAccount(context.Background(), h.accountID)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	account.UpstreamProtocol = "ws"
	if err := h.store.UpdateAccount(context.Background(), account); err != nil {
		t.Fatalf("set account upstream protocol: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/responses",
		strings.NewReader(`{"model":"gpt-5.1-codex","input":"hi","stream":true}`))
	req.Header.Set("Authorization", "Bearer "+h.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	raw, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		t.Fatalf("read response: %v", readErr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, raw)
	}

	reader := upstream.ParseSSE(strings.NewReader(string(raw)))
	var types []string
	for {
		event, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("parse relayed SSE: %v; body=%s", err, raw)
		}
		if event != nil {
			types = append(types, event.Type)
		}
	}
	wantTypes := []string{"response.created", "response.output_text.delta", "response.completed"}
	if strings.Join(types, ",") != strings.Join(wantTypes, ",") {
		t.Fatalf("event types = %v, want %v", types, wantTypes)
	}

	headers, _ := cap.get()
	if !strings.EqualFold(headers.Get("Upgrade"), "websocket") || !strings.Contains(strings.ToLower(headers.Get("Connection")), "upgrade") {
		t.Fatalf("upstream handshake headers = Upgrade %q, Connection %q", headers.Get("Upgrade"), headers.Get("Connection"))
	}
	frame := cap.getWS()
	if len(frame) == 0 {
		t.Fatal("upstream did not receive a response.create frame")
	}
	var envelope map[string]any
	if err := json.Unmarshal(frame, &envelope); err != nil {
		t.Fatalf("decode upstream response.create frame: %v", err)
	}
	if envelope["type"] != "response.create" {
		t.Fatalf("upstream frame type = %#v, want response.create", envelope["type"])
	}
}

// TestEnvironmentContextIsDropped verifies the Codex environment block never
// reaches the backend, matching Pi's behaviour.
func TestEnvironmentContextIsDropped(t *testing.T) {
	cap := &upstreamCapture{}
	h := newHarness(t, sseUpstream(cap), "passthrough")

	body := `{
		"model": "gpt-5.1-codex",
		"instructions": "Base instructions.\n<environment_context>\n  <cwd>/home/u/proj</cwd>\n  <timezone>Europe/Berlin</timezone>\n</environment_context>",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"real question"}]},
			{"type":"message","role":"user",
			 "internal_chat_message_metadata_passthrough":{"content_item_kinds":["environments.environment_context"]},
			 "content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/home/u/proj</cwd>\n  <timezone>Europe/Berlin</timezone>\n</environment_context>"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"follow up"}]}
		]
	}`

	req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+h.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	_, sentBody := cap.get()
	sent := string(sentBody)

	if strings.Contains(sent, "<environment_context>") {
		t.Errorf("environment_context leaked upstream:\n%s", sent)
	}
	if strings.Contains(sent, "Europe/Berlin") {
		t.Errorf("environment timezone leaked upstream:\n%s", sent)
	}
	if strings.Contains(sent, "environments.environment_context") {
		t.Errorf("environment content-item metadata leaked upstream:\n%s", sent)
	}

	var decoded struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(sentBody, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Input) != 2 {
		t.Errorf("input items = %d, want 2 (the environment item removed)", len(decoded.Input))
	}
	for _, item := range decoded.Input {
		raw, _ := json.Marshal(item)
		if strings.Contains(string(raw), "environment_context") {
			t.Errorf("input item still references environment_context: %s", raw)
		}
	}
}

// TestWebSocketClientAndUpstream verifies the WS->WS path end to end.
func TestWebSocketClientAndUpstream(t *testing.T) {
	cap := &upstreamCapture{}
	h := newHarness(t, wsUpstream(t, cap), "passthrough")

	wsURL := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/v1/responses"
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.Dial(wsURL, http.Header{"Authorization": []string{"Bearer " + h.key}})
	if err != nil {
		t.Fatalf("client websocket dial failed: %v", err)
	}
	defer conn.Close()

	// Pi sends the response.create envelope.
	frame := `{"type":"response.create","model":"gpt-5.1-codex","instructions":"hi","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatalf("client write failed: %v", err)
	}

	var events []map[string]any
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("client read failed after %d events: %v", len(events), err)
		}
		var ev map[string]any
		if err := json.Unmarshal(payload, &ev); err != nil {
			t.Fatalf("event is not JSON: %v", err)
		}
		events = append(events, ev)
		if ev["type"] == "response.completed" {
			break
		}
	}

	if len(events) < 3 {
		t.Fatalf("got %d events, want at least 3", len(events))
	}
	if events[0]["type"] != "response.created" {
		t.Errorf("first event = %v, want response.created", events[0]["type"])
	}
	if events[len(events)-1]["type"] != "response.completed" {
		t.Errorf("last event = %v, want response.completed", events[len(events)-1]["type"])
	}

	// The upstream frame must carry the response.create envelope with Pi's body.
	cap.mu.Lock()
	sentFrame := string(cap.wsFrame)
	cap.mu.Unlock()
	if !strings.HasPrefix(sentFrame, `{"type":"response.create","model":"gpt-5.1-codex","input":`) {
		t.Errorf("upstream websocket frame is not Pi-shaped:\n%s", sentFrame)
	}
	if !strings.Contains(sentFrame, `"stream":true,`) || !strings.HasSuffix(sentFrame, `"store":false}`) {
		t.Errorf("upstream websocket frame lacks Pi's field order:\n%s", sentFrame)
	}
}

// TestWebSocketClientWithSSEUpstream verifies the cross-transport bridge.
func TestWebSocketClientWithSSEUpstream(t *testing.T) {
	cap := &upstreamCapture{}
	h := newHarness(t, sseUpstream(cap), "sse") // force the upstream to SSE

	wsURL := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/v1/responses"
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.Dial(wsURL, http.Header{"Authorization": []string{"Bearer " + h.key}})
	if err != nil {
		t.Fatalf("client websocket dial failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage,
		[]byte(`{"type":"response.create","model":"gpt-5.1-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	var types []string
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read failed after %d events: %v", len(types), err)
		}
		var ev map[string]any
		if err := json.Unmarshal(payload, &ev); err != nil {
			t.Fatalf("event is not JSON: %v", err)
		}
		evType, _ := ev["type"].(string)
		types = append(types, evType)
		if evType == "response.completed" {
			break
		}
	}
	if len(types) == 0 || types[len(types)-1] != "response.completed" {
		t.Fatalf("event types = %v, want a response.completed terminator", types)
	}
}

// TestMiddlewareCanDropRequest verifies a middleware can refuse a request before
// it reaches the backend.
func TestMiddlewareCanDropRequest(t *testing.T) {
	cap := &upstreamCapture{}
	h := newHarness(t, sseUpstream(cap), "passthrough")

	// Enable block_prompt with a pattern matching the payload.
	err := h.store.UpsertMiddleware(context.Background(), &store.MiddlewareRow{
		Name: "block_prompt", Enabled: true, OrderIndex: 5, Config: `{"pattern":"forbidden-token"}`,
	})
	if err != nil {
		t.Fatalf("configure middleware: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/responses",
		strings.NewReader(`{"model":"gpt-5.1-codex","input":"please use forbidden-token"}`))
	req.Header.Set("Authorization", "Bearer "+h.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (request should have been dropped)", resp.StatusCode)
	}
	if headers, _ := cap.get(); headers != nil {
		t.Errorf("request reached the upstream despite being blocked")
	}
}

// TestNonStreamingResponseAggregation verifies stream:false returns one object.
func TestNonStreamingResponseAggregation(t *testing.T) {
	cap := &upstreamCapture{}
	h := newHarness(t, sseUpstream(cap), "passthrough")

	req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/responses",
		strings.NewReader(`{"model":"gpt-5.1-codex","stream":false,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer "+h.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type = %q, want JSON for stream:false", ct)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["id"] != "resp_test_1" {
		t.Errorf("id = %v, want resp_test_1", payload["id"])
	}
	if payload["status"] != "completed" {
		t.Errorf("status = %v", payload["status"])
	}
}

// TestUpstreamErrorIsReported verifies a backend failure becomes an HTTP error.
func TestUpstreamErrorIsReported(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"usage_limit_reached","message":"limit","plan_type":"plus","resets_at":4102444800}}`))
	}), "passthrough")

	req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/responses",
		strings.NewReader(`{"model":"gpt-5.1-codex","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer "+h.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "usage limit") {
		t.Errorf("error body = %s, want a friendly usage-limit message", raw)
	}
}

// TestInvalidKeyRejected verifies auth.
func TestInvalidKeyRejected(t *testing.T) {
	h := newHarness(t, sseUpstream(&upstreamCapture{}), "passthrough")

	for _, key := range []string{"", "sk-pi-wrong"} {
		req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/responses",
			strings.NewReader(`{"model":"gpt-5.1-codex","input":"hi"}`))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("key %q: status = %d, want 401", key, resp.StatusCode)
		}
	}
}

// usageTerminalSSE reports cached tokens, which the ledger stores separately.
const usageTerminalSSE = `data: {"type":"response.completed","response":{"id":"resp_usage_1","status":"completed","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":80},"output_tokens_details":{"reasoning_tokens":5}}}}` + "\n\n"

// TestUsageRecordCapturesMetricsOnTheWire is the end-to-end evidence for the
// statistics feature: a real HTTP request through the gateway must leave a
// finished usage_records row carrying tokens, cached tokens, TTFT, latency and
// the calculated cost.
func TestUsageRecordCapturesMetricsOnTheWire(t *testing.T) {
	cap := &upstreamCapture{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		cap.set(r, nil)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte(deltaSSE))
		if flusher != nil {
			flusher.Flush()
		}
		// A measurable gap between the first token and completion keeps TTFT and
		// TPS distinguishable from zero.
		time.Sleep(25 * time.Millisecond)
		_, _ = w.Write([]byte(usageTerminalSSE))
		if flusher != nil {
			flusher.Flush()
		}
	})
	h := newHarness(t, handler, "sse")

	body := `{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true}`
	req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+h.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("status = %d, body = %s", resp.StatusCode, raw)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	rec := waitForUsageRecord(t, h.store)

	if rec.Outcome != "succeeded" {
		t.Errorf("outcome = %q, want succeeded", rec.Outcome)
	}
	if rec.Model != "gpt-5.5" {
		t.Errorf("model = %q, want gpt-5.5", rec.Model)
	}
	if rec.APIKeyName != "test-key" || rec.AccountName != "test-account" {
		t.Errorf("attribution = %q/%q", rec.APIKeyName, rec.AccountName)
	}
	if rec.ClientTransport != "sse" {
		t.Errorf("client transport = %q, want sse", rec.ClientTransport)
	}
	for _, tc := range []struct {
		name string
		got  *int64
		want int64
	}{
		{"input_tokens", rec.InputTokens, 100},
		{"cached_tokens", rec.CachedTokens, 80},
		{"output_tokens", rec.OutputTokens, 20},
		{"total_tokens", rec.TotalTokens, 120},
		{"reasoning_tokens", rec.ReasoningTokens, 5},
	} {
		if tc.got == nil {
			t.Errorf("%s is NULL, want %d", tc.name, tc.want)
			continue
		}
		if *tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, *tc.got, tc.want)
		}
	}
	if rec.FirstTokenMS <= 0 {
		t.Errorf("first_token_ms = %d, want > 0 (TTFT must be measured)", rec.FirstTokenMS)
	}
	if rec.LatencyMS <= 0 {
		t.Errorf("latency_ms = %d, want > 0", rec.LatencyMS)
	}
	if rec.FirstTokenMS > rec.LatencyMS {
		t.Errorf("first_token_ms (%d) must not exceed latency_ms (%d)", rec.FirstTokenMS, rec.LatencyMS)
	}
	if rec.CostMicros == nil || *rec.CostMicros <= 0 {
		t.Errorf("cost_micros = %v, want a positive calculated cost", rec.CostMicros)
	} else if rec.CostSource != "builtin" && rec.CostSource != "override" {
		t.Errorf("cost_source = %q, want builtin/override", rec.CostSource)
	}
}

// waitForUsageRecord waits for the ledger write, which happens in the handler's
// finish path and may land just after the client has read the response.
func waitForUsageRecord(t *testing.T, st *store.Store) store.UsageRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		records, total, err := st.ListUsageRecords(context.Background(), store.UsageFilter{Limit: 20})
		if err != nil {
			t.Fatalf("list usage records: %v", err)
		}
		for _, r := range records {
			if r.Outcome != "running" {
				return r
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no finalised usage record; total = %d", total)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
