package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"pi-gateway/internal/egress"
	"pi-gateway/internal/store"
)

func validateProxyOrEmpty(raw string) error {
	return egress.Validate(raw)
}

func timeDurationSeconds(secs int) time.Duration {
	return time.Duration(secs) * time.Second
}

// validateMiddlewareConfig ensures the stored config is a JSON object, so a bad
// edit cannot break every subsequent request.
func validateMiddlewareConfig(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return errInvalid("config must be a JSON object")
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		return errInvalid("config must be a valid JSON object: " + err.Error())
	}
	return nil
}

// overviewView powers the dashboard.
type overviewView struct {
	Accounts      overviewAccounts `json:"accounts"`
	Keys          int              `json:"keys"`
	KeysEnabled   int              `json:"keys_enabled"`
	Captures      any              `json:"captures"`
	Upstream      overviewUpstream `json:"upstream"`
	Settings      any              `json:"settings"`
	RecentCapture []any            `json:"recent_captures"`
	Audit         any              `json:"audit"`
}

type overviewAccounts struct {
	Total    int            `json:"total"`
	Enabled  int            `json:"enabled"`
	ByStatus map[string]int `json:"by_status"`
	Expiring []any          `json:"expiring_soon"`
}

type overviewUpstream struct {
	BaseURL       string `json:"base_url"`
	Transport     string `json:"transport"`
	WebsocketPool int    `json:"websocket_pool"`
	UserAgent     string `json:"user_agent"`
	Originator    string `json:"originator"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	accts := overviewAccounts{ByStatus: map[string]int{}, Expiring: []any{}}
	now := time.Now()
	for _, a := range accounts {
		accts.Total++
		if a.Enabled {
			accts.Enabled++
		}
		status := a.Status
		if status == "" {
			status = "unknown"
		}
		accts.ByStatus[status]++
		if a.ExpiresAt > 0 {
			expires := time.UnixMilli(a.ExpiresAt)
			if expires.Before(now.Add(6 * time.Hour)) {
				accts.Expiring = append(accts.Expiring, map[string]any{
					"id":         a.ID,
					"name":       a.Name,
					"expires_at": a.ExpiresAt,
					"status":     a.Status,
				})
			}
		}
	}

	keys, _ := s.store.ListKeys(ctx)
	keysEnabled := 0
	for _, k := range keys {
		if k.Enabled {
			keysEnabled++
		}
	}

	stats, _ := s.store.Stats(ctx, 500)
	recent, _, _ := s.store.ListCaptures(ctx, store.CaptureFilter{Limit: 12})
	recentAny := make([]any, 0, len(recent))
	for _, c := range recent {
		recentAny = append(recentAny, c)
	}
	audit, _ := s.store.ListAudit(ctx, 20)

	rt := s.settings.Get()
	writeJSON(w, http.StatusOK, overviewView{
		Accounts:    accts,
		Keys:        len(keys),
		KeysEnabled: keysEnabled,
		Captures:    stats,
		Upstream: overviewUpstream{
			BaseURL:       s.cfg.Upstream.BaseURL,
			Transport:     rt.UpstreamTransport,
			WebsocketPool: s.upstream.PoolSize(),
			UserAgent:     firstNonEmptyString(rt.UserAgent, s.cfg.Upstream.UserAgent),
			Originator:    s.cfg.Upstream.Originator,
		},
		Settings:      rt,
		RecentCapture: recentAny,
		Audit:         audit,
	})
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
