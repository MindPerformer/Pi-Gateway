package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// UsageRepricingSchemaSQL is deliberately separate from the original billing
// ledger. Estimates never charge API-key budgets or rewrite settled bills.
const UsageRepricingSchemaSQL = `
CREATE TABLE IF NOT EXISTS usage_repriced_costs (
 usage_record_id INTEGER PRIMARY KEY REFERENCES usage_records(id) ON DELETE CASCADE,
 cost_micros INTEGER NOT NULL CHECK (cost_micros >= 0),
 price_version TEXT NOT NULL,
 calculated_at INTEGER NOT NULL,
 rate_snapshot TEXT NOT NULL,
 cost_source TEXT NOT NULL,
 preview_token TEXT NOT NULL,
 usage_fingerprint TEXT NOT NULL
);
`

// EnsureUsageRepricingSchema supports hosts which initialize pricing separately.
func (s *Store) EnsureUsageRepricingSchema(ctx context.Context) error {
	schema := UsageRepricingSchemaSQL
	if s.driver == "postgres" {
		schema = postgresSchema(schema)
	}
	_, err := s.ExecContext(ctx, schema)
	return err
}

var ErrUsageRepricingConflict = errors.New("usage changed since preview; create a new preview")

// UsageRepricingRecord contains precisely the data bound by a preview. Nullable
// token fields remain nullable: absence must never be silently priced as zero.
type UsageRepricingRecord struct {
	ID                   int64
	AccountID            int64
	Model                string
	ServiceTier          string
	RequestedServiceTier string
	Outcome              string
	UpstreamSendState    string
	StartedAt            int64
	CompletedAt          int64
	InputTokens          *int64
	CachedTokens         *int64
	CacheWriteTokens     *int64
	OutputTokens         *int64
	RepricedMicros       *int64
	RepricedPreviewToken string
}

func (r UsageRepricingRecord) Fingerprint() string {
	r.RepricedMicros, r.RepricedPreviewToken = nil, ""
	raw, _ := json.Marshal(r)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

const repricingRecordColumns = `u.id,u.account_id,u.model,u.service_tier,u.requested_service_tier,u.outcome,u.upstream_send_state,u.started_at,u.completed_at,
 u.input_tokens,u.cached_tokens,u.cache_write_tokens,u.output_tokens,p.cost_micros,coalesce(p.preview_token,'')`

func scanRepricingRecord(row interface{ Scan(...any) error }) (UsageRepricingRecord, error) {
	var r UsageRepricingRecord
	err := row.Scan(&r.ID, &r.AccountID, &r.Model, &r.ServiceTier, &r.RequestedServiceTier, &r.Outcome, &r.UpstreamSendState, &r.StartedAt, &r.CompletedAt,
		&r.InputTokens, &r.CachedTokens, &r.CacheWriteTokens, &r.OutputTokens, &r.RepricedMicros, &r.RepricedPreviewToken)
	return r, err
}

// ListUsageRepricingRecords scans a bounded ID page, including already estimated
// records so the UI can report them. Range bounds refer to request start time.
func (s *Store) ListUsageRepricingRecords(ctx context.Context, f UsageFilter, afterID int64) ([]UsageRepricingRecord, bool, error) {
	if f.Limit <= 0 {
		f.Limit = 500
	}
	if f.Limit > 2000 {
		f.Limit = 2000
	}
	where := `u.cost_micros IS NULL AND u.id>? AND u.completed_at>0
 AND u.outcome IN ('succeeded','failed','cancelled','incomplete')
 AND (u.outcome='succeeded' OR u.upstream_send_state='sent' OR u.input_tokens IS NOT NULL OR u.output_tokens IS NOT NULL)
 AND NOT (u.outcome='failed' AND u.upstream_send_state='not_sent')`
	args := []any{afterID}
	if f.Start != 0 {
		where += ` AND u.started_at>=?`
		args = append(args, f.Start)
	}
	if f.End != 0 {
		where += ` AND u.started_at<?`
		args = append(args, f.End)
	}
	if f.AccountID != 0 {
		where += ` AND u.account_id=?`
		args = append(args, f.AccountID)
	}
	if f.Model != "" {
		where += ` AND u.model=?`
		args = append(args, f.Model)
	}
	args = append(args, f.Limit+1)
	rows, err := s.QueryContext(ctx, `SELECT `+repricingRecordColumns+` FROM usage_records u LEFT JOIN usage_repriced_costs p ON p.usage_record_id=u.id WHERE `+where+` ORDER BY u.id LIMIT ?`, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	records := make([]UsageRepricingRecord, 0)
	for rows.Next() {
		r, err := scanRepricingRecord(rows)
		if err != nil {
			return nil, false, err
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(records) > f.Limit
	if more {
		records = records[:f.Limit]
	}
	return records, more, nil
}

// GetUsageRepricingRecords rechecks only the IDs captured by a preview. New
// requests arriving after preview do not invalidate an otherwise unchanged batch.
func (s *Store) GetUsageRepricingRecords(ctx context.Context, ids []int64) ([]UsageRepricingRecord, error) {
	if len(ids) == 0 {
		return []UsageRepricingRecord{}, nil
	}
	if len(ids) > 2000 {
		return nil, errors.New("usage repricing batch too large")
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	rows, err := s.QueryContext(ctx, `SELECT `+repricingRecordColumns+` FROM usage_records u LEFT JOIN usage_repriced_costs p ON p.usage_record_id=u.id WHERE u.cost_micros IS NULL AND u.id IN (`+marks+`) ORDER BY u.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]UsageRepricingRecord, 0, len(ids))
	for rows.Next() {
		r, err := scanRepricingRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

type UsageRepricingUpdate struct {
	Record       UsageRepricingRecord
	CostMicros   int64
	PriceVersion string
	RateSnapshot string
	CostSource   string
}

type UsageRepricingResult struct {
	Updated    int   `json:"updated"`
	Skipped    int   `json:"skipped"`
	CostMicros int64 `json:"cost_micros"`
}

// ApplyUsageRepricing is all-or-nothing. Every original record is rechecked in
// the transaction; CAS failure rolls back the entire batch, not a partial bill.
// The caller's validation runs again immediately before commit (price revision).
func (s *Store) ApplyUsageRepricing(ctx context.Context, token string, updates []UsageRepricingUpdate, validate func() error) (UsageRepricingResult, error) {
	if token == "" || len(updates) > 2000 {
		return UsageRepricingResult{}, errors.New("invalid usage repricing batch")
	}
	return withStoreTransaction(s, ctx, nil, func(tx *storeTx) (UsageRepricingResult, error) {
		var result UsageRepricingResult
		for _, update := range updates {
			if err := ctx.Err(); err != nil {
				return UsageRepricingResult{}, err
			}
			r := update.Record
			if update.CostMicros < 0 || r.InputTokens == nil || r.CachedTokens == nil || r.CacheWriteTokens == nil || r.OutputTokens == nil || update.PriceVersion == "" || update.RateSnapshot == "" || update.CostSource == "" {
				return UsageRepricingResult{}, errors.New("invalid usage repricing update")
			}
			terminal := r.Outcome == "succeeded" || r.Outcome == "failed" || r.Outcome == "cancelled" || r.Outcome == "incomplete"
			if !terminal || r.CompletedAt <= 0 || (r.Outcome == "failed" && r.UpstreamSendState == "not_sent") || *r.InputTokens < 0 || *r.CachedTokens < 0 || *r.CacheWriteTokens < 0 || *r.OutputTokens < 0 {
				return UsageRepricingResult{}, errors.New("ineligible usage repricing record")
			}
			// A row lock prevents concurrent settlement changes on PostgreSQL. SQLite
			// upgrades its transaction to a writer below and aborts on snapshot conflict.
			query := `SELECT ` + repricingRecordColumns + ` FROM usage_records u LEFT JOIN usage_repriced_costs p ON p.usage_record_id=u.id WHERE u.id=? AND u.cost_micros IS NULL`
			if s.driver == "postgres" {
				query += ` FOR UPDATE OF u`
			}
			current, err := scanRepricingRecord(tx.QueryRowContext(ctx, query, update.Record.ID))
			if errors.Is(err, sql.ErrNoRows) {
				return UsageRepricingResult{}, ErrUsageRepricingConflict
			}
			if err != nil {
				return UsageRepricingResult{}, err
			}
			if current.Fingerprint() != update.Record.Fingerprint() {
				return UsageRepricingResult{}, ErrUsageRepricingConflict
			}
			if current.RepricedMicros != nil {
				if current.RepricedPreviewToken != token || *current.RepricedMicros != update.CostMicros {
					return UsageRepricingResult{}, ErrUsageRepricingConflict
				}
				result.Skipped++
				continue
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO usage_repriced_costs(usage_record_id,cost_micros,price_version,calculated_at,rate_snapshot,cost_source,preview_token,usage_fingerprint)
 SELECT id,?,?,?,?,?,?,? FROM usage_records WHERE id=? AND cost_micros IS NULL
 AND model=? AND service_tier=? AND requested_service_tier=? AND outcome=? AND upstream_send_state=? AND started_at=? AND completed_at=?
 AND account_id=? AND input_tokens=? AND cached_tokens=? AND cache_write_tokens=? AND output_tokens=?
 ON CONFLICT(usage_record_id) DO NOTHING`, update.CostMicros, update.PriceVersion, NowMS(), update.RateSnapshot, update.CostSource, token, r.Fingerprint(), r.ID,
				r.Model, r.ServiceTier, r.RequestedServiceTier, r.Outcome, r.UpstreamSendState, r.StartedAt, r.CompletedAt, r.AccountID, *r.InputTokens, *r.CachedTokens, *r.CacheWriteTokens, *r.OutputTokens)
			if err != nil {
				return UsageRepricingResult{}, err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return UsageRepricingResult{}, err
			}
			if n != 1 {
				return UsageRepricingResult{}, ErrUsageRepricingConflict
			}
			result.Updated++
			if result.CostMicros > int64(^uint64(0)>>1)-update.CostMicros {
				return UsageRepricingResult{}, fmt.Errorf("repricing total overflow")
			}
			result.CostMicros += update.CostMicros
		}
		if validate != nil {
			if err := validate(); err != nil {
				return UsageRepricingResult{}, err
			}
		}
		if err := tx.Commit(); err != nil {
			return UsageRepricingResult{}, err
		}
		return result, nil
	})
}

// EqualUsageRepricingRecords is useful for callers validating a whole preview,
// including rows which were unavailable rather than eligible for an estimate.
func EqualUsageRepricingRecords(a, b []UsageRepricingRecord) bool { return reflect.DeepEqual(a, b) }
