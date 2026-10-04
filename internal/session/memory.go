package session

import (
	"context"
	"sync"
	"time"
)

type memoryEntry struct {
	payload []byte
	expires time.Time
}

// Memory has no background goroutines. Expired entries are reclaimed lazily;
// even an idle instance retains at most MaxEntries bounded records.
type Memory struct {
	mu      sync.Mutex
	options Options
	entries map[string]memoryEntry
	bytes   int64
	now     func() time.Time
	closed  bool
}

var _ Store = (*Memory)(nil)

func NewMemory(options Options) *Memory {
	return &Memory{options: options.normalized(), entries: make(map[string]memoryEntry), now: time.Now}
}

func (m *Memory) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	clear(m.entries)
	m.bytes = 0
	return nil
}

func (m *Memory) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return unavailable(err)
	}
	if m.closed {
		return ErrUnavailable
	}
	return nil
}

func (m *Memory) remove(key string) {
	if entry, ok := m.entries[key]; ok {
		m.bytes -= int64(len(entry.payload))
		delete(m.entries, key)
	}
}

func (m *Memory) get(key string, now time.Time) (memoryEntry, bool) {
	entry, ok := m.entries[key]
	if ok && !now.Before(entry.expires) {
		m.remove(key)
		return memoryEntry{}, false
	}
	return entry, ok
}

func (m *Memory) room(key string, size int, now time.Time) bool {
	fits := func() bool {
		old, exists := m.entries[key]
		return (exists || len(m.entries) < m.options.MaxEntries) && m.bytes-int64(len(old.payload))+int64(size) <= m.options.MaxBytes
	}
	if fits() {
		return true
	}
	for k, entry := range m.entries {
		if !now.Before(entry.expires) {
			m.remove(k)
		}
	}
	return fits()
}

func (m *Memory) put(key string, payload []byte, expires time.Time) {
	m.remove(key)
	m.entries[key] = memoryEntry{payload: payload, expires: expires}
	m.bytes += int64(len(payload))
}

func responseKey(responseID string) string { return "response:" + Fingerprint("response", responseID) }

func (m *Memory) Resolve(ctx context.Context, keyID int64, responseID string) (*Record, error) {
	if !validReference(keyID, responseID) {
		return nil, ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ready(ctx); err != nil {
		return nil, err
	}
	entry, ok := m.get(responseKey(responseID), m.now())
	if !ok {
		return nil, ErrMiss
	}
	r, err := decode(entry.payload, responseID, m.options, m.now())
	if err != nil {
		return nil, err
	}
	if r.KeyID != keyID {
		return nil, ErrWrongOwner
	}
	return r, nil
}

// RecordCompleted accepts only a completed, client-visible response; callers
// determine terminal validity. Version is a compare-and-swap precondition.
func (m *Memory) RecordCompleted(ctx context.Context, record Record) error {
	if !validReference(record.KeyID, record.ResponseID) {
		return ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ready(ctx); err != nil {
		return err
	}
	now := m.now()
	key := responseKey(record.ResponseID)
	var old *Record
	if entry, ok := m.get(key, now); ok {
		var err error
		old, err = decode(entry.payload, record.ResponseID, m.options, now)
		if err != nil {
			return err
		}
	}
	next, payload, err := prepare(record, old, m.options, now)
	if err != nil {
		return err
	}
	if !m.room(key, len(payload), now) {
		return ErrUnavailable
	}
	m.put(key, payload, next.ExpiresAt)
	return nil
}

func (m *Memory) Invalidate(ctx context.Context, request Invalidation) error {
	if !validInvalidation(request) {
		return ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ready(ctx); err != nil {
		return err
	}
	key := responseKey(request.ResponseID)
	entry, ok := m.get(key, m.now())
	if !ok {
		return ErrMiss
	}
	r, err := decode(entry.payload, request.ResponseID, m.options, m.now())
	if err != nil {
		return err
	}
	if err := checkInvalidation(*r, request); err != nil {
		return err
	}
	m.remove(key)
	return nil
}
