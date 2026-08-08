package telemetry

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// MW-007's Revised line, and the reason this file exists.
//
// The library used to reach for OpenTelemetry directly: InitTracing builds
// exporters, picks an endpoint, chooses a sampler, and calls
// otel.SetTracerProvider -- it configures the *host's* telemetry stack. A
// library that does that cannot be embedded twice, and it cannot be embedded
// at all by a host that has already configured its own: whoever initialises
// last wins, silently.
//
// So the direction inverts. This package defines small interfaces the library
// emits through, and an adapter that knows about OpenTelemetry lives in
// telemetry/otel and is handed the host's provider. Nothing here imports
// OpenTelemetry, and nothing here initialises anything.

// OperationEvent describes an operation that is starting. It carries no
// payload: names, shapes, and identifiers only, because an observer's output
// is a log line or a span, and the caller's data belongs in neither.
type OperationEvent struct {
	Operation     string
	Provider      string
	Model         string
	RequestID     string
	CorrelationID string
	Mode          string
	Intelligence  string
}

// OperationResult describes an operation that has finished.
type OperationResult struct {
	Operation string
	Duration  time.Duration
	Usage     types.TokenUsage
	Cost      types.CostInfo

	// Attempts is every provider call the answer took.
	Attempts int

	// ErrorKind names the failure in the library's own taxonomy, empty on
	// success. It is the kind rather than the message, because a message can
	// quote a provider's echo of the caller's input and a kind cannot.
	ErrorKind string
}

// Observer receives what the library did. Implementations must be safe for
// concurrent use and must not block: an observer that stalls stalls the
// operation it is observing.
//
// OperationStarted returns a context so an implementation that opens a span
// can put it there; an implementation with nothing to add returns ctx.
type Observer interface {
	OperationStarted(ctx context.Context, event OperationEvent) context.Context
	OperationFinished(ctx context.Context, result OperationResult)
}

// nopObserver is the default. It exists so the emission sites never have to
// check for nil, and so a library with no observer configured costs nothing
// rather than branching on every call.
type nopObserver struct{}

func (nopObserver) OperationStarted(ctx context.Context, _ OperationEvent) context.Context {
	return ctx
}
func (nopObserver) OperationFinished(context.Context, OperationResult) {}

var (
	observerMu sync.RWMutex
	observer   Observer = nopObserver{}
)

// SetObserver installs the observer the library emits through, and returns a
// function that restores the previous one -- which is what a test needs, and
// what a host that installs an observer for part of its lifetime needs.
//
// Passing nil restores the no-op observer rather than panicking later at an
// emission site.
func SetObserver(next Observer) (restore func()) {
	observerMu.Lock()
	defer observerMu.Unlock()

	previous := observer
	if next == nil {
		next = nopObserver{}
	}
	observer = next

	return func() {
		observerMu.Lock()
		defer observerMu.Unlock()
		observer = previous
	}
}

// CurrentObserver returns the installed observer. It is never nil.
func CurrentObserver() Observer {
	observerMu.RLock()
	defer observerMu.RUnlock()
	return observer
}

// ObserveOperationStart emits a start event and returns the context the
// observer wants used for the rest of the operation.
func ObserveOperationStart(ctx context.Context, event OperationEvent) context.Context {
	return CurrentObserver().OperationStarted(ctx, event)
}

// ObserveOperationEnd emits a finish event.
func ObserveOperationEnd(ctx context.Context, result OperationResult) {
	CurrentObserver().OperationFinished(ctx, result)
}

// SpanIDSource reports the trace or span ID of whatever span is already on the
// context, or "" when there is none.
//
// These are hooks for the same reason the observer is (OB-001): reading two
// strings off an ambient span was, along with requesttracking's trace lookup,
// what kept OpenTelemetry in the build of every consumer of this library. The
// defaults return "", which is also what the previous implementation returned
// when tracing was disabled -- so nothing changes for anyone who was not
// tracing, and telemetry/otel installs the real readers for anyone who is.
type SpanIDSource func(context.Context) string

var (
	traceIDSource atomic.Pointer[SpanIDSource]
	spanIDSource  atomic.Pointer[SpanIDSource]
)

// SetSpanIDSources installs the readers and returns a function restoring the
// previous pair. Either may be nil, which restores that one's default.
func SetSpanIDSources(traceID, spanID SpanIDSource) (restore func()) {
	previousTrace := traceIDSource.Load()
	previousSpan := spanIDSource.Load()

	if traceID == nil {
		traceIDSource.Store(nil)
	} else {
		traceIDSource.Store(&traceID)
	}
	if spanID == nil {
		spanIDSource.Store(nil)
	} else {
		spanIDSource.Store(&spanID)
	}

	return func() {
		traceIDSource.Store(previousTrace)
		spanIDSource.Store(previousSpan)
	}
}

// GetTraceID returns the ambient trace ID, or "" when nothing is tracing.
func GetTraceID(ctx context.Context) string { return readSpanID(traceIDSource.Load(), ctx) }

// GetSpanID returns the ambient span ID, or "" when nothing is tracing.
func GetSpanID(ctx context.Context) string { return readSpanID(spanIDSource.Load(), ctx) }

func readSpanID(source *SpanIDSource, ctx context.Context) string {
	if source == nil || ctx == nil {
		return ""
	}
	return (*source)(ctx)
}
