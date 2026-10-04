package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

// APIKey is a client credential scoped through account groups.
type APIKey struct {
	ID                int64   `json:"id"`
	Name              string  `json:"name"`
	Key               string  `json:"key"`
	Enabled           bool    `json:"enabled"`
	Label             string  `json:"label"`
	MaxConcurrency    int     `json:"max_concurrency"`
	RequestsPerMinute int     `json:"requests_per_minute"`
	DailyLimitUSD     float64 `json:"daily_limit_usd"`
	WeeklyLimitUSD    float64 `json:"weekly_limit_usd"`
	GroupIDs          []int64 `json:"group_ids"`
	Strategy          string  `json:"strategy"`
	Transport         string  `json:"transport"`
	LastUsedAt        int64   `json:"last_used_at"`
	RequestCount      int64   `json:"request_count"`
	CreatedAt         int64   `json:"created_at"`
	UpdatedAt         int64   `json:"updated_at"`
}

const keyColumns = `id,name,key,enabled,label,max_concurrency,requests_per_minute,daily_limit_usd,weekly_limit_usd,strategy,transport,last_used_at,request_count,created_at,updated_at`

// GenerateKey produces a new client API key (sk-pi-<40 hex chars>).
func GenerateKey() string {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		panic("store: crypto/rand unavailable: " + err.Error())
	}
	return "sk-pi-" + hex.EncodeToString(buf)
}

func scanKey(row interface{ Scan(...any) error }) (*APIKey, error) {
	var k APIKey
	var enabled int
	if err := row.Scan(&k.ID, &k.Name, &k.Key, &enabled, &k.Label, &k.MaxConcurrency, &k.RequestsPerMinute, &k.DailyLimitUSD, &k.WeeklyLimitUSD, &k.Strategy, &k.Transport, &k.LastUsedAt, &k.RequestCount, &k.CreatedAt, &k.UpdatedAt); err != nil {
		return nil, err
	}
	k.Enabled = enabled == 1
	k.GroupIDs = []int64{}
	return &k, nil
}

// CreateKey inserts the key and its group bindings atomically.
func (s *Store) CreateKey(ctx context.Context, k *APIKey) error {
	if k == nil {
		return errors.New("store: nil API key")
	}
	if k.Key == "" {
		k.Key = GenerateKey()
	}
	if k.CreatedAt == 0 {
		k.CreatedAt = NowMS()
	}
	k.UpdatedAt = NowMS()
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id, err := s.insertID(ctx, tx, `INSERT INTO api_keys
 (name,key,enabled,label,max_concurrency,requests_per_minute,daily_limit_usd,weekly_limit_usd,strategy,transport,created_at,updated_at)
 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, k.Name, k.Key, boolToInt(k.Enabled), k.Label, k.MaxConcurrency, k.RequestsPerMinute, k.DailyLimitUSD, k.WeeklyLimitUSD, k.Strategy, k.Transport, k.CreatedAt, k.UpdatedAt)
	if err != nil {
		return fmt.Errorf("store: create key: %w", err)
	}
	k.ID = id
	if err := replaceKeyGroups(ctx, tx, k.ID, k.GroupIDs); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateKey(ctx context.Context, k *APIKey) error {
	if k == nil {
		return errors.New("store: nil API key")
	}
	k.UpdatedAt = NowMS()
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE api_keys SET
 name=?,enabled=?,label=?,max_concurrency=?,requests_per_minute=?,daily_limit_usd=?,weekly_limit_usd=?,strategy=?,transport=?,updated_at=? WHERE id=?`,
		k.Name, boolToInt(k.Enabled), k.Label, k.MaxConcurrency, k.RequestsPerMinute, k.DailyLimitUSD, k.WeeklyLimitUSD, k.Strategy, k.Transport, k.UpdatedAt, k.ID)
	if err != nil {
		return fmt.Errorf("store: update key: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return sql.ErrNoRows
	}
	if err := replaceKeyGroups(ctx, tx, k.ID, k.GroupIDs); err != nil {
		return err
	}
	return tx.Commit()
}

// GetKeyByValue also returns disabled keys so callers can distinguish them.
func (s *Store) GetKeyByValue(ctx context.Context, value string) (*APIKey, error) {
	return s.loadKey(ctx, `SELECT `+keyColumns+` FROM api_keys WHERE key=?`, value)
}

func (s *Store) GetKey(ctx context.Context, id int64) (*APIKey, error) {
	return s.loadKey(ctx, `SELECT `+keyColumns+` FROM api_keys WHERE id=?`, id)
}

func (s *Store) loadKey(ctx context.Context, query string, arg any) (*APIKey, error) {
	k, err := scanKey(s.QueryRowContext(ctx, query, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get key: %w", err)
	}
	k.GroupIDs, err = s.ListKeyGroupIDs(ctx, k.ID)
	return k, err
}

func (s *Store) ListKeys(ctx context.Context) ([]*APIKey, error) {
	rows, err := s.QueryContext(ctx, `SELECT `+keyColumns+` FROM api_keys ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list keys: %w", err)
	}
	out := []*APIKey{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: scan key: %w", err)
		}
		out = append(out, k)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, k := range out {
		k.GroupIDs, err = s.ListKeyGroupIDs(ctx, k.ID)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) MarkKeyUsed(ctx context.Context, id int64) error {
	now := NowMS()
	_, err := s.ExecContext(ctx, `UPDATE api_keys SET last_used_at=?,request_count=request_count+1,updated_at=? WHERE id=?`, now, now, id)
	if err != nil {
		return fmt.Errorf("store: mark key used: %w", err)
	}
	return nil
}

func (s *Store) DeleteKey(ctx context.Context, id int64) error {
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range []string{`DELETE FROM api_keys WHERE id=?`, `DELETE FROM api_key_groups WHERE api_key_id=?`, `DELETE FROM key_budget_windows WHERE api_key_id=?`} {
		if _, err := tx.ExecContext(ctx, query, id); err != nil {
			return err
		}
	}
	// Charge events and usage records remain as historical, idempotency facts.
	return tx.Commit()
}
