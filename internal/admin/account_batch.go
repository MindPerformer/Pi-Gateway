package admin

import (
	"fmt"
	"net/http"
	"pi-gateway/internal/store"
)

type accountOperationResult struct {
	Index  int    `json:"index"`
	ID     int64  `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func validAccountIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 || len(ids) > 1000 {
		return nil, fmt.Errorf("select 1 to 1000 accounts")
	}
	seen := map[int64]bool{}
	out := []int64{}
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("account IDs must be positive integers")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *Server) handleBatchAccounts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs      []int64 `json:"ids"`
		Action   string  `json:"action"`
		GroupIDs []int64 `json:"group_ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ids, err := validAccountIDs(body.IDs)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	switch body.Action {
	case "enable", "disable", "recover":
	case "add_groups":
		if len(body.GroupIDs) == 0 || len(body.GroupIDs) > 100 {
			writeErr(w, 400, "select 1 to 100 groups")
			return
		}
		for _, id := range body.GroupIDs {
			if id <= 0 {
				writeErr(w, 400, "invalid group ID")
				return
			}
			group, err := s.store.GetAccountGroup(r.Context(), id)
			if err != nil {
				writeErr(w, 500, "could not load account groups")
				return
			}
			if group == nil {
				writeErr(w, 400, "account group not found")
				return
			}
		}
	default:
		writeErr(w, 400, "action must be enable, disable, recover or add_groups")
		return
	}
	results := []accountOperationResult{}
	for i, id := range ids {
		result := accountOperationResult{Index: i, ID: id, Status: "success"}
		account, err := s.store.GetAccount(r.Context(), id)
		if err == nil && account == nil {
			err = fmt.Errorf("account not found")
		}
		if err == nil {
			result.Name = account.Name
			switch body.Action {
			case "enable", "disable":
				enabled := body.Action == "enable"
				if enabled && account.AccessToken == "" {
					err = fmt.Errorf("link ChatGPT before enabling this account")
				} else {
					err = s.store.PatchAccountManagementFields(r.Context(), id, store.AccountManagementPatch{Enabled: &enabled})
				}
			case "recover":
				err = s.store.RecoverAccount(r.Context(), id)
			case "add_groups":
				err = s.store.AddAccountGroups(r.Context(), id, body.GroupIDs)
			}
		}
		if err != nil {
			result.Status = "failed"
			result.Error = "account operation failed"
			if account == nil {
				result.Error = "account not found or unavailable"
			}
			if account != nil && body.Action == "enable" && account.AccessToken == "" {
				result.Error = "link ChatGPT before enabling this account"
			}
		}
		results = append(results, result)
	}
	_ = s.store.RecordAudit(r.Context(), "account.batch", fmt.Sprintf("%s: %d accounts", body.Action, len(ids)))
	writeJSON(w, 200, map[string]any{"results": results})
}
