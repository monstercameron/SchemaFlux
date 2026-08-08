package otel

import (
	"context"
	"errors"
	"testing"
	"time"

	otelglobal "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/monstercameron/schemaflux/internal/types"
)

// resetTracingGlobals snapshots and restores the package-level tracing state
// (tracer, traceProvider, tracingEnabled, traceSampleRate) that InitTracing
// and StartSpan share. This legacy path (InitTracing is deprecated in favour
// of Install) is still exercised by other packages' tests running in the
// same process image, and this suite runs with -shuffle=on -count=3, so
// leaking "tracing enabled" out of one test into an unrelated one would be a
// self-inflicted flake.
func resetTracingGlobals(t *testing.T) {
	t.Helper()
	prevTracer := tracer
	prevProvider := traceProvider
	prevEnabled := tracingEnabled
	prevRate := traceSampleRate
	t.Cleanup(func() {
		if traceProvider != nil && traceProvider != prevProvider {
			_ = traceProvider.Shutdown(context.Background())
		}
		tracer = prevTracer
		traceProvider = prevProvider
		tracingEnabled = prevEnabled
		traceSampleRate = prevRate
	})
}

func TestInitTracing_DisabledByDefault(t *testing.T) {
	resetTracingGlobals(t)
	t.Setenv("SCHEMAFLUX_ENABLE_TRACING", "")
	t.Setenv("SCHEMAFLUX_TRACE", "")
	tracingEnabled = false

	if err := InitTracing("test-service"); err != nil {
		t.Fatalf("InitTracing returned an error when tracing is disabled: %v", err)
	}
	if tracingEnabled {
		t.Error("InitTracing left tracingEnabled=true although neither env var was set")
	}
}

// BUG (found while writing this test, not fixed -- test files only):
// telemetry/otel/tracing.go:70's resource.Merge(resource.Default(),
// resource.NewWithAttributes(semconv.SchemaURL, ...)) always errors with the
// versions pinned in go.mod (go.opentelemetry.io/otel/sdk v1.44.0,
// go.opentelemetry.io/otel/semconv/v1.17.0 imported at tracing.go:23):
// resource.Default() now bakes in schema https://opentelemetry.io/schemas/1.41.0
// internally, while tracing.go still builds its own resource attributes
// against the pinned v1.17.0 schema, and resource.Merge refuses to merge two
// resources with different (conflicting) schema URLs. The result: with the
// current dependency versions, InitTracing(...) returns a non-nil error on
// every call where tracing is enabled (SCHEMAFLUX_TRACE=true /
// SCHEMAFLUX_ENABLE_TRACING=true), regardless of exporter configuration --
// the stdout exporter, OTLP exporter, sampler, and otel.SetTracerProvider
// code below the resource.Merge call (tracing.go:84-165) are unreachable
// through this entry point. Reproduced outside the test suite too: a
// throwaway `resource.Merge(resource.Default(),
// resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName("x")))`
// with the same semconv v1.17.0 import fails identically. Fix would be
// either bumping the semconv import to match sdk's internal schema version,
// or building the resource without resource.Default() and merging
// attributes by hand -- a production-code change outside this task's
// test-file-only scope, so it is reported rather than patched. InitTracing
// is already marked Deprecated in favour of Install, which is unaffected
// (Install never calls resource.Merge or InitTracing).
//
// The two tests below assert the actual (broken) current behaviour -- an
// error, and tracer/traceProvider left unset -- so they pin down the bug
// rather than silently pass by accident, and so they will fail (as they
// should) the day someone fixes the schema mismatch and forgets to update
// them.
func TestInitTracing_EnabledFailsOnResourceSchemaConflict(t *testing.T) {
	resetTracingGlobals(t)
	t.Setenv("SCHEMAFLUX_ENABLE_TRACING", "")
	t.Setenv("SCHEMAFLUX_TRACE", "true")
	t.Setenv("SCHEMAFLUX_TRACE_SAMPLE_RATE", "0.5")
	t.Setenv("SCHEMAFLUX_EXPORT_TRACES_STDOUT", "")
	t.Setenv("SCHEMAFLUX_OTLP_ENDPOINT", "")
	t.Setenv("SCHEMAFLUX_JAEGER_ENDPOINT", "")

	err := InitTracing("test-service")
	if err == nil {
		t.Fatal("InitTracing succeeded; expected the resource schema conflict documented above. " +
			"If this now passes, telemetry/otel's dependencies were updated to fix the schema mismatch -- " +
			"update this test (and re-check whether the exporter/provider paths need direct coverage) rather than deleting it.")
	}
	if traceSampleRate != 0.5 {
		t.Errorf("traceSampleRate = %v, want 0.5 (SCHEMAFLUX_TRACE_SAMPLE_RATE is parsed before the resource is built)", traceSampleRate)
	}
	// tracingEnabled is set to true (tracing.go:59) before the resource
	// build that then fails -- InitTracing returns an error but leaves this
	// global true. StartSpan still no-ops safely because tracer stays nil,
	// but this is a second, smaller inconsistency worth flagging alongside
	// the main one: an error return that does not roll back state it already
	// mutated.
	if !tracingEnabled {
		t.Error("tracingEnabled = false after a failed InitTracing; expected it to remain true (tracing.go:59 runs before the failing resource build)")
	}
	if tracer != nil {
		t.Error("tracer was set despite InitTracing returning an error")
	}
	if traceProvider != nil {
		t.Error("traceProvider was set despite InitTracing returning an error")
	}
}

func TestInitTracing_InvalidSampleRateIsIgnoredEvenThoughInitFails(t *testing.T) {
	resetTracingGlobals(t)
	t.Setenv("SCHEMAFLUX_TRACE", "true")
	t.Setenv("SCHEMAFLUX_TRACE_SAMPLE_RATE", "not-a-number")
	t.Setenv("SCHEMAFLUX_OTLP_ENDPOINT", "")
	t.Setenv("SCHEMAFLUX_JAEGER_ENDPOINT", "")
	t.Setenv("SCHEMAFLUX_EXPORT_TRACES_STDOUT", "")
	traceSampleRate = 0.1

	// The sample rate parse (tracing.go:62-67) runs, and is unaffected by
	// the later resource-schema failure -- this covers the "ignore an
	// unparsable rate" branch independent of that bug.
	_ = InitTracing("test-service")
	if traceSampleRate != 0.1 {
		t.Errorf("traceSampleRate = %v, want unchanged 0.1 for an unparsable SCHEMAFLUX_TRACE_SAMPLE_RATE", traceSampleRate)
	}
}

func TestShutdownTracing_NilProviderIsANoop(t *testing.T) {
	resetTracingGlobals(t)
	traceProvider = nil

	if err := ShutdownTracing(context.Background()); err != nil {
		t.Fatalf("ShutdownTracing with a nil provider returned an error: %v", err)
	}
}

func TestShutdownTracing_ShutsDownRealProvider(t *testing.T) {
	resetTracingGlobals(t)
	exporter := tracetest.NewInMemoryExporter()
	traceProvider = sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))

	if err := ShutdownTracing(context.Background()); err != nil {
		t.Fatalf("ShutdownTracing: %v", err)
	}
}

// StartSpan is a no-op (returns the incoming context and a non-recording
// span) when tracing is disabled, which is the default in every test that
// has not called InitTracing.
func TestStartSpan_DisabledIsANoop(t *testing.T) {
	resetTracingGlobals(t)
	tracingEnabled = false
	tracer = nil

	ctx := context.Background()
	newCtx, span := StartSpan(ctx, "op", types.OpOptions{})
	if newCtx != ctx {
		t.Error("StartSpan with tracing disabled returned a different context")
	}
	if span.IsRecording() {
		t.Error("StartSpan with tracing disabled returned a recording span")
	}
}

// StartSpan with tracing enabled sets the full attribute set, including the
// optional RequestID/CorrelationID/Steering attributes and the Steering
// truncation path (steering longer than 200 runes).
func TestStartSpan_EnabledSetsAttributesIncludingTruncatedSteering(t *testing.T) {
	resetTracingGlobals(t)
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	tracingEnabled = true
	tracer = provider.Tracer("test")

	longSteering := ""
	for i := 0; i < 50; i++ {
		longSteering += "0123456789"
	}

	ctx, span := StartSpan(context.Background(), "extract", types.OpOptions{
		Mode:          types.TransformMode,
		Intelligence:  types.Fast,
		Threshold:     0.75,
		RequestID:     "req-1",
		CorrelationID: "corr-1",
		Steering:      longSteering,
	})
	if ctx == nil {
		t.Fatal("StartSpan returned a nil context")
	}
	if !span.IsRecording() {
		t.Fatal("StartSpan with tracing enabled returned a non-recording span")
	}
	span.End()

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("%d spans, want 1", len(spans))
	}
	attrs := map[string]string{}
	for _, a := range spans[0].Attributes {
		attrs[string(a.Key)] = a.Value.AsString()
	}
	if attrs["schemaflux.request_id"] != "req-1" {
		t.Errorf("request_id = %q", attrs["schemaflux.request_id"])
	}
	if attrs["schemaflux.correlation_id"] != "corr-1" {
		t.Errorf("correlation_id = %q", attrs["schemaflux.correlation_id"])
	}
	steering := attrs["schemaflux.steering"]
	if len(steering) != 200 {
		t.Errorf("steering attribute length = %d, want 200 (truncated)", len(steering))
	}
	if steering[len(steering)-3:] != "..." {
		t.Errorf("truncated steering = %q, want a ... suffix", steering)
	}
}

// RecordLLMCall's every conditional branch: nil/non-recording span is a
// no-op, usage/cost are only recorded when non-nil, cached/reasoning tokens
// and costs are only recorded when positive, and an error sets span status
// and attribute while a nil error sets Ok.
func TestRecordLLMCall_AllBranches(t *testing.T) {
	// nil span must not panic.
	RecordLLMCall(nil, "m", "p", nil, nil, 0, nil)

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tr := provider.Tracer("test")

	t.Run("success with full usage and cost", func(t *testing.T) {
		_, span := tr.Start(context.Background(), "s1")
		usage := &types.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, CachedTokens: 3, ReasoningTokens: 2}
		cost := &types.CostInfo{TotalCost: 1, PromptCost: 0.5, CompletionCost: 0.5, Currency: "USD", CachedCost: 0.1, ReasoningCost: 0.2}
		RecordLLMCall(span, "gpt", "openai", usage, cost, 2*time.Second, nil)
		span.End()
	})

	t.Run("failure sets error status", func(t *testing.T) {
		_, span := tr.Start(context.Background(), "s2")
		RecordLLMCall(span, "gpt", "openai", nil, nil, 0, errors.New("boom"))
		span.End()
	})

	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("%d spans, want 2", len(spans))
	}

	successAttrs := map[string]string{}
	for _, a := range spans[0].Attributes {
		successAttrs[string(a.Key)] = a.Value.AsString()
	}
	for _, key := range []string{"llm.tokens.cached", "llm.tokens.reasoning", "llm.cost.cached_usd", "llm.cost.reasoning_usd"} {
		if _, ok := successAttrs[key]; !ok {
			t.Errorf("success span missing attribute %q", key)
		}
	}
	if spans[0].Status.Code.String() != "Ok" {
		t.Errorf("success span status = %v, want Ok", spans[0].Status.Code)
	}

	failAttrs := map[string]string{}
	for _, a := range spans[1].Attributes {
		failAttrs[string(a.Key)] = a.Value.AsString()
	}
	if failAttrs["llm.error"] != "boom" {
		t.Errorf("llm.error = %q, want %q", failAttrs["llm.error"], "boom")
	}
	if spans[1].Status.Code.String() != "Error" {
		t.Errorf("failure span status = %v, want Error", spans[1].Status.Code)
	}
}

// RecordLLMCall must not record cached/reasoning fields when they are zero,
// the complement of the "all branches" case above.
func TestRecordLLMCall_ZeroCachedAndReasoningAreOmitted(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tr := provider.Tracer("test")

	_, span := tr.Start(context.Background(), "s")
	usage := &types.TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}
	cost := &types.CostInfo{TotalCost: 1, Currency: "USD"}
	RecordLLMCall(span, "gpt", "openai", usage, cost, 0, nil)
	span.End()

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("%d spans", len(spans))
	}
	for _, a := range spans[0].Attributes {
		key := string(a.Key)
		if key == "llm.tokens.cached" || key == "llm.tokens.reasoning" || key == "llm.cost.cached_usd" || key == "llm.cost.reasoning_usd" {
			t.Errorf("zero-valued field %q was recorded anyway", key)
		}
	}
}

func TestAddSpanTags_NoRecordingSpanIsANoop(t *testing.T) {
	// Neither call should panic; there is nothing else observable about a
	// no-op span from outside the package.
	AddSpanTags(context.Background(), map[string]string{"k": "v"})
}

func TestAddSpanTags_RecordingSpanGetsAttributes(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tr := provider.Tracer("test")

	ctx, span := tr.Start(context.Background(), "s")
	AddSpanTags(ctx, map[string]string{"user": "u1", "operation": "op1"})
	span.End()

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("%d spans, want 1", len(spans))
	}
	attrs := map[string]string{}
	for _, a := range spans[0].Attributes {
		attrs[string(a.Key)] = a.Value.AsString()
	}
	if attrs["user"] != "u1" || attrs["operation"] != "op1" {
		t.Errorf("attrs = %v, want user=u1 operation=op1", attrs)
	}
}

// RecordSpanEvent covers every value kind the type switch recognises, plus
// the default branch (an unrecognised type formatted with %v), and confirms
// a non-recording context does not panic.
func TestRecordSpanEvent_AllValueKinds(t *testing.T) {
	RecordSpanEvent(context.Background(), "noop", map[string]any{"k": "v"}) // no-op, no span

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tr := provider.Tracer("test")

	ctx, span := tr.Start(context.Background(), "s")
	RecordSpanEvent(ctx, "kinds", map[string]any{
		"s":   "text",
		"i":   7,
		"i64": int64(8),
		"f":   1.5,
		"b":   true,
		"o":   struct{ X int }{X: 1},
	})
	span.End()

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("%d spans", len(spans))
	}
	if len(spans[0].Events) != 1 {
		t.Fatalf("%d events, want 1", len(spans[0].Events))
	}
	if spans[0].Events[0].Name != "kinds" {
		t.Errorf("event name = %q", spans[0].Events[0].Name)
	}
	if len(spans[0].Events[0].Attributes) != 6 {
		t.Errorf("event attribute count = %d, want 6", len(spans[0].Events[0].Attributes))
	}
}

func TestExtractAndInjectTraceContext_DisabledIsANoop(t *testing.T) {
	resetTracingGlobals(t)
	tracingEnabled = false

	carrier := map[string]string{"traceparent": "should-not-be-read"}
	in := context.Background()
	if got := ExtractTraceContext(in, carrier); got != in {
		t.Error("ExtractTraceContext returned a different context while tracing is disabled")
	}

	out := map[string]string{}
	InjectTraceContext(context.Background(), out)
	if len(out) != 0 {
		t.Errorf("InjectTraceContext wrote %v while tracing is disabled", out)
	}
}

func TestExtractAndInjectTraceContext_RoundTripWhenEnabled(t *testing.T) {
	resetTracingGlobals(t)
	tracingEnabled = true

	// InitTracing is what would normally install a global propagator
	// (tracing.go's otel.SetTextMapPropagator call), but InitTracing cannot
	// be used here -- see the resource-schema-conflict bug documented above
	// TestInitTracing_EnabledFailsOnResourceSchemaConflict, which means it
	// never reaches that line either. Installing the propagator directly
	// exercises Extract/InjectTraceContext's actual job (delegating to
	// whatever propagator is globally configured) independent of that
	// unrelated, already-reported bug.
	prevPropagator := otelglobal.GetTextMapPropagator()
	otelglobal.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	t.Cleanup(func() { otelglobal.SetTextMapPropagator(prevPropagator) })

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tr := provider.Tracer("test")

	ctx, span := tr.Start(context.Background(), "s")
	defer span.End()

	carrier := map[string]string{}
	InjectTraceContext(ctx, carrier)
	if len(carrier) == 0 {
		t.Fatal("InjectTraceContext with tracing enabled and a real propagator wrote nothing to the carrier")
	}

	extracted := ExtractTraceContext(context.Background(), carrier)
	if GetTraceID(extracted) != GetTraceID(ctx) {
		t.Errorf("round trip trace ID = %q, want %q", GetTraceID(extracted), GetTraceID(ctx))
	}
}

// BUG (found while writing this test, not fixed -- test files only):
// GetTraceID/GetSpanID (tracing.go:331-346) call
// trace.SpanFromContext(ctx).SpanContext().TraceID().String() /
// .SpanID().String() unconditionally. trace.SpanFromContext never returns
// nil (it returns a no-op span for a context carrying none), so the `if
// span != nil` guard never protects anything, and a context with no span
// produces the all-zero TraceID/SpanID hex string ("00000000000000000000000000000000"
// / "0000000000000000") rather than "". That is a real-looking-but-fake
// identifier, not a refusal -- the opposite of otel.go's traceIDFromContext/
// spanIDFromContext (otel.go:150-164), which explicitly check
// spanContext.IsValid() and return "" for exactly this case. A caller
// logging or correlating on GetTraceID's result cannot tell "no span" apart
// from "a span whose ID happens to be all zeros" (astronomically unlikely
// but not the point -- the point is the function claims to return "the
// current trace ID" and instead returns a well-formed lie). This test
// documents the actual behaviour rather than the "" a reader of otel.go
// would expect by analogy.
// An untraced context must yield "", not a string that looks like an ID.
//
// This test previously asserted the opposite, pinning the bug: the functions
// guarded with `span != nil`, but trace.SpanFromContext never returns nil -- it
// returns a no-op span with a zero SpanContext -- so an untraced context took
// the success branch and produced "00000000000000000000000000000000". That
// passes an `if id != ""` check and correlates nothing, so every log line and
// stored result built from an untraced context carried a plausible-looking lie.
//
// Fixed by checking SpanContext().IsValid(), which is what otel.go's newer
// traceIDFromContext already did.
func TestAnUntracedContextYieldsNoIDRatherThanAZeroOne(t *testing.T) {
	zeroTraceID := oteltrace.TraceID{}.String()

	if got := GetTraceID(context.Background()); got != "" {
		t.Errorf("GetTraceID with no span = %q, want the empty string", got)
		if got == zeroTraceID {
			t.Error("it returned the all-zero placeholder, which reads as a real trace ID to anything checking for non-empty")
		}
	}
	if got := GetSpanID(context.Background()); got != "" {
		t.Errorf("GetSpanID with no span = %q, want the empty string", got)
	}
}

func TestGetEnvironment(t *testing.T) {
	t.Setenv("SCHEMAFLUX_ENVIRONMENT", "")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("ENV", "")
	if got := getEnvironment(); got != "development" {
		t.Errorf("getEnvironment() = %q, want %q with no env vars set", got, "development")
	}

	t.Setenv("ENV", "from-env")
	if got := getEnvironment(); got != "from-env" {
		t.Errorf("getEnvironment() = %q, want %q", got, "from-env")
	}

	t.Setenv("ENVIRONMENT", "from-environment")
	if got := getEnvironment(); got != "from-environment" {
		t.Errorf("getEnvironment() = %q, want %q", got, "from-environment")
	}

	t.Setenv("SCHEMAFLUX_ENVIRONMENT", "from-schemaflux")
	if got := getEnvironment(); got != "from-schemaflux" {
		t.Errorf("getEnvironment() = %q, want %q (highest priority)", got, "from-schemaflux")
	}
}

func TestTruncateString(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		maxLen int
		want   string
	}{
		{"shorter than max", "hello", 10, "hello"},
		{"exactly max", "hello", 5, "hello"},
		{"longer than max", "hello world", 8, "hello..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateString(tc.in, tc.maxLen); got != tc.want {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tc.in, tc.maxLen, got, tc.want)
			}
		})
	}
}
