package tracing

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func TestInitEmptyEndpointLeavesDisabled(t *testing.T) {
	// A fresh Init("") must not enable tracing.
	Init("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if Enabled() {
		t.Fatal("empty endpoint should leave tracing disabled")
	}
}

func TestStartOnDisabledReturnsNoopSpan(t *testing.T) {
	// With tracing off, Start must still work and return a span
	// callers can End without a nil check.
	ctx, span := Start(context.Background(), "op")
	if ctx == nil {
		t.Fatal("nil ctx from Start")
	}
	if span == nil {
		t.Fatal("nil span from Start")
	}
	span.End() // must not panic
}

func TestShutdownWithoutInitIsSafe(t *testing.T) {
	// Even when Init was never called (or was called with ""),
	// Shutdown must be a no-op rather than an nil-pointer panic.
	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown on disabled: %v", err)
	}
}
