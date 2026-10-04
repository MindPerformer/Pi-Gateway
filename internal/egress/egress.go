// Package egress builds per-account outbound transports.
//
// Every account may carry its own proxy URL. An empty proxy means a direct
// connection. Supported schemes: http, https, socks5, socks5h. The same proxy is
// used for the SSE (HTTP) and WebSocket transports so an account always egresses
// from one place.
package egress

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// Direct is the sentinel proxy value meaning "no proxy".
const Direct = ""

// SupportedSchemes lists the accepted proxy schemes.
var SupportedSchemes = []string{"http", "https", "socks5", "socks5h"}

// Parse validates a proxy URL and normalises it. Empty returns nil (direct).
func Parse(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if len(trimmed) > 4096 {
		return nil, fmt.Errorf("proxy url is too long (%d bytes, max 4096)", len(trimmed))
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("proxy url is invalid")
	}
	scheme := strings.ToLower(u.Scheme)
	if !isSupportedScheme(scheme) {
		return nil, fmt.Errorf("proxy scheme %q is not supported (want %s)", u.Scheme, strings.Join(SupportedSchemes, ", "))
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("proxy url must include a host")
	}
	port := u.Port()
	if port == "" {
		return nil, fmt.Errorf("proxy url must include a port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, fmt.Errorf("proxy port must be between 1 and 65535")
	}
	u.Scheme = scheme
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("proxy url must not include a path")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("proxy url must not include a query or fragment")
	}
	return u, nil
}

// Validate checks a proxy URL string.
func Validate(raw string) error {
	_, err := Parse(raw)
	return err
}

func isSupportedScheme(s string) bool {
	for _, want := range SupportedSchemes {
		if s == want {
			return true
		}
	}
	return false
}

// Redact hides userinfo for display purposes.
func Redact(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "[invalid proxy URL]"
	}
	if u.User == nil {
		return raw
	}
	u.User = url.UserPassword("***", "***")
	return u.String()
}

// Options controls transport construction.
type Options struct {
	ConnectTimeout time.Duration
	IdleTimeout    time.Duration
	// ResponseHeaderTimeout bounds the wait for HTTP response headers, not the
	// streaming body. Non-positive values default to ConnectTimeout (15s by default).
	ResponseHeaderTimeout time.Duration
	// RootCAs optionally supplies trusted roots for upstreams and HTTPS proxies.
	// Nil uses system roots; certificate and hostname verification stay enabled.
	RootCAs *x509.CertPool
}

// Factory caches transports per proxy URL so connection pools are reused.
type Factory struct {
	opts Options

	mu        sync.Mutex
	httpByKey map[string]*http.Client
	tlsConf   *tls.Config

	// BaseDialer overrides the underlying dialer (used by tests).
	BaseDialer func(ctx context.Context, network, addr string) (net.Conn, error)
}

// NewFactory builds a transport factory.
func NewFactory(opts Options) *Factory {
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = 15 * time.Second
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 300 * time.Second
	}
	if opts.ResponseHeaderTimeout <= 0 {
		opts.ResponseHeaderTimeout = opts.ConnectTimeout
	}
	tlsConf := &tls.Config{}
	if opts.RootCAs != nil {
		tlsConf.RootCAs = opts.RootCAs.Clone()
	}
	return &Factory{opts: opts, httpByKey: map[string]*http.Client{}, tlsConf: tlsConf}
}

// dialContext builds the base dialer honouring the connect timeout.
func (f *Factory) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if f.BaseDialer != nil {
		return f.BaseDialer(ctx, network, addr)
	}
	d := &net.Dialer{Timeout: f.opts.ConnectTimeout, KeepAlive: 30 * time.Second}
	return d.DialContext(ctx, network, addr)
}

// proxyDialer builds a SOCKS dialer, resolving locally for socks5 and remotely for socks5h.
func (f *Factory) proxyDialer(u *url.URL) (proxy.Dialer, error) {
	var auth *proxy.Auth
	if u.User != nil {
		user := u.User.Username()
		pass, _ := u.User.Password()
		auth = &proxy.Auth{User: user, Password: pass}
	}
	base := &net.Dialer{Timeout: f.opts.ConnectTimeout, KeepAlive: 30 * time.Second}
	return proxy.SOCKS5("tcp", u.Host, auth, base)
}

// HTTPClient returns a cached HTTP client bound to the given proxy.
func (f *Factory) HTTPClient(rawProxy string) (*http.Client, error) {
	u, err := Parse(rawProxy)
	if err != nil {
		return nil, err
	}
	key := "direct"
	if u != nil {
		key = u.String()
	}

	f.mu.Lock()
	if c, ok := f.httpByKey[key]; ok {
		f.mu.Unlock()
		return c, nil
	}
	f.mu.Unlock()

	tlsConf := f.tlsConf.Clone()
	tlsConf.NextProtos = []string{"http/1.1"}
	transport := &http.Transport{
		// Pi talks HTTP/1.1 (undici); keep the fingerprint off HTTP/2.
		ForceAttemptHTTP2:     false,
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       f.opts.IdleTimeout,
		TLSHandshakeTimeout:   f.opts.ConnectTimeout,
		ResponseHeaderTimeout: f.opts.ResponseHeaderTimeout,
		// We manage Accept-Encoding ourselves so the wire matches Pi's client.
		DisableCompression: true,
		TLSClientConfig:    tlsConf,
		DialContext:        f.dialContext,
	}

	if u != nil {
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
			pu := *u
			transport.Proxy = http.ProxyURL(&pu)
		case "socks5", "socks5h":
			dialer, err := f.proxyDialer(u)
			if err != nil {
				return nil, err
			}
			cd, ok := dialer.(proxy.ContextDialer)
			if !ok {
				return nil, fmt.Errorf("proxy dialer does not support context")
			}
			scheme := strings.ToLower(u.Scheme)
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				if scheme == "socks5" {
					resolved, err := resolveFirst(ctx, addr)
					if err != nil {
						return nil, err
					}
					addr = resolved
				}
				return cd.DialContext(ctx, network, addr)
			}
		}
	}

	client := &http.Client{
		Transport: transport,
		// Streaming responses are governed by per-request contexts, not this timeout.
		Timeout: 0,
	}

	f.mu.Lock()
	f.httpByKey[key] = client
	f.mu.Unlock()
	return client, nil
}

// WSDialConfig describes how to open a WebSocket connection for a proxy setting.
//
// gorilla/websocket cannot take an *http.Client, so the pieces are surfaced
// explicitly: HTTP proxies are handled by Proxy (CONNECT), HTTPS and SOCKS
// proxies by NetDialContext. Target TLS remains the WebSocket dialer's job.
type WSDialConfig struct {
	Proxy           func(*http.Request) (*url.URL, error)
	NetDialContext  func(ctx context.Context, network, addr string) (net.Conn, error)
	TLSClientConfig *tls.Config
	ConnectTimeout  time.Duration
}

// WSDialConfig returns the dial configuration for a WebSocket connection.
func (f *Factory) WSDialConfig(rawProxy string) (*WSDialConfig, error) {
	u, err := Parse(rawProxy)
	if err != nil {
		return nil, err
	}

	cfg := &WSDialConfig{ConnectTimeout: f.opts.ConnectTimeout, TLSClientConfig: f.tlsConf.Clone()}
	if u == nil {
		cfg.NetDialContext = f.dialContext
		return cfg, nil
	}

	switch strings.ToLower(u.Scheme) {
	case "http":
		pu := *u
		cfg.Proxy = http.ProxyURL(&pu)
		cfg.NetDialContext = f.dialContext
	case "https":
		// Gorilla's HTTP proxy dialer does not support TLS to the proxy. Return
		// an already CONNECTed tunnel, not a TLS connection to the target.
		cfg.NetDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return f.dialHTTPSProxy(ctx, u, network, addr)
		}
	case "socks5", "socks5h":
		dialer, err := f.proxyDialer(u)
		if err != nil {
			return nil, err
		}
		cd, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("proxy dialer does not support context")
		}
		scheme := strings.ToLower(u.Scheme)
		cfg.NetDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if scheme == "socks5" {
				resolved, err := resolveFirst(ctx, addr)
				if err != nil {
					return nil, err
				}
				addr = resolved
			}
			return cd.DialContext(ctx, network, addr)
		}
	default:
		return nil, fmt.Errorf("proxy scheme %q cannot be used for websockets", u.Scheme)
	}
	return cfg, nil
}

// httpsProxyError keeps untrusted proxy response text out of logs. The cause is
// still available to errors.Is/As for context, network and certificate errors.
type httpsProxyError struct {
	operation string
	cause     error
}

func (e *httpsProxyError) Error() string { return "egress: HTTPS proxy " + e.operation + " failed" }
func (e *httpsProxyError) Unwrap() error { return e.cause }

type proxyTunnelConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *proxyTunnelConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

// dialHTTPSProxy negotiates TLS with the proxy, then HTTP/1.1 CONNECT. It does not
// negotiate target TLS, so Gorilla can use the same tunnel for both ws and wss.
func (f *Factory) dialHTTPSProxy(ctx context.Context, u *url.URL, network, addr string) (conn net.Conn, err error) {
	ctx, cancel := context.WithTimeout(ctx, f.opts.ConnectTimeout)
	defer cancel()
	raw, err := f.dialContext(ctx, network, u.Host)
	if err != nil {
		return nil, &httpsProxyError{operation: "dial", cause: err}
	}
	// A deadline alone does not interrupt explicit cancellation. Join the close
	// callback before handing off the tunnel so it cannot close a reused socket.
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = raw.Close()
		close(closed)
	})
	defer func() {
		if !stop() {
			<-closed
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if deadline, ok := ctx.Deadline(); err != nil && ok && !time.Now().Before(deadline) {
			err = context.DeadlineExceeded
		}
		if err != nil {
			_ = raw.Close()
			conn = nil
		}
	}()
	deadline, _ := ctx.Deadline()
	if err = raw.SetDeadline(deadline); err != nil {
		return nil, &httpsProxyError{operation: "deadline", cause: err}
	}

	tlsConf := f.tlsConf.Clone()
	tlsConf.ServerName = u.Hostname()
	tlsConf.NextProtos = []string{"http/1.1"}
	secure := tls.Client(raw, tlsConf)
	if err = secure.HandshakeContext(ctx); err != nil {
		return nil, &httpsProxyError{operation: "TLS handshake", cause: err}
	}
	request := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: make(http.Header),
	}
	if auth := BasicAuthHeader(u); auth != "" {
		request.Header.Set("Proxy-Authorization", auth)
	}
	if err = request.Write(secure); err != nil {
		return nil, &httpsProxyError{operation: "CONNECT write", cause: err}
	}
	reader := bufio.NewReader(secure)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return nil, &httpsProxyError{operation: "CONNECT response", cause: err}
	}
	if response.StatusCode != http.StatusOK {
		// Never report a proxy-controlled reason phrase or body: it may echo
		// credentials. Closing raw also avoids draining an unbounded error body.
		return nil, fmt.Errorf("egress: HTTPS proxy CONNECT failed (HTTP %d)", response.StatusCode)
	}
	// A successful CONNECT body is the tunnel itself; do not drain or close it.
	if err = secure.SetDeadline(time.Time{}); err != nil {
		return nil, &httpsProxyError{operation: "clear deadline", cause: err}
	}
	return &proxyTunnelConn{Conn: secure, reader: reader}, nil
}

// resolveFirst performs local DNS resolution for socks5 (as opposed to socks5h,
// which leaves resolution to the proxy).
func resolveFirst(ctx context.Context, addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if net.ParseIP(host) != nil {
		return addr, nil
	}
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return "", err
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("no addresses for %s", host)
	}
	return net.JoinHostPort(ips[0], port), nil
}

// BasicAuthHeader renders a Proxy-Authorization value when the proxy needs one.
// gorilla/websocket and http.Transport handle this internally, but our WebSocket
// HTTPS proxy tunnel needs it when writing CONNECT manually.
func BasicAuthHeader(u *url.URL) string {
	if u == nil || u.User == nil {
		return ""
	}
	pass, _ := u.User.Password()
	token := base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + pass))
	return "Basic " + token
}

// DecodeBody unwraps a response body according to Content-Encoding.
//
// Go's transport is configured with DisableCompression so that the outbound
// Accept-Encoding header matches Pi exactly; the cost is that we must decode the
// (rare) compressed response ourselves.
func DecodeBody(resp *http.Response) (io.ReadCloser, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("egress: no response body")
	}
	enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	switch enc {
	case "", "identity":
		return resp.Body, nil
	case "gzip":
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("egress: gzip: %w", err)
		}
		return &multiCloser{Reader: zr, closers: []io.Closer{zr, resp.Body}}, nil
	case "deflate":
		// HTTP "deflate" is zlib-wrapped; some servers send raw deflate.
		zr, err := zlib.NewReader(resp.Body)
		if err != nil {
			fr := flate.NewReader(resp.Body)
			return &multiCloser{Reader: fr, closers: []io.Closer{fr, resp.Body}}, nil
		}
		return &multiCloser{Reader: zr, closers: []io.Closer{zr, resp.Body}}, nil
	default:
		// zstd or unknown encodings are surfaced untouched.
		return resp.Body, nil
	}
}

type multiCloser struct {
	io.Reader
	closers []io.Closer
}

func (m *multiCloser) Close() error {
	var first error
	for _, c := range m.closers {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
