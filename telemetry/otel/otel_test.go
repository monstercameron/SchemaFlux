package otel

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/monstercameron/schemaflux/telemetry"
)

// MW-007's whole point: this adapter uses the host's provider and touches
// nothing global. The first test is the one that matters — it asserts an
// absence, which is the only way to check that a library did *not* reconfigure
// its host.

func TestInstallingDoesNotTouchTheGlobalProvider(t *testing.T) {
	before := otel.GetTracerProvider()

	exporter := tracetest.NewInMemoryExporter()
	provider := trace.NewTracerProvider(trace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	restore, err := Install(provider)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	defer restore()

	if otel.GetTracerProvider() != before {
		t.Error("installing the adapter replaced the host's global TracerProvider; " +
			"a library that does this cannot be embedded twice")
	}
}

// Falling back to the global provider when handed nil is how a library ends up
// writing into a stack nobody asked it to write into, so nil is an error.
func TestANilProviderIsRefusedRatherThanFallingBackToTheGlobal(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("a nil TracerProvider was accepted")
	}
	if _, err := Install(nil); err == nil {
		t.Fatal("Install accepted a nil TracerProvider")
	}
}

func TestAnOperationProducesOneSpanOnTheHostsProvider(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := trace.NewTracerProvider(trace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	observer, err := New(provider)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := observer.OperationStarted(context.Background(), telemetry.OperationEvent{
		Operation: "extract",
		Provider:  "openai",
		Model:     "gpt-5.6-luna",
		RequestID: "req-7",
	})
	observer.OperationFinished(ctx, telemetry.OperationResult{
		Operation: "extract",
		Attempts:  1,
	})

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("%d spans exported, want 1", len(spans))
	}
	if spans[0].Name != "schemaflux.extract" {
		t.Errorf("span name = %q", spans[0].Name)
	}
	if spans[0].SpanKind != oteltrace.SpanKindClient {
		t.Errorf("span kind = %v, want client", spans[0].SpanKind)
	}

	attributes := map[string]string{}
	for _, attr := range spans[0].Attributes {
		attributes[string(attr.Key)] = attr.Value.String()
	}
	if attributes["schemaflux.model"] != "gpt-5.6-luna" {
		t.Errorf("model attribute = %q", attributes["schemaflux.model"])
	}
	if attributes["schemaflux.request_id"] != "req-7" {
		t.Errorf("request id attribute = %q", attributes["schemaflux.request_id"])
	}
}

// An unpriced call must not export a cost of zero: that is indistinguishable
// from a free one, which is exactly the distinction PR-001 exists for.
func TestAnUnpricedCallIsMarkedUnpricedRatherThanZero(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := trace.NewTracerProvider(trace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	observer, _ := New(provider)
	ctx := observer.OperationStarted(context.Background(), telemetry.OperationEvent{Operation: "extract"})
	observer.OperationFinished(ctx, telemetry.OperationResult{Operation: "extract"})

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("%d spans", len(spans))
	}

	for _, attr := range spans[0].Attributes {
		if string(attr.Key) == "schemaflux.cost.total" {
			t.Error("an unpriced call exported a cost figure")
		}
	}
	found := false
	for _, attr := range spans[0].Attributes {
		if string(attr.Key) == "schemaflux.cost.unpriced" {
			found = true
		}
	}
	if !found {
		t.Error("an unpriced call did not say so")
	}
}

func TestAFailureExportsTheKindNotAMessage(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := trace.NewTracerProvider(trace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	observer, _ := New(provider)
	ctx := observer.OperationStarted(context.Background(), telemetry.OperationEvent{Operation: "extract"})
	observer.OperationFinished(ctx, telemetry.OperationResult{
		Operation: "extract",
		ErrorKind: "rate_limited",
	})

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("%d spans", len(spans))
	}

	kind := ""
	for _, attr := range spans[0].Attributes {
		if string(attr.Key) == "schemaflux.error.kind" {
			kind = attr.Value.String()
		}
	}
	if kind != "rate_limited" {
		t.Errorf("error kind attribute = %q", kind)
	}
	if spans[0].Status.Code.String() != "Error" {
		t.Errorf("span status = %v, want Error", spans[0].Status.Code)
	}
}

// A nil observer and a finish with no matching start must not panic: an
// observer is called from the library's hot path, and a panic there takes down
// the operation it was only supposed to watch.
func TestTheAdapterSurvivesBeingCalledOutOfOrder(t *testing.T) {
	var observer *Observer
	ctx := observer.OperationStarted(context.Background(), telemetry.OperationEvent{})
	observer.OperationFinished(ctx, telemetry.OperationResult{})

	exporter := tracetest.NewInMemoryExporter()
	provider := trace.NewTracerProvider(trace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	real, _ := New(provider)
	// Finishing a context that never started an operation: no span to end.
	real.OperationFinished(context.Background(), telemetry.OperationResult{Operation: "extract"})

	if len(exporter.GetSpans()) != 0 {
		t.Error("a finish with no start produced a span")
	}
}
