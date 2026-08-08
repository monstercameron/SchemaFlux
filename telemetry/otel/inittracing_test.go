package otel

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// InitTracing has to succeed, and until now it could not.
//
// It built its resource with `resource.Merge(resource.Default(), ...)` against a
// pinned `semconv/v1.17.0`, while the SDK's own `resource.Default()` carries
// schema 1.41.0. Merging two resources with conflicting schema URLs is an error,
// so every call returned "failed to create resource" and returned early —
// leaving the exporters, the sampler, the tracer, and `otel.SetTracerProvider`
// unreachable. Roughly thirty statements below the merge could not run, and no
// test noticed because none of them called the function with tracing enabled.
//
// Bumping the import to `semconv/v1.41.0` fixes it: the only three symbols this
// file uses — SchemaURL, ServiceName, ServiceVersion — all exist there.
//
// `ServiceVersion` also stopped claiming "1.0.0" as a literal and reads
// types.Version, so a trace says which build produced it rather than a string
// that was wrong for every release before this one and will be wrong again
// after the next.
func TestInitTracingSucceedsAndBuildsATracer(t *testing.T) {
	t.Setenv("SCHEMAFLUX_ENABLE_TRACING", "true")
	t.Setenv("SCHEMAFLUX_TRACE_EXPORTER", "stdout")
	t.Cleanup(func() { _ = ShutdownTracing(context.Background()) })

	if err := InitTracing("schemaflux-test"); err != nil {
		t.Fatalf("InitTracing: %v\n\nIf this says 'failed to create resource', the semconv "+
			"import has drifted from the SDK's schema again — see this test's doc comment.", err)
	}

	if !tracingEnabled {
		t.Error("InitTracing returned nil but left tracing disabled")
	}
	if tracer == nil {
		t.Error("InitTracing returned nil but built no tracer; every StartSpan would be a no-op")
	}

	// And the span it produces has to be real, which is the whole point of the
	// thirty statements that used to be unreachable.
	ctx, span := StartSpan(context.Background(), "probe", types.OpOptions{})
	defer span.End()

	if id := GetTraceID(ctx); id == "" {
		t.Error("a span started after InitTracing carries no trace ID")
	}
	if id := GetSpanID(ctx); id == "" {
		t.Error("a span started after InitTracing carries no span ID")
	}
}

// Disabled stays disabled, and reports no error. A library that failed to start
// because tracing was off would be worse than one that never traced.
func TestInitTracingStaysOffWhenNotAskedFor(t *testing.T) {
	t.Setenv("SCHEMAFLUX_ENABLE_TRACING", "")
	t.Setenv("SCHEMAFLUX_TRACE", "")

	if err := InitTracing("schemaflux-test"); err != nil {
		t.Fatalf("InitTracing with tracing off returned an error: %v", err)
	}
}
