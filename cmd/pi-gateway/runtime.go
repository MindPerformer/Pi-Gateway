package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pi-gateway/internal/config"
	"pi-gateway/internal/session"
)

type sessionStore interface {
	session.Store
	session.AffinityStore
	session.CatalogCache
	Close() error
}

func newSessionStore(cfg *config.Config, deploymentID string) (sessionStore, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(deploymentID) == "" {
		return nil, errors.New("session: database deployment identity is required")
	}
	options := session.Options{
		Namespace:      cfg.Redis.Prefix + deploymentID,
		TTL:            time.Duration(cfg.Redis.SessionTTLSeconds) * time.Second,
		CommitTimeout:  time.Duration(cfg.Redis.CommitTimeoutMS) * time.Millisecond,
		MaxEntries:     cfg.Redis.MaxEntries,
		MaxRecordBytes: cfg.Redis.MaxRecordBytes,
		MaxBytes:       cfg.Redis.MaxBytes,
	}
	if !cfg.Redis.Enabled {
		return session.NewMemory(options), nil
	}
	// Construction validates configuration, but deliberately does not require a
	// reachable Redis. Keep this backend even during outages: an untrusted miss
	// must never become trusted merely because a fallback store is empty.
	redisStore, err := session.NewRedis(session.RedisOptions{
		URL: cfg.Redis.URL, Password: cfg.Redis.Password, TLS: cfg.Redis.TLS,
		Options: options,
	})
	if err != nil {
		return nil, errors.New("config: invalid Redis configuration (values redacted)")
	}
	return redisStore, nil
}

func healthcheckURL(host string, port int) (string, error) {
	if port <= 0 || port > 65535 {
		return "", errors.New("healthcheck: invalid local port")
	}
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	} else if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = "::1"
	}
	if strings.ContainsAny(host, "/?#@\r\n") {
		return "", errors.New("healthcheck: invalid local host")
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/healthz", nil
}

// checkHealth is a read-only local probe: no store, migrations or workers.
func checkHealth(ctx context.Context, host string, port int) error {
	endpoint, err := healthcheckURL(host, port)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("healthcheck: invalid local endpoint")
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		Timeout:       2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("healthcheck: local HTTP request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("healthcheck: unexpected HTTP status %d", resp.StatusCode)
	}
	return nil
}
