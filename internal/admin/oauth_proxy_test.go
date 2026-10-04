package admin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pi-gateway/internal/accounts"
	"pi-gateway/internal/oauth"
	"pi-gateway/internal/store"
)

type rejectSharedOAuthTransport struct{}

func (rejectSharedOAuthTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("shared OAuth transport must not be used for per-flow egress")
}

func localOAuthJWT(claims map[string]any) string {
	data, _ := json.Marshal(claims)
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(data) + ".sig"
}

// The CONNECT destination is always a local TLS stub, never the requested
// public OAuth host. This proves real factory/proxy traffic without credentials
// or socket connections reaching an external service.
func localOAuthTunnel(t *testing.T, target string, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	targetURL, _ := url.Parse(target)
	var tunnels sync.WaitGroup
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tunnels.Add(1)
		defer tunnels.Done()
		if r.Method != http.MethodConnect || (r.Host != "auth.openai.com:443" && r.Host != "auth0.openai.com:443") {
			http.Error(w, "test only permits OAuth CONNECT", 403)
			return
		}
		remote, err := net.DialTimeout("tcp", targetURL.Host, time.Second)
		if err != nil {
			http.Error(w, "local connect failed", 502)
			return
		}
		downstream, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			remote.Close()
			return
		}
		hits.Add(1)
		_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffer.Flush()
		copyDone := make(chan struct{})
		go func() {
			defer close(copyDone)
			_, _ = io.Copy(remote, buffer)
			remote.Close()
		}()
		_, _ = io.Copy(downstream, remote)
		downstream.Close()
		<-copyDone
	}))
	t.Cleanup(func() {
		server.Close()
		tunnels.Wait() // httptest.Close itself cannot wait for hijacked connections.
	})
	return server
}

func TestOAuthFlowKeepAndExplicitProxyUseRealTokenTransport(t *testing.T) {
	for _, kind := range []string{oauth.FlowKindCodex, oauth.FlowKindChatGPT} {
		for _, choice := range []string{"keep_saved", "keep_manual", "direct_id", "direct_url", "manual", "saved"} {
			t.Run(kind+"/"+choice, func(t *testing.T) {
				ctx := context.Background()
				var tokenCalls, directDials, firstDials, secondDials atomic.Int64
				tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					tokenCalls.Add(1)
					if r.Method != "POST" {
						t.Error("token request was not POST")
					}
					if err := r.ParseForm(); err != nil {
						t.Error(err)
						return
					}
					if r.Form.Get("grant_type") != "authorization_code" && r.Form.Get("grant_type") != "refresh_token" {
						t.Errorf("unexpected grant: %q", r.Form.Get("grant_type"))
					}
					access := "local-chatgpt-access"
					if r.Form.Get("client_id") == oauth.ClientID {
						access = localOAuthJWT(map[string]any{oauth.JWTClaimPath: map[string]any{"chatgpt_account_id": "local-codex-account"}})
					}
					writeJSON(w, 200, map[string]any{"access_token": access, "refresh_token": "local-refresh", "id_token": localOAuthJWT(map[string]any{"email": "local@example.test"}), "expires_in": 3600, "scope": oauth.ChatGPTDirectTokenScope})
				}))
				t.Cleanup(tokenServer.Close)
				h := newProxyHarness(t, tokenServer.URL, "sse")
				first := localOAuthTunnel(t, tokenServer.URL, &firstDials)
				second := localOAuthTunnel(t, tokenServer.URL, &secondDials)
				target, _ := url.Parse(tokenServer.URL)
				// Even explicit direct test traffic is forcibly routed to the local stub.
				// Any other non-loopback dial is rejected instead of reaching the Internet.
				h.factory.BaseDialer = func(ctx context.Context, network, address string) (net.Conn, error) {
					if address == "auth.openai.com:443" || address == "auth0.openai.com:443" {
						directDials.Add(1)
						address = target.Host
					}
					host, _, err := net.SplitHostPort(address)
					if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
						return nil, errors.New("test rejected non-local network destination")
					}
					return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
				}
				roots := x509.NewCertPool()
				roots.AddCert(tokenServer.Certificate())
				serverName := "127.0.0.1"
				if len(tokenServer.Certificate().DNSNames) > 0 {
					serverName = tokenServer.Certificate().DNSNames[0]
				}
				for _, endpoint := range []string{"", first.URL, second.URL} {
					client, err := h.factory.HTTPClient(endpoint)
					if err != nil {
						t.Fatal(err)
					}
					client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: serverName, MinVersion: tls.VersionTLS12}
					t.Cleanup(client.CloseIdleConnections)
				}
				shared := oauth.NewClient(&http.Client{Transport: rejectSharedOAuthTransport{}})
				manager := accounts.New(h.db, shared, h.factory, accounts.Options{Logger: h.admin.logger})
				h.admin.accounts = manager
				h.account.AccountID = "chatgpt:local@example.test"
				h.account.OAuthClientID = "issued-local-client"
				h.account.RefreshToken = "local-refresh"
				if err := h.db.UpdateAccount(ctx, h.account); err != nil {
					t.Fatal(err)
				}
				oldProxy := &store.Proxy{Name: "existing", URL: first.URL}
				newProxy := &store.Proxy{Name: "selected", URL: second.URL}
				for _, p := range []*store.Proxy{oldProxy, newProxy} {
					if err := h.db.CreateProxy(ctx, p); err != nil {
						t.Fatal(err)
					}
				}
				oldID := oldProxy.ID
				if choice == "keep_manual" {
					oldID = 0
				}
				if err := h.db.SetAccountProxy(ctx, h.account.ID, oldID, first.URL); err != nil {
					t.Fatal(err)
				}
				flows := oauth.NewFlowManager(shared, oauth.FlowOptions{
					CallbackHost: "127.0.0.1", CallbackPort: 0, Timeout: time.Second, DeviceID: oauth.NewDeviceUUID, Logger: h.admin.logger,
					OnComplete: func(flow *oauth.Flow, token *oauth.Token) error {
						if flow.Kind == oauth.FlowKindCodex {
							a, err := h.db.GetAccount(ctx, h.account.ID)
							if err != nil {
								return err
							}
							if err := manager.LinkCodexCredential(ctx, a, token); err != nil {
								return err
							}
							flow.DBAccountID = a.ID
							return nil
						}
						a, err := manager.CreateFromToken(ctx, "reauthorized", token, flow.Meta["proxy_url"])
						if err == nil {
							flow.DBAccountID = a.ID
						}
						return err
					},
				})
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := flows.Close(ctx); err != nil {
						t.Errorf("close OAuth flow manager: %v", err)
					}
				})
				h.admin.flows = flows
				h.admin.bindOAuthProxy()
				payload := map[string]any{"kind": kind, "account_id": strconv.FormatInt(h.account.ID, 10)}
				expectedURL := first.URL
				switch choice {
				case "direct_id":
					payload["proxy_id"] = nil
					expectedURL = ""
				case "direct_url":
					payload["proxy_url"] = ""
					expectedURL = ""
				case "manual":
					payload["proxy_id"] = nil
					payload["proxy_url"] = second.URL
					expectedURL = second.URL
				case "saved":
					payload["proxy_id"] = newProxy.ID
					payload["proxy_url"] = "ignored-invalid-url"
					expectedURL = second.URL
				}
				raw, _ := json.Marshal(payload)
				started := h.request(t, "POST", "/api/oauth/start", string(raw), 200, true)["flow"].(map[string]any)
				flowID := started["id"].(string)
				if got := flows.Get(flowID).Meta["proxy_url"]; got != expectedURL {
					t.Fatalf("wrong effective flow proxy: %q want %q", got, expectedURL)
				}
				authorize, _ := url.Parse(started["auth_url"].(string))
				callback, _ := url.Parse(started["redirect_uri"].(string))
				query := callback.Query()
				query.Set("code", "local-code")
				query.Set("state", authorize.Query().Get("state"))
				query.Set("client_id", "issued-local-client")
				callback.RawQuery = query.Encode()
				input, _ := json.Marshal(map[string]string{"input": callback.String()})
				completed := h.request(t, "POST", "/api/oauth/"+flowID+"/complete", string(input), 200, true)
				finished := completed["flow"].(map[string]any)
				if finished["status"] != oauth.FlowCompleted {
					t.Fatalf("OAuth failed: %+v", finished)
				}
				if finished["db_account_id"] != float64(h.account.ID) {
					t.Fatalf("completed flow lost persisted account id: %+v", finished)
				}
				a, err := h.db.GetAccount(ctx, h.account.ID)
				if err != nil {
					t.Fatal(err)
				}
				if a.ProxyURL != expectedURL {
					t.Fatalf("completion persisted wrong proxy: %q want %q", a.ProxyURL, expectedURL)
				}
				if strings.HasPrefix(choice, "keep") && ((oldID == 0 && a.ProxyID != nil) || (oldID > 0 && (a.ProxyID == nil || *a.ProxyID != oldID))) {
					t.Fatal("keep altered existing proxy binding")
				}
				// Refresh must use exactly the same selected exit, not the shared direct client.
				if kind == oauth.FlowKindCodex {
					if err := h.db.SaveCodexCredential(ctx, a.ID, a.CodexAccessToken, a.CodexRefreshToken, a.CodexIDToken, 1, a.CodexAccountID); err != nil {
						t.Fatal(err)
					}
					a, err = h.db.GetAccount(ctx, a.ID)
					if err != nil {
						t.Fatal(err)
					}
					if _, _, _, err := manager.EnsureFreshCodexToken(ctx, a); err != nil {
						t.Fatal(err)
					}
				} else if err := manager.Refresh(ctx, a); err != nil {
					t.Fatal(err)
				}
				if tokenCalls.Load() != 2 {
					t.Fatalf("exchange and refresh did not both reach local token server: %d", tokenCalls.Load())
				}
				switch expectedURL {
				case first.URL:
					if firstDials.Load() < 1 || secondDials.Load() != 0 || directDials.Load() != 0 {
						t.Fatalf("keep bypassed old proxy: old=%d new=%d direct=%d", firstDials.Load(), secondDials.Load(), directDials.Load())
					}
				case second.URL:
					if secondDials.Load() < 1 || firstDials.Load() != 0 || directDials.Load() != 0 {
						t.Fatalf("explicit proxy choice ignored: old=%d new=%d direct=%d", firstDials.Load(), secondDials.Load(), directDials.Load())
					}
				case "":
					if directDials.Load() < 1 || firstDials.Load() != 0 || secondDials.Load() != 0 {
						t.Fatalf("explicit direct inherited a proxy: old=%d new=%d direct=%d", firstDials.Load(), secondDials.Load(), directDials.Load())
					}
				}
			})
		}
	}
}

func TestOAuthKeepRejectsMissingAccountAndRefreshNeverFallsBackToDirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid proxy must not make a token request") }))
	defer target.Close()
	h := newProxyHarness(t, target.URL, "sse")
	h.request(t, "POST", "/api/oauth/start", `{"kind":"codex","account_id":"999999"}`, 404, true)
	h.request(t, "POST", "/api/oauth/start", `{"kind":"codex","account_id":"not-an-id"}`, 400, true)
	var dials atomic.Int64
	h.factory.BaseDialer = func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("test blocks all network")
	}
	a := *h.account
	a.ProxyURL = "invalid-proxy"
	a.OAuthClientID = "issued-local-client"
	a.RefreshToken = "local-refresh"
	if err := h.admin.accounts.Refresh(context.Background(), &a); err == nil {
		t.Fatal("invalid proxy refresh unexpectedly succeeded")
	}
	if dials.Load() != 0 {
		t.Fatalf("invalid proxy fell back to a network dial: %d", dials.Load())
	}
	if !strings.Contains(a.LastError, "proxy") {
		t.Fatalf("missing proxy error: %q", a.LastError)
	}
	a.CodexAccessToken = "local-codex-access"
	a.CodexRefreshToken = "local-codex-refresh"
	a.CodexAccountID = "local-codex-account"
	a.CodexExpiresAt = 1
	if _, _, _, err := h.admin.accounts.EnsureFreshCodexToken(context.Background(), &a); err == nil || !strings.Contains(err.Error(), "proxy") {
		t.Fatalf("invalid proxy Codex refresh did not fail closed: %v", err)
	}
	if dials.Load() != 0 {
		t.Fatalf("invalid proxy Codex refresh fell back to a network dial: %d", dials.Load())
	}
}
