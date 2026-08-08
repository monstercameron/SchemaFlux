package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// --- Fixtures shared across this file's tests.

// widget/widgetSummary is a stable, BatchItemwise operation wired through
// NewIDBatchAlgebra -- the fixture PL-002/003/004/005/007's tests run
// against, standing in for a future ExtractMany/ClassifyMany-shaped
// operation.
type widget struct {
	Name string
}

type widgetSummary struct {
	Length int `json:"length"`
}

func widgetOp() Op[widget, widgetSummary] {
	return Op[widget, widgetSummary]{
		ID: types.OperationID{Name: "widgetSummarize", Version: "v1"},
		Semantics: types.Semantics{
			Category:  types.CategoryTransformation,
			Stability: types.StabilityExperimental,
		},
		Contract: OutputContract[widgetSummary]{
			Decode: func(body string) (widgetSummary, error) {
				var s widgetSummary
				if err := json.Unmarshal([]byte(body), &s); err != nil {
					return widgetSummary{}, err
				}
				return s, nil
			},
		},
		Batch: NewIDBatchAlgebra[widget, widgetSummary](types.BatchItemwise, types.AlgebraIndependent,
			"Summarize each widget's name length.",
			BatchItemCodec[widget, widgetSummary]{
				DecodeItem: func(raw json.RawMessage) (widgetSummary, error) {
					var s widgetSummary
					if err := json.Unmarshal(raw, &s); err != nil {
						return widgetSummary{}, err
					}
					return s, nil
				},
			}),
		BuildPrompt: func(w widget, _ types.OpOptions) (string, string) {
			return "summarize", w.Name
		},
	}
}

// atomicOnlyOp is BatchNone -- MDSP is illegal for it, which the forced-shape
// refusal tests need.
func atomicOnlyOp() Op[string, string] {
	return Op[string, string]{
		ID:        types.OperationID{Name: "echo", Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityExperimental},
		Contract: OutputContract[string]{
			Decode: func(body string) (string, error) { return body, nil },
		},
		Batch: BatchAlgebra[string, string]{Class: types.BatchNone},
		BuildPrompt: func(input string, _ types.OpOptions) (string, string) {
			return "echo", input
		},
	}
}

func widgets(n int) []widget {
	items := make([]widget, n)
	for i := range items {
		items[i] = widget{Name: fmt.Sprintf("widget-%03d", i)}
	}
	return items
}

// --- PL-001: Preflight makes zero provider calls.

func TestPreflightMakesZeroProviderCalls(t *testing.T) {
	var calls int32
	setLLMCaller(func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		atomic.AddInt32(&calls, 1)
		return `{}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	items := widgets(240)
	plan, err := Preflight(widgetOp(), items, types.PlanRequest{})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !plan.Eligible {
		t.Fatalf("plan not eligible: %s", plan.IneligibleReason)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("Preflight made %d provider call(s); want 0", calls)
	}
	if plan.MaxCalls <= 0 {
		t.Fatalf("MaxCalls = %d, want a positive call ceiling", plan.MaxCalls)
	}
	if plan.MaxCalls > 240 {
		t.Fatalf("MaxCalls = %d, want at most one call per item (240)", plan.MaxCalls)
	}
}

// --- PL-001: determinism.

func TestPreflightIsDeterministic(t *testing.T) {
	items := widgets(37)
	req := types.PlanRequest{Options: types.OpOptions{Intelligence: types.Fast}}

	planA, err := Preflight(widgetOp(), items, req)
	if err != nil {
		t.Fatalf("Preflight (first): %v", err)
	}
	planB, err := Preflight(widgetOp(), items, req)
	if err != nil {
		t.Fatalf("Preflight (second): %v", err)
	}

	bytesA, err := planA.Serialize()
	if err != nil {
		t.Fatalf("Serialize (first): %v", err)
	}
	bytesB, err := planB.Serialize()
	if err != nil {
		t.Fatalf("Serialize (second): %v", err)
	}

	if string(bytesA) != string(bytesB) {
		t.Fatalf("two plans from identical inputs were not byte-identical:\nfirst:  %s\nsecond: %s", bytesA, bytesB)
	}
	if planA.Digest() != planB.Digest() {
		t.Fatalf("Digest() differed across identical inputs: %s vs %s", planA.Digest(), planB.Digest())
	}
}

// --- PL-001: no caller value in a serialized plan.

func TestPlanSerializeExcludesCallerValues(t *testing.T) {
	const marker = "SCHEMAFLUX-MARKER-4f9c9e2a"

	items := []widget{{Name: marker}}
	plan, err := Preflight(widgetOp(), items, types.PlanRequest{})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}

	encoded, err := plan.Serialize()
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if strings.Contains(string(encoded), marker) {
		t.Fatalf("serialized plan contains the caller's value: %s", encoded)
	}

	// Steering is caller-authored free text and must not survive into the
	// plan either -- only whether it was set.
	plan2, err := Preflight(widgetOp(), widgets(1), types.PlanRequest{
		Options: types.OpOptions{Steering: marker},
	})
	if err != nil {
		t.Fatalf("Preflight (steering): %v", err)
	}
	encoded2, err := plan2.Serialize()
	if err != nil {
		t.Fatalf("Serialize (steering): %v", err)
	}
	if strings.Contains(string(encoded2), marker) {
		t.Fatalf("serialized plan leaked steering text: %s", encoded2)
	}
	if !plan2.Policy.HasSteering {
		t.Fatal("plan lost that steering was set, not just its content")
	}
}

// --- PL-002: forced shape overrides the planner and is recorded.

func TestForcedShapeOverridesPlannerAndIsRecorded(t *testing.T) {
	req := types.PlanRequest{Force: types.ShapeAtomic, ForceReason: "risk: auditing every item individually"}
	plan, err := Preflight(widgetOp(), widgets(20), req)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !plan.Eligible {
		t.Fatalf("plan not eligible: %s", plan.IneligibleReason)
	}
	if plan.Shape != types.ShapeAtomic {
		t.Fatalf("Shape = %s, want atomic (forced)", plan.Shape)
	}
	if !plan.ShapeForced {
		t.Fatal("ShapeForced = false, want true")
	}
	if plan.ShapeReason != "risk: auditing every item individually" {
		t.Fatalf("ShapeReason = %q, did not record the caller's reason", plan.ShapeReason)
	}
	if plan.MaxCalls != 20 {
		t.Fatalf("MaxCalls = %d, want 20 (one call per item under forced atomic)", plan.MaxCalls)
	}

	// The un-forced default for the same input is MDSP, proving the force
	// really did override the planner rather than happening to agree.
	defaultPlan, err := Preflight(widgetOp(), widgets(20), types.PlanRequest{})
	if err != nil {
		t.Fatalf("Preflight (default): %v", err)
	}
	if defaultPlan.Shape != types.ShapeMDSP {
		t.Fatalf("default Shape = %s, want mdsp (so the forced case above is a real override)", defaultPlan.Shape)
	}
}

// --- PL-002: an illegal forced shape is refused at plan time.

func TestForcedIllegalShapeIsRefusedAtPlanTime(t *testing.T) {
	req := types.PlanRequest{Force: types.ShapeMDSP, ForceReason: "cost"}
	plan, err := Preflight(atomicOnlyOp(), []string{"a", "b", "c"}, req)
	if err != nil {
		t.Fatalf("Preflight returned an error rather than an ineligible plan: %v", err)
	}
	if plan.Eligible {
		t.Fatal("plan was eligible for a shape the operation cannot run (BatchNone forced to MDSP)")
	}
	if !strings.Contains(plan.IneligibleReason, "not legal") {
		t.Fatalf("IneligibleReason does not explain the refusal: %q", plan.IneligibleReason)
	}
	// Not silently downgraded: the shape field must not read as if atomic
	// had been chosen and accepted.
	if plan.Shape == types.ShapeAtomic {
		t.Fatal("an ineligible forced plan silently recorded atomic as if it had been chosen")
	}
}

func TestForcingSDMPIsRefused(t *testing.T) {
	plan, err := Preflight(widgetOp(), widgets(5), types.PlanRequest{Force: types.ShapeSDMP})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if plan.Eligible {
		t.Fatal("SDMP was accepted; no operation in this delivery declares a staged form")
	}
}

// --- PL-002: PlanBuilder's three named forcing points.

func TestPlanBuilderAtomicBatchedAdaptive(t *testing.T) {
	b := NewPlanBuilder(types.OpOptions{}).Atomic()
	if b.Build().Force != types.ShapeAtomic {
		t.Fatalf("Atomic() did not force ShapeAtomic: %+v", b.Build())
	}

	b2 := NewPlanBuilder(types.OpOptions{}).Batched()
	if b2.Build().Force != types.ShapeMDSP {
		t.Fatalf("Batched() did not force ShapeMDSP: %+v", b2.Build())
	}

	b3 := NewPlanBuilder(types.OpOptions{}).Atomic().Adaptive()
	if b3.Build().Force != types.ShapeUnspecified {
		t.Fatalf("Adaptive() after Atomic() did not clear the force: %+v", b3.Build())
	}

	b4 := NewPlanBuilder(types.OpOptions{}).Batched().WithReason("cheaper at 240 items").WithBudget(1.5).WithMaxItemsPerCall(10)
	req := b4.Build()
	if req.ForceReason != "cheaper at 240 items" || req.Budget != 1.5 || req.MaxItemsPerCall != 10 {
		t.Fatalf("builder chain did not carry through: %+v", req)
	}
}

// --- PL-003: the batch protocol's coverage classification.

func TestClassifyBatchIDsAllOK(t *testing.T) {
	coverage := classifyBatchIDs(3, []string{"i-000001", "i-000002", "i-000003"})
	if !coverage.OK() {
		t.Fatalf("expected OK coverage, got %+v", coverage)
	}
}

func TestClassifyBatchIDsDetectsMissing(t *testing.T) {
	coverage := classifyBatchIDs(3, []string{"i-000001", "i-000003"})
	if coverage.OK() {
		t.Fatal("coverage reported OK with a missing id")
	}
	if len(coverage.Missing) != 1 || coverage.Missing[0] != "i-000002" {
		t.Fatalf("Missing = %v, want [i-000002]", coverage.Missing)
	}
	if len(coverage.Duplicate) != 0 || len(coverage.Unknown) != 0 {
		t.Fatalf("an omission was also classified as duplicate/unknown: %+v", coverage)
	}
}

func TestClassifyBatchIDsDetectsDuplicate(t *testing.T) {
	coverage := classifyBatchIDs(3, []string{"i-000001", "i-000001", "i-000002", "i-000003"})
	if coverage.OK() {
		t.Fatal("coverage reported OK with a duplicated id")
	}
	if len(coverage.Duplicate) != 1 || coverage.Duplicate[0] != "i-000001" {
		t.Fatalf("Duplicate = %v, want [i-000001]", coverage.Duplicate)
	}
	if len(coverage.Missing) != 0 || len(coverage.Unknown) != 0 {
		t.Fatalf("a duplicate was also classified as missing/unknown: %+v", coverage)
	}
}

func TestClassifyBatchIDsDetectsUnknown(t *testing.T) {
	coverage := classifyBatchIDs(2, []string{"i-000001", "i-000002", "i-999999"})
	if coverage.OK() {
		t.Fatal("coverage reported OK with an invented id")
	}
	if len(coverage.Unknown) != 1 || coverage.Unknown[0] != "i-999999" {
		t.Fatalf("Unknown = %v, want [i-999999]", coverage.Unknown)
	}
	if len(coverage.Missing) != 0 || len(coverage.Duplicate) != 0 {
		t.Fatalf("an invented id was also classified as missing/duplicate: %+v", coverage)
	}
}

// TestClassifyBatchIDsReorderedIsFine: PL-003 requires that reordering alone
// -- no omission, no duplication, no invention -- is not a coverage failure.
// Output position is never trusted, so the response naming every id in a
// different order than offered must still resolve cleanly.
func TestClassifyBatchIDsReorderedIsFine(t *testing.T) {
	coverage := classifyBatchIDs(3, []string{"i-000003", "i-000001", "i-000002"})
	if !coverage.OK() {
		t.Fatalf("a pure reordering was rejected: %+v", coverage)
	}
}

// --- PL-003: NewIDBatchAlgebra's Merge, reconstructing by id and never by
// position.

func mergeBody(pairs ...[2]string) string {
	var b strings.Builder
	b.WriteString(`{"items":[`)
	for i, p := range pairs {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":%q,"output":%s}`, p[0], p[1])
	}
	b.WriteString(`]}`)
	return b.String()
}

func TestNewIDBatchAlgebraMergeReordersByID(t *testing.T) {
	op := widgetOp()
	items := widgets(3)

	// Answered out of order on purpose: item 3's answer arrives first.
	body := mergeBody(
		[2]string{"i-000003", `{"length":30}`},
		[2]string{"i-000001", `{"length":10}`},
		[2]string{"i-000002", `{"length":20}`},
	)

	outputs, err := op.Batch.Merge(body, items)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	want := []int{10, 20, 30}
	for i, s := range outputs {
		if s.Length != want[i] {
			t.Fatalf("outputs[%d].Length = %d, want %d (Merge trusted response order over id)", i, s.Length, want[i])
		}
	}
}

func TestNewIDBatchAlgebraMergeRejectsMissingID(t *testing.T) {
	op := widgetOp()
	items := widgets(3)
	body := mergeBody(
		[2]string{"i-000001", `{"length":10}`},
		[2]string{"i-000002", `{"length":20}`},
	)
	if _, err := op.Batch.Merge(body, items); err == nil {
		t.Fatal("Merge accepted a response missing an offered id")
	}
}

func TestNewIDBatchAlgebraMergeRejectsDuplicateID(t *testing.T) {
	op := widgetOp()
	items := widgets(3)
	body := mergeBody(
		[2]string{"i-000001", `{"length":10}`},
		[2]string{"i-000001", `{"length":11}`},
		[2]string{"i-000002", `{"length":20}`},
	)
	if _, err := op.Batch.Merge(body, items); err == nil {
		t.Fatal("Merge accepted a response with a duplicated id")
	}
}

func TestNewIDBatchAlgebraMergeRejectsUnknownID(t *testing.T) {
	op := widgetOp()
	items := widgets(2)
	body := mergeBody(
		[2]string{"i-000001", `{"length":10}`},
		[2]string{"i-000002", `{"length":20}`},
		[2]string{"i-999999", `{"length":99}`},
	)
	if _, err := op.Batch.Merge(body, items); err == nil {
		t.Fatal("Merge accepted a response with an invented id")
	}
}

// --- PL-004: token-aware chunk packing, each bound checked in isolation.

func uniformSizes(n, tokensEach, bytesEach int) []itemSize {
	sizes := make([]itemSize, n)
	for i := range sizes {
		sizes[i] = itemSize{tokens: tokensEach, bytes: bytesEach}
	}
	return sizes
}

func TestPackChunksItemCountBound(t *testing.T) {
	sizes := uniformSizes(10, 1, 1)
	budget := chunkBudget{maxItemsPerCall: 3, contextLimit: 1_000_000, outputTokensPerItem: 1}
	chunks, oversized, err := packChunks(sizes, budget)
	if err != nil {
		t.Fatalf("packChunks: %v", err)
	}
	if len(oversized) != 0 {
		t.Fatalf("unexpected oversized items: %v", oversized)
	}
	for _, c := range chunks {
		if c.ItemCount > 3 {
			t.Fatalf("chunk of %d items exceeds the item-count bound of 3", c.ItemCount)
		}
	}
	if chunks[0].ItemCount != 3 || chunks[0].BoundingRule != string(boundItemCount) {
		t.Fatalf("first chunk = %+v, want 3 items bound by item_count", chunks[0])
	}
}

func TestPackChunksInputTokensBound(t *testing.T) {
	sizes := uniformSizes(10, 40, 1)
	budget := chunkBudget{maxInputTokens: 100, contextLimit: 1_000_000, outputTokensPerItem: 1}
	chunks, _, err := packChunks(sizes, budget)
	if err != nil {
		t.Fatalf("packChunks: %v", err)
	}
	// floor(100/40) = 2 items fit before a third (120 tokens) would not.
	if chunks[0].ItemCount != 2 {
		t.Fatalf("first chunk = %d items, want 2 (input_tokens bound)", chunks[0].ItemCount)
	}
	if chunks[0].BoundingRule != string(boundInputTokens) {
		t.Fatalf("BoundingRule = %q, want %q", chunks[0].BoundingRule, boundInputTokens)
	}
}

func TestPackChunksOutputReserveBound(t *testing.T) {
	sizes := uniformSizes(10, 1, 1)
	budget := chunkBudget{outputReserve: 100, outputTokensPerItem: 40, contextLimit: 1_000_000}
	chunks, _, err := packChunks(sizes, budget)
	if err != nil {
		t.Fatalf("packChunks: %v", err)
	}
	if chunks[0].ItemCount != 2 {
		t.Fatalf("first chunk = %d items, want 2 (output_reserve bound)", chunks[0].ItemCount)
	}
	if chunks[0].BoundingRule != string(boundOutputReserve) {
		t.Fatalf("BoundingRule = %q, want %q", chunks[0].BoundingRule, boundOutputReserve)
	}
}

func TestPackChunksContextLimitBound(t *testing.T) {
	sizes := uniformSizes(10, 30, 1)
	budget := chunkBudget{contextLimit: 100, outputTokensPerItem: 1}
	chunks, _, err := packChunks(sizes, budget)
	if err != nil {
		t.Fatalf("packChunks: %v", err)
	}
	// Each item costs 30 (input) + 1 (output) = 31 toward the 100 context
	// limit; three items = 93, a fourth would push it to 124.
	if chunks[0].ItemCount != 3 {
		t.Fatalf("first chunk = %d items, want 3 (context_limit bound)", chunks[0].ItemCount)
	}
	if chunks[0].BoundingRule != string(boundContextLimit) {
		t.Fatalf("BoundingRule = %q, want %q", chunks[0].BoundingRule, boundContextLimit)
	}
}

func TestPackChunksPayloadBytesBound(t *testing.T) {
	sizes := uniformSizes(10, 1, 500)
	budget := chunkBudget{maxPayloadBytes: 1200, contextLimit: 1_000_000, outputTokensPerItem: 1}
	chunks, _, err := packChunks(sizes, budget)
	if err != nil {
		t.Fatalf("packChunks: %v", err)
	}
	if chunks[0].ItemCount != 2 {
		t.Fatalf("first chunk = %d items, want 2 (payload_bytes bound)", chunks[0].ItemCount)
	}
	if chunks[0].BoundingRule != string(boundPayloadBytes) {
		t.Fatalf("BoundingRule = %q, want %q", chunks[0].BoundingRule, boundPayloadBytes)
	}
}

func TestPackChunksPerCallCostBound(t *testing.T) {
	sizes := uniformSizes(10, 100, 1)
	budget := chunkBudget{
		contextLimit:           1_000_000,
		outputTokensPerItem:    0,
		maxCostPerCallUSD:      0.05,
		costPerPromptToken:     0.25, // $0.25 per 1K tokens -> $0.025 per 100-token item
		costPerCompletionToken: 0,
	}
	chunks, _, err := packChunks(sizes, budget)
	if err != nil {
		t.Fatalf("packChunks: %v", err)
	}
	// Each item costs 100 * 0.25/1000 = $0.025; two items = $0.05 (at the
	// ceiling), three items = $0.075 (over it).
	if chunks[0].ItemCount != 2 {
		t.Fatalf("first chunk = %d items, want 2 (per_call_cost bound)", chunks[0].ItemCount)
	}
	if chunks[0].BoundingRule != string(boundPerCallCost) {
		t.Fatalf("BoundingRule = %q, want %q", chunks[0].BoundingRule, boundPerCallCost)
	}
}

// TestPackChunksOversizedItemNamesTheBound: an item too large for any chunk
// is reported, not silently dropped or truncated, and the report says which
// bound it broke.
func TestPackChunksOversizedItemNamesTheBound(t *testing.T) {
	sizes := []itemSize{
		{tokens: 1, bytes: 10},
		{tokens: 1, bytes: 5000}, // alone, this one breaks the payload bound.
		{tokens: 1, bytes: 10},
	}
	budget := chunkBudget{maxPayloadBytes: 1000, contextLimit: 1_000_000, outputTokensPerItem: 1}
	chunks, oversized, err := packChunks(sizes, budget)
	if err != nil {
		t.Fatalf("packChunks: %v", err)
	}
	if len(oversized) != 1 || oversized[0].Index != 1 {
		t.Fatalf("oversized = %+v, want item 1 flagged", oversized)
	}
	if oversized[0].Bound != string(boundPayloadBytes) {
		t.Fatalf("oversized bound = %q, want %q", oversized[0].Bound, boundPayloadBytes)
	}
	// The two small items still packed together, so the oversized item did
	// not block everything after it.
	total := 0
	for _, c := range chunks {
		total += c.ItemCount
	}
	if total != 2 {
		t.Fatalf("chunked item total = %d, want 2 (the two non-oversized items)", total)
	}
}

// --- PL-004/PR-002: an unpriced model reports unpriced, never $0.00.

func TestCostFromTokensUnpricedModelReportsUnpriced(t *testing.T) {
	est := costFromTokens("definitely-not-a-real-model-xyz", 10_000, 5_000)
	if est.Priced {
		t.Fatal("an unrecognised model reported Priced = true")
	}
	if est.EstimatedCost != 0 {
		t.Fatalf("EstimatedCost = %v on an unpriced model; want the zero value to be read as unpriced, not spent", est.EstimatedCost)
	}
	if est.EstimatedPromptTokens != 10_000 || est.EstimatedCompletionTokens != 5_000 {
		t.Fatalf("token estimate not carried through on the unpriced path: %+v", est)
	}
}

func TestCostFromTokensPricedModelReportsRealCost(t *testing.T) {
	est := costFromTokens("gpt-4", 1000, 1000)
	if !est.Priced {
		t.Fatal("gpt-4 has a pricing entry and should report Priced = true")
	}
	if est.EstimatedCost <= 0 {
		t.Fatalf("EstimatedCost = %v for a priced model, want > 0", est.EstimatedCost)
	}
}

// --- PL-005: adaptive chunk sizing, bounded, with a recorded reason.

func TestAdaptiveChunkStateHalvesOnTruncationAndConverges(t *testing.T) {
	state := NewAdaptiveChunkState(25, 200)

	// A provider that truncates above 20 items: every chunk above 20 fails.
	for i := 0; i < 10; i++ {
		if state.Size() > 20 {
			state.Record(OutcomeTruncated)
		} else {
			state.Record(OutcomeCompliant)
			state.Record(OutcomeCompliant)
			state.Record(OutcomeCompliant)
		}
	}

	if state.Size() > 20 {
		t.Fatalf("size = %d after repeated truncation above 20; did not converge under the ceiling", state.Size())
	}
	if state.Reason() == "" {
		t.Fatal("Reason() is empty; PL-005 requires the plan to explain why")
	}

	// Now it should be stable: further compliant runs below the ceiling grow
	// it back toward 20 and no further truncation should occur once it is
	// there, but a single more truncation still halves -- proving the state
	// is reactive, not stuck.
	stableSize := state.Size()
	state.Record(OutcomeTruncated)
	if state.Size() >= stableSize {
		t.Fatalf("a truncation did not reduce size: before %d, after %d", stableSize, state.Size())
	}
}

func TestAdaptiveChunkStateGrowsOnSustainedCompliance(t *testing.T) {
	state := NewAdaptiveChunkState(4, 32)
	for i := 0; i < 3; i++ {
		state.Record(OutcomeCompliant)
	}
	if state.Size() <= 4 {
		t.Fatalf("size = %d after 3 consecutive compliant chunks, want growth above the start of 4", state.Size())
	}
	if state.Size() > 32 {
		t.Fatalf("size = %d exceeds the operation's own ceiling of 32", state.Size())
	}
}

func TestAdaptiveChunkStateNeverExceedsDeterministicMax(t *testing.T) {
	state := NewAdaptiveChunkState(10, 12)
	for i := 0; i < 50; i++ {
		state.Record(OutcomeCompliant)
	}
	if state.Size() > 12 {
		t.Fatalf("size = %d, exceeded the deterministic max of 12", state.Size())
	}
}

func TestAdaptiveChunkStateHalvesAfterSustainedRepairs(t *testing.T) {
	state := NewAdaptiveChunkState(16, 64)
	for i := 0; i < RepairRateThreshold; i++ {
		state.Record(OutcomeRepaired)
	}
	if state.Size() != 8 {
		t.Fatalf("size = %d after %d consecutive repairs, want 8 (halved from 16)", state.Size(), RepairRateThreshold)
	}
}

func TestAdaptiveChunkStateSingleRepairDoesNotHalve(t *testing.T) {
	state := NewAdaptiveChunkState(16, 64)
	state.Record(OutcomeRepaired)
	if state.Size() != 16 {
		t.Fatalf("size = %d after one repair, want unchanged at 16 (below RepairRateThreshold)", state.Size())
	}
}

// --- PL-006: a stable batched operation must declare an algebra kind.

func TestNewOpRequiresAlgebraKindForBatchedStableOp(t *testing.T) {
	_, err := NewOp(Op[string, string]{
		ID:        types.OperationID{Name: "batched", Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityStable},
		Batch:     BatchAlgebra[string, string]{Class: types.BatchItemwise}, // Kind left unspecified.
	})
	if err == nil {
		t.Fatal("NewOp accepted a stable BatchItemwise op with no algebra kind")
	}
	if !strings.Contains(err.Error(), "algebra kind") {
		t.Fatalf("error does not name the problem: %v", err)
	}

	op, err := NewOp(Op[string, string]{
		ID:        types.OperationID{Name: "batched", Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityStable},
		Batch:     BatchAlgebra[string, string]{Class: types.BatchItemwise, Kind: types.AlgebraIndependent},
	})
	if err != nil {
		t.Fatalf("NewOp rejected a stable op that declared both class and kind: %v", err)
	}
	if op.Batch.Kind != types.AlgebraIndependent {
		t.Fatalf("Kind did not round-trip: %v", op.Batch.Kind)
	}
}

func TestNewOpAllowsBatchNoneWithoutAlgebraKind(t *testing.T) {
	// BatchNone has nothing to validate a response against, so it is exempt
	// from the Kind requirement the same way it is exempt from Encode/Merge.
	_, err := NewOp(Op[string, string]{
		ID:        types.OperationID{Name: "single", Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityStable},
		Batch:     BatchAlgebra[string, string]{Class: types.BatchNone},
	})
	if err != nil {
		t.Fatalf("NewOp rejected a stable BatchNone op for missing an algebra kind: %v", err)
	}
}

// --- PL-007: the plural API, RunOpMany.

func TestRunOpManyAtomicPreservesOrder(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	setLLMCaller(func(_ context.Context, _, user string, _ types.OpOptions) (string, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return "GOT:" + user, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	op := atomicOnlyOp()
	items := make([]string, 25)
	for i := range items {
		items[i] = fmt.Sprintf("item-%02d", i)
	}

	results, plan, err := RunOpMany(context.Background(), op, items, types.PlanRequest{})
	if err != nil {
		t.Fatalf("RunOpMany: %v", err)
	}
	if plan.Shape != types.ShapeAtomic {
		t.Fatalf("Shape = %s, want atomic (BatchNone operation)", plan.Shape)
	}
	if len(results) != len(items) {
		t.Fatalf("got %d results for %d items", len(results), len(items))
	}
	for i, item := range items {
		want := "GOT:" + item
		if results[i].Value != want {
			t.Fatalf("results[%d] = %q, want %q (order not preserved)", i, results[i].Value, want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != len(items) {
		t.Fatalf("made %d calls for %d items, want exactly one call per item", calls, len(items))
	}
}

func TestRunOpManyBatchedResolvesByID(t *testing.T) {
	setLLMCaller(func(_ context.Context, _, user string, _ types.OpOptions) (string, error) {
		var tagged []struct {
			ID   string `json:"id"`
			Item widget `json:"item"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(user, "Process these items:\n")), &tagged); err != nil {
			t.Fatalf("test provider could not parse the batch request: %v", err)
		}
		// Answer in reverse order, to prove RunOpMany resolves by id rather
		// than trusting response position.
		var pairs [][2]string
		for i := len(tagged) - 1; i >= 0; i-- {
			length := len(tagged[i].Item.Name)
			pairs = append(pairs, [2]string{tagged[i].ID, fmt.Sprintf(`{"length":%d}`, length)})
		}
		return mergeBody(pairs...), nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	op := widgetOp()
	items := widgets(5)

	results, plan, err := RunOpMany(context.Background(), op, items, types.PlanRequest{})
	if err != nil {
		t.Fatalf("RunOpMany: %v", err)
	}
	if plan.Shape != types.ShapeMDSP {
		t.Fatalf("Shape = %s, want mdsp", plan.Shape)
	}
	if len(results) != len(items) {
		t.Fatalf("got %d results for %d items", len(results), len(items))
	}
	for i, item := range items {
		if results[i].Value.Length != len(item.Name) {
			t.Fatalf("results[%d].Length = %d, want %d (item %q resolved to the wrong output)",
				i, results[i].Value.Length, len(item.Name), item.Name)
		}
	}
}

func TestRunOpManyRefusesIllegalForcedShape(t *testing.T) {
	_, _, err := RunOpMany(context.Background(), atomicOnlyOp(), []string{"a", "b"},
		types.PlanRequest{Force: types.ShapeMDSP})
	if err == nil {
		t.Fatal("RunOpMany ran a shape its operation cannot support")
	}
}
