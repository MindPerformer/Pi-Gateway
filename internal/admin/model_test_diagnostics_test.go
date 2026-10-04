package admin

import (
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"pi-gateway/internal/oauth"
	"pi-gateway/internal/upstream"
)

func TestAccountModelTestHTTPErrorDetails(t *testing.T) {
	for _, protocol := range []string{"sse", "ws"} {
		for _, encoding := range []string{"identity", "gzip", "deflate", "raw-deflate"} {
			t.Run(protocol+"/"+encoding, func(t *testing.T) {
				const body = `{"error":{"code":"model_not_supported","message":"chosen-model is unavailable for this account"},"request_id":"local-request"}`
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if encoding != "identity" {
						w.Header().Set("Content-Encoding", strings.TrimPrefix(encoding, "raw-"))
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					var writer io.WriteCloser
					switch encoding {
					case "gzip":
						writer = gzip.NewWriter(w)
					case "deflate":
						writer = zlib.NewWriter(w)
					case "raw-deflate":
						writer, _ = flate.NewWriter(w, flate.DefaultCompression)
					default:
						_, _ = io.WriteString(w, body)
						return
					}
					_, _ = io.WriteString(writer, body)
					_ = writer.Close()
				}))
				defer target.Close()
				s, a := newModelAdmin(t, target.URL)
				a.UpstreamProtocol = protocol
				if err := s.store.UpdateAccount(context.Background(), a); err != nil {
					t.Fatal(err)
				}
				w, result := modelAdminRequest(t, s.handleTestAccountModel, context.Background(), a.ID, "POST", `{"model":"chosen-model","message":"Hi"}`)
				if w.Code != 502 || result.OK || result.UpstreamStatus != 400 || result.UpstreamBodyTruncated || result.UpstreamEvent != "" || result.Error != "chosen-model is unavailable for this account" || !strings.Contains(result.UpstreamBody, `"model_not_supported"`) || !strings.Contains(result.UpstreamBody, "local-request") {
					t.Fatalf("missing real decoded failure details: %d %s", w.Code, w.Body.String())
				}
				if result.Model != "chosen-model" || result.Output != "" || result.FirstTokenMS != nil || s.accounts.Inflight(a.ID) != 0 {
					t.Fatalf("lost existing result fields or slot: %+v", result)
				}
				var audit string
				if err := s.store.DB().QueryRow("SELECT detail FROM audit_events WHERE action='account.model_test'").Scan(&audit); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(audit, "model_not_supported") || strings.Contains(audit, "local-request") || strings.Contains(audit, "unavailable for this account") || strings.Contains(audit, "upstream_body") {
					t.Fatalf("persisted full diagnostic: %s", audit)
				}
			})
		}
	}
}

func TestAccountModelTestStreamErrorDetails(t *testing.T) {
	for _, protocol := range []string{"sse", "ws"} {
		for _, tc := range []struct{ name, body, event, message string }{
			{"failed", `{"type":"response.failed","response":{"status":"failed","error":{"message":"model access denied","code":"model_access","status":403}}}`, "response.failed", "model access denied"},
			{"error", `{"type":"error","error":{"message":"unsupported reasoning effort","status":400}}`, "error", "unsupported reasoning effort"},
			{"incomplete", `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, "response.incomplete", "max_output_tokens"},
			{"untyped", `{"error":{"message":"raw untyped failure"}}`, "", "raw untyped failure"},
			{"completed_error", `{"type":"response.completed","response":{"status":"completed","error":{"message":"hidden terminal failure"}}}`, "response.completed", "hidden terminal failure"},
		} {
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					const delta = `{"type":"response.output_text.delta","delta":"partial output"}`
					if protocol == "ws" {
						ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
						if err != nil {
							t.Error(err)
							return
						}
						defer ws.Close()
						_, _, _ = ws.ReadMessage()
						_ = ws.WriteMessage(websocket.TextMessage, []byte(delta))
						_ = ws.WriteMessage(websocket.TextMessage, []byte(tc.body))
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write(upstream.FormatSSEFrame([]byte(delta)))
					_, _ = w.Write(upstream.FormatSSEFrame([]byte(tc.body)))
				}))
				defer target.Close()
				s, a := newModelAdmin(t, target.URL)
				a.UpstreamProtocol = protocol
				if err := s.store.UpdateAccount(context.Background(), a); err != nil {
					t.Fatal(err)
				}
				w, result := modelAdminRequest(t, s.handleTestAccountModel, context.Background(), a.ID, "POST", `{"model":"chosen-model","message":"Hi"}`)
				wantStatus := 200
				if protocol == "ws" {
					wantStatus = 101
				}
				if w.Code != 502 || result.UpstreamStatus != wantStatus || result.UpstreamEvent != tc.event || result.Error != tc.message || result.Output != "partial output" || result.FirstTokenMS == nil || result.Model != "chosen-model" || result.UpstreamBodyTruncated {
					t.Fatalf("incorrect stream diagnostic: %d %s", w.Code, w.Body.String())
				}
				var before, after any
				_ = json.Unmarshal([]byte(tc.body), &before)
				if err := json.Unmarshal([]byte(result.UpstreamBody), &after); err != nil {
					t.Fatal(err)
				}
				original, _ := json.Marshal(before)
				received, _ := json.Marshal(after)
				if string(original) != string(received) {
					t.Fatalf("failure body was invented/normalized: got=%s want=%s", received, original)
				}
			})
		}
	}
}

func TestAccountModelTestPlainHTMLAndAbsentBodies(t *testing.T) {
	for _, body := range []string{"", "plain upstream overload", "<html><body>Gateway policy denied this model</body></html>"} {
		t.Run(body, func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503); _, _ = io.WriteString(w, body) }))
			defer target.Close()
			s, a := newModelAdmin(t, target.URL)
			w, result := modelAdminRequest(t, s.handleTestAccountModel, context.Background(), a.ID, "POST", `{"model":"chosen-model","message":"Hi"}`)
			if w.Code != 502 || result.UpstreamStatus != 503 || result.UpstreamBody != body || result.UpstreamBodyTruncated {
				t.Fatalf("altered/fictional error body: %s", w.Body.String())
			}
			if body == "" && strings.Contains(w.Body.String(), `"upstream_body"`) {
				t.Fatal("empty error body should be omitted")
			}
		})
	}
	t.Run("connection_failure", func(t *testing.T) {
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		target.Close()
		s, a := newModelAdmin(t, target.URL)
		w, result := modelAdminRequest(t, s.handleTestAccountModel, context.Background(), a.ID, "POST", `{"model":"chosen-model","message":"Hi"}`)
		if w.Code != 502 || result.UpstreamStatus != 0 || result.UpstreamBody != "" || strings.Contains(w.Body.String(), `"upstream_status"`) {
			t.Fatalf("invented response before headers: %s", w.Body.String())
		}
	})
}

func TestAccountModelTestDiagnosticCredentials(t *testing.T) {
	const body = `{"error":{"message":"diagnostic denied: selected-primary main-refresh main-id secret-codex secret-codex-refresh codex-id proxy-password"},"echo":"selected\u002dprimary","nested":"{\"access_token\":\"nested-unknown\",\"message\":\"Bearer nested-bearer\"}","access_token":"unknown-access","refreshToken":"unknown-refresh","password":{"value":"unknown-password"},"Authorization":"Basic unknown-basic","api_key":"unknown-key","escaped_key":{"\u0074oken":"unknown-escaped"},"detail":"Bearer loose-unknown","headers":{"X-Request-Debug":"nonsecret-response-field"}}`
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400); _, _ = io.WriteString(w, body) }))
	defer target.Close()
	s, a := newModelAdmin(t, target.URL)
	var hits atomic.Int64
	proxy := localForwardProxy(t, target.URL, &hits, false)
	a.ProxyURL = strings.Replace(proxy.URL, "://", "://proxy-user:proxy-password@", 1)
	if err := s.store.UpdateAccount(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.DB().Exec("UPDATE accounts SET refresh_token=?, id_token=?, codex_id_token=? WHERE id=?", "main-refresh", "main-id", "codex-id", a.ID); err != nil {
		t.Fatal(err)
	}
	w, result := modelAdminRequest(t, s.handleTestAccountModel, context.Background(), a.ID, "POST", `{"model":"chosen-model","message":"Hi"}`)
	if w.Code != 502 || result.UpstreamStatus != 400 || !strings.Contains(result.Error, "diagnostic denied") || !strings.Contains(result.UpstreamBody, "nonsecret-response-field") || hits.Load() != 1 {
		t.Fatalf("diagnostics or proxy were lost: %s", w.Body.String())
	}
	for _, secret := range []string{"selected-primary", `selected\u002dprimary`, "main-refresh", "main-id", "secret-codex", "secret-codex-refresh", "codex-id", "proxy-password", "nested-unknown", "nested-bearer", "unknown-access", "unknown-refresh", "unknown-password", "unknown-basic", "unknown-key", "unknown-escaped", "loose-unknown"} {
		if strings.Contains(result.UpstreamBody, secret) || strings.Contains(result.Error, secret) {
			t.Fatalf("credential disclosed: %q in %s", secret, w.Body.String())
		}
	}
	if strings.Contains(w.Body.String(), "model-test-pi") || strings.Contains(w.Body.String(), "admin-test-") {
		t.Fatal("raw request headers disclosed")
	}
}

func TestAccountModelTestDiagnosticBodyLimit(t *testing.T) {
	for _, body := range []string{
		strings.Repeat("x", maxModelTestDiagnostic-6) + "selected-primary" + strings.Repeat("y", 2000),
		strings.Repeat("界", maxModelTestDiagnostic),
		`{"access_token":"` + strings.Repeat("sensitive", 200000) + `"}`,
	} {
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400); _, _ = io.WriteString(w, body) }))
		s, a := newModelAdmin(t, target.URL)
		w, result := modelAdminRequest(t, s.handleTestAccountModel, context.Background(), a.ID, "POST", `{"model":"chosen-model","message":"Hi"}`)
		target.Close()
		if w.Code != 502 || !result.UpstreamBodyTruncated || len(result.UpstreamBody) > maxModelTestDiagnostic || len(result.Error) > maxModelTestDiagnostic || !utf8.ValidString(result.UpstreamBody) {
			t.Fatalf("unsafe diagnostic bounds: status=%d bodylen=%d errorlen=%d truncated=%v", w.Code, len(result.UpstreamBody), len(result.Error), result.UpstreamBodyTruncated)
		}
		if strings.Contains(result.UpstreamBody, "select") || strings.Contains(result.UpstreamBody, "sensitive") || strings.Contains(result.Error, "select") || strings.Contains(result.Error, "sensitive") {
			t.Fatal("truncation exposed a credential fragment")
		}
	}
}

type modelTestRoundTripper func(*http.Request) (*http.Response, error)

func (f modelTestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAccountModelTestFreshCredentialRedaction(t *testing.T) {
	var refreshes, calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/accounts/oauth/token" {
			refreshes.Add(1)
			fmt.Fprint(w, `{"access_token":"fresh-access","refresh_token":"fresh-refresh","expires_in":3600,"scope":"chatgpt.tokens.use.direct"}`)
			return
		}
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fresh-access" {
			t.Errorf("wrong fresh credential")
		}
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":{"message":"fresh failure: fresh-access fresh-refresh selected-primary old-refresh"}}`)
	}))
	defer target.Close()
	s, a := newModelAdmin(t, target.URL)
	if _, err := s.store.DB().Exec("UPDATE accounts SET expires_at=1, refresh_token=?, oauth_client_id=? WHERE id=?", "old-refresh", "local-issued", a.ID); err != nil {
		t.Fatal(err)
	}
	client, err := s.factory.HTTPClient("")
	if err != nil {
		t.Fatal(err)
	}
	base := client.Transport
	local, _ := url.Parse(target.URL)
	client.Transport = modelTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		clone := r.Clone(r.Context())
		endpoint := *r.URL
		if endpoint.String() == oauth.ChatGPTTokenURL {
			endpoint.Scheme, endpoint.Host = local.Scheme, local.Host
		}
		if endpoint.Host != local.Host {
			return nil, fmt.Errorf("test rejects non-local endpoint")
		}
		clone.URL, clone.Host = &endpoint, local.Host
		return base.RoundTrip(clone)
	})
	w, result := modelAdminRequest(t, s.handleTestAccountModel, context.Background(), a.ID, "POST", `{"model":"chosen-model","message":"Hi"}`)
	if w.Code != 502 || refreshes.Load() != 1 || calls.Load() != 1 || !strings.Contains(result.Error, "fresh failure") {
		t.Fatalf("local refresh not exercised: %s refresh=%d calls=%d", w.Body.String(), refreshes.Load(), calls.Load())
	}
	for _, secret := range []string{"fresh-access", "fresh-refresh", "selected-primary", "old-refresh"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("old/fresh credential leaked: %q", secret)
		}
	}
}

func TestModelTestRedactorEscapesAndBoundaries(t *testing.T) {
	redactor := &modelTestRedactor{}
	redactor.add("selected-primary")
	redactor.add(`quote"slash\\credential`)
	for _, tc := range []struct {
		name, body string
		clipped    bool
		forbidden  []string
	}{
		{"unwrapped_json", `rejected: {\"token\":\"unknown-token-value\",\"message\":\"selected\u002dprimary\"}`, false, []string{"unknown-token-value", "selected", "primary"}},
		{"escaped_plaintext", `rejected selected\u002dprimary`, false, []string{"selected", "primary"}},
		{"nested_strings", `{"detail":"{\"detail\":\"{\\\"password\\\":\\\"nested-password-value\\\"}\"}"}`, false, []string{"nested-password-value"}},
		{"plaintext_fields", "password=unknown password with spaces\nAuthorization: Basic dXNlcjpwYXNz\nBearer arbitrary-bearer\nhttp://user:arbitrary-proxy@localhost/", false, []string{"unknown", "with spaces", "dXNlcjpwYXNz", "arbitrary-bearer", "arbitrary-proxy"}},
		{"escaped_known", `{"detail":"quote\"slash\\\\credential"}`, false, []string{"credential", "slash"}},
		{"clipped_known", "upstream rejected select", true, []string{"select"}},
		{"clipped_unicode", `{"detail":"selected\u002dpri`, true, []string{"selected", "pri"}},
		{"escaped_key_fragment", `{"\u0074oken":"unknown-secret`, true, []string{"unknown-secret"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clean, clipped := redactor.bounded(tc.body, maxModelTestDiagnostic, tc.clipped)
			if clipped != tc.clipped || !strings.Contains(clean, "REDACTED") {
				t.Fatalf("missing redaction/truncation: %q %v", clean, clipped)
			}
			for _, secret := range tc.forbidden {
				if strings.Contains(clean, secret) {
					t.Fatalf("escaped secret leaked: %q in %q", secret, clean)
				}
			}
		})
	}
}

func TestAccountModelTestNamedSSEAndFallbackDiagnostic(t *testing.T) {
	for _, named := range []bool{false, true} {
		t.Run(fmt.Sprint(named), func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if websocket.IsWebSocketUpgrade(r) {
					w.WriteHeader(503)
					fmt.Fprint(w, "old handshake body must not replace the SSE failure")
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if named {
					fmt.Fprint(w, "event: error\ndata: {\"message\":\"named error detail\"}\n\n")
				} else {
					fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
				}
			}))
			defer target.Close()
			s, a := newModelAdmin(t, target.URL)
			runtime := s.settings.Get()
			runtime.UpstreamTransport = "auto"
			if err := s.settings.Set(context.Background(), runtime); err != nil {
				t.Fatal(err)
			}
			w, result := modelAdminRequest(t, s.handleTestAccountModel, context.Background(), a.ID, "POST", `{"model":"chosen-model","message":"Hi"}`)
			if w.Code != 502 || result.UpstreamStatus != 200 || result.Transport != "sse" || strings.Contains(result.UpstreamBody, "handshake") {
				t.Fatalf("stale fallback diagnostic: %s", w.Body.String())
			}
			if named {
				if result.UpstreamEvent != "error" || result.Error != "named error detail" || strings.Contains(result.UpstreamBody, `"type"`) {
					t.Fatalf("named SSE error normalized: %s", w.Body.String())
				}
			} else if result.UpstreamBody != "" {
				t.Fatalf("invented body on stream EOF: %s", w.Body.String())
			}
		})
	}
}

func TestAccountModelTestWSClippedHandshake(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.(http.Flusher).Flush() // Unknown length, so Gorilla's 1024-byte cap matters.
		_, _ = io.WriteString(w, strings.Repeat("x", 1018)+"selected-primary"+strings.Repeat("y", 4096))
	}))
	defer target.Close()
	s, a := newModelAdmin(t, target.URL)
	a.UpstreamProtocol = "ws"
	if err := s.store.UpdateAccount(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	w, result := modelAdminRequest(t, s.handleTestAccountModel, context.Background(), a.ID, "POST", `{"model":"chosen-model","message":"Hi"}`)
	if w.Code != 502 || result.UpstreamStatus != 400 || !result.UpstreamBodyTruncated || strings.Contains(result.UpstreamBody, "select") || strings.Contains(result.Error, "select") {
		t.Fatalf("handshake prefix leaked or not marked: %s", w.Body.String())
	}
}
