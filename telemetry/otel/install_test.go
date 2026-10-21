package otel

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	wrapped "github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// These tests install the Observer and drive it through the emission functions
// of the package the library's call path calls, as a call of an operation does:
// an operation unit around request units, with notes on the requests. A call
// made through the root's client replaces them when the client exists.

// installed registers an Observer on a recording provider for one test and
// returns the provider, for the host's own span, and the exporter.
func installed(t *testing.T) (*sdktrace.TracerProvider, *tracetest.InMemoryExporter) {
	t.Helper()
	noObserver(t)
	provider, exporter := recording(t)
	restore, err := Install(provider)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	t.Cleanup(restore)
	return provider, exporter
}

// attributeOf returns the value of the attribute with the key.
func attributeOf(span tracetest.SpanStub, key string) (attribute.Value, bool) {
	for _, kv := range span.Attributes {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func TestARequestSpanSitsUnderAnOperationSpanUnderTheHostsSpan(t *testing.T) {
	provider, exporter := installed(t)

	hostCtx, host := provider.Tracer("host").Start(context.Background(), "handle ticket")
	operation := wrapped.ObserveOperationStart(hostCtx, wrapped.OperationEvent{Scope: wrapped.ScopeOperation, Operation: "extract"})
	first := wrapped.ObserveOperationStart(operation, wrapped.OperationEvent{
		Operation: "extract", Provider: "openai", Model: "gpt-5", RequestID: "req_1", SchemaName: "ticket", ResponseFormat: "json",
		Mode: "strict", Intelligence: "fast",
	})
	wrapped.ObserveOperationNote(first, wrapped.Note{Name: wrapped.NoteRetry, Attempt: 1, ErrorKind: "rate limited", Wait: 50 * time.Millisecond})
	wrapped.ObserveOperationEnd(first, wrapped.OperationResult{Attempts: 2, ProviderDuration: 40 * time.Millisecond, Provider: "openai", Model: "gpt-5-2026",
		FinishReason: "stop"})
	repair := wrapped.ObserveOperationStart(operation, wrapped.OperationEvent{Operation: "extract", Provider: "openai", Model: "gpt-5", Repair: true})
	wrapped.ObserveOperationEnd(repair, wrapped.OperationResult{Attempts: 1})
	wrapped.ObserveOperationEnd(operation, wrapped.OperationResult{Scope: wrapped.ScopeOperation, Operation: "extract"})
	host.End()

	spans := exporter.GetSpans()
	if len(spans) != 4 {
		t.Fatalf("%d spans exported, want the host's, the operation's and two requests", len(spans))
	}
	hostSpan := spanNamed(t, spans, "handle ticket")
	operationSpan := spanNamed(t, spans, "schemaflux.extract")
	if operationSpan.SpanKind != oteltrace.SpanKindInternal || operationSpan.Parent.SpanID() != hostSpan.SpanContext.SpanID() {
		t.Errorf("the operation span is kind %v under %s, want Internal under the host's span", operationSpan.SpanKind, operationSpan.Parent.SpanID())
	}
	var requests tracetest.SpanStubs
	for _, span := range spans {
		if span.Name == "chat gpt-5" {
			requests = append(requests, span)
		}
	}
	if len(requests) != 2 {
		t.Fatalf("%d request spans, want 2", len(requests))
	}
	slices.SortFunc(requests, func(a, b tracetest.SpanStub) int { return a.StartTime.Compare(b.StartTime) })
	for _, request := range requests {
		if request.SpanKind != oteltrace.SpanKindClient {
			t.Errorf("a request span has kind %v, want Client", request.SpanKind)
		}
		if request.Parent.SpanID() != operationSpan.SpanContext.SpanID() || request.SpanContext.TraceID() != hostSpan.SpanContext.TraceID() {
			t.Error("a request span is not a child of the operation span in the host's trace")
		}
	}
	if repairAttr, ok := attributeOf(requests[1], "schemaflux.repair"); !ok || !repairAttr.AsBool() {
		t.Error("the second request is not marked as a repair")
	}
	if _, ok := attributeOf(requests[0], "schemaflux.repair"); ok {
		t.Error("the first request is marked as a repair")
	}
	if len(requests[0].Events) != 1 || requests[0].Events[0].Name != "retry" {
		t.Errorf("the first request has events %v, want its one retry", requests[0].Events)
	}
	for key, want := range map[string]string{"schemaflux.mode": "strict", "schemaflux.tier": "fast", "schemaflux.schema.name": "ticket", "gen_ai.output.type": "json"} {
		if got, ok := attributeOf(requests[0], key); !ok || got.AsString() != want {
			t.Errorf("%s = %v (set: %v), want %q", key, got.Emit(), ok, want)
		}
	}
	if got, _ := attributeOf(requests[0], "schemaflux.attempts"); got.AsInt64() != 2 {
		t.Errorf("schemaflux.attempts = %d, want 2", got.AsInt64())
	}
	for _, span := range []tracetest.SpanStub{operationSpan, requests[0], requests[1]} {
		if span.Status.Code != codes.Unset {
			t.Errorf("span %q has status %v on success", span.Name, span.Status.Code)
		}
	}
}

// A request whose answer is later refused finished without an error, and the
// operation did not: the failure is on the operation's span, and each request
// says only whether it got an answer.
func TestAnOperationThatFailedAfterItsRequestsAnsweredIsTheOneWithStatusError(t *testing.T) {
	_, exporter := installed(t)

	operation := wrapped.ObserveOperationStart(context.Background(), wrapped.OperationEvent{Scope: wrapped.ScopeOperation, Operation: "extract"})
	for repair := range 2 {
		request := wrapped.ObserveOperationStart(operation, wrapped.OperationEvent{Operation: "extract", Provider: "openai", Model: "m", Repair: repair == 1})
		wrapped.ObserveOperationEnd(request, wrapped.OperationResult{Attempts: 1, Provider: "openai", Model: "m", Usage: types.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}})
	}
	wrapped.ObserveOperationEnd(operation, wrapped.OperationResult{Scope: wrapped.ScopeOperation, Operation: "extract", ErrorKind: "repair exhausted"})

	for _, span := range exporter.GetSpans() {
		switch span.SpanKind {
		case oteltrace.SpanKindInternal:
			if span.Status.Code != codes.Error || span.Status.Description != "invalid answer" {
				t.Errorf("operation span status = %v %q, want Error \"invalid answer\"", span.Status.Code, span.Status.Description)
			}
		default:
			if span.Status.Code != codes.Unset {
				t.Errorf("a request that was answered has status %v", span.Status.Code)
			}
		}
	}
}

// The names of a failure in the library's events become the names of the kinds
// of the Error a caller holds.
func TestTheErrorTypeIsTheNameOfTheKindOfTheError(t *testing.T) {
	cases := []struct{ event, want string }{
		{"rate limited", "rate limited"},
		{"provider unavailable", "unavailable"},
		{"circuit open", "unavailable"},
		{"authentication", "unauthorized"},
		{"permission", "unauthorized"},
		{"timeout", "timeout"},
		{"canceled", "canceled"},
		{"schema violation", "invalid answer"},
		{"malformed output", "invalid answer"},
		{"repair exhausted", "invalid answer"},
		{"output truncated", "truncated"},
		{"context too large", "too large"},
		{"budget exceeded", "budget exceeded"},
		{"admission rejected", "busy"},
		{"shutdown", "closed"},
		{"configuration", "configuration"},
		{"invalid request", "invalid request"},
		{"policy violation", "policy"},
		{"unsupported capability", "unsupported"},
		{"review required", "review required"},
		{wrapped.ErrorKindInternal, "internal"},
		{"unknown", "internal"},
	}
	for _, tc := range cases {
		t.Run(tc.event, func(t *testing.T) {
			_, exporter := installed(t)
			ctx := wrapped.ObserveOperationStart(context.Background(), wrapped.OperationEvent{Operation: "extract", Provider: "p", Model: "m"})
			wrapped.ObserveOperationEnd(ctx, wrapped.OperationResult{Attempts: 1, ErrorKind: tc.event})

			span := exporter.GetSpans()[0]
			got, _ := attributeOf(span, "error.type")
			if got.AsString() != tc.want || span.Status.Description != tc.want || span.Status.Code != codes.Error {
				t.Errorf("error.type %q, status %v %q, want Error %q", got.AsString(), span.Status.Code, span.Status.Description, tc.want)
			}
		})
	}
}

func TestInstallThenRestoreThenNothing(t *testing.T) {
	noObserver(t)
	provider, exporter := recording(t)
	restore, err := Install(provider)
	if err != nil {
		t.Fatal(err)
	}

	// A unit that began while the Observer was registered is still finished to
	// it after the restore, so its span is ended and exported.
	open := wrapped.ObserveOperationStart(context.Background(), wrapped.OperationEvent{Scope: wrapped.ScopeOperation, Operation: "extract"})
	wrapped.ObserveOperationEnd(wrapped.ObserveOperationStart(open, wrapped.OperationEvent{Operation: "extract", Model: "m"}), wrapped.OperationResult{Attempts: 1})
	restore()
	wrapped.ObserveOperationEnd(open, wrapped.OperationResult{Scope: wrapped.ScopeOperation, Operation: "extract"})

	if got := len(exporter.GetSpans()); got != 2 {
		t.Fatalf("%d spans exported for a call that began under the install, want its two", got)
	}
	exporter.Reset()

	// A unit that begins after the restore produces nothing.
	if wrapped.HasObserver() {
		t.Fatal("the restore left an observer registered")
	}
	after := wrapped.ObserveOperationStart(context.Background(), wrapped.OperationEvent{Scope: wrapped.ScopeOperation, Operation: "extract"})
	wrapped.ObserveOperationEnd(wrapped.ObserveOperationStart(after, wrapped.OperationEvent{Operation: "extract", Model: "m"}), wrapped.OperationResult{Attempts: 1})
	wrapped.ObserveOperationEnd(after, wrapped.OperationResult{Scope: wrapped.ScopeOperation, Operation: "extract"})
	if got := len(exporter.GetSpans()); got != 0 {
		t.Errorf("%d spans exported after the restore, want none", got)
	}

	// The id readers went with it.
	hostCtx, host := provider.Tracer("host").Start(context.Background(), "host")
	defer host.End()
	if telemetry.TraceID(hostCtx) != "" || telemetry.SpanID(hostCtx) != "" {
		t.Error("the restore left the id readers installed")
	}
}

func TestIDsOfTheSpanTheCallRanInAreWhatTheLibraryReports(t *testing.T) {
	_, exporter := installed(t)
	var seenTrace, seenSpan string
	probe := &idProbe{trace: &seenTrace, span: &seenSpan}
	defer telemetry.RegisterObserver(probe)()

	ctx := wrapped.ObserveOperationStart(context.Background(), wrapped.OperationEvent{Scope: wrapped.ScopeOperation, Operation: "extract"})
	wrapped.ObserveOperationEnd(ctx, wrapped.OperationResult{Scope: wrapped.ScopeOperation, Operation: "extract"})

	span := exporter.GetSpans()[0]
	if seenTrace != span.SpanContext.TraceID().String() || seenSpan != span.SpanContext.SpanID().String() {
		t.Errorf("the observer after this one read trace %q and span %q, want the operation span's %s and %s",
			seenTrace, seenSpan, span.SpanContext.TraceID(), span.SpanContext.SpanID())
	}
}

// idProbe is an Observer that reads the ids of the span on the context it is
// given in Started.
type idProbe struct{ trace, span *string }

func (p *idProbe) Started(ctx context.Context, _ telemetry.Event) context.Context {
	*p.trace, *p.span = telemetry.TraceID(ctx), telemetry.SpanID(ctx)
	return ctx
}
func (*idProbe) Finished(context.Context, telemetry.Result) {}

// Two installs on two providers, both registered. Each provider's spans form
// their own tree under the caller's span: neither takes the other's span, on a
// provider its backend never hears from, as a parent.
func TestTwoInstallsEachKeepTheirOwnTree(t *testing.T) {
	noObserver(t)
	firstProvider, firstExporter := recording(t)
	secondProvider, secondExporter := recording(t)
	restoreFirst, err := Install(firstProvider)
	if err != nil {
		t.Fatal(err)
	}
	defer restoreFirst()
	restoreSecond, err := Install(secondProvider)
	if err != nil {
		t.Fatal(err)
	}
	defer restoreSecond()

	hostCtx, host := firstProvider.Tracer("host").Start(context.Background(), "host")
	operation := wrapped.ObserveOperationStart(hostCtx, wrapped.OperationEvent{Scope: wrapped.ScopeOperation, Operation: "extract"})
	request := wrapped.ObserveOperationStart(operation, wrapped.OperationEvent{Operation: "extract", Model: "m"})
	wrapped.ObserveOperationNote(request, wrapped.Note{Name: wrapped.NoteRetry, Attempt: 1})
	wrapped.ObserveOperationEnd(request, wrapped.OperationResult{Attempts: 2})
	wrapped.ObserveOperationEnd(operation, wrapped.OperationResult{Scope: wrapped.ScopeOperation})
	host.End()

	for name, exporter := range map[string]*tracetest.InMemoryExporter{"first": firstExporter, "second": secondExporter} {
		spans := exporter.GetSpans()
		operationSpan, requestSpan := spanNamed(t, spans, "schemaflux.extract"), spanNamed(t, spans, "chat m")
		if operationSpan.Parent.SpanID() != host.SpanContext().SpanID() {
			t.Errorf("%s provider: the operation span's parent is %s, want the host's span", name, operationSpan.Parent.SpanID())
		}
		if requestSpan.Parent.SpanID() != operationSpan.SpanContext.SpanID() {
			t.Errorf("%s provider: the request span's parent is %s, want this provider's operation span %s",
				name, requestSpan.Parent.SpanID(), operationSpan.SpanContext.SpanID())
		}
		if len(requestSpan.Events) != 1 {
			t.Errorf("%s provider: the request span has %d events, want the retry", name, len(requestSpan.Events))
		}
	}
}

// An observer that returns a context that is not derived from the one it was
// given is passed over by the library, and the trace of the observers around it
// stays whole.
func TestAnObserverThatDropsTheContextLeavesTheTraceWhole(t *testing.T) {
	noObserver(t)
	provider, exporter := recording(t)
	defer telemetry.RegisterObserver(dropping{})()
	restore, err := Install(provider)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	defer telemetry.RegisterObserver(dropping{})()

	hostCtx, host := provider.Tracer("host").Start(context.Background(), "host")
	operation := wrapped.ObserveOperationStart(hostCtx, wrapped.OperationEvent{Scope: wrapped.ScopeOperation, Operation: "extract"})
	request := wrapped.ObserveOperationStart(operation, wrapped.OperationEvent{Operation: "extract", Model: "m"})
	if request.Done() != hostCtx.Done() {
		t.Error("the call's context lost the host's cancellation")
	}
	wrapped.ObserveOperationEnd(request, wrapped.OperationResult{Attempts: 1})
	wrapped.ObserveOperationEnd(operation, wrapped.OperationResult{Scope: wrapped.ScopeOperation})
	host.End()

	spans := exporter.GetSpans()
	hostSpan, operationSpan, requestSpan := spanNamed(t, spans, "host"), spanNamed(t, spans, "schemaflux.extract"), spanNamed(t, spans, "chat m")
	if operationSpan.Parent.SpanID() != hostSpan.SpanContext.SpanID() || requestSpan.Parent.SpanID() != operationSpan.SpanContext.SpanID() {
		t.Error("an observer that dropped the context broke the parent chain of the library's spans")
	}
}

// dropping is an Observer that returns context.Background().
type dropping struct{}

func (dropping) Started(context.Context, telemetry.Event) context.Context {
	return context.Background()
}
func (dropping) Finished(context.Context, telemetry.Result) {}

// A span started while one observer is registered is ended, whatever is
// registered by the time the call returns. Fifty goroutines make calls while the
// observer is registered and removed forty times; a call that began under an
// install and finished after its removal must not leave its span open for ever.
func TestEverySpanStartedIsEndedWhileObserversComeAndGo(t *testing.T) {
	noObserver(t)
	tally := &tallyingProvider{TracerProvider: sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSyncer(discarding{}))}

	var callers sync.WaitGroup
	stop := make(chan struct{})
	for worker := range 50 {
		callers.Go(func() {
			for sequence := 0; ; sequence++ {
				select {
				case <-stop:
					return
				default:
				}
				id := "w" + strconv.Itoa(worker) + "-" + strconv.Itoa(sequence)
				operation := wrapped.ObserveOperationStart(context.Background(), wrapped.OperationEvent{Scope: wrapped.ScopeOperation, Operation: "extract"})
				request := wrapped.ObserveOperationStart(operation, wrapped.OperationEvent{Operation: "extract", Model: "m", RequestID: id})
				wrapped.ObserveOperationNote(request, wrapped.Note{Name: wrapped.NoteRetry, Attempt: 1})
				time.Sleep(50 * time.Microsecond)
				wrapped.ObserveOperationEnd(request, wrapped.OperationResult{Attempts: 2})
				wrapped.ObserveOperationEnd(operation, wrapped.OperationResult{Scope: wrapped.ScopeOperation, Operation: "extract"})
			}
		})
	}
	for range 40 {
		restore, err := Install(tally)
		if err != nil {
			t.Fatalf("Install: %v", err)
		}
		time.Sleep(3 * time.Millisecond)
		restore()
		time.Sleep(time.Millisecond)
	}
	close(stop)
	callers.Wait()

	started, ended := tally.started.Load(), tally.ended.Load()
	if started == 0 {
		t.Fatal("no span was started, so the test observed nothing")
	}
	if started != ended {
		t.Errorf("%d spans were started and %d were ended: %d were never ended", started, ended, started-ended)
	}
	if twice := tally.endedTwice.Load(); twice != 0 {
		t.Errorf("%d spans were ended more than once", twice)
	}
}

// discarding is a span exporter that accepts every span and keeps none.
type discarding struct{}

func (discarding) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (discarding) Shutdown(context.Context) error                             { return nil }

// A span a sampler dropped, through the library's own emission, has nothing
// written to it and is ended once, for every unit of a call.
func TestDroppedSpansOfACallAreWrittenNothingAndEnded(t *testing.T) {
	noObserver(t)
	tally := &tallyingProvider{TracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))}
	restore, err := Install(tally)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()

	for range 20 {
		operation := wrapped.ObserveOperationStart(context.Background(), wrapped.OperationEvent{Scope: wrapped.ScopeOperation, Operation: "extract"})
		request := wrapped.ObserveOperationStart(operation, wrapped.OperationEvent{Operation: "extract", Model: "m", Provider: "p", RequestID: "r"})
		wrapped.ObserveOperationNote(request, wrapped.Note{Name: wrapped.NoteHedge, Attempt: 2})
		wrapped.ObserveOperationEnd(request, wrapped.OperationResult{Attempts: 2, Provider: "p", Model: "m", ErrorKind: "timeout"})
		wrapped.ObserveOperationEnd(operation, wrapped.OperationResult{Scope: wrapped.ScopeOperation, ErrorKind: "timeout"})
	}
	if started, ended := tally.started.Load(), tally.ended.Load(); started != 40 || ended != 40 {
		t.Errorf("%d spans started and %d ended, want 40 and 40", started, ended)
	}
	if writes := tally.writesWhenNotRecording.Load(); writes != 0 {
		t.Errorf("%d writes reached spans that were not recording", writes)
	}
}
