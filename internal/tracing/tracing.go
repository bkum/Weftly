// Package tracing wires OpenTelemetry span emission for the engine.
// A single global TracerProvider is installed via Init; when no
// endpoint is configured the package returns a no-op tracer so
// callers can always call Start without a nil check.
//
// Design goals:
//   - Zero runtime cost when tracing is off (Start returns a no-op span).
//   - No panics on Init failure — a broken exporter shouldn't take the
//     workflow down; log and continue with the no-op path.
//   - Simple two-call surface: Init(endpoint) once at startup, Start()
//     per operation. Callers own defer span.End().
package tracing

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

var (
	initOnce sync.Once
	tp       *sdktrace.TracerProvider
	tracer   trace.Tracer = noop.NewTracerProvider().Tracer("weftly")
	enabled  bool
)

// Init sets up the global tracer provider to export to endpoint via
// OTLP/HTTP (usually http://collector:4318). Empty endpoint disables
// tracing — subsequent Start calls become no-ops. Safe to call
// multiple times; only the first takes effect.
//
// The service name is baked as "weftly"; the resource picks up host
// name for basic identity. Callers should defer Shutdown from the
// same goroutine that called Init.
func Init(endpoint string, log *slog.Logger) {
	if endpoint == "" {
		return
	}
	initOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		hostname, _ := os.Hostname()
		res, err := resource.New(ctx,
			resource.WithAttributes(
				attribute.String("service.name", "weftly"),
				attribute.String("host.name", hostname),
			),
		)
		if err != nil {
			log.Warn("tracing: build resource failed; disabling", "err", err)
			return
		}
		exp, err := otlptracehttp.New(ctx,
			otlptracehttp.WithEndpointURL(endpoint),
		)
		if err != nil {
			log.Warn("tracing: otlp exporter init failed; disabling", "err", err)
			return
		}
		tp = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exp),
			sdktrace.WithResource(res),
		)
		otel.SetTracerProvider(tp)
		tracer = tp.Tracer("weftly")
		enabled = true
		log.Info("tracing: OTLP/HTTP enabled", "endpoint", endpoint)
	})
}

// Start creates a span rooted at the tracer. Callers must End() the
// returned span — an error return value is unnecessary because a
// disabled tracer returns a no-op span that still satisfies the
// interface.
func Start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{trace.WithAttributes(attrs...)}
	return tracer.Start(ctx, name, opts...)
}

// Enabled reports whether tracing is actively exporting spans. Used
// by tests that want to skip when no collector is available.
func Enabled() bool { return enabled }

// Shutdown flushes pending spans. Safe on a disabled tracer.
func Shutdown(ctx context.Context) error {
	if tp == nil {
		return nil
	}
	if err := tp.Shutdown(ctx); err != nil {
		return fmt.Errorf("tracing: shutdown: %w", err)
	}
	return nil
}
