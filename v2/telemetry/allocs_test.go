package telemetry_test

import (
	"context"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/types"
	wrapped "github.com/monstercameron/schemaflux/telemetry"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// The library emits on every call of every operation. With nothing registered
// through this package, that must cost nothing: no allocation, whether nothing
// was ever registered or everything was registered and removed again.

// onACall does what a call of an operation does with the emission functions of
// the package this one wraps and with this package's readers: an operation unit
// around a request unit, a note on the request, and the ids read once.
func onACall(ctx context.Context) {
	operation := wrapped.ObserveOperationStart(ctx, wrapped.OperationEvent{Scope: wrapped.ScopeOperation, Operation: "extract"})
	request := wrapped.ObserveOperationStart(operation, wrapped.OperationEvent{
		Operation: "extract", Provider: "openai", Model: "gpt-5", RequestID: "req_1", SchemaName: "invoice",
	})
	wrapped.ObserveOperationNote(request, wrapped.Note{Name: wrapped.NoteRetry, Attempt: 1, ErrorKind: "rate limited", Wait: time.Second})
	wrapped.ObserveOperationEnd(request, wrapped.OperationResult{Attempts: 2, Provider: "openai", Model: "gpt-5"})
	wrapped.ObserveOperationEnd(operation, wrapped.OperationResult{Scope: wrapped.ScopeOperation, Operation: "extract"})
	_ = telemetry.TraceID(ctx)
	_ = telemetry.SpanID(ctx)
}

func TestWithNoObserverRegisteredACallsEmissionAllocatesNothing(t *testing.T) {
	withNoObserver(t)
	ctx := context.Background()

	if got := testing.AllocsPerRun(200, func() { onACall(ctx) }); got != 0 {
		t.Errorf("a call's emission allocates %.0f times with nothing registered, want 0", got)
	}
}

func TestAfterTheLastObserverIsRestoredACallsEmissionAllocatesNothingAgain(t *testing.T) {
	withNoObserver(t)
	ctx := context.Background()

	restores := []func(){
		telemetry.RegisterObserver(&recorder{}),
		telemetry.RegisterObserver(&recorder{stamp: "s"}),
	}
	// While two are registered a call costs something: that is what the
	// measurement afterwards is compared with.
	if got := testing.AllocsPerRun(50, func() { onACall(ctx) }); got == 0 {
		t.Error("a call allocates nothing with two observers registered, so the check that follows measures nothing")
	}
	for _, restore := range restores {
		restore()
	}
	if got := testing.AllocsPerRun(200, func() { onACall(ctx) }); got != 0 {
		t.Errorf("a call's emission allocates %.0f times after every observer was restored, want 0", got)
	}
}

func TestWithNoSinkRegisteredRecordingAMetricAllocatesNothing(t *testing.T) {
	cleanMetrics(t)
	metadata := &types.ResultMetadata{
		Provider: "openai", Model: "gpt-5", Mode: types.Strict, Intelligence: types.Fast, Duration: time.Second,
		TokenUsage: &types.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		CostInfo:   &types.CostInfo{TotalCost: 0.1, Priced: true},
	}
	record := func() {
		wrapped.RecordLLMMetrics(metadata)
		wrapped.RecordLLMFailure("openai", "gpt-5", types.Strict, types.Fast, "rate limited")
		wrapped.RecordMetric("extract_duration", 5, nil)
	}
	if config.IsMetricsEnabled() {
		t.Fatal("metrics are on with no sink registered")
	}
	if got := testing.AllocsPerRun(200, record); got != 0 {
		t.Errorf("recording metrics allocates %.0f times with no sink registered, want 0", got)
	}

	restore := telemetry.RegisterSink(&collector{})
	if got := testing.AllocsPerRun(20, record); got == 0 {
		t.Error("recording allocates nothing with a sink registered, so the checks around it measure nothing")
	}
	restore()
	if got := testing.AllocsPerRun(200, record); got != 0 {
		t.Errorf("recording metrics allocates %.0f times after the last sink was removed, want 0", got)
	}
}
