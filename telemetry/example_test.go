package telemetry_test

import (
	"context"
	"fmt"
	"time"

	callpath "github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// printer is an Observer that writes each unit as it starts and finishes, with
// a line for each note. A real observer would open a span or write a log line.
type printer struct{}

func (printer) Started(ctx context.Context, e telemetry.Event) context.Context {
	switch e.Scope {
	case telemetry.ScopeOperation:
		fmt.Printf("%s started\n", e.Operation)
	default:
		fmt.Printf("  request to %s/%s started (repair: %v)\n", e.Provider, e.Model, e.Repair)
	}
	return ctx
}

func (printer) Finished(ctx context.Context, r telemetry.Result) {
	how := "ok"
	if r.Kind != 0 {
		how = "failed: " + r.Kind.Error()
	}
	switch r.Scope {
	case telemetry.ScopeOperation:
		fmt.Printf("%s finished, %s\n", r.Operation, how)
	default:
		fmt.Printf("  request finished after %d attempts, %d tokens, $%.4f, %s\n", r.Attempts, r.Usage.Total, r.Cost.Total, how)
	}
}

func (printer) Noted(ctx context.Context, n telemetry.Note) {
	fmt.Printf("  %s of attempt %d (%s), then wait %v\n", n.Name, n.Attempt, n.Kind, n.Wait)
}

// RegisterObserver registers an observer that is told when each call of an
// operation and each request it makes starts and finishes. The call here is made
// by hand with the events the library's call path emits, because the client that
// makes it through the root does not exist yet: see the end of this file.
func ExampleRegisterObserver() {
	restore := telemetry.RegisterObserver(printer{})
	defer restore()

	// One call of an operation that makes one request, which is retried once.
	ctx := callpath.ObserveOperationStart(context.Background(), callpath.OperationEvent{
		Scope: callpath.ScopeOperation, Operation: "extract",
	})
	request := callpath.ObserveOperationStart(ctx, callpath.OperationEvent{
		Operation: "extract", Provider: "openai", Model: "gpt-5",
	})
	callpath.ObserveOperationNote(request, callpath.Note{
		Name: callpath.NoteRetry, Attempt: 1, ErrorKind: types.KindRateLimited.String(), Wait: 250 * time.Millisecond,
	})
	callpath.ObserveOperationEnd(request, callpath.OperationResult{
		Attempts: 2, Provider: "openai", Model: "gpt-5",
		Usage: types.TokenUsage{PromptTokens: 100, CompletionTokens: 30, TotalTokens: 130},
		Cost:  types.CostInfo{TotalCost: 0.0042, Currency: "USD", Priced: true},
	})
	callpath.ObserveOperationEnd(ctx, callpath.OperationResult{Scope: callpath.ScopeOperation, Operation: "extract"})

	// Output:
	// extract started
	//   request to openai/gpt-5 started (repair: false)
	//   retry of attempt 1 (rate limited), then wait 250ms
	//   request finished after 2 attempts, 130 tokens, $0.0042, ok
	// extract finished, ok
}

// A call that fails says how in the Kind of the operation's Result, which is the
// kind of the Error the caller of the operation holds. The request that was
// answered and then refused finished without a failure.
func ExampleRegisterObserver_failure() {
	defer telemetry.RegisterObserver(printer{})()

	ctx := callpath.ObserveOperationStart(context.Background(), callpath.OperationEvent{
		Scope: callpath.ScopeOperation, Operation: "classify",
	})
	request := callpath.ObserveOperationStart(ctx, callpath.OperationEvent{
		Operation: "classify", Provider: "openai", Model: "gpt-5",
	})
	callpath.ObserveOperationEnd(request, callpath.OperationResult{Attempts: 1, Provider: "openai", Model: "gpt-5"})
	callpath.ObserveOperationEnd(ctx, callpath.OperationResult{
		Scope: callpath.ScopeOperation, Operation: "classify", ErrorKind: types.KindSchemaViolation.String(),
	})

	// Output:
	// classify started
	//   request to openai/gpt-5 started (repair: false)
	//   request finished after 1 attempts, 0 tokens, $0.0000, ok
	// classify finished, failed: invalid answer
}

// logged is a Sink that prints the observations of one metric.
type logged struct{ name string }

func (l logged) Record(m telemetry.Metric) {
	if m.Name == l.name {
		fmt.Printf("%s %s=%s\n", m.Name, "outcome", m.Tags["outcome"])
	}
}

// RegisterSink registers a sink for the library's own metrics. They are recorded
// while a sink is registered, and Snapshots reads what they add up to.
func ExampleRegisterSink() {
	callpath.ResetMetrics()
	remove := telemetry.RegisterSink(logged{name: "llm_requests"})
	defer remove()

	// The call path records one count on llm_requests for each request, tagged
	// with how it ended.
	callpath.RecordLLMMetrics(&types.ResultMetadata{
		Provider: "openai", Model: "gpt-5", Mode: types.Strict, Intelligence: types.Fast, Duration: 1200 * time.Millisecond,
		TokenUsage: &types.TokenUsage{PromptTokens: 100, CompletionTokens: 30, TotalTokens: 130},
	})
	callpath.RecordLLMFailure("openai", "gpt-5", types.Strict, types.Fast, types.KindRateLimited.String())

	for _, s := range telemetry.Snapshots() {
		if s.Name == "llm_tokens_total" {
			fmt.Printf("%s: %d observation, %.0f in all, tags %v\n", s.Name, s.Count, s.Sum, s.Tags)
		}
	}
	callpath.ResetMetrics()

	// Output:
	// llm_requests outcome=ok
	// llm_requests outcome=rate limited
	// llm_tokens_total: 1 observation, 130 in all, tags map[intelligence:fast mode:strict model:gpt-5 provider:openai]
}

// SetSpanIDSources lets the library read the trace and span ids of whatever span
// is on a context, which telemetry/otel's Install does for OpenTelemetry.
func ExampleSetSpanIDSources() {
	type spanKey struct{}
	trace := func(ctx context.Context) string {
		if id, ok := ctx.Value(spanKey{}).(string); ok {
			return "trace-of-" + id
		}
		return ""
	}
	restore := telemetry.SetSpanIDSources(trace, nil)
	defer restore()

	ctx := context.WithValue(context.Background(), spanKey{}, "checkout")
	fmt.Printf("%q %q %q\n", telemetry.TraceID(ctx), telemetry.SpanID(ctx), telemetry.TraceID(context.Background()))

	// Output:
	// "trace-of-checkout" "" ""
}

// END-TO-END EXAMPLE, to be added with the first operation of the root.
//
// ExampleRegisterObserver makes its call by hand because the root has a client
// (schemaflux.WithClient) and no operation yet. When schemaflux.Extract exists,
// this is the example that replaces the hand-made events, keeping the printer
// and the output:
//
//	var model schemafluxtest.Provider
//	model.FailThen(1, schemaflux.ErrRateLimited, `{"queue":"billing"}`)
//	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model, Retries: 1})
//	_, err := schemaflux.Extract[Ticket](ctx, "Customer was charged twice.")
//
// with the observer registered, printing an operation that wraps one request,
// the retry of that request as a note, and the operation's finish. The test that
// goes with it holds, over every function of the root that asks a model, that
// the operation's Started comes before its requests' and its Finished after, and
// that its Result carries the Kind of the Error the call returned.
