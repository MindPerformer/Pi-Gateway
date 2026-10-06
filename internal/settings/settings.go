// Package settings exposes runtime-adjustable settings to every component,
// backed by the SQLite settings table so changes survive a restart.
package settings

import (
	"context"
	"sync"

	"pi-gateway/internal/config"
	"pi-gateway/internal/store"
)

// Holder provides concurrent access to the current settings.
type Holder struct {
	mu       sync.RWMutex
	store    *store.Store
	defaults store.Settings
	current  *store.Settings
}

// New builds a holder from the static configuration defaults.
func New(st *store.Store, cfg *config.Config) *Holder {
	return &Holder{
		store:    st,
		defaults: DefaultsFromConfig(cfg),
	}
}

// DefaultsFromConfig derives runtime defaults from the config file.
func DefaultsFromConfig(cfg *config.Config) store.Settings {
	return store.Settings{
		SwitchOn429: true, AccountCooldownSeconds: 60, MaxAttempts: 2,
		UpstreamTransport: cfg.Upstream.Transport,
		CaptureEnabled:    cfg.Capture.Enabled,
		CaptureLimit:      cfg.Capture.PerAccountLimit,
		DefaultModel:      cfg.Models.DefaultModel,
		ModelMappings:     cfg.Models.Mappings,
		DefaultStrategy:   cfg.Accounts.DefaultStrategy,
		MaxPerAccount:     cfg.Accounts.MaxConcurrentPerAccount,
		RefreshMarginSecs: cfg.Accounts.RefreshMarginSeconds,
		ReasoningEffort:   cfg.Reasoning.DefaultEffort,
		ReasoningSummary:  cfg.Reasoning.DefaultSummary,
		DefaultProxyURL:   cfg.Accounts.DefaultProxyURL,
		UserAgent:         cfg.Upstream.UserAgent,
	}
}

// Load reads settings from the database, keeping the documented defaults for
// anything unset.
func (h *Holder) Load(ctx context.Context) error {
	loaded, err := h.store.LoadSettings(ctx, &h.defaults)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.current = loaded
	h.mu.Unlock()
	return nil
}

// Get returns the current settings (never nil after Load).
func (h *Holder) Get() *store.Settings {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.current == nil {
		clone := h.defaults
		return &clone
	}
	clone := *h.current
	return &clone
}

// Defaults returns the config-derived defaults.
func (h *Holder) Defaults() store.Settings { return h.defaults }

// Set persists and applies new settings.
func (h *Holder) Set(ctx context.Context, in *store.Settings) error {
	if err := h.store.SaveSettings(ctx, in); err != nil {
		return err
	}
	h.mu.Lock()
	h.current = in
	h.mu.Unlock()
	return nil
}
