package otel_test

import (
	"context"
	"fmt"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/monstercameron/schemaflux/internal/types"
	callpath "github.com/monstercameron/schemaflux/telemetry"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
	fluxotel "github.com/monstercameron/schemaflux/v2/telemetry/otel"
)

// Install sets tracing up and reads the trace of one extraction whose first
// answer was refused and whose repair worked.
//
// The extraction here is made by hand, with the events the library's call path
// emits, because the client that makes the call through the root does not exist
// yet: see the end of this file for what replaces it.
func ExampleInstall() {
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

	// A model that answers in-process: first with something unusable, then with
	// the answer. No key, no network.
	var model schemafluxtest.Provider
	model.Reply(
		"I could not produce JSON.",
		`{"queue":"billing","priority":"high"}`,
	)

	// The host's own span, and the call made on its context.
	ctx, span := provider.Tracer("example").Start(context.Background(), "handle ticket")
	answer := extractTicket(ctx, &model)
	span.End()
	fmt.Println("answer:", answer)

	// What was exported, as a tree: each span under its parent, and the spans of
	// one parent in the order they ended.
	children := map[oteltrace.SpanID][]tracetest.SpanStub{}
	for _, exported := range exporter.GetSpans() {
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
	// answer: {"queue":"billing","priority":"high"}
	// handle ticket (internal)
	//   schemaflux.extract (internal)
	//     chat test-model (client)
	//     chat test-model (client), a repair
}

// New returns the Observer alone, for a host that registers it itself, beside an
// observer of its own. Install does this and installs the id readers as well.
func ExampleNew() {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = provider.Shutdown(context.Background()) }()

	observer, err := fluxotel.New(provider)
	if err != nil {
		fmt.Println("no observer:", err)
		return
	}
	defer telemetry.RegisterObserver(observer)()

	// A call of an operation whose answer was refused, made by hand as in
	// ExampleInstall.
	ctx := callpath.ObserveOperationStart(context.Background(), callpath.OperationEvent{Scope: callpath.ScopeOperation, Operation: "sort"})
	callpath.ObserveOperationEnd(ctx, callpath.OperationResult{
		Scope: callpath.ScopeOperation, Operation: "sort", ErrorKind: types.KindSchemaViolation.String(),
	})

	for _, exported := range exporter.GetSpans() {
		fmt.Printf("%s: status %s, %q\n", exported.Name, exported.Status.Code, exported.Status.Description)
	}

	// A nil provider is refused, not replaced by the global one.
	_, err = fluxotel.New(nil)
	fmt.Println(err)

	// Output:
	// schemaflux.sort: status Error, "invalid answer"
	// otel: no TracerProvider; pass the one your application configured
}

// InstallIDSources lets the library read the trace and span ids of the span a
// call runs in, which Install does as well as registering the Observer. A
// program that wants request ids to be trace ids sets requestid.StrategyTrace.
func ExampleInstallIDSources() {
	provider := sdktrace.NewTracerProvider()
	defer func() { _ = provider.Shutdown(context.Background()) }()
	ctx, span := provider.Tracer("example").Start(context.Background(), "handle ticket")
	defer span.End()

	fmt.Println("before:", telemetry.TraceID(ctx) == "")
	restore := fluxotel.InstallIDSources()
	fmt.Println("installed:", telemetry.TraceID(ctx) == span.SpanContext().TraceID().String(), telemetry.SpanID(ctx) == span.SpanContext().SpanID().String())
	restore()
	fmt.Println("restored:", telemetry.TraceID(ctx) == "")

	// Output:
	// before: true
	// installed: true true
	// restored: true
}

// extractTicket stands in for schemaflux.Extract[Ticket](ctx, text): one call of
// an operation that asks the model, has the answer refused, and asks again. It
// emits what the library's call path emits for such a call, an operation unit
// around two request units, and returns the answer it kept.
func extractTicket(ctx context.Context, model *schemafluxtest.Provider) string {
	operation := callpath.ObserveOperationStart(ctx, callpath.OperationEvent{Scope: callpath.ScopeOperation, Operation: "extract"})
	defer func() {
		callpath.ObserveOperationEnd(operation, callpath.OperationResult{Scope: callpath.ScopeOperation, Operation: "extract"})
	}()

	var answer string
	for attempt := range 2 {
		request := callpath.ObserveOperationStart(operation, callpath.OperationEvent{
			Operation: "extract", Provider: model.Name(), Model: "test-model", SchemaName: "Ticket", ResponseFormat: "json",
			Repair: attempt > 0,
		})
		resp, err := model.Complete(request, schemaflux.Request{Model: "test-model", SchemaName: "Ticket", Format: "json"})
		callpath.ObserveOperationEnd(request, callpath.OperationResult{
			Attempts: 1, Provider: resp.Provider, Model: "test-model", FinishReason: "stop",
			Usage: types.TokenUsage{PromptTokens: resp.Usage.Input, CompletionTokens: resp.Usage.Output},
		})
		if err == nil && len(resp.Content) > 0 && resp.Content[0] == '{' {
			answer = resp.Content
			break
		}
	}
	return answer
}

// END-TO-END EXAMPLE, to be added with the client.
//
// The example above makes its call by hand because schemaflux.Client and
// schemaflux.WithClient are not written. When they and one operation exist, this
// is the example that replaces extractTicket, the callpath import and the
// internal/types import, and keeps the rest unchanged, including its output:
//
//	type Ticket struct {
//		Queue    string `json:"queue"`
//		Priority string `json:"priority"`
//	}
//
//	var model schemafluxtest.Provider
//	model.Reply("I could not produce JSON.", `{"queue":"billing","priority":"high"}`)
//	ctx = schemaflux.WithClient(ctx, schemaflux.Client{Provider: &model})
//	ticket, err := schemaflux.Extract[Ticket](ctx, "Customer was charged twice for the March invoice.")
//
// under the host's "handle ticket" span, printing the same tree. It is the one
// place in this package that shows a call made through the root; the
// test that goes with it, over every function of the root that asks a model,
// holds that each has an operation span of its own (the claim the package
// comment makes about operations).
