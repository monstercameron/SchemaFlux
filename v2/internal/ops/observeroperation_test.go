package ops

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// OB-006. telemetry.Observer was defined, tested, and adapted to
// OpenTelemetry, and nothing in the library ever called it: a repository-wide
// search for ObserveOperationStart outside telemetry/ found no production call
// site at all. A host that installed an observer -- or the otel adapter --
// received exactly nothing, from built-in and custom providers alike.
//
// These cover the emission site now that CallLLM has one. CallLLM is the
// single funnel every provider passes through (the package default, a
// client's own instance, a context-attached one, and anything a caller
// registered with RegisterProvider), so observing here is what makes a
// caller's own transport visible rather than only the providers this
// repository ships.

// collectingObserver records what it was handed.
type collectingObserver struct {
	mu       sync.Mutex
	started  []telemetry.OperationEvent
	finished []telemetry.OperationResult
}

func (o *collectingObserver) OperationStarted(ctx context.Context, event telemetry.OperationEvent) context.Context {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.started = append(o.started, event)
	return ctx
}

func (o *collectingObserver) OperationFinished(_ context.Context, result telemetry.OperationResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finished = append(o.finished, result)
}

func (o *collectingObserver) snapshot() ([]telemetry.OperationEvent, []telemetry.OperationResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]telemetry.OperationEvent(nil), o.started...),
		append([]telemetry.OperationResult(nil), o.finished...)
}

func installCollector(t *testing.T) *collectingObserver {
	t.Helper()
	collector := &collectingObserver{}
	t.Cleanup(telemetry.AddObserver(collector))
	return collector
}

// A provider a caller wrote themselves is the case that matters: it is the
// one this library does not ship, and the one a host is most likely to want
// in its traces. It reaches the model through the same CallLLM funnel, so it
// has to produce the same events.
type callerSuppliedProvider struct {
	name         string
	finishReason string
	err          error
	calls        int
	delay        time.Duration
}

func (p *callerSuppliedProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls++
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return llm.CompletionResponse{}, ctx.Err()
		}
	}
	if p.err != nil {
		return llm.CompletionResponse{}, p.err
	}
	return llm.CompletionResponse{
		Content:      "{\"ok\":true}",
		Model:        req.Model,
		Provider:     p.name,
		FinishReason: p.finishReason,
		Usage:        types.TokenUsage{PromptTokens: 11, CompletionTokens: 3, TotalTokens: 14},
	}, nil
}

func (p *callerSuppliedProvider) Name() string { return p.name }

func (p *callerSuppliedProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }

func (p *callerSuppliedProvider) RetryPolicy() (int, time.Duration) { return 0, time.Millisecond }

func TestACallerSuppliedProviderFiresTheSameObserverEventsAsABuiltIn(t *testing.T) {
	collector := installCollector(t)

	provider := &callerSuppliedProvider{name: "acme-inhouse", finishReason: "stop"}
	opts := types.OpOptions{
		Mode:           types.Strict,
		Intelligence:   types.Fast,
		Model:          "acme-1",
		SchemaName:     "InvoiceHeader",
		ResponseFormat: "json",
	}

	if _, err := CallLLM(context.Background(), provider, "Return JSON.", "input", opts); err != nil {
		t.Fatalf("CallLLM: %v", err)
	}

	started, finished := collector.snapshot()
	if len(started) != 1 || len(finished) != 1 {
		t.Fatalf("a custom provider must be as visible as a built-in one: got %d start and %d finish events, want 1 and 1",
			len(started), len(finished))
	}

	if started[0].Provider != "acme-inhouse" {
		t.Errorf("start event provider = %q, want the caller's own transport name", started[0].Provider)
	}
	if started[0].Model != "acme-1" {
		t.Errorf("start event model = %q, want acme-1", started[0].Model)
	}
	if started[0].RequestID == "" {
		t.Error("start event carries no RequestID; a host cannot correlate the span with the library's own log lines")
	}
	if started[0].SchemaName != "InvoiceHeader" {
		t.Errorf("start event schema name = %q, want InvoiceHeader -- the shape of a structured request, which is what a host groups by",
			started[0].SchemaName)
	}
	if started[0].ResponseFormat != "json" {
		t.Errorf("start event response format = %q, want json", started[0].ResponseFormat)
	}

	if finished[0].Attempts != 1 {
		t.Errorf("finish attempts = %d, want 1", finished[0].Attempts)
	}
	if finished[0].FinishReason != "stop" {
		t.Errorf("finish reason = %q, want stop -- a truncated answer and a complete one arrive with the same status code and the finish reason is the only thing that tells them apart",
			finished[0].FinishReason)
	}
	if finished[0].Provider != "acme-inhouse" {
		t.Errorf("finish provider = %q, want the provider that actually answered", finished[0].Provider)
	}
	if finished[0].Usage.TotalTokens != 14 {
		t.Errorf("finish usage total = %d, want 14", finished[0].Usage.TotalTokens)
	}
	if finished[0].ErrorKind != "" {
		t.Errorf("finish error kind = %q on a successful call, want empty", finished[0].ErrorKind)
	}
}

// One logical request, one pair of events, whatever the retry count -- the
// attempt breakdown is a field, not a second span. A host that saw three
// starts for one operation would double-count every retried request in its
// own latency figures.
func TestRetriesProduceOnePairOfEventsAndAnAttemptCount(t *testing.T) {
	collector := installCollector(t)

	provider := &captureProvider{
		errors: []error{
			&types.OperationError{Kind: types.KindProviderUnavailable, Message: "upstream unavailable"},
			&types.OperationError{Kind: types.KindProviderUnavailable, Message: "upstream unavailable"},
			nil,
		},
	}

	opts := types.OpOptions{Mode: types.Strict, Intelligence: types.Fast, Model: "local-1"}
	if _, err := CallLLM(context.Background(), provider, "Return JSON.", "input", opts); err != nil {
		t.Fatalf("CallLLM: %v", err)
	}

	started, finished := collector.snapshot()
	if len(started) != 1 || len(finished) != 1 {
		t.Fatalf("got %d start and %d finish events for one logical request, want 1 and 1", len(started), len(finished))
	}
	if finished[0].Attempts != 3 {
		t.Errorf("attempts = %d, want 3 -- the retries are the breakdown of the one operation", finished[0].Attempts)
	}
	if finished[0].ProviderDuration > finished[0].Duration {
		t.Errorf("provider duration %v exceeds total duration %v; the provider time is contained by the wall clock",
			finished[0].ProviderDuration, finished[0].Duration)
	}
}

// The provider-reported request time has to be separable from the wall clock,
// because the difference is this library's own retry backoff and a host
// diagnosing latency needs to know which of the two it is looking at.
func TestTheProviderTimeIsReportedApartFromTheBackoffTime(t *testing.T) {
	collector := installCollector(t)

	provider := &callerSuppliedProvider{name: "acme-inhouse", finishReason: "stop", delay: 5 * time.Millisecond}

	opts := types.OpOptions{Mode: types.Strict, Intelligence: types.Fast, Model: "acme-1"}
	if _, err := CallLLM(context.Background(), provider, "Return JSON.", "input", opts); err != nil {
		t.Fatalf("CallLLM: %v", err)
	}

	_, finished := collector.snapshot()
	if len(finished) != 1 {
		t.Fatalf("got %d finish events, want 1", len(finished))
	}
	if finished[0].ProviderDuration <= 0 {
		t.Error("provider duration is zero; nothing measured the time actually spent inside the provider")
	}
	if finished[0].Duration < finished[0].ProviderDuration {
		t.Errorf("total %v is shorter than the provider time %v it contains",
			finished[0].Duration, finished[0].ProviderDuration)
	}
}

// A cancelled request and a provider that returned an error are two different
// facts and used to be indistinguishable to anything downstream. The
// taxonomy already separates them (internal/llm/classify.go); the observer
// has to report the taxonomy's answer rather than a flattened "error".
func TestCancellationAndTimeoutAreReportedApartFromAProviderError(t *testing.T) {
	cases := []struct {
		name     string
		provider *callerSuppliedProvider
		context  func() (context.Context, context.CancelFunc)
		wantKind string
	}{
		{
			name: "a provider failure",
			provider: &callerSuppliedProvider{
				name: "acme-inhouse",
				err:  &types.OperationError{Kind: types.KindProviderUnavailable, Message: "upstream unavailable"},
			},
			context:  func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			wantKind: types.KindProviderUnavailable.String(),
		},
		{
			name:     "the caller cancelled",
			provider: &callerSuppliedProvider{name: "acme-inhouse", err: context.Canceled},
			context:  func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			wantKind: types.KindCanceled.String(),
		},
		{
			name:     "the deadline expired",
			provider: &callerSuppliedProvider{name: "acme-inhouse", delay: 100 * time.Millisecond},
			context: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), time.Millisecond)
			},
			wantKind: types.KindTimeout.String(),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			collector := installCollector(t)

			ctx, cancel := testCase.context()
			defer cancel()

			opts := types.OpOptions{Mode: types.Strict, Intelligence: types.Fast, Model: "acme-1"}
			if _, err := CallLLM(ctx, testCase.provider, "Return JSON.", "input", opts); err == nil {
				t.Fatal("CallLLM returned no error; this case is about the failure path")
			}

			_, finished := collector.snapshot()
			if len(finished) != 1 {
				t.Fatalf("got %d finish events, want 1", len(finished))
			}
			if finished[0].ErrorKind != testCase.wantKind {
				t.Errorf("error kind = %q, want %q", finished[0].ErrorKind, testCase.wantKind)
			}
		})
	}
}

// The kind, never the message. A provider's error text quotes the request
// back often enough that this is the whole reason OperationResult carries a
// kind at all, and the emission site is where that promise is kept or broken.
func TestTheEmittedEventsCarryNoProviderMessageOrCallerInput(t *testing.T) {
	collector := installCollector(t)

	const marker = "MARKER-4815162342-DO-NOT-EXPORT"
	provider := &callerSuppliedProvider{
		name: "acme-inhouse",
		err:  errors.New("provider rejected the request: " + marker),
	}

	opts := types.OpOptions{Mode: types.Strict, Intelligence: types.Fast, Model: "acme-1"}
	if _, err := CallLLM(context.Background(), provider, "Return JSON.", marker, opts); err == nil {
		t.Fatal("CallLLM returned no error; this case is about the failure path")
	}

	started, finished := collector.snapshot()
	if len(started) != 1 || len(finished) != 1 {
		t.Fatalf("got %d start and %d finish events, want 1 and 1", len(started), len(finished))
	}
	for _, field := range eventStrings(started[0]) {
		if strings.Contains(field, marker) {
			t.Errorf("a start event field carries the caller's input: %q", field)
		}
	}
	for _, field := range resultStrings(finished[0]) {
		if strings.Contains(field, marker) {
			t.Errorf("a finish event field carries the caller's input or a provider message: %q", field)
		}
	}
}

func eventStrings(event telemetry.OperationEvent) []string {
	return []string{
		event.Operation, event.Provider, event.Model, event.RequestID,
		event.CorrelationID, event.Mode, event.Intelligence,
		event.SchemaName, event.ResponseFormat,
	}
}

func resultStrings(result telemetry.OperationResult) []string {
	return []string{
		result.Operation, result.ErrorKind, result.FinishReason,
		result.Provider, result.Model, result.Cost.Currency, result.Cost.PricingSource,
	}
}

// OperationResult.Provider and .Model mean "what answered". When nothing
// answered they have to be empty rather than echoing what was asked for --
// the request's provider and model are on the start event, where they are
// true. Reporting a provider that never responded as the one that did is the
// same failure as an operation returning a plausible zero value.
func TestAFailureNamesNoAnsweringProviderOrModel(t *testing.T) {
	collector := installCollector(t)

	provider := &callerSuppliedProvider{
		name: "acme-inhouse",
		err:  &types.OperationError{Kind: types.KindProviderUnavailable, Message: "upstream unavailable"},
	}

	opts := types.OpOptions{Mode: types.Strict, Intelligence: types.Fast, Model: "acme-1"}
	if _, err := CallLLM(context.Background(), provider, "Return JSON.", "input", opts); err == nil {
		t.Fatal("CallLLM returned no error; this case is about the failure path")
	}

	started, finished := collector.snapshot()
	if len(started) != 1 || len(finished) != 1 {
		t.Fatalf("got %d start and %d finish events, want 1 and 1", len(started), len(finished))
	}
	if started[0].Provider != "acme-inhouse" || started[0].Model != "acme-1" {
		t.Errorf("the start event should still say what was asked for; got provider %q model %q",
			started[0].Provider, started[0].Model)
	}
	if finished[0].Provider != "" {
		t.Errorf("finish provider = %q, want empty -- nothing answered", finished[0].Provider)
	}
	if finished[0].Model != "" {
		t.Errorf("finish model = %q, want empty -- nothing answered", finished[0].Model)
	}
}

// A truncated response is a real answer this library then refuses. The error
// kind says it was truncated; the finish reason is the provider's own word for
// why, and both are worth having, so the finish reason is recorded when the
// provider responds rather than only when the response survives validation.
func TestATruncatedAnswerReportsBothTheKindAndTheFinishReason(t *testing.T) {
	collector := installCollector(t)

	provider := &captureProvider{
		resp: llm.CompletionResponse{
			Content:      "{\"partial\":",
			Model:        "local-1",
			Provider:     "local",
			FinishReason: "length",
		},
	}

	opts := types.OpOptions{Mode: types.Strict, Intelligence: types.Fast, Model: "local-1"}
	if _, err := CallLLM(context.Background(), provider, "Return JSON.", "input", opts); err == nil {
		t.Fatal("a truncated response was accepted")
	}

	_, finished := collector.snapshot()
	if len(finished) != 1 {
		t.Fatalf("got %d finish events, want 1", len(finished))
	}
	if finished[0].ErrorKind != types.KindOutputTruncated.String() {
		t.Errorf("error kind = %q, want %q", finished[0].ErrorKind, types.KindOutputTruncated.String())
	}
	if finished[0].FinishReason != "length" {
		t.Errorf("finish reason = %q, want length -- the provider's own word for why it stopped",
			finished[0].FinishReason)
	}
}

// The operation name is what a span is called. It reaches CallLLM either from
// the context an operation attached it to, or from the operation segment of
// the cache identity an operation already builds.
func TestTheOperationNameReachesTheEventFromTheContext(t *testing.T) {
	collector := installCollector(t)

	provider := &callerSuppliedProvider{name: "acme-inhouse", finishReason: "stop"}
	ctx := WithOperationName(context.Background(), "classify")

	opts := types.OpOptions{Mode: types.Strict, Intelligence: types.Fast, Model: "acme-1"}
	if _, err := CallLLM(ctx, provider, "Return JSON.", "input", opts); err != nil {
		t.Fatalf("CallLLM: %v", err)
	}

	started, finished := collector.snapshot()
	if len(started) != 1 || len(finished) != 1 {
		t.Fatalf("got %d start and %d finish events, want 1 and 1", len(started), len(finished))
	}
	if started[0].Operation != "classify" {
		t.Errorf("start event operation = %q, want classify", started[0].Operation)
	}
	if finished[0].Operation != "classify" {
		t.Errorf("finish event operation = %q, want classify -- both ends name the same operation", finished[0].Operation)
	}
}

func TestTheOperationNameFallsBackToTheCacheIdentitySegment(t *testing.T) {
	collector := installCollector(t)

	provider := &callerSuppliedProvider{name: "acme-inhouse", finishReason: "stop"}
	opts := types.OpOptions{
		Mode:         types.Strict,
		Intelligence: types.Fast,
		Model:        "acme-1",
		CacheIdentity: SchemaCacheKey("extract", "v3", SchemaDescriptor{
			Name: "Invoice", Version: "1", Hash: "abc", Dialect: "json-schema",
		}),
	}

	if _, err := CallLLM(context.Background(), provider, "Return JSON.", "input", opts); err != nil {
		t.Fatalf("CallLLM: %v", err)
	}

	started, _ := collector.snapshot()
	if len(started) != 1 {
		t.Fatalf("got %d start events, want 1", len(started))
	}
	if started[0].Operation != "extract" {
		t.Errorf("start event operation = %q, want extract", started[0].Operation)
	}
}

// An observer that panics is the host's bug, not this library's failure: the
// answer is already correct by the time the finish event is emitted, and
// losing it to a broken log sink would be the worst possible trade.
func TestAnObserverThatPanicsDoesNotFailTheOperation(t *testing.T) {
	t.Cleanup(telemetry.AddObserver(panickingObserver{}))

	provider := &callerSuppliedProvider{name: "acme-inhouse", finishReason: "stop"}
	opts := types.OpOptions{Mode: types.Strict, Intelligence: types.Fast, Model: "acme-1"}

	content, err := CallLLM(context.Background(), provider, "Return JSON.", "input", opts)
	if err != nil {
		t.Fatalf("an observer's panic reached the caller as an operation failure: %v", err)
	}
	if content == "" {
		t.Error("the answer was lost")
	}
}

type panickingObserver struct{}

func (panickingObserver) OperationStarted(context.Context, telemetry.OperationEvent) context.Context {
	panic("a host's observer is broken")
}

func (panickingObserver) OperationFinished(context.Context, telemetry.OperationResult) {
	panic("a host's observer is broken")
}
