package otel

import (
	"context"
	"slices"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/telemetry"
)

// attributeOf returns the value of the attribute with the key.
func attributeOf(span tracetest.SpanStub, key string) (attribute.Value, bool) {
	for _, kv := range span.Attributes {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

// The keys for what a middleware did, and for what became of a run's items, with
// their types: a request that was escalated, one a cache answered, a hedged one,
// an operation that ran over many items.
func TestTheKeysOfEscalationCacheHedgeAndRunsCarryTheirTypes(t *testing.T) {
	observer, exporter := observerOn(t)
	request := telemetry.OperationEvent{Operation: "is", Provider: "openai", Model: "gpt-5.6-luna"}

	escalated := observer.OperationStarted(context.Background(), request)
	observer.OperationFinished(escalated, telemetry.OperationResult{
		Attempts: 2, Provider: "openai", Model: "gpt-5.6-luna", FinishReason: "stop",
		Usage:      types.TokenUsage{PromptTokens: 700, CompletionTokens: 60, TotalTokens: 760},
		AskedFirst: "jev", AskedFirstModel: "jev-1.13.0", Escalated: "below the floor",
	})
	cached := observer.OperationStarted(context.Background(), request)
	observer.OperationFinished(cached, telemetry.OperationResult{Cached: true, FinishReason: "stop"})
	hedged := observer.OperationStarted(context.Background(), request)
	observer.OperationNoted(hedged, telemetry.Note{Name: telemetry.NoteHedge, Attempt: 2})
	observer.OperationFinished(hedged, telemetry.OperationResult{Attempts: 1, Provider: "openai", Model: "gpt-5.6-luna"})
	run := observer.OperationStarted(context.Background(), telemetry.OperationEvent{Scope: telemetry.ScopeOperation, Operation: "extract"})
	observer.OperationFinished(run, telemetry.OperationResult{Scope: telemetry.ScopeOperation, Operation: "extract", Requests: 9, Items: 6, Succeeded: 3, Failed: 2, Cancelled: 1})

	spans := exporter.GetSpans()
	if len(spans) != 4 {
		t.Fatalf("%d spans exported, want 4", len(spans))
	}
	wants := []struct {
		name    string
		got     []string
		present []string
		absent  []string
	}{
		{"an escalated request", typed(spans[0].Attributes), []string{
			"gen_ai.usage.input_tokens INT64 700", "gen_ai.usage.output_tokens INT64 60", "schemaflux.attempts INT64 2",
			"schemaflux.escalation.model STRING jev-1.13.0", "schemaflux.escalation.provider STRING jev",
			"schemaflux.escalation.reason STRING below the floor", "schemaflux.response.provider STRING openai",
		}, []string{"schemaflux.cache.hit", "schemaflux.hedged"}},
		{"a request a cache answered", typed(spans[1].Attributes), []string{
			"gen_ai.usage.input_tokens INT64 0", "gen_ai.usage.output_tokens INT64 0", "schemaflux.attempts INT64 0",
			"schemaflux.cache.hit BOOL true",
		}, []string{"schemaflux.response.provider", "gen_ai.response.model", "schemaflux.cost.priced", "schemaflux.escalation.provider", "schemaflux.hedged"}},
		{"a hedged request", typed(spans[2].Attributes), []string{"schemaflux.hedged BOOL true", "schemaflux.attempts INT64 1"},
			[]string{"schemaflux.cache.hit", "schemaflux.escalation.provider"}},
		{"a run over many items", typed(spans[3].Attributes), []string{
			"schemaflux.items.cancelled INT64 1", "schemaflux.items.failed INT64 2", "schemaflux.items.succeeded INT64 3",
			"schemaflux.items.total INT64 6", "schemaflux.operation STRING extract", "schemaflux.requests INT64 9",
		}, []string{"schemaflux.attempts", "error.type"}},
	}
	for _, want := range wants {
		for _, line := range want.present {
			if !slices.Contains(want.got, line) {
				t.Errorf("%s: %q is missing from %v", want.name, line, want.got)
			}
		}
		for _, key := range want.absent {
			for _, line := range want.got {
				if strings.HasPrefix(line, key+" ") {
					t.Errorf("%s carries %q", want.name, line)
				}
			}
		}
	}
	if len(spans[2].Events) != 1 || spans[2].Events[0].Name != "hedge" {
		t.Errorf("the hedged request's events = %v, want its one hedge", spans[2].Events)
	}
}

// A failed request that was charged for a call asked first still says what was
// used, though no provider answered it.
func TestAFailedRequestThatPaidForACallAskedFirstSaysWhatWasUsed(t *testing.T) {
	observer, exporter := observerOn(t)
	ctx := observer.OperationStarted(context.Background(), telemetry.OperationEvent{Operation: "is", Provider: "openai", Model: "m"})
	observer.OperationFinished(ctx, telemetry.OperationResult{
		Attempts: 2, ErrorKind: "authentication", Usage: types.TokenUsage{PromptTokens: 300, CompletionTokens: 40},
		AskedFirst: "jev", Escalated: "below the floor",
	})
	got := typed(exporter.GetSpans()[0].Attributes)
	for _, line := range []string{"gen_ai.usage.input_tokens INT64 300", "gen_ai.usage.output_tokens INT64 40", "schemaflux.escalation.provider STRING jev"} {
		if !slices.Contains(got, line) {
			t.Errorf("%q is missing from %v", line, got)
		}
	}
	for _, line := range got {
		if strings.HasPrefix(line, "schemaflux.response.provider") || strings.HasPrefix(line, "gen_ai.response.model") {
			t.Errorf("a request nothing answered names who answered: %q", line)
		}
	}
}

// Every attribute key the Observer writes, and every event it adds, is named in
// the package comment, and the comment lists no key the Observer never writes.
func TestThePackageCommentNamesEveryAttributeAndEventTheObserverWrites(t *testing.T) {
	doc := strings.Join(strings.Fields(packageDoc(t)), " ")
	observer, exporter := observerOn(t)

	every := fullEvent
	ctx := observer.OperationStarted(context.Background(), every)
	observer.OperationNoted(ctx, telemetry.Note{Name: telemetry.NoteRetry, Attempt: 1, ErrorKind: "timeout"})
	observer.OperationNoted(ctx, telemetry.Note{Name: telemetry.NoteHedge, Attempt: 2})
	observer.OperationFinished(ctx, fullResult)
	success := observer.OperationStarted(context.Background(), every)
	ok := fullResult
	ok.ErrorKind = ""
	observer.OperationFinished(success, ok)
	unpriced := observer.OperationStarted(context.Background(), every)
	ok.Cost = types.CostInfo{Currency: "USD", Quality: types.PricingUnknown}
	observer.OperationFinished(unpriced, ok)
	cached := observer.OperationStarted(context.Background(), every)
	observer.OperationFinished(cached, telemetry.OperationResult{Cached: true})
	run := observer.OperationStarted(context.Background(), telemetry.OperationEvent{Scope: telemetry.ScopeOperation, Operation: "extract"})
	observer.OperationFinished(run, telemetry.OperationResult{Scope: telemetry.ScopeOperation, Operation: "extract", Requests: 3, Items: 4, Succeeded: 2, Failed: 1, Cancelled: 1, ErrorKind: "internal"})

	keys := map[string]bool{}
	events := map[string]bool{}
	for _, span := range exporter.GetSpans() {
		for _, kv := range span.Attributes {
			keys[string(kv.Key)] = true
		}
		for _, event := range span.Events {
			events[event.Name] = true
			for _, kv := range event.Attributes {
				keys[string(kv.Key)] = true
			}
		}
	}
	if len(keys) < 30 {
		t.Fatalf("only %d keys were written; the test is not looking at every one", len(keys))
	}
	for key := range keys {
		if !strings.Contains(doc, key) {
			t.Errorf("the package comment does not name the attribute %q", key)
		}
	}
	for event := range events {
		if !strings.Contains(doc, `"`+event+`"`) {
			t.Errorf("the package comment does not name the event %q", event)
		}
	}

	// A key the comment lists in a table and the code never writes is as stale.
	rows := 0
	for _, line := range strings.Split(packageDoc(t), "\n") {
		if !strings.HasPrefix(line, "\t") {
			continue
		}
		fields := strings.Fields(line)
		// A row of a table is a key and then its type; the tree of spans and the
		// list of error.type values have no type in the second column.
		if len(fields) < 3 || !slices.Contains([]string{"string", "int", "int64", "bool", "float64", "[]string"}, fields[1]) {
			continue
		}
		rows++
		if !keys[fields[0]] {
			t.Errorf("the package comment lists the attribute %q, which the Observer never writes", fields[0])
		}
	}
	if rows < 30 {
		t.Errorf("only %d rows of the tables were read", rows)
	}
}

// The strings of an escalation come from a provider and a middleware, so they
// reach a span cut and printable like every other string that does.
func TestTheEscalationOfARequestReachesTheSpanBoundedAndPrintable(t *testing.T) {
	long := strings.Repeat("x", 5000)
	observer, exporter := observerOn(t)
	ctx := observer.OperationStarted(context.Background(), telemetry.OperationEvent{Operation: "is", Provider: "p", Model: "m"})
	observer.OperationFinished(ctx, telemetry.OperationResult{
		Attempts: 2, Provider: "p", Model: "m",
		AskedFirst: long, AskedFirstModel: "model\n" + long, Escalated: "below\x00the floor" + long,
	})
	span := exporter.GetSpans()[0]
	for key, limit := range map[string]int{"schemaflux.escalation.provider": 64, "schemaflux.escalation.model": 128, "schemaflux.escalation.reason": 64} {
		value, ok := attributeOf(span, key)
		if !ok || len(value.AsString()) != limit {
			t.Errorf("%s has %d bytes (set: %v), want %d", key, len(value.AsString()), ok, limit)
		}
		for index := 0; index < len(value.AsString()); index++ {
			if c := value.AsString()[index]; c < 0x20 || c > 0x7e {
				t.Errorf("%s holds the byte %#x", key, c)
				break
			}
		}
	}
	if value, _ := attributeOf(span, "schemaflux.escalation.model"); !strings.HasPrefix(value.AsString(), "model?") {
		t.Errorf("the newline in the model was not replaced: %q", value.AsString()[:8])
	}
}
