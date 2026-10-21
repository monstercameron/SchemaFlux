package otel

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// What a trace of a run over many items shows, through the root's client: one
// operation span for the run, whichever entry it came in by, that says how many
// items it was given and what became of them, with the requests of the items as
// its children.

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
type echoingProvider struct {
	err     error
	failing map[string]bool
}

func (p echoingProvider) Complete(_ context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	if p.failing[req.User] {
		return schemaflux.Response{}, p.err
	}
	return schemaflux.Response{Content: req.User, Model: req.Model, FinishReason: "stop", Usage: schemaflux.Usage{Input: 11, Output: 7, Total: 18}}, nil
}
func (echoingProvider) Name() string                      { return "local" }
func (echoingProvider) RetryPolicy() (int, time.Duration) { return 0, time.Millisecond }

// failingItems is an echoingProvider that fails the items it is given.
func failingItems(err error, items ...string) echoingProvider {
	failing := map[string]bool{}
	for _, item := range items {
		failing[item] = true
	}
	return echoingProvider{err: err, failing: failing}
}

func itemNames(n int) []string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf("item-%02d", i)
	}
	return items
}

// withHostSpan installs the Observer, runs call on a client whose provider is
// provider, under a span named "caller", and returns what was exported.
func withHostSpan(t *testing.T, provider schemaflux.Provider, call func(ctx context.Context)) tracetest.SpanStubs {
	t.Helper()
	tracerProvider, exporter := installed(t)
	return underHostSpan(tracerProvider, exporter, provider, call)
}

// underHostSpan is withHostSpan for an Observer that is already installed. What
// was exported before is dropped, so a test can make several calls.
func underHostSpan(tracerProvider *sdktrace.TracerProvider, exporter *tracetest.InMemoryExporter, provider schemaflux.Provider, call func(ctx context.Context)) tracetest.SpanStubs {
	exporter.Reset()
	ctx, caller := tracerProvider.Tracer("host").Start(context.Background(), "caller")
	call(schemaflux.WithClient(ctx, schemaflux.Client{Provider: provider, Model: "test-model"}))
	caller.End()
	return exporter.GetSpans()
}

func intAttr(t *testing.T, span tracetest.SpanStub, key string) int {
	t.Helper()
	value, ok := attributeOf(span, key)
	if !ok {
		t.Fatalf("span %q has no %s: %v", span.Name, key, typed(span.Attributes))
	}
	return int(value.AsInt64())
}

func stringAttr(span tracetest.SpanStub, key string) string {
	value, _ := attributeOf(span, key)
	return value.AsString()
}

// withName returns the spans with the name.
func withName(spans tracetest.SpanStubs, name string) tracetest.SpanStubs {
	var found tracetest.SpanStubs
	for _, span := range spans {
		if span.Name == name {
			found = append(found, span)
		}
	}
	return found
}

// requestSpans returns the spans of requests to a model.
func requestSpans(spans tracetest.SpanStubs) tracetest.SpanStubs {
	var found tracetest.SpanStubs
	for _, span := range spans {
		if strings.HasPrefix(span.Name, "chat") {
			found = append(found, span)
		}
	}
	return found
}

func itemCounts(t *testing.T, span tracetest.SpanStub) [4]int {
	t.Helper()
	return [4]int{intAttr(t, span, "schemaflux.items.total"), intAttr(t, span, "schemaflux.items.succeeded"),
		intAttr(t, span, "schemaflux.items.failed"), intAttr(t, span, "schemaflux.items.cancelled")}
}

func TestARunOverManyItemsIsOneSpanThatSaysWhatBecameOfItsItems(t *testing.T) {
	refused := &schemaflux.Error{Kind: schemaflux.ErrPolicy, Message: "route not allowed"}
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
		{"a run that requires every item, and one failed", types.PolicyRequireAll, []string{"item-02"}, [4]int{6, 5, 1, 0}, codes.Error, "policy"},
		{"a fail-fast run stopped by a failure", types.PolicyFailFast, []string{"item-02"}, [4]int{6, 2, 1, 3}, codes.Error, "policy"},
		{"every item failed, though the call returned a result", types.PolicyCollectFailures, itemNames(6), [4]int{6, 0, 6, 0}, codes.Error, "policy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var runErr error
			spans := withHostSpan(t, failingItems(refused, tc.failing...), func(ctx context.Context) {
				_, runErr = ops.RunOpManyPartial(ctx, echoOperation("callerEcho"), itemNames(6), types.OpOptions{},
					ops.PartialConfig{Policy: tc.policy, Concurrency: 1, MaxRetries: 1})
			})
			if (runErr != nil) != (tc.policy == types.PolicyRequireAll || tc.policy == types.PolicyFailFast) {
				t.Fatalf("the run returned %v", runErr)
			}

			runs := withName(spans, "schemaflux.callerEcho")
			if len(runs) != 1 {
				t.Fatalf("%d spans for the run, want one", len(runs))
			}
			run := runs[0]
			if got := itemCounts(t, run); got != tc.wantCounts {
				t.Errorf("items total, succeeded, failed, cancelled = %v, want %v", got, tc.wantCounts)
			}
			if run.Status.Code != tc.wantStatus {
				t.Errorf("status = %v %q, want %v", run.Status.Code, run.Status.Description, tc.wantStatus)
			}
			if got := stringAttr(run, "error.type"); got != tc.wantErrType {
				t.Errorf("error.type = %q, want %q", got, tc.wantErrType)
			}
			made := requestSpans(spans)
			if got := intAttr(t, run, "schemaflux.requests"); got != len(made) || len(made) == 0 {
				t.Errorf("schemaflux.requests = %d with %d request spans", got, len(made))
			}
			for _, request := range made {
				if request.Parent.SpanID() != run.SpanContext.SpanID() {
					t.Error("a request span is not a child of the run's span")
					break
				}
			}
			for _, kv := range run.Attributes {
				if strings.Contains(kv.Value.Emit(), "item-") {
					t.Errorf("the run's span carries an item: %s=%s", kv.Key, kv.Value.Emit())
				}
			}
		})
	}
}

func TestRunOpManyIsOneSpanOverTheRequestsOfItsItems(t *testing.T) {
	spans := withHostSpan(t, failingItems(nil), func(ctx context.Context) {
		out, _, err := ops.RunOpMany(ctx, echoOperation("callerEcho"), itemNames(6), types.PlanRequest{Force: types.ShapeAtomic})
		if err != nil || len(out) != 6 {
			t.Fatalf("RunOpMany returned %d results and %v", len(out), err)
		}
	})
	runs := withName(spans, "schemaflux.callerEcho")
	if len(runs) != 1 {
		t.Fatalf("%d operation spans for six items, want one", len(runs))
	}
	run := runs[0]
	if caller := withName(spans, "caller")[0]; run.Parent.SpanID() != caller.SpanContext.SpanID() {
		t.Error("the run's span is not under the caller's")
	}
	if got := itemCounts(t, run); got != [4]int{6, 6, 0, 0} {
		t.Errorf("items total, succeeded, failed, cancelled = %v, want 6, 6, 0, 0", got)
	}
	made := requestSpans(spans)
	if len(made) != 6 || intAttr(t, run, "schemaflux.requests") != 6 {
		t.Fatalf("%d request spans, schemaflux.requests %d, want 6", len(made), intAttr(t, run, "schemaflux.requests"))
	}
	for _, request := range made {
		if request.Parent.SpanID() != run.SpanContext.SpanID() {
			t.Fatal("a request span is not under the run's")
		}
	}
}

func TestRunOpManyThatFailsIsAnErrorSpanWithEveryItemFailed(t *testing.T) {
	spans := withHostSpan(t, failingItems(&schemaflux.Error{Kind: schemaflux.ErrPolicy}, "item-03"), func(ctx context.Context) {
		if _, _, err := ops.RunOpMany(ctx, echoOperation("callerEcho"), itemNames(6), types.PlanRequest{Force: types.ShapeAtomic}); err == nil {
			t.Fatal("RunOpMany did not fail")
		}
	})
	runs := withName(spans, "schemaflux.callerEcho")
	if len(runs) != 1 || runs[0].Status.Code != codes.Error {
		t.Fatalf("want one operation span with status Error, got %d", len(runs))
	}
	if got := itemCounts(t, runs[0]); got != [4]int{6, 0, 6, 0} {
		t.Errorf("items total, succeeded, failed, cancelled = %v, want 6, 0, 6, 0", got)
	}
}

func TestAStagedPlanIsOneSpanOverItsStages(t *testing.T) {
	plan := ops.StagedPlan[string]{
		ID: types.OperationID{Name: "three-stage"},
		Stages: []ops.Stage[string]{
			{ID: types.OperationID{Name: "stage-a"}, Op: echoOperation("stage-a")},
			{ID: types.OperationID{Name: "stage-b"}, Op: echoOperation("stage-b")},
			{ID: types.OperationID{Name: "stage-c"}, Op: echoOperation("stage-c")},
		},
	}
	spans := withHostSpan(t, failingItems(nil), func(ctx context.Context) {
		if _, err := ops.RunStagedPlan(ctx, plan, "input", types.OpOptions{}); err != nil {
			t.Fatalf("RunStagedPlan: %v", err)
		}
	})
	runs := withName(spans, "schemaflux.three-stage")
	if len(runs) != 1 {
		t.Fatalf("%d spans for the plan, want one", len(runs))
	}
	run := runs[0]
	for _, stage := range []string{"schemaflux.stage-a", "schemaflux.stage-b", "schemaflux.stage-c"} {
		found := withName(spans, stage)
		if len(found) != 1 || found[0].Parent.SpanID() != run.SpanContext.SpanID() || intAttr(t, found[0], "schemaflux.requests") != 1 {
			t.Errorf("stage %s is not one span under the plan's that made one request", stage)
		}
	}
	if got := intAttr(t, run, "schemaflux.requests"); got != 3 {
		t.Errorf("the plan made %d requests, want 3", got)
	}
	if _, ok := attributeOf(run, "schemaflux.items.total"); ok {
		t.Error("a plan's span carries item counts; its stages are not items")
	}
}

func TestAnOperationSpanSaysHowManyRequestsItMade(t *testing.T) {
	tracerProvider, exporter := installed(t)
	provider := &scripted{answer: func(int) (schemaflux.Response, error) { return schemaflux.Response{Content: "not JSON at all"}, nil }}
	extract := func(input any) func(ctx context.Context) {
		return func(ctx context.Context) {
			options := ops.NewExtractOptions()
			options.CommonOptions.Context = ctx
			_, _ = ops.Extract[invoice](input, options)
		}
	}

	spans := underHostSpan(tracerProvider, exporter, provider, extract("Invoice from Acme"))
	operations := withName(spans, "schemaflux.extract")
	if len(operations) != 1 {
		t.Fatalf("want one operation span, got %d", len(operations))
	}
	if got := intAttr(t, operations[0], "schemaflux.requests"); got != len(requestSpans(spans)) || got < 2 {
		t.Errorf("schemaflux.requests = %d with %d request spans, want a request and its repairs", got, len(requestSpans(spans)))
	}
	if _, ok := attributeOf(operations[0], "schemaflux.items.total"); ok {
		t.Error("a call of one item carries item counts")
	}

	spans = underHostSpan(tracerProvider, exporter, provider, extract(nil))
	operations = withName(spans, "schemaflux.extract")
	if len(operations) != 1 || intAttr(t, operations[0], "schemaflux.requests") != 0 {
		t.Errorf("a call refused before it sent anything made requests: %d spans", len(operations))
	}
}
