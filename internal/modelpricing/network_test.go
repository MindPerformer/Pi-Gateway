package modelpricing

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"testing"

	"pi-gateway/internal/store"
)

func TestProxyClientIsIndependentAndTrustIsExplicit(t *testing.T) {
	s, st := testService(t)
	ctx := context.Background()
	p := &store.Proxy{Name: "fixture", URL: "http://127.0.0.1:19999"}
	if err := st.CreateProxy(ctx, p); err != nil {
		t.Fatal(err)
	}
	source := Source{ID: "proxy-fixture", URL: "https://remote-only.invalid/prices", ProxyID: p.ID, TrustedHosts: []string{"remote-only.invalid"}, Format: "standard"}
	client, err := s.sourceClient(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	base, err := s.factory.HTTPClient(p.URL)
	if err != nil {
		t.Fatal(err)
	}
	if client == base || client.Transport == base.Transport || client.Timeout == 0 || client.Jar != nil {
		t.Fatal("pricing reused the account client/transport")
	}
	defer client.CloseIdleConnections()
	target, _ := url.Parse(source.URL)
	req := &http.Request{URL: target, Header: make(http.Header)}
	if err = client.CheckRedirect(req, nil); err != nil {
		t.Fatalf("explicit proxy hostname wrongly depended on local DNS: %v", err)
	}
	target, _ = url.Parse("https://untrusted.invalid/prices")
	req.URL = target
	if err = client.CheckRedirect(req, nil); err == nil {
		t.Fatal("proxy redirect escaped trusted host list")
	}
}
func TestDirectDialRevalidatesDestinationBeforeConnecting(t *testing.T) {
	s, _ := testService(t)
	client, err := s.sourceClient(context.Background(), Source{URL: "https://example.com", Format: "standard"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	transport := client.Transport.(*http.Transport)
	conn, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:80")
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("rebound loopback destination reached the dialer")
	}
	for _, ip := range []string{"::", "100::1", "64:ff9b::a00:1", "2001:db8::1", "ff02::1"} {
		if publicIP(net.ParseIP(ip)) {
			t.Errorf("non-public IPv6 address accepted: %s", ip)
		}
	}
}
func TestModelsDevRejectsRoundingUnknownCostToZero(t *testing.T) {
	for _, value := range []string{"0.0000000000000000001", "1e100000000", "-0.1"} {
		raw := []byte(`{"openai":{"models":{"m":{"cost":{"input":` + value + `,"output":1}}}}}`)
		if _, err := parseModelsDev(raw); err == nil {
			t.Errorf("inexact/unbounded cost accepted: %s", value)
		}
	}
	result, err := parseModelsDev([]byte(`{"openai":{"models":{"m":{"cost":{"input":1e-6,"output":1}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Models[0].Standard.Input != "0.000001" {
		t.Fatal("scientific decimal not preserved exactly")
	}
}
