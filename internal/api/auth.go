package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"pi-gateway/internal/store"
)

// ErrUnauthorized is returned when a client key is missing or invalid.
var ErrUnauthorized = errors.New("unauthorized")

// extractClientKey pulls the client API key from the request.
//
// Both Authorization: Bearer <key> and x-api-key: <key> are accepted so any
// OpenAI-compatible client can be pointed at this gateway.
func extractClientKey(r *http.Request) string {
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		if len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
			return strings.TrimSpace(auth[7:])
		}
		return auth
	}
	if v := strings.TrimSpace(r.Header.Get("x-api-key")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.URL.Query().Get("api_key")); v != "" {
		return v
	}
	return ""
}

// authenticate resolves the client key and enforces the enabled flag.
func (s *Server) authenticate(ctx context.Context, r *http.Request) (*store.APIKey, error) {
	value := extractClientKey(r)
	if value == "" {
		return nil, ErrUnauthorized
	}
	key, err := s.store.GetKeyByValue(ctx, value)
	if err != nil {
		return nil, err
	}
	if key == nil || !key.Enabled {
		return nil, ErrUnauthorized
	}
	return key, nil
}
