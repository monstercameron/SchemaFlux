package otel_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// What a trace of a run over many items shows: one operation span for the run,
// whichever entry it came in by, that says how many items it was given and what
// became of them, with the requests of the items as its children.

// echoOperation is an operation whose prompt is its item, so a scripted
// provider can decide by item what to do.
func echoOperation(name string) ops.Op[string, string] {
	return ops.Op[string, string]{
		ID:        types.OperationID{Name: name, Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityExperimental},
		Contract:  ops.OutputContract[string]{Decode: func(body string) (string, error) { return body, nil }},
		Batch:     ops.BatchAlgebra[string, string]{Class: types.BatchNone},
		BuildPrompt: func(input string, _ types.OpOptions) (string, string) {
			return "echo", input
		},
	}
}

// echoingProvider answers each request with its item, and fails the items in
// failing with err.
func echoingProvider(err error, failing ...string) *scriptedProvider {
	return &scriptedProvider{answer: func(ctx context.Context, call int, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		for _, item := range failing {
			if req.UserPrompt == item {
				return llm.CompletionResponse{}, err
			}
		}
		response, _ := answering(req.UserPrompt)(ctx, call, req)
		return response, nil
	}}
}

func runItems(n int) []string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf("item-%02d", i)
	}
	return items
}

// intAttribute reads an int attribute of a span, and fails the test when it is
// missing.
func intAttribute(t *testing.T, span tracetest.SpanStub, key string) int {
	t.Helper()
	value, ok := attributeOf(span, key)
	if !ok {
		t.Fatalf("span %q has no %s:\n%s", span.Name, key, describe(tracetest.SpanStubs{span}))
	}
	return int(value.AsInt64())
}

func TestARunOverManyItemsIsOneSpanThatSaysWhatBecameOfItsItems(t *testing.T) {
	refused := types.Newf(types.KindPolicyViolation, "provider", "route not allowed")
	cases := []struct {
		name        string
		policy      types.BatchPolicy
		failing     []string
		wantCounts  [4]int // items, succeeded, failed, cancelled
		wantStatus  codes.Code
		wantErrType string
	}{
		{"every item succeeded", types.PolicyCollectFailures, nil, [4]int{6, 6, 0, 0}, codes.Unset, ""},
		{"a run that collects its failures, and two failed", types.PolicyCollectFailures, []string{"item-01", "item-04"}, [4]int{6, 4, 2, 0}, codes.Unset, ""},
		{"retry then collect, one failed for good", types.PolicyRetryThenCollect, []string{"item-02"}, [4]int{6, 5, 1, 0}, codes.Unset, ""},
		{"a run that requires every item, and one failed", types.PolicyRequireAll, []string{"item-02"}, [4]int{6, 5, 1, 0}, codes.Error, "policy violation"},
		{"a fail-fast run stopped by a failure", types.PolicyFailFast, []string{"item-02"}, [4]int{6, 2, 1, 3}, codes.Error, "policy violation"},
		{"every item failed, though the call returned a result", types.PolicyCollectFailures, runItems(6), [4]int{6, 0, 6, 0}, codes.Error, "policy violation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, exporter := traced(t)
			useProvider(t, echoingProvider(refused, tc.failing...))
			var runErr error
			spans := underCaller(provider, exporter, func(ctx context.Context) {
				_, runErr = ops.RunOpManyPartial(ctx, echoOperation("callerEcho"), runItems(6), types.OpOptions{},
					ops.PartialConfig{Policy: tc.policy, Concurrency: 1, MaxRetries: 1})
			})
			if (runErr != nil) != (tc.policy == types.PolicyRequireAll || tc.policy == types.PolicyFailFast) {
				t.Fatalf("the run returned %v", runErr)
			}

			runs := named(spans, "schemaflux.callerEcho")
			if len(runs) != 1 {
				t.Fatalf("%d spans for the run, want one:\n%s", len(runs), describe(spans))
			}
			run := runs[0]
			if got := [4]int{intAttribute(t, run, "schemaflux.items.total"), intAttribute(t, run, "schemaflux.items.succeeded"),
				intAttribute(t, run, "schemaflux.items.failed"), intAttribute(t, run, "schemaflux.items.cancelled")}; got != tc.wantCounts {
				t.Errorf("items total, succeeded, failed, cancelled = %v, want %v", got, tc.wantCounts)
			}
			if run.Status.Code != tc.wantStatus {
				t.Errorf("status = %v %q, want %v", run.Status.Code, run.Status.Description, tc.wantStatus)
			}
			if got, _ := attributeOf(run, "error.type"); got.AsString() != tc.wantErrType {
				t.Errorf("error.type = %q, want %q", got.AsString(), tc.wantErrType)
			}

			made := requests(spans)
			if got := intAttribute(t, run, "schemaflux.requests"); got != len(made) || len(made) == 0 {
				t.Errorf("schemaflux.requests = %d with %d request spans", got, len(made))
			}
			for _, request := range made {
				if request.Parent.SpanID() != run.SpanContext.SpanID() {
					t.Errorf("a request span is not a child of the run's span:\n%s", describe(spans))
					break
				}
			}
			if leaked := leaks(spans); len(leaked) != 0 {
				t.Errorf("leaked: %v", leaked)
			}
			for _, kv := range run.Attributes {
				if strings.Contains(kv.Value.Emit(), "item-") {
					t.Errorf("the run's span carries an item: %s=%s", kv.Key, kv.Value.Emit())
				}
			}
		})
	}
}

// RunOpMany used to be one operation span for each item, and RunOpManyPartial
// one span for the whole run.
func TestRunOpManyIsOneSpanOverTheRequestsOfItsItems(t *testing.T) {
	provider, exporter := traced(t)
	useProvider(t, echoingProvider(nil))
	spans := underCaller(provider, exporter, func(ctx context.Context) {
		out, _, err := ops.RunOpMany(ctx, echoOperation("callerEcho"), runItems(6), types.PlanRequest{Force: types.ShapeAtomic})
		if err != nil || len(out) != 6 {
			t.Fatalf("RunOpMany returned %d results and %v", len(out), err)
		}
	})

	runs := named(spans, "schemaflux.callerEcho")
	if len(runs) != 1 {
		t.Fatalf("%d operation spans for six items, want one:\n%s", len(runs), describe(spans))
	}
	run := runs[0]
	caller := named(spans, "caller")[0]
	if run.Parent.SpanID() != caller.SpanContext.SpanID() {
		t.Error("the run's span is not under the caller's")
	}
	if got := [4]int{intAttribute(t, run, "schemaflux.items.total"), intAttribute(t, run, "schemaflux.items.succeeded"),
		intAttribute(t, run, "schemaflux.items.failed"), intAttribute(t, run, "schemaflux.items.cancelled")}; got != [4]int{6, 6, 0, 0} {
		t.Errorf("items total, succeeded, failed, cancelled = %v, want 6, 6, 0, 0", got)
	}
	made := requests(spans)
	if len(made) != 6 || intAttribute(t, run, "schemaflux.requests") != 6 {
		t.Fatalf("%d request spans, schemaflux.requests %d, want 6:\n%s", len(made), intAttribute(t, run, "schemaflux.requests"), describe(spans))
	}
	for _, request := range made {
		if request.Parent.SpanID() != run.SpanContext.SpanID() {
			t.Fatalf("a request span is not under the run's:\n%s", describe(spans))
		}
	}
}

// When RunOpMany fails it returns no item, and every item is a failure of it.
func TestRunOpManyThatFailsIsAnErrorSpanWithEveryItemFailed(t *testing.T) {
	provider, exporter := traced(t)
	useProvider(t, echoingProvider(types.Newf(types.KindPolicyViolation, "provider", "no"), "item-03"))
	spans := underCaller(provider, exporter, func(ctx context.Context) {
		if _, _, err := ops.RunOpMany(ctx, echoOperation("callerEcho"), runItems(6), types.PlanRequest{Force: types.ShapeAtomic}); err == nil {
			t.Fatal("RunOpMany did not fail")
		}
	})
	runs := named(spans, "schemaflux.callerEcho")
	if len(runs) != 1 || runs[0].Status.Code != codes.Error {
		t.Fatalf("want one operation span with status Error:\n%s", describe(spans))
	}
	if got := [4]int{intAttribute(t, runs[0], "schemaflux.items.total"), intAttribute(t, runs[0], "schemaflux.items.succeeded"),
		intAttribute(t, runs[0], "schemaflux.items.failed"), intAttribute(t, runs[0], "schemaflux.items.cancelled")}; got != [4]int{6, 0, 6, 0} {
		t.Errorf("items total, succeeded, failed, cancelled = %v, want 6, 0, 6, 0", got)
	}
}

// The stages of a plan are the operations under the plan's own, which carries
// the plan's name; nothing else tied the stages of one run together.
func TestAStagedPlanIsOneSpanOverItsStages(t *testing.T) {
	provider, exporter := traced(t)
	useProvider(t, echoingProvider(nil))
	plan := ops.StagedPlan[string]{
		ID: types.OperationID{Name: "three-stage"},
		Stages: []ops.Stage[string]{
			{ID: types.OperationID{Name: "stage-a"}, Op: echoOperation("stage-a")},
			{ID: types.OperationID{Name: "stage-b"}, Op: echoOperation("stage-b")},
			{ID: types.OperationID{Name: "stage-c"}, Op: echoOperation("stage-c")},
		},
	}
	spans := underCaller(provider, exporter, func(ctx context.Context) {
		if _, err := ops.RunStagedPlan(ctx, plan, "input", types.OpOptions{}); err != nil {
			t.Fatalf("RunStagedPlan: %v", err)
		}
	})

	runs := named(spans, "schemaflux.three-stage")
	if len(runs) != 1 {
		t.Fatalf("%d spans for the plan, want one:\n%s", len(runs), describe(spans))
	}
	run := runs[0]
	if caller := named(spans, "caller")[0]; run.Parent.SpanID() != caller.SpanContext.SpanID() {
		t.Error("the plan's span is not under the caller's")
	}
	for _, stage := range []string{"schemaflux.stage-a", "schemaflux.stage-b", "schemaflux.stage-c"} {
		found := named(spans, stage)
		if len(found) != 1 || found[0].Parent.SpanID() != run.SpanContext.SpanID() {
			t.Errorf("stage %s is not one span under the plan's:\n%s", stage, describe(spans))
			continue
		}
		if got := intAttribute(t, found[0], "schemaflux.requests"); got != 1 {
			t.Errorf("stage %s made %d requests, want 1", stage, got)
		}
	}
	if got := intAttribute(t, run, "schemaflux.requests"); got != 3 {
		t.Errorf("the plan made %d requests, want 3: its stages' are its own", got)
	}
	if _, ok := attributeOf(run, "schemaflux.items.total"); ok {
		t.Error("a plan's span carries item counts; its stages are not items")
	}
}

// Every operation span says how many requests it made, and one that was refused
// before anything was sent made none.
func TestAnOperationSpanSaysHowManyRequestsItMade(t *testing.T) {
	provider, exporter := traced(t)
	useProvider(t, &scriptedProvider{answer: answering("this is not JSON")})
	spans := underCaller(provider, exporter, func(ctx context.Context) {
		_, _ = ops.Extract[invoice]("Invoice INV-1 from Acme", extractOptions(ctx))
	})
	operations := named(spans, "schemaflux.extract")
	if len(operations) != 1 {
		t.Fatalf("want one operation span:\n%s", describe(spans))
	}
	if got := intAttribute(t, operations[0], "schemaflux.requests"); got != len(requests(spans)) || got < 2 {
		t.Errorf("schemaflux.requests = %d with %d request spans, want a request and its repairs", got, len(requests(spans)))
	}
	if _, ok := attributeOf(operations[0], "schemaflux.items.total"); ok {
		t.Error("a call of one item carries item counts")
	}

	spans = underCaller(provider, exporter, func(ctx context.Context) {
		_, _ = ops.Extract[invoice](nil, extractOptions(ctx))
	})
	operations = named(spans, "schemaflux.extract")
	if len(operations) != 1 || intAttribute(t, operations[0], "schemaflux.requests") != 0 {
		t.Errorf("a call refused before it sent anything made requests:\n%s", describe(spans))
	}
}
