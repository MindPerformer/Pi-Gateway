package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
)

const deploymentIDKey = "deployment_uuid"

var deploymentIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// DeploymentID is a non-secret, persistent database identity. Independent
// databases must not share session metadata merely because numeric IDs match.
// Never replace a persisted malformed ID: rotating it silently would obscure
// corruption and invalidate existing namespace isolation assumptions.
func (s *Store) DeploymentID(ctx context.Context) (string, error) {
	var id string
	err := s.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, deploymentIDKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", errors.New("store: generate deployment identity failed")
		}
		b[6] = (b[6] & 0x0f) | 0x40
		b[8] = (b[8] & 0x3f) | 0x80
		candidate := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
		// The unique key elects exactly one winner across processes/backends.
		if _, err := s.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?,?,?) ON CONFLICT(key) DO NOTHING`, deploymentIDKey, candidate, NowMS()); err != nil {
			return "", fmt.Errorf("store: persist deployment identity: %w", err)
		}
		err = s.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, deploymentIDKey).Scan(&id)
	}
	if err != nil {
		return "", fmt.Errorf("store: read deployment identity: %w", err)
	}
	if !deploymentIDPattern.MatchString(id) {
		return "", errors.New("store: invalid persisted deployment identity")
	}
	return id, nil
}
