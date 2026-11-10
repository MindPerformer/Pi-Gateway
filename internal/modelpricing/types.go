// Package modelpricing manages immutable runtime prices and bounded administrative history.
package modelpricing

import (
	"encoding/json"
	"errors"
	"pi-gateway/internal/pricing"
	"pi-gateway/internal/store"
)

var ErrConflict = store.ErrPricingRevision
var ErrNotFound = errors.New("pricing resource not found")
var ErrExternal = errors.New("external pricing source failed")

const MaxModels = 10000
const MaxDocumentBytes = 16 << 20
const HistoryLimit = 200
const RunLimit = 50
const VersionLimit = 50

type StandardPriceFile struct {
	SchemaVersion int                  `json:"schema_version"`
	Currency      string               `json:"currency"`
	Unit          string               `json:"unit"`
	Models        []pricing.ModelPrice `json:"models"`
}
type Source struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	URL            string   `json:"url"`
	Format         string   `json:"format"`
	Enabled        bool     `json:"enabled"`
	IntervalHours  int      `json:"interval_hours"`
	AutoApply      bool     `json:"auto_apply"`
	ProxyID        int64    `json:"proxy_id,omitempty"`
	TrustedHosts   []string `json:"trusted_hosts,omitempty"`
	LastAttemptAt  int64    `json:"last_attempt_at,omitempty"`
	LastSuccessAt  int64    `json:"last_success_at,omitempty"`
	LastError      string   `json:"last_error,omitempty"`
	CandidateCount int      `json:"candidate_count,omitempty"`
	ETag           string   `json:"-"`
	LastModified   string   `json:"-"`
	Failures       int      `json:"-"`
	NextAttemptAt  int64    `json:"-"`
}
type storedSource struct {
	Source
	ETagValue     string `json:"etag"`
	ModifiedValue string `json:"last_modified"`
	FailuresValue int    `json:"failures"`
	NextValue     int64  `json:"next_attempt_at"`
}
type SyncItem struct {
	Model    string              `json:"model"`
	Before   *pricing.ModelPrice `json:"before"`
	After    pricing.ModelPrice  `json:"after"`
	Warnings []string            `json:"warnings"`
	Locked   bool                `json:"locked"`
	Selected bool                `json:"selected,omitempty"`
}
type SyncRun struct {
	ID           string     `json:"id"`
	SourceID     string     `json:"source_id"`
	CreatedAt    int64      `json:"created_at"`
	Status       string     `json:"status"`
	Error        string     `json:"error,omitempty"`
	BaseRevision int64      `json:"base_revision"`
	Hash         string     `json:"hash"`
	Items        []SyncItem `json:"items"`
	SourceHash   string     `json:"source_hash,omitempty"`
}
type HistoryEntry struct {
	ID        string              `json:"id"`
	CreatedAt int64               `json:"created_at"`
	Actor     string              `json:"actor"`
	Action    string              `json:"action"`
	Model     string              `json:"model,omitempty"`
	Before    *pricing.ModelPrice `json:"before"`
	After     *pricing.ModelPrice `json:"after"`
	Revision  int64               `json:"revision"`
}
type ModelView struct {
	Model             string              `json:"model"`
	Name              string              `json:"name,omitempty"`
	Effective         *pricing.ModelPrice `json:"effective"`
	Builtin           *pricing.ModelPrice `json:"builtin"`
	Manual            *pricing.ModelPrice `json:"manual"`
	External          *pricing.ModelPrice `json:"external"`
	FollowingSourceID string              `json:"following_source_id,omitempty"`
	LegacyMultiplier  *float64            `json:"legacy_multiplier,omitempty"`
	Status            string              `json:"status"`
	Source            string              `json:"source"`
}
type modelState struct {
	Model          string                        `json:"model"`
	Manual         *pricing.ModelPrice           `json:"manual,omitempty"`
	Following      string                        `json:"following_source_id,omitempty"`
	Blocked        bool                          `json:"blocked,omitempty"`
	BlockedAliases []string                      `json:"blocked_aliases,omitempty"`
	External       map[string]pricing.ModelPrice `json:"external,omitempty"`
}
type state struct {
	Revision int64
	Models   map[string]*modelState
	Sources  map[string]Source
	Runs     []SyncRun
	History  []HistoryEntry
	Versions map[string]json.RawMessage
}
