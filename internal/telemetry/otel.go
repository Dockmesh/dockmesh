// Package telemetry wires OpenTelemetry tracing (P.12.3). Optional —
// if no OTLP endpoint is configured, Init returns a no-op shutdown
// func and everything stays a zero-overhead passthrough.
package telemetry

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Config is the minimal wiring surface.
type Config struct {
	Endpoint       string // "host:4317" — empty disables tracing entirely
	Insecure       bool   // plaintext gRPC (dev / same-host collector)
	ServiceName    string
	ServiceVersion string
	Environment    string
}

// Init bootstraps the tracer provider and installs it as the global
// otel tracer + propagator. Returns a Shutdown func that should be
// deferred from main — it flushes any in-flight spans to the OTLP
// collector. When Endpoint is empty, Init is a no-op that returns a
// cheap no-op shutdown.
func Init(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) {
	if cfg.Endpoint == "" {
		slog.Info("otel tracing disabled (DOCKMESH_OTEL_ENDPOINT not set)")
		return func(context.Context) error { return nil }, nil
	}

	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.Endpoint)}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("otlp grpc exporter: %w", err)
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	slog.Info("otel tracing enabled",
		"endpoint", cfg.Endpoint, "insecure", cfg.Insecure,
		"service", cfg.ServiceName, "version", cfg.ServiceVersion)

	return tp.Shutdown, nil
}
