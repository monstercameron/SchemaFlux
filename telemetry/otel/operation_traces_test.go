package otel_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/mw"
	"github.com/monstercameron/schemaflux/telemetry"
	fluxotel "github.com/monstercameron/schemaflux/telemetry/otel"
)

// What a trace of one operation looks like: the operation span, its request
// spans, and where a retry, a repair, a hedge, a cancellation and a panic show.

// traced installs the Observer on an in-memory provider for one test and
// returns the provider, for the caller's own span, and the exporter.
func traced(t *testing.T) (*sdktrace.TracerProvider, *tracetest.InMemoryExporter) {
	t.Helper()
	provider, exporter := inMemory(t, sdktrace.AlwaysSample())
	restore, err := fluxotel.Install(provider)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	t.Cleanup(restore)
	return provider, exporter
}

// underCaller runs call on a context that carries a span named "caller", ends
// that span, and returns everything exported.
func underCaller(provider *sdktrace.TracerProvider, exporter *tracetest.InMemoryExporter, call func(ctx context.Context)) tracetest.SpanStubs {
	ctx, caller := provider.Tracer("host").Start(context.Background(), "caller")
	call(ctx)
	caller.End()
	spans := exporter.GetSpans()
	exporter.Reset()
	return spans
}

func extractOptions(ctx context.Context) ops.ExtractOptions {
	options := ops.NewExtractOptions()
	options.CommonOptions.Context = ctx
	return options
}

// named returns the spans with the name, in the order they started. Spans the
// clock cannot tell apart keep the order they ended in, which for requests
// made one after another is the same order.
func named(spans tracetest.SpanStubs, name string) tracetest.SpanStubs {
	var found tracetest.SpanStubs
	for _, span := range spans {
		if span.Name == name {
			found = append(found, span)
		}
	}
	slices.SortStableFunc(found, func(a, b tracetest.SpanStub) int { return a.StartTime.Compare(b.StartTime) })
	return found
}

// requests returns the request spans: the ones named by the convention.
func requests(spans tracetest.SpanStubs) tracetest.SpanStubs {
	var found tracetest.SpanStubs
	for _, span := range spans {
		if strings.HasPrefix(span.Name, "chat") {
			found = append(found, span)
		}
	}
	slices.SortStableFunc(found, func(a, b tracetest.SpanStub) int { return a.StartTime.Compare(b.StartTime) })
	return found
}

func attributeOf(span tracetest.SpanStub, key string) (attribute.Value, bool) {
	for _, kv := range span.Attributes {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

// describe renders spans as text, each under its parent and the spans of one
// parent in the order they ended: name, kind, parent, status and every
// attribute and event with its type.
func describe(spans tracetest.SpanStubs) string {
	names := map[oteltrace.SpanID]string{}
	children := map[oteltrace.SpanID][]tracetest.SpanStub{}
	for _, span := range spans {
		names[span.SpanContext.SpanID()] = span.Name
	}
	for _, span := range spans {
		parent := span.Parent.SpanID()
		if _, exported := names[parent]; !exported {
			parent = oteltrace.SpanID{}
		}
		children[parent] = append(children[parent], span)
	}

	var out strings.Builder
	var walk func(parent oteltrace.SpanID, indent string)
	walk = func(parent oteltrace.SpanID, indent string) {
		for _, span := range children[parent] {
			parentName := "none"
			if span.Parent.IsValid() {
				parentName = names[span.Parent.SpanID()]
			}
			fmt.Fprintf(&out, "%s%s  kind=%s parent=%s status=%s description=%q\n",
				indent, span.Name, span.SpanKind, parentName, span.Status.Code, span.Status.Description)
			attributes := slices.Clone(span.Attributes)
			slices.SortFunc(attributes, func(a, b attribute.KeyValue) int { return strings.Compare(string(a.Key), string(b.Key)) })
			for _, kv := range attributes {
				fmt.Fprintf(&out, "%s    %-40s %-11s %s\n", indent, kv.Key, kv.Value.Type(), kv.Value.Emit())
			}
			for _, event := range span.Events {
				fmt.Fprintf(&out, "%s    event %q\n", indent, event.Name)
				for _, kv := range event.Attributes {
					fmt.Fprintf(&out, "%s        %-36s %-11s %s\n", indent, kv.Key, kv.Value.Type(), kv.Value.Emit())
				}
			}
			walk(span.SpanContext.SpanID(), indent+"  ")
		}
	}
	walk(oteltrace.SpanID{}, "")
	return out.String()
}

func TestAnExtractionThatSucceedsIsAnOperationSpanOverOneRequestSpan(t *testing.T) {
	provider, exporter := traced(t)
	useProvider(t, &scriptedProvider{answer: answering(invoiceAnswer)})

	spans := underCaller(provider, exporter, func(ctx context.Context) {
		if _, err := ops.Extract[invoice]("Invoice INV-1 from Acme", extractOptions(ctx)); err != nil {
			t.Fatalf("Extract: %v", err)
		}
	})
	t.Logf("an Extract that succeeds:\n%s", describe(spans))

	if len(spans) != 3 {
		t.Fatalf("%d spans exported, want the caller's, the operation's and the request's", len(spans))
	}
	caller, operation, request := named(spans, "caller")[0], named(spans, "schemaflux.extract")[0], requests(spans)[0]

	if operation.SpanKind != oteltrace.SpanKindInternal || operation.Parent.SpanID() != caller.SpanContext.SpanID() {
		t.Errorf("the operation span is kind %v under %s, want Internal under the caller", operation.SpanKind, operation.Parent.SpanID())
	}
	if request.SpanKind != oteltrace.SpanKindClient || request.Parent.SpanID() != operation.SpanContext.SpanID() {
		t.Errorf("the request span is kind %v under %s, want Client under the operation span", request.SpanKind, request.Parent.SpanID())
	}
	if request.Name != "chat local-mock" {
		t.Errorf("the request span is named %q, want \"chat <model>\"", request.Name)
	}
	for _, span := range []tracetest.SpanStub{operation, request} {
		if span.Status.Code != codes.Unset {
			t.Errorf("%q has status %v, want Unset on success", span.Name, span.Status.Code)
		}
		if span.EndTime.IsZero() || span.EndTime.Before(span.StartTime) {
			t.Errorf("%q was not ended", span.Name)
		}
	}
	for key, want := range map[string]string{
		"gen_ai.operation.name":        "chat",
		"gen_ai.provider.name":         "local",
		"gen_ai.request.model":         "local-mock",
		"gen_ai.response.model":        "local-mock",
		"gen_ai.output.type":           "json",
		"gen_ai.usage.input_tokens":    "11",
		"gen_ai.usage.output_tokens":   "7",
		"schemaflux.operation":         "extract",
		"schemaflux.schema.name":       "invoice",
		"schemaflux.attempts":          "1",
		"schemaflux.cost.priced":       "false",
		"schemaflux.response.provider": "local",
	} {
		if got, ok := attributeOf(request, key); !ok || got.Emit() != want {
			t.Errorf("request attribute %s = %q (set %v), want %q", key, got.Emit(), ok, want)
		}
	}
	for _, key := range []string{"schemaflux.repair", "schemaflux.stream", "error.type", "schemaflux.cost.total"} {
		if got, ok := attributeOf(request, key); ok {
			t.Errorf("request attribute %s = %s is set on a plain successful request", key, got.Emit())
		}
	}
}

// The finding this scope exists for: an extraction whose answer is refused
// used to export nothing but successful request spans. The operation span is
// where the refusal shows, and the repair is a second request under it.
func TestAnExtractionRefusedAfterARepairIsAnErrorOnTheOperationSpan(t *testing.T) {
	provider, exporter := traced(t)
	useProvider(t, &scriptedProvider{answer: answering("this is not JSON at all")})

	var extractErr error
	spans := underCaller(provider, exporter, func(ctx context.Context) {
		_, extractErr = ops.Extract[invoice]("Invoice INV-1 from Acme", extractOptions(ctx))
	})
	t.Logf("an Extract refused after a repair (error kind %q):\n%s", types.KindOf(extractErr), describe(spans))

	if types.KindOf(extractErr) != types.KindRepairExhausted {
		t.Fatalf("Extract returned %v, want a repair-exhausted error", extractErr)
	}
	operation := named(spans, "schemaflux.extract")
	made := requests(spans)
	if len(operation) != 1 || len(made) != 2 {
		t.Fatalf("%d operation spans and %d request spans, want 1 and 2", len(operation), len(made))
	}
	if operation[0].Status.Code != codes.Error || operation[0].Status.Description != "repair exhausted" {
		t.Errorf("the operation span has status %v %q, want Error \"repair exhausted\"",
			operation[0].Status.Code, operation[0].Status.Description)
	}
	if got, _ := attributeOf(operation[0], "error.type"); got.AsString() != "repair exhausted" {
		t.Errorf("the operation span's error.type = %q", got.AsString())
	}
	for index, request := range made {
		if request.Parent.SpanID() != operation[0].SpanContext.SpanID() {
			t.Errorf("request %d is not a child of the operation span", index)
		}
		if request.Status.Code != codes.Unset {
			t.Errorf("request %d has status %v: the request itself got an answer", index, request.Status.Code)
		}
		_, repair := attributeOf(request, "schemaflux.repair")
		if repair != (index == 1) {
			t.Errorf("request %d marked as a repair = %v, want %v", index, repair, index == 1)
		}
	}
}

func TestAnExtractionRepairedOnceEndsWithoutAnError(t *testing.T) {
	provider, exporter := traced(t)
	useProvider(t, &scriptedProvider{answer: func(ctx context.Context, call int, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		if call == 0 {
			return answering(`{"number": 5, "vendor": ["Acme"]}`)(ctx, call, req)
		}
		return answering(invoiceAnswer)(ctx, call, req)
	}})

	spans := underCaller(provider, exporter, func(ctx context.Context) {
		if _, err := ops.Extract[invoice]("Invoice INV-1 from Acme", extractOptions(ctx)); err != nil {
			t.Fatalf("Extract: %v", err)
		}
	})

	operation, made := named(spans, "schemaflux.extract"), requests(spans)
	if len(operation) != 1 || len(made) != 2 {
		t.Fatalf("%d operation spans and %d request spans, want 1 and 2:\n%s", len(operation), len(made), describe(spans))
	}
	if operation[0].Status.Code != codes.Unset {
		t.Errorf("the operation span has status %v after a repair that worked", operation[0].Status.Code)
	}
	if _, first := attributeOf(made[0], "schemaflux.repair"); first {
		t.Error("the first request is marked as a repair")
	}
	if _, second := attributeOf(made[1], "schemaflux.repair"); !second {
		t.Error("the second request is not marked as a repair, so it reads as a duplicate of the first")
	}
}

// The Result form wraps the plain form, which runs through the operation
// runner. All three would start the operation; it is one span.
func TestTheResultFormOfAnOperationIsOneOperationSpan(t *testing.T) {
	provider, exporter := traced(t)
	script := &scriptedProvider{answer: answering(invoiceAnswer)}
	useProvider(t, script)

	cases := []struct {
		name      string
		operation string
		answer    string
		call      func(ctx context.Context) error
	}{
		{"ExtractResult", "extract", invoiceAnswer, func(ctx context.Context) error {
			_, err := ops.ExtractResult[invoice]("Invoice INV-1 from Acme", extractOptions(ctx))
			return err
		}},
		{"SummarizeResult", "summarize", `{"text":"A short summary.","confidence":0.9}`, func(ctx context.Context) error {
			options := ops.NewSummarizeOptions()
			options.CommonOptions.Context = ctx
			_, err := ops.SummarizeResult("A long text about invoices.", options)
			return err
		}},
		{"GenerateResult", "generate", "A generated line.", func(ctx context.Context) error {
			options := ops.NewGenerateOptions()
			options.CommonOptions.Context = ctx
			_, err := ops.GenerateResult[string]("Write one line.", options)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script.answer = answering(tc.answer)
			var callErr error
			spans := underCaller(provider, exporter, func(ctx context.Context) { callErr = tc.call(ctx) })

			operation := named(spans, "schemaflux."+tc.operation)
			if len(operation) != 1 {
				t.Fatalf("%d spans named schemaflux.%s, want one (call returned %v):\n%s", len(operation), tc.operation, callErr, describe(spans))
			}
			wantStatus := codes.Unset
			if callErr != nil {
				wantStatus = codes.Error
			}
			if operation[0].Status.Code != wantStatus {
				t.Errorf("operation status = %v with the call returning %v", operation[0].Status.Code, callErr)
			}
			for _, request := range requests(spans) {
				if request.Parent.SpanID() != operation[0].SpanContext.SpanID() {
					t.Errorf("a request span is not a child of the operation span")
				}
				if got, _ := attributeOf(request, "schemaflux.operation"); got.AsString() != tc.operation {
					t.Errorf("the request's schemaflux.operation = %q, want %q", got.AsString(), tc.operation)
				}
			}
			if len(requests(spans)) == 0 {
				t.Error("no request span was exported")
			}
		})
	}
}

// A request made by an operation that has no span of its own is a child of the
// caller's span, and says nothing of an operation rather than naming an empty
// one.
func TestARequestWithNoOperationSpanIsAChildOfTheCaller(t *testing.T) {
	provider, exporter := traced(t)
	// SummarizeWithMetadata is an operation with no span of its own: the plain
	// forms (Summarize) and the Result forms have one.
	useProvider(t, &scriptedProvider{answer: answering(`{"text":"A short summary.","key_points":["invoices"],"confidence":0.9}`)})

	spans := underCaller(provider, exporter, func(ctx context.Context) {
		options := ops.NewSummarizeOptions()
		options.CommonOptions.Context = ctx
		if _, err := ops.SummarizeWithMetadata("A long text about invoices.", options); err != nil {
			t.Fatalf("SummarizeWithMetadata: %v", err)
		}
	})

	made := requests(spans)
	if len(spans) != 2 || len(made) != 1 {
		t.Fatalf("%d spans, %d of them requests; want the caller's and one request:\n%s", len(spans), len(made), describe(spans))
	}
	if made[0].Parent.SpanID() != named(spans, "caller")[0].SpanContext.SpanID() {
		t.Error("the request is not a child of the caller's span")
	}
	if got, ok := attributeOf(made[0], "schemaflux.operation"); ok {
		t.Errorf("schemaflux.operation = %q on a request whose operation did not name itself; an unknown name is left out", got.AsString())
	}
	for _, span := range spans {
		if span.Name == "schemaflux." || span.Name == "schemaflux.llm" {
			t.Errorf("a span is named %q", span.Name)
		}
	}
}

// A retry is one request span with a "retry" event for each attempt that
// failed, carrying the kind of the failure and never its text.
func TestARetryIsAnEventOnTheRequestSpan(t *testing.T) {
	provider, exporter := traced(t)
	script := &scriptedProvider{retries: 2, backoff: time.Millisecond}
	script.answer = func(ctx context.Context, call int, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		switch call {
		case 0:
			return llm.CompletionResponse{}, &llm.RateLimitError{
				APIError:   llm.NewAPIError("local", "m", 429, `{"error":{"message":"slow down, customer 4471"}}`),
				RetryAfter: 5 * time.Millisecond,
			}
		case 1:
			return llm.CompletionResponse{}, llm.NewAPIError("local", "m", 500, `{"error":{"message":"boom"}}`)
		}
		return answering(invoiceAnswer)(ctx, call, req)
	}
	useProvider(t, script)

	spans := underCaller(provider, exporter, func(ctx context.Context) {
		if _, err := ops.Extract[invoice]("Invoice INV-1 from Acme", extractOptions(ctx)); err != nil {
			t.Fatalf("Extract: %v", err)
		}
	})

	made := requests(spans)
	if len(made) != 1 {
		t.Fatalf("%d request spans for one request that was retried, want 1:\n%s", len(made), describe(spans))
	}
	request := made[0]
	if got, _ := attributeOf(request, "schemaflux.attempts"); got.AsInt64() != 3 {
		t.Errorf("schemaflux.attempts = %d, want 3", got.AsInt64())
	}
	if len(request.Events) != 2 {
		t.Fatalf("%d events on the request span, want a retry for each failed attempt:\n%s", len(request.Events), describe(spans))
	}
	for index, want := range []string{"rate limited", "provider unavailable"} {
		event := request.Events[index]
		if event.Name != "retry" {
			t.Errorf("event %d is named %q, want \"retry\"", index, event.Name)
		}
		got := map[string]attribute.Value{}
		for _, kv := range event.Attributes {
			got[string(kv.Key)] = kv.Value
		}
		if got["error.type"].AsString() != want || got["schemaflux.attempt"].AsInt64() != int64(index+1) {
			t.Errorf("retry %d: error.type=%q attempt=%d, want %q and %d",
				index, got["error.type"].AsString(), got["schemaflux.attempt"].AsInt64(), want, index+1)
		}
		if got["schemaflux.backoff_ms"].Type() != attribute.INT64 {
			t.Errorf("retry %d: schemaflux.backoff_ms is %v, want an integer", index, got["schemaflux.backoff_ms"].Type())
		}
		for key, value := range got {
			if strings.Contains(value.Emit(), "4471") || strings.Contains(value.Emit(), "slow down") {
				t.Errorf("retry %d: %s carries the provider's error text: %s", index, key, value.Emit())
			}
		}
	}
	if request.Status.Code != codes.Unset {
		t.Errorf("status = %v for a request that succeeded on its third attempt", request.Status.Code)
	}
}

// A hedged request is an event on the request span, at the moment the second
// copy is sent.
func TestAHedgedCopyIsAnEventOnTheRequestSpan(t *testing.T) {
	provider, exporter := traced(t)
	slowFirst := &scriptedProvider{answer: func(ctx context.Context, call int, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		if call == 0 {
			select {
			case <-ctx.Done():
				return llm.CompletionResponse{}, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		return answering(invoiceAnswer)(ctx, call, req)
	}}
	useProvider(t, mw.Hedge(20*time.Millisecond)(slowFirst))

	spans := underCaller(provider, exporter, func(ctx context.Context) {
		if _, err := ops.Extract[invoice]("Invoice INV-1 from Acme", extractOptions(ctx)); err != nil {
			t.Fatalf("Extract: %v", err)
		}
	})

	made := requests(spans)
	if len(made) != 1 || len(made[0].Events) != 1 {
		t.Fatalf("want one request span with one event:\n%s", describe(spans))
	}
	event := made[0].Events[0]
	if event.Name != "hedge" || len(event.Attributes) != 1 || event.Attributes[0].Key != "schemaflux.attempt" || event.Attributes[0].Value.AsInt64() != 2 {
		t.Errorf("event = %q %v, want \"hedge\" with schemaflux.attempt=2", event.Name, event.Attributes)
	}

	// A request that answers before the hedge delay sends no second copy and has
	// no event.
	useProvider(t, mw.Hedge(5*time.Second)(&scriptedProvider{answer: answering(invoiceAnswer)}))
	spans = underCaller(provider, exporter, func(ctx context.Context) {
		_, _ = ops.Extract[invoice]("Invoice INV-1 from Acme", extractOptions(ctx))
	})
	if made := requests(spans); len(made) != 1 || len(made[0].Events) != 0 {
		t.Errorf("a request that was not hedged has events:\n%s", describe(spans))
	}
}

// A provider that panics: the request and the operation are both finished as
// failures of kind "internal", and the panic goes on to the caller. The request
// used to be exported as a success.
func TestAPanickingProviderIsReportedAsAFailureAndThePanicContinues(t *testing.T) {
	provider, exporter := traced(t)
	useProvider(t, &scriptedProvider{answer: func(context.Context, int, llm.CompletionRequest) (llm.CompletionResponse, error) {
		panic("provider bug while handling invoice INV-1")
	}})

	var recovered any
	spans := underCaller(provider, exporter, func(ctx context.Context) {
		defer func() { recovered = recover() }()
		_, _ = ops.Extract[invoice]("Invoice INV-1 from Acme", extractOptions(ctx))
	})

	if recovered != "provider bug while handling invoice INV-1" {
		t.Fatalf("the caller recovered %v; the panic must reach it with its own value", recovered)
	}
	operation, made := named(spans, "schemaflux.extract"), requests(spans)
	if len(operation) != 1 || len(made) != 1 {
		t.Fatalf("%d operation and %d request spans, want one of each: every span that was started is ended, a panic included:\n%s",
			len(operation), len(made), describe(spans))
	}
	for _, span := range []tracetest.SpanStub{operation[0], made[0]} {
		if span.Status.Code != codes.Error || span.Status.Description != telemetry.ErrorKindInternal {
			t.Errorf("%q has status %v %q, want Error %q", span.Name, span.Status.Code, span.Status.Description, telemetry.ErrorKindInternal)
		}
		if got, _ := attributeOf(span, "error.type"); got.AsString() != telemetry.ErrorKindInternal {
			t.Errorf("%q has error.type %q", span.Name, got.AsString())
		}
	}
	if strings.Contains(describe(spans), "INV-1") {
		t.Errorf("the panic's value reached a span:\n%s", describe(spans))
	}
}

// A cancelled call and an expired deadline are told apart from each other and
// from a provider's failure, on the request span and on the operation span.
func TestCancellationAndDeadlineHaveTheirOwnErrorTypes(t *testing.T) {
	provider, exporter := traced(t)
	script := &scriptedProvider{}
	useProvider(t, script)
	waits := func(ctx context.Context, _ int, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
		<-ctx.Done()
		return llm.CompletionResponse{}, ctx.Err()
	}

	cases := []struct {
		name   string
		answer func(context.Context, int, llm.CompletionRequest) (llm.CompletionResponse, error)
		derive func(ctx context.Context) (context.Context, context.CancelFunc)
		want   string
	}{
		{"cancelled while the provider answers", waits, func(ctx context.Context) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(ctx)
			time.AfterFunc(30*time.Millisecond, cancel)
			return ctx, cancel
		}, "canceled"},
		{"a deadline that passes", waits, func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeout(ctx, 30*time.Millisecond)
		}, "timeout"},
		{"the provider is unavailable", func(context.Context, int, llm.CompletionRequest) (llm.CompletionResponse, error) {
			return llm.CompletionResponse{}, llm.NewAPIError("local", "m", 500, `{"error":{"message":"boom"}}`)
		}, func(ctx context.Context) (context.Context, context.CancelFunc) { return context.WithCancel(ctx) }, "provider unavailable"},
		{"the key is refused", func(context.Context, int, llm.CompletionRequest) (llm.CompletionResponse, error) {
			return llm.CompletionResponse{}, llm.NewAPIError("local", "m", 401, `{"error":{"message":"bad key"}}`)
		}, func(ctx context.Context) (context.Context, context.CancelFunc) { return context.WithCancel(ctx) }, "authentication"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script.answer = tc.answer
			spans := underCaller(provider, exporter, func(ctx context.Context) {
				ctx, cancel := tc.derive(ctx)
				defer cancel()
				if _, err := ops.Extract[invoice]("Invoice INV-1 from Acme", extractOptions(ctx)); err == nil {
					t.Fatal("Extract returned no error")
				}
			})

			operation, made := named(spans, "schemaflux.extract"), requests(spans)
			if len(operation) != 1 || len(made) != 1 {
				t.Fatalf("want one operation and one request span:\n%s", describe(spans))
			}
			for _, span := range []tracetest.SpanStub{operation[0], made[0]} {
				got, _ := attributeOf(span, "error.type")
				if got.AsString() != tc.want || span.Status.Code != codes.Error || span.Status.Description != tc.want {
					t.Errorf("%q: error.type=%q status=%v %q, want %q and Error", span.Name, got.AsString(), span.Status.Code, span.Status.Description, tc.want)
				}
				if len(span.Events) != 0 {
					t.Errorf("%q has %d events; a recorded error would carry its text", span.Name, len(span.Events))
				}
			}
			for _, key := range []string{"gen_ai.response.model", "gen_ai.usage.input_tokens", "schemaflux.cost.priced", "schemaflux.response.provider"} {
				if value, ok := attributeOf(made[0], key); ok {
					t.Errorf("%s = %s on a request no provider answered", key, value.Emit())
				}
			}
		})
	}
}

// A call refused before anything is sent starts no request, and so no request
// span: there was no request.
func TestACallRefusedBeforeAnythingIsSentHasNoRequestSpan(t *testing.T) {
	provider, exporter := traced(t)
	script := &scriptedProvider{answer: answering(invoiceAnswer)}
	useProvider(t, script)

	spans := underCaller(provider, exporter, func(ctx context.Context) {
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := ops.Extract[invoice]("Invoice INV-1 from Acme", extractOptions(ctx)); err == nil {
			t.Fatal("Extract on a cancelled context returned no error")
		}
	})
	if made := requests(spans); len(made) != 0 || script.calls.Load() != 0 {
		t.Errorf("%d request spans and %d provider calls for a call that sent nothing", len(made), script.calls.Load())
	}
	for _, operation := range named(spans, "schemaflux.extract") {
		if operation.Status.Code != codes.Error || operation.Status.Description != "canceled" {
			t.Errorf("the operation span of a call cancelled before it began has status %v %q", operation.Status.Code, operation.Status.Description)
		}
	}
}

// checkTree asserts the shape of what a concurrent operation exported under
// one caller span: every operation span is a child of the caller, every
// request span is a child of an operation span when there are any and of the
// caller when there are none, no span is a child of a sibling, and each
// operation span has the request spans of its own call and no other's.
func checkTree(t *testing.T, spans tracetest.SpanStubs, wantOperations, wantRequests int) {
	t.Helper()
	caller := named(spans, "caller")
	if len(caller) != 1 {
		t.Fatalf("%d caller spans", len(caller))
	}
	callerID := caller[0].SpanContext.SpanID()

	operations := map[oteltrace.SpanID]int{}
	countOperations := 0
	for _, span := range spans {
		if strings.HasPrefix(span.Name, "schemaflux.") {
			countOperations++
			operations[span.SpanContext.SpanID()] = 0
			if span.Parent.SpanID() != callerID {
				t.Errorf("operation span %q has parent %s, want the caller", span.Name, span.Parent.SpanID())
			}
		}
		if span.SpanContext.TraceID() != caller[0].SpanContext.TraceID() {
			t.Errorf("span %q is in a trace of its own", span.Name)
		}
	}
	made := requests(spans)
	for _, request := range made {
		parent := request.Parent.SpanID()
		switch {
		case countOperations == 0:
			if parent != callerID {
				t.Errorf("request span has parent %s, want the caller", parent)
			}
		default:
			if _, ok := operations[parent]; !ok {
				t.Errorf("request span has parent %s, which is no operation span: a sibling or the caller", parent)
			}
			operations[parent]++
		}
	}
	if countOperations != wantOperations || len(made) != wantRequests {
		t.Errorf("%d operation spans and %d request spans, want %d and %d:\n%s", countOperations, len(made), wantOperations, wantRequests, describe(spans))
	}
	if wantOperations > 0 && wantRequests%wantOperations == 0 {
		for id, children := range operations {
			if children != wantRequests/wantOperations {
				t.Errorf("operation span %s has %d request spans, want %d", id, children, wantRequests/wantOperations)
			}
		}
	}
}

func TestOperationsRunAtOnceAreSiblingsUnderTheCaller(t *testing.T) {
	provider, exporter := traced(t)
	script := &scriptedProvider{answer: func(ctx context.Context, call int, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		time.Sleep(2 * time.Millisecond)
		return answering(invoiceAnswer)(ctx, call, req)
	}}
	useProvider(t, script)
	extract := func(ctx context.Context) (invoice, error) {
		return ops.Extract[invoice]("Invoice INV-1 from Acme", extractOptions(ctx))
	}

	t.Run("Gather of three", func(t *testing.T) {
		spans := underCaller(provider, exporter, func(ctx context.Context) {
			if _, err := ops.Gather(ctx, extract, extract, extract); err != nil {
				t.Fatalf("Gather: %v", err)
			}
		})
		checkTree(t, spans, 3, 3)
	})

	t.Run("Each over ten", func(t *testing.T) {
		spans := underCaller(provider, exporter, func(ctx context.Context) {
			_, err := ops.Each(ctx, make([]int, 10), func(ctx context.Context, _ int) (invoice, error) { return extract(ctx) })
			if err != nil {
				t.Fatalf("Each: %v", err)
			}
		})
		checkTree(t, spans, 10, 10)
	})

	t.Run("a batch of six", func(t *testing.T) {
		spans := underCaller(provider, exporter, func(ctx context.Context) {
			inputs := make([]any, 6)
			for index := range inputs {
				inputs[index] = fmt.Sprintf("Invoice %d", index)
			}
			result := ops.ExtractBatch[invoice](ops.NewBatchProcessor(script).WithConcurrency(4), inputs, types.OpOptions{Context: ctx})
			if result.Metadata.Failed != 0 {
				t.Fatalf("%d of the batch failed", result.Metadata.Failed)
			}
		})
		checkTree(t, spans, 6, 6)
	})

	t.Run("Sort over sixty items", func(t *testing.T) {
		// Above fifty items a sort scores each item with a request of its own,
		// several at a time.
		script.answer = answering(`{"rank_score":0.5}`)
		items := make([]string, 60)
		for index := range items {
			items[index] = fmt.Sprintf("item %02d", index)
		}
		spans := underCaller(provider, exporter, func(ctx context.Context) {
			options := ops.NewSortOptions()
			options.Criteria = "alphabetical"
			options.CommonOptions.Context = ctx
			if _, err := ops.Sort(items, options); err != nil {
				t.Fatalf("Sort: %v", err)
			}
		})
		made := requests(spans)
		if len(made) < 50 {
			t.Fatalf("a sort of sixty items made %d requests; the test needs the many it makes at once", len(made))
		}
		// The plain form of Sort has no span of its own today, so its requests
		// are children of the caller; once it has one they are its children.
		checkTree(t, spans, len(named(spans, "schemaflux.sort")), len(made))
	})
}

// With a sampler that drops everything, running real operations writes nothing
// to any span and still ends each one once.
func TestDroppedSpansOfRealOperationsAreWrittenNothingAndEnded(t *testing.T) {
	tally := &tallyProvider{TracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))}
	restore, err := fluxotel.Install(tally)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	useProvider(t, &scriptedProvider{retries: 1, backoff: time.Millisecond, answer: func(ctx context.Context, call int, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		if call%2 == 0 {
			return llm.CompletionResponse{}, llm.NewAPIError("local", "m", 500, "boom")
		}
		return answering(invoiceAnswer)(ctx, call, req)
	}})

	for range 10 {
		if _, err := ops.Extract[invoice]("Invoice", ops.NewExtractOptions()); err != nil {
			t.Fatalf("Extract: %v", err)
		}
	}

	if started, ended := tally.started.Load(), tally.ended.Load(); started != 20 || ended != 20 {
		t.Errorf("%d spans started and %d ended, want 20 and 20 (an operation and a request for each call)", started, ended)
	}
	if twice := tally.endedTwice.Load(); twice != 0 {
		t.Errorf("%d spans were ended twice", twice)
	}
	if writes := tally.onNonRecording.Load(); writes != 0 {
		t.Errorf("%d attributes or events were written to spans the sampler dropped", writes)
	}
}

// The provider is called on the context that carries the request span, so a
// host's own HTTP instrumentation on the client it gave the library nests
// under it.
func TestTheProviderIsCalledInsideTheRequestSpan(t *testing.T) {
	provider, exporter := traced(t)
	var seen oteltrace.SpanContext
	useProvider(t, &scriptedProvider{answer: func(ctx context.Context, call int, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		seen = oteltrace.SpanContextFromContext(ctx)
		return answering(invoiceAnswer)(ctx, call, req)
	}})

	spans := underCaller(provider, exporter, func(ctx context.Context) {
		_, _ = ops.Extract[invoice]("Invoice", extractOptions(ctx))
	})
	if made := requests(spans); len(made) != 1 || !seen.Equal(made[0].SpanContext) {
		t.Errorf("the provider saw span %s, want the request span", seen.SpanID())
	}
}

// An observer that is not this package's, installed beside it. It returns a
// context that is not derived from the one it was given, which is the mistake
// that used to drop the caller's deadline, client and call record.
type unrelatedContextObserver struct{}

func (unrelatedContextObserver) OperationStarted(context.Context, telemetry.OperationEvent) context.Context {
	return context.Background()
}

func (unrelatedContextObserver) OperationFinished(context.Context, telemetry.OperationResult) {}

// A bad observer cannot change what a call does, and a good one beside it
// still gets its trace: the operation span under the caller's, and the request
// span under the operation span. Both orders, since the bad one before the
// good one used to hand it a context with nothing of the caller's in it.
func TestABadObserverBesideTheSpanObserverLeavesTheTraceWhole(t *testing.T) {
	for _, tc := range []struct {
		name string
		// badFirst installs the bad observer before the span observer.
		badFirst bool
	}{
		{"the bad observer installed first", true},
		{"the bad observer installed last", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, exporter := inMemory(t, sdktrace.AlwaysSample())
			if tc.badFirst {
				t.Cleanup(telemetry.AddObserver(unrelatedContextObserver{}))
			}
			restore, err := fluxotel.Install(provider)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(restore)
			if !tc.badFirst {
				t.Cleanup(telemetry.AddObserver(unrelatedContextObserver{}))
			}
			script := &scriptedProvider{answer: func(ctx context.Context, call int, req llm.CompletionRequest) (llm.CompletionResponse, error) {
				select {
				case <-ctx.Done():
					return llm.CompletionResponse{}, ctx.Err()
				case <-time.After(2 * time.Second):
					return answering(invoiceAnswer)(ctx, call, req)
				}
			}}
			useProvider(t, script)

			started := time.Now()
			var callErr error
			var meta types.Meta
			spans := underCaller(provider, exporter, func(ctx context.Context) {
				ctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
				defer cancel()
				result, err := ops.ExtractResult[invoice]("Invoice INV-1 from Acme", extractOptions(ctx))
				callErr, meta = err, result.Meta
			})

			if elapsed := time.Since(started); elapsed > time.Second {
				t.Errorf("the call ran for %v: the caller's 150 ms deadline was dropped", elapsed)
			}
			if types.KindOf(callErr) != types.KindTimeout && !errors.Is(callErr, context.DeadlineExceeded) {
				t.Errorf("the call returned %v, want the deadline's error", callErr)
			}
			if meta.Attempts != 1 {
				t.Errorf("the envelope reports %d attempts, want 1: the call record was dropped", meta.Attempts)
			}

			operation, made := named(spans, "schemaflux.extract"), requests(spans)
			caller := named(spans, "caller")
			if len(operation) != 1 || len(made) != 1 || len(caller) != 1 {
				t.Fatalf("want a caller, an operation and a request span:\n%s", describe(spans))
			}
			if operation[0].Parent.SpanID() != caller[0].SpanContext.SpanID() {
				t.Error("the operation span is not a child of the caller's span")
			}
			if made[0].Parent.SpanID() != operation[0].SpanContext.SpanID() {
				t.Error("the request span is not a child of the operation span")
			}
			if operation[0].Status.Code != codes.Error || operation[0].Status.Description != "timeout" {
				t.Errorf("the operation span has status %v %q, want Error \"timeout\"", operation[0].Status.Code, operation[0].Status.Description)
			}
		})
	}
}

// A plain Extract refused before it reaches the runner is one call of an
// operation like any other, and shows on an operation span with the kind of
// the error it returned. It used to export none, where ExtractResult did.
func TestAnExtractRefusedBeforeItReachesTheRunnerHasAnOperationSpan(t *testing.T) {
	provider, exporter := traced(t)
	script := &scriptedProvider{answer: answering(invoiceAnswer)}
	useProvider(t, script)

	badThreshold := extractOptions(context.Background())
	badThreshold.CommonOptions.Threshold = 7
	unwritableExample := extractOptions(context.Background())
	unwritableExample.Examples = []any{make(chan int)}

	cases := []struct {
		name  string
		input any
		with  func(ctx context.Context) ops.ExtractOptions
		want  string
	}{
		{"a nil input", nil, extractOptions, "invalid request"},
		{"a threshold outside 0 to 1", "Invoice INV-1 from Acme", func(ctx context.Context) ops.ExtractOptions {
			options := badThreshold
			options.CommonOptions.Context = ctx
			return options
		}, "configuration"},
		{"an example that cannot be written as JSON", "Invoice INV-1 from Acme", func(ctx context.Context) ops.ExtractOptions {
			options := unwritableExample
			options.CommonOptions.Context = ctx
			return options
		}, "configuration"},
	}
	for _, tc := range cases {
		for _, form := range []string{"Extract", "ExtractResult"} {
			t.Run(tc.name+" through "+form, func(t *testing.T) {
				var callErr error
				spans := underCaller(provider, exporter, func(ctx context.Context) {
					if form == "Extract" {
						_, callErr = ops.Extract[invoice](tc.input, tc.with(ctx))
					} else {
						_, callErr = ops.ExtractResult[invoice](tc.input, tc.with(ctx))
					}
				})
				if callErr == nil {
					t.Fatal("the call was not refused")
				}

				operation := named(spans, "schemaflux.extract")
				if len(operation) != 1 {
					t.Fatalf("%d operation spans for a refused call, want exactly one (one span, not two nested under one name):\n%s", len(operation), describe(spans))
				}
				if got, _ := attributeOf(operation[0], "error.type"); got.AsString() != tc.want || operation[0].Status.Code != codes.Error || operation[0].Status.Description != tc.want {
					t.Errorf("the operation span has error.type %q and status %v %q, want %q (the call returned a %q error)",
						got.AsString(), operation[0].Status.Code, operation[0].Status.Description, tc.want, types.KindOf(callErr))
				}
				if types.KindOf(callErr).String() != tc.want {
					t.Errorf("the error the call returns is of kind %q, want %q", types.KindOf(callErr), tc.want)
				}
				if made := requests(spans); len(made) != 0 || script.calls.Load() != 0 {
					t.Errorf("%d request spans and %d provider calls for a call refused before anything was sent", len(made), script.calls.Load())
				}
			})
		}
	}
}

// A Result form over a plain form over the runner is one call, and one span,
// on every path: a success, a refusal before the runner, a refusal in it.
func TestOneCallOfExtractIsOneOperationSpanOnEveryPath(t *testing.T) {
	provider, exporter := traced(t)
	script := &scriptedProvider{answer: answering(invoiceAnswer)}
	useProvider(t, script)

	cases := []struct {
		name   string
		answer string
		input  any
	}{
		{"a success", invoiceAnswer, "Invoice INV-1 from Acme"},
		{"a refusal before the runner", invoiceAnswer, nil},
		{"a refusal after repairs", "this is not JSON", "Invoice INV-1 from Acme"},
	}
	for _, tc := range cases {
		for _, form := range []string{"Extract", "ExtractResult"} {
			t.Run(tc.name+" through "+form, func(t *testing.T) {
				script.answer = answering(tc.answer)
				spans := underCaller(provider, exporter, func(ctx context.Context) {
					if form == "Extract" {
						_, _ = ops.Extract[invoice](tc.input, extractOptions(ctx))
					} else {
						_, _ = ops.ExtractResult[invoice](tc.input, extractOptions(ctx))
					}
				})
				if operation := named(spans, "schemaflux.extract"); len(operation) != 1 {
					t.Errorf("%d operation spans, want 1:\n%s", len(operation), describe(spans))
				}
				for _, span := range spans {
					if strings.HasPrefix(span.Name, "schemaflux.") && span.Parent.IsValid() && strings.HasPrefix(spanName(spans, span.Parent.SpanID()), "schemaflux.") {
						t.Errorf("operation span %q is nested under another operation span", span.Name)
					}
				}
			})
		}
	}
}

// spanName returns the name of the exported span with the ID, or "".
func spanName(spans tracetest.SpanStubs, id oteltrace.SpanID) string {
	for _, span := range spans {
		if span.SpanContext.SpanID() == id {
			return span.Name
		}
	}
	return ""
}

// The decisions: Is, Which, WhichOrNone, Rate and Assess, each in its plain
// and its Result form. A Result form is an operation span over the request,
// and the two used to disagree about what the operation was called: the span
// said "is" and the request beneath it "assess", because the shared core named
// the call over the name already on its context. Every span of one call names
// the one operation.
func TestEverySpanOfOneCallNamesTheSameOperation(t *testing.T) {
	provider, exporter := traced(t)
	script := &scriptedProvider{}
	useProvider(t, script)

	type triage struct {
		Spam    bool   `ask:"Is this message spam?"`
		Team    string `ask:"Which team should handle it?" oneof:"billing,technical"`
		Urgency int    `ask:"How urgent is it?" scale:"1-5"`
	}
	labels := []string{"billing", "technical"}
	subject := "My invoice is wrong"

	cases := []struct {
		name string
		// answer is what the provider says. operation is the name the Result
		// form reports; a plain form has no operation span and only has to
		// agree with itself.
		answer    string
		operation string
		result    bool
		call      func(ctx context.Context) error
	}{
		{"Is", `{"q1":true}`, "is", false, func(ctx context.Context) error {
			_, err := ops.Is(ctx, subject, "Is this about billing?")
			return err
		}},
		{"IsResult", `{"q1":true,"confidence":0.9}`, "is", true, func(ctx context.Context) error {
			_, err := ops.IsResult(ctx, subject, "Is this about billing?")
			return err
		}},
		{"Which", `{"q1":1}`, "which", false, func(ctx context.Context) error {
			_, err := ops.Which(ctx, subject, "Which team?", labels)
			return err
		}},
		{"WhichResult", `{"q1":1,"confidence":0.9}`, "which", true, func(ctx context.Context) error {
			_, err := ops.WhichResult(ctx, subject, "Which team?", labels)
			return err
		}},
		{"WhichOrNone", `{"q1":1}`, "which", false, func(ctx context.Context) error {
			_, _, err := ops.WhichOrNone(ctx, subject, "Which team?", labels)
			return err
		}},
		{"WhichOrNoneResult", `{"q1":3,"confidence":0.9}`, "which", true, func(ctx context.Context) error {
			_, err := ops.WhichOrNoneResult(ctx, subject, "Which team?", labels)
			return err
		}},
		{"Rate", `{"q1":3}`, "rate", false, func(ctx context.Context) error {
			_, err := ops.Rate(ctx, subject, "How urgent?", 1, 5)
			return err
		}},
		{"RateResult", `{"q1":3,"confidence":0.9}`, "rate", true, func(ctx context.Context) error {
			_, err := ops.RateResult(ctx, subject, "How urgent?", 1, 5)
			return err
		}},
		{"Assess", `{"q1":true,"q2":1,"q3":3}`, "assess", false, func(ctx context.Context) error {
			_, err := ops.Assess[triage](ctx, subject)
			return err
		}},
		{"AssessResult", `{"q1":true,"q2":1,"q3":3,"confidence":0.9}`, "assess", true, func(ctx context.Context) error {
			_, err := ops.AssessResult[triage](ctx, subject)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script.answer = answering(tc.answer)
			var callErr error
			spans := underCaller(provider, exporter, func(ctx context.Context) { callErr = tc.call(ctx) })
			if callErr != nil {
				t.Fatalf("the call returned %v", callErr)
			}

			names := map[string]string{} // span name -> its schemaflux.operation
			for _, span := range spans {
				if !strings.HasPrefix(span.Name, "schemaflux.") && !strings.HasPrefix(span.Name, "chat") {
					continue
				}
				value, _ := attributeOf(span, "schemaflux.operation")
				names[span.Name+" #"+span.SpanContext.SpanID().String()] = value.AsString()
				if strings.HasPrefix(span.Name, "schemaflux.") {
					if want := strings.TrimPrefix(span.Name, "schemaflux."); value.AsString() != want {
						t.Errorf("the operation span %q says schemaflux.operation=%q", span.Name, value.AsString())
					}
				}
			}
			distinct := map[string]bool{}
			for _, operation := range names {
				distinct[operation] = true
			}
			if len(distinct) != 1 {
				t.Errorf("the spans of one call name %d operations %v:\n%s", len(distinct), distinct, describe(spans))
			}
			if tc.result {
				if operation := named(spans, "schemaflux."+tc.operation); len(operation) != 1 {
					t.Fatalf("%d operation spans named schemaflux.%s, want 1:\n%s", len(operation), tc.operation, describe(spans))
				}
				if !distinct[tc.operation] {
					t.Errorf("the spans of %s name %v, want %q throughout", tc.name, distinct, tc.operation)
				}
			}
			if len(requests(spans)) == 0 {
				t.Error("no request span was exported")
			}
		})
	}
}

// A failure the library itself raises says what it is. A span that reports
// "unknown" has an error nobody classified, which is a gap and says nothing to
// a reader of the trace. This provokes every refusal the exported entry points
// below can produce on their own, and fails on any span that says "unknown".
func TestNoSpanSaysUnknownForAFailureTheLibraryRaised(t *testing.T) {
	provider, exporter := traced(t)
	script := &scriptedProvider{answer: answering(invoiceAnswer)}
	useProvider(t, script)

	type untagged struct {
		Spam bool
	}
	cancelled := func(ctx context.Context) context.Context {
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		return ctx
	}
	answers := func(body string) func() {
		return func() { script.answer = answering(body) }
	}
	empty := func() {
		script.answer = func(ctx context.Context, call int, req llm.CompletionRequest) (llm.CompletionResponse, error) {
			response, _ := answering("")(ctx, call, req)
			return response, nil
		}
	}
	truncated := func() {
		script.answer = func(ctx context.Context, call int, req llm.CompletionRequest) (llm.CompletionResponse, error) {
			response, _ := answering(`{"number":"INV`)(ctx, call, req)
			response.FinishReason = "length"
			return response, nil
		}
	}
	failing := func(status int) func() {
		return func() {
			script.answer = func(context.Context, int, llm.CompletionRequest) (llm.CompletionResponse, error) {
				return llm.CompletionResponse{}, llm.NewAPIError("local", "m", status, `{"error":{"message":"refused"}}`)
			}
		}
	}
	noOp := func() {}
	badOptions := func(ctx context.Context) ops.ExtractOptions {
		options := extractOptions(ctx)
		options.CommonOptions.Threshold = 7
		return options
	}
	exampleOptions := func(ctx context.Context) ops.ExtractOptions {
		options := extractOptions(ctx)
		options.Examples = []any{make(chan int)}
		return options
	}
	const input = "Invoice INV-1 from Acme"

	refusals := []struct {
		name  string
		setup func()
		call  func(ctx context.Context) error
	}{
		{"Extract: a nil input", noOp, func(ctx context.Context) error { _, err := ops.Extract[invoice](nil, extractOptions(ctx)); return err }},
		{"ExtractResult: a nil input", noOp, func(ctx context.Context) error {
			_, err := ops.ExtractResult[invoice](nil, extractOptions(ctx))
			return err
		}},
		{"Extract: a bad option", noOp, func(ctx context.Context) error { _, err := ops.Extract[invoice](input, badOptions(ctx)); return err }},
		{"ExtractResult: a bad option", noOp, func(ctx context.Context) error {
			_, err := ops.ExtractResult[invoice](input, badOptions(ctx))
			return err
		}},
		{"Extract: an example that cannot be written", noOp, func(ctx context.Context) error {
			_, err := ops.Extract[invoice](input, exampleOptions(ctx))
			return err
		}},
		{"ExtractResult: an example that cannot be written", noOp, func(ctx context.Context) error {
			_, err := ops.ExtractResult[invoice](input, exampleOptions(ctx))
			return err
		}},
		{"Extract: a target no answer could satisfy", noOp, func(ctx context.Context) error { _, err := ops.Extract[any](input, extractOptions(ctx)); return err }},
		{"ExtractResult: a target no answer could satisfy", noOp, func(ctx context.Context) error {
			_, err := ops.ExtractResult[any](input, extractOptions(ctx))
			return err
		}},
		{"Extract: a cancelled context", noOp, func(ctx context.Context) error {
			_, err := ops.Extract[invoice](input, extractOptions(cancelled(ctx)))
			return err
		}},
		{"ExtractResult: a cancelled context", noOp, func(ctx context.Context) error {
			_, err := ops.ExtractResult[invoice](input, extractOptions(cancelled(ctx)))
			return err
		}},
		{"Extract: an answer that is not JSON", answers("this is not JSON"), func(ctx context.Context) error {
			_, err := ops.Extract[invoice](input, extractOptions(ctx))
			return err
		}},
		{"ExtractResult: an answer that is not JSON", answers("this is not JSON"), func(ctx context.Context) error {
			_, err := ops.ExtractResult[invoice](input, extractOptions(ctx))
			return err
		}},
		{"Extract: an answer of the wrong shape", answers(`{"number": 5, "vendor": ["Acme"]}`), func(ctx context.Context) error {
			_, err := ops.Extract[invoice](input, extractOptions(ctx))
			return err
		}},
		{"Extract: an empty answer", empty, func(ctx context.Context) error {
			_, err := ops.Extract[invoice](input, extractOptions(ctx))
			return err
		}},
		{"ExtractResult: an empty answer", empty, func(ctx context.Context) error {
			_, err := ops.ExtractResult[invoice](input, extractOptions(ctx))
			return err
		}},
		{"Extract: a truncated answer", truncated, func(ctx context.Context) error {
			_, err := ops.Extract[invoice](input, extractOptions(ctx))
			return err
		}},
		{"Extract: a refused key", failing(401), func(ctx context.Context) error {
			_, err := ops.Extract[invoice](input, extractOptions(ctx))
			return err
		}},
		{"ExtractResult: a provider that is down", failing(500), func(ctx context.Context) error {
			_, err := ops.ExtractResult[invoice](input, extractOptions(ctx))
			return err
		}},
		{"RunOp: no BuildPrompt", noOp, func(ctx context.Context) error {
			op := ops.Op[string, string]{ID: types.OperationID{Name: "custom"}, Contract: ops.OutputContract[string]{Decode: func(body string) (string, error) { return body, nil }}}
			_, _, err := ops.RunOp(ctx, op, input, types.OpOptions{})
			return err
		}},
		{"RunOp: no Decode", noOp, func(ctx context.Context) error {
			op := ops.Op[string, string]{ID: types.OperationID{Name: "custom"}, BuildPrompt: func(in string, _ types.OpOptions) (string, string) { return "system", in }}
			_, _, err := ops.RunOp(ctx, op, input, types.OpOptions{})
			return err
		}},
		{"RunOpResult: no Decode", noOp, func(ctx context.Context) error {
			op := ops.Op[string, string]{ID: types.OperationID{Name: "custom"}, BuildPrompt: func(in string, _ types.OpOptions) (string, string) { return "system", in }}
			_, err := ops.RunOpResult(ctx, op, input, types.OpOptions{})
			return err
		}},
		{"IsResult: a blank question", noOp, func(ctx context.Context) error { _, err := ops.IsResult(ctx, input, " "); return err }},
		{"WhichResult: one label", noOp, func(ctx context.Context) error {
			_, err := ops.WhichResult(ctx, input, "Which?", []string{"only"})
			return err
		}},
		{"RateResult: a scale with no room", noOp, func(ctx context.Context) error { _, err := ops.RateResult(ctx, input, "How?", 3, 3); return err }},
		{"AssessResult: a type with no questions", noOp, func(ctx context.Context) error { _, err := ops.AssessResult[untagged](ctx, input); return err }},
		{"IsResult: a refused key", failing(401), func(ctx context.Context) error { _, err := ops.IsResult(ctx, input, "Is it?"); return err }},
		{"IsResult: an answer that does not answer", answers(`{"q2":true}`), func(ctx context.Context) error {
			_, err := ops.IsResult(ctx, input, "Is it?")
			return err
		}},
		{"SummarizeResult: a provider that is down", failing(500), func(ctx context.Context) error {
			options := ops.NewSummarizeOptions()
			options.CommonOptions.Context = ctx
			_, err := ops.SummarizeResult("A long text.", options)
			return err
		}},
		{"SummarizeResult: a cancelled context", noOp, func(ctx context.Context) error {
			options := ops.NewSummarizeOptions()
			options.CommonOptions.Context = cancelled(ctx)
			_, err := ops.SummarizeResult("A long text.", options)
			return err
		}},
	}

	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			script.answer = answering(invoiceAnswer)
			tc.setup()
			var callErr error
			spans := underCaller(provider, exporter, func(ctx context.Context) { callErr = tc.call(ctx) })
			if callErr == nil {
				t.Fatalf("the call was not refused:\n%s", describe(spans))
			}
			if llm.Classify(callErr) == types.KindUnknown {
				t.Errorf("the error the call returns has no kind, by the library's own reading of it: %v", callErr)
			}
			for _, span := range spans {
				if got, _ := attributeOf(span, "error.type"); got.AsString() == "unknown" || span.Status.Description == "unknown" {
					t.Errorf("span %q says \"unknown\" for an error of kind %q:\n%s", span.Name, types.KindOf(callErr), describe(spans))
				}
			}
		})
	}
}

// A stream whose provider ends it with no result is a failure the library
// names, on the request span.
func TestAStreamThatEndsWithNoResultIsNotUnknown(t *testing.T) {
	provider, exporter := traced(t)
	useProvider(t, &scriptedProvider{stream: func(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
		return func(yield func(llm.StreamChunk, error) bool) {
			yield(llm.StreamChunk{Delta: "piece "}, nil)
		}
	}})

	ctx, caller := provider.Tracer("host").Start(context.Background(), "caller")
	stream, err := ops.StreamSummarize("A long text.", summarizeOptions(ctx))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Text(); err == nil {
		t.Fatal("a stream that ended with no result reported none")
	}
	caller.End()

	made := eventually(t, exporter)
	if len(made) != 1 {
		t.Fatalf("%d request spans:\n%s", len(made), describe(exporter.GetSpans()))
	}
	if got, _ := attributeOf(made[0], "error.type"); got.AsString() == "unknown" || got.AsString() == "" {
		t.Errorf("the request span's error.type = %q for a stream that ended with no result", got.AsString())
	}
}
