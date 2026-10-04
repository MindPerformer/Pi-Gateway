package middleware

import (
	"context"
	"strings"
	"testing"

	"pi-gateway/internal/piwire"
)

// TestDropEnvironmentContextHandlesStringInput verifies the environment block is
// stripped even when a client sends the Responses "input" as a bare string. The
// array branch was already covered; the string form previously bypassed cleanup
// and would leak local context upstream.
func TestDropEnvironmentContextHandlesStringInput(t *testing.T) {
	body := piwire.NewOrderedMap()
	body.Set("input", "<environment_context>cwd=/secret\nhost=dev</environment_context>\nhello")

	req := &Request{Body: body}
	res, err := (&DropEnvironmentContext{}).Apply(context.Background(), req, map[string]any{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got, _ := req.Body.Get("input")
	s, ok := got.(string)
	if !ok {
		t.Fatalf("input type = %T, want string", got)
	}
	if strings.Contains(s, "environment_context") || strings.Contains(s, "/secret") {
		t.Errorf("environment block survived: %q", s)
	}
	if !strings.Contains(s, "hello") {
		t.Errorf("user text was lost: %q", s)
	}
	if len(res.Changes) == 0 {
		t.Errorf("expected a change note for the stripped block")
	}
}

// TestDropEnvironmentContextLeavesCleanStringInputAlone verifies a string without
// any environment block is passed through untouched.
func TestDropEnvironmentContextLeavesCleanStringInputAlone(t *testing.T) {
	body := piwire.NewOrderedMap()
	body.Set("input", "just a question")

	req := &Request{Body: body}
	res, err := (&DropEnvironmentContext{}).Apply(context.Background(), req, map[string]any{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, _ := req.Body.Get("input")
	if got != "just a question" {
		t.Errorf("clean string input was modified: %v", got)
	}
	if len(res.Changes) != 0 {
		t.Errorf("unexpected changes: %v", res.Changes)
	}
}
