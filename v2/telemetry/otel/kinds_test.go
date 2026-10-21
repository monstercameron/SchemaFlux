package otel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/mw"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// A span, an event and the *Error the caller holds describe one failure, so
// they have to name one kind. These tests produce each kind through a real
// operation and a scripted provider and compare the three.

// finishRecorder is an Observer that keeps every Result it is told of.
type finishRecorder struct {
	mu      sync.Mutex
	results []telemetry.Result
}

func (r *finishRecorder) Started(ctx context.Context, _ telemetry.Event) context.Context { return ctx }

func (r *finishRecorder) Finished(_ context.Context, result telemetry.Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, result)
}

// ofScope returns the results of one scope, in the order they finished.
func (r *finishRecorder) ofScope(scope telemetry.Scope) []telemetry.Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	var found []telemetry.Result
	for _, result := range r.results {
		if result.Scope == scope {
			found = append(found, result)
		}
	}
	return found
}

// recordingFinishes registers a finishRecorder for the length of the test.
func recordingFinishes(t *testing.T) *finishRecorder {
	t.Helper()
	recorder := &finishRecorder{}
	t.Cleanup(telemetry.RegisterObserver(recorder))
	return recorder
}

// extractThrough runs one extraction on a client whose provider is provider, and
// returns what the caller of an operation holds: the error, as the root's
// *Error.
func extractThrough(provider schemaflux.Provider) (held *schemaflux.Error, returned error) {
	options := ops.NewExtractOptions()
	options.CommonOptions.Context = schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: provider, Model: "test-model"})
	_, returned = ops.ExtractResult[invoice]("Invoice from Acme", options)
	if returned != nil {
		errors.As(bind.ConvertError("extract", returned), &held)
	}
	return held, returned
}

// spanOfKind returns the one span of the kind of unit: the operation's is
// Internal and the request's is Client, told apart by name.
func operationAndRequest(t *testing.T, spans tracetest.SpanStubs) (operation, request tracetest.SpanStub) {
	t.Helper()
	var requests int
	for _, span := range spans {
		switch {
		case span.Name == "schemaflux.extract":
			operation = span
		case strings.HasPrefix(span.Name, "chat"):
			request = span
			requests++
		}
	}
	if operation.Name == "" || requests != 1 {
		t.Fatalf("%d spans: want one operation span and one request span, got an operation %q and %d requests", len(spans), operation.Name, requests)
	}
	return operation, request
}

func TestTheKindOnEverySpanAndEventIsTheKindTheCallerHolds(t *testing.T) {
	seen := 0
	for kind := schemaflux.ErrConfiguration; kind <= schemaflux.ErrInternal; kind++ {
		seen++
		t.Run(kind.Error(), func(t *testing.T) {
			_, exporter := installed(t)
			recorder := recordingFinishes(t)
			provider := &scripted{answer: func(int) (schemaflux.Response, error) {
				return schemaflux.Response{}, &schemaflux.Error{Kind: kind, Message: "a failure"}
			}}

			held, returned := extractThrough(provider)
			if held == nil {
				t.Fatalf("the call returned %v, which is not an *Error of the root", returned)
			}
			want := held.Kind
			operation, request := operationAndRequest(t, exporter.GetSpans())

			for _, span := range []tracetest.SpanStub{operation, request} {
				got, _ := attributeOf(span, "error.type")
				if got.AsString() != want.Error() || span.Status.Code != codes.Error || span.Status.Description != want.Error() {
					t.Errorf("%s: error.type %q, status %v %q; the caller holds %q", span.Name, got.AsString(), span.Status.Code, span.Status.Description, want.Error())
				}
			}
			for _, scope := range []telemetry.Scope{telemetry.ScopeOperation, telemetry.ScopeRequest} {
				results := recorder.ofScope(scope)
				if len(results) != 1 || results[0].Kind != want {
					t.Errorf("the %s's finish carries %+v; the caller holds %v", scope, results, want)
				}
			}
		})
	}
	if seen != 18 {
		t.Errorf("%d kinds were driven, want the eighteen", seen)
	}
}

// A copy of a hedge that panics is an internal error to the caller. It is a
// defect in code, and is told apart from an answer the model got wrong.
func TestAHedgeCopyThatPanickedIsInternalOnEverySpanAndEvent(t *testing.T) {
	_, exporter := installed(t)
	recorder := recordingFinishes(t)

	held, returned := extractThrough(mw.Hedge(5 * time.Millisecond)(panickingProvider{}))
	if held == nil || held.Kind != schemaflux.ErrInternal {
		t.Fatalf("the caller holds %v (%v), want an internal error", held, returned)
	}
	operation, request := operationAndRequest(t, exporter.GetSpans())
	for _, span := range []tracetest.SpanStub{operation, request} {
		if got, _ := attributeOf(span, "error.type"); got.AsString() != "internal" {
			t.Errorf("%s: error.type %q, want \"internal\"", span.Name, got.AsString())
		}
	}
	for _, result := range append(recorder.ofScope(telemetry.ScopeOperation), recorder.ofScope(telemetry.ScopeRequest)...) {
		if result.Kind != schemaflux.ErrInternal {
			t.Errorf("the %s's finish carries %v, want ErrInternal", result.Scope, result.Kind)
		}
	}
}

type panickingProvider struct{}

func (panickingProvider) Name() string                      { return "local" }
func (panickingProvider) RetryPolicy() (int, time.Duration) { return -1, time.Millisecond }
func (panickingProvider) Complete(context.Context, schemaflux.Request) (schemaflux.Response, error) {
	panic("the copy blew up")
}
