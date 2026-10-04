package accounts

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"math"
	"sort"
	"strings"

	"pi-gateway/internal/session"
	"pi-gateway/internal/store"
)

// Bound cache work even if account identities keep changing while a request
// waits for capacity. An unchanged identity (including misses/errors) is read
// just once, not once per 50ms scheduling poll. There is at most one write.
const maxAffinityReads = 4

type requestAffinity struct {
	backend session.AffinityStore
	key     session.AffinityKey
	reads   int
	account int64
}

func (r *requestAffinity) load(ctx context.Context, m *Manager, key *store.APIKey, candidates []*store.Account, model, scope, strategy string) int64 {
	var backend session.AffinityStore
	if key != nil && key.ID > 0 {
		if scope != "" {
			backend = m.affinity
		} else if strategy == string(StrategySticky) {
			backend = m.sticky
		}
	}
	if backend == nil {
		r.backend, r.key, r.account = nil, session.AffinityKey{}, 0
		return 0
	}
	identity := m.affinityKey(key, candidates, model, scope, strategy)
	if identity == r.key {
		return r.account
	}
	r.backend, r.key, r.account = backend, identity, 0
	if r.reads >= maxAffinityReads {
		return 0
	}
	r.reads++
	bounded, cancel := context.WithTimeout(ctx, m.affinityTimeout)
	account, err := backend.GetAffinity(bounded, identity)
	valid := bounded.Err() == nil
	cancel()
	if err == nil && valid && account > 0 {
		r.account = account
	}
	return r.account
}

// save reports whether cache IO ran, so the caller can refresh authorization
// after the bounded wait. Errors never turn an ordinary request into a failure.
// The backend must honor context cancellation; no goroutines or queues outlive
// this acquisition or require a manager shutdown hook.
func (r *requestAffinity) save(ctx context.Context, m *Manager, accountID int64) bool {
	if r.backend == nil || ctx.Err() != nil {
		return false
	}
	bounded, cancel := context.WithTimeout(ctx, m.affinityTimeout)
	defer cancel()
	_ = r.backend.SetAffinity(bounded, r.key, accountID, m.affinityTTL)
	return true
}

// preferAccount only reorders the strategy's output, preserving its weight
// filtering/single-account restrictions and all normal busy-account fallbacks.
func preferAccount(candidates []*store.Account, accountID int64) {
	if accountID <= 0 {
		return
	}
	for i, candidate := range candidates {
		if candidate.ID == accountID {
			copy(candidates[1:i+1], candidates[:i])
			candidates[0] = candidate
			return
		}
	}
}

// affinityKey binds hints to the current database identity, never to the
// caller's possibly stale APIKey/Account snapshots. Only the final digest leaves
// this package: neither tokens, proxy credentials nor account objects are sent
// to the cache. Volatile usage/health counters deliberately are not identities.
func (m *Manager) affinityKey(key *store.APIKey, candidates []*store.Account, model, scope, strategy string) session.AffinityKey {
	d := affinityDigest{h: sha256.New()}
	d.text("account-affinity-v1")
	d.text(m.affinityIdentity)
	d.text(scope)
	d.number(key.ID)
	d.text(session.Fingerprint("account-affinity-client-token", key.Key))
	d.ids(key.GroupIDs)
	d.text(key.Strategy)
	d.text(strategy)
	d.text(key.Transport)
	d.number(int64(key.MaxConcurrency))
	d.number(int64(key.RequestsPerMinute))
	d.number(int64(math.Float64bits(key.DailyLimitUSD)))
	d.number(int64(math.Float64bits(key.WeeklyLimitUSD)))
	d.text(strings.TrimSpace(model))

	// Membership order must not change identity. Do not mutate the eligible
	// slice, which is still needed by the configured scheduling strategy.
	ordered := append([]*store.Account(nil), candidates...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	d.number(int64(len(ordered)))
	for _, a := range ordered {
		d.number(a.ID)
		d.text(a.AccountID)
		d.text(a.OAuthClientID)
		d.text(a.AccessToken)
		d.text(a.RefreshToken)
		d.text(a.IDToken)
		d.text(a.ProxyURL)
		proxyID := int64(-1)
		if a.ProxyID != nil {
			proxyID = *a.ProxyID
		}
		d.number(proxyID)
		d.text(a.UpstreamProtocol)
		d.ids(a.GroupIDs)
		d.number(int64(a.Weight))
		d.number(int64(a.Concurrency))
	}
	return session.AffinityKey{
		KeyID:        key.ID,
		SessionID:    session.Fingerprint("account-affinity-scope", scope),
		Provider:     "account-selection-v1",
		IdentityHash: hex.EncodeToString(d.h.Sum(nil)),
	}
}

// Length framing avoids collisions between adjacent variable-length fields.
// Writing credentials directly into the digest avoids serialized secret copies.
type affinityDigest struct {
	h       hash.Hash
	buf     [8]byte
	scratch [1024]byte
}

func (d *affinityDigest) number(n int64) {
	binary.LittleEndian.PutUint64(d.buf[:], uint64(n))
	_, _ = d.h.Write(d.buf[:])
}

func (d *affinityDigest) text(value string) {
	d.number(int64(len(value)))
	// Reuse one bounded buffer instead of allocating a copy of every provider
	// token on every scheduling poll. No unsafe conversion or retained secrets.
	for len(value) > 0 {
		n := copy(d.scratch[:], value)
		_, _ = d.h.Write(d.scratch[:n])
		value = value[n:]
	}
}

func (d *affinityDigest) ids(ids []int64) {
	d.number(int64(len(ids)))
	if len(ids) == 0 {
		return
	}
	ordered := append([]int64(nil), ids...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for _, id := range ordered {
		d.number(id)
	}
}
