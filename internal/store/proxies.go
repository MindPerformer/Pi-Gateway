package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrProxyInUse is returned when a proxy still has accounts assigned to it.
var ErrProxyInUse = errors.New("proxy is still assigned to one or more accounts")

// ProxyTest stores the latest connection test without retaining credentials.
type ProxyTest struct {
	Success   bool   `json:"success"`
	Status    int    `json:"status,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
	TestedAt  int64  `json:"tested_at"`
}

// Proxy is a saved outbound proxy. URL is only read by the server; API views
// must use egress.Redact before returning it to a browser.
type Proxy struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	URL          string     `json:"-"`
	LastTest     *ProxyTest `json:"last_test,omitempty"`
	AccountCount int        `json:"account_count"`
	CreatedAt    int64      `json:"created_at"`
	UpdatedAt    int64      `json:"updated_at"`
}

// proxyBindingSchema is run after the regular schema and is also safe for
// existing databases. The application updates proxy_url together with proxy_id
// so all egress paths continue to use the established transport factory.
const proxyBindingSchema = `
CREATE INDEX IF NOT EXISTS idx_accounts_proxy_id ON accounts(proxy_id);
CREATE TRIGGER IF NOT EXISTS account_proxy_insert AFTER INSERT ON accounts
WHEN NEW.proxy_id IS NOT NULL BEGIN
 UPDATE accounts SET proxy_url=(SELECT url FROM proxies WHERE id=NEW.proxy_id) WHERE id=NEW.id;
END;
CREATE TRIGGER IF NOT EXISTS account_proxy_update AFTER UPDATE OF proxy_id, proxy_url ON accounts
WHEN NEW.proxy_id IS NOT NULL AND NEW.proxy_url != (SELECT url FROM proxies WHERE id=NEW.proxy_id) BEGIN
 UPDATE accounts SET proxy_url=(SELECT url FROM proxies WHERE id=NEW.proxy_id) WHERE id=NEW.id;
END;
CREATE TRIGGER IF NOT EXISTS proxy_url_update AFTER UPDATE OF url ON proxies
WHEN OLD.url != NEW.url BEGIN
 UPDATE accounts SET proxy_url=NEW.url, updated_at=NEW.updated_at WHERE proxy_id=NEW.id;
END;
`

func encodeProxyTest(test *ProxyTest) string {
	if test == nil {
		return ""
	}
	b, _ := json.Marshal(test)
	return string(b)
}

func decodeProxyTest(raw string) *ProxyTest {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var test ProxyTest
	if err := json.Unmarshal([]byte(raw), &test); err != nil {
		return nil
	}
	return &test
}

func scanProxy(row interface{ Scan(...any) error }) (*Proxy, error) {
	var p Proxy
	var testJSON string
	if err := row.Scan(&p.ID, &p.Name, &p.URL, &testJSON, &p.AccountCount, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.LastTest = decodeProxyTest(testJSON)
	return &p, nil
}

// CreateProxy inserts a saved proxy.
func (s *Store) CreateProxy(ctx context.Context, p *Proxy) error {
	if p == nil {
		return errors.New("store: nil proxy")
	}
	now := NowMS()
	if p.CreatedAt == 0 {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	id, err := s.insertID(ctx, s, `INSERT INTO proxies(name, url, last_test_json, created_at, updated_at) VALUES(?,?,?,?,?)`,
		p.Name, p.URL, encodeProxyTest(p.LastTest), p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("store: create proxy: %w", err)
	}
	p.ID = id
	return nil
}

// GetProxy loads a saved proxy and its current account reference count.
func (s *Store) GetProxy(ctx context.Context, id int64) (*Proxy, error) {
	row := s.QueryRowContext(ctx, `SELECT p.id, p.name, p.url, p.last_test_json,
		(SELECT COUNT(*) FROM accounts a WHERE a.proxy_id=p.id), p.created_at, p.updated_at
		FROM proxies p WHERE p.id=?`, id)
	p, err := scanProxy(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get proxy: %w", err)
	}
	return p, nil
}

// ListProxies returns a deterministic page and total count.
func (s *Store) ListProxies(ctx context.Context, search string, limit, offset int) ([]*Proxy, int, error) {
	search = strings.TrimSpace(search)
	where := ""
	args := []any{}
	if search != "" {
		// Search only public endpoint text, never the write-only username/password.
		where = ` WHERE p.name LIKE ? OR (CASE WHEN instr(p.url, '@') > 0
			THEN substr(p.url, 1, instr(p.url, '://')+2) || substr(p.url, instr(p.url, '@')+1)
			ELSE p.url END) LIKE ?`
		needle := "%" + search + "%"
		args = append(args, needle, needle)
	}
	var total int
	if err := s.QueryRowContext(ctx, "SELECT COUNT(*) FROM proxies p"+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: count proxies: %w", err)
	}
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.QueryContext(ctx, `SELECT p.id, p.name, p.url, p.last_test_json,
		(SELECT COUNT(*) FROM accounts a WHERE a.proxy_id=p.id), p.created_at, p.updated_at
		FROM proxies p`+where+` ORDER BY p.updated_at DESC, p.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: list proxies: %w", err)
	}
	defer rows.Close()
	items := make([]*Proxy, 0)
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("store: scan proxy: %w", err)
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: list proxy rows: %w", err)
	}
	return items, total, nil
}

// UpdateProxy persists a proxy and atomically updates resolved URLs for every
// account currently bound to it. The account rows are what the runtime egress
// factory consumes, so changing this value changes real traffic immediately.
func (s *Store) UpdateProxy(ctx context.Context, p *Proxy) error {
	if p == nil || p.ID <= 0 {
		return errors.New("store: invalid proxy")
	}
	now := NowMS()
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin update proxy: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE proxies SET name=?, url=?, last_test_json=?, updated_at=? WHERE id=?`,
		p.Name, p.URL, encodeProxyTest(p.LastTest), now, p.ID); err != nil {
		return fmt.Errorf("store: update proxy: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET proxy_url=?, updated_at=? WHERE proxy_id=?`, p.URL, now, p.ID); err != nil {
		return fmt.Errorf("store: update bound account proxy: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit proxy update: %w", err)
	}
	p.UpdatedAt = now
	return nil
}

// UpdateProxyTest saves a test result for a proxy.
func (s *Store) UpdateProxyTest(ctx context.Context, id int64, expectedURL string, test *ProxyTest) error {
	result, err := s.ExecContext(ctx, `UPDATE proxies SET last_test_json=? WHERE id=? AND url=?`, encodeProxyTest(test), id, expectedURL)
	if err != nil {
		return fmt.Errorf("store: update proxy test: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteProxy protects references rather than silently converting accounts to
// direct connections.
func (s *Store) DeleteProxy(ctx context.Context, id int64) error {
	var count int
	if err := s.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE proxy_id=?`, id).Scan(&count); err != nil {
		return fmt.Errorf("store: count proxy references: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("%w (%d account(s))", ErrProxyInUse, count)
	}
	res, err := s.ExecContext(ctx, `DELETE FROM proxies WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("store: delete proxy: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SetProxyAccounts replaces the account set for a proxy. Removing a binding
// intentionally changes the account to direct egress; it never leaves a stale
// proxy URL behind.
func (s *Store) SetProxyAccounts(ctx context.Context, proxyID int64, accountIDs []int64) error {
	var proxyURL string
	if err := s.QueryRowContext(ctx, `SELECT url FROM proxies WHERE id=?`, proxyID).Scan(&proxyURL); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		return fmt.Errorf("store: get proxy for assignment: %w", err)
	}
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin proxy assignment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := NowMS()
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET proxy_id=NULL, proxy_url='', updated_at=? WHERE proxy_id=?`, now, proxyID); err != nil {
		return fmt.Errorf("store: clear proxy assignment: %w", err)
	}
	for _, accountID := range accountIDs {
		if accountID <= 0 {
			return errors.New("store: invalid account id")
		}
		res, err := tx.ExecContext(ctx, `UPDATE accounts SET proxy_id=?, proxy_url=?, updated_at=? WHERE id=?`, proxyID, proxyURL, now, accountID)
		if err != nil {
			return fmt.Errorf("store: assign proxy account: %w", err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("store: account %d not found", accountID)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit proxy assignment: %w", err)
	}
	return nil
}

// ListProxyAccounts returns accounts currently bound to a proxy.
func (s *Store) ListProxyAccounts(ctx context.Context, proxyID int64, search string, limit, offset int) ([]*Account, int, error) {
	search = strings.TrimSpace(search)
	where := "accounts.proxy_id=?"
	args := []any{proxyID}
	if search != "" {
		where += " AND (accounts.name LIKE ? OR accounts.email LIKE ?)"
		needle := "%" + search + "%"
		args = append(args, needle, needle)
	}
	var total int
	if err := s.QueryRowContext(ctx, "SELECT COUNT(*) FROM accounts WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: count proxy accounts: %w", err)
	}
	rows, err := s.QueryContext(ctx, "SELECT "+accountColumns+" FROM accounts WHERE "+where+" ORDER BY accounts.name, accounts.id LIMIT ? OFFSET ?", append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: list proxy accounts: %w", err)
	}
	defer rows.Close()
	items := make([]*Account, 0)
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("store: scan proxy account: %w", err)
		}
		items = append(items, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: list proxy account rows: %w", err)
	}
	return items, total, nil
}

// SetAccountProxy applies a single account's persistent proxy association.
func (s *Store) SetAccountProxy(ctx context.Context, accountID, proxyID int64, proxyURL string) error {
	if proxyID > 0 {
		var exists int
		if err := s.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxies WHERE id=?`, proxyID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
	}
	_, err := s.ExecContext(ctx, `UPDATE accounts SET proxy_id=?, proxy_url=?, updated_at=? WHERE id=?`, nullableID(proxyID), proxyURL, NowMS(), accountID)
	if err != nil {
		return fmt.Errorf("store: set account proxy: %w", err)
	}
	return nil
}

func nullableID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}
