package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// CompactionKey initializes once, so summaries survive restarts and concurrent
// initialization without storing conversation text in the database.
func (s *Store) CompactionKey(ctx context.Context) ([]byte, error) {
	const name = "internal_compaction_key_v1"
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if _, err := s.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES (?,?,?) ON CONFLICT(key) DO NOTHING`, name, base64.StdEncoding.EncodeToString(key), NowMS()); err != nil {
		return nil, err
	}
	value, err := s.GetSecret(ctx, name)
	if err != nil {
		return nil, err
	}
	key, err = base64.StdEncoding.DecodeString(value)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("store: invalid persisted compaction key")
	}
	return key, nil
}
