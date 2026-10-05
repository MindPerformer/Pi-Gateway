package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Capture outcomes.
const (
	OutcomePending = "pending"
	OutcomeOK      = "ok"
	OutcomeError   = "error"
	OutcomeAborted = "aborted"
)

// Header is a captured wire header. Order is preserved as observed.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Frame is one direction-stamped unit of wire traffic.
//
// Dir is "out" for bytes sent to the upstream and "in" for bytes received from it.
// For the WebSocket transport a single exchange produces an "out" frame holding the
// response.create envelope followed by one "in" frame per server event, which is what
// makes a full round-trip replayable.
type Frame struct {
	Seq         int    `json:"seq"`
	Dir         string `json:"dir"`
	Kind        string `json:"kind"`
	Type        string `json:"type,omitempty"`
	AtMS        int64  `json:"at_ms"`
	Bytes       int    `json:"bytes"`
	Data        any    `json:"data,omitempty"`
	Text        string `json:"text,omitempty"`
	RuleEventID string `json:"rule_event_id,omitempty"`
}

// Frame kinds.
const (
	KindHandshakeRequest  = "handshake_request"
	KindHandshakeResponse = "handshake_response"
	KindRequestBody       = "request_body"
	KindHTTPResponse      = "http_response"
	KindSSEEvent          = "sse_event"
	KindWSFrame           = "ws_frame"
	KindWSError           = "ws_error"
	KindClosed            = "closed"
)

// Capture is one captured client->upstream exchange.
type Capture struct {
	ID                  int64             `json:"id"`
	AccountID           int64             `json:"account_id"`
	AccountName         string            `json:"account_name"`
	APIKeyID            int64             `json:"api_key_id"`
	APIKeyName          string            `json:"api_key_name"`
	ClientTransport     string            `json:"client_transport"`
	UpstreamTransport   string            `json:"upstream_transport"`
	URL                 string            `json:"url"`
	Model               string            `json:"model"`
	SessionID           string            `json:"session_id"`
	Status              int               `json:"status"`
	Outcome             string            `json:"outcome"`
	Error               string            `json:"error"`
	DurationMS          int64             `json:"duration_ms"`
	TTFBMS              int64             `json:"ttfb_ms"`
	RequestHeaders      []Header          `json:"request_headers"`
	RequestBody         string            `json:"request_body"`
	RequestBytes        int64             `json:"request_bytes"`
	ResponseHeaders     []Header          `json:"response_headers"`
	ResponseFrames      []Frame           `json:"response_frames"`
	ResponseText        string            `json:"response_text"`
	ResponseID          string            `json:"response_id"`
	PromptTokens        int64             `json:"prompt_tokens"`
	CompletionTokens    int64             `json:"completion_tokens"`
	TotalTokens         int64             `json:"total_tokens"`
	Truncated           bool              `json:"truncated"`
	CreatedAt           int64             `json:"created_at"`
	RuleTraces          []json.RawMessage `json:"rule_traces"`
	RulesVersion        int64             `json:"rules_version"`
	RulesTraceTruncated bool              `json:"rules_trace_truncated"`
	RulesTraceOmitted   int               `json:"rules_trace_omitted"`
}

const captureColumns = `id, account_id, account_name, api_key_id, api_key_name, client_transport,
	upstream_transport, url, model, session_id, status, outcome, error, duration_ms, ttfb_ms,
	request_headers, request_body, request_bytes, response_headers, response_frames, response_text,
	response_id, prompt_tokens, completion_tokens, total_tokens, truncated, created_at,
	rule_traces, rules_version, rules_trace_truncated, rules_trace_omitted`

func scanCapture(row interface{ Scan(...any) error }) (*Capture, error) {
	var c Capture
	var reqHeaders, respHeaders, frames, traces string
	var truncated, traceTruncated int
	if err := row.Scan(&c.ID, &c.AccountID, &c.AccountName, &c.APIKeyID, &c.APIKeyName, &c.ClientTransport,
		&c.UpstreamTransport, &c.URL, &c.Model, &c.SessionID, &c.Status, &c.Outcome, &c.Error,
		&c.DurationMS, &c.TTFBMS, &reqHeaders, &c.RequestBody, &c.RequestBytes, &respHeaders,
		&frames, &c.ResponseText, &c.ResponseID, &c.PromptTokens, &c.CompletionTokens,
		&c.TotalTokens, &truncated, &c.CreatedAt, &traces, &c.RulesVersion,
		&traceTruncated, &c.RulesTraceOmitted); err != nil {
		return nil, err
	}
	c.Truncated = truncated == 1
	c.RulesTraceTruncated = traceTruncated == 1
	c.RuleTraces = []json.RawMessage{}
	if traces != "" && traces != "null" {
		if err := jsonUnmarshal(traces, &c.RuleTraces); err != nil {
			c.RuleTraces = []json.RawMessage{}
			c.RulesTraceTruncated = true
		}
	}
	if c.RuleTraces == nil {
		c.RuleTraces = []json.RawMessage{}
	}
	c.RequestHeaders = []Header{}
	c.ResponseHeaders = []Header{}
	c.ResponseFrames = []Frame{}
	if reqHeaders != "" {
		_ = jsonUnmarshal(reqHeaders, &c.RequestHeaders)
	}
	if respHeaders != "" {
		_ = jsonUnmarshal(respHeaders, &c.ResponseHeaders)
	}
	if frames != "" {
		_ = jsonUnmarshal(frames, &c.ResponseFrames)
	}
	return &c, nil
}

// InsertCapture stores a capture and trims the owning account's history to limit records.
// A limit <= 0 disables the trim.
func (s *Store) InsertCapture(ctx context.Context, c *Capture, limit int) error {
	if c.CreatedAt == 0 {
		c.CreatedAt = NowMS()
	}
	if c.Outcome == "" {
		c.Outcome = OutcomeOK
	}
	reqHeaders, err := jsonMarshal(orEmptyHeaders(c.RequestHeaders))
	if err != nil {
		return fmt.Errorf("store: encode request headers: %w", err)
	}
	respHeaders, err := jsonMarshal(orEmptyHeaders(c.ResponseHeaders))
	if err != nil {
		return fmt.Errorf("store: encode response headers: %w", err)
	}
	frames, err := jsonMarshal(orEmptyFrames(c.ResponseFrames))
	if err != nil {
		return fmt.Errorf("store: encode frames: %w", err)
	}

	traceValues := c.RuleTraces
	if traceValues == nil {
		traceValues = []json.RawMessage{}
	}
	traces, err := jsonMarshal(traceValues)
	if err != nil {
		return fmt.Errorf("store: encode rule traces: %w", err)
	}

	id, err := s.insertID(ctx, s, `INSERT INTO captures
		(account_id, account_name, api_key_id, api_key_name, client_transport, upstream_transport, url,
		 model, session_id, status, outcome, error, duration_ms, ttfb_ms, request_headers, request_body,
		 request_bytes, response_headers, response_frames, response_text, response_id,
		 prompt_tokens, completion_tokens, total_tokens, truncated, created_at,
		 rule_traces, rules_version, rules_trace_truncated, rules_trace_omitted)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.AccountID, c.AccountName, c.APIKeyID, c.APIKeyName, c.ClientTransport, c.UpstreamTransport,
		c.URL, c.Model, c.SessionID, c.Status, c.Outcome, c.Error, c.DurationMS, c.TTFBMS,
		reqHeaders, c.RequestBody, c.RequestBytes, respHeaders, frames, c.ResponseText, c.ResponseID,
		c.PromptTokens, c.CompletionTokens, c.TotalTokens, boolToInt(c.Truncated), c.CreatedAt,
		traces, c.RulesVersion, boolToInt(c.RulesTraceTruncated), c.RulesTraceOmitted)
	if err != nil {
		return fmt.Errorf("store: insert capture: %w", err)
	}
	c.ID = id

	if limit > 0 {
		if err := s.trimAccountCaptures(ctx, c.AccountID, limit); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) trimAccountCaptures(ctx context.Context, accountID int64, limit int) error {
	// Keep the newest `limit` rows for this account; ids are monotonic so ranking by id is enough.
	_, err := s.ExecContext(ctx, `DELETE FROM captures WHERE account_id = ? AND id NOT IN (
		SELECT id FROM captures WHERE account_id = ? ORDER BY id DESC LIMIT ?
	)`, accountID, accountID, limit)
	if err != nil {
		return fmt.Errorf("store: trim captures: %w", err)
	}
	return nil
}

func orEmptyHeaders(h []Header) []Header {
	if h == nil {
		return []Header{}
	}
	return h
}

func orEmptyFrames(f []Frame) []Frame {
	if f == nil {
		return []Frame{}
	}
	return f
}

// CaptureFilter narrows a capture listing.
type CaptureFilter struct {
	AccountID       int64
	APIKeyID        int64
	Outcome         string
	Transport       string
	Search          string
	Limit           int
	Offset          int
	IncludePayloads bool
	Unassigned      bool
}

// ListCaptures returns captures matching the filter, newest first.
func (s *Store) ListCaptures(ctx context.Context, f CaptureFilter) ([]*Capture, int, error) {
	where, args := f.sqlWhere()

	var total int
	if err := s.QueryRowContext(ctx, `SELECT COUNT(*) FROM captures `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: count captures: %w", err)
	}

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	// Listing omits the heavy payload columns unless explicitly requested.
	cols := `id, account_id, account_name, api_key_id, api_key_name, client_transport,
		upstream_transport, url, model, session_id, status, outcome, error, duration_ms, ttfb_ms,
		request_headers, '' AS request_body, request_bytes, response_headers, '[]' AS response_frames,
		'' AS response_text, response_id, prompt_tokens, completion_tokens, total_tokens, truncated, created_at,
		'[]' AS rule_traces, rules_version, rules_trace_truncated, rules_trace_omitted`
	if f.IncludePayloads {
		cols = captureColumns
	}

	query := `SELECT ` + cols + ` FROM captures ` + where + ` ORDER BY id DESC LIMIT ? OFFSET ?`
	rows, err := s.QueryContext(ctx, query, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: list captures: %w", err)
	}
	defer rows.Close()

	out := []*Capture{}
	for rows.Next() {
		c, err := scanCapture(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("store: scan capture: %w", err)
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

func (f CaptureFilter) sqlWhere() (string, []any) {
	clauses := []string{}
	args := []any{}
	if f.Unassigned {
		clauses = append(clauses, "account_id = 0")
	} else if f.AccountID > 0 {
		clauses = append(clauses, "account_id = ?")
		args = append(args, f.AccountID)
	}
	if f.APIKeyID > 0 {
		clauses = append(clauses, "api_key_id = ?")
		args = append(args, f.APIKeyID)
	}
	if f.Outcome != "" {
		clauses = append(clauses, "outcome = ?")
		args = append(args, f.Outcome)
	}
	if f.Transport != "" {
		clauses = append(clauses, "(client_transport = ? OR upstream_transport = ?)")
		args = append(args, f.Transport, f.Transport)
	}
	if f.Search != "" {
		clauses = append(clauses, "(model LIKE ? OR session_id LIKE ? OR account_name LIKE ? OR api_key_name LIKE ? OR error LIKE ?)")
		like := "%" + f.Search + "%"
		args = append(args, like, like, like, like, like)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// GetCapture loads a capture with all payloads.
func (s *Store) GetCapture(ctx context.Context, id int64) (*Capture, error) {
	row := s.QueryRowContext(ctx, `SELECT `+captureColumns+` FROM captures WHERE id=?`, id)
	c, err := scanCapture(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get capture: %w", err)
	}
	return c, nil
}

// ClearUnassignedCaptures removes only captures that were recorded before account selection.
// It is intentionally separate from ClearCaptures(0), whose historical behavior clears all rows.
func (s *Store) ClearUnassignedCaptures(ctx context.Context) (int64, error) {
	res, err := s.ExecContext(ctx, `DELETE FROM captures WHERE account_id=0`)
	if err != nil {
		return 0, fmt.Errorf("store: clear unassigned captures: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// DeleteCapture removes one capture.
func (s *Store) DeleteCapture(ctx context.Context, id int64) error {
	if _, err := s.ExecContext(ctx, `DELETE FROM captures WHERE id=?`, id); err != nil {
		return fmt.Errorf("store: delete capture: %w", err)
	}
	return nil
}

// ClearCaptures removes captures, optionally scoped to a single account.
func (s *Store) ClearCaptures(ctx context.Context, accountID int64) (int64, error) {
	var res sql.Result
	var err error
	if accountID > 0 {
		res, err = s.ExecContext(ctx, `DELETE FROM captures WHERE account_id=?`, accountID)
	} else {
		res, err = s.ExecContext(ctx, `DELETE FROM captures`)
	}
	if err != nil {
		return 0, fmt.Errorf("store: clear captures: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// CaptureStats summarises capture volume for the dashboard.
type CaptureStats struct {
	Total     int64 `json:"total"`
	OK        int64 `json:"ok"`
	Errors    int64 `json:"errors"`
	AvgMS     int64 `json:"avg_ms"`
	TokensIn  int64 `json:"tokens_in"`
	TokensOut int64 `json:"tokens_out"`
}

// Stats aggregates capture counters over the last N records.
func (s *Store) Stats(ctx context.Context, sample int) (*CaptureStats, error) {
	if sample <= 0 {
		sample = 500
	}
	var st CaptureStats
	err := s.QueryRowContext(ctx, `SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN outcome='ok' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN outcome='error' THEN 1 ELSE 0 END),0),
			COALESCE(CAST(AVG(NULLIF(duration_ms,0)) AS INTEGER),0),
			COALESCE(SUM(prompt_tokens),0),
			COALESCE(SUM(completion_tokens),0)
		FROM (SELECT * FROM captures ORDER BY id DESC LIMIT ?) AS sampled`, sample).
		Scan(&st.Total, &st.OK, &st.Errors, &st.AvgMS, &st.TokensIn, &st.TokensOut)
	if err != nil {
		return nil, fmt.Errorf("store: capture stats: %w", err)
	}
	return &st, nil
}
