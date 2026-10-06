package store

import (
	"bytes"
	"context"
	"testing"
)

func TestCompactionKeyPersists(t *testing.T) {
	s := openTestStore(t)
	first, err := s.CompactionKey(context.Background())
	if err != nil || len(first) != 32 {
		t.Fatalf("key size=%d err=%v", len(first), err)
	}
	second, err := s.CompactionKey(context.Background())
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("key changed")
	}
	if err := s.SetSecret(context.Background(), "internal_compaction_key_v1", "corrupt"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompactionKey(context.Background()); err == nil {
		t.Fatal("corrupt key silently replaced")
	}
}
