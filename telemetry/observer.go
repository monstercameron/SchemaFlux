package telemetry

import (
	"context"
	"sync"
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
