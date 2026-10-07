package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
)

func adminSessionHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// CreateAdminSession persists only a token digest. Password changes invalidate
// sessions through their credential version, including across gateway instances.
func (s *Store) CreateAdminSession(ctx context.Context, token, username, passwordHash string, expiresAt int64) error {
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM admin_sessions WHERE expires_at<=?`, NowMS()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO admin_sessions(token_hash,username,credential_version,expires_at) VALUES(?,?,?,?)`, adminSessionHash(token), username, adminSessionHash(passwordHash), expiresAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AdminSessionValid(ctx context.Context, token, username, passwordHash string) (bool, error) {
	var expiresAt int64
	err := s.QueryRowContext(ctx, `SELECT expires_at FROM admin_sessions WHERE token_hash=? AND username=? AND credential_version=?`, adminSessionHash(token), username, adminSessionHash(passwordHash)).Scan(&expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return expiresAt > NowMS(), nil
}

func (s *Store) DeleteAdminSession(ctx context.Context, token string) error {
	_, err := s.ExecContext(ctx, `DELETE FROM admin_sessions WHERE token_hash=?`, adminSessionHash(token))
	return err
}
