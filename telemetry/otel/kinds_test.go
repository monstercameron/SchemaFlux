package otel_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/mw"
	"github.com/monstercameron/schemaflux/telemetry"
)

// A span, an event and the error the caller holds describe one failure, so they
// have to name one kind. These tests produce each kind through a real operation
// and a scripted provider and compare what the observers were told with what the
// caller holds.

// finishRecorder is an observer that keeps every result it is told of.
type finishRecorder struct {
	mu      sync.Mutex
	results []telemetry.OperationResult
}

func (r *finishRecorder) OperationStarted(ctx context.Context, _ telemetry.OperationEvent) context.Context {
	return ctx
}

func (r *finishRecorder) OperationFinished(_ context.Context, result telemetry.OperationResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, result)
}

// ofScope returns the results of one scope, in the order they finished.
func (r *finishRecorder) ofScope(scope telemetry.Scope) []telemetry.OperationResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	var found []telemetry.OperationResult
	for _, result := range r.results {
		if result.Scope == scope {
			found = append(found, result)
		}
	}
	return found
}

// recordingFinishes installs a finishRecorder for the length of the test.
func recordingFinishes(t *testing.T) *finishRecorder {
	t.Helper()
	recorder := &finishRecorder{}
	t.Cleanup(telemetry.AddObserver(recorder))
	return recorder
}

// callerHolds is the name the kind of err has for somebody who reads it as the
// library documents: its kind, except that a refusal and a recovered panic are
// told apart by their marks. It reads types, not the code under test.
func callerHolds(err error) string {
	switch types.NarrowingOf(err) {
	case types.NarrowRefusal:
		return "refused"
	case types.NarrowPanic:
		return "internal"
	}
	return types.KindOf(err).String()
}

// operationAndRequest returns the one operation span and the one request span
// of a call that made a single request.
func operationAndRequest(t *testing.T, spans tracetest.SpanStubs) (operation, request tracetest.SpanStub) {
	t.Helper()
	operations, requestSpans := named(spans, "schemaflux.extract"), requests(spans)
	if len(operations) != 1 || len(requestSpans) != 1 {
		t.Fatalf("%d operation spans and %d request spans, want one of each:\n%s", len(operations), len(requestSpans), describe(spans))
	}
	return operations[0], requestSpans[0]
}

// failureCase is one failure a provider can return, and the name the caller's
// error has for it.
type failureCase struct {
	name string
	err  error
	want string
}

func TestTheKindOnEverySpanAndEventIsTheKindTheCallerHolds(t *testing.T) {
	var failures []failureCase
	for kind := types.KindConfiguration; kind.String() != types.KindUnknown.String(); kind++ {
		failures = append(failures, failureCase{kind.String(), types.Newf(kind, "provider", "a failure"), kind.String()})
	}
	panicked := types.Newf(types.KindInvariantViolation, "provider", "a failure")
	panicked.Narrow = types.NarrowPanic
	failures = append(failures,
		failureCase{"a refusal", types.ClassifiedRefusal(types.Newf(types.KindPolicyViolation, "provider", "declined")), "refused"},
		failureCase{"a recovered panic", panicked, "internal"},
	)
	if len(failures) < 25 {
		t.Fatalf("only %d failures: the kinds were not enumerated", len(failures))
	}

	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			provider, exporter := traced(t)
			recorder := recordingFinishes(t)
			var returned error
			spans := underCaller(provider, exporter, func(ctx context.Context) {
				useProvider(t, &scriptedProvider{answer: func(context.Context, int, llm.CompletionRequest) (llm.CompletionResponse, error) {
					return llm.CompletionResponse{}, tc.err
				}})
				_, returned = ops.ExtractResult[invoice]("Invoice from Acme", extractOptions(ctx))
			})
			if returned == nil {
				t.Fatal("the call did not fail")
			}
			if held := callerHolds(returned); held != tc.want {
				t.Fatalf("the caller holds %q, want %q: the test is not looking at the kind it set out to", held, tc.want)
			}
			operation, request := operationAndRequest(t, spans)

			for _, span := range []tracetest.SpanStub{operation, request} {
				got, _ := attributeOf(span, "error.type")
				if got.AsString() != tc.want || span.Status.Code != codes.Error || span.Status.Description != tc.want {
					t.Errorf("%s: error.type %q, status %v %q; the caller holds %q", span.Name, got.AsString(), span.Status.Code, span.Status.Description, tc.want)
				}
			}
			for _, scope := range []telemetry.Scope{telemetry.ScopeOperation, telemetry.ScopeRequest} {
				results := recorder.ofScope(scope)
				if len(results) != 1 || results[0].ErrorKind != tc.want {
					t.Errorf("the %s's finish carries %+v; the caller holds %q", scope, results, tc.want)
				}
			}
		})
	}
}

// A copy of a hedge that panics is an internal error to the caller: a defect in
// code, told apart from an answer the model got wrong.
func TestAHedgeCopyThatPanickedIsInternalOnEverySpanAndEvent(t *testing.T) {
	provider, exporter := traced(t)
	recorder := recordingFinishes(t)
	var returned error
	spans := underCaller(provider, exporter, func(ctx context.Context) {
		useProvider(t, mw.Hedge(5*time.Millisecond)(panickingProvider{}))
		_, returned = ops.ExtractResult[invoice]("Invoice from Acme", extractOptions(ctx))
	})
	if callerHolds(returned) != "internal" {
		t.Fatalf("the caller holds %q (%v), want internal", callerHolds(returned), returned)
	}
	operation, request := operationAndRequest(t, spans)
	for _, span := range []tracetest.SpanStub{operation, request} {
		if got, _ := attributeOf(span, "error.type"); got.AsString() != "internal" {
			t.Errorf("%s: error.type %q, want \"internal\"", span.Name, got.AsString())
		}
	}
	for _, scope := range []telemetry.Scope{telemetry.ScopeOperation, telemetry.ScopeRequest} {
		for _, result := range recorder.ofScope(scope) {
			if result.ErrorKind != "internal" {
				t.Errorf("the %s's finish carries %q, want \"internal\"", scope, result.ErrorKind)
			}
		}
	}
}

type panickingProvider struct{}

func (panickingProvider) Name() string                               { return "local" }
func (panickingProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (panickingProvider) RetryPolicy() (int, time.Duration)          { return -1, time.Millisecond }
func (panickingProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	panic("the copy blew up")
}
