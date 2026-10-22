package accounts

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAcquisitionQueryErrorActiveContext(t *testing.T) {
	driverErr := errors.New("driver query failure")
	if got := acquisitionQueryError(t.Context(), driverErr); got != driverErr {
		t.Fatalf("active-context driver error changed: %v", got)
	}
	if got := acquisitionQueryError(t.Context(), nil); got != nil {
		t.Fatalf("successful query became failure: %v", got)
	}
}

func TestAcquisitionQueryErrorCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assertAcquisitionQueryError(t, ctx, context.Canceled)
}

func TestAcquisitionQueryErrorDeadlineContext(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Unix(0, 0))
	defer cancel()
	assertAcquisitionQueryError(t, ctx, context.DeadlineExceeded)
}

func assertAcquisitionQueryError(t *testing.T, ctx context.Context, want error) {
	t.Helper()
	driverErr := errors.New("interrupted (9)")
	got := acquisitionQueryError(ctx, driverErr)
	if !errors.Is(got, want) || !errors.Is(got, driverErr) {
		t.Fatalf("request/driver cause lost: %v", got)
	}
	if got := acquisitionQueryError(ctx, nil); got != nil {
		t.Fatalf("successful query became failure after cancellation: %v", got)
	}
	if got := acquisitionQueryError(ctx, want); got != want {
		t.Fatalf("existing context error was unnecessarily wrapped: %v", got)
	}
}
