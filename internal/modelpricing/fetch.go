package modelpricing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"pi-gateway/internal/pricing"
)

func trustedHost(source Source, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, h := range source.TrustedHosts {
		if strings.EqualFold(strings.TrimSuffix(h, "."), host) {
			return true
		}
	}
	return false
}
func validateSource(source *Source) error {
	if source.ID == "" || len(source.ID) > 128 || source.ID == "import" || strings.ContainsAny(source.ID, "/\\ \r\n\x00") {
		return fmt.Errorf("invalid or reserved source id")
	}
	if len(source.Name) > 256 {
		return fmt.Errorf("source name too long")
	}
	if source.Format != "standard" && source.Format != "models_dev" {
		return fmt.Errorf("format must be standard or models_dev")
	}
	if source.IntervalHours == 0 {
		source.IntervalHours = 24
	}
	if source.IntervalHours < 1 || source.IntervalHours > 8760 {
		return fmt.Errorf("interval_hours must be 1..8760")
	}
	if source.ProxyID < 0 {
		return fmt.Errorf("invalid proxy_id")
	}
	if len(source.TrustedHosts) > 32 {
		return fmt.Errorf("too many trusted hosts")
	}
	for _, h := range source.TrustedHosts {
		if h == "" || strings.ContainsAny(h, "/*:@?#\\ \r\n") {
			if net.ParseIP(h) == nil {
				return fmt.Errorf("trusted_hosts must contain exact hostnames or IP addresses")
			}
		}
	}
	_, err := validateURL(*source, source.URL)
	return err
}
func validateURL(source Source, raw string) (*url.URL, error) {
	if len(raw) > 4096 {
		return nil, fmt.Errorf("source URL too long")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("source URL must be HTTPS without credentials or fragment")
	}
	if source.ProxyID != 0 && !trustedHost(source, u.Hostname()) {
		return nil, fmt.Errorf("proxy resolves destinations remotely: every destination must be explicitly listed in trusted_hosts")
	}
	return u, nil
}

var nonPublicPrefixes = func() []netip.Prefix {
	out := []netip.Prefix{}
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "2001:db8::/32", "2001::/32", "2002::/16", "64:ff9b::/96"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func publicIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if a.Is6() && !netip.MustParsePrefix("2000::/3").Contains(a) {
		return false
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}
func resolvedAddresses(ctx context.Context, source Source, host string) ([]net.IPAddr, error) {
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("source DNS lookup failed")
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("source DNS returned no addresses")
	}
	for _, ip := range ips {
		if !trustedHost(source, host) && !publicIP(ip.IP) {
			return nil, fmt.Errorf("source resolves to a non-public address; trusted_hosts is an explicit network trust exemption")
		}
	}
	return ips, nil
}
func (s *Service) sourceClient(ctx context.Context, source Source) (*http.Client, error) {
	proxyURL := ""
	if source.ProxyID != 0 {
		p, err := s.store.GetProxy(ctx, source.ProxyID)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, fmt.Errorf("proxy not found")
		}
		proxyURL = p.URL
	}
	base, err := s.factory.HTTPClient(proxyURL)
	if err != nil {
		return nil, err
	}
	client := *base
	client.Timeout = 30 * time.Second
	client.Jar = nil
	transport, ok := base.Transport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("unsupported pricing transport")
	}
	t := transport.Clone()
	t.MaxResponseHeaderBytes = 64 << 10
	client.Transport = t
	if source.ProxyID == 0 {
		t.Proxy = nil
		t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := resolvedAddresses(ctx, source, host)
			if err != nil {
				return nil, err
			}
			var last error
			for _, ip := range ips {
				d := net.Dialer{Timeout: 10 * time.Second}
				conn, e := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
				if e == nil {
					return conn, nil
				}
				last = e
			}
			return nil, last
		}
		client.Transport = t
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many source redirects")
		}
		if _, err := validateURL(source, req.URL.String()); err != nil {
			return err
		}
		if source.ProxyID == 0 {
			if _, err := resolvedAddresses(req.Context(), source, req.URL.Hostname()); err != nil {
				return err
			}
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			req.Header.Del("If-None-Match")
			req.Header.Del("If-Modified-Since")
		}
		return nil
	}
	return &client, nil
}

type fetched struct {
	Models         []pricing.ModelPrice
	Warnings       map[string][]string
	ETag, Modified string
	NotModified    bool
}

func (s *Service) fetch(ctx context.Context, source Source, conditional bool) (*fetched, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	u, err := validateURL(source, source.URL)
	if err != nil {
		return nil, err
	}
	// Proxy destinations are explicitly trusted, not falsely claimed to be locally DNS-pinned.
	if source.ProxyID == 0 {
		if _, err = resolvedAddresses(ctx, source, u.Hostname()); err != nil {
			return nil, err
		}
	}
	client, err := s.sourceClient(ctx, source)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "pi-gateway-pricing/1")
	if conditional {
		if source.ETag != "" {
			req.Header.Set("If-None-Match", source.ETag)
		}
		if source.LastModified != "" {
			req.Header.Set("If-Modified-Since", source.LastModified)
		}
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pricing source request failed")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified && conditional {
		return &fetched{NotModified: true}, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pricing source returned HTTP %d", response.StatusCode)
	}
	if response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity" {
		return nil, fmt.Errorf("unexpected source compression")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read pricing source failed")
	}
	if len(raw) > MaxDocumentBytes {
		return nil, fmt.Errorf("source exceeds 16 MiB")
	}
	var out *fetched
	if source.Format == "models_dev" {
		out, err = parseModelsDev(raw)
	} else {
		var document StandardPriceFile
		err = json.Unmarshal(raw, &document)
		if err == nil {
			out, err = parseStandard(document)
		}
	}
	if err != nil {
		return nil, err
	}
	if len(out.Models) == 0 {
		return nil, fmt.Errorf("source contains no usable models")
	}
	out.ETag = response.Header.Get("ETag")
	out.Modified = response.Header.Get("Last-Modified")
	return out, nil
}
func parseStandard(document StandardPriceFile) (*fetched, error) {
	if document.SchemaVersion != 1 || document.Currency != "USD" || document.Unit != "per_million_tokens" {
		return nil, fmt.Errorf("expected schema_version=1, currency=USD, unit=per_million_tokens")
	}
	if len(document.Models) == 0 || len(document.Models) > MaxModels {
		return nil, fmt.Errorf("models must contain 1..%d entries", MaxModels)
	}
	seen := map[string]bool{}
	out := &fetched{Models: make([]pricing.ModelPrice, len(document.Models)), Warnings: map[string][]string{}}
	for i, m := range document.Models {
		m = clonePrice(m)
		out.Models[i] = m
		if !validModelID(m.Model) {
			return nil, fmt.Errorf("invalid model identifier")
		}
		if seen[m.Model] {
			return nil, fmt.Errorf("duplicate model %s", m.Model)
		}
		seen[m.Model] = true
		if err := pricing.ValidateModel(m); err != nil {
			return nil, err
		}
		out.Warnings[m.Model] = missingWarnings(m)
	}
	return out, nil
}
func missingWarnings(m pricing.ModelPrice) []string {
	out := []string{}
	if m.Disabled {
		out = append(out, "price is disabled; automatic apply is forbidden")
	}
	if m.Standard == nil {
		out = append(out, "standard tier is absent")
	}
	for name, r := range map[string]*pricing.DecimalRates{"standard": m.Standard, "flex": m.Flex, "fast": m.Fast, "long_standard": m.LongStandard, "long_flex": m.LongFlex, "long_fast": m.LongFast} {
		if r == nil {
			continue
		}
		for field, value := range map[string]string{"input": r.Input, "cached_input": r.CachedInput, "cache_write": r.CacheWrite, "output": r.Output} {
			if value == "" {
				out = append(out, name+"."+field+" is unknown")
			}
		}
	}
	sort.Strings(out)
	return out
}
func parseModelsDev(raw []byte) (*fetched, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("invalid models.dev JSON")
	}
	provider, ok := root["openai"]
	if !ok {
		return nil, fmt.Errorf("models.dev OpenAI provider is absent")
	}
	var p struct {
		Models map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(provider, &p); err != nil {
		return nil, err
	}
	if len(p.Models) > MaxModels {
		return nil, fmt.Errorf("too many models")
	}
	out := &fetched{Models: []pricing.ModelPrice{}, Warnings: map[string][]string{}}
	for id, data := range p.Models {
		var entry struct {
			ID   string                     `json:"id"`
			Name string                     `json:"name"`
			Cost map[string]json.RawMessage `json:"cost"`
		}
		if err := json.Unmarshal(data, &entry); err != nil {
			return nil, err
		}
		if entry.ID != "" {
			id = entry.ID
		}
		if !validModelID(id) {
			return nil, fmt.Errorf("invalid models.dev model")
		}
		r := &pricing.DecimalRates{}
		warnings := []string{"models.dev does not fully describe gateway tier/context policy; review explicitly"}
		for field, dst := range map[string]*string{"input": &r.Input, "output": &r.Output, "cache_read": &r.CachedInput, "cache_write": &r.CacheWrite} {
			v, present := entry.Cost[field]
			if !present || bytes.Equal(v, []byte("null")) {
				continue
			}
			var number json.Number
			if err := json.Unmarshal(v, &number); err != nil {
				return nil, fmt.Errorf("invalid models.dev cost %s", field)
			}
			text := number.String()
			if len(text) > 80 {
				return nil, fmt.Errorf("models.dev cost exceeds precision limit")
			}
			if i := strings.IndexAny(text, "eE"); i >= 0 {
				exponent, e := strconv.Atoi(text[i+1:])
				if e != nil || exponent < -36 || exponent > 36 {
					return nil, fmt.Errorf("models.dev cost exceeds precision limit")
				}
			}
			rat, ok := new(big.Rat).SetString(text)
			if !ok || rat.Sign() < 0 {
				return nil, fmt.Errorf("invalid models.dev cost %s", field)
			}
			*dst = rat.FloatString(18)
			converted, _ := new(big.Rat).SetString(*dst)
			if converted.Cmp(rat) != 0 {
				return nil, fmt.Errorf("models.dev cost exceeds exact decimal precision")
			}
			*dst = strings.TrimRight(strings.TrimRight(*dst, "0"), ".")
			if *dst == "" {
				*dst = "0"
			}
		}
		for key := range entry.Cost {
			if key != "input" && key != "output" && key != "cache_read" && key != "cache_write" {
				warnings = append(warnings, "unsupported models.dev cost field: "+key)
			}
		}
		m := pricing.ModelPrice{Model: id, Name: entry.Name, Standard: r, ContextMode: "same", Source: "external"}
		if err := pricing.ValidateModel(m); err != nil {
			return nil, err
		}
		warnings = append(warnings, missingWarnings(m)...)
		out.Models = append(out.Models, m)
		out.Warnings[id] = warnings
	}
	sort.Slice(out.Models, func(i, j int) bool { return out.Models[i].Model < out.Models[j].Model })
	return out, nil
}
func hashValue(v any) string { sum := sha256.Sum256(encode(v)); return hex.EncodeToString(sum[:]) }
func sourceHash(s Source) string {
	return hashValue(struct {
		URL, Format string
		ProxyID     int64
		Trusted     []string
	}{s.URL, s.Format, s.ProxyID, s.TrustedHosts})
}
