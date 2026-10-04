package config

import (
	"errors"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

const (
	// DefaultCodexBaseURL is independent of the main ChatGPT upstream.
	DefaultCodexBaseURL = "https://chatgpt.com/backend-api"
	// DefaultCodexClientVersion comes from the local reference implementation;
	// it is a configurable compatibility value, not a claim about the latest release.
	DefaultCodexClientVersion = "0.153.4"
)

// UpstreamModelsURL derives the model-list endpoint beside the configured
// Responses endpoint. It never switches to the separate Codex quota service.
func (c *Config) UpstreamModelsURL() string {
	endpoint := c.UpstreamSSEURL()
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	u.RawPath = strings.TrimSuffix(strings.TrimRight(u.EscapedPath(), "/"), "/responses") + "/models"
	u.Path, _ = url.PathUnescape(u.RawPath)
	return u.String()
}

// CodexModelsURL derives the catalog endpoint independently of Upstream.BaseURL.
// Zero-value model settings use defaults. Invalid settings return an empty URL;
// Validate reports the same failure without exposing configured URLs or values.
func (c *Config) CodexModelsURL() string {
	u, err := c.codexModelsURL()
	if err != nil {
		return ""
	}
	return u.String()
}

func (c *Config) codexModelsURL() (*url.URL, error) {
	base, version := c.Models.CodexBaseURL, c.Models.CodexClientVersion
	if base == "" {
		base = DefaultCodexBaseURL
	}
	if version == "" {
		version = DefaultCodexClientVersion
	}
	if len(version) > 64 {
		return nil, errors.New("models.codex_client_version: invalid length (maximum 64 bytes)")
	}
	for i := 0; i < len(version); i++ {
		ch := version[i]
		alnum := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9'
		if !alnum && (i == 0 || !strings.ContainsRune("._+-", rune(ch))) {
			return nil, errors.New("models.codex_client_version: invalid characters (use ASCII letters, digits, '.', '_', '+', '-'; start with a letter or digit)")
		}
	}
	if strings.TrimSpace(base) != base {
		return nil, errors.New("models.codex_base_url: invalid surrounding whitespace")
	}
	u, err := url.Parse(base)
	if err != nil || u.Opaque != "" {
		return nil, errors.New("models.codex_base_url: invalid URL syntax")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("models.codex_base_url: unsupported scheme (want http or https)")
	}
	if u.User != nil {
		return nil, errors.New("models.codex_base_url: userinfo is not allowed")
	}
	if strings.Contains(base, "#") {
		return nil, errors.New("models.codex_base_url: fragments are not allowed")
	}
	if !validCodexCatalogHost(u) {
		return nil, errors.New("models.codex_base_url: invalid host or port")
	}
	if strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), "api.openai.com") {
		return nil, errors.New("models.codex_base_url: main OpenAI API host is not allowed for Codex credentials")
	}
	if hasCodexURLControl(u.Path) {
		return nil, errors.New("models.codex_base_url: invalid path characters")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errors.New("models.codex_base_url: invalid query syntax")
	}
	for key, values := range query {
		if hasCodexURLControl(key) {
			return nil, errors.New("models.codex_base_url: invalid query characters")
		}
		switch strings.ToLower(strings.ReplaceAll(key, "-", "_")) {
		case "token", "access_token", "refresh_token", "id_token", "api_key", "apikey", "key", "authorization", "auth", "bearer", "password", "client_secret", "secret":
			return nil, errors.New("models.codex_base_url: credential query parameters are not allowed")
		}
		for _, value := range values {
			if hasCodexURLControl(value) {
				return nil, errors.New("models.codex_base_url: invalid query characters")
			}
		}
	}
	// Preserve escaped proxy prefixes and query values; never append to RawQuery.
	path := strings.TrimRight(u.EscapedPath(), "/")
	last, _ := url.PathUnescape(path[strings.LastIndex(path, "/")+1:])
	switch {
	case path == "":
		path = "/backend-api/codex/models"
	case last == "models":
		// An explicit model-list endpoint is already complete.
	case last == "codex":
		path += "/models"
	default:
		path += "/codex/models"
	}
	u.RawPath = path
	u.Path, _ = url.PathUnescape(path)
	query.Set("client_version", version)
	u.RawQuery = query.Encode()
	u.ForceQuery = false
	return u, nil
}

func validCodexCatalogHost(u *url.URL) bool {
	host := u.Hostname()
	if host == "" || hasCodexURLControl(host) {
		return false
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		// IP literals must use brackets for IPv6, but not for IPv4.
		if addr.Is6() != strings.HasPrefix(u.Host, "[") {
			return false
		}
		// Scoped local IPv6 is allowed, without escaped whitespace or delimiters.
		for _, ch := range addr.Zone() {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.ContainsRune("._~-", ch)) {
				return false
			}
		}
	} else {
		if strings.ContainsAny(u.Host, "[]") {
			return false
		}
		host = strings.TrimSuffix(host, ".")
		if len(host) == 0 || len(host) > 253 {
			return false
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return false
			}
			for i := 0; i < len(label); i++ {
				ch := label[i]
				if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
					return false
				}
			}
		}
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return false
	}
	return true
}

func hasCodexURLControl(value string) bool {
	for _, ch := range value {
		if ch < ' ' || ch == 0x7f {
			return true
		}
	}
	return false
}
