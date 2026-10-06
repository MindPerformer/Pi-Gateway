package piwire

import (
	"net/http"
	"testing"
)

func TestPiFingerprintAllowlist(t *testing.T) {
	client := http.Header{"User-Agent": {"pi (win32 10.0.26200; x64)"}, "X-Stainless-Os": {"Windows"}, "X-Stainless-Runtime-Version": {"v22.17.1"}, "X-Stainless-Timeout": {"300"}, "Accept-Language": {"*"}, "Authorization": {"client-secret"}, "X-Codex-Turn-Metadata": {"private"}}
	h := BuildSSEHeaders(HeaderOptions{ClientHeaders: client, AccessToken: "upstream"})
	for _, name := range []string{"User-Agent", "X-Stainless-Os", "X-Stainless-Runtime-Version", "X-Stainless-Timeout", "Accept-Language"} {
		if h.Get(name) != client.Get(name) {
			t.Fatalf("lost Pi header %s", name)
		}
	}
	if h.Get("Authorization") != "Bearer upstream" || h.Get("X-Codex-Turn-Metadata") != "" {
		t.Fatal("client credentials/metadata leaked")
	}
	client.Set("User-Agent", "codex-tui/0.154.0")
	if BuildSSEHeaders(HeaderOptions{ClientHeaders: client}).Get("User-Agent") != DefaultUserAgent() {
		t.Fatal("Codex bypassed Pi shape")
	}
}
