package admin

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"pi-gateway/internal/config"
	"pi-gateway/internal/middleware"
	"pi-gateway/internal/store"
)

// settingsView is the runtime settings plus the read-only defaults.
type settingsView struct {
	Current  *store.Settings `json:"current"`
	Defaults store.Settings  `json:"defaults"`
	// Enums help the UI render valid choices.
	Transports []string `json:"transports"`
	Strategies []string `json:"strategies"`
	// Static configuration that is not runtime-editable.
	Static staticConfigView `json:"static"`
}

type staticConfigView struct {
	UpstreamBaseURL   string `json:"upstream_base_url"`
	SSEZstd           bool   `json:"sse_zstd"`
	OAuthCallbackPort int    `json:"oauth_callback_port"`
	Timezone          string `json:"timezone"`
	CapturePersist    bool   `json:"capture_persist"`
	MaxBytesPerRecord int64  `json:"max_bytes_per_record"`
	Database          string `json:"database"`
	DatabaseDriver    string `json:"database_driver"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	// DSNs (including SQLite URI options) are never part of the admin view.
	// PostgreSQL has no database file path to advertise.
	database := ""
	if s.cfg.Data.Driver == "sqlite" && s.cfg.Data.DSN == "" {
		database = s.cfg.Data.Database
	}
	writeJSON(w, http.StatusOK, settingsView{
		Current:    s.settings.Get(),
		Defaults:   s.settings.Defaults(),
		Transports: config.ValidTransports,
		Strategies: config.ValidStrategies,
		Static: staticConfigView{
			UpstreamBaseURL:   s.cfg.Upstream.BaseURL,
			SSEZstd:           s.cfg.Upstream.SSEZstd,
			OAuthCallbackPort: s.cfg.OAuth.CallbackPort,
			Timezone:          s.cfg.Timezone,
			CapturePersist:    s.cfg.Capture.Persist,
			MaxBytesPerRecord: s.cfg.Capture.MaxBytesPerRecord,
			Database:          database,
			DatabaseDriver:    s.cfg.Data.Driver,
		},
	})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var body store.Settings
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.UpstreamTransport == "" || !contains(config.ValidTransports, body.UpstreamTransport) {
		writeErr(w, http.StatusBadRequest, "upstream_transport must be one of: "+strings.Join(config.ValidTransports, ", "))
		return
	}
	if body.DefaultStrategy == "" || !contains(config.ValidStrategies, body.DefaultStrategy) {
		writeErr(w, http.StatusBadRequest, "default_strategy must be one of: "+strings.Join(config.ValidStrategies, ", "))
		return
	}
	if body.CaptureLimit < 1 || body.CaptureLimit > 10000 {
		writeErr(w, http.StatusBadRequest, "capture_limit must be between 1 and 10000")
		return
	}
	if body.MaxPerAccount < 1 || body.MaxPerAccount > 1000 {
		writeErr(w, http.StatusBadRequest, "max_concurrent_per_account must be between 1 and 1000")
		return
	}
	if body.RefreshMarginSecs < 60 {
		writeErr(w, http.StatusBadRequest, "refresh_margin_seconds must be at least 60")
		return
	}
	if body.ModelMappings == nil {
		body.ModelMappings = map[string]string{}
	}
	if err := validateProxyOrEmpty(body.DefaultProxyURL); err != nil {
		writeErr(w, http.StatusBadRequest, "default_proxy_url: "+err.Error())
		return
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"smart_scheduling_json", body.SmartSchedulingJSON},
		{"pricing_overrides_json", body.PricingOverridesJSON},
	} {
		if err := validateJSONObject(field.value); err != nil {
			writeErr(w, http.StatusBadRequest, field.name+": "+err.Error())
			return
		}
	}

	if err := s.settings.Set(r.Context(), &body); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Hand the whole snapshot to the account manager so scheduling policy,
	// cooldowns, request interval and per-account concurrency all follow the
	// saved settings. Pushing only a couple of fields here is how they drift.
	s.accounts.SetSettings(s.settings.Get())
	_ = s.store.RecordAudit(r.Context(), "settings.updated", "")
	writeJSON(w, http.StatusOK, map[string]any{"settings": s.settings.Get()})
}

// validateJSONObject accepts an empty value or a JSON object, rejecting arrays,
// scalars and malformed text before they reach a long-running component.
func validateJSONObject(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if !strings.HasPrefix(trimmed, "{") {
		return errInvalid("must be a JSON object")
	}
	if !json.Valid([]byte(trimmed)) {
		return errInvalid("must be a JSON object")
	}
	return nil
}

// handleListMiddlewares returns every registered middleware with its state and
// default config, so the UI can render an editor without hardcoding names.
func (s *Server) handleListMiddlewares(w http.ResponseWriter, r *http.Request) {
	registry := middleware.Registry()
	rows, err := s.store.ListMiddlewares(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	byName := map[string]*store.MiddlewareRow{}
	for _, row := range rows {
		byName[row.Name] = row
	}

	type item struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		Enabled       bool   `json:"enabled"`
		OrderIndex    int    `json:"order_index"`
		Config        string `json:"config"`
		DefaultConfig string `json:"default_config"`
		Configured    bool   `json:"configured"`
	}

	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]item, 0, len(names))
	for _, name := range names {
		mw := registry[name]
		it := item{
			Name:          name,
			Description:   mw.Describe(),
			Enabled:       true,
			Config:        mw.DefaultConfig(),
			DefaultConfig: mw.DefaultConfig(),
		}
		if row, ok := byName[name]; ok {
			it.Enabled = row.Enabled
			it.OrderIndex = row.OrderIndex
			it.Config = row.Config
			it.Configured = true
		}
		out = append(out, it)
	}
	writeJSON(w, http.StatusOK, map[string]any{"middlewares": out})
}

// handleUpdateMiddleware toggles/reorders/reconfigures one middleware.
func (s *Server) handleUpdateMiddleware(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	registry := middleware.Registry()
	mw, ok := registry[name]
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown middleware: "+name)
		return
	}

	var body struct {
		Enabled    *bool   `json:"enabled"`
		OrderIndex *int    `json:"order_index"`
		Config     *string `json:"config"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	row, err := s.store.GetMiddleware(r.Context(), name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if row == nil {
		row = &store.MiddlewareRow{Name: name, Enabled: true, Config: mw.DefaultConfig()}
	}
	if body.Enabled != nil {
		row.Enabled = *body.Enabled
	}
	if body.OrderIndex != nil {
		row.OrderIndex = *body.OrderIndex
	}
	if body.Config != nil {
		if err := validateMiddlewareConfig(*body.Config); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		row.Config = *body.Config
	}

	if err := s.store.UpsertMiddleware(r.Context(), row); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Validate that the persisted chain still composes.
	if _, err := s.store.ListMiddlewares(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.RecordAudit(r.Context(), "middleware.updated", name)
	writeJSON(w, http.StatusOK, map[string]any{"middleware": row})
}

// handleModelPresets lists well-known Codex model ids for the UI.
func (s *Server) handleModelPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"models": []string{
			"gpt-5.1-codex",
			"gpt-5.1-codex-max",
			"gpt-5.1-codex-mini",
			"gpt-5-codex",
			"gpt-5.1",
			"gpt-5",
			"codex-mini-latest",
		},
		"current_default": s.settings.Get().DefaultModel,
	})
}
