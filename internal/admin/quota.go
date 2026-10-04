package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"pi-gateway/internal/quota"
	"pi-gateway/internal/store"
)

// quotaView is the account quota state as the UI consumes it.
type quotaView struct {
	// Report is the parsed usage: rate-limit windows, credits, spend control.
	Report *quota.Report `json:"report"`
	// ResetCredits is the inventory of consumable rate-limit resets.
	ResetCredits *quota.ResetCredits `json:"reset_credits"`
	// Windows is a flattened convenience list ordered for display.
	Windows []quota.Window `json:"windows"`
	// Session is the 5-hour window of the primary ("codex") bucket, if reported.
	Session *quota.Window `json:"session"`
	// Weekly is the 7-day window of the primary bucket, if reported.
	Weekly *quota.Window `json:"weekly"`
	// UpdatedAt is when the snapshot was fetched (unix ms).
	UpdatedAt int64 `json:"updated_at"`
	// Error explains why the snapshot may be stale.
	Error string `json:"error,omitempty"`
	// Available reports whether a quota client is configured at all.
	Available bool `json:"available"`
}

func (s *Server) buildQuotaView(account *store.Account) quotaView {
	view := quotaView{
		Windows:   []quota.Window{},
		Available: s.accounts.QuotaClient() != nil,
	}
	// Quota state is produced by the Codex credential; without one there is no
	// authoritative snapshot to show, so no account-facing response may expose a
	// stale cached value.
	if account == nil || !account.CodexLinked() {
		return view
	}
	view.UpdatedAt = account.QuotaUpdatedAt
	view.Error = account.QuotaError
	snapshot := s.accounts.QuotaSnapshot(account)
	if snapshot == nil {
		return view
	}
	view.Report = snapshot.Report
	view.ResetCredits = snapshot.ResetCredits
	if snapshot.Report != nil {
		view.Windows = snapshot.Report.Windows
		view.Session = quota.FindWindow(snapshot.Report.Windows, "codex", quota.KindShortTerm)
		view.Weekly = quota.FindWindow(snapshot.Report.Windows, "codex", quota.KindWeekly)
	}
	return view
}

// handleGetQuota returns the stored snapshot without contacting the upstream.
func (s *Server) handleGetQuota(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	account, err := s.store.GetAccount(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if account == nil {
		writeErr(w, http.StatusNotFound, "account not found")
		return
	}
	// The snapshot is produced by the Codex credential; without one there is
	// nothing authoritative to show, so surface the same conflict as refresh.
	if !account.CodexLinked() {
		writeErr(w, http.StatusConflict, "this account has no Codex credential; link one to read quota")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"quota": s.buildQuotaView(account)})
}

// handleRefreshQuota fetches fresh quota data from the upstream and returns it.
func (s *Server) handleRefreshQuota(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	account, err := s.store.GetAccount(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if account == nil {
		writeErr(w, http.StatusNotFound, "account not found")
		return
	}
	if s.accounts.QuotaClient() == nil {
		writeErr(w, http.StatusServiceUnavailable, "quota fetching is not configured")
		return
	}
	// Quota state lives on the Codex backend, so it needs the account's optional
	// Codex credential rather than the ChatGPT model credential.
	if !account.CodexLinked() {
		writeErr(w, http.StatusConflict, "this account has no Codex credential; link one to read quota")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	snapshot, refreshErr := s.accounts.RefreshQuota(ctx, account)
	updated, err := s.store.GetAccount(r.Context(), id)
	if err != nil || updated == nil {
		updated = account
	}

	view := s.buildQuotaView(updated)
	writeJSON(w, http.StatusOK, map[string]any{
		"quota":   view,
		"fetched": snapshot != nil,
		"error":   errorString(refreshErr),
	})
}

// handleConsumeResetCredit spends one rate-limit reset credit for an account.
//
// This is the manual "use a reset" action: it immediately restores the exhausted
// window, so the response carries the refreshed quota.
func (s *Server) handleConsumeResetCredit(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	account, err := s.store.GetAccount(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if account == nil {
		writeErr(w, http.StatusNotFound, "account not found")
		return
	}
	if s.accounts.QuotaClient() == nil {
		writeErr(w, http.StatusServiceUnavailable, "quota fetching is not configured")
		return
	}
	// Quota state lives on the Codex backend, so it needs the account's optional
	// Codex credential rather than the ChatGPT model credential.
	if !account.CodexLinked() {
		writeErr(w, http.StatusConflict, "this account has no Codex credential; link one to read quota")
		return
	}

	var body struct {
		CreditID string `json:"credit_id"`
	}
	_ = decodeJSON(r, &body)

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	result, snapshot, err := s.accounts.ConsumeResetCredit(ctx, account, body.CreditID)

	updated, getErr := s.store.GetAccount(r.Context(), id)
	if getErr != nil || updated == nil {
		updated = account
	}
	view := s.buildQuotaView(updated)

	status := http.StatusOK
	if err != nil {
		status = http.StatusBadGateway
		var consumeErr *quota.ConsumeError
		switch {
		case errors.As(err, &consumeErr):
			// A business refusal is a client-visible decision, not a gateway fault.
			status = http.StatusConflict
			if consumeErr.Code == quota.CodeNoCredit || consumeErr.Code == quota.CodeNothingToReset {
				status = http.StatusConflict
			}
		}
	}

	payload := map[string]any{
		"quota":   view,
		"fetched": snapshot != nil,
		"error":   errorString(err),
	}
	if result != nil {
		payload["result"] = result
	}
	if err == nil && result != nil {
		_ = s.store.RecordAudit(r.Context(), "account.reset_credit_consumed", account.Name+" ("+result.Code+")")
	}
	writeJSON(w, status, payload)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
