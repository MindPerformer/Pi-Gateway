package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// MiddlewareRow is the persisted configuration for one middleware instance.
type MiddlewareRow struct {
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	OrderIndex int    `json:"order_index"`
	Config     string `json:"config"`
	UpdatedAt  int64  `json:"updated_at"`
}

// ListMiddlewares returns persisted middleware rows ordered by execution order.
func (s *Store) ListMiddlewares(ctx context.Context) ([]*MiddlewareRow, error) {
	rows, err := s.QueryContext(ctx,
		`SELECT name, enabled, order_index, config, updated_at FROM middlewares ORDER BY order_index, name`)
	if err != nil {
		return nil, fmt.Errorf("store: list middlewares: %w", err)
	}
	defer rows.Close()

	out := []*MiddlewareRow{}
	for rows.Next() {
		var m MiddlewareRow
		var enabled int
		if err := rows.Scan(&m.Name, &enabled, &m.OrderIndex, &m.Config, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: scan middleware: %w", err)
		}
		m.Enabled = enabled == 1
		out = append(out, &m)
	}
	return out, rows.Err()
}

// GetMiddleware loads a single middleware row.
func (s *Store) GetMiddleware(ctx context.Context, name string) (*MiddlewareRow, error) {
	var m MiddlewareRow
	var enabled int
	err := s.QueryRowContext(ctx,
		`SELECT name, enabled, order_index, config, updated_at FROM middlewares WHERE name=?`, name).
		Scan(&m.Name, &enabled, &m.OrderIndex, &m.Config, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get middleware: %w", err)
	}
	m.Enabled = enabled == 1
	return &m, nil
}

// UpsertMiddleware inserts or updates a middleware row.
func (s *Store) UpsertMiddleware(ctx context.Context, m *MiddlewareRow) error {
	if m.Config == "" {
		m.Config = "{}"
	}
	m.UpdatedAt = NowMS()
	_, err := s.ExecContext(ctx,
		`INSERT INTO middlewares (name, enabled, order_index, config, updated_at) VALUES (?,?,?,?,?)
		 ON CONFLICT(name) DO UPDATE SET enabled=excluded.enabled, order_index=excluded.order_index,
		   config=excluded.config, updated_at=excluded.updated_at`,
		m.Name, boolToInt(m.Enabled), m.OrderIndex, m.Config, m.UpdatedAt)
	if err != nil {
		return fmt.Errorf("store: upsert middleware: %w", err)
	}
	return nil
}

// DeleteMiddleware removes a middleware row.
func (s *Store) DeleteMiddleware(ctx context.Context, name string) error {
	if _, err := s.ExecContext(ctx, `DELETE FROM middlewares WHERE name=?`, name); err != nil {
		return fmt.Errorf("store: delete middleware: %w", err)
	}
	return nil
}
