// Package admin implements the management API and serves the embedded web UI.
package admin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"pi-gateway/internal/accounts"
	"pi-gateway/internal/config"
	"pi-gateway/internal/egress"
	"pi-gateway/internal/oauth"
	"pi-gateway/internal/settings"
	"pi-gateway/internal/store"
	"pi-gateway/internal/upstream"
)

// adminPasswordHashKey is the settings key holding the admin password hash.
const adminPasswordHashKey = "admin.password_hash"

// Server serves the admin API. Browser sessions are persisted by Store.
type Server struct {
	cfg      *config.Config
	store    *store.Store
	accounts *accounts.Manager
	settings *settings.Holder
	flows    *oauth.FlowManager
	upstream *upstream.Client
	factory  *egress.Factory
	logger   *slog.Logger

	mu sync.Mutex
	// generatedPassword is surfaced once at startup when no password was configured.
	generatedPassword string
}

// Options configures the admin server.
type Options struct {
	Config   *config.Config
	Store    *store.Store
	Accounts *accounts.Manager
	Settings *settings.Holder
	Flows    *oauth.FlowManager
	Upstream *upstream.Client
	Factory  *egress.Factory
	Logger   *slog.Logger
}

// New builds the admin server.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		cfg:      opts.Config,
		store:    opts.Store,
		accounts: opts.Accounts,
		settings: opts.Settings,
		flows:    opts.Flows,
		upstream: opts.Upstream,
		factory:  opts.Factory,
		logger:   logger,
	}
	s.bindOAuthProxy()
	return s
}

// GeneratedPassword returns a password generated on first start (empty when a
// password was configured or already stored).
func (s *Server) GeneratedPassword() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generatedPassword
}

// BootstrapPassword ensures an admin password hash exists.
//
// When a password is configured it is (re)hashed into the database. Otherwise an
// existing hash is reused, or a random password is generated and returned so the
// operator can log in once and change it.
func (s *Server) BootstrapPassword(ctx context.Context) error {
	stored, err := s.store.GetSecret(ctx, adminPasswordHashKey)
	if err != nil {
		return err
	}

	if pw := strings.TrimSpace(s.cfg.Admin.Password); pw != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("admin: hash password: %w", err)
		}
		if !compareHash(stored, pw) {
			if err := s.store.SetSecret(ctx, adminPasswordHashKey, string(hash)); err != nil {
				return err
			}
		}
		return nil
	}

	if stored != "" {
		return nil
	}

	generated, err := randomPassword()
	if err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(generated), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("admin: hash generated password: %w", err)
	}
	if err := s.store.SetSecret(ctx, adminPasswordHashKey, string(hash)); err != nil {
		return err
	}
	s.mu.Lock()
	s.generatedPassword = generated
	s.mu.Unlock()
	return nil
}

func compareHash(hash, password string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func randomPassword() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// Routes registers the management API and the SPA.
func (s *Server) Routes(mux *http.ServeMux, spa http.Handler) {
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.requireAuth(s.handleLogout))
	mux.HandleFunc("GET /api/auth/me", s.requireAuth(s.handleMe))

	mux.HandleFunc("GET /api/overview", s.requireAuth(s.handleOverview))

	mux.HandleFunc("GET /api/proxies", s.requireAuth(s.handleListProxies))
	mux.HandleFunc("POST /api/proxies", s.requireAuth(s.handleCreateProxy))
	mux.HandleFunc("POST /api/proxies/probe", s.requireAuth(s.handleProbeProxy))
	mux.HandleFunc("GET /api/proxies/{id}", s.requireAuth(s.handleGetProxy))
	mux.HandleFunc("PATCH /api/proxies/{id}", s.requireAuth(s.handleUpdateProxy))
	mux.HandleFunc("DELETE /api/proxies/{id}", s.requireAuth(s.handleDeleteProxy))
	mux.HandleFunc("POST /api/proxies/{id}/test", s.requireAuth(s.handleTestSavedProxy))
	mux.HandleFunc("GET /api/proxies/{id}/accounts", s.requireAuth(s.handleProxyAccounts))
	mux.HandleFunc("PUT /api/proxies/{id}/accounts", s.requireAuth(s.handleAssignProxyAccounts))

	mux.HandleFunc("GET /api/accounts", s.requireAuth(s.handleListAccounts))
	mux.HandleFunc("POST /api/accounts/batch", s.requireAuth(s.handleBatchAccounts))
	mux.HandleFunc("POST /api/accounts/export", s.requireAuth(s.handleExportAccounts))
	mux.HandleFunc("POST /api/accounts/import", s.requireAuth(s.handleImportAccounts))
	// OAuth lives under its own prefix: /api/accounts/{id}/... would otherwise
	// make "oauth" ambiguous with the {id} wildcard on Go's ServeMux.
	mux.HandleFunc("POST /api/oauth/start", s.requireAuth(s.handleOAuthStart))
	mux.HandleFunc("GET /api/oauth/{flowID}", s.requireAuth(s.handleOAuthStatus))
	mux.HandleFunc("POST /api/oauth/{flowID}/complete", s.requireAuth(s.handleOAuthComplete))
	mux.HandleFunc("POST /api/accounts", s.requireAuth(s.handleCreateAccount))
	mux.HandleFunc("PATCH /api/accounts/{id}", s.requireAuth(s.handleUpdateAccount))
	mux.HandleFunc("DELETE /api/accounts/{id}", s.requireAuth(s.handleDeleteAccount))
	mux.HandleFunc("POST /api/accounts/{id}/refresh", s.requireAuth(s.handleRefreshAccount))
	mux.HandleFunc("POST /api/accounts/{id}/recover", s.requireAuth(s.handleRecoverAccount))
	mux.HandleFunc("POST /api/accounts/{id}/test-proxy", s.requireAuth(s.handleTestProxy))
	mux.HandleFunc("GET /api/accounts/{id}/models", s.requireAuth(s.handleAccountModels))
	mux.HandleFunc("POST /api/accounts/{id}/models/refresh", s.requireAuth(s.handleRefreshAccountModels))
	mux.HandleFunc("POST /api/accounts/{id}/test-model", s.requireAuth(s.handleTestAccountModel))
	mux.HandleFunc("GET /api/accounts/{id}/quota", s.requireAuth(s.handleGetQuota))
	mux.HandleFunc("POST /api/accounts/{id}/quota/refresh", s.requireAuth(s.handleRefreshQuota))
	mux.HandleFunc("POST /api/accounts/{id}/reset-credits/consume", s.requireAuth(s.handleConsumeResetCredit))
	// The optional Codex quota credential: start a codex flow (POST /api/oauth/start
	// with kind=codex and account_id) to link it, or delete it here.
	mux.HandleFunc("DELETE /api/accounts/{id}/codex", s.requireAuth(s.handleUnlinkCodex))

	mux.HandleFunc("GET /api/keys", s.requireAuth(s.handleListKeys))
	mux.HandleFunc("POST /api/keys", s.requireAuth(s.handleCreateKey))
	mux.HandleFunc("PATCH /api/keys/{id}", s.requireAuth(s.handleUpdateKey))
	mux.HandleFunc("DELETE /api/keys/{id}", s.requireAuth(s.handleDeleteKey))
	mux.HandleFunc("GET /api/keys/{id}/reveal", s.requireAuth(s.handleRevealKey))
	mux.HandleFunc("POST /api/keys/{id}/reset-budget", s.requireAuth(s.handleResetKeyBudget))

	mux.HandleFunc("GET /api/account-groups", s.requireAuth(s.handleListAccountGroups))
	mux.HandleFunc("POST /api/account-groups", s.requireAuth(s.handleCreateAccountGroup))
	mux.HandleFunc("PATCH /api/account-groups/{id}", s.requireAuth(s.handleUpdateAccountGroup))
	mux.HandleFunc("DELETE /api/account-groups/{id}", s.requireAuth(s.handleDeleteAccountGroup))
	mux.HandleFunc("PUT /api/account-groups/{id}/accounts", s.requireAuth(s.handleSetAccountGroupAccounts))

	mux.HandleFunc("GET /api/stats/summary", s.requireAuth(s.handleStatsSummary))
	mux.HandleFunc("GET /api/stats/trend", s.requireAuth(s.handleStatsTrend))
	mux.HandleFunc("GET /api/stats/models", s.requireAuth(s.handleStatsModels))
	mux.HandleFunc("GET /api/stats/keys", s.requireAuth(s.handleStatsKeys))
	mux.HandleFunc("GET /api/stats/accounts", s.requireAuth(s.handleStatsAccounts))
	mux.HandleFunc("GET /api/usage/records", s.requireAuth(s.handleUsageRecords))

	mux.HandleFunc("GET /api/captures", s.requireAuth(s.handleListCaptures))
	mux.HandleFunc("GET /api/captures/export", s.requireAuth(s.handleExportCaptures))
	mux.HandleFunc("GET /api/captures/{id}", s.requireAuth(s.handleGetCapture))
	mux.HandleFunc("DELETE /api/captures/{id}", s.requireAuth(s.handleDeleteCapture))
	mux.HandleFunc("DELETE /api/captures", s.requireAuth(s.handleClearCaptures))

	mux.HandleFunc("GET /api/settings", s.requireAuth(s.handleGetSettings))
	mux.HandleFunc("PUT /api/settings", s.requireAuth(s.handlePutSettings))
	mux.HandleFunc("POST /api/settings/password", s.requireAuth(s.handleChangePassword))

	mux.HandleFunc("GET /api/rules", s.requireAuth(s.handleListRules))
	mux.HandleFunc("POST /api/rules", s.requireAuth(s.handleCreateRule))
	// Literal subroutes are registered explicitly; they take precedence over {id}.
	mux.HandleFunc("GET /api/rules/schema", s.requireAuth(s.handleRulesSchema))
	mux.HandleFunc("POST /api/rules/validate", s.requireAuth(s.handleValidateRules))
	mux.HandleFunc("POST /api/rules/simulate", s.requireAuth(s.handleSimulateRules))
	mux.HandleFunc("POST /api/rules/simulate-capture", s.requireAuth(s.handleSimulateCapture))
	mux.HandleFunc("POST /api/rules/reorder", s.requireAuth(s.handleReorderRules))
	mux.HandleFunc("POST /api/rules/batch", s.requireAuth(s.handleBatchRules))
	mux.HandleFunc("GET /api/rules/{id}", s.requireAuth(s.handleGetRule))
	mux.HandleFunc("PUT /api/rules/{id}", s.requireAuth(s.handleUpdateRule))
	mux.HandleFunc("DELETE /api/rules/{id}", s.requireAuth(s.handleDeleteRule))
	mux.HandleFunc("POST /api/rules/{id}/duplicate", s.requireAuth(s.handleDuplicateRule))

	mux.HandleFunc("GET /api/middlewares", s.requireAuth(s.handleListLegacyRules))
	mux.HandleFunc("PUT /api/middlewares/{name}", s.requireAuth(s.handleUpdateLegacyRule))
	mux.HandleFunc("GET /api/model-presets", s.requireAuth(s.handleModelPresets))

	if spa != nil {
		mux.Handle("/", spa)
	}
}

// ---- session handling ----

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		valid, err := s.sessionValid(r)
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, "could not validate session; retry shortly")
			return
		}
		if !valid {
			writeErr(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next(w, r)
	}
}

func sessionToken(r *http.Request) string {
	token := bearerToken(r)
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	return token
}

func (s *Server) sessionValid(r *http.Request) (bool, error) {
	token := sessionToken(r)
	if token == "" {
		return false, nil
	}
	hash, err := s.store.GetSecret(r.Context(), adminPasswordHashKey)
	if err != nil {
		return false, err
	}
	return s.store.AdminSessionValid(r.Context(), token, s.cfg.Admin.Username, hash)
}

func bearerToken(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	hash, err := s.store.GetSecret(r.Context(), adminPasswordHashKey)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	userOK := subtle.ConstantTimeCompare([]byte(strings.TrimSpace(body.Username)), []byte(s.cfg.Admin.Username)) == 1
	passOK := compareHash(hash, body.Password)
	if !userOK || !passOK {
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	token, err := randomPassword()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ttl := time.Duration(s.cfg.Admin.SessionTTLMinute) * time.Minute
	expiresAt := time.Now().Add(ttl).UnixMilli()
	if err := s.store.CreateAdminSession(r.Context(), token, s.cfg.Admin.Username, hash, expiresAt); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not persist login session")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"expires_at": expiresAt,
		"username":   s.cfg.Admin.Username,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAdminSession(r.Context(), sessionToken(r)); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not revoke session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"username": s.cfg.Admin.Username})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current_password"`
		Next    string `json:"new_password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body.Next) < 8 {
		writeErr(w, http.StatusBadRequest, "new password must be at least 8 characters")
		return
	}
	hash, err := s.store.GetSecret(r.Context(), adminPasswordHashKey)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !compareHash(hash, body.Current) {
		writeErr(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(body.Next), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.SetSecret(r.Context(), adminPasswordHashKey, string(newHash)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.RecordAudit(r.Context(), "password.changed", "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- helpers ----

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	limited := io.LimitReader(r.Body, 1<<20)
	dec := json.NewDecoder(limited)
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeErr(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func pathInt(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	if raw == "" {
		return 0, fmt.Errorf("missing %s", name)
	}
	var v int64
	if _, err := fmt.Sscanf(raw, "%d", &v); err != nil {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return v, nil
}
