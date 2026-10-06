package rules

import (
	"strings"
	"testing"
)

func TestTraceArrayDeletionKeepsSurvivors(t *testing.T) {
	a := []any{map[string]any{"id": "first"}, map[string]any{"id": "environment"}, map[string]any{"id": "time"}, map[string]any{"id": "user"}}
	b := []any{a[0], a[2], a[3]}
	changes, omitted := structuralDiff(a, b)
	if omitted != 0 || len(changes) != 1 || changes[0].Path != "/1" || changes[0].Operation != "remove" {
		t.Fatalf("shifted survivors reported as edits: %+v", changes)
	}
	large := strings.Repeat("完整", 100_000)
	value, clipped := boundValue(large, "/text")
	if clipped || value != large {
		t.Fatal("large trace value cropped")
	}
}
