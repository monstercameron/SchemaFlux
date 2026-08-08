// Package otel adapts SchemaFlux's observer interface to OpenTelemetry.
//
// It is a separate package on purpose (MW-007, ARC-17, ARC-18). The library's
// core defines the observer interface and emits through it; this package is
// the only thing that knows OpenTelemetry exists. A host that does not import
// it does not get OpenTelemetry's behaviour, and a host that does hands over
// its own provider.
//
// What this package will not do, and why:
//
//   - It does not call otel.SetTracerProvider. That is the host's global, and
//     a library that sets it cannot be embedded twice -- whichever copy
//     initialises last silently wins.
//   - It does not build exporters, choose an endpoint, or pick a sampler.
//     Those are deployment decisions, and a library that makes them has
//     decided where its host's telemetry goes.
//   - It does not own shutdown. It did not start anything, so it has nothing
//     to stop; the host flushes its own provider.
//
// The previous arrangement did all four, in telemetry.InitTracing, which is
// now deprecated.
package otel

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/telemetry"
)

// instrumentationName identifies this library as the source of the spans it
// produces, which is what lets a host filter them.
const instrumentationName = "github.com/monstercameron/schemaflux"

// Observer implements telemetry.Observer by opening a span per operation on a
// tracer taken from the host's provider.
type Observer struct {
	tracer oteltrace.Tracer
}

// New returns an observer that traces onto the provider the host supplies.
//
// A nil provider is a caller mistake rather than a reason to reach for the
// global one: silently falling back to otel.GetTracerProvider() is how a
// library ends up writing into a stack nobody asked it to write into.
func New(provider oteltrace.TracerProvider) (*Observer, error) {
	if provider == nil {
		return nil, fmt.Errorf("otel: no TracerProvider; pass the one your application configured")
	}
	return &Observer{tracer: provider.Tracer(instrumentationName)}, nil
}

// Install wires the observer into the library and returns the restore function
// SetObserver hands back, so a host can put things back as they were.
func Install(provider oteltrace.TracerProvider) (restore func(), err error) {
	observer, err := New(provider)
	if err != nil {
		return nil, err
	}
	return telemetry.SetObserver(observer), nil
}

// OperationStarted opens a span and returns the context carrying it.
func (o *Observer) OperationStarted(ctx context.Context, event telemetry.OperationEvent) context.Context {
	if o == nil || o.tracer == nil {
		return ctx
	}

	ctx, span := o.tracer.Start(ctx, "schemaflux."+event.Operation,
		oteltrace.WithSpanKind(oteltrace.SpanKindClient))

	// Names, identifiers, and settings only. The caller's payload is not here
	// and must never be: a span attribute is exported to whatever backend the
	// host configured, which is the least controlled place this library could
	// put somebody's invoice.
	span.SetAttributes(
		attribute.String("schemaflux.operation", event.Operation),
		attribute.String("schemaflux.provider", event.Provider),
		attribute.String("schemaflux.model", event.Model),
		attribute.String("schemaflux.request_id", event.RequestID),
		attribute.String("schemaflux.correlation_id", event.CorrelationID),
		attribute.String("schemaflux.mode", event.Mode),
		attribute.String("schemaflux.intelligence", event.Intelligence),
	)
	return ctx
}

// OperationFinished records the outcome on the span this operation opened and
// ends it.
func (o *Observer) OperationFinished(ctx context.Context, result telemetry.OperationResult) {
	if o == nil {
		return
	}

	span := oteltrace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	defer span.End()

	span.SetAttributes(
		attribute.Int64("schemaflux.duration_ms", result.Duration.Milliseconds()),
		attribute.Int("schemaflux.attempts", result.Attempts),
		attribute.Int("schemaflux.tokens.prompt", result.Usage.PromptTokens),
		attribute.Int("schemaflux.tokens.completion", result.Usage.CompletionTokens),
		attribute.Int("schemaflux.tokens.total", result.Usage.TotalTokens),
		attribute.Int("schemaflux.tokens.cached", result.Usage.CachedTokens),
	)

	// Cost is recorded only when it was actually priced. An unpriced call
	// reporting a cost of zero is indistinguishable from a free one, and
	// PR-001 exists because that distinction is the whole point.
	if result.Cost.Priced {
		span.SetAttributes(attribute.Float64("schemaflux.cost.total", result.Cost.TotalCost))
	} else {
		span.SetAttributes(attribute.Bool("schemaflux.cost.unpriced", true))
	}

	if result.ErrorKind != "" {
		// The kind, not the message: a provider's error message can quote the
		// caller's input back, and a span is an export.
		span.SetAttributes(attribute.String("schemaflux.error.kind", result.ErrorKind))
		span.SetStatus(codes.Error, result.ErrorKind)
	}
}

// InstallIDSources lets the core read the trace and span IDs of a span already
// on the context without importing OpenTelemetry itself.
//
// internal/requesttracking used to call oteltrace.SpanFromContext directly for
// this one string, which was the last OpenTelemetry import anywhere in the
// core -- so every consumer of this library compiled against the vendor
// whether or not they used it. It is a hook now, defaulting to "", and this
// installs the real reader. Returns the restore function.
func InstallIDSources() (restore func()) {
	restoreTracking := requesttracking.SetTraceIDSource(traceIDFromContext)
	restoreTelemetry := telemetry.SetSpanIDSources(traceIDFromContext, spanIDFromContext)

	return func() {
		restoreTelemetry()
		restoreTracking()
	}
}

func traceIDFromContext(ctx context.Context) string {
	spanContext := oteltrace.SpanFromContext(ctx).SpanContext()
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceID().String()
}

func spanIDFromContext(ctx context.Context) string {
	spanContext := oteltrace.SpanFromContext(ctx).SpanContext()
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.SpanID().String()
}
