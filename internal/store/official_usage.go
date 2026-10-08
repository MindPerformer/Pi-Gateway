package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The upstream query is workspace_user=true, so identity includes both workspace
// and user. Missing upstream counts remain NULL.
type OfficialUsageDay struct {
	AccountID           int64    `json:"account_id"`
	WorkspaceID         string   `json:"workspace_id"`
	Day                 string   `json:"day"`
	Credits             *float64 `json:"credits"`
	UncachedInputTokens *int64   `json:"uncached_input_tokens"`
	CachedInputTokens   *int64   `json:"cached_input_tokens"`
	OutputTokens        *int64   `json:"output_tokens"`
	TotalTokens         *int64   `json:"total_tokens"`
	Users               int64    `json:"users"`
	Threads             int64    `json:"threads"`
	Turns               int64    `json:"turns"`
	Settled             bool     `json:"settled"`
	Raw                 string   `json:"-"`
	SyncedAt            int64    `json:"synced_at"`
}

// OfficialUsageKey uses JWT metadata only for deduplication, never authorization.
// Distinct users in one Team workspace must retain distinct usage histories.
func OfficialUsageKey(a *Account) string {
	user := ""
	parts := strings.Split(a.CodexAccessToken, ".")
	if len(parts) == 3 {
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		var claims map[string]any
		if err == nil && json.Unmarshal(payload, &claims) == nil {
			if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
				user, _ = auth["chatgpt_user_id"].(string)
			}
			if user == "" {
				user, _ = claims["sub"].(string)
			}
		}
	}
	if user == "" {
		user = fmt.Sprintf("account:%d", a.ID)
	}
	sum := sha256.Sum256([]byte(a.CodexAccountID + "\x00" + user))
	return fmt.Sprintf("%x", sum)
}

type OfficialUsageSync struct {
	WorkspaceID string `json:"workspace_id"`
	SyncedAt    int64  `json:"synced_at"`
	AttemptedAt int64  `json:"attempted_at"`
	Error       string `json:"error,omitempty"`
}

func (s *Store) OfficialUsageSyncFor(ctx context.Context, workspace string) (OfficialUsageSync, error) {
	state := OfficialUsageSync{WorkspaceID: workspace}
	err := s.QueryRowContext(ctx, `SELECT synced_at,attempted_at,error FROM official_usage_sync WHERE workspace_id=?`, workspace).Scan(&state.SyncedAt, &state.AttemptedAt, &state.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	return state, err
}

// SaveOfficialUsage atomically upserts sparse days in the fetched interval.
// Later settlement corrections overwrite earlier values instead of adding them.
// The credential comparison prevents an unlinked/replaced credential's late write.
func (s *Store) SaveOfficialUsage(ctx context.Context, account *Account, start, end string, days []OfficialUsageDay, fetchErr string) error {
	if account == nil {
		return errors.New("official usage: missing account")
	}
	key := OfficialUsageKey(account)
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE accounts SET updated_at=updated_at WHERE id=? AND codex_account_id=? AND codex_refresh_token=? AND codex_refresh_token<>''`, account.ID, account.CodexAccountID, account.CodexRefreshToken)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrCodexCredentialChanged
	}
	now := NowMS()
	if fetchErr == "" {
		for _, d := range days {
			if d.Day < start || d.Day > end {
				return errors.New("official usage: day outside fetched range")
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO official_daily_usage(workspace_id,day,credits,uncached_input_tokens,cached_input_tokens,output_tokens,total_tokens,users,threads,turns,settled,raw,synced_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(workspace_id,day) DO UPDATE SET credits=excluded.credits,uncached_input_tokens=excluded.uncached_input_tokens,cached_input_tokens=excluded.cached_input_tokens,output_tokens=excluded.output_tokens,total_tokens=excluded.total_tokens,users=excluded.users,threads=excluded.threads,turns=excluded.turns,settled=excluded.settled,raw=excluded.raw,synced_at=excluded.synced_at`,
				key, d.Day, d.Credits, d.UncachedInputTokens, d.CachedInputTokens, d.OutputTokens, d.TotalTokens, d.Users, d.Threads, d.Turns, boolToInt(d.Settled), d.Raw, now)
			if err != nil {
				return err
			}
		}
	}
	synced := int64(0)
	if fetchErr == "" {
		synced = now
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO official_usage_sync(workspace_id,synced_at,attempted_at,error) VALUES (?,?,?,?) ON CONFLICT(workspace_id) DO UPDATE SET synced_at=CASE WHEN excluded.synced_at=0 THEN official_usage_sync.synced_at ELSE excluded.synced_at END,attempted_at=excluded.attempted_at,error=excluded.error`, key, synced, now, fetchErr)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListOfficialUsage(ctx context.Context, workspace, start, end string) ([]OfficialUsageDay, error) {
	rows, err := s.QueryContext(ctx, `SELECT workspace_id,day,credits,uncached_input_tokens,cached_input_tokens,output_tokens,total_tokens,users,threads,turns,settled,synced_at FROM official_daily_usage WHERE workspace_id=? AND day>=? AND day<=? ORDER BY day DESC`, workspace, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OfficialUsageDay{}
	for rows.Next() {
		var d OfficialUsageDay
		var settled int
		if err := rows.Scan(&d.WorkspaceID, &d.Day, &d.Credits, &d.UncachedInputTokens, &d.CachedInputTokens, &d.OutputTokens, &d.TotalTokens, &d.Users, &d.Threads, &d.Turns, &settled, &d.SyncedAt); err != nil {
			return nil, err
		}
		d.Settled = settled != 0
		out = append(out, d)
	}
	return out, rows.Err()
}
