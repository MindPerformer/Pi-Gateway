package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type KeyBudgetWindow struct {
	Period      string `json:"period"`
	WindowStart int64  `json:"window_start"`
	UsedMicros  int64  `json:"used_micros"`
}

// Budget timestamps are Unix milliseconds. UTC natural days and Monday-start
// weeks make the persisted windows independent of the host timezone/DST.
func budgetWindows(now int64) []KeyBudgetWindow {
	t := time.UnixMilli(now).UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	week := day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
	return []KeyBudgetWindow{{Period: "daily", WindowStart: day.UnixMilli()}, {Period: "weekly", WindowStart: week.UnixMilli()}}
}

func rollBudgetWindows(ctx context.Context, tx *storeTx, id int64, now int64, cost int64) error {
	for _, w := range budgetWindows(now) {
		_, err := tx.ExecContext(ctx, `INSERT INTO key_budget_windows(api_key_id,period,window_start,used_micros) VALUES (?,?,?,?)
   ON CONFLICT(api_key_id,period) DO UPDATE SET window_start=excluded.window_start,
   used_micros=CASE WHEN key_budget_windows.window_start=excluded.window_start THEN key_budget_windows.used_micros+excluded.used_micros ELSE excluded.used_micros END`,
			id, w.Period, w.WindowStart, cost)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) KeyBudgetStatus(ctx context.Context, apiKeyID int64, now int64) ([]KeyBudgetWindow, error) {
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := rollBudgetWindows(ctx, tx, apiKeyID, now, 0); err != nil {
		return nil, err
	}
	out := budgetWindows(now)
	for i := range out {
		if err := tx.QueryRowContext(ctx, `SELECT used_micros FROM key_budget_windows WHERE api_key_id=? AND period=?`, apiKeyID, out[i].Period).Scan(&out[i].UsedMicros); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) SettleKeyCharge(ctx context.Context, requestID string, apiKeyID int64, costMicros int64, now int64) (bool, error) {
	if requestID == "" {
		return false, errors.New("store: empty charge request ID")
	}
	if costMicros < 0 {
		return false, errors.New("store: negative charge")
	}
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO key_charge_events(request_id,api_key_id,cost_micros,created_at) VALUES (?,?,?,?) ON CONFLICT(request_id) DO NOTHING`, requestID, apiKeyID, costMicros, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if err := rollBudgetWindows(ctx, tx, apiKeyID, now, costMicros); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) ResetKeyBudget(ctx context.Context, apiKeyID int64, period string, now int64) error {
	if period != "daily" && period != "weekly" && period != "" && period != "all" {
		return fmt.Errorf("store: invalid budget period %q", period)
	}
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, w := range budgetWindows(now) {
		if period != "" && period != "all" && period != w.Period {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO key_budget_windows(api_key_id,period,window_start,used_micros) VALUES (?,?,?,0)
   ON CONFLICT(api_key_id,period) DO UPDATE SET window_start=excluded.window_start,used_micros=0`, apiKeyID, w.Period, w.WindowStart)
		if err != nil {
			return err
		}
	}
	// Retain charge events: resetting a window must not allow duplicate settlement.
	return tx.Commit()
}
