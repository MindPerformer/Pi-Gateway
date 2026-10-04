package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Nullable token counts distinguish missing upstream usage from measured zeroes.
type UsageRecord struct {
	ID                int64
	RequestID         string
	APIKeyID          int64
	APIKeyName        string
	AccountID         int64
	AccountName       string
	Model             string
	ClientTransport   string
	UpstreamTransport string
	AttemptIndex      int
	Outcome           string
	StatusCode        int
	ErrorCode         string
	ErrorMessage      string
	UpstreamSendState string
	InputTokens       *int64
	CachedTokens      *int64
	CacheWriteTokens  *int64
	OutputTokens      *int64
	ReasoningTokens   *int64
	TotalTokens       *int64
	ConnectMS         int64
	HeadersMS         int64
	FirstEventMS      int64
	FirstTokenMS      int64
	LatencyMS         int64
	CostMicros        *int64
	CostSource        string
	PriceVersion      string
	SessionID         string
	StartedAt         int64
	CompletedAt       int64
}

// UsageRange uses Unix milliseconds and the half-open interval [Start, End).
// A zero bound is omitted, allowing callers to query all retained history.
type UsageRange struct{ Start, End int64 }
type UsageFilter struct {
	UsageRange
	APIKeyID  int64
	AccountID int64
	Model     string
	Outcome   string
	Search    string
	Limit     int
	Offset    int
}
type UsagePercentiles struct{ P50, P90, P95 float64 }
type UsageSummary struct {
	RequestCount, SuccessCount, FailureCount, CancelledCount, IncompleteCount int64
	InputTokens, CachedTokens, CacheWriteTokens                               int64
	OutputTokens, ReasoningTokens, TotalTokens                                int64
	CacheEligibleRequests, CacheHitRequests                                   int64
	AvgLatencyMS                                                              float64
	TTFT                                                                      UsagePercentiles
	OutputTPS                                                                 UsagePercentiles
	CostMicros                                                                int64
}
type UsageBucket struct {
	BucketStart                                          int64
	RequestCount                                         int64
	InputTokens, OutputTokens, CachedTokens, TotalTokens int64
	AvgLatencyMS, TTFTMS, OutputTPS                      float64
	CostMicros                                           int64
}
type UsageDimension struct {
	Key                                                  string
	ID                                                   int64
	RequestCount                                         int64
	SuccessCount                                         int64
	FailureCount                                         int64
	InputTokens, OutputTokens, CachedTokens, TotalTokens int64
	AvgLatencyMS, TTFTMS, OutputTPS                      float64
	CostMicros                                           int64
	Share                                                float64
}

const usageColumns = `id,request_id,api_key_id,api_key_name,account_id,account_name,model,
 client_transport,upstream_transport,attempt_index,outcome,status_code,error_code,error_message,upstream_send_state,
 input_tokens,cached_tokens,cache_write_tokens,output_tokens,reasoning_tokens,total_tokens,
 connect_ms,headers_ms,first_event_ms,first_token_ms,latency_ms,cost_micros,cost_source,price_version,session_id,started_at,completed_at`

func (s *Store) StartUsageRecord(ctx context.Context, r *UsageRecord) (int64, error) {
	if r == nil {
		return 0, errors.New("store: nil usage record")
	}
	if r.StartedAt == 0 {
		r.StartedAt = NowMS()
	}
	id, err := s.insertID(ctx, s, `INSERT INTO usage_records
 (request_id,api_key_id,api_key_name,account_id,account_name,model,client_transport,upstream_transport,attempt_index,outcome,upstream_send_state,session_id,started_at)
 VALUES (?,?,?,?,?,?,?,?,?,'running',?,?,?)`, r.RequestID, r.APIKeyID, r.APIKeyName, r.AccountID, r.AccountName, r.Model, r.ClientTransport, r.UpstreamTransport, r.AttemptIndex, r.UpstreamSendState, r.SessionID, r.StartedAt)
	if err != nil {
		return 0, fmt.Errorf("store: start usage: %w", err)
	}
	r.ID, r.Outcome, r.CompletedAt = id, "running", 0
	return id, nil
}

func (s *Store) FinishUsageRecord(ctx context.Context, id int64, r *UsageRecord) error {
	if r == nil {
		return errors.New("store: nil usage record")
	}
	switch r.Outcome {
	case "succeeded", "failed", "cancelled", "incomplete":
	default:
		return fmt.Errorf("store: invalid terminal usage outcome %q", r.Outcome)
	}
	if r.CompletedAt == 0 {
		r.CompletedAt = NowMS()
	}
	res, err := s.ExecContext(ctx, `UPDATE usage_records SET outcome=?,status_code=?,error_code=?,error_message=?,upstream_send_state=?,
 input_tokens=?,cached_tokens=?,cache_write_tokens=?,output_tokens=?,reasoning_tokens=?,total_tokens=?,
 connect_ms=?,headers_ms=?,first_event_ms=?,first_token_ms=?,latency_ms=?,cost_micros=?,cost_source=?,price_version=?,completed_at=?,
 upstream_transport=CASE WHEN ?='' THEN upstream_transport ELSE ? END WHERE id=?`,
		r.Outcome, r.StatusCode, r.ErrorCode, r.ErrorMessage, r.UpstreamSendState, r.InputTokens, r.CachedTokens, r.CacheWriteTokens, r.OutputTokens, r.ReasoningTokens, r.TotalTokens,
		r.ConnectMS, r.HeadersMS, r.FirstEventMS, r.FirstTokenMS, r.LatencyMS, r.CostMicros, r.CostSource, r.PriceVersion, r.CompletedAt, r.UpstreamTransport, r.UpstreamTransport, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ReconcileRunningUsageRecords(ctx context.Context, olderThan int64) (int64, error) {
	res, err := s.ExecContext(ctx, `UPDATE usage_records SET outcome='incomplete',completed_at=? WHERE outcome='running' AND started_at<?`, NowMS(), olderThan)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) DeleteUsageRecordsBefore(ctx context.Context, cutoff int64, batch, maxBatches int) (int64, error) {
	if batch <= 0 || batch > 5000 {
		batch = 5000
	}
	if maxBatches <= 0 || maxBatches > 12 {
		maxBatches = 12
	}
	var total int64
	for i := 0; i < maxBatches; i++ {
		if i > 0 {
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return total, ctx.Err()
			case <-timer.C:
			}
		}
		res, err := s.ExecContext(ctx, `DELETE FROM usage_records WHERE id IN
   (SELECT id FROM usage_records WHERE outcome<>'running' AND completed_at<? ORDER BY completed_at,id LIMIT ?)`, cutoff, batch)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < int64(batch) {
			break
		}
	}
	return total, nil
}

func usageWhere(f UsageFilter) (string, []any) {
	where, args := []string{"1=1"}, []any{}
	if f.Start != 0 {
		where = append(where, "started_at>=?")
		args = append(args, f.Start)
	}
	if f.End != 0 {
		where = append(where, "started_at<?")
		args = append(args, f.End)
	}
	if f.APIKeyID != 0 {
		where = append(where, "api_key_id=?")
		args = append(args, f.APIKeyID)
	}
	if f.AccountID != 0 {
		where = append(where, "account_id=?")
		args = append(args, f.AccountID)
	}
	if f.Model != "" {
		where = append(where, "model=?")
		args = append(args, f.Model)
	}
	if f.Outcome != "" {
		where = append(where, "outcome=?")
		args = append(args, f.Outcome)
	}
	if f.Search != "" {
		// Treat user search as literal text rather than allowing LIKE wildcards.
		where = append(where, `(instr(lower(request_id),lower(?))>0 OR instr(lower(api_key_name),lower(?))>0 OR
   instr(lower(account_name),lower(?))>0 OR instr(lower(model),lower(?))>0 OR
   instr(lower(error_code),lower(?))>0 OR instr(lower(error_message),lower(?))>0 OR instr(lower(session_id),lower(?))>0)`)
		for i := 0; i < 7; i++ {
			args = append(args, f.Search)
		}
	}
	return strings.Join(where, " AND "), args
}

func scanUsage(row interface{ Scan(...any) error }) (UsageRecord, error) {
	var r UsageRecord
	err := row.Scan(&r.ID, &r.RequestID, &r.APIKeyID, &r.APIKeyName, &r.AccountID, &r.AccountName, &r.Model,
		&r.ClientTransport, &r.UpstreamTransport, &r.AttemptIndex, &r.Outcome, &r.StatusCode, &r.ErrorCode, &r.ErrorMessage, &r.UpstreamSendState,
		&r.InputTokens, &r.CachedTokens, &r.CacheWriteTokens, &r.OutputTokens, &r.ReasoningTokens, &r.TotalTokens,
		&r.ConnectMS, &r.HeadersMS, &r.FirstEventMS, &r.FirstTokenMS, &r.LatencyMS, &r.CostMicros, &r.CostSource, &r.PriceVersion, &r.SessionID, &r.StartedAt, &r.CompletedAt)
	return r, err
}

func (s *Store) ListUsageRecords(ctx context.Context, f UsageFilter) (records []UsageRecord, total int64, err error) {
	where, args := usageWhere(f)
	tx, err := s.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM usage_records WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+usageColumns+` FROM usage_records WHERE `+where+` ORDER BY started_at DESC,id DESC LIMIT ? OFFSET ?`, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	records = []UsageRecord{}
	for rows.Next() {
		r, e := scanUsage(rows)
		if e != nil {
			rows.Close()
			return nil, 0, e
		}
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	return records, total, tx.Commit()
}

// Only valid generation intervals contribute to TPS; missing/zero output and
// latency<=TTFT are NULL, not zero samples in averages or percentiles.
const outputTPSExpr = `CASE WHEN output_tokens>0 AND latency_ms>first_token_ms
 THEN output_tokens*1000.0/max(latency_ms-first_token_ms,1) END`

func percentileSQL(table, ratio string) string {
	// This is the frozen SQLite order statistic, not interpolation/nearest rank.
	return `coalesce((SELECT x FROM ` + table + ` ORDER BY x LIMIT 1 OFFSET
  (SELECT CAST(count(*)*` + ratio + ` AS INTEGER) FROM ` + table + `)),0)`
}

func (s *Store) UsageSummaryFor(ctx context.Context, f UsageFilter) (*UsageSummary, error) {
	where, args := usageWhere(f)
	query := `WITH filtered AS (SELECT * FROM usage_records WHERE ` + where + `),
 ttft AS (SELECT first_token_ms AS x FROM filtered),
 tps AS (SELECT ` + outputTPSExpr + ` AS x FROM filtered WHERE output_tokens>0 AND latency_ms>first_token_ms)
 SELECT count(*),coalesce(sum(CASE WHEN outcome='succeeded' THEN 1 ELSE 0 END),0),coalesce(sum(CASE WHEN outcome='failed' THEN 1 ELSE 0 END),0),
 coalesce(sum(CASE WHEN outcome='cancelled' THEN 1 ELSE 0 END),0),coalesce(sum(CASE WHEN outcome='incomplete' THEN 1 ELSE 0 END),0),
 coalesce(sum(input_tokens),0),coalesce(sum(cached_tokens),0),coalesce(sum(cache_write_tokens),0),
 coalesce(sum(output_tokens),0),coalesce(sum(reasoning_tokens),0),coalesce(sum(total_tokens),0),
 count(input_tokens),coalesce(sum(CASE WHEN cached_tokens>0 THEN 1 ELSE 0 END),0),coalesce(avg(latency_ms),0),coalesce(sum(cost_micros),0),` +
		percentileSQL("ttft", "0.5") + `,` + percentileSQL("ttft", "0.9") + `,` + percentileSQL("ttft", "0.95") + `,` +
		percentileSQL("tps", "0.5") + `,` + percentileSQL("tps", "0.9") + `,` + percentileSQL("tps", "0.95") + ` FROM filtered`
	var u UsageSummary
	err := s.QueryRowContext(ctx, query, args...).Scan(&u.RequestCount, &u.SuccessCount, &u.FailureCount, &u.CancelledCount, &u.IncompleteCount,
		&u.InputTokens, &u.CachedTokens, &u.CacheWriteTokens, &u.OutputTokens, &u.ReasoningTokens, &u.TotalTokens,
		&u.CacheEligibleRequests, &u.CacheHitRequests, &u.AvgLatencyMS, &u.CostMicros,
		&u.TTFT.P50, &u.TTFT.P90, &u.TTFT.P95, &u.OutputTPS.P50, &u.OutputTPS.P90, &u.OutputTPS.P95)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// Shared means use all latency/TTFT samples, and only eligible TPS samples.
const usageMetricSums = `coalesce(sum(input_tokens),0),coalesce(sum(output_tokens),0),
 coalesce(sum(cached_tokens),0),coalesce(sum(total_tokens),0),coalesce(avg(latency_ms),0),
 coalesce(avg(first_token_ms),0),coalesce(avg(` + outputTPSExpr + `),0),coalesce(sum(cost_micros),0)`

func (s *Store) UsageTrend(ctx context.Context, f UsageFilter, bucket string) ([]UsageBucket, error) {
	var size int64
	switch bucket {
	case "hour":
		size = 3600000
	case "day":
		size = 86400000
	default:
		return nil, fmt.Errorf("store: invalid usage bucket %q", bucket)
	}
	where, args := usageWhere(f)
	// Floor to UTC boundaries, including timestamps before the Unix epoch.
	aligned := fmt.Sprintf(`started_at-((started_at%%%d+%d)%%%d)`, size, size, size)
	rows, err := s.QueryContext(ctx, `SELECT `+aligned+` AS bucket_start,count(*),`+usageMetricSums+
		` FROM usage_records WHERE `+where+` GROUP BY bucket_start ORDER BY bucket_start`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageBucket{}
	for rows.Next() {
		var b UsageBucket
		if err := rows.Scan(&b.BucketStart, &b.RequestCount, &b.InputTokens, &b.OutputTokens, &b.CachedTokens, &b.TotalTokens, &b.AvgLatencyMS, &b.TTFTMS, &b.OutputTPS, &b.CostMicros); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) usageDimensions(ctx context.Context, f UsageFilter, kind string) ([]UsageDimension, error) {
	var group, key, id string
	switch kind {
	case "model":
		group, key, id = "model", "model", "0"
	case "key":
		group, key, id = "api_key_id", "max(api_key_name)", "api_key_id"
	case "account":
		group, key, id = "account_id", "max(account_name)", "account_id"
	default:
		return nil, fmt.Errorf("store: invalid usage dimension %q", kind)
	}
	where, args := usageWhere(f)
	rows, err := s.QueryContext(ctx, `WITH filtered AS (SELECT * FROM usage_records WHERE `+where+`)
 SELECT `+key+`,`+id+`,count(*) AS request_count,coalesce(sum(CASE WHEN outcome='succeeded' THEN 1 ELSE 0 END),0),coalesce(sum(CASE WHEN outcome='failed' THEN 1 ELSE 0 END),0),`+
		usageMetricSums+`,count(*)*1.0/(SELECT count(*) FROM filtered)
 FROM filtered GROUP BY `+group+` ORDER BY request_count DESC,1,2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageDimension{}
	for rows.Next() {
		var d UsageDimension
		if err := rows.Scan(&d.Key, &d.ID, &d.RequestCount, &d.SuccessCount, &d.FailureCount, &d.InputTokens, &d.OutputTokens, &d.CachedTokens, &d.TotalTokens, &d.AvgLatencyMS, &d.TTFTMS, &d.OutputTPS, &d.CostMicros, &d.Share); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) UsageByModel(ctx context.Context, f UsageFilter) ([]UsageDimension, error) {
	return s.usageDimensions(ctx, f, "model")
}
func (s *Store) UsageByKey(ctx context.Context, f UsageFilter) ([]UsageDimension, error) {
	return s.usageDimensions(ctx, f, "key")
}
func (s *Store) UsageByAccount(ctx context.Context, f UsageFilter) ([]UsageDimension, error) {
	return s.usageDimensions(ctx, f, "account")
}
