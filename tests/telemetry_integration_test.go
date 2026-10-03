package tests

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/schemafluxtest"
	"github.com/monstercameron/schemaflux/telemetry"
	fluxotel "github.com/monstercameron/schemaflux/telemetry/otel"
)

// Tracing through the public API, the way an embedding host codes against it:
// the host builds its own TracerProvider, installs the adapter with one call,
// opens a span for its own work, calls an ordinary SchemaFlux operation, and
// finds the library's spans underneath its own. No provider is reached,
// schemafluxtest answers in-process, and no credential is involved.

type telemetryInvoice struct {
	Number string `json:"number"`
	Vendor string `json:"vendor"`
}

// hostProvider stands in for the embedding application's telemetry stack.
func hostProvider(t *testing.T) (*sdktrace.TracerProvider, *tracetest.InMemoryExporter) {
	t.Helper()

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return provider, exporter
}

func attributesOf(span tracetest.SpanStub) map[string]string {
	attributes := map[string]string{}
	for _, kv := range span.Attributes {
		attributes[string(kv.Key)] = kv.Value.Emit()
	}
	return attributes
}

func TestAPublicOperationJoinsTheHostsTrace(t *testing.T) {
	provider, exporter := hostProvider(t)
	restore, err := fluxotel.Install(provider)
	if err != nil {
		t.Fatalf("installing the adapter on the host's provider: %v", err)
	}
	t.Cleanup(restore)
	schemafluxtest.Install(t, schemafluxtest.New().Reply(`{"number":"INV-1","vendor":"Acme"}`))

	// The host's own span for its own unit of work, the way a pipeline stage
	// would open one before calling into the library.
	ctx, stage := provider.Tracer("host/pipeline").Start(context.Background(), "stage.parse-invoice")

	value, err := schemaflux.Extract[telemetryInvoice]("Invoice INV-1 from Acme",
		schemaflux.ExtractOptions{CommonOptions: schemaflux.CommonOptions{Context: ctx}})
	stage.End()

	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if value.Number != "INV-1" {
		t.Fatalf("Extract returned %+v", value)
	}

	var stageSpan, operationSpan, requestSpan tracetest.SpanStub
	spans := exporter.GetSpans()
	for _, span := range spans {
		switch {
		case span.Name == "stage.parse-invoice":
			stageSpan = span
		case span.Name == "schemaflux.extract":
			operationSpan = span
		case strings.HasPrefix(span.Name, "chat "):
			requestSpan = span
		}
	}
	if len(spans) != 3 || stageSpan.Name == "" || operationSpan.Name == "" || requestSpan.Name == "" {
		t.Fatalf("got %d spans, want the host's stage, the operation's span and its request's span", len(spans))
	}

	if operationSpan.Parent.SpanID() != stageSpan.SpanContext.SpanID() {
		t.Errorf("the operation span's parent is %s, want the host's stage %s",
			operationSpan.Parent.SpanID(), stageSpan.SpanContext.SpanID())
	}
	if requestSpan.Parent.SpanID() != operationSpan.SpanContext.SpanID() {
		t.Errorf("the request span's parent is %s, want the operation span %s",
			requestSpan.Parent.SpanID(), operationSpan.SpanContext.SpanID())
	}
	for _, span := range []tracetest.SpanStub{operationSpan, requestSpan} {
		if span.SpanContext.TraceID() != stageSpan.SpanContext.TraceID() {
			t.Errorf("%q is in a different trace from the host's stage", span.Name)
		}
		if span.Status.Code != codes.Unset {
			t.Errorf("%q has status %v on a call that succeeded; Ok is the host's to set", span.Name, span.Status.Code)
		}
	}
	if operationSpan.SpanKind != oteltrace.SpanKindInternal || requestSpan.SpanKind != oteltrace.SpanKindClient {
		t.Errorf("kinds = %v and %v, want Internal for the operation and Client for the request",
			operationSpan.SpanKind, requestSpan.SpanKind)
	}

	attributes := attributesOf(requestSpan)
	for key, want := range map[string]string{
		"gen_ai.operation.name": "chat",
		"gen_ai.provider.name":  "local",
		"schemaflux.operation":  "extract",
	} {
		if attributes[key] != want {
			t.Errorf("%s = %q, want %q", key, attributes[key], want)
		}
	}
	for _, key := range []string{"gen_ai.request.model", "schemaflux.schema.name", "schemaflux.request_id"} {
		if attributes[key] == "" {
			t.Errorf("%s is missing from the request span", key)
		}
	}
}

// A host that installs nothing gets no spans: not on a provider it built and
// never handed over, and not on the process-wide one, which a library that
// went looking for a telemetry stack would find. The global provider here
// records everything, so a span the library made on it would be seen.
func TestWithNothingInstalledAnOperationMakesNoSpanAnywhere(t *testing.T) {
	if telemetry.HasObserver() {
		t.Fatal("an observer is installed before the test began")
	}
	global, globalExporter := hostProvider(t)
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(global)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	schemafluxtest.Install(t, schemafluxtest.New().Reply(`{"number":"INV-2","vendor":"Acme"}`))
	ctx, stage := global.Tracer("host/pipeline").Start(context.Background(), "stage.parse-invoice")
	_, err := schemaflux.Extract[telemetryInvoice]("Invoice INV-2 from Acme",
		schemaflux.ExtractOptions{CommonOptions: schemaflux.CommonOptions{Context: ctx}})
	stage.End()
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	spans := globalExporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "stage.parse-invoice" {
		t.Errorf("the global provider received %d spans, want only the host's own: "+
			"the library found a telemetry stack nobody gave it", len(spans))
	}
	if telemetry.HasObserver() {
		t.Error("running an operation installed an observer")
	}
}

// After the restore Install returned has run, the library is as it was: an
// operation makes no span on the provider it had been given, and the provider
// is still usable by the host, since the library shut nothing down.
func TestAfterTheRestoreAnOperationMakesNoSpan(t *testing.T) {
	provider, exporter := hostProvider(t)
	restore, err := fluxotel.Install(provider)
	if err != nil {
		t.Fatal(err)
	}
	schemafluxtest.Install(t, schemafluxtest.New().Reply(`{"number":"INV-3","vendor":"Acme"}`, `{"number":"INV-3","vendor":"Acme"}`))

	if _, err := schemaflux.Extract[telemetryInvoice]("Invoice INV-3 from Acme", schemaflux.ExtractOptions{}); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(exporter.GetSpans()) == 0 {
		t.Fatal("no span while installed, so the check below would show nothing")
	}
	exporter.Reset()

	restore()
	if _, err := schemaflux.Extract[telemetryInvoice]("Invoice INV-3 from Acme", schemaflux.ExtractOptions{}); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if spans := exporter.GetSpans(); len(spans) != 0 {
		t.Errorf("%d spans were made after the restore", len(spans))
	}

	_, host := provider.Tracer("host").Start(context.Background(), "the host's own")
	host.End()
	if spans := exporter.GetSpans(); len(spans) != 1 {
		t.Errorf("the host's provider exported %d spans after the restore, want its own one: the library must not shut it down", len(spans))
	}
}
