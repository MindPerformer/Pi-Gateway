package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"pi-gateway/internal/session"
)

// This identity is private to one downstream WebSocket, not a wire session hint.
type downstreamWSContextKey struct{}

func internalSessionID(ctx context.Context) string {
	if id, ok := ctx.Value(downstreamWSContextKey{}).(string); ok && id != "" {
		return id
	}
	return uuid.NewString()
}

func poolSessionID(ctx context.Context, logicalSession string) string {
	connection, _ := ctx.Value(downstreamWSContextKey{}).(string)
	return session.Fingerprint("downstream-pool", connection+"\x00"+logicalSession)
}

func (s *Server) resolveContinuation(ctx context.Context, keyID int64, raw any) (*session.Record, string, *apiError) {
	if raw == nil {
		return nil, "", nil
	}
	previous, ok := raw.(string)
	if !ok || strings.TrimSpace(previous) == "" || len(previous) > 4096 {
		return nil, "", continuationAPIError(session.ErrInvalid)
	}
	record, err := s.upstream.ResolveContinuation(ctx, keyID, previous)
	if err != nil {
		return nil, "", continuationAPIError(err)
	}
	return record, previous, nil
}

// Missing, corrupt and foreign records deliberately have the same public error:
// knowing a response ID is not a capability to probe another client's session.
func continuationAPIError(err error) *apiError {
	if errors.Is(err, context.Canceled) {
		return &apiError{Status: 499, Type: "cancelled", Code: "client_closed_request", Message: "client closed request"}
	}
	if errors.Is(err, session.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) {
		return &apiError{Status: http.StatusServiceUnavailable, Type: "server_error", Code: "session_state_unavailable", Message: "Continuation state is temporarily unavailable; no new response was sent upstream."}
	}
	return &apiError{Status: http.StatusBadRequest, Type: "invalid_request_error", Code: "previous_response_not_found", Message: "The previous response cannot be continued. Start a new chain with complete input and without previous_response_id."}
}
