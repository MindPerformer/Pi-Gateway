package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestChatGPTRedirectURI(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"", 0, "http://127.0.0.1:1455/auth/callback"},
		{"127.0.0.1", 1455, "http://127.0.0.1:1455/auth/callback"},
		{"0.0.0.0", 2000, "http://0.0.0.0:2000/auth/callback"},
	}
	for _, tc := range cases {
		if got := ChatGPTRedirectURI(tc.host, tc.port); got != tc.want {
			t.Errorf("ChatGPTRedirectURI(%q,%d) = %q, want %q", tc.host, tc.port, got, tc.want)
		}
	}
}

func TestAgentHostID(t *testing.T) {
	got, err := AgentHostID("0F5D2A1C-1111-2222-3333-444455556666")
	if err != nil {
		t.Fatalf("AgentHostID returned error: %v", err)
	}
	if want := "urn:uuid:0f5d2a1c-1111-2222-3333-444455556666"; got != want {
		t.Errorf("AgentHostID = %q, want %q", got, want)
	}
	for _, bad := range []string{"", "not-a-uuid", "0f5d2a1c11112222333344445555666"} {
		if _, err := AgentHostID(bad); err == nil {
			t.Errorf("AgentHostID(%q) = nil error, want error", bad)
		}
	}
}

func TestBuildChatGPTAuthorizeURL(t *testing.T) {
	redirect := ChatGPTRedirectURI("", 0)
	raw := BuildChatGPTAuthorizeURL("challenge-value", "state-value", "nonce-value", "urn:uuid:abc", redirect)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("authorize url did not parse: %v", err)
	}
	if u.Scheme != "https" || u.Host != "auth.openai.com" || u.Path != "/api/accounts/authorize" {
		t.Fatalf("unexpected authorize endpoint: %s", raw)
	}
	q := u.Query()
	want := map[string]string{
		"client_id":             ChatGPTDynamicClientID,
		"agent_name_hint":       "Pi",
		"ext_agent_host_id":     "urn:uuid:abc",
		"response_type":         "code",
		"redirect_uri":          redirect,
		"resource":              ChatGPTResource,
		"scope":                 ChatGPTScope,
		"state":                 "state-value",
		"code_challenge":        "challenge-value",
		"code_challenge_method": "S256",
		"nonce":                 "nonce-value",
	}
	for k, v := range want {
		if got := q.Get(k); got != v {
			t.Errorf("query %s = %q, want %q", k, got, v)
		}
	}
	// The legacy Codex-only parameters must not leak into this flow.
	for _, forbidden := range []string{"originator", "codex_cli_simplified_flow", "id_token_add_organizations"} {
		if q.Has(forbidden) {
			t.Errorf("authorize url unexpectedly carries legacy parameter %q", forbidden)
		}
	}
	if !strings.Contains(q.Get("scope"), ChatGPTDirectTokenScope) {
		t.Errorf("scope %q must include %q", q.Get("scope"), ChatGPTDirectTokenScope)
	}
}

func TestParseChatGPTCallback(t *testing.T) {
	redirect := ChatGPTRedirectURI("", 0)
	valid := redirect + "?code=the-code&state=the-state&client_id=issued-client-123"

	code, clientID, err := ParseChatGPTCallback(valid, "the-state", redirect)
	if err != nil {
		t.Fatalf("valid callback returned error: %v", err)
	}
	if code != "the-code" || clientID != "issued-client-123" {
		t.Errorf("got code=%q clientID=%q", code, clientID)
	}

	cases := []struct {
		name  string
		input string
		state string
	}{
		{"empty", "", "the-state"},
		{"not a url", "just-a-code", "the-state"},
		{"wrong origin", "http://evil.example.com/auth/callback?code=c&state=the-state&client_id=x", "the-state"},
		{"wrong path", "http://127.0.0.1:1455/other?code=c&state=the-state&client_id=x", "the-state"},
		{"state mismatch", redirect + "?code=c&state=other&client_id=x", "the-state"},
		{"missing code", redirect + "?state=the-state&client_id=x", "the-state"},
		{"missing client id", redirect + "?code=c&state=the-state", "the-state"},
		{"error param", redirect + "?error=access_denied&state=the-state", "the-state"},
	}
	for _, tc := range cases {
		if _, _, err := ParseChatGPTCallback(tc.input, tc.state, redirect); err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
	}
}

// chatGPTTokenServer stubs the token endpoint and echoes the request form back so
// tests can assert what was sent.
func chatGPTTokenServer(t *testing.T, status int, payload map[string]any) (*httptest.Server, *url.Values) {
	t.Helper()
	var seen url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		seen = r.PostForm
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/x-www-form-urlencoded") {
			t.Errorf("content-type = %q", ct)
		}
		if accept := r.Header.Get("Accept"); !strings.Contains(accept, "application/json") {
			t.Errorf("accept = %q", accept)
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func jwtWithClaims(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	body, _ := json.Marshal(claims)
	enc := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	return enc(header) + "." + enc(body) + ".sig"
}

func TestValidateChatGPTClientID(t *testing.T) {
	for _, clientID := range []string{"", ClientID, ChatGPTDynamicClientID} {
		if err := ValidateChatGPTClientID(clientID); err == nil {
			t.Errorf("ValidateChatGPTClientID(%q) returned nil", clientID)
		}
	}
	if err := ValidateChatGPTClientID("issued-client-123"); err != nil {
		t.Fatalf("issued client id rejected: %v", err)
	}
}

func TestExchangeChatGPTCodeRequiresIssuedClientID(t *testing.T) {
	c := NewClient(nil)
	if _, err := c.ExchangeChatGPTCode(context.Background(), "code", "verifier", "  ", ChatGPTRedirectURI("", 0)); err == nil {
		t.Fatal("expected an error when the issued client id is missing")
	}
}

func TestExchangeChatGPTCodeSendsResourceAndParsesToken(t *testing.T) {
	idToken := jwtWithClaims(t, map[string]any{"email": "user@example.com"})
	srv, seen := chatGPTTokenServer(t, http.StatusOK, map[string]any{
		"access_token":  "access-token",
		"refresh_token": "refresh-token",
		"id_token":      idToken,
		"scope":         "openid profile email offline_access resource.invoke " + ChatGPTDirectTokenScope,
		"expires_in":    3600,
	})

	c := NewClient(nil)
	c.chatgptTokenURL = srv.URL
	redirect := ChatGPTRedirectURI("", 0)
	tok, err := c.ExchangeChatGPTCode(context.Background(), "the-code", "the-verifier", "issued-client", redirect)
	if err != nil {
		t.Fatalf("exchange returned error: %v", err)
	}

	// The form must carry exactly what Pi sends, including the resource parameter.
	want := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     "issued-client",
		"code":          "the-code",
		"code_verifier": "the-verifier",
		"redirect_uri":  redirect,
		"resource":      ChatGPTResource,
	}
	for k, v := range want {
		if got := seen.Get(k); got != v {
			t.Errorf("form %s = %q, want %q", k, got, v)
		}
	}
	if tok.Access != "access-token" || tok.Refresh != "refresh-token" {
		t.Errorf("unexpected token: %+v", tok)
	}
	if tok.Email != "user@example.com" {
		t.Errorf("email = %q, want user@example.com", tok.Email)
	}
	if !containsScope(tok.Scopes, ChatGPTDirectTokenScope) {
		t.Errorf("scopes = %v, want %q", tok.Scopes, ChatGPTDirectTokenScope)
	}
	// The 3-minute expiry margin must be applied.
	if remaining := time.Until(tok.ExpiresAt).Seconds(); remaining > 3600 || remaining < 3600-5*60 {
		t.Errorf("expiry margin not applied; remaining=%.0fs", remaining)
	}
}

func TestRefreshChatGPTCarriesResourceAndClientID(t *testing.T) {
	srv, seen := chatGPTTokenServer(t, http.StatusOK, map[string]any{
		"access_token":  "new-access",
		"refresh_token": "new-refresh",
		"id_token":      jwtWithClaims(t, map[string]any{"email": "user@example.com"}),
		"scope":         ChatGPTScope,
		"expires_in":    1800,
	})

	c := NewClient(nil)
	c.chatgptTokenURL = srv.URL
	if _, err := c.RefreshChatGPT(context.Background(), "old-refresh", "issued-client"); err != nil {
		t.Fatalf("refresh returned error: %v", err)
	}
	if got := seen.Get("grant_type"); got != "refresh_token" {
		t.Errorf("grant_type = %q", got)
	}
	if got := seen.Get("refresh_token"); got != "old-refresh" {
		t.Errorf("refresh_token = %q", got)
	}
	if got := seen.Get("client_id"); got != "issued-client" {
		t.Errorf("client_id = %q", got)
	}
	if got := seen.Get("resource"); got != ChatGPTResource {
		t.Errorf("resource = %q, want %q", got, ChatGPTResource)
	}
}

func TestRefreshChatGPTRequiresClientID(t *testing.T) {
	c := NewClient(nil)
	if _, err := c.RefreshChatGPT(context.Background(), "refresh", ""); err == nil {
		t.Fatal("expected an error when the issued client id is missing")
	}
}

func TestChatGPTTokenRejectsMissingDirectScope(t *testing.T) {
	srv, _ := chatGPTTokenServer(t, http.StatusOK, map[string]any{
		"access_token":  "a",
		"refresh_token": "r",
		"id_token":      jwtWithClaims(t, map[string]any{}),
		"scope":         "openid profile email offline_access",
		"expires_in":    3600,
	})
	c := NewClient(nil)
	c.chatgptTokenURL = srv.URL
	if _, err := c.RefreshChatGPT(context.Background(), "refresh", "client"); err == nil {
		t.Fatal("expected an error when the direct-token scope is absent")
	}
}

func TestExchangeChatGPTCodeRejectsMissingIDToken(t *testing.T) {
	srv, _ := chatGPTTokenServer(t, http.StatusOK, map[string]any{
		"access_token":  "a",
		"refresh_token": "r",
		"scope":         ChatGPTScope,
		"expires_in":    3600,
	})
	c := NewClient(nil)
	c.chatgptTokenURL = srv.URL
	if _, err := c.ExchangeChatGPTCode(context.Background(), "code", "verifier", "issued-client", ChatGPTRedirectURI("", 0)); err == nil {
		t.Fatal("expected an error when the id token is absent")
	}
}

func TestRefreshChatGPTAcceptsMissingIDToken(t *testing.T) {
	srv, seen := chatGPTTokenServer(t, http.StatusOK, map[string]any{
		"access_token":  "new-access",
		"refresh_token": "new-refresh",
		"scope":         ChatGPTScope,
		"expires_in":    3600,
	})
	c := NewClient(nil)
	c.chatgptTokenURL = srv.URL
	tok, err := c.RefreshChatGPT(context.Background(), "old-refresh", "issued-client")
	if err != nil {
		t.Fatalf("Pi-compatible refresh without id_token was rejected: %v", err)
	}
	if tok.Access != "new-access" || tok.Refresh != "new-refresh" || tok.ClientID != "issued-client" || tok.IDToken != "" {
		t.Fatalf("refresh did not preserve the rotated credential: %+v", tok)
	}
	if !containsScope(tok.Scopes, ChatGPTDirectTokenScope) {
		t.Fatalf("refresh lost the direct-token scope: %v", tok.Scopes)
	}
	if seen.Get("resource") != "https://api.openai.com/v1" || seen.Get("client_id") != "issued-client" || seen.Has("scope") {
		t.Fatalf("unexpected refresh form: %v", *seen)
	}
}

func TestValidateChatGPTAccessTokenRejectsCodexShape(t *testing.T) {
	codex := jwtWithClaims(t, map[string]any{
		JWTClaimPath: map[string]any{"chatgpt_account_id": "codex-account"},
	})
	if err := ValidateChatGPTAccessToken(codex); err == nil {
		t.Fatal("Codex-shaped access token was accepted as a ChatGPT credential")
	}

	chatgpt := jwtWithClaims(t, map[string]any{"scope": ChatGPTDirectTokenScope})
	if err := ValidateChatGPTAccessToken(chatgpt); err != nil {
		t.Fatalf("valid ChatGPT-shaped access token rejected: %v", err)
	}
}

func TestVerifyIDTokenNoncePolicy(t *testing.T) {
	if err := VerifyIDTokenNonce("not-a-jwt", "expected"); err != nil {
		t.Fatalf("decode failure should be accepted under Pi policy: %v", err)
	}
	if err := VerifyIDTokenNonce(jwtWithClaims(t, map[string]any{}), "expected"); err != nil {
		t.Fatalf("missing nonce should be accepted under Pi policy: %v", err)
	}
	if err := VerifyIDTokenNonce(jwtWithClaims(t, map[string]any{"nonce": "other"}), "expected"); err == nil {
		t.Fatal("nonce mismatch must be rejected")
	}
	if err := VerifyIDTokenNonce(jwtWithClaims(t, map[string]any{"nonce": 123}), "expected"); err == nil {
		t.Fatal("non-string nonce must be rejected as a mismatch")
	}
	if err := VerifyIDTokenNonce(jwtWithClaims(t, map[string]any{"nonce": "expected"}), "expected"); err != nil {
		t.Fatalf("matching nonce rejected: %v", err)
	}
}

func TestCodexAuthorizeURLMatchesPiBranding(t *testing.T) {
	u, err := url.Parse(BuildAuthorizeURL("test-challenge", "test-state", ""))
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "auth.openai.com" || u.Path != "/oauth/authorize" {
		t.Fatalf("unexpected URL: %s", u)
	}
	want := map[string]string{
		"response_type": "code", "client_id": "app_EMoamEEZ73f0CkXaXp7hrann",
		"redirect_uri": "http://localhost:1455/auth/callback", "scope": "openid profile email offline_access",
		"code_challenge": "test-challenge", "code_challenge_method": "S256", "state": "test-state",
		"id_token_add_organizations": "true", "codex_cli_simplified_flow": "true", "originator": "pi",
	}
	q := u.Query()
	if len(q) != len(want) {
		t.Fatalf("extra OAuth parameters: %v", q)
	}
	for key, value := range want {
		if q.Get(key) != value {
			t.Errorf("%s = %q, want %q", key, q.Get(key), value)
		}
	}
	if strings.Contains(strings.ToLower(u.String()), "gateway") {
		t.Fatal("gateway identity leaked to OAuth")
	}
}

func TestOAuthCallbackShowsPiWithoutLeakingCode(t *testing.T) {
	client := NewClient(&http.Client{Transport: lifecycleRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Header: make(http.Header), Body: http.NoBody}, nil
	})})
	manager := NewFlowManager(client, FlowOptions{CallbackHost: "127.0.0.1", CallbackPort: 0, Logger: quietLogger()})
	t.Cleanup(func() { closeLifecycleManager(t, manager) })
	flow, err := manager.StartFlow(context.Background(), FlowKindCodex, nil)
	if err != nil {
		t.Fatal(err)
	}
	authorize, err := url.Parse(flow.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	state := authorize.Query().Get("state")
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/auth/callback?code=never-echo-this-code&state="+url.QueryEscape(state), nil)
	manager.handleCallback(recorder, request)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, "return to Pi.") {
		t.Fatalf("callback status=%d body=%s", recorder.Code, body)
	}
	for _, forbidden := range []string{"gateway", "never-echo-this-code", state} {
		if forbidden != "" && strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Errorf("callback exposes %q", forbidden)
		}
	}
	if !strings.Contains(body, "Authorization received") {
		t.Fatal("callback must not claim token exchange succeeded before it finishes")
	}
}
