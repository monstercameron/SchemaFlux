package tests

import (
	"context"
	"errors"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

// This file covers the Op descriptor and planning/execution wrappers: NewOp,
// MustNewOp, RunOp, WithDiagnosticSink, RunOpResult, Preflight,
// NewPlanBuilder, RunOpBatch, RunOpBatchResult, and RunOpMany.

// echoOp is a minimal atomic Op[string, string]: the response body IS the
// output, unchanged. It exists purely so this file's tests can drive the Op
// machinery without depending on any real operation's prompt shape.
func echoOp() schemaflux.Op[string, string] {
	return schemaflux.Op[string, string]{
		ID:        schemaflux.OperationID{Name: "rootwrap-echo", Version: "v1"},
		Semantics: schemaflux.Semantics{Stability: schemaflux.StabilityExperimental},
		Contract: schemaflux.OutputContract[string]{
			Decode: func(body string) (string, error) { return body, nil },
		},
		Batch:       schemaflux.BatchAlgebra[string, string]{Class: schemaflux.BatchNone},
		BuildPrompt: func(input string, _ schemaflux.OpOptions) (string, string) { return "echo", input },
	}
}

// --- NewOp / MustNewOp -------------------------------------------------------

// NewOp accepts an experimental op with no declared batch class.
func TestNewOpAcceptsAnExperimentalOpWithNoBatchClass(t *testing.T) {
	op, err := schemaflux.NewOp(echoOp())
	if err != nil {
		t.Fatalf("NewOp: %v", err)
	}
	if op.ID.Name != "rootwrap-echo" {
		t.Errorf("ID.Name = %q, want the op echoed back unchanged", op.ID.Name)
	}
}

// A stable op with no batch class is rejected: "stable" promises composition
// behaviour across a batch, and BatchUnspecified says nothing about it.
func TestNewOpRejectsAStableOpWithNoBatchClass(t *testing.T) {
	op := echoOp()
	op.Semantics.Stability = schemaflux.StabilityStable
	op.Batch = schemaflux.BatchAlgebra[string, string]{}

	_, err := schemaflux.NewOp(op)
	if err == nil {
		t.Fatal("expected an error: a stable op must declare a batch class")
	}
}

func TestMustNewOpReturnsTheValidatedOp(t *testing.T) {
	op := schemaflux.MustNewOp(echoOp())
	if op.ID.Name != "rootwrap-echo" {
		t.Errorf("ID.Name = %q, want it unchanged", op.ID.Name)
	}
}

func TestMustNewOpPanicsOnAnInvalidOp(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected MustNewOp to panic on an invalid op")
		}
	}()
	op := echoOp()
	op.Semantics.Stability = schemaflux.StabilityStable
	op.Batch = schemaflux.BatchAlgebra[string, string]{}
	schemaflux.MustNewOp(op)
}

// --- RunOp / RunOpResult -----------------------------------------------------

func TestRunOpReturnsTheDecodedBody(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "GOT: hello", nil)

	value, _, err := schemaflux.RunOp(context.Background(), echoOp(), "hello", schemaflux.OpOptions{})
	if err != nil {
		t.Fatalf("RunOp: %v", err)
	}
	if value != "GOT: hello" {
		t.Errorf("value = %q, want the scripted body decoded verbatim", value)
	}
}

func TestRunOpResultCarriesTheEnvelope(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "GOT: hello", nil)

	result, err := schemaflux.RunOpResult(context.Background(), echoOp(), "hello", schemaflux.OpOptions{})
	if err != nil {
		t.Fatalf("RunOpResult: %v", err)
	}
	if result.Value != "GOT: hello" {
		t.Errorf("Value = %q, want the scripted body", result.Value)
	}
	if result.Meta.Operation != "rootwrap-echo@v1" {
		t.Errorf("Meta.Operation = %q, want the op's own id string", result.Meta.Operation)
	}
}

// WithDiagnosticSink captures a repair-exhausted body's reference into the
// caller's own sink, reachable from the failing error.
type recordingSink struct {
	records []types.DiagnosticRecord
}

func (s *recordingSink) Capture(record types.DiagnosticRecord) {
	s.records = append(s.records, record)
}

func TestWithDiagnosticSinkCapturesAnExhaustedRepair(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "not usable", nil)

	failingOp := echoOp()
	failingOp.Contract.Decode = func(string) (string, error) { return "", errors.New("never valid") }
	failingOp.DefaultPolicy.RepairAttempts = 1

	sink := &recordingSink{}
	ctx := schemaflux.WithDiagnosticSink(context.Background(), sink, schemaflux.DiagnosticPolicy{})

	_, err := schemaflux.RunOpResult(ctx, failingOp, "hello", schemaflux.OpOptions{})
	if err == nil {
		t.Fatal("expected the exhausted repair to surface as an error")
	}
	var opErr *schemaflux.OperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("err = %v (%T), want an *OperationError", err, err)
	}
	if opErr.Diagnostic.IsZero() {
		t.Error("Diagnostic is zero, want a reference into the configured sink")
	}
	if len(sink.records) == 0 {
		t.Error("the sink captured nothing, want the failing body recorded")
	}
}

// Without a configured sink, nothing is captured -- diagnostics are opt-in.
func TestRunOpResultCapturesNothingWithoutASink(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "not usable", nil)

	failingOp := echoOp()
	failingOp.Contract.Decode = func(string) (string, error) { return "", errors.New("never valid") }
	failingOp.DefaultPolicy.RepairAttempts = 1

	_, err := schemaflux.RunOpResult(context.Background(), failingOp, "hello", schemaflux.OpOptions{})
	if err == nil {
		t.Fatal("expected the exhausted repair to surface as an error")
	}
	var opErr *schemaflux.OperationError
	if errors.As(err, &opErr) && !opErr.Diagnostic.IsZero() {
		t.Error("Diagnostic is non-zero with no sink configured")
	}
}

// --- Preflight / NewPlanBuilder ----------------------------------------------

func TestPreflightMakesNoProviderCallAndReportsEligibility(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("must not be called"))

	plan, err := schemaflux.Preflight(echoOp(), []string{"a", "b", "c"}, schemaflux.PlanRequest{})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !plan.Eligible {
		t.Errorf("Eligible = false: %s", plan.IneligibleReason)
	}
	if plan.ItemCount != 3 {
		t.Errorf("ItemCount = %d, want 3", plan.ItemCount)
	}
}

// NewPlanBuilder's Atomic() must reach Preflight as a forced shape, visible
// on the resulting plan.
func TestNewPlanBuilderAtomicForcesTheShapeOntoThePlan(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("must not be called"))

	req := schemaflux.NewPlanBuilder(schemaflux.OpOptions{}).Atomic().WithReason("risk review requires one call per item").Build()
	plan, err := schemaflux.Preflight(echoOp(), []string{"a", "b"}, req)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if plan.Shape != schemaflux.ShapeAtomic || !plan.ShapeForced {
		t.Errorf("Shape=%v ShapeForced=%v, want the builder's forced atomic shape", plan.Shape, plan.ShapeForced)
	}
}

// A staged plan is planned through the root without asking the provider anything,
// then run: the first stage's request is what the plan priced (the client leaves
// the library's reinforcement block off), and the run asks no more than the plan's
// call ceiling.
func TestPreflightStagedPlanPlansBeforeThePlanIsRun(t *testing.T) {
	provider := testfixtures.WithScriptedProvider(t, "answered", nil)
	stage := func(name, system string) schemaflux.Stage[string] {
		op := echoOp()
		op.ID.Name = name
		op.BuildPrompt = func(input string, _ schemaflux.OpOptions) (string, string) { return system, input }
		return schemaflux.Stage[string]{Op: op}
	}
	plan := schemaflux.StagedPlan[string]{
		ID:     schemaflux.OperationID{Name: "rootwrap-stages", Version: "v1"},
		Stages: []schemaflux.Stage[string]{stage("first", strings.Repeat("s", 400)), stage("second", strings.Repeat("t", 40))},
	}
	datum := strings.Repeat("d", 80)

	planned, err := schemaflux.PreflightStagedPlan(context.Background(), plan, datum, schemaflux.PlanRequest{})
	if err != nil || !planned.Plan.Eligible {
		t.Fatalf("PreflightStagedPlan: %v (%s)", err, planned.Plan.IneligibleReason)
	}
	if got := len(provider.Requests()); got != 0 {
		t.Fatalf("planning sent %d requests", got)
	}
	if planned.Plan.Shape != schemaflux.ShapeSDMP || planned.Plan.MaxCalls != 2 || len(planned.Stages) != 2 {
		t.Fatalf("shape %v, max calls %d, %d stages", planned.Plan.Shape, planned.Plan.MaxCalls, len(planned.Stages))
	}
	// 100 tokens of system prompt and 20 of datum, and the 54 tokens of reinforcement
	// block the call path puts in front of every system prompt; the second stage is
	// priced from the datum.
	const reinforcementTokens = 54
	if first, second := planned.Stages[0], planned.Stages[1]; first.Estimate.EstimatedPromptTokens != 120+reinforcementTokens || first.UpperBound ||
		second.Estimate.EstimatedPromptTokens != 30+reinforcementTokens || !second.UpperBound {
		t.Errorf("stages priced %+v and %+v", first, second)
	}

	if _, err := schemaflux.RunStagedPlan(context.Background(), plan, datum, schemaflux.OpOptions{}); err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}
	if got := len(provider.Requests()); got == 0 || got > planned.Plan.MaxCalls {
		t.Errorf("the run sent %d requests, the plan said at most %d", got, planned.Plan.MaxCalls)
	}
}

func TestPreflightStagedPlanRefusesWhatCannotBePlannedAsAnInvalidRequest(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("must not be called"))
	plan := schemaflux.StagedPlan[string]{Stages: []schemaflux.Stage[string]{{Op: echoOp()}}}

	//lint:ignore SA1012 a nil context is what is being refused
	if _, err := schemaflux.PreflightStagedPlan(nil, plan, "d", schemaflux.PlanRequest{}); !errors.Is(err, schemaflux.ErrInvalidRequest) {
		t.Errorf("a nil context: %v", err)
	}
	if _, err := schemaflux.PreflightStagedPlan(context.Background(), schemaflux.StagedPlan[string]{}, "d", schemaflux.PlanRequest{}); !errors.Is(err, schemaflux.ErrInvalidRequest) {
		t.Errorf("no stages: %v", err)
	}
}

// An over-budget plan is refused with the budget kind, by the plan and by the run.
func TestRunOpManyRefusesAnOverBudgetPlanAsABudgetError(t *testing.T) {
	provider := testfixtures.WithScriptedProvider(t, "", errors.New("must not be called"))

	_, plan, err := schemaflux.RunOpMany(context.Background(), echoOp(), []string{"a", "b", "c"},
		schemaflux.PlanRequest{Options: schemaflux.OpOptions{Model: "gpt-4"}, Budget: 1e-9})
	if !errors.Is(err, schemaflux.ErrBudgetExceededKind) || errors.Is(err, schemaflux.ErrConfiguration) {
		t.Fatalf("err = %v, want a budget error and not a configuration one", err)
	}
	if plan.Eligible || plan.IneligibleKind != schemaflux.KindOf(err) {
		t.Errorf("plan eligible %v, refusal kind %v, error kind %v", plan.Eligible, plan.IneligibleKind, schemaflux.KindOf(err))
	}
	if got := len(provider.Requests()); got != 0 {
		t.Errorf("a refused plan sent %d requests", got)
	}
}

// --- RunOpBatch / RunOpBatchResult -------------------------------------------

// batchEchoOp tags each item's own text as its id, so Merge can pair the
// response back up without trusting response order.
func batchEchoOp() schemaflux.Op[string, string] {
	op := echoOp()
	op.Batch = schemaflux.BatchAlgebra[string, string]{
		Class: schemaflux.BatchItemwise,
		Kind:  schemaflux.AlgebraIndependent,
		Encode: func(items []string, _ schemaflux.OpOptions) (string, string, error) {
			return "echo each item, prefixed with GOT:", strings.Join(items, "\n"), nil
		},
		Merge: func(body string, items []string) ([]string, error) {
			out := make([]string, len(items))
			for i, item := range items {
				out[i] = "GOT: " + item
			}
			return out, nil
		},
	}
	return op
}

func TestRunOpBatchReturnsOnePerItem(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "ignored: Merge below does not read the body", nil)

	out, err := schemaflux.RunOpBatch(context.Background(), batchEchoOp(), []string{"a", "b", "c"}, schemaflux.OpOptions{})
	if err != nil {
		t.Fatalf("RunOpBatch: %v", err)
	}
	want := []string{"GOT: a", "GOT: b", "GOT: c"}
	if len(out) != len(want) {
		t.Fatalf("out = %v, want %v", out, want)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("out[%d] = %q, want %q", i, out[i], want[i])
		}
	}
}

func TestRunOpBatchEmptyItemsMakesNoProviderCall(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("must not be called"))

	out, err := schemaflux.RunOpBatch(context.Background(), batchEchoOp(), []string{}, schemaflux.OpOptions{})
	if err != nil {
		t.Fatalf("RunOpBatch: %v", err)
	}
	if out != nil {
		t.Errorf("out = %v, want nil for an empty batch", out)
	}
}

func TestRunOpBatchResultCarriesTheEnvelope(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "ignored", nil)

	result, err := schemaflux.RunOpBatchResult(context.Background(), batchEchoOp(), []string{"a", "b"}, schemaflux.OpOptions{})
	if err != nil {
		t.Fatalf("RunOpBatchResult: %v", err)
	}
	if len(result.Value) != 2 {
		t.Fatalf("Value = %v, want 2 items", result.Value)
	}
	if result.Meta.Strategy != schemaflux.ShapeMDSP.String() {
		t.Errorf("Meta.Strategy = %q, want the MDSP shape recorded", result.Meta.Strategy)
	}
}

// --- RunOpMany ---------------------------------------------------------------

// Forced atomic, RunOpMany must run one call per item, in input order, and
// hand back both the results and the plan that produced them.
func TestRunOpManyRunsAtomicallyInOrder(t *testing.T) {
	// runManyAtomic runs items with bounded concurrency (DefaultConcurrency),
	// so calls do not necessarily arrive in input order. ReplyFunc answers
	// from the request's own content rather than a fixed per-call-index list,
	// which is the only way to script a per-item answer that survives
	// concurrent dispatch -- see schemafluxtest.Provider.ReplyFunc's doc
	// comment on exactly this trap.
	p := schemafluxtest.New().ReplyFunc(func(_ int, req schemaflux.CompletionRequest) (string, error) {
		return "GOT: " + req.UserPrompt, nil
	})
	schemafluxtest.Install(t, p)

	req := schemaflux.NewPlanBuilder(schemaflux.OpOptions{}).Atomic().WithReason("test").Build()
	results, plan, err := schemaflux.RunOpMany(context.Background(), echoOp(), []string{"a", "b", "c"}, req)
	if err != nil {
		t.Fatalf("RunOpMany: %v", err)
	}
	if plan.Shape != schemaflux.ShapeAtomic {
		t.Errorf("plan.Shape = %v, want atomic (forced)", plan.Shape)
	}
	if len(results) != 3 {
		t.Fatalf("results = %v, want 3", results)
	}
	for i, want := range []string{"GOT: a", "GOT: b", "GOT: c"} {
		if results[i].Value != want {
			t.Errorf("results[%d].Value = %q, want %q", i, results[i].Value, want)
		}
	}
}

func TestRunOpManyEmptyItemsMakesNoProviderCall(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("must not be called"))

	req := schemaflux.NewPlanBuilder(schemaflux.OpOptions{}).Atomic().Build()
	results, plan, err := schemaflux.RunOpMany(context.Background(), echoOp(), []string{}, req)
	if err != nil {
		t.Fatalf("RunOpMany: %v", err)
	}
	if results != nil {
		t.Errorf("results = %v, want nil for no items", results)
	}
	if !plan.Eligible {
		t.Errorf("plan.Eligible = false: %s", plan.IneligibleReason)
	}
}
