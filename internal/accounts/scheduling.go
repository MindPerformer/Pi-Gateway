package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"pi-gateway/internal/store"
)

// SetSettings applies a complete runtime snapshot. Call after PUT /api/settings.
func (m *Manager) SetSettings(s *store.Settings) {
	if s == nil {
		return
	}
	w, err := parseSmartWeights(s.SmartSchedulingJSON)
	m.mu.Lock()
	defer m.mu.Unlock()
	// The global strategy shown and edited by the admin UI is authoritative.
	// Retain the legacy rotation setting only when that value is absent.
	m.rotationStrategy = s.DefaultStrategy
	if m.rotationStrategy == "" {
		m.rotationStrategy = s.RotationStrategy
	}
	if m.rotationStrategy == "" {
		m.rotationStrategy = string(StrategySmart)
	}
	m.requestInterval = time.Duration(max(0, s.RequestIntervalMS)) * time.Millisecond
	m.accountCooldown = time.Duration(max(0, s.AccountCooldownSeconds)) * time.Second
	m.waitTimeout = time.Duration(max(0, s.ConcurrencyWaitTimeoutSeconds)) * time.Second
	m.maxAttempts = max(0, s.MaxAttempts)
	m.maxPerAccount = max(0, s.MaxPerAccount)
	if s.RefreshMarginSecs > 0 {
		m.refreshMargin = time.Duration(s.RefreshMarginSecs) * time.Second
	}
	m.smartWeights = w
	m.warnSmartLocked(err)
}

// The JSON accepts the six named fields or a six-element "weights" array.
// Unspecified fields retain defaults; an explicit zero disables a signal.
func parseSmartWeights(raw string) (SmartWeights, error) {
	w := DefaultSmartWeights()
	if strings.TrimSpace(raw) == "" {
		return w, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return w, errors.New("smart scheduling must be a JSON object")
	}
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		return DefaultSmartWeights(), err
	}
	var extra struct {
		Weights json.RawMessage `json:"weights"`
	}
	if err := json.Unmarshal([]byte(raw), &extra); err != nil {
		return DefaultSmartWeights(), err
	}
	if len(extra.Weights) > 0 {
		var values []int
		if err := json.Unmarshal(extra.Weights, &values); err != nil {
			return DefaultSmartWeights(), err
		}
		if len(values) != 6 {
			return DefaultSmartWeights(), errors.New("smart weights must contain six values")
		}
		w.Load, w.Quota, w.Health, w.Latency, w.Reset, w.Queue = values[0], values[1], values[2], values[3], values[4], values[5]
	}
	if err := validateSmartWeights(w); err != nil {
		return DefaultSmartWeights(), err
	}
	return w, nil
}

func validateSmartWeights(w SmartWeights) error {
	for _, v := range []int{w.Load, w.Quota, w.Health, w.Latency, w.Reset, w.Queue} {
		if v < 0 {
			return errors.New("smart weights must be nonnegative")
		}
	}
	if w.ScoreTolerance < 0 || math.IsNaN(w.ScoreTolerance) || math.IsInf(w.ScoreTolerance, 0) {
		return errors.New("invalid score tolerance")
	}
	return nil
}

func (m *Manager) warnSmartLocked(err error) {
	if err != nil && !m.warnedSmartConfig {
		m.logger.Warn("invalid smart_scheduling_json; using default weights", "error", err)
		m.warnedSmartConfig = true
	}
}

// SetRotationStrategy updates the process default; a key Strategy still wins.
func (m *Manager) SetRotationStrategy(v string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rotationStrategy = strings.TrimSpace(v)
	if m.rotationStrategy == "" {
		m.rotationStrategy = string(StrategySmart)
	}
}
func (m *Manager) SetRequestInterval(d time.Duration) {
	if d >= 0 {
		m.mu.Lock()
		m.requestInterval = d
		m.mu.Unlock()
	}
}
func (m *Manager) SetRequestIntervalMS(n int) {
	if n >= 0 {
		m.SetRequestInterval(time.Duration(n) * time.Millisecond)
	}
}
func (m *Manager) SetAccountCooldown(d time.Duration) {
	if d >= 0 {
		m.mu.Lock()
		m.accountCooldown = d
		m.mu.Unlock()
	}
}
func (m *Manager) SetAccountCooldownSeconds(n int) {
	if n >= 0 {
		m.SetAccountCooldown(time.Duration(n) * time.Second)
	}
}
func (m *Manager) SetConcurrencyWaitTimeout(d time.Duration) {
	if d >= 0 {
		m.mu.Lock()
		m.waitTimeout = d
		m.mu.Unlock()
	}
}
func (m *Manager) SetMaxAttempts(n int) {
	if n >= 0 {
		m.mu.Lock()
		m.maxAttempts = n
		m.mu.Unlock()
	}
}
func (m *Manager) SetSmartWeights(w SmartWeights) {
	err := validateSmartWeights(w)
	if err != nil {
		w = DefaultSmartWeights()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.smartWeights = w
	m.warnSmartLocked(err)
}
func (m *Manager) SetSmartSchedulingJSON(raw string) {
	w, err := parseSmartWeights(raw)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.smartWeights = w
	m.warnSmartLocked(err)
}

// MaxAttempts exposes the retry budget; callers must only retry not-sent requests.
func (m *Manager) MaxAttempts() int { m.mu.Lock(); defer m.mu.Unlock(); return m.maxAttempts }

func (m *Manager) nowTime() time.Time {
	if m.now == nil {
		return time.Now()
	}
	return m.now()
}

// Result is reported once per upstream attempt by the API completion path.
// A zero FirstOutputMS means no latency sample. Health persistence does not
// duplicate MarkAccountUsed's request/error counters or freeze account capacity.
type Result struct {
	Failed         bool
	StatusCode     int
	FirstOutputMS  float64
	RetryAfter     time.Duration
	Error          string
	QuotaExhausted bool
	QuotaJSON      string
}

// RecordResult atomically updates current DB state, never a stale Account copy.
// Success resets failures but cannot erase another in-flight attempt's cooldown.
// Cold-start latency is seeded with the first sample; subsequent samples and
// failure rate use alpha=0.2. Timestamps are Unix milliseconds.
func (m *Manager) RecordResult(ctx context.Context, id int64, r Result, now time.Time) error {
	if r.FirstOutputMS < 0 || math.IsNaN(r.FirstOutputMS) || math.IsInf(r.FirstOutputMS, 0) {
		return errors.New("accounts: invalid latency sample")
	}
	if r.QuotaJSON != "" && !json.Valid([]byte(r.QuotaJSON)) {
		return errors.New("accounts: invalid quota JSON")
	}
	failed := r.Failed || r.StatusCode >= 400 || r.QuotaExhausted
	sample := 0
	if failed {
		sample = 10000
	}
	rateLimited := r.StatusCode == 429
	until := int64(0)
	if rateLimited {
		m.mu.Lock()
		cooldown := m.accountCooldown
		m.mu.Unlock()
		a, err := m.store.GetAccount(ctx, id)
		if err != nil {
			return err
		}
		if a != nil && a.Cooldown429Seconds >= 0 {
			cooldown = time.Duration(a.Cooldown429Seconds) * time.Second
		}
		until = now.Add(max(cooldown, r.RetryAfter)).UnixMilli()
	}
	status := ""
	switch {
	case r.QuotaExhausted:
		status = "quota_exhausted"
	case r.StatusCode == 401:
		status = store.AccountStatusInvalid
	case r.StatusCode == 403:
		status = store.AccountStatusBanned
	}
	res, err := m.store.ExecContext(ctx, `UPDATE accounts SET
 consecutive_failures=CASE WHEN ? THEN consecutive_failures+1 ELSE 0 END,
 ewma_failure_rate_bp=CAST(ROUND(0.8*ewma_failure_rate_bp+0.2*?) AS INTEGER),
 ewma_first_output_ms=CASE WHEN CAST(? AS DOUBLE PRECISION)<=0 THEN ewma_first_output_ms WHEN ewma_first_output_ms<=0 THEN ? ELSE 0.8*ewma_first_output_ms+0.2*? END,
 cooldown_until=CASE WHEN ? THEN MAX(cooldown_until,?) ELSE cooldown_until END,
 cooldown_kind=CASE WHEN ? THEN 'rate_limit' ELSE cooldown_kind END,
 status=CASE WHEN ?='' THEN status ELSE ? END,
 quota_json=CASE WHEN ?='' THEN quota_json ELSE ? END,
 quota_updated_at=CASE WHEN ?='' THEN quota_updated_at ELSE ? END,
 last_error=?,updated_at=? WHERE id=?`, failed, sample, r.FirstOutputMS, r.FirstOutputMS, r.FirstOutputMS,
		rateLimited, until, rateLimited, status, status, r.QuotaJSON, r.QuotaJSON, r.QuotaJSON, now.UnixMilli(), r.Error, now.UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("accounts: record result: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("accounts: result account not found")
	}
	return nil
}

func (m *Manager) RecordSuccess(ctx context.Context, a *store.Account, firstOutput time.Duration, now time.Time) error {
	if a == nil {
		return errors.New("accounts: nil account")
	}
	return m.RecordResult(ctx, a.ID, Result{FirstOutputMS: float64(firstOutput) / float64(time.Millisecond)}, now)
}

func (m *Manager) RecordFailure(ctx context.Context, a *store.Account, status string, retryAfter time.Duration, errText string, now time.Time) error {
	if a == nil {
		return errors.New("accounts: nil account")
	}
	r := Result{Failed: true, RetryAfter: retryAfter, Error: errText}
	switch status {
	case "429", "rate_limit", "rate_limit_exceeded":
		r.StatusCode = 429
	case "401", store.AccountStatusInvalid:
		r.StatusCode = 401
	case "403", store.AccountStatusBanned:
		r.StatusCode = 403
	case "quota_exhausted":
		r.QuotaExhausted = true
	}
	return m.RecordResult(ctx, a.ID, r, now)
}

func (m *Manager) MarkQuotaExhausted(ctx context.Context, a *store.Account, errText string) error {
	if a == nil {
		return errors.New("accounts: nil account")
	}
	return m.RecordResult(ctx, a.ID, Result{Failed: true, QuotaExhausted: true, Error: errText}, m.nowTime())
}
