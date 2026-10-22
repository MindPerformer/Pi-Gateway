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
	return s.withTransaction(ctx, nil, func(tx *storeTx) error {
		return tx.createAdminSession(ctx, token, username, passwordHash, expiresAt)
	})
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

func (tx *storeTx) createAdminSession(ctx context.Context, token string, username string, passwordHash string, expiresAt int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM admin_sessions WHERE expires_at<=?`, NowMS()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO admin_sessions(token_hash,username,credential_version,expires_at) VALUES(?,?,?,?)`, adminSessionHash(token), username, adminSessionHash(passwordHash), expiresAt); err != nil {
		return err
	}
	return tx.Commit()
}
