package store

import "fmt"

// Each migration is a reviewed static SQL statement. Runtime table/column input
// can select a registered statement, but cannot become SQL syntax.
// TestAdditiveMigrationStatements guards drift against additiveColumns.
type additiveMigrationSQL struct{ sqlite, postgres string }

var additiveMigrationStatements = map[string]map[string]additiveMigrationSQL{
	"account_groups": {
		"disabled_models": {sqlite: `ALTER TABLE account_groups ADD COLUMN disabled_models TEXT NOT NULL DEFAULT '[]'`, postgres: `ALTER TABLE account_groups ADD COLUMN IF NOT EXISTS disabled_models TEXT NOT NULL DEFAULT '[]'`},
		"switch_on_429":   {sqlite: `ALTER TABLE account_groups ADD COLUMN switch_on_429 TEXT NOT NULL DEFAULT 'inherit'`, postgres: `ALTER TABLE account_groups ADD COLUMN IF NOT EXISTS switch_on_429 TEXT NOT NULL DEFAULT 'inherit'`},
	},
	"account_model_catalog": {
		"source_catalogs_json": {sqlite: `ALTER TABLE account_model_catalog ADD COLUMN source_catalogs_json TEXT NOT NULL DEFAULT '{}'`, postgres: `ALTER TABLE account_model_catalog ADD COLUMN IF NOT EXISTS source_catalogs_json TEXT NOT NULL DEFAULT '{}'`},
	},
	"accounts": {
		"codex_access_token":   {sqlite: `ALTER TABLE accounts ADD COLUMN codex_access_token TEXT NOT NULL DEFAULT ''`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS codex_access_token TEXT NOT NULL DEFAULT ''`},
		"codex_account_id":     {sqlite: `ALTER TABLE accounts ADD COLUMN codex_account_id TEXT NOT NULL DEFAULT ''`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS codex_account_id TEXT NOT NULL DEFAULT ''`},
		"codex_expires_at":     {sqlite: `ALTER TABLE accounts ADD COLUMN codex_expires_at INTEGER NOT NULL DEFAULT 0`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS codex_expires_at BIGINT NOT NULL DEFAULT 0`},
		"codex_id_token":       {sqlite: `ALTER TABLE accounts ADD COLUMN codex_id_token TEXT NOT NULL DEFAULT ''`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS codex_id_token TEXT NOT NULL DEFAULT ''`},
		"codex_refresh_token":  {sqlite: `ALTER TABLE accounts ADD COLUMN codex_refresh_token TEXT NOT NULL DEFAULT ''`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS codex_refresh_token TEXT NOT NULL DEFAULT ''`},
		"consecutive_failures": {sqlite: `ALTER TABLE accounts ADD COLUMN consecutive_failures INTEGER NOT NULL DEFAULT 0`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS consecutive_failures BIGINT NOT NULL DEFAULT 0`},
		"cooldown_429_seconds": {sqlite: `ALTER TABLE accounts ADD COLUMN cooldown_429_seconds INTEGER NOT NULL DEFAULT -1`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS cooldown_429_seconds BIGINT NOT NULL DEFAULT -1`},
		"cooldown_kind":        {sqlite: `ALTER TABLE accounts ADD COLUMN cooldown_kind TEXT NOT NULL DEFAULT ''`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS cooldown_kind TEXT NOT NULL DEFAULT ''`},
		"cooldown_until":       {sqlite: `ALTER TABLE accounts ADD COLUMN cooldown_until INTEGER NOT NULL DEFAULT 0`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS cooldown_until BIGINT NOT NULL DEFAULT 0`},
		"disabled_models":      {sqlite: `ALTER TABLE accounts ADD COLUMN disabled_models TEXT NOT NULL DEFAULT '[]'`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS disabled_models TEXT NOT NULL DEFAULT '[]'`},
		"ewma_failure_rate_bp": {sqlite: `ALTER TABLE accounts ADD COLUMN ewma_failure_rate_bp INTEGER NOT NULL DEFAULT 0`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS ewma_failure_rate_bp BIGINT NOT NULL DEFAULT 0`},
		"ewma_first_output_ms": {sqlite: `ALTER TABLE accounts ADD COLUMN ewma_first_output_ms REAL NOT NULL DEFAULT 0`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS ewma_first_output_ms DOUBLE PRECISION NOT NULL DEFAULT 0`},
		"last_started_at":      {sqlite: `ALTER TABLE accounts ADD COLUMN last_started_at INTEGER NOT NULL DEFAULT 0`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS last_started_at BIGINT NOT NULL DEFAULT 0`},
		"oauth_client_id":      {sqlite: `ALTER TABLE accounts ADD COLUMN oauth_client_id TEXT NOT NULL DEFAULT ''`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS oauth_client_id TEXT NOT NULL DEFAULT ''`},
		"proxy_id":             {sqlite: `ALTER TABLE accounts ADD COLUMN proxy_id INTEGER REFERENCES proxies(id) ON DELETE RESTRICT`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS proxy_id BIGINT REFERENCES proxies(id) ON DELETE RESTRICT`},
		"quota_error":          {sqlite: `ALTER TABLE accounts ADD COLUMN quota_error TEXT NOT NULL DEFAULT ''`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS quota_error TEXT NOT NULL DEFAULT ''`},
		"quota_json":           {sqlite: `ALTER TABLE accounts ADD COLUMN quota_json TEXT NOT NULL DEFAULT ''`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS quota_json TEXT NOT NULL DEFAULT ''`},
		"quota_updated_at":     {sqlite: `ALTER TABLE accounts ADD COLUMN quota_updated_at INTEGER NOT NULL DEFAULT 0`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS quota_updated_at BIGINT NOT NULL DEFAULT 0`},
		"supplemental_models":  {sqlite: `ALTER TABLE accounts ADD COLUMN supplemental_models TEXT NOT NULL DEFAULT '[]'`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS supplemental_models TEXT NOT NULL DEFAULT '[]'`},
		"upstream_protocol":    {sqlite: `ALTER TABLE accounts ADD COLUMN upstream_protocol TEXT NOT NULL DEFAULT ''`, postgres: `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS upstream_protocol TEXT NOT NULL DEFAULT ''`},
	},
	"api_keys": {
		"daily_limit_usd":     {sqlite: `ALTER TABLE api_keys ADD COLUMN daily_limit_usd REAL NOT NULL DEFAULT 0`, postgres: `ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS daily_limit_usd DOUBLE PRECISION NOT NULL DEFAULT 0`},
		"label":               {sqlite: `ALTER TABLE api_keys ADD COLUMN label TEXT NOT NULL DEFAULT ''`, postgres: `ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS label TEXT NOT NULL DEFAULT ''`},
		"max_concurrency":     {sqlite: `ALTER TABLE api_keys ADD COLUMN max_concurrency INTEGER NOT NULL DEFAULT 0`, postgres: `ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS max_concurrency BIGINT NOT NULL DEFAULT 0`},
		"requests_per_minute": {sqlite: `ALTER TABLE api_keys ADD COLUMN requests_per_minute INTEGER NOT NULL DEFAULT 0`, postgres: `ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS requests_per_minute BIGINT NOT NULL DEFAULT 0`},
		"weekly_limit_usd":    {sqlite: `ALTER TABLE api_keys ADD COLUMN weekly_limit_usd REAL NOT NULL DEFAULT 0`, postgres: `ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS weekly_limit_usd DOUBLE PRECISION NOT NULL DEFAULT 0`},
	},
	"captures": {
		"rule_traces":           {sqlite: `ALTER TABLE captures ADD COLUMN rule_traces TEXT NOT NULL DEFAULT '[]'`, postgres: `ALTER TABLE captures ADD COLUMN IF NOT EXISTS rule_traces TEXT NOT NULL DEFAULT '[]'`},
		"rules_trace_omitted":   {sqlite: `ALTER TABLE captures ADD COLUMN rules_trace_omitted INTEGER NOT NULL DEFAULT 0`, postgres: `ALTER TABLE captures ADD COLUMN IF NOT EXISTS rules_trace_omitted BIGINT NOT NULL DEFAULT 0`},
		"rules_trace_truncated": {sqlite: `ALTER TABLE captures ADD COLUMN rules_trace_truncated INTEGER NOT NULL DEFAULT 0`, postgres: `ALTER TABLE captures ADD COLUMN IF NOT EXISTS rules_trace_truncated BIGINT NOT NULL DEFAULT 0`},
		"rules_version":         {sqlite: `ALTER TABLE captures ADD COLUMN rules_version INTEGER NOT NULL DEFAULT 0`, postgres: `ALTER TABLE captures ADD COLUMN IF NOT EXISTS rules_version BIGINT NOT NULL DEFAULT 0`},
	},
	"usage_records": {
		"billing_details":        {sqlite: `ALTER TABLE usage_records ADD COLUMN billing_details TEXT NOT NULL DEFAULT ''`, postgres: `ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS billing_details TEXT NOT NULL DEFAULT ''`},
		"reasoning_effort":       {sqlite: `ALTER TABLE usage_records ADD COLUMN reasoning_effort TEXT NOT NULL DEFAULT ''`, postgres: `ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS reasoning_effort TEXT NOT NULL DEFAULT ''`},
		"request_kind":           {sqlite: `ALTER TABLE usage_records ADD COLUMN request_kind TEXT NOT NULL DEFAULT ''`, postgres: `ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS request_kind TEXT NOT NULL DEFAULT ''`},
		"requested_service_tier": {sqlite: `ALTER TABLE usage_records ADD COLUMN requested_service_tier TEXT NOT NULL DEFAULT ''`, postgres: `ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS requested_service_tier TEXT NOT NULL DEFAULT ''`},
		"service_tier":           {sqlite: `ALTER TABLE usage_records ADD COLUMN service_tier TEXT NOT NULL DEFAULT ''`, postgres: `ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS service_tier TEXT NOT NULL DEFAULT ''`},
	},
}

func registeredAdditiveMigration(table, column string) (additiveMigrationSQL, error) {
	statement, ok := additiveMigrationStatements[table][column]
	if !ok {
		return additiveMigrationSQL{}, fmt.Errorf("store: unregistered additive migration %s.%s", table, column)
	}
	return statement, nil
}
