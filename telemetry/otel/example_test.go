package otel_test

import (
	"context"
	"fmt"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/schemafluxtest"
	fluxotel "github.com/monstercameron/schemaflux/telemetry/otel"
)

// ticket is what the example extracts.
type ticket struct {
	Queue    string `json:"queue"`
	Priority string `json:"priority"`
}

// Example sets tracing up and reads the trace of one extraction whose first
// answer was refused and whose repair worked.
func Example() {
	// The host's own OpenTelemetry SDK. Here it exports to memory; a real host
	// builds its exporter, sampler and resource the way OpenTelemetry
	// documents, and shuts the provider down when it exits.
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = provider.Shutdown(context.Background()) }()

	// The one call. From here on the library's operations produce spans on
	// that provider; restore undoes it.
	restore, err := fluxotel.Install(provider)
	if err != nil {
		fmt.Println("install failed:", err)
		return
	}
	defer restore()

	// A model that answers in-process: first with something unusable, then
	// with the answer. No key, no network.
	model := schemafluxtest.New().Reply(
		"I could not produce JSON.",
		`{"queue":"billing","priority":"high"}`,
	)
	schemaflux.NewClient("example-key").WithProviderInstance(model)

	// The host's own span, and the call made on its context.
	ctx, span := provider.Tracer("example").Start(context.Background(), "handle ticket")
	options := schemaflux.NewExtractOptions()
	options.CommonOptions.Context = ctx
	extracted, err := schemaflux.Extract[ticket]("Customer was charged twice for the March invoice.", options)
	span.End()
	if err != nil {
		fmt.Println("extraction failed:", err)
		return
	}
	fmt.Println("queue:", extracted.Queue)

	// What was exported, as a tree: each span under its parent, and the spans
	// of one parent in the order they ended.
	spans := exporter.GetSpans()
	children := map[oteltrace.SpanID][]tracetest.SpanStub{}
	for _, exported := range spans {
		children[exported.Parent.SpanID()] = append(children[exported.Parent.SpanID()], exported)
	}
	var print func(parent oteltrace.SpanID, indent string)
	print = func(parent oteltrace.SpanID, indent string) {
		for _, exported := range children[parent] {
			line := fmt.Sprintf("%s%s (%s)", indent, exported.Name, exported.SpanKind)
			for _, attribute := range exported.Attributes {
				if attribute.Key == "schemaflux.repair" {
					line += ", a repair"
				}
			}
			fmt.Println(line)
			print(exported.SpanContext.SpanID(), indent+"  ")
		}
	}
	print(oteltrace.SpanID{}, "")

	// Output:
	// queue: billing
	// handle ticket (internal)
	//   schemaflux.extract (internal)
	//     chat local-mock (client)
	//     chat local-mock (client), a repair
}
