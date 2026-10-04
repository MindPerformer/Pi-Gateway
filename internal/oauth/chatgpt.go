package oauth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Sign in with ChatGPT ("openai-chatgpt") constants, mirrored from Pi's
// auth/oauth/openai-chatgpt.ts. This is the modern subscription route that sends
// the resulting token straight to api.openai.com — not the legacy Codex backend.

const (
	// ChatGPTDynamicClientID is sent on every authorization request; OpenAI
	// registers a fresh public client and returns the issued client id in the
	// callback. The issued id (not this constant) is what later refreshes use.
	ChatGPTDynamicClientID = "dynamic_agent_client"
	ChatGPTAgentNameHint   = "Pi"

	ChatGPTAuthBaseURL  = "https://auth.openai.com"
	ChatGPTAuthorizeURL = ChatGPTAuthBaseURL + "/api/accounts/authorize"
	ChatGPTTokenURL     = ChatGPTAuthBaseURL + "/api/accounts/oauth/token"

	// ChatGPTResource scopes the grant to the OpenAI API host.
	ChatGPTResource = "https://api.openai.com/v1"

	ChatGPTCallbackPath        = "/auth/callback"
	ChatGPTCallbackPort        = 1455
	ChatGPTDefaultCallbackHost = "127.0.0.1"

	// ChatGPTDirectTokenScope must be present in the granted scopes.
	ChatGPTDirectTokenScope = "chatgpt.tokens.use.direct"

	ChatGPTScope = "openid profile email offline_access resource.invoke " + ChatGPTDirectTokenScope

	// ChatGPTExpiryMargin is subtracted from the real expiry so a request never
	// starts with a token that is about to expire (Pi uses 3 minutes).
	ChatGPTExpiryMargin = 3 * time.Minute
)

// uuidPattern matches the device id OpenAI expects in ext_agent_host_id.
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ChatGPTRedirectURI builds the loopback callback URL Pi registers.
func ChatGPTRedirectURI(host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" {
		host = ChatGPTDefaultCallbackHost
	}
	if port <= 0 {
		port = ChatGPTCallbackPort
	}
	return fmt.Sprintf("http://%s:%d%s", host, port, ChatGPTCallbackPath)
}

// NewDeviceUUID returns a fresh random UUID used as the installation identity.
func NewDeviceUUID() string { return uuid.NewString() }

// GenerateRandomValue returns 32 random bytes as base64url — Pi's randomValue,
// used for the ChatGPT state and nonce.
func GenerateRandomValue() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("oauth: random value: %w", err)
	}
	return base64URLEncode(buf), nil
}

// AgentHostID renders OpenAI's stable per-installation identifier
// `urn:uuid:<uuid>` (Pi's agentHostId).
func AgentHostID(deviceID string) (string, error) {
	id := strings.ToLower(strings.TrimSpace(deviceID))
	if !uuidPattern.MatchString(id) {
		return "", errors.New("oauth: Sign in with ChatGPT requires a device ID (UUID) for this installation")
	}
	return "urn:uuid:" + id, nil
}

// BuildChatGPTAuthorizeURL builds the authorization URL with exactly the query
// parameters Pi sets for Sign in with ChatGPT.
func BuildChatGPTAuthorizeURL(challenge, state, nonce, agentHostID, redirectURI string) string {
	u, err := url.Parse(ChatGPTAuthorizeURL)
	if err != nil {
		return ChatGPTAuthorizeURL
	}
	q := u.Query()
	q.Set("client_id", ChatGPTDynamicClientID)
	q.Set("agent_name_hint", ChatGPTAgentNameHint)
	q.Set("ext_agent_host_id", agentHostID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("resource", ChatGPTResource)
	q.Set("scope", ChatGPTScope)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("nonce", nonce)
	u.RawQuery = q.Encode()
	return u.String()
}

// ParseChatGPTCallback validates a pasted/looked-up callback URL and returns the
// authorization code plus the client id OpenAI issued for this login.
//
// Pi requires the full redirect URL, matching the registered redirect in both
// origin and path, with a matching state.
func ParseChatGPTCallback(input, expectedState, redirectURI string) (code, clientID string, err error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return "", "", errors.New("oauth: paste the full callback URL from the browser")
	}
	got, err := url.Parse(raw)
	if err != nil || got.Scheme == "" || got.Host == "" {
		return "", "", errors.New("oauth: paste the full callback URL from the browser")
	}
	expected, err := url.Parse(redirectURI)
	if err != nil {
		return "", "", fmt.Errorf("oauth: invalid redirect uri %q: %w", redirectURI, err)
	}
	if got.Scheme != expected.Scheme || got.Host != expected.Host || got.Path != expected.Path {
		return "", "", fmt.Errorf("oauth: the pasted callback URL must start with %s", redirectURI)
	}
	if e := got.Query().Get("error"); e != "" {
		return "", "", fmt.Errorf("oauth: ChatGPT authorization failed: %s", e)
	}

	code = got.Query().Get("code")
	if code == "" {
		return "", "", errors.New("oauth: missing authorization code")
	}
	state := got.Query().Get("state")
	if state == "" {
		return "", "", errors.New("oauth: missing OAuth state")
	}
	if state != expectedState {
		return "", "", errors.New("oauth: OAuth state mismatch")
	}
	clientID = strings.TrimSpace(got.Query().Get("client_id"))
	if clientID == "" {
		return "", "", errors.New("oauth: OpenAI OAuth registration callback did not contain an issued client ID")
	}
	return code, clientID, nil
}

// ValidateChatGPTClientID requires the issued registration id, not the legacy
// Codex client id or the dynamic registration placeholder. The provider verifies
// its binding to a refresh token during RefreshChatGPT.
func ValidateChatGPTClientID(clientID string) error {
	switch strings.TrimSpace(clientID) {
	case "":
		return errors.New("oauth: missing issued ChatGPT oauth_client_id; connect ChatGPT again")
	case ClientID, ChatGPTDynamicClientID:
		return errors.New("oauth: oauth_client_id must be an issued ChatGPT client id, not the Codex client or registration placeholder")
	default:
		return nil
	}
}

// ExchangeChatGPTCode swaps an authorization code for a ChatGPT credential. The
// client id must be the one OpenAI issued in the callback.
func (c *Client) ExchangeChatGPTCode(ctx context.Context, code, verifier, clientID, redirectURI string) (*Token, error) {
	if err := ValidateChatGPTClientID(clientID); err != nil {
		return nil, err
	}
	clientID = strings.TrimSpace(clientID)
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
		"resource":      {ChatGPTResource},
	}
	tok, err := c.postChatGPTToken(ctx, form, "exchange")
	if err != nil {
		return nil, err
	}
	// Remember the issued client id: refreshes are bound to it.
	tok.ClientID = clientID
	return tok, nil
}

// RefreshChatGPT exchanges a refresh token for a new ChatGPT credential. Both the
// issued client id and the resource parameter are required.
func (c *Client) RefreshChatGPT(ctx context.Context, refreshToken, clientID string) (*Token, error) {
	if err := ValidateChatGPTClientID(clientID); err != nil {
		return nil, err
	}
	clientID = strings.TrimSpace(clientID)
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
		"resource":      {ChatGPTResource},
	}
	tok, err := c.postChatGPTToken(ctx, form, "refresh")
	if err != nil {
		return nil, err
	}
	tok.ClientID = clientID
	return tok, nil
}

type rawChatGPTTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	Scope        string `json:"scope"`
	ExpiresIn    *int   `json:"expires_in"`
}

// postChatGPTToken performs the form POST and validates the ChatGPT contract: an
// access token, refresh token, scope containing the direct-token scope, and a
// positive expiry. Pi requires an ID token only for the authorization-code exchange.
func (c *Client) postChatGPTToken(ctx context.Context, form url.Values, operation string) (*Token, error) {
	endpoint := c.chatgptTokenURL
	if endpoint == "" {
		endpoint = ChatGPTTokenURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oauth: chatgpt %s request: %w", operation, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: chatgpt %s request failed: %w", operation, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("oauth: chatgpt %s read body: %w", operation, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("oauth: chatgpt token %s failed (%d): %s", operation, resp.StatusCode, safeErrorBody(body))
	}

	var raw rawChatGPTTokenResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("oauth: chatgpt %s decode: %w", operation, err)
	}
	if strings.TrimSpace(raw.AccessToken) == "" {
		return nil, fmt.Errorf("oauth: chatgpt %s response has invalid access_token", operation)
	}
	if err := ValidateChatGPTAccessToken(raw.AccessToken); err != nil {
		return nil, fmt.Errorf("oauth: chatgpt %s response: %w", operation, err)
	}
	if strings.TrimSpace(raw.RefreshToken) == "" {
		return nil, fmt.Errorf("oauth: chatgpt %s response has invalid refresh_token", operation)
	}
	if strings.TrimSpace(raw.Scope) == "" {
		return nil, fmt.Errorf("oauth: chatgpt %s response has invalid scope", operation)
	}
	if raw.ExpiresIn == nil || *raw.ExpiresIn <= 0 {
		return nil, fmt.Errorf("oauth: chatgpt %s response has invalid expires_in", operation)
	}
	scopes := strings.Fields(raw.Scope)
	if !containsScope(scopes, ChatGPTDirectTokenScope) {
		return nil, fmt.Errorf("oauth: chatgpt grant did not include %s", ChatGPTDirectTokenScope)
	}
	// Pi's exchangeAuthorizationCode requires an ID token, but refreshAccessToken
	// only calls credentialFromTokenResponse and accepts a response without one.
	if form.Get("grant_type") == "authorization_code" && strings.TrimSpace(raw.IDToken) == "" {
		return nil, fmt.Errorf("oauth: chatgpt %s response did not contain an ID token", operation)
	}

	tok := &Token{
		Access:    raw.AccessToken,
		Refresh:   raw.RefreshToken,
		IDToken:   raw.IDToken,
		ExpiresAt: time.Now().Add(time.Duration(*raw.ExpiresIn)*time.Second - ChatGPTExpiryMargin),
		Scopes:    scopes,
	}
	// The ChatGPT credential carries no chatgpt_account_id claim; FillTokenMetadata
	// still harvests plan type and email when present.
	FillTokenMetadata(tok)
	return tok, nil
}

// VerifyIDTokenNonce follows Pi's deliberately loose nonce baseline: an empty
// expected nonce, an undecodable ID token, or a missing nonce is accepted because
// some legitimate providers omit/reshape the claim. A decoded nonce that is present
// but non-string or different is an explicit mismatch and is rejected.
func VerifyIDTokenNonce(idToken, expected string) error {
	if strings.TrimSpace(expected) == "" {
		slog.Debug("oauth: nonce check skipped because no expected nonce was supplied")
		return nil
	}
	claims, err := DecodeJWT(idToken)
	if err != nil {
		slog.Debug("oauth: accepting ID token with undecodable nonce claim per Pi compatibility policy", "error", err)
		return nil
	}
	raw, present := claims["nonce"]
	if !present || raw == nil {
		slog.Debug("oauth: accepting ID token without nonce claim per Pi compatibility policy")
		return nil
	}
	got, ok := raw.(string)
	if !ok || got == "" || got != expected {
		return errors.New("oauth: ID token nonce mismatch")
	}
	return nil
}

func containsScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}
