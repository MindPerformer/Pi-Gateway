package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// UpgradeDefaultRules installs missing defaults once. The rules and durable
// marker share the publication transaction, so concurrent callers cannot seed
// duplicates and a later user deletion is not undone on restart.
func (s *Store) UpgradeDefaultRules(ctx context.Context, marker string, defaults []RuleChange, validate RuleSetValidator) error {
	if marker == "" || validate == nil || len(defaults) == 0 {
		return fmt.Errorf("%w: upgrade marker, defaults and validator are required", ErrRuleInvalid)
	}
	tx, err := s.beginRulePublication(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var done string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, marker).Scan(&done)
	if err == nil {
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	snapshot, err := loadRuleSetTx(ctx, tx)
	if err != nil {
		return err
	}
	if !snapshot.LegacyMigrated {
		return fmt.Errorf("%w: initialize rules before upgrading defaults", ErrRuleInvalid)
	}
	existing := make(map[string]bool, len(snapshot.Rules))
	for _, row := range snapshot.Rules {
		existing[row.ID] = true
	}
	var changes []RuleChange
	for _, change := range defaults {
		if change.Kind != "create" || change.ID == "" {
			return fmt.Errorf("%w: defaults must create a stable rule ID", ErrRuleInvalid)
		}
		if !existing[change.ID] {
			changes = append(changes, change)
		}
	}
	if len(changes) > 0 {
		changed, _, err := applyRuleChanges(snapshot, changes)
		if err != nil {
			return err
		}
		snapshot.Version++
		snapshot.UpdatedAt = NowMS()
		if err := validate(ctx, snapshot); err != nil {
			return err
		}
		for _, row := range changed {
			if err := persistRuleTx(ctx, tx, row); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE rule_set_metadata SET version=?,updated_at=? WHERE singleton=1`, snapshot.Version, snapshot.UpdatedAt); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES (?,?,?)`, marker, "1", NowMS()); err != nil {
		return err
	}
	return tx.Commit()
}
