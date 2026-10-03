package otel

import (
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/telemetry"
)

// These tests drive the Observer with events directly and read what reaches
// the SDK's in-memory exporter. The tests that run real operations of the
// library are in operations_test.go.

// recording returns a provider that samples every span into an in-memory
// exporter.
func recording(t *testing.T) (*sdktrace.TracerProvider, *tracetest.InMemoryExporter) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return provider, exporter
}

// observerOn returns an Observer on a recording provider, installed nowhere.
func observerOn(t *testing.T) (*Observer, *tracetest.InMemoryExporter) {
	t.Helper()
	provider, exporter := recording(t)
	observer, err := New(provider)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return observer, exporter
}

// typed renders an attribute set as "key TYPE value" lines, sorted, so a test
// compares keys, types and values at once.
func typed(attributes []attribute.KeyValue) []string {
	lines := make([]string, 0, len(attributes))
	for _, kv := range attributes {
		lines = append(lines, fmt.Sprintf("%s %s %s", kv.Key, kv.Value.Type(), kv.Value.Emit()))
	}
	slices.Sort(lines)
	return lines
}

func spanNamed(t *testing.T, spans tracetest.SpanStubs, name string) tracetest.SpanStub {
	t.Helper()
	for _, span := range spans {
		if span.Name == name {
			return span
		}
	}
	t.Fatalf("no span named %q among %d exported", name, len(spans))
	return tracetest.SpanStub{}
}

func TestInstallingTouchesNeitherTheGlobalProviderNorTheGlobalPropagator(t *testing.T) {
	providerBefore := otel.GetTracerProvider()
	hostPropagator := propagation.NewCompositeTextMapPropagator(propagation.Baggage{})
	previousPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(hostPropagator)
	t.Cleanup(func() { otel.SetTextMapPropagator(previousPropagator) })

	provider, _ := recording(t)
	restore, err := Install(provider)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if otel.GetTracerProvider() != providerBefore {
		t.Error("installing replaced the host's global TracerProvider")
	}
	if got := otel.GetTextMapPropagator().Fields(); !slices.Equal(got, hostPropagator.Fields()) {
		t.Errorf("installing changed the host's global propagator: fields %v", got)
	}

	restore()
	if otel.GetTracerProvider() != providerBefore {
		t.Error("the restore replaced the host's global TracerProvider")
	}
	if got := otel.GetTextMapPropagator().Fields(); !slices.Equal(got, hostPropagator.Fields()) {
		t.Errorf("the restore changed the host's global propagator: fields %v", got)
	}
}

func TestANilProviderIsRefusedRatherThanFallingBackToTheGlobal(t *testing.T) {
	observer, err := New(nil)
	if err == nil || observer != nil {
		t.Fatalf("New(nil) = %v, %v; want a refusal", observer, err)
	}
	restore, err := Install(nil)
	if err == nil || restore != nil {
		t.Fatal("Install(nil) did not refuse")
	}
	if telemetry.HasObserver() {
		t.Error("a refused Install left an observer installed")
	}
}

// Every attribute a request span can carry, with its type. This is the
// checklist of the package comment: a key that changes name or type fails
// here.
func TestARequestSpanCarriesTheConventionsKeysWithTheirTypes(t *testing.T) {
	observer, exporter := observerOn(t)

	ctx := observer.OperationStarted(context.Background(), telemetry.OperationEvent{
		Operation:      "extract",
		Provider:       "openai",
		Model:          "gpt-5.6-luna",
		RequestID:      "req_1",
		CorrelationID:  "corr_1",
		Mode:           "strict",
		Intelligence:   "smart",
		SchemaName:     "invoice",
		ResponseFormat: "json",
		Repair:         true,
		Stream:         true,
	})
	observer.OperationFinished(ctx, telemetry.OperationResult{
		Operation:        "extract",
		Duration:         80 * time.Millisecond,
		ProviderDuration: 60 * time.Millisecond,
		Attempts:         2,
		Provider:         "openai",
		Model:            "gpt-5.6-luna-2026-08-01",
		FinishReason:     "stop",
		Usage: types.TokenUsage{
			PromptTokens: 100, CompletionTokens: 30, TotalTokens: 130,
			CachedTokens: 40, ReasoningTokens: 7, CacheWriteTokens: 12,
		},
		Cost: types.CostInfo{TotalCost: 0.0042, Currency: "USD", Priced: true, Quality: types.PricingExact},
	})

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("%d spans exported, want 1", len(spans))
	}
	span := spans[0]
	if span.Name != "chat gpt-5.6-luna" {
		t.Errorf("name = %q, want the convention's \"chat <model>\"", span.Name)
	}
	if span.SpanKind != oteltrace.SpanKindClient {
		t.Errorf("kind = %v, want Client", span.SpanKind)
	}
	if span.Status.Code != codes.Unset || span.Status.Description != "" {
		t.Errorf("status = %v %q, want Unset: Ok is the application's to set", span.Status.Code, span.Status.Description)
	}

	want := []string{
		"error.type absent",
		"gen_ai.operation.name STRING chat",
		"gen_ai.output.type STRING json",
		"gen_ai.provider.name STRING openai",
		"gen_ai.request.model STRING gpt-5.6-luna",
		"gen_ai.response.finish_reasons STRINGSLICE [\"stop\"]",
		"gen_ai.response.model STRING gpt-5.6-luna-2026-08-01",
		"gen_ai.usage.cache_creation.input_tokens INT64 12",
		"gen_ai.usage.cache_read.input_tokens INT64 40",
		"gen_ai.usage.input_tokens INT64 100",
		"gen_ai.usage.output_tokens INT64 30",
		"gen_ai.usage.reasoning.output_tokens INT64 7",
		"schemaflux.attempts INT64 2",
		"schemaflux.correlation_id STRING corr_1",
		"schemaflux.cost.currency STRING USD",
		"schemaflux.cost.priced BOOL true",
		"schemaflux.cost.total FLOAT64 0.0042",
		"schemaflux.intelligence STRING smart",
		"schemaflux.mode STRING strict",
		"schemaflux.operation STRING extract",
		"schemaflux.provider_duration_ms INT64 60",
		"schemaflux.repair BOOL true",
		"schemaflux.request_id STRING req_1",
		"schemaflux.response.provider STRING openai",
		"schemaflux.schema.name STRING invoice",
		"schemaflux.stream BOOL true",
	}
	got := typed(span.Attributes)
	got = append(got, "error.type absent")
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("attributes:\n got  %s\n want %s", strings.Join(got, "\n      "), strings.Join(want, "\n      "))
	}

	for _, kv := range span.Attributes {
		if key := string(kv.Key); !strings.HasPrefix(key, "gen_ai.") && !strings.HasPrefix(key, "schemaflux.") && key != "error.type" {
			t.Errorf("attribute %q is outside the convention's namespace and the library's own", key)
		}
	}
	if scope := span.InstrumentationScope; scope.Name != instrumentationName || scope.Version != types.Version || scope.SchemaURL == "" {
		t.Errorf("instrumentation scope = %+v, want the library's name, its version and the convention's schema URL", scope)
	}
}

// What an attribute is absent for: a fact nobody established. Each case names
// keys that must not appear and the ones that must.
func TestAttributesAreSetOnlyForWhatIsKnown(t *testing.T) {
	answered := telemetry.OperationResult{Attempts: 1, Provider: "openai", Model: "m", FinishReason: "stop",
		Usage: types.TokenUsage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}}

	cases := []struct {
		name    string
		event   telemetry.OperationEvent
		result  telemetry.OperationResult
		present []string
		absent  []string
	}{
		{
			name:    "an unnamed operation",
			event:   telemetry.OperationEvent{Provider: "openai", Model: "m"},
			result:  answered,
			present: []string{"gen_ai.operation.name", "gen_ai.provider.name", "gen_ai.request.model"},
			absent:  []string{"schemaflux.operation", "schemaflux.schema.name", "gen_ai.output.type", "schemaflux.repair", "schemaflux.stream", "schemaflux.request_id", "schemaflux.correlation_id", "schemaflux.mode", "schemaflux.intelligence"},
		},
		{
			name:    "a request that failed before any answer",
			event:   telemetry.OperationEvent{Operation: "extract", Provider: "openai", Model: "m"},
			result:  telemetry.OperationResult{Attempts: 2, ErrorKind: "rate limited"},
			present: []string{"schemaflux.attempts", "error.type"},
			absent:  []string{"gen_ai.response.model", "schemaflux.response.provider", "gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens", "schemaflux.cost.priced", "schemaflux.cost.total", "gen_ai.response.finish_reasons", "gen_ai.usage.cache_read.input_tokens"},
		},
		{
			name:  "an unpriced answer",
			event: telemetry.OperationEvent{Operation: "extract", Provider: "openai", Model: "m"},
			result: func() telemetry.OperationResult {
				result := answered
				result.Cost = types.CostInfo{Quality: types.PricingUnknown}
				return result
			}(),
			present: []string{"schemaflux.cost.priced", "gen_ai.usage.input_tokens"},
			absent:  []string{"schemaflux.cost.total", "schemaflux.cost.currency"},
		},
		{
			name:    "an answer whose cost was never worked out",
			event:   telemetry.OperationEvent{Operation: "extract", Provider: "openai", Model: "m"},
			result:  answered,
			present: []string{"gen_ai.response.model", "gen_ai.usage.output_tokens"},
			absent:  []string{"schemaflux.cost.priced", "schemaflux.cost.total", "schemaflux.cost.currency"},
		},
		{
			name:  "an answer that reported no usage",
			event: telemetry.OperationEvent{Operation: "extract", Provider: "openai", Model: "m"},
			result: telemetry.OperationResult{Attempts: 1, Provider: "openai", Model: "m", FinishReason: "stop",
				Cost: types.CostInfo{Quality: types.PricingUnknown}},
			present: []string{"gen_ai.response.model", "schemaflux.response.provider"},
			absent:  []string{"gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens", "gen_ai.usage.cache_read.input_tokens", "gen_ai.usage.reasoning.output_tokens", "gen_ai.usage.cache_creation.input_tokens"},
		},
		{
			name:  "usage reported only under the input and output names",
			event: telemetry.OperationEvent{Operation: "extract", Provider: "codex", Model: "m"},
			result: telemetry.OperationResult{Attempts: 1, Provider: "codex", Model: "m",
				Usage: types.TokenUsage{InputTokens: 9, OutputTokens: 4}},
			present: []string{"gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens"},
			absent:  []string{"gen_ai.usage.cache_read.input_tokens"},
		},
		{
			name:    "no cached, written or reasoning tokens",
			event:   telemetry.OperationEvent{Operation: "extract", Provider: "openai", Model: "m"},
			result:  answered,
			present: []string{"gen_ai.usage.input_tokens"},
			absent:  []string{"gen_ai.usage.cache_read.input_tokens", "gen_ai.usage.cache_creation.input_tokens", "gen_ai.usage.reasoning.output_tokens"},
		},
		{
			name:    "a truncated answer that was refused",
			event:   telemetry.OperationEvent{Operation: "extract", Provider: "openai", Model: "m"},
			result:  telemetry.OperationResult{Attempts: 1, Provider: "openai", Model: "m", FinishReason: "length", ErrorKind: "output truncated", Usage: types.TokenUsage{PromptTokens: 5, CompletionTokens: 3}},
			present: []string{"gen_ai.response.finish_reasons", "error.type", "gen_ai.usage.output_tokens", "gen_ai.response.model"},
			absent:  []string{"schemaflux.cost.priced", "schemaflux.cost.total"},
		},
		{
			name:    "a text request with no provider time measured",
			event:   telemetry.OperationEvent{Operation: "summarize", Provider: "openai", Model: "m", ResponseFormat: "text"},
			result:  answered,
			present: []string{"gen_ai.output.type"},
			absent:  []string{"schemaflux.provider_duration_ms", "schemaflux.schema.name"},
		},
		{
			name:    "a response format the convention has no value for",
			event:   telemetry.OperationEvent{Operation: "summarize", Provider: "openai", Model: "m", ResponseFormat: "markdown"},
			result:  answered,
			present: []string{"gen_ai.request.model"},
			absent:  []string{"gen_ai.output.type"},
		},
		{
			name:    "a priced answer with no currency named",
			event:   telemetry.OperationEvent{Operation: "extract", Provider: "openai", Model: "m"},
			result:  telemetry.OperationResult{Attempts: 1, Provider: "openai", Model: "m", Cost: types.CostInfo{Priced: true, TotalCost: 0.5}},
			present: []string{"schemaflux.cost.priced", "schemaflux.cost.total"},
			absent:  []string{"schemaflux.cost.currency"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observer, exporter := observerOn(t)
			observer.OperationFinished(observer.OperationStarted(context.Background(), tc.event), tc.result)

			spans := exporter.GetSpans()
			if len(spans) != 1 {
				t.Fatalf("%d spans exported, want 1", len(spans))
			}
			keys := map[string]attribute.Value{}
			for _, kv := range spans[0].Attributes {
				keys[string(kv.Key)] = kv.Value
				if kv.Value.Type() == attribute.STRING && kv.Value.AsString() == "" {
					t.Errorf("attribute %q is set and empty", kv.Key)
				}
			}
			for _, key := range tc.present {
				if _, ok := keys[key]; !ok {
					t.Errorf("attribute %q is missing", key)
				}
			}
			for _, key := range tc.absent {
				if value, ok := keys[key]; ok {
					t.Errorf("attribute %q = %s is set, and nothing established it", key, value.Emit())
				}
			}
		})
	}
}

func TestAnOperationSpanIsInternalAndNamedForTheOperation(t *testing.T) {
	cases := []struct {
		operation string
		errorKind string
		wantName  string
	}{
		{"extract", "", "schemaflux.extract"},
		{"sort", "", "schemaflux.sort"},
		{"summarize", "", "schemaflux.summarize"},
		{"redact_llm", "", "schemaflux.redact_llm"},
		{"extract", "repair exhausted", "schemaflux.extract"},
		{"extract", "schema violation", "schemaflux.extract"},
		{"is", "canceled", "schemaflux.is"},
		{"is", "timeout", "schemaflux.is"},
		{"classify", "internal", "schemaflux.classify"},
		{"", "", "schemaflux.operation"},
	}
	for _, tc := range cases {
		t.Run(tc.wantName+" "+tc.errorKind, func(t *testing.T) {
			observer, exporter := observerOn(t)
			ctx := observer.OperationStarted(context.Background(), telemetry.OperationEvent{
				Scope: telemetry.ScopeOperation, Operation: tc.operation,
			})
			observer.OperationFinished(ctx, telemetry.OperationResult{
				Scope: telemetry.ScopeOperation, Operation: tc.operation, ErrorKind: tc.errorKind,
				// An operation's result carries none of these; set here to show
				// they would not reach its span if it did.
				Attempts: 3, Provider: "openai", Model: "m", Usage: types.TokenUsage{PromptTokens: 9, CompletionTokens: 9},
			})

			spans := exporter.GetSpans()
			if len(spans) != 1 {
				t.Fatalf("%d spans exported, want 1", len(spans))
			}
			span := spans[0]
			if span.Name != tc.wantName || span.SpanKind != oteltrace.SpanKindInternal {
				t.Errorf("span = %q kind %v, want %q kind Internal", span.Name, span.SpanKind, tc.wantName)
			}
			// An operation that made no request says so.
			want := []string{"schemaflux.operation STRING " + tc.operation, "schemaflux.requests INT64 0"}
			wantStatus := codes.Unset
			if tc.errorKind != "" {
				want = []string{"error.type STRING " + tc.errorKind, "schemaflux.operation STRING " + tc.operation, "schemaflux.requests INT64 0"}
				wantStatus = codes.Error
			}
			if got := typed(span.Attributes); !slices.Equal(got, want) {
				t.Errorf("attributes = %v, want %v", got, want)
			}
			if span.Status.Code != wantStatus || span.Status.Description != tc.errorKind {
				t.Errorf("status = %v %q, want %v %q", span.Status.Code, span.Status.Description, wantStatus, tc.errorKind)
			}
		})
	}
}

// A failure is its kind and nothing else: Error status, the kind as the
// description and as error.type, and no event, since RecordError would put the
// error's text in one.
func TestAFailureIsItsKindAndNothingElse(t *testing.T) {
	kinds := []string{
		"rate limited", "provider unavailable", "authentication", "timeout", "canceled",
		"schema violation", "malformed output", "repair exhausted", "output truncated", "internal", "unknown",
	}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			observer, exporter := observerOn(t)
			ctx := observer.OperationStarted(context.Background(), telemetry.OperationEvent{Operation: "extract", Provider: "openai", Model: "m"})
			observer.OperationFinished(ctx, telemetry.OperationResult{Operation: "extract", Attempts: 1, ErrorKind: kind})

			span := exporter.GetSpans()[0]
			if span.Status.Code != codes.Error || span.Status.Description != kind {
				t.Errorf("status = %v %q, want Error %q", span.Status.Code, span.Status.Description, kind)
			}
			found := false
			for _, kv := range span.Attributes {
				if kv.Key == "error.type" {
					found = kv.Value.Type() == attribute.STRING && kv.Value.AsString() == kind
				}
			}
			if !found {
				t.Errorf("error.type is not the string %q", kind)
			}
			if len(span.Events) != 0 {
				t.Errorf("%d events on a failed span; an exception event would carry the error's text", len(span.Events))
			}
		})
	}
}

func TestNotesBecomeSpanEventsOnTheRequestSpan(t *testing.T) {
	observer, exporter := observerOn(t)
	operation := observer.OperationStarted(context.Background(), telemetry.OperationEvent{Scope: telemetry.ScopeOperation, Operation: "extract"})
	observer.OperationNoted(operation, telemetry.Note{Name: telemetry.NoteRetry, Attempt: 9})

	request := observer.OperationStarted(operation, telemetry.OperationEvent{Operation: "extract", Provider: "openai", Model: "m"})
	observer.OperationNoted(request, telemetry.Note{Name: telemetry.NoteRetry, Attempt: 1, ErrorKind: "rate limited", Wait: 20 * time.Millisecond})
	observer.OperationNoted(context.WithValue(request, contextKey{}, "deeper"), telemetry.Note{Name: telemetry.NoteHedge, Attempt: 2})
	observer.OperationFinished(request, telemetry.OperationResult{Attempts: 2})
	observer.OperationNoted(request, telemetry.Note{Name: telemetry.NoteRetry, Attempt: 2, ErrorKind: "timeout"})
	observer.OperationFinished(operation, telemetry.OperationResult{Scope: telemetry.ScopeOperation})

	spans := exporter.GetSpans()
	requestSpan, operationSpan := spanNamed(t, spans, "chat m"), spanNamed(t, spans, "schemaflux.extract")
	if len(operationSpan.Events) != 0 {
		t.Errorf("the operation span has %d events; a note belongs to a request", len(operationSpan.Events))
	}
	if len(requestSpan.Events) != 2 {
		t.Fatalf("the request span has %d events, want the retry and the hedge", len(requestSpan.Events))
	}
	retry, hedge := requestSpan.Events[0], requestSpan.Events[1]
	wantRetry := []string{"error.type STRING rate limited", "schemaflux.attempt INT64 1", "schemaflux.backoff_ms INT64 20"}
	if retry.Name != "retry" || !slices.Equal(typed(retry.Attributes), wantRetry) {
		t.Errorf("retry event = %q %v, want %v", retry.Name, typed(retry.Attributes), wantRetry)
	}
	if hedge.Name != "hedge" || !slices.Equal(typed(hedge.Attributes), []string{"schemaflux.attempt INT64 2"}) {
		t.Errorf("hedge event = %q %v", hedge.Name, typed(hedge.Attributes))
	}
}

// tallyingProvider wraps a provider and counts what is done to the spans
// started on it.
type tallyingProvider struct {
	oteltrace.TracerProvider
	started, ended, writes, writesWhenNotRecording atomic.Int64
}

type tallyingTracer struct {
	oteltrace.Tracer
	tally *tallyingProvider
}

type tallyingSpan struct {
	oteltrace.Span
	tally *tallyingProvider
}

func (p *tallyingProvider) Tracer(name string, opts ...oteltrace.TracerOption) oteltrace.Tracer {
	return &tallyingTracer{Tracer: p.TracerProvider.Tracer(name, opts...), tally: p}
}

func (t *tallyingTracer) Start(ctx context.Context, name string, opts ...oteltrace.SpanStartOption) (context.Context, oteltrace.Span) {
	ctx, span := t.Tracer.Start(ctx, name, opts...)
	t.tally.started.Add(1)
	wrapped := &tallyingSpan{Span: span, tally: t.tally}
	return oteltrace.ContextWithSpan(ctx, wrapped), wrapped
}

func (s *tallyingSpan) End(opts ...oteltrace.SpanEndOption) {
	s.tally.ended.Add(1)
	s.Span.End(opts...)
}

func (s *tallyingSpan) write() {
	s.tally.writes.Add(1)
	if !s.Span.IsRecording() {
		s.tally.writesWhenNotRecording.Add(1)
	}
}

func (s *tallyingSpan) SetAttributes(attributes ...attribute.KeyValue) {
	s.write()
	s.Span.SetAttributes(attributes...)
}

func (s *tallyingSpan) AddEvent(name string, opts ...oteltrace.EventOption) {
	s.write()
	s.Span.AddEvent(name, opts...)
}

func (s *tallyingSpan) SetStatus(code codes.Code, description string) {
	s.write()
	s.Span.SetStatus(code, description)
}

// fullEvent and fullResult have every field set, so every attribute the
// Observer can compute would be computed for them.
var (
	fullEvent = telemetry.OperationEvent{
		Operation: "extract", Provider: "openai", Model: "gpt-5.6-luna", RequestID: "req_1", CorrelationID: "corr_1",
		Mode: "strict", Intelligence: "smart", SchemaName: "invoice", ResponseFormat: "json", Repair: true, Stream: true,
	}
	fullResult = telemetry.OperationResult{
		Operation: "extract", Duration: time.Second, ProviderDuration: time.Second, Attempts: 2,
		Provider: "openai", Model: "gpt-5.6-luna", FinishReason: "stop", ErrorKind: "rate limited",
		Usage: types.TokenUsage{PromptTokens: 100, CompletionTokens: 30, CachedTokens: 4, ReasoningTokens: 2, CacheWriteTokens: 1},
		Cost:  types.CostInfo{TotalCost: 0.01, Currency: "USD", Priced: true, Quality: types.PricingExact},
		// What a decision pair that escalated reports.
		AskedFirst: "jev", AskedFirstModel: "jev-1", Escalated: "below the floor",
	}
)

// A span a sampler dropped has nothing written to it, at start, at a note or
// at finish, and is ended once all the same.
func TestASpanThatIsNotRecordingIsWrittenNothingAndStillEnded(t *testing.T) {
	samplers := map[string]sdktrace.Sampler{
		"never sample":             sdktrace.NeverSample(),
		"parent based, no parent":  sdktrace.ParentBased(sdktrace.NeverSample()),
		"ratio of zero":            sdktrace.TraceIDRatioBased(0),
		"parent based over a zero": sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0)),
	}
	for name, sampler := range samplers {
		for _, scope := range []telemetry.Scope{telemetry.ScopeRequest, telemetry.ScopeOperation} {
			t.Run(name+" "+scope.String(), func(t *testing.T) {
				tally := &tallyingProvider{TracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSampler(sampler))}
				observer, err := New(tally)
				if err != nil {
					t.Fatal(err)
				}
				event, result := fullEvent, fullResult
				event.Scope, result.Scope = scope, scope

				for range 10 {
					ctx := observer.OperationStarted(context.Background(), event)
					observer.OperationNoted(ctx, telemetry.Note{Name: telemetry.NoteRetry, Attempt: 1, ErrorKind: "timeout"})
					observer.OperationFinished(ctx, result)
					observer.OperationFinished(ctx, result)
				}

				if started, ended := tally.started.Load(), tally.ended.Load(); started != 10 || ended != 10 {
					t.Errorf("%d spans started and %d ended, want 10 and 10: a span that is not recording is still ended, once", started, ended)
				}
				if writes := tally.writesWhenNotRecording.Load(); writes != 0 {
					t.Errorf("%d attributes, events or statuses were written to spans that were not recording", writes)
				}
			})
		}
	}
}

// The same held as a cost. Against a provider that samples nothing, a start, a
// note and a finish with every field set cost exactly what the span itself
// costs: its name, what the provider allocates to start a span it will not
// record, and the record of it in the context. That is measured here by doing
// those things by hand, so the comparison holds whatever the SDK allocates; a
// single attribute computed for the dropped span is one allocation more.
func TestNothingIsComputedForASpanThatIsNotRecording(t *testing.T) {
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
	observer, err := New(provider)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	measure := func(event telemetry.OperationEvent, result telemetry.OperationResult) float64 {
		return testing.AllocsPerRun(200, func() {
			started := observer.OperationStarted(ctx, event)
			observer.OperationNoted(started, telemetry.Note{Name: telemetry.NoteRetry, Attempt: 1, ErrorKind: "timeout", Wait: time.Second})
			observer.OperationFinished(started, result)
		})
	}

	tracer := provider.Tracer(instrumentationName)
	model := fullEvent.Model
	spanAlone := testing.AllocsPerRun(200, func() {
		started, span := tracer.Start(ctx, "chat "+model, oteltrace.WithSpanKind(oteltrace.SpanKindClient),
			oteltrace.WithAttributes(creationAttributes(telemetry.OperationEvent{Provider: fullEvent.Provider, Model: model, Operation: fullEvent.Operation})...))
		started = context.WithValue(started, openedSpanKey{}, &openedSpan{span: span})
		_ = started
		span.End()
	})

	full := measure(fullEvent, fullResult)
	if full != spanAlone {
		t.Errorf("a dropped span costs %.0f allocations with every field set, and the span alone costs %.0f: something is computed for it", full, spanAlone)
	}
	if bare := measure(telemetry.OperationEvent{Model: model}, telemetry.OperationResult{}); bare != full {
		t.Errorf("a dropped span costs %.0f allocations with no field set and %.0f with every one", bare, full)
	}

	// And against a provider that records, the fields do cost something, or the
	// comparison above would hold for a reason other than the one it is for.
	recorder, err := New(sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample())))
	if err != nil {
		t.Fatal(err)
	}
	observer = recorder
	if recorded := measure(fullEvent, fullResult); recorded <= full {
		t.Errorf("a recorded span costs %.0f allocations and a dropped one %.0f; the recorded one should cost more", recorded, full)
	}
}

func TestASpanIsEndedOnceHoweverOftenItsFinishArrives(t *testing.T) {
	for _, sampler := range []sdktrace.Sampler{sdktrace.AlwaysSample(), sdktrace.NeverSample()} {
		t.Run(sampler.Description(), func(t *testing.T) {
			tally := &tallyingProvider{TracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSampler(sampler))}
			observer, err := New(tally)
			if err != nil {
				t.Fatal(err)
			}

			ctx := observer.OperationStarted(context.Background(), fullEvent)
			var finishers sync.WaitGroup
			for range 8 {
				finishers.Add(1)
				go func() {
					defer finishers.Done()
					observer.OperationFinished(context.WithValue(ctx, contextKey{}, "derived"), fullResult)
				}()
			}
			finishers.Wait()

			if ended := tally.ended.Load(); ended != 1 {
				t.Errorf("the span was ended %d times, want once", ended)
			}
		})
	}
}

// The host's own span is on the context the library's finish is given. Neither
// a finish with no start, nor one from another Observer, nor a nil or zero
// Observer may end it or write to it.
func TestAFinishNeverTouchesASpanTheObserverDidNotOpen(t *testing.T) {
	cases := []struct {
		name   string
		finish func(t *testing.T, observer *Observer, hostCtx context.Context)
	}{
		{"a finish with no start", func(_ *testing.T, observer *Observer, hostCtx context.Context) {
			observer.OperationFinished(hostCtx, fullResult)
		}},
		{"a finish of the other scope", func(_ *testing.T, observer *Observer, hostCtx context.Context) {
			ctx := observer.OperationStarted(hostCtx, fullEvent)
			wrong := fullResult
			wrong.Scope = telemetry.ScopeOperation
			observer.OperationFinished(ctx, wrong)
			observer.OperationFinished(ctx, fullResult)
		}},
		{"a note with no start", func(_ *testing.T, observer *Observer, hostCtx context.Context) {
			observer.OperationNoted(hostCtx, telemetry.Note{Name: telemetry.NoteRetry})
		}},
		{"a second finish", func(_ *testing.T, observer *Observer, hostCtx context.Context) {
			ctx := observer.OperationStarted(hostCtx, fullEvent)
			observer.OperationFinished(ctx, fullResult)
			observer.OperationFinished(ctx, fullResult)
		}},
		{"a finish by another Observer", func(t *testing.T, observer *Observer, hostCtx context.Context) {
			other, _ := observerOn(t)
			ctx := observer.OperationStarted(hostCtx, fullEvent)
			other.OperationFinished(ctx, fullResult)
			other.OperationNoted(ctx, telemetry.Note{Name: telemetry.NoteRetry})
			observer.OperationFinished(ctx, fullResult)
		}},
		{"a nil Observer", func(_ *testing.T, _ *Observer, hostCtx context.Context) {
			var none *Observer
			if got := none.OperationStarted(hostCtx, fullEvent); got != hostCtx {
				t.Error("a nil Observer changed the context")
			}
			none.OperationFinished(hostCtx, fullResult)
			none.OperationNoted(hostCtx, telemetry.Note{Name: telemetry.NoteRetry})
		}},
		{"a zero Observer", func(t *testing.T, _ *Observer, hostCtx context.Context) {
			zero := &Observer{}
			if got := zero.OperationStarted(hostCtx, fullEvent); got != hostCtx {
				t.Error("a zero Observer changed the context")
			}
			zero.OperationFinished(hostCtx, fullResult)
			zero.OperationNoted(hostCtx, telemetry.Note{Name: telemetry.NoteRetry})
		}},
		{"a finish on a nil context", func(_ *testing.T, observer *Observer, _ context.Context) {
			//lint:ignore SA1012 a nil context is what a careless emitter passes
			observer.OperationFinished(nil, fullResult)
			//lint:ignore SA1012 as above
			observer.OperationNoted(nil, telemetry.Note{Name: telemetry.NoteRetry})
		}},
		{"a start on a nil context", func(_ *testing.T, observer *Observer, _ context.Context) {
			//lint:ignore SA1012 as above
			ctx := observer.OperationStarted(nil, fullEvent)
			observer.OperationFinished(ctx, fullResult)
		}},
		{"a finish on a context derived from the start's", func(_ *testing.T, observer *Observer, hostCtx context.Context) {
			ctx := observer.OperationStarted(hostCtx, fullEvent)
			observer.OperationFinished(context.WithValue(ctx, contextKey{}, "derived"), fullResult)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, exporter := recording(t)
			observer, err := New(provider)
			if err != nil {
				t.Fatal(err)
			}
			hostCtx, host := provider.Tracer("host").Start(context.Background(), "host")

			tc.finish(t, observer, hostCtx)

			if !host.IsRecording() {
				t.Fatal("the host's span was ended by the library")
			}
			host.End()
			hostSpan := spanNamed(t, exporter.GetSpans(), "host")
			if len(hostSpan.Attributes) != 0 || len(hostSpan.Events) != 0 || hostSpan.Status.Code != codes.Unset {
				t.Errorf("the host's span was written to: %d attributes, %d events, status %v",
					len(hostSpan.Attributes), len(hostSpan.Events), hostSpan.Status.Code)
			}
			for _, span := range exporter.GetSpans() {
				if span.Name != "host" && span.Parent.SpanID() != hostSpan.SpanContext.SpanID() && span.Parent.IsValid() {
					t.Errorf("span %q has a parent that is not the host's span", span.Name)
				}
			}
		})
	}
}

func TestALibrarySpanIsAChildOfTheSpanOnTheCallersContext(t *testing.T) {
	provider, exporter := recording(t)
	observer, err := New(provider)
	if err != nil {
		t.Fatal(err)
	}

	hostCtx, host := provider.Tracer("host").Start(context.Background(), "pipeline stage")
	operation := observer.OperationStarted(hostCtx, telemetry.OperationEvent{Scope: telemetry.ScopeOperation, Operation: "extract"})
	first := observer.OperationStarted(operation, telemetry.OperationEvent{Operation: "extract", Model: "first"})
	observer.OperationFinished(first, telemetry.OperationResult{Attempts: 1})
	second := observer.OperationStarted(operation, telemetry.OperationEvent{Operation: "extract", Model: "second", Repair: true})
	observer.OperationFinished(second, telemetry.OperationResult{Attempts: 1})
	observer.OperationFinished(operation, telemetry.OperationResult{Scope: telemetry.ScopeOperation})
	host.End()

	spans := exporter.GetSpans()
	hostSpan, operationSpan := spanNamed(t, spans, "pipeline stage"), spanNamed(t, spans, "schemaflux.extract")
	if operationSpan.Parent.SpanID() != hostSpan.SpanContext.SpanID() {
		t.Error("the operation span is not a child of the host's span")
	}
	for _, name := range []string{"chat first", "chat second"} {
		request := spanNamed(t, spans, name)
		if request.Parent.SpanID() != operationSpan.SpanContext.SpanID() {
			t.Errorf("%q is not a child of the operation span: a repair is a sibling of the request it repairs, not its child", name)
		}
		if request.SpanContext.TraceID() != hostSpan.SpanContext.TraceID() {
			t.Errorf("%q is in a trace of its own", name)
		}
	}

	// With no span on the context the library's span is a root.
	exporter.Reset()
	observer.OperationFinished(observer.OperationStarted(context.Background(), telemetry.OperationEvent{Model: "m"}), telemetry.OperationResult{})
	if root := exporter.GetSpans()[0]; root.Parent.IsValid() {
		t.Error("with no span on the context the library's span has a parent")
	}
}

// Two Observers on two providers, both installed. Each provider's spans form
// their own tree under the caller's span: neither takes the other's span, on a
// provider its backend never hears from, as a parent.
func TestTwoObserversEachKeepTheirOwnTree(t *testing.T) {
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
	operation := telemetry.ObserveOperationStart(hostCtx, telemetry.OperationEvent{Scope: telemetry.ScopeOperation, Operation: "extract"})
	request := telemetry.ObserveOperationStart(operation, telemetry.OperationEvent{Operation: "extract", Model: "m"})
	telemetry.ObserveOperationNote(request, telemetry.Note{Name: telemetry.NoteRetry, Attempt: 1})
	telemetry.ObserveOperationEnd(request, telemetry.OperationResult{Attempts: 2})
	telemetry.ObserveOperationEnd(operation, telemetry.OperationResult{Scope: telemetry.ScopeOperation})
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

// Install sets tracing up whole: the Observer and the ID sources. Its restore
// takes both out, and nothing of an earlier install with them.
func TestInstallAddsTheObserverAndTheIDSourcesAndItsRestoreRemovesBoth(t *testing.T) {
	if telemetry.HasObserver() {
		t.Fatal("an observer is installed before the test began")
	}
	provider, exporter := recording(t)
	ctx, span := provider.Tracer("host").Start(context.Background(), "host")
	defer span.End()
	wantTrace, wantSpan := span.SpanContext().TraceID().String(), span.SpanContext().SpanID().String()

	requesttracking.Configure(requesttracking.Config{
		Enabled: true, RequestIDStrategy: requesttracking.IDStrategyTrace,
		CorrelationIDStrategy: requesttracking.CorrelationStrategyNone,
	})
	t.Cleanup(func() { requesttracking.Configure(requesttracking.DefaultConfig()) })
	traced := func() bool { return requesttracking.Resolve(ctx, "", "").RequestID == wantTrace }

	if telemetry.GetTraceID(ctx) != "" || traced() {
		t.Fatal("an ID source is installed before Install was called")
	}

	restore, err := Install(provider)
	if err != nil {
		t.Fatal(err)
	}
	if !telemetry.HasObserver() {
		t.Error("Install added no observer")
	}
	if got := telemetry.GetTraceID(ctx); got != wantTrace {
		t.Errorf("GetTraceID = %q after Install, want the span's trace ID", got)
	}
	if got := telemetry.GetSpanID(ctx); got != wantSpan {
		t.Errorf("GetSpanID = %q after Install, want the span's ID", got)
	}
	if !traced() {
		t.Error("the request tracker cannot read the trace ID after Install")
	}
	telemetry.ObserveOperationEnd(telemetry.ObserveOperationStart(ctx, telemetry.OperationEvent{Model: "m"}), telemetry.OperationResult{})
	if len(exporter.GetSpans()) != 1 {
		t.Error("an event after Install produced no span")
	}

	restore()
	restore()
	if telemetry.HasObserver() {
		t.Error("the restore left the observer installed")
	}
	if telemetry.GetTraceID(ctx) != "" || telemetry.GetSpanID(ctx) != "" || traced() {
		t.Error("the restore left an ID source installed: the check is made on a context that carries a span")
	}
}

// The ID sources of several installs come out in whatever order their restores
// run, and a restore run a second time brings nothing back. Both halves are
// checked on a context that carries a span, since on any other context a
// source that is still installed answers "" and looks removed.
func TestIDSourceRestoresComposeInAnyOrder(t *testing.T) {
	provider, _ := recording(t)
	ctx, span := provider.Tracer("host").Start(context.Background(), "host")
	defer span.End()
	trace := span.SpanContext().TraceID().String()

	requesttracking.Configure(requesttracking.Config{
		Enabled: true, RequestIDStrategy: requesttracking.IDStrategyTrace,
		CorrelationIDStrategy: requesttracking.CorrelationStrategyNone,
	})
	t.Cleanup(func() { requesttracking.Configure(requesttracking.DefaultConfig()) })

	cases := []struct {
		name      string
		order     []int // restores run, by install position; a position may repeat
		installed bool  // whether a source is still in effect afterwards
	}{
		{"none", nil, true},
		{"first only", []int{0}, true},
		{"second only", []int{1}, true},
		{"second then first", []int{1, 0}, false},
		{"first then second", []int{0, 1}, false},
		{"second, first, second again", []int{1, 0, 1}, false},
		{"first, second, first again", []int{0, 1, 0}, false},
		{"first twice", []int{0, 0}, true},
		{"second twice", []int{1, 1}, true},
		{"second twice then first twice", []int{1, 1, 0, 0}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restores := []func(){InstallIDSources(), InstallIDSources()}
			t.Cleanup(func() {
				for _, restore := range restores {
					restore()
				}
			})
			for _, index := range tc.order {
				restores[index]()
			}

			telemetryHalf := telemetry.GetTraceID(ctx) == trace && telemetry.GetSpanID(ctx) != ""
			trackingHalf := requesttracking.Resolve(ctx, "", "").RequestID == trace
			if telemetryHalf != tc.installed {
				t.Errorf("telemetry's readers installed = %v, want %v", telemetryHalf, tc.installed)
			}
			if trackingHalf != tc.installed {
				t.Errorf("the request tracker's source installed = %v, want %v", trackingHalf, tc.installed)
			}
		})
	}
}

func TestTraceAndSpanIDFromContext(t *testing.T) {
	provider, _ := recording(t)
	spanCtx, span := provider.Tracer("host").Start(context.Background(), "host")
	defer span.End()
	_, unsampled := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample())).Tracer("host").Start(context.Background(), "dropped")
	_, noopSpan := noop.NewTracerProvider().Tracer("host").Start(context.Background(), "noop")
	remote := oteltrace.ContextWithRemoteSpanContext(context.Background(), span.SpanContext())

	cases := []struct {
		name      string
		ctx       context.Context
		wantTrace string
		wantSpan  string
	}{
		{"no span", context.Background(), "", ""},
		{"a value and no span", context.WithValue(context.Background(), contextKey{}, 1), "", ""},
		{"a recording span", spanCtx, span.SpanContext().TraceID().String(), span.SpanContext().SpanID().String()},
		{"a context derived from one with a span", context.WithValue(spanCtx, contextKey{}, 1), span.SpanContext().TraceID().String(), span.SpanContext().SpanID().String()},
		{"a span a sampler dropped", oteltrace.ContextWithSpan(context.Background(), unsampled), unsampled.SpanContext().TraceID().String(), unsampled.SpanContext().SpanID().String()},
		{"a no-op span", oteltrace.ContextWithSpan(context.Background(), noopSpan), "", ""},
		{"a remote span context", remote, span.SpanContext().TraceID().String(), span.SpanContext().SpanID().String()},
		{"an invalid span context", oteltrace.ContextWithSpanContext(context.Background(), oteltrace.SpanContext{}), "", ""},
		{"a trace ID with no span ID", oteltrace.ContextWithSpanContext(context.Background(),
			oteltrace.NewSpanContext(oteltrace.SpanContextConfig{TraceID: span.SpanContext().TraceID()})), "", ""},
		{"a span ID with no trace ID", oteltrace.ContextWithSpanContext(context.Background(),
			oteltrace.NewSpanContext(oteltrace.SpanContextConfig{SpanID: span.SpanContext().SpanID()})), "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := traceIDFromContext(tc.ctx); got != tc.wantTrace {
				t.Errorf("trace ID = %q, want %q", got, tc.wantTrace)
			}
			if got := spanIDFromContext(tc.ctx); got != tc.wantSpan {
				t.Errorf("span ID = %q, want %q", got, tc.wantSpan)
			}
		})
	}
}

// / Everything that reaches a span from a caller or a provider goes through one
// helper, with one limit for each kind of value.
func TestBounded(t *testing.T) {
	long := strings.Repeat("x", 20000)
	cases := []struct {
		name  string
		value string
		limit int
		want  string
	}{
		{"a generated id", "req_ba09dc8ced0b338b", maxIDLength, "req_ba09dc8ced0b338b"},
		{"empty", "", maxIDLength, ""},
		{"exactly the bound", strings.Repeat("a", maxIDLength), maxIDLength, strings.Repeat("a", maxIDLength)},
		{"one over the bound", strings.Repeat("a", maxIDLength+1), maxIDLength, strings.Repeat("a", maxIDLength)},
		{"twenty thousand bytes", long, maxIDLength, long[:maxIDLength]},
		{"a newline", "corr\nforged line", maxIDLength, "corr?forged line"},
		{"a NUL", "corr\x00id", maxIDLength, "corr?id"},
		{"a tab and a carriage return", "a\tb\rc", maxIDLength, "a?b?c"},
		{"an escape sequence", "id\x1b[31mred", maxIDLength, "id?[31mred"},
		{"a delete character", "id\x7f", maxIDLength, "id?"},
		{"non-ASCII text", "café", maxIDLength, "caf??"},
		{"invalid UTF-8", "id\xff\xfe", maxIDLength, "id??"},
		{"spaces and punctuation are kept", "user 42 / order #7", maxIDLength, "user 42 / order #7"},
		{"long and unprintable", strings.Repeat("\n", 300), maxIDLength, strings.Repeat("?", maxIDLength)},
		{"a multi-byte character across the bound", strings.Repeat("a", maxIDLength-1) + "é", maxIDLength, strings.Repeat("a", maxIDLength-1) + "?"},
		{"a model name as a provider writes it", "accounts/fireworks/models/llama-v3p1-70b-instruct:free", maxModelLength, "accounts/fireworks/models/llama-v3p1-70b-instruct:free"},
		{"a model name over its bound", strings.Repeat("m", maxModelLength+1), maxModelLength, strings.Repeat("m", maxModelLength)},
		{"a model name with a newline and a NUL", "m\n" + long + "\x00", maxModelLength, "m?" + strings.Repeat("x", maxModelLength-2)},
		{"a provider over its bound", strings.Repeat("p", maxProviderLength+1), maxProviderLength, strings.Repeat("p", maxProviderLength)},
		{"a finish reason over its bound", strings.Repeat("f", maxFinishReasonLength+1), maxFinishReasonLength, strings.Repeat("f", maxFinishReasonLength)},
		{"a schema name over its bound", strings.Repeat("s", maxSchemaNameLength+1), maxSchemaNameLength, strings.Repeat("s", maxSchemaNameLength)},
		{"an operation name over its bound", strings.Repeat("o", maxOperationLength+1), maxOperationLength, strings.Repeat("o", maxOperationLength)},
		{"a limit of zero", "anything", 0, ""},
		{"a limit of one", "anything", 1, "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bounded(tc.value, tc.limit)
			if got != tc.want {
				t.Errorf("bounded = %q, want %q", got, tc.want)
			}
			if len(got) > tc.limit {
				t.Errorf("%d bytes, over the bound of %d", len(got), tc.limit)
			}
			for index := 0; index < len(got); index++ {
				if got[index] < 0x20 || got[index] > 0x7e {
					t.Fatalf("byte %d is %#x, not a printable character", index, got[index])
				}
			}
		})
	}
}

// The limits are separate constants because the values differ in kind; this
// keeps each within what an identifier needs and a backend tolerates, so one
// cannot be set to nothing or to everything by accident.
func TestEachKindOfValueHasItsOwnNamedLimit(t *testing.T) {
	limits := map[string]int{
		"id":            maxIDLength,
		"model":         maxModelLength,
		"provider":      maxProviderLength,
		"finish reason": maxFinishReasonLength,
		"schema name":   maxSchemaNameLength,
		"operation":     maxOperationLength,
	}
	for kind, limit := range limits {
		if limit < 16 || limit > 256 {
			t.Errorf("the limit for a %s is %d, outside what an identifier needs and a backend tolerates", kind, limit)
		}
	}
}

// A model a caller pinned and the model and provider a provider answers with
// are strings nobody here chose. They reach the span's name and three of its
// attributes, so they are bounded and printable like the ids.
func TestWhatACallerOrAProviderSuppliesReachesTheSpanBoundedAndPrintable(t *testing.T) {
	hostile := "h\n" + strings.Repeat("x", 5000) + "\x00" + strings.Repeat("y", 300)

	observer, exporter := observerOn(t)
	ctx := observer.OperationStarted(context.Background(), telemetry.OperationEvent{
		Operation:     hostile,
		Provider:      hostile,
		Model:         hostile,
		SchemaName:    hostile,
		RequestID:     hostile,
		CorrelationID: hostile,
		Mode:          "strict",
	})
	observer.OperationFinished(ctx, telemetry.OperationResult{
		Attempts:     1,
		Provider:     hostile,
		Model:        hostile,
		FinishReason: hostile,
		Usage:        types.TokenUsage{PromptTokens: 1, CompletionTokens: 1},
	})
	operation := observer.OperationStarted(context.Background(), telemetry.OperationEvent{Scope: telemetry.ScopeOperation, Operation: hostile})
	observer.OperationFinished(operation, telemetry.OperationResult{Scope: telemetry.ScopeOperation, Operation: hostile, ErrorKind: "internal"})

	limits := map[string]int{
		"gen_ai.request.model":           maxModelLength,
		"gen_ai.response.model":          maxModelLength,
		"gen_ai.provider.name":           maxProviderLength,
		"schemaflux.response.provider":   maxProviderLength,
		"gen_ai.response.finish_reasons": maxFinishReasonLength,
		"schemaflux.schema.name":         maxSchemaNameLength,
		"schemaflux.operation":           maxOperationLength,
		"schemaflux.request_id":          maxIDLength,
		"schemaflux.correlation_id":      maxIDLength,
	}
	printable := func(label, value string, limit int) {
		t.Helper()
		if len(value) > limit {
			t.Errorf("%s is %d bytes, over its bound of %d", label, len(value), limit)
		}
		for index := 0; index < len(value); index++ {
			if value[index] < 0x20 || value[index] > 0x7e {
				t.Errorf("%s holds the unprintable byte %#x at %d", label, value[index], index)
				return
			}
		}
	}

	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("%d spans exported, want a request and an operation", len(spans))
	}
	for _, span := range spans {
		// The name is "chat " or "schemaflux." and then a bounded value.
		switch span.SpanKind {
		case oteltrace.SpanKindClient:
			printable("the request span's name", span.Name, len("chat ")+maxModelLength)
		default:
			printable("the operation span's name", span.Name, len("schemaflux.")+maxOperationLength)
		}
		seen := 0
		for _, kv := range span.Attributes {
			limit, supplied := limits[string(kv.Key)]
			if !supplied {
				continue
			}
			seen++
			if kv.Value.Type() == attribute.STRINGSLICE {
				for _, element := range kv.Value.AsStringSlice() {
					printable(string(kv.Key), element, limit)
				}
				continue
			}
			printable(string(kv.Key), kv.Value.AsString(), limit)
		}
		if seen == 0 {
			t.Errorf("span %q carried none of the attributes that hold a supplied string", span.Name)
		}
	}
}

func TestACallersIDsReachTheSpanBoundedAndPrintable(t *testing.T) {
	observer, exporter := observerOn(t)
	ctx := observer.OperationStarted(context.Background(), telemetry.OperationEvent{
		Model:         "m",
		RequestID:     "user@example.com " + strings.Repeat("x", 20000),
		CorrelationID: "corr\n\x00 second line",
	})
	observer.OperationFinished(ctx, telemetry.OperationResult{})

	for _, kv := range exporter.GetSpans()[0].Attributes {
		switch kv.Key {
		case "schemaflux.request_id":
			if got := kv.Value.AsString(); len(got) != 128 {
				t.Errorf("request id is %d bytes on the span, want it cut to 128", len(got))
			}
		case "schemaflux.correlation_id":
			if got := kv.Value.AsString(); got != "corr?? second line" {
				t.Errorf("correlation id = %q, want the control characters replaced", got)
			}
		}
	}
}

type contextKey struct{}

// packageDoc returns the text of this package's documentation comment.
func packageDoc(t *testing.T) string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), "otel.go", nil, parser.ParseComments|parser.PackageClauseOnly)
	if err != nil {
		t.Fatalf("parsing otel.go: %v", err)
	}
	if parsed.Doc == nil {
		t.Fatal("otel.go has no package comment")
	}
	return parsed.Doc.Text()
}

// errorTypeValues is every value error.type can take, generated from the
// library's error kinds: each kind's name, the kind of a call that ended in a
// panic, and the name of an error that carries no kind. A kind added to the
// library appears here without anyone remembering to add it.
func errorTypeValues() []string {
	values := []string{telemetry.ErrorKindInternal, telemetry.ErrorKindRefused, types.KindUnknown.String()}
	for kind := types.ErrorKind(1); kind.String() != types.KindUnknown.String(); kind++ {
		values = append(values, kind.String())
	}
	slices.Sort(values)
	return slices.Compact(values)
}

// The package comment is where an engineer learns what error.type can be, so it
// lists every value, and it cannot go stale: this fails with the block to paste
// when a kind is added, renamed or removed.
func TestThePackageCommentListsEveryErrorTypeTheLibraryCanReport(t *testing.T) {
	doc := packageDoc(t)

	_, afterIntro, found := strings.Cut(doc, "The complete list of values, on a span\nand on a retry event, is:\n")
	if !found {
		t.Fatal("the package comment no longer introduces the list of error.type values")
	}
	var listed []string
	for _, line := range strings.Split(afterIntro, "\n") {
		if !strings.HasPrefix(line, "\t") {
			if len(listed) > 0 {
				break
			}
			continue
		}
		listed = append(listed, strings.TrimSpace(line))
	}

	want := errorTypeValues()
	if !slices.Equal(listed, want) {
		block := "//\t" + strings.Join(want, "\n//\t")
		t.Errorf("the error.type values in the package comment are\n  %s\nwant, from the library's kinds,\n  %s\nreplace the list with:\n%s",
			strings.Join(listed, "\n  "), strings.Join(want, "\n  "), block)
	}
	if len(want) < 20 {
		t.Errorf("only %d error.type values were generated; the enumeration of the kinds stopped early", len(want))
	}
}

// What an engineer needs in order to filter, shut down and choose an ID
// strategy is in the comment, with the values the code uses.
func TestThePackageCommentSaysWhatAnEngineerSettingUpNeeds(t *testing.T) {
	doc := packageDoc(t)
	for _, tc := range []struct{ name, want string }{
		{"the instrumentation scope's name", instrumentationName},
		{"the shutdown order, restore first", "Shut down in the reverse order"},
		{"the provider's shutdown last", "then shut the provider down"},
		{"the request ID strategy that reads the trace", "SCHEMAFLUX_REQUEST_ID_STRATEGY=trace"},
		{"the version of the convention", "v1.41.0"},
		{"that the keys are in development upstream", "in development"},
		{"the model bound", "128 characters for an ID, a model"},
		{"what is never recorded", "What is never recorded"},
		{"the retry event", "schemaflux.backoff_ms"},
		{"the name of Install", "otel.Install(provider)"},
		{"a stream abandoned at its timeout", "ends at the stream's timeout"},
		{"the operation spans of Extract", "Extract in both its forms"},
	} {
		if !strings.Contains(strings.Join(strings.Fields(doc), " "), strings.Join(strings.Fields(tc.want), " ")) {
			t.Errorf("the package comment does not mention %s (%q)", tc.name, tc.want)
		}
	}
}
