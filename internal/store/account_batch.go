package store

import "context"

// AddAccountGroups appends memberships without replacing existing ones or
// writing a stale credential/management snapshot.
func (s *Store) AddAccountGroups(ctx context.Context, accountID int64, groupIDs []int64) error {
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM accounts WHERE id=?`, accountID).Scan(&exists); err != nil {
		return err
	}
	for _, groupID := range uniqueIDs(groupIDs) {
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM account_groups WHERE id=?`, groupID).Scan(&exists); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_group_accounts(account_id,group_id) VALUES(?,?) ON CONFLICT(account_id,group_id) DO NOTHING`, accountID, groupID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET updated_at=? WHERE id=?`, NowMS(), accountID); err != nil {
		return err
	}
	return tx.Commit()
}
