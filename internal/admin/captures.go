package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pi-gateway/internal/store"
)

// handleListCaptures returns capture summaries (payload columns omitted).
func (s *Server) handleListCaptures(w http.ResponseWriter, r *http.Request) {
	filter := captureFilterFromQuery(r)
	items, total, err := s.store.ListCaptures(r.Context(), filter)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"captures": items,
		"total":    total,
		"limit":    filter.Limit,
		"offset":   filter.Offset,
	})
}

func captureFilterFromQuery(r *http.Request) store.CaptureFilter {
	q := r.URL.Query()
	f := store.CaptureFilter{
		Outcome:   strings.TrimSpace(q.Get("outcome")),
		Transport: strings.TrimSpace(q.Get("transport")),
		Search:    strings.TrimSpace(q.Get("search")),
	}
	if v, err := strconv.ParseInt(q.Get("account_id"), 10, 64); err == nil {
		f.AccountID = v
	}
	if v, err := strconv.ParseInt(q.Get("api_key_id"), 10, 64); err == nil {
		f.APIKeyID = v
	}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil {
		f.Limit = v
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil {
		f.Offset = v
	}
	f.IncludePayloads = q.Get("include_payloads") == "true"
	return f
}

// handleGetCapture returns one full capture including headers and frames.
func (s *Server) handleGetCapture(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := s.store.GetCapture(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if c == nil {
		writeErr(w, http.StatusNotFound, "capture not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"capture": c})
}

func (s *Server) handleDeleteCapture(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.DeleteCapture(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleClearCaptures(w http.ResponseWriter, r *http.Request) {
	var accountID int64
	if v, err := strconv.ParseInt(r.URL.Query().Get("account_id"), 10, 64); err == nil {
		accountID = v
	}
	n, err := s.store.ClearCaptures(r.Context(), accountID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.RecordAudit(r.Context(), "captures.cleared", fmt.Sprintf("%d records", n))
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

func exportCaptureLimit(raw string) int {
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 || v > 5000 {
		return 1000
	}
	return v
}

// handleExportCaptures exports captured exchanges as one JSON object per line.
func (s *Server) handleExportCaptures(w http.ResponseWriter, r *http.Request) {
	filter := captureFilterFromQuery(r)
	filter.IncludePayloads = true
	filter.Limit = exportCaptureLimit(r.URL.Query().Get("limit"))
	filter.Offset = 0

	// ListCaptures caps each page at 500. Fetch bounded pages so export limits
	// up to 5000 (including the default 1000) are not reset to its default 50.
	remaining := filter.Limit
	var items []*store.Capture
	var total int
	for remaining > 0 {
		filter.Limit = min(remaining, 500)
		page, count, err := s.store.ListCaptures(r.Context(), filter)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		total = count
		items = append(items, page...)
		remaining -= len(page)
		filter.Offset += len(page)
		if len(page) < filter.Limit {
			break
		}
	}

	filename := fmt.Sprintf("pi-gateway-captures-%s.jsonl", time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	for _, c := range items {
		if err := enc.Encode(c); err != nil {
			// The client went away; nothing more to stream.
			return
		}
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
