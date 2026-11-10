package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrPricingRevision = errors.New("pricing revision conflict")

const PricingSchemaSQL = `
CREATE TABLE IF NOT EXISTS pricing_state (id INTEGER PRIMARY KEY, revision INTEGER NOT NULL);
INSERT INTO pricing_state(id,revision) VALUES(1,0) ON CONFLICT(id) DO NOTHING;
CREATE TABLE IF NOT EXISTS pricing_models (id TEXT PRIMARY KEY, document TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS pricing_sources (id TEXT PRIMARY KEY, document TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS pricing_runs (id TEXT PRIMARY KEY, document TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS pricing_history (id TEXT PRIMARY KEY, document TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS pricing_versions (id TEXT PRIMARY KEY, document TEXT NOT NULL);
`

// PricingRecords stores bounded, individually addressable records, never an ever-growing JSON blob.
type PricingRecords struct {
	Revision                                 int64
	Models, Sources, Runs, History, Versions map[string]json.RawMessage
}

func pricingTables(p *PricingRecords) map[string]*map[string]json.RawMessage {
	return map[string]*map[string]json.RawMessage{"pricing_models": &p.Models, "pricing_sources": &p.Sources, "pricing_runs": &p.Runs, "pricing_history": &p.History, "pricing_versions": &p.Versions}
}
func (s *Store) LoadPricing(ctx context.Context) (*PricingRecords, error) {
	p := &PricingRecords{}
	err := s.withTransaction(ctx, &sql.TxOptions{ReadOnly: true}, func(tx *storeTx) error {
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM pricing_state WHERE id=1`).Scan(&p.Revision); err != nil {
			return err
		}
		for table, dst := range pricingTables(p) {
			*dst = map[string]json.RawMessage{}
			rows, err := tx.QueryContext(ctx, `SELECT id,document FROM `+table)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id, raw string
				if err = rows.Scan(&id, &raw); err != nil {
					rows.Close()
					return err
				}
				(*dst)[id] = json.RawMessage(raw)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		return tx.Commit()
	})
	return p, err
}

// SavePricing performs revision CAS and audit in the same transaction.
func (s *Store) SavePricing(ctx context.Context, expected int64, p *PricingRecords, action string) error {
	return s.withTransaction(ctx, nil, func(tx *storeTx) error {
		result, err := tx.ExecContext(ctx, `UPDATE pricing_state SET revision=? WHERE id=1 AND revision=?`, expected+1, expected)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrPricingRevision
		}
		for table, records := range pricingTables(p) {
			if _, err = tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
				return err
			}
			for id, raw := range *records {
				if !json.Valid(raw) {
					return fmt.Errorf("invalid pricing record")
				}
				if _, err = tx.ExecContext(ctx, `INSERT INTO `+table+`(id,document) VALUES(?,?)`, id, string(raw)); err != nil {
					return err
				}
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(action,detail,created_at) VALUES(?,?,?)`, "pricing."+action, fmt.Sprintf("revision=%d", expected+1), time.Now().UnixMilli()); err != nil {
			return err
		}
		return tx.Commit()
	})
}
