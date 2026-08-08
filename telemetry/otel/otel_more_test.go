package otel

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/telemetry"
)

// A priced call must export the actual figure, not the "unpriced" marker --
// the complement of TestAnUnpricedCallIsMarkedUnpricedRatherThanZero in
// otel_test.go, which only exercises the unpriced branch.
func TestAPricedCallExportsTheCostFigure(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := trace.NewTracerProvider(trace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	observer, err := New(provider)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := observer.OperationStarted(context.Background(), telemetry.OperationEvent{Operation: "extract"})
	observer.OperationFinished(ctx, telemetry.OperationResult{
		Operation: "extract",
		Cost:      types.CostInfo{Priced: true, TotalCost: 1.23},
	})

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("%d spans, want 1", len(spans))
	}

	var gotCost float64
	var gotCostSet, gotUnpriced bool
	for _, attr := range spans[0].Attributes {
		switch string(attr.Key) {
		case "schemaflux.cost.total":
			gotCost = attr.Value.AsFloat64()
			gotCostSet = true
		case "schemaflux.cost.unpriced":
			gotUnpriced = true
		}
	}
	if !gotCostSet {
		t.Fatal("priced call did not export schemaflux.cost.total")
	}
	if gotCost != 1.23 {
		t.Errorf("schemaflux.cost.total = %v, want 1.23", gotCost)
	}
	if gotUnpriced {
		t.Error("priced call also exported schemaflux.cost.unpriced=true")
	}
}

// InstallIDSources wires trace/span ID readers into requesttracking and
// telemetry so the core can describe a span's identity without importing
// OpenTelemetry (see the doc comment on InstallIDSources). This drives it
// with a real recording span, so traceIDFromContext/spanIDFromContext see a
// valid SpanContext, and confirms restore() puts both packages back.
func TestInstallIDSources_WiresAndRestores(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := trace.NewTracerProvider(trace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	prevCfg := requesttracking.GetConfig()
	requesttracking.Configure(requesttracking.Config{
		Enabled:               true,
		RequestIDStrategy:     requesttracking.IDStrategyTrace,
		CorrelationIDStrategy: requesttracking.CorrelationStrategyRequest,
	})
	t.Cleanup(func() { requesttracking.Configure(prevCfg) })

	restore := InstallIDSources()
	defer restore()

	observer, err := New(provider)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := observer.OperationStarted(context.Background(), telemetry.OperationEvent{Operation: "extract"})

	// telemetry.GetTraceID / GetSpanID read straight off the span via the
	// hooks InstallIDSources just installed.
	if got := telemetry.GetTraceID(ctx); got == "" {
		t.Error("telemetry.GetTraceID returned empty string for a recording span")
	}
	if got := telemetry.GetSpanID(ctx); got == "" {
		t.Error("telemetry.GetSpanID returned empty string for a recording span")
	}

	// requesttracking's IDStrategyTrace prefers the ambient trace ID over
	// generating a random one -- this is the behaviour InstallIDSources
	// exists to make available outside telemetry/otel.
	meta := requesttracking.Resolve(ctx, "", "")
	if meta.RequestID == "" {
		t.Fatal("Resolve produced an empty request ID")
	}
	traceID := telemetry.GetTraceID(ctx)
	if meta.RequestID != traceID {
		t.Errorf("Resolve's request ID = %q, want the ambient trace ID %q (IDStrategyTrace)", meta.RequestID, traceID)
	}

	observer.OperationFinished(ctx, telemetry.OperationResult{Operation: "extract"})

	restore()

	// After restore, requesttracking falls back to generating its own ID
	// again rather than reading a (now stale) trace ID source.
	meta2 := requesttracking.Resolve(context.Background(), "", "")
	if meta2.RequestID == traceID {
		t.Error("requesttracking still returned the trace ID after InstallIDSources' restore() ran")
	}
}

// A context carrying no span must read back as "", the same refusal
// otel.go's own doc comment describes: an invalid SpanContext produces no
// identifier rather than a zero-valued placeholder that looks like a real
// one.
func TestTraceAndSpanIDFromContext_NoSpanIsEmpty(t *testing.T) {
	restore := InstallIDSources()
	defer restore()

	ctx := context.Background()
	if got := telemetry.GetTraceID(ctx); got != "" {
		t.Errorf("trace ID from a context with no span = %q, want empty", got)
	}
	if got := telemetry.GetSpanID(ctx); got != "" {
		t.Errorf("span ID from a context with no span = %q, want empty", got)
	}
}
