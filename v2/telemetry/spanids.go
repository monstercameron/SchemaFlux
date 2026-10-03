package telemetry

import (
	"context"

	wrapped "github.com/monstercameron/schemaflux/telemetry"
)

// SetSpanIDSources installs the readers TraceID and SpanID use and returns the
// function that removes this install. Each reader answers with the trace or
// span id of whatever span is already on the context it is given, or the empty
// string when there is none. Either may be nil, which gives that reader its
// default, the empty string.
//
// The library reads two strings off an ambient span only through these hooks, so
// that linking the library does not link a tracing vendor. telemetry/otel's
// InstallIDSources installs the readers for OpenTelemetry, and a host that
// adds the observer itself calls it beside RegisterObserver.
//
// The newest install that has not been removed is the one in effect. Removals
// may run in any order and more than once; one that runs out of order never
// brings back a pair whose own removal has already run.
func SetSpanIDSources(traceID, spanID func(context.Context) string) (restore func()) {
	return wrapped.SetSpanIDSources(traceID, spanID)
}

// TraceID returns the trace id of the span on ctx, as the installed reader
// reports it, or the empty string when no reader is installed or the context
// carries no span. A nil ctx has none.
func TraceID(ctx context.Context) string { return wrapped.GetTraceID(ctx) }

// SpanID returns the span id of the span on ctx, as the installed reader reports
// it, or the empty string when no reader is installed or the context carries no
// span. A nil ctx has none.
func SpanID(ctx context.Context) string { return wrapped.GetSpanID(ctx) }
