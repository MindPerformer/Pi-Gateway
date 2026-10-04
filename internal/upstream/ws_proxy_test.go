package upstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pi-gateway/internal/egress"

	"github.com/gorilla/websocket"
)

type wsProxyFixture struct {
	server   *httptest.Server
	requests chan *http.Request
	connects atomic.Int32
}

// This is a real TCP CONNECT forwarder; it does not terminate the target TLS.
func newWSProxyFixture(t *testing.T, secure bool, forwardAddr string) *wsProxyFixture {
	t.Helper()
	fixture := &wsProxyFixture{requests: make(chan *http.Request, 32)}
	var handlers sync.WaitGroup
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		if r.Method != http.MethodConnect {
			t.Errorf("proxy received %s, want CONNECT", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		fixture.connects.Add(1)
		fixture.requests <- r
		addr := r.Host
		if forwardAddr != "" {
			addr = forwardAddr
		}
		target, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			http.Error(w, "target unavailable", http.StatusBadGateway)
			return
		}
		defer target.Close()
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(target, rw)
			_ = target.Close()
			close(done)
		}()
		_, _ = io.Copy(conn, target)
		_ = conn.Close()
		<-done
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	if secure {
		server.StartTLS()
	} else {
		server.Start()
	}
	fixture.server = server
	t.Cleanup(func() {
		server.Close()
		handlers.Wait()
	})
	return fixture
}

func newWSProxyTarget(t *testing.T, secure bool) (*httptest.Server, *atomic.Int32, <-chan http.Header, <-chan []byte) {
	t.Helper()
	handshakes := &atomic.Int32{}
	headers := make(chan http.Header, 32)
	frames := make(chan []byte, 32)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handshakes.Add(1)
		headers <- r.Header.Clone()
		socket, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer socket.Close()
		for {
			_ = socket.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, payload, err := socket.ReadMessage()
			if err != nil {
				return
			}
			frames <- payload
			if err := socket.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp-proxy","status":"completed","output":[]}}`)); err != nil {
				return
			}
		}
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	if secure {
		server.StartTLS()
	} else {
		server.Start()
	}
	t.Cleanup(server.Close)
	return server, handshakes, headers, frames
}

func wsProxyURL(t *testing.T, endpoint, password string) string {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("proxy-user", password)
	return u.String()
}

func TestWSProxyRoundTripAndAuthenticationIsolation(t *testing.T) {
	for _, scheme := range []string{"direct", "http", "https"} {
		for _, secureTarget := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wss=%t", scheme, secureTarget), func(t *testing.T) {
				target, handshakes, headers, frames := newWSProxyTarget(t, secureTarget)
				roots := x509.NewCertPool()
				if secureTarget {
					roots.AddCert(target.Certificate())
				}
				var proxy *wsProxyFixture
				rawProxy := ""
				if scheme != "direct" {
					proxy = newWSProxyFixture(t, scheme == "https", "")
					rawProxy = wsProxyURL(t, proxy.server.URL, "p@ss:word")
					if scheme == "https" {
						roots.AddCert(proxy.server.Certificate())
					}
				}
				c := New(Config{WSURL: "ws" + strings.TrimPrefix(target.URL, "http") + "/responses?route=proxy", ConnectTimeout: time.Second}, egress.NewFactory(egress.Options{RootCAs: roots}))
				t.Cleanup(c.Close)
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				events := 0
				res, err := c.Stream(ctx, &Request{
					ProxyURL: rawProxy, Transport: "websocket", Body: []byte(`{"model":"test","input":[]}`),
					WSHeaders: http.Header{
						"Authorization":       []string{"Bearer upstream-only"},
						"pRoXy-AuThOrIzAtIoN": []string{"Basic must-not-reach-target"},
					},
				}, func(event *Event) error {
					if event.Type != "response.completed" {
						t.Errorf("unexpected event %q", event.Type)
					}
					events++
					return nil
				})
				if err != nil || res == nil || res.Status != http.StatusSwitchingProtocols || events != 1 || handshakes.Load() != 1 {
					t.Fatalf("WS round trip failed: result=%+v err=%v events=%d handshakes=%d", res, err, events, handshakes.Load())
				}
				header := <-headers
				if header.Get("Authorization") != "Bearer upstream-only" || header.Get("Proxy-Authorization") != "" {
					t.Fatal("upstream/proxy authorization boundaries were not preserved")
				}
				var frame map[string]any
				if err := json.Unmarshal(<-frames, &frame); err != nil || frame["type"] != "response.create" || frame["model"] != "test" {
					t.Fatalf("wrong tunneled WS frame: %v %v", frame, err)
				}
				if proxy != nil {
					r := <-proxy.requests
					u, _ := url.Parse(rawProxy)
					if r.Host != target.Listener.Addr().String() || r.Header.Get("Proxy-Authorization") != egress.BasicAuthHeader(u) || r.Header.Get("Authorization") != "" {
						t.Fatal("CONNECT target or authentication incorrect")
					}
					if scheme == "https" && r.TLS == nil {
						t.Fatal("HTTPS proxy CONNECT used plaintext")
					}
				}
			})
		}
	}
}

func TestWSHTTPSProxyStillVerifiesTargetCertificate(t *testing.T) {
	target, handshakes, _, _ := newWSProxyTarget(t, true)
	proxy := newWSProxyFixture(t, true, target.Listener.Addr().String())
	roots := x509.NewCertPool()
	roots.AddCert(proxy.server.Certificate())
	roots.AddCert(target.Certificate())
	_, port, _ := net.SplitHostPort(target.Listener.Addr().String())
	c := New(Config{WSURL: "wss://" + net.JoinHostPort("wrong-target.invalid", port)}, egress.NewFactory(egress.Options{RootCAs: roots}))
	t.Cleanup(c.Close)
	conn, _, err := c.dialWS(context.Background(), &Request{ProxyURL: wsProxyURL(t, proxy.server.URL, "private-password")})
	if conn != nil {
		conn.Close()
		t.Fatal("target with wrong certificate was accepted")
	}
	var certErr *tls.CertificateVerificationError
	var hostnameErr x509.HostnameError
	if !errors.As(err, &certErr) || !errors.As(err, &hostnameErr) || proxy.connects.Load() != 1 || handshakes.Load() != 0 {
		t.Fatalf("target verification bypassed: error=%v connects=%d handshakes=%d", err, proxy.connects.Load(), handshakes.Load())
	}
	if strings.Contains(err.Error(), "private-password") {
		t.Fatal("target TLS error contains proxy password")
	}
}

func TestWSHTTPSProxyPoolFingerprintIsolation(t *testing.T) {
	target, handshakes, _, _ := newWSProxyTarget(t, true)
	proxyA := newWSProxyFixture(t, true, "")
	proxyB := newWSProxyFixture(t, true, "")
	roots := x509.NewCertPool()
	roots.AddCert(target.Certificate())
	roots.AddCert(proxyA.server.Certificate())
	roots.AddCert(proxyB.server.Certificate())
	c := New(Config{WSURL: "wss" + strings.TrimPrefix(target.URL, "https"), Pool: true, ConnectTimeout: time.Second, IdleTimeout: 5 * time.Second}, egress.NewFactory(egress.Options{RootCAs: roots}))
	t.Cleanup(c.Close)
	a1 := wsProxyURL(t, proxyA.server.URL, "password-one")
	a2 := wsProxyURL(t, proxyA.server.URL, "password-two")
	b := wsProxyURL(t, proxyB.server.URL, "password-one")
	// Same session/account, switching exit and credentials, with direct mixed in.
	for _, proxyURL := range []string{a1, a1, b, b, "", "", a2, a2, a1} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := c.Stream(ctx, &Request{
			Transport: "websocket-cached", ProxyURL: proxyURL, SessionID: "proxy-session", PoolSessionID: "proxy-internal", AccountID: 17, ClientKeyID: 1,
			Body: []byte(`{"model":"test","input":[]}`),
		}, func(*Event) error { return nil })
		cancel() // A finished handshake/request must not close its pooled socket.
		if err != nil {
			t.Fatal(err)
		}
	}
	if handshakes.Load() != 4 || proxyA.connects.Load() != 2 || proxyB.connects.Load() != 1 || c.PoolSize() != 4 {
		t.Fatalf("exit or credentials reused incorrectly: handshakes=%d proxyA=%d proxyB=%d pool=%d", handshakes.Load(), proxyA.connects.Load(), proxyB.connects.Load(), c.PoolSize())
	}
	c.pool.mu.Lock()
	defer c.pool.mu.Unlock()
	for _, proxyURL := range []string{a1, a2, b, ""} {
		key := c.requestPoolKey(&Request{ClientKeyID: 1, SessionID: "proxy-session", PoolSessionID: "proxy-internal", AccountID: 17, ProxyURL: proxyURL})
		if c.pool.entries[key] == nil {
			t.Fatal("proxy fingerprint missing from connection pool key")
		}
	}
	for key := range c.pool.entries {
		if strings.Contains(key, "password-") || strings.Contains(key, "proxy-user") || strings.Contains(key, "https://") {
			t.Fatal("raw proxy credentials exposed in pool key")
		}
	}
}

func TestWSHTTPSProxyHandshakeDeadlineAndCancellation(t *testing.T) {
	for _, phase := range []string{"target-tls", "ws-upgrade", "wss-upgrade"} {
		for _, mode := range []string{"handshake-timeout", "context-deadline", "cancel"} {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				started, closed := make(chan struct{}), make(chan struct{})
				roots := x509.NewCertPool()
				var targetURL string
				if phase == "target-tls" {
					listener, err := net.Listen("tcp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = listener.Close() })
					targetURL = "wss://" + listener.Addr().String()
					go func() {
						conn, err := listener.Accept()
						if err != nil {
							return
						}
						defer conn.Close()
						defer close(closed)
						close(started)
						_, _ = io.Copy(io.Discard, conn)
					}()
				} else {
					server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						defer conn.Close()
						defer close(closed)
						close(started)
						_, _ = io.Copy(io.Discard, conn)
					}))
					server.Config.ErrorLog = log.New(io.Discard, "", 0)
					if phase == "wss-upgrade" {
						server.StartTLS()
						roots.AddCert(server.Certificate())
					} else {
						server.Start()
					}
					t.Cleanup(server.Close)
					targetURL = "ws" + strings.TrimPrefix(server.URL, "http")
				}
				proxy := newWSProxyFixture(t, true, "")
				roots.AddCert(proxy.server.Certificate())
				handshakeTimeout := 3 * time.Second
				ctx, cancel := context.WithCancel(context.Background())
				if mode == "handshake-timeout" {
					handshakeTimeout = 150 * time.Millisecond
				} else if mode == "context-deadline" {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 150*time.Millisecond)
				}
				defer cancel()
				c := New(Config{WSURL: targetURL, ConnectTimeout: handshakeTimeout}, egress.NewFactory(egress.Options{RootCAs: roots}))
				t.Cleanup(c.Close)
				result := make(chan error, 1)
				go func() {
					conn, _, err := c.dialWS(ctx, &Request{ProxyURL: proxy.server.URL})
					if conn != nil {
						conn.Close()
					}
					result <- err
				}()
				select {
				case <-started:
				case err := <-result:
					t.Fatalf("handshake failed before %s: %v", phase, err)
				case <-time.After(2 * time.Second):
					t.Fatal("handshake did not reach target")
				}
				if mode == "cancel" {
					cancel()
				}
				select {
				case err := <-result:
					var timeout net.Error
					if mode == "cancel" {
						if !errors.Is(err, context.Canceled) {
							t.Fatalf("cancellation not preserved: %v", err)
						}
					} else if !errors.Is(err, context.DeadlineExceeded) && !(errors.As(err, &timeout) && timeout.Timeout()) {
						t.Fatalf("handshake timeout not preserved: %v", err)
					}
				case <-time.After(time.Second):
					t.Fatal("handshake cancellation/deadline did not close tunnel")
				}
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("aborted handshake leaked target connection")
				}
			})
		}
	}
}
