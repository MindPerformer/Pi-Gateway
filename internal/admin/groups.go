package admin

import (
	"database/sql"
	"errors"
	"net/http"

	"pi-gateway/internal/store"
)

// A grouped key reaches enabled groups plus wholly ungrouped public accounts.
// An ungrouped key reaches the entire enabled pool.
func (s *Server) handleListAccountGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.store.ListAccountGroups(r.Context())
	if err != nil {
		writePolicyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

func (s *Server) handleCreateAccountGroup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SwitchOn429    string   `json:"switch_on_429"`
		Name           string   `json:"name"`
		Enabled        *bool    `json:"enabled"`
		Notes          string   `json:"notes"`
		AccountIDs     []int64  `json:"account_ids"`
		DisabledModels []string `json:"disabled_models"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	g := &store.AccountGroup{Name: body.Name, Enabled: true, Notes: body.Notes,
		AccountIDs: body.AccountIDs, DisabledModels: body.DisabledModels, SwitchOn429: body.SwitchOn429}
	if body.Enabled != nil {
		g.Enabled = *body.Enabled
	}
	// Properties, members and restrictions are persisted in one transaction.
	if err := s.store.CreateAccountGroup(r.Context(), g); err != nil {
		writePolicyError(w, err)
		return
	}
	_ = s.store.RecordAudit(r.Context(), "account_group.created", g.Name)
	writeJSON(w, http.StatusOK, map[string]any{"group": g})
}

func (s *Server) handleUpdateAccountGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		SwitchOn429    *string  `json:"switch_on_429"`
		Name           *string  `json:"name"`
		Enabled        *bool    `json:"enabled"`
		Notes          *string  `json:"notes"`
		AccountIDs     []int64  `json:"account_ids"`
		DisabledModels []string `json:"disabled_models"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	patch := store.AccountGroupPatch{Name: body.Name, Enabled: body.Enabled, Notes: body.Notes,
		AccountIDs: body.AccountIDs, DisabledModels: body.DisabledModels, SwitchOn429: body.SwitchOn429}
	if err := s.store.PatchAccountGroup(r.Context(), id, patch); err != nil {
		writePolicyError(w, err)
		return
	}
	g, err := s.store.GetAccountGroup(r.Context(), id)
	if err != nil {
		writePolicyError(w, err)
		return
	}
	if g == nil {
		writePolicyError(w, sql.ErrNoRows)
		return
	}
	_ = s.store.RecordAudit(r.Context(), "account_group.updated", g.Name)
	writeJSON(w, http.StatusOK, map[string]any{"group": g})
}

func (s *Server) handleDeleteAccountGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	g, err := s.store.GetAccountGroup(r.Context(), id)
	if err != nil {
		writePolicyError(w, err)
		return
	}
	if g == nil {
		writePolicyError(w, sql.ErrNoRows)
		return
	}
	if err := s.store.DeleteAccountGroup(r.Context(), id); err != nil {
		writePolicyError(w, err)
		return
	}
	// Keys losing their last group intentionally become ungrouped (whole pool).
	_ = s.store.RecordAudit(r.Context(), "account_group.deleted", g.Name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSetAccountGroupAccounts(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		AccountIDs []int64 `json:"account_ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SetAccountGroupAccounts(r.Context(), id, body.AccountIDs); err != nil {
		writePolicyError(w, err)
		return
	}
	g, err := s.store.GetAccountGroup(r.Context(), id)
	if err != nil {
		writePolicyError(w, err)
		return
	}
	if g == nil {
		writePolicyError(w, sql.ErrNoRows)
		return
	}
	_ = s.store.RecordAudit(r.Context(), "account_group.members", g.Name)
	writeJSON(w, http.StatusOK, map[string]any{"group": g})
}

func writePolicyError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrGroupNameConflict):
		status = http.StatusConflict
	case errors.Is(err, store.ErrInvalidPolicy):
		status = http.StatusBadRequest
	case errors.Is(err, sql.ErrNoRows):
		status = http.StatusNotFound
	}
	writeErr(w, status, err.Error())
}
