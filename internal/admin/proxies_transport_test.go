package admin

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
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

	"pi-gateway/internal/oauth"
	"pi-gateway/internal/store"
)

// This tiny SOCKS server is test-only: production uses the existing egress
// factory and golang.org/x/net/proxy. It refuses every non-local test target.
func localSOCKSProxy(t *testing.T, target string, hits *atomic.Int64) string {
	t.Helper()
	targetURL, _ := url.Parse(target)
	targetHost, targetPort, _ := net.SplitHostPort(targetURL.Host)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	active := map[net.Conn]struct{}{}
	var workers sync.WaitGroup
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		for conn := range active {
			conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			active[conn] = struct{}{}
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				defer func() { mu.Lock(); delete(active, conn); mu.Unlock() }()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				header := make([]byte, 2)
				if _, err := io.ReadFull(conn, header); err != nil || header[0] != 5 {
					return
				}
				methods := make([]byte, int(header[1]))
				if _, err := io.ReadFull(conn, methods); err != nil {
					return
				}
				hasAuth := false
				for _, method := range methods {
					if method == 2 {
						hasAuth = true
					}
				}
				if !hasAuth {
					_, _ = conn.Write([]byte{5, 255})
					return
				}
				_, _ = conn.Write([]byte{5, 2})
				if _, err := io.ReadFull(conn, header); err != nil || header[0] != 1 {
					return
				}
				username := make([]byte, int(header[1]))
				if _, err := io.ReadFull(conn, username); err != nil {
					return
				}
				length := make([]byte, 1)
				if _, err := io.ReadFull(conn, length); err != nil {
					return
				}
				password := make([]byte, int(length[0]))
				if _, err := io.ReadFull(conn, password); err != nil {
					return
				}
				if string(username) != "local-user" || string(password) != "local-secret" {
					_, _ = conn.Write([]byte{1, 1})
					return
				}
				_, _ = conn.Write([]byte{1, 0})
				connect := make([]byte, 4)
				if _, err := io.ReadFull(conn, connect); err != nil || connect[0] != 5 || connect[1] != 1 {
					return
				}
				host := ""
				switch connect[3] {
				case 1:
					data := make([]byte, 4)
					if _, err := io.ReadFull(conn, data); err != nil {
						return
					}
					host = net.IP(data).String()
				case 3:
					if _, err := io.ReadFull(conn, length); err != nil {
						return
					}
					data := make([]byte, int(length[0]))
					if _, err := io.ReadFull(conn, data); err != nil {
						return
					}
					host = string(data)
				case 4:
					data := make([]byte, 16)
					if _, err := io.ReadFull(conn, data); err != nil {
						return
					}
					host = net.IP(data).String()
				default:
					return
				}
				port := make([]byte, 2)
				if _, err := io.ReadFull(conn, port); err != nil {
					return
				}
				if host != targetHost || strconv.Itoa(int(binary.BigEndian.Uint16(port))) != targetPort {
					return
				}
				remote, err := net.DialTimeout("tcp", targetURL.Host, time.Second)
				if err != nil {
					return
				}
				defer remote.Close()
				hits.Add(1)
				_, _ = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
				done := make(chan struct{})
				go func() { _, _ = io.Copy(remote, conn); remote.Close(); close(done) }()
				_, _ = io.Copy(conn, remote)
				conn.Close()
				<-done
			}()
		}
	}()
	return listener.Addr().String()
}

func TestSavedProxyAuthenticatedSOCKS5AndSOCKS5H(t *testing.T) {
	var targetHits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("probe target received credentials")
		}
		w.WriteHeader(204)
	}))
	defer target.Close()
	h := newProxyHarness(t, target.URL, "sse")
	var socksHits atomic.Int64
	address := localSOCKSProxy(t, target.URL, &socksHits)
	for _, scheme := range []string{"socks5", "socks5h"} {
		endpoint := scheme + "://local-user:local-secret@" + address
		result := h.request(t, "POST", "/api/proxies", proxyPayload(scheme, endpoint), 201, true)
		id := savedProxyID(result)
		test := h.request(t, "POST", fmt.Sprintf("/api/proxies/%d/test", id), "", 200, true)["test"].(map[string]any)
		if test["success"] != true || test["status"] != float64(204) {
			t.Fatalf("%s did not reach fake upstream through authenticated SOCKS: %+v", scheme, test)
		}
	}
	if socksHits.Load() != 2 || targetHits.Load() != 2 {
		t.Fatalf("SOCKS or target bypassed: proxy=%d target=%d", socksHits.Load(), targetHits.Load())
	}
}

func TestSavedProxyOAuthCompletionPersistsLatestBindingAndDoesNotExposeMeta(t *testing.T) {
	ctx := context.Background()
	db := newAdminTestStore(t)
	p := &store.Proxy{Name: "oauth proxy", URL: "http://user:old-secret@127.0.0.1:1234"}
	if err := db.CreateProxy(ctx, p); err != nil {
		t.Fatal(err)
	}
	var persisted int
	manager := oauth.NewFlowManager(nil, oauth.FlowOptions{OnComplete: func(flow *oauth.Flow, _ *oauth.Token) error {
		persisted++
		if flow.DBAccountID > 0 {
			return nil
		} // Reauthorization of an existing row.
		a := &store.Account{Name: "new oauth account", Enabled: true, ProxyURL: flow.Meta["proxy_url"]}
		if err := db.CreateAccount(ctx, a); err != nil {
			return err
		}
		flow.DBAccountID = a.ID
		return nil
	}})
	s := &Server{store: db, flows: manager}
	s.bindOAuthProxy()
	// Intercept the same callback used by OAuth finish, without any external login.
	var completion func(*oauth.Flow, *oauth.Token) error
	manager.WrapCompletion(func(next func(*oauth.Flow, *oauth.Token) error) func(*oauth.Flow, *oauth.Token) error {
		completion = next
		return next
	})
	p.URL = "http://user:updated-secret@127.0.0.1:1235"
	if err := db.UpdateProxy(ctx, p); err != nil {
		t.Fatal(err)
	}
	flow := &oauth.Flow{Meta: map[string]string{"proxy_id": strconv.FormatInt(p.ID, 10), "proxy_url": "http://old:old-secret@127.0.0.1:1234"}}
	if err := completion(flow, &oauth.Token{}); err != nil {
		t.Fatal(err)
	}
	a, err := db.GetAccount(ctx, flow.DBAccountID)
	if err != nil || a == nil || a.ProxyID == nil || *a.ProxyID != p.ID || a.ProxyURL != p.URL {
		t.Fatalf("OAuth proxy not persisted: %+v %v", a, err)
	}
	raw, _ := json.Marshal(flow)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "proxy_url") {
		t.Fatalf("flow metadata exposed proxy credentials: %s", raw)
	}
	flow.Meta["proxy_id"] = "999999"
	if err := completion(flow, &oauth.Token{}); err == nil || persisted != 1 {
		t.Fatal("missing proxy should fail before the account persistence callback")
	}
	flow.Meta["proxy_id"] = "null"
	flow.Meta["proxy_url"] = ""
	if err := completion(flow, &oauth.Token{}); err != nil {
		t.Fatal(err)
	}
	a, err = db.GetAccount(ctx, flow.DBAccountID)
	if err != nil || a.ProxyID != nil || a.ProxyURL != "" {
		t.Fatalf("explicit direct OAuth selection retained a saved proxy: %+v %v", a, err)
	}
}
