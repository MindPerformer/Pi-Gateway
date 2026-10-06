package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Settings holds the runtime-adjustable knobs that override config defaults.
type Settings struct {
	SwitchOn429 bool `json:"switch_on_429"`

	UpstreamTransport             string            `json:"upstream_transport"`
	CaptureEnabled                bool              `json:"capture_enabled"`
	CaptureLimit                  int               `json:"capture_limit"`
	DefaultModel                  string            `json:"default_model"`
	ModelMappings                 map[string]string `json:"model_mappings"`
	DefaultStrategy               string            `json:"default_strategy"`
	MaxPerAccount                 int               `json:"max_concurrent_per_account"`
	RefreshMarginSecs             int               `json:"refresh_margin_seconds"`
	ReasoningEffort               string            `json:"reasoning_default_effort"`
	ReasoningSummary              string            `json:"reasoning_default_summary"`
	DefaultProxyURL               string            `json:"default_proxy_url"`
	UserAgent                     string            `json:"user_agent"`
	RotationStrategy              string            `json:"rotation_strategy"`
	RequestIntervalMS             int               `json:"request_interval_ms"`
	MaxWaitingPerKey              int               `json:"max_waiting_per_key"`
	MaxWaitingPerAccount          int               `json:"max_waiting_per_account"`
	ConcurrencyWaitTimeoutSeconds int               `json:"concurrency_wait_timeout_seconds"`
	AccountCooldownSeconds        int               `json:"account_cooldown_seconds"`
	MaxAttempts                   int               `json:"max_attempts"`
	UsageRetentionDays            int               `json:"usage_retention_days"`
	SmartSchedulingJSON           string            `json:"smart_scheduling_json"`
	PricingOverridesJSON          string            `json:"pricing_overrides_json"`
}

const (
	settingPrefix = "settings."
)

// LoadSettings reads the runtime settings row, falling back to the supplied defaults.
func (s *Store) LoadSettings(ctx context.Context, defaults *Settings) (*Settings, error) {
	var out Settings
	if defaults != nil {
		out = *defaults
	}
	out.SwitchOn429 = true
	// New knobs have store-level defaults so older config callers also get the
	// frozen behavior. Persisted values (including zero) still override these.
	if out.RotationStrategy == "" {
		out.RotationStrategy = "smart"
	}
	if out.ConcurrencyWaitTimeoutSeconds == 0 {
		out.ConcurrencyWaitTimeoutSeconds = 30
	}
	if out.AccountCooldownSeconds == 0 {
		out.AccountCooldownSeconds = 60
	}
	if out.MaxAttempts == 0 {
		out.MaxAttempts = 2
	}
	if out.UsageRetentionDays == 0 {
		out.UsageRetentionDays = 31
	}
	if out.SmartSchedulingJSON == "" {
		out.SmartSchedulingJSON = "{}"
	}
	if out.PricingOverridesJSON == "" {
		out.PricingOverridesJSON = "{}"
	}
	rows, err := s.QueryContext(ctx, `SELECT key, value FROM settings WHERE key LIKE ?`, settingPrefix+"%")
	if err != nil {
		return nil, fmt.Errorf("store: load settings: %w", err)
	}
	defer rows.Close()

	values := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("store: scan setting: %w", err)
		}
		values[strings.TrimPrefix(k, settingPrefix)] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	getStr := func(name string) (string, bool) {
		v, ok := values[name]
		return v, ok
	}
	getInt := func(name string, dst *int) {
		if v, ok := values[name]; ok {
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				*dst = n
			}
		}
	}
	getBool := func(name string, dst *bool) {
		if v, ok := values[name]; ok {
			*dst = v == "true" || v == "1"
		}
	}

	if v, ok := getStr("upstream_transport"); ok && v != "" {
		out.UpstreamTransport = v
	}
	getBool("switch_on_429", &out.SwitchOn429)
	getBool("capture_enabled", &out.CaptureEnabled)
	getInt("capture_limit", &out.CaptureLimit)
	if v, ok := getStr("default_model"); ok {
		out.DefaultModel = v
	}
	if v, ok := getStr("model_mappings"); ok && v != "" {
		m := map[string]string{}
		if err := jsonUnmarshal(v, &m); err == nil {
			out.ModelMappings = m
		}
	}
	if v, ok := getStr("default_strategy"); ok && v != "" {
		out.DefaultStrategy = v
	}
	getInt("max_concurrent_per_account", &out.MaxPerAccount)
	getInt("refresh_margin_seconds", &out.RefreshMarginSecs)
	if v, ok := getStr("reasoning_default_effort"); ok {
		out.ReasoningEffort = v
	}
	if v, ok := getStr("reasoning_default_summary"); ok {
		out.ReasoningSummary = v
	}
	if v, ok := getStr("default_proxy_url"); ok {
		out.DefaultProxyURL = v
	}
	if v, ok := getStr("user_agent"); ok {
		out.UserAgent = v
	}
	if v, ok := getStr("rotation_strategy"); ok && v != "" {
		out.RotationStrategy = v
	}
	getInt("request_interval_ms", &out.RequestIntervalMS)
	getInt("max_waiting_per_key", &out.MaxWaitingPerKey)
	getInt("max_waiting_per_account", &out.MaxWaitingPerAccount)
	getInt("concurrency_wait_timeout_seconds", &out.ConcurrencyWaitTimeoutSeconds)
	getInt("account_cooldown_seconds", &out.AccountCooldownSeconds)
	getInt("max_attempts", &out.MaxAttempts)
	getInt("usage_retention_days", &out.UsageRetentionDays)
	if v, ok := getStr("smart_scheduling_json"); ok && v != "" {
		out.SmartSchedulingJSON = v
	}
	if v, ok := getStr("pricing_overrides_json"); ok && v != "" {
		out.PricingOverridesJSON = v
	}
	return &out, nil
}

// SaveSettings writes the runtime settings row.
func (s *Store) SaveSettings(ctx context.Context, in *Settings) error {
	mappings, err := jsonMarshal(in.ModelMappings)
	if err != nil {
		return fmt.Errorf("store: encode model mappings: %w", err)
	}
	pairs := map[string]string{
		"switch_on_429":                    fmt.Sprintf("%t", in.SwitchOn429),
		"upstream_transport":               in.UpstreamTransport,
		"capture_enabled":                  fmt.Sprintf("%t", in.CaptureEnabled),
		"capture_limit":                    fmt.Sprintf("%d", in.CaptureLimit),
		"default_model":                    in.DefaultModel,
		"model_mappings":                   mappings,
		"default_strategy":                 in.DefaultStrategy,
		"max_concurrent_per_account":       fmt.Sprintf("%d", in.MaxPerAccount),
		"refresh_margin_seconds":           fmt.Sprintf("%d", in.RefreshMarginSecs),
		"reasoning_default_effort":         in.ReasoningEffort,
		"reasoning_default_summary":        in.ReasoningSummary,
		"default_proxy_url":                in.DefaultProxyURL,
		"user_agent":                       in.UserAgent,
		"rotation_strategy":                in.RotationStrategy,
		"request_interval_ms":              fmt.Sprintf("%d", in.RequestIntervalMS),
		"max_waiting_per_key":              fmt.Sprintf("%d", in.MaxWaitingPerKey),
		"max_waiting_per_account":          fmt.Sprintf("%d", in.MaxWaitingPerAccount),
		"concurrency_wait_timeout_seconds": fmt.Sprintf("%d", in.ConcurrencyWaitTimeoutSeconds),
		"account_cooldown_seconds":         fmt.Sprintf("%d", in.AccountCooldownSeconds),
		"max_attempts":                     fmt.Sprintf("%d", in.MaxAttempts),
		"usage_retention_days":             fmt.Sprintf("%d", in.UsageRetentionDays),
		"smart_scheduling_json":            in.SmartSchedulingJSON,
		"pricing_overrides_json":           in.PricingOverridesJSON,
	}
	now := NowMS()
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin settings tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for k, v := range pairs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings (key, value, updated_at) VALUES (?,?,?)
			 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
			settingPrefix+k, v, now); err != nil {
			return fmt.Errorf("store: save setting %s: %w", k, err)
		}
	}
	return tx.Commit()
}

// GetSecret reads an internal (non-user-facing) settings value.
func (s *Store) GetSecret(ctx context.Context, key string) (string, error) {
	var v string
	err := s.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: get secret %s: %w", key, err)
	}
	return v, nil
}

// SetSecret writes an internal settings value.
func (s *Store) SetSecret(ctx context.Context, key, value string) error {
	_, err := s.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES (?,?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		key, value, NowMS())
	if err != nil {
		return fmt.Errorf("store: set secret %s: %w", key, err)
	}
	return nil
}

// RecordAudit appends an audit event.
func (s *Store) RecordAudit(ctx context.Context, action, detail string) error {
	if _, err := s.ExecContext(ctx, `INSERT INTO audit_events (action, detail, created_at) VALUES (?,?,?)`,
		action, detail, NowMS()); err != nil {
		return fmt.Errorf("store: record audit: %w", err)
	}
	return nil
}

// AuditEvent is a single administrative action record.
type AuditEvent struct {
	ID        int64  `json:"id"`
	Action    string `json:"action"`
	Detail    string `json:"detail"`
	CreatedAt int64  `json:"created_at"`
}

// ListAudit returns the most recent audit events.
func (s *Store) ListAudit(ctx context.Context, limit int) ([]*AuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.QueryContext(ctx,
		`SELECT id, action, detail, created_at FROM audit_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer rows.Close()

	out := []*AuditEvent{}
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.Action, &e.Detail, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan audit: %w", err)
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}
