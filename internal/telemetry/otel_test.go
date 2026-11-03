package telemetry

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestInitStartsProvider(t *testing.T) {
	prev := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	shutdown, err := Init(context.Background(), Config{
		Endpoint:       "127.0.0.1:4317",
		Insecure:       true,
		ServiceName:    "dockmesh",
		ServiceVersion: "test",
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		_ = shutdown(ctx)
	})
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
		t.Fatalf("global provider is %T, want the SDK provider", otel.GetTracerProvider())
	}
	_, span := otel.Tracer("test").Start(context.Background(), "probe")
	defer span.End()
	ro, ok := span.(sdktrace.ReadOnlySpan)
	if !ok {
		t.Fatalf("span is %T, not recorded by the SDK", span)
	}
	attrs := map[attribute.Key]string{}
	for _, kv := range ro.Resource().Attributes() {
		attrs[kv.Key] = kv.Value.String()
	}
	if attrs["service.name"] != "dockmesh" || attrs["service.version"] != "test" {
		t.Fatalf("resource attributes %v", attrs)
	}
}

func TestInitDisabled(t *testing.T) {
	shutdown, err := Init(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
