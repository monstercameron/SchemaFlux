package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
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

func uniformSizes(n, tokensEach int) []itemSize {
	sizes := make([]itemSize, n)
	for i := range sizes {
		sizes[i] = itemSize{tokens: tokensEach}
	}
	return sizes
}

func TestPackChunksItemCountBound(t *testing.T) {
	sizes := uniformSizes(10, 1)
	budget := chunkBudget{maxItemsPerCall: 3, contextLimit: 1_000_000, outputTokensPerItem: 1}
	chunks, oversized := packChunks(sizes, budget)
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

func TestPackChunksOutputReserveBound(t *testing.T) {
	sizes := uniformSizes(10, 1)
	budget := chunkBudget{outputReserve: 100, outputTokensPerItem: 40, contextLimit: 1_000_000}
	chunks, _ := packChunks(sizes, budget)
	if chunks[0].ItemCount != 2 {
		t.Fatalf("first chunk = %d items, want 2 (output_reserve bound)", chunks[0].ItemCount)
	}
	if chunks[0].BoundingRule != string(boundOutputReserve) {
		t.Fatalf("BoundingRule = %q, want %q", chunks[0].BoundingRule, boundOutputReserve)
	}
}

func TestPackChunksContextLimitBound(t *testing.T) {
	sizes := uniformSizes(10, 30)
	budget := chunkBudget{contextLimit: 100, outputTokensPerItem: 1}
	chunks, _ := packChunks(sizes, budget)
	// Each item costs 30 (input) + 1 (output) = 31 toward the 100 context
	// limit; three items = 93, a fourth would push it to 124.
	if chunks[0].ItemCount != 3 {
		t.Fatalf("first chunk = %d items, want 3 (context_limit bound)", chunks[0].ItemCount)
	}
	if chunks[0].BoundingRule != string(boundContextLimit) {
		t.Fatalf("BoundingRule = %q, want %q", chunks[0].BoundingRule, boundContextLimit)
	}
}

func TestPackChunksPerCallCostBound(t *testing.T) {
	sizes := uniformSizes(10, 100)
	budget := chunkBudget{
		contextLimit:           1_000_000,
		outputTokensPerItem:    0,
		maxCostPerCallUSD:      0.05,
		costPerPromptToken:     0.25, // $0.25 per 1K tokens -> $0.025 per 100-token item
		costPerCompletionToken: 0,
	}
	chunks, _ := packChunks(sizes, budget)
	// Each item costs 100 * 0.25/1000 = $0.025; two items = $0.05 (at the
	// ceiling), three items = $0.075 (over it).
	if chunks[0].ItemCount != 2 {
		t.Fatalf("first chunk = %d items, want 2 (per_call_cost bound)", chunks[0].ItemCount)
	}
	if chunks[0].BoundingRule != string(boundPerCallCost) {
		t.Fatalf("BoundingRule = %q, want %q", chunks[0].BoundingRule, boundPerCallCost)
	}
}

// A plan carries the version of the estimator that made it, so that a plan kept
// from an older estimator is not mistaken for one made by this one.
func TestPreflightRecordsTheEstimatorVersionItUsed(t *testing.T) {
	plan, err := Preflight(widgetOp(), widgets(3), types.PlanRequest{Options: types.OpOptions{Model: "gpt-4"}})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if plan.EstimatorVersion != "plan-estimator-4" {
		t.Errorf("EstimatorVersion = %q", plan.EstimatorVersion)
	}
}

// The request's budget bounds what one call may cost, so a request that may
// spend little is cut into more calls than one that may spend freely.
func TestPlanChunksBoundEachCallByTheRequestsBudget(t *testing.T) {
	sizes := make([]itemSize, 100)
	for i := range sizes {
		sizes[i] = itemSize{tokens: 1000}
	}
	capability := types.CapabilitySnapshot{Model: "gpt-4", MaxOutputTokens: 1 << 30, ContextTokens: 1 << 30}
	free, _ := planChunks(context.Background(), sizes, types.PlanRequest{MaxItemsPerCall: 100}, capability, 0)
	bounded, _ := planChunks(context.Background(), sizes, types.PlanRequest{MaxItemsPerCall: 100, Budget: 0.5}, capability, 0)
	if len(bounded) <= len(free) {
		t.Errorf("a budget of 0.5 left %d chunks against %d without one: no single call may cost more than the whole request may", len(bounded), len(free))
	}
}

// TestPackChunksOversizedItemNamesTheBound: an item too large for any chunk
// is reported, not silently dropped or truncated, and the report says which
// bound it broke.
// Every item is in exactly one place: a chunk, or the list of items that fit no
// chunk. That is held here, for every mix of sizes and bounds.
func TestPackChunksPlacesEveryItemExactlyOnce(t *testing.T) {
	budgets := map[string]chunkBudget{
		"no bounds":                {},
		"an item count bound":      {maxItemsPerCall: 3},
		"a context bound":          {contextLimit: 1000, outputTokensPerItem: 1},
		"an output reserve":        {outputReserve: 100, outputTokensPerItem: 20},
		"a context and a count":    {maxItemsPerCall: 4, contextLimit: 500, outputTokensPerItem: 1},
		"a bound no item can meet": {maxItemsPerCall: 1, contextLimit: 1, outputTokensPerItem: 5},
	}
	sizeSets := map[string][]int{
		"none":                    nil,
		"one small":               {1},
		"one too large":           {5000},
		"all small":               {1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		"large ones at both ends": {5000, 1, 1, 5000},
		"alternating":             {1, 5000, 1, 5000, 1, 5000, 1},
		"all too large":           {5000, 5000, 5000},
		"every size":              {0, 1, 10, 100, 400, 499, 500, 501, 999, 1000, 1001, 5000},
	}
	for budgetName, budget := range budgets {
		for setName, tokens := range sizeSets {
			t.Run(budgetName+", "+setName, func(t *testing.T) {
				sizes := make([]itemSize, len(tokens))
				for i, count := range tokens {
					sizes[i] = itemSize{tokens: count}
				}
				chunks, oversized := packChunks(sizes, budget)
				placed := len(oversized)
				for _, chunk := range chunks {
					if chunk.ItemCount < 1 {
						t.Fatalf("a chunk holds %d items", chunk.ItemCount)
					}
					placed += chunk.ItemCount
				}
				if placed != len(sizes) {
					t.Errorf("%d items were placed out of %d (chunks %+v, oversized %+v)", placed, len(sizes), chunks, oversized)
				}
				seen := map[int]bool{}
				for _, item := range oversized {
					if item.Index < 0 || item.Index >= len(sizes) || seen[item.Index] {
						t.Errorf("oversized item index %d is out of range or repeated", item.Index)
					}
					seen[item.Index] = true
				}
			})
		}
	}
}

func TestPackChunksOversizedItemNamesTheBound(t *testing.T) {
	sizes := []itemSize{
		{tokens: 1},
		{tokens: 5000}, // alone, this one breaks the context limit.
		{tokens: 1},
	}
	budget := chunkBudget{contextLimit: 1000, outputTokensPerItem: 1}
	chunks, oversized := packChunks(sizes, budget)
	if len(oversized) != 1 || oversized[0].Index != 1 {
		t.Fatalf("oversized = %+v, want item 1 flagged", oversized)
	}
	if oversized[0].Bound != string(boundContextLimit) {
		t.Fatalf("oversized bound = %q, want %q", oversized[0].Bound, boundContextLimit)
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
	est := costFromTokens(context.Background(), "definitely-not-a-real-model-xyz", 10_000, 5_000)
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
	est := costFromTokens(context.Background(), "gpt-4", 1000, 1000)
	if !est.Priced {
		t.Fatal("gpt-4 has a pricing entry and should report Priced = true")
	}
	if est.EstimatedCost <= 0 {
		t.Fatalf("EstimatedCost = %v for a priced model, want > 0", est.EstimatedCost)
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

// planManyBatchCaller answers a batch request for widgetOp with each item's
// name length, in the order asked.
func planManyBatchCaller(t *testing.T) LLMCaller {
	return func(_ context.Context, _, user string, _ types.OpOptions) (string, error) {
		body, err := planManyBatchAnswer(user)
		if err != nil {
			t.Errorf("test provider could not parse the batch request: %v", err)
		}
		return body, err
	}
}

func planManyBatchAnswer(user string) (string, error) {
	var tagged []struct {
		ID   string `json:"id"`
		Item widget `json:"item"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(user, "Process these items:\n")), &tagged); err != nil {
		return "", err
	}
	var pairs [][2]string
	for _, item := range tagged {
		pairs = append(pairs, [2]string{item.ID, fmt.Sprintf(`{"length":%d}`, len(item.Item.Name))})
	}
	return mergeBody(pairs...), nil
}

// planManyProvider answers widgetOp's batch requests and reports token usage,
// so the chunk's spend is something a test can read back off the results.
type planManyProvider struct{}

func (planManyProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	body, err := planManyBatchAnswer(req.UserPrompt)
	if err != nil {
		return llm.CompletionResponse{}, err
	}
	return llm.CompletionResponse{
		Content:      body,
		Provider:     "planmany",
		Model:        "plan-many-model",
		FinishReason: "stop",
		Usage:        types.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
	}, nil
}

func (planManyProvider) Name() string                               { return "planmany" }
func (planManyProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (planManyProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }

// One call served a whole chunk, so summing a per-result figure over the
// results has to give that call's figure once. Every item carried the chunk's
// whole Meta, and a caller totalling Meta.Attempts or Meta.Cost over a hundred
// items of one chunk got a hundred times what was spent.
func TestRunOpManyReportsAChunksSpendOnce(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "plan-many-model")
	ctx := WithProvider(context.Background(), planManyProvider{})

	items := widgets(6)
	results, plan, err := RunOpMany(ctx, widgetOp(), items, types.PlanRequest{})
	if err != nil {
		t.Fatalf("RunOpMany: %v", err)
	}
	if len(plan.Chunks) != 1 || plan.Chunks[0].ItemCount != 6 {
		t.Fatalf("the fixture was meant to plan one chunk of six, got %+v", plan.Chunks)
	}

	if results[0].Meta.Attempts < 1 || results[0].Meta.Usage.TotalTokens != 150 {
		t.Fatalf("the chunk's first item carries attempts %d and %d tokens; the call's record was lost",
			results[0].Meta.Attempts, results[0].Meta.Usage.TotalTokens)
	}
	total, tokens := 0, 0
	for i, result := range results {
		total += result.Meta.Attempts
		tokens += result.Meta.Usage.TotalTokens
		if i > 0 {
			if result.Meta.Attempts != 0 || result.Meta.Repairs != 0 || result.Meta.Elapsed != 0 ||
				result.Meta.Usage != (types.TokenUsage{}) || result.Meta.Cost != (types.CostInfo{}) {
				t.Errorf("item %d repeats the chunk's spend: %+v", i, result.Meta)
			}
		}
		if result.Meta.Operation != results[0].Meta.Operation || result.Meta.Model != results[0].Meta.Model {
			t.Errorf("item %d lost the chunk's descriptive fields: %+v", i, result.Meta)
		}
	}
	if total != results[0].Meta.Attempts {
		t.Errorf("summed attempts = %d, the call took %d", total, results[0].Meta.Attempts)
	}
	if tokens != 150 {
		t.Errorf("summed tokens = %d, the call used 150", tokens)
	}
}

func TestItemMetaFromChunk(t *testing.T) {
	chunk := types.Meta{
		Operation:     "widgetSummarize",
		Provider:      "p",
		Model:         "m",
		SchemaID:      "s",
		Usage:         types.TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		Cost:          types.CostInfo{Priced: true, TotalCost: 0.5, Currency: "USD"},
		CacheHitRatio: 0.25,
		Attempts:      2,
		Repairs:       1,
		Elapsed:       3 * time.Second,
		Timing:        types.Timing{Calls: 2},
		Checks:        []types.Check{{Name: "schema", Passed: true}},
		ModelClaims:   map[string]any{"k": "v"},
	}

	first := itemMetaFromChunk(chunk, true)
	if first.Usage != chunk.Usage || first.Cost != chunk.Cost || first.Attempts != 2 || first.Repairs != 1 ||
		first.Elapsed != 3*time.Second || first.CacheHitRatio != 0.25 || first.Timing.Calls != 2 {
		t.Errorf("the first item must carry the chunk's whole spend: %+v", first)
	}

	rest := itemMetaFromChunk(chunk, false)
	if rest.Usage != (types.TokenUsage{}) || rest.Cost != (types.CostInfo{}) || rest.Attempts != 0 || rest.Repairs != 0 ||
		rest.Elapsed != 0 || rest.CacheHitRatio != 0 || rest.Timing != (types.Timing{}) {
		t.Errorf("a later item must carry none of it: %+v", rest)
	}
	for name, got := range map[string]types.Meta{"first": first, "rest": rest} {
		if got.Operation != "widgetSummarize" || got.Provider != "p" || got.Model != "m" || got.SchemaID != "s" {
			t.Errorf("%s lost its descriptive fields: %+v", name, got)
		}
		if len(got.Checks) != 1 || got.Checks[0].Name != "schema" || got.ModelClaims["k"] != "v" {
			t.Errorf("%s lost its checks or claims: %+v", name, got)
		}
	}

	// The copies are independent of the chunk's and of each other.
	first.Checks[0].Name = "changed"
	first.ModelClaims["k"] = "changed"
	if chunk.Checks[0].Name != "schema" || chunk.ModelClaims["k"] != "v" || rest.Checks[0].Name != "schema" || rest.ModelClaims["k"] != "v" {
		t.Error("the items share their Checks or ModelClaims with the chunk")
	}

	// Nil slices and maps stay nil-safe.
	empty := itemMetaFromChunk(types.Meta{Attempts: 1}, false)
	if len(empty.Checks) != 0 || len(empty.ModelClaims) != 0 {
		t.Errorf("empty chunk meta grew content: %+v", empty)
	}
}

func TestRunManyBatchedRefusesAPlanThatLeavesItemsUncovered(t *testing.T) {
	setLLMCaller(planManyBatchCaller(t))
	t.Cleanup(func() { setLLMCaller(nil) })

	items := widgets(5)
	plan := types.Plan{
		Shape:  types.ShapeMDSP,
		Chunks: []types.ChunkPlan{{ItemCount: 2}},
	}
	results, _, err := runManyBatched(context.Background(), widgetOp(), items, types.OpOptions{}, plan)
	if err == nil {
		t.Fatal("a plan whose chunks cover two of five items ran")
	}
	if results != nil {
		t.Errorf("results were returned beside the error: %+v", results)
	}

	over := types.Plan{Shape: types.ShapeMDSP, Chunks: []types.ChunkPlan{{ItemCount: 9}}}
	if _, _, err := runManyBatched(context.Background(), widgetOp(), items, types.OpOptions{}, over); err == nil {
		t.Fatal("a plan whose chunk is larger than the items ran")
	}

	exact := types.Plan{Shape: types.ShapeMDSP, Chunks: []types.ChunkPlan{{ItemCount: 3}, {ItemCount: 2}}}
	results, _, err = runManyBatched(context.Background(), widgetOp(), items, types.OpOptions{}, exact)
	if err != nil {
		t.Fatalf("a plan that covers every item was refused: %v", err)
	}
	if len(results) != 5 || results[4].Value.Length != len(items[4].Name) {
		t.Errorf("results = %+v", results)
	}
}

func TestRunOpManyRefusesIllegalForcedShape(t *testing.T) {
	_, _, err := RunOpMany(context.Background(), atomicOnlyOp(), []string{"a", "b"},
		types.PlanRequest{Force: types.ShapeMDSP})
	if err == nil {
		t.Fatal("RunOpMany ran a shape its operation cannot support")
	}
}

// A batched plan's prompt estimate has to count the items themselves. It once
// counted only the id protocol's scaffolding, a few tokens per item, so a
// hundred one-kilobyte items were estimated at about six hundred prompt tokens
// when they are about twenty-five thousand, and batching looked an order of
// magnitude cheaper than sending the same items one at a time.
func TestPreflightCountsTheItemsInABatchedPromptEstimate(t *testing.T) {
	withoutReinforcement(t)
	items := make([]widget, 100)
	for i := range items {
		items[i] = widget{Name: strings.Repeat("x", 1000)}
	}
	itemTokens := estimateTokens(`{"Name":"` + strings.Repeat("x", 1000) + `"}`)

	batched, err := Preflight(widgetOp(), items, types.PlanRequest{Force: types.ShapeMDSP})
	if err != nil || !batched.Eligible {
		t.Fatalf("batched plan: %v, eligible %v (%s)", err, batched.Eligible, batched.IneligibleReason)
	}
	atomicPlan, err := Preflight(widgetOp(), items, types.PlanRequest{Force: types.ShapeAtomic})
	if err != nil || !atomicPlan.Eligible {
		t.Fatalf("atomic plan: %v", err)
	}

	got := batched.EstimatedCost.EstimatedPromptTokens
	if got < 100*itemTokens {
		t.Errorf("EstimatedPromptTokens = %d, want at least the items' own %d", got, 100*itemTokens)
	}
	if got < atomicPlan.EstimatedCost.EstimatedPromptTokens {
		t.Errorf("a batched plan estimates %d prompt tokens, fewer than the %d the same items cost sent alone",
			got, atomicPlan.EstimatedCost.EstimatedPromptTokens)
	}

	small := make([]widget, 100)
	for i := range small {
		small[i] = widget{Name: "x"}
	}
	smallPlan, err := Preflight(widgetOp(), small, types.PlanRequest{Force: types.ShapeMDSP})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if smallPlan.EstimatedCost.EstimatedPromptTokens >= got {
		t.Errorf("tiny items estimate %d prompt tokens, large ones %d: item size does not reach the estimate",
			smallPlan.EstimatedCost.EstimatedPromptTokens, got)
	}
}

// A batched plan's estimate is the prompt tokens of the requests as rendered, as
// given, and an answer for every item: the planning assumption for each chunked
// one and the model's ceiling for each that is sent alone. The prompt side is
// the descriptor's (planBatch); this is the arithmetic that prices it.
func TestEstimateChunkedCostPricesTheRenderedPromptAndEveryAnswer(t *testing.T) {
	capability := types.CapabilitySnapshot{Model: "definitely-not-a-real-model-xyz", MaxOutputTokens: 100}
	cases := []struct {
		name          string
		prompt        int
		chunks        []types.ChunkPlan
		oversized     []types.OversizedItem
		wantCompleted int
	}{
		{"nothing", 0, nil, nil, 0},
		{"one chunk of three", 30, []types.ChunkPlan{{ItemCount: 3}}, nil, 3 * outputTokensPerItemDefault},
		{"two chunks", 200, []types.ChunkPlan{{ItemCount: 2}, {ItemCount: 2}}, nil, 4 * outputTokensPerItemDefault},
		{"one oversized item sent alone", 800, []types.ChunkPlan{{ItemCount: 1}}, []types.OversizedItem{{Index: 1}}, outputTokensPerItemDefault + 100},
		{"only oversized items", 140, nil, []types.OversizedItem{{Index: 0}, {Index: 1}}, 200},
		{"a large prompt", 10000, []types.ChunkPlan{{ItemCount: 10}}, nil, 10 * outputTokensPerItemDefault},
		{"one item", 5, []types.ChunkPlan{{ItemCount: 1}}, nil, outputTokensPerItemDefault},
		{"a prompt of nothing still pays for the answers", 0, []types.ChunkPlan{{ItemCount: 5}}, nil, 5 * outputTokensPerItemDefault},
		{"three oversized among chunked", 60, []types.ChunkPlan{{ItemCount: 3}}, []types.OversizedItem{{Index: 3}, {Index: 4}, {Index: 5}}, 3*outputTokensPerItemDefault + 300},
		{"uneven chunks", 321, []types.ChunkPlan{{ItemCount: 1}, {ItemCount: 5}, {ItemCount: 2}}, nil, 8 * outputTokensPerItemDefault},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := estimateChunkedCost(context.Background(), tc.prompt, tc.chunks, tc.oversized, capability)
			if got.EstimatedPromptTokens != tc.prompt {
				t.Errorf("EstimatedPromptTokens = %d, want the %d it was given", got.EstimatedPromptTokens, tc.prompt)
			}
			if got.EstimatedCompletionTokens != tc.wantCompleted {
				t.Errorf("EstimatedCompletionTokens = %d, want %d", got.EstimatedCompletionTokens, tc.wantCompleted)
			}
			if got.Priced {
				t.Errorf("a model with no price reported itself priced: %+v", got)
			}
		})
	}
}

// tokensOfBytes is estimateTokens for a length, so the two cannot disagree about
// what a payload weighs.
func TestTokensOfBytesAgreesWithEstimateTokens(t *testing.T) {
	for length := 0; length <= 64; length++ {
		if got, want := tokensOfBytes(length), estimateTokens(strings.Repeat("x", length)); got != want {
			t.Errorf("tokensOfBytes(%d) = %d, estimateTokens = %d", length, got, want)
		}
	}
}

// The budget bounds the whole request. It used to bound one chunk at most, and
// only for a priced model, so a request estimated at forty dollars went ahead
// under a one dollar budget.
func TestPreflightRefusesAPlanOverItsBudget(t *testing.T) {
	items := widgets(200)
	cases := []struct {
		name         string
		model        string
		budget       float64
		force        types.ExecutionShape
		wantEligible bool
		wantReason   string
	}{
		{"batched, a budget nothing could meet", "gpt-4", 1e-9, types.ShapeMDSP, false, "over the budget"},
		{"atomic, a budget nothing could meet", "gpt-4", 1e-9, types.ShapeAtomic, false, "over the budget"},
		{"batched, a generous budget", "gpt-4", 1e6, types.ShapeMDSP, true, ""},
		{"atomic, a generous budget", "gpt-4", 1e6, types.ShapeAtomic, true, ""},
		{"no budget set", "gpt-4", 0, types.ShapeAtomic, true, ""},
		{"a negative budget is no budget", "gpt-4", -5, types.ShapeMDSP, true, ""},
		{"unpriced model, atomic, with a budget", "definitely-not-a-real-model-xyz", 5, types.ShapeAtomic, false, "no price"},
		{"unpriced model, batched, with a budget", "definitely-not-a-real-model-xyz", 5, types.ShapeMDSP, false, "no price"},
		{"unpriced model, no budget", "definitely-not-a-real-model-xyz", 0, types.ShapeMDSP, true, ""},
		{"unforced plan over budget", "gpt-4", 1e-9, types.ShapeUnspecified, false, "over the budget"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := types.PlanRequest{Options: types.OpOptions{Model: tc.model}, Force: tc.force, Budget: tc.budget}
			plan, err := Preflight(widgetOp(), items, req)
			if err != nil {
				t.Fatalf("Preflight: %v", err)
			}
			if plan.Eligible != tc.wantEligible {
				t.Fatalf("Eligible = %v (%s), want %v", plan.Eligible, plan.IneligibleReason, tc.wantEligible)
			}
			if tc.wantReason != "" && !strings.Contains(plan.IneligibleReason, tc.wantReason) {
				t.Errorf("IneligibleReason = %q, want it to contain %q", plan.IneligibleReason, tc.wantReason)
			}
		})
	}
}

func TestPreflightShapeReasonDoesNotClaimOneCall(t *testing.T) {
	plan, err := Preflight(widgetOp(), widgets(5000), types.PlanRequest{Options: types.OpOptions{MaxOutputTokens: 100}})
	if err != nil || !plan.Eligible {
		t.Fatalf("Preflight: %v (%s)", err, plan.IneligibleReason)
	}
	if len(plan.Chunks) < 2 {
		t.Fatalf("expected the items to need several chunks, got %d", len(plan.Chunks))
	}
	if strings.Contains(plan.ShapeReason, "into one call") {
		t.Errorf("ShapeReason = %q, but %d chunks were planned", plan.ShapeReason, len(plan.Chunks))
	}
}

func TestCapabilitySnapshotReadsTheDeclaredContextWindow(t *testing.T) {
	llm.RegisterCapabilities(llm.ProviderCapabilities{Provider: "planwindow", Model: "plan-window-model", ContextWindow: 4321})

	known := capabilitySnapshotFor(context.Background(), types.OpOptions{Model: "plan-window-model"}, &scriptedCapabilityProvider{provider: "planwindow"})
	if known.ContextTokens != 4321 {
		t.Errorf("ContextTokens = %d, want the declared 4321", known.ContextTokens)
	}
	unknown := capabilitySnapshotFor(context.Background(), types.OpOptions{Model: "no-such-model-anywhere"}, &scriptedCapabilityProvider{provider: "planwindow"})
	if unknown.ContextTokens != DefaultContextTokens {
		t.Errorf("ContextTokens = %d for an undeclared model, want the default %d", unknown.ContextTokens, DefaultContextTokens)
	}
	undeclared := capabilitySnapshotFor(context.Background(), types.OpOptions{Model: "plan-window-model"}, &scriptedCapabilityProvider{provider: "some-other-provider"})
	if undeclared.ContextTokens != DefaultContextTokens {
		t.Errorf("ContextTokens = %d for another provider's route, want the default %d", undeclared.ContextTokens, DefaultContextTokens)
	}
}

// --- The planner takes a context.

// planClientProvider is a named provider that answers widgetOp's batch
// requests and counts them, so a test can tell which client a call went to.
type planClientProvider struct {
	name  string
	calls atomic.Int32
}

func (p *planClientProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls.Add(1)
	body, err := planManyBatchAnswer(req.UserPrompt)
	if err != nil {
		return llm.CompletionResponse{}, err
	}
	return llm.CompletionResponse{Content: body, Provider: p.name, Model: req.Model, FinishReason: "stop"}, nil
}
func (p *planClientProvider) Name() string                               { return p.name }
func (p *planClientProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *planClientProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }

// planWindows declares the context window of each route a planning test uses.
// A widget costs 36 tokens in a chunk (6 of its own, 6 of protocol, 24 of
// answer), and widgetOp's batch carries a system prompt of 9 tokens, so a window
// of 2000 holds 55 of them ((2000-9)/36) and one of 4000 holds 110. The window
// of 4000 held 111 under estimator 2, which counted the system prompt as no
// tokens.
func planWindows(t *testing.T, windows map[[2]string]int) {
	t.Helper()
	llm.ResetCapabilityRegistryForTest()
	t.Cleanup(llm.ResetCapabilityRegistryForTest)
	for route, window := range windows {
		llm.RegisterCapabilities(llm.ProviderCapabilities{Provider: route[0], Model: route[1], ContextWindow: window})
	}
}

func chunkSizes(plan types.Plan) []int {
	sizes := make([]int, len(plan.Chunks))
	for i, chunk := range plan.Chunks {
		sizes[i] = chunk.ItemCount
	}
	return sizes
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The planner plans for the client on the context. Two clients with their own
// providers, models and windows get chunks cut for their own window, and a
// context with no client gets the process default's, as it always did.
func TestPreflightForUsesTheContextsClient(t *testing.T) {
	withoutReinforcement(t)
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "process-model")
	planWindows(t, map[[2]string]int{
		{"planclient-small", "small-model"}:     2000,
		{"planclient-large", "large-model"}:     4000,
		{"planclient-process", "process-model"}: 100000,
	})
	swapNoProvider(t)
	SetDefaultProvider(&scriptedCapabilityProvider{provider: "planclient-process"})

	small := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: &scriptedCapabilityProvider{provider: "planclient-small"}, Model: "small-model"})
	large := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: &scriptedCapabilityProvider{provider: "planclient-large"}, Model: "large-model"})
	req := types.PlanRequest{Options: types.OpOptions{MaxOutputTokens: 100000}}
	items := widgets(200)

	cases := []struct {
		name       string
		ctx        context.Context
		wantChunks []int
		wantRoute  types.CapabilitySnapshot
	}{
		{"the small client", small, []int{52, 52, 52, 44},
			types.CapabilitySnapshot{Provider: "planclient-small", Model: "small-model", MaxOutputTokens: 100000, ContextTokens: 2000}},
		{"the large client", large, []int{105, 95},
			types.CapabilitySnapshot{Provider: "planclient-large", Model: "large-model", MaxOutputTokens: 100000, ContextTokens: 4000}},
		{"no client", context.Background(), []int{200},
			types.CapabilitySnapshot{Provider: "planclient-process", Model: "process-model", MaxOutputTokens: 100000, ContextTokens: 100000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PreflightFor(tc.ctx, widgetOp(), items, req)
			if err != nil || !plan.Eligible {
				t.Fatalf("PreflightFor: %v (%s)", err, plan.IneligibleReason)
			}
			if got := chunkSizes(plan); !sameInts(got, tc.wantChunks) {
				t.Errorf("chunks = %v, want %v", got, tc.wantChunks)
			}
			if plan.Capability != tc.wantRoute {
				t.Errorf("capability = %+v, want %+v", plan.Capability, tc.wantRoute)
			}
		})
	}

	// Two clients differ in capability, and in how many requests carry the same
	// items. Every request carries the batch's system prompt, so the client with
	// the smaller window, whose plan has more requests, has the larger estimate.
	// Under estimator 2 the two agreed, because the system prompt was no tokens.
	a, _ := PreflightFor(small, widgetOp(), items, req)
	b, _ := PreflightFor(large, widgetOp(), items, req)
	if a.CapabilityDigest == b.CapabilityDigest {
		t.Errorf("two clients should differ in capability: %q %q", a.CapabilityDigest, b.CapabilityDigest)
	}
	opt := req.Options
	if want := batchedPromptOf(t, widgetOp(), items, a, opt); a.EstimatedCost.EstimatedPromptTokens != want {
		t.Errorf("the small client's prompt estimate = %d, want %d", a.EstimatedCost.EstimatedPromptTokens, want)
	}
	if want := batchedPromptOf(t, widgetOp(), items, b, opt); b.EstimatedCost.EstimatedPromptTokens != want {
		t.Errorf("the large client's prompt estimate = %d, want %d", b.EstimatedCost.EstimatedPromptTokens, want)
	}
	if len(a.Chunks) <= len(b.Chunks) || a.EstimatedCost.EstimatedPromptTokens <= b.EstimatedCost.EstimatedPromptTokens {
		t.Errorf("%d requests estimated at %d prompt tokens, against %d requests at %d: more requests carry the system prompt more times",
			len(a.Chunks), a.EstimatedCost.EstimatedPromptTokens, len(b.Chunks), b.EstimatedCost.EstimatedPromptTokens)
	}
}

// The route a plan is made for is the one a call takes: the snapshot's
// provider, then the context's, then the process hook's and default, and the
// model is the call's, then the client's, then the tier's.
func TestPreflightForResolvesTheRouteAsACallDoes(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "process-model")
	planWindows(t, map[[2]string]int{
		{"route-snapshot", "snap-model"}:    1111,
		{"route-default", "dflt-model"}:     2222,
		{"route-context", "process-model"}:  3333,
		{"route-process", "process-model"}:  4444,
		{"route-snapshot", "call-model"}:    5555,
		{"route-process", "dflt-model"}:     6666,
		{"route-snapshot", "process-model"}: 7777,
	})
	hook := func(context.Context, string, string, types.OpOptions) (string, error) { return "", nil }
	failure := errors.New("the client cannot make a call")

	cases := []struct {
		name     string
		snapshot *ExecConfig // on the context
		dflt     *ExecConfig // the default snapshot
		ctxProv  llm.Provider
		hook     LLMCaller
		process  llm.Provider
		opts     types.OpOptions
		want     types.CapabilitySnapshot
	}{
		{name: "the context's snapshot", snapshot: &ExecConfig{Provider: &namedProvider{name: "route-snapshot"}, Model: "snap-model"},
			process: &namedProvider{name: "route-process"}, want: types.CapabilitySnapshot{Provider: "route-snapshot", Model: "snap-model", ContextTokens: 1111}},
		{name: "the default snapshot when the context carries none", dflt: &ExecConfig{Provider: &namedProvider{name: "route-default"}, Model: "dflt-model"},
			process: &namedProvider{name: "route-process"}, want: types.CapabilitySnapshot{Provider: "route-default", Model: "dflt-model", ContextTokens: 2222}},
		{name: "the context's snapshot hides the default one", snapshot: &ExecConfig{Provider: &namedProvider{name: "route-snapshot"}, Model: "snap-model"},
			dflt: &ExecConfig{Provider: &namedProvider{name: "route-default"}, Model: "dflt-model"}, want: types.CapabilitySnapshot{Provider: "route-snapshot", Model: "snap-model", ContextTokens: 1111}},
		{name: "a provider on the context", ctxProv: &namedProvider{name: "route-context"}, process: &namedProvider{name: "route-process"},
			want: types.CapabilitySnapshot{Provider: "route-context", Model: "process-model", ContextTokens: 3333}},
		{name: "a snapshot with no provider falls to the context's", snapshot: &ExecConfig{Model: "dflt-model"}, ctxProv: &namedProvider{name: "route-context"},
			want: types.CapabilitySnapshot{Provider: "route-context", Model: "dflt-model", ContextTokens: DefaultContextTokens}},
		{name: "the process default", process: &namedProvider{name: "route-process"},
			want: types.CapabilitySnapshot{Provider: "route-process", Model: "process-model", ContextTokens: 4444}},
		{name: "the hook beside the process default", hook: hook, process: &namedProvider{name: "route-process"},
			want: types.CapabilitySnapshot{Provider: "route-process", Model: "process-model", ContextTokens: 4444}},
		{name: "the hook alone names no provider", hook: hook,
			want: types.CapabilitySnapshot{Model: config.GetModel(types.OpOptions{}.Intelligence, ""), ContextTokens: DefaultContextTokens}},
		{name: "nothing names a provider", want: types.CapabilitySnapshot{Model: config.GetModel(types.OpOptions{}.Intelligence, ""), ContextTokens: DefaultContextTokens}},
		{name: "a snapshot whose client cannot call, with no provider", snapshot: &ExecConfig{Err: failure, Model: "snap-model"},
			want: types.CapabilitySnapshot{Model: "snap-model", ContextTokens: DefaultContextTokens}},
		{name: "the call's model over the client's", snapshot: &ExecConfig{Provider: &namedProvider{name: "route-snapshot"}, Model: "snap-model"},
			opts: types.OpOptions{Model: "call-model"}, want: types.CapabilitySnapshot{Provider: "route-snapshot", Model: "call-model", ContextTokens: 5555}},
		{name: "the tier's model when the client names none", snapshot: &ExecConfig{Provider: &namedProvider{name: "route-snapshot"}},
			want: types.CapabilitySnapshot{Provider: "route-snapshot", Model: "process-model", ContextTokens: 7777}},
		{name: "a model the route does not declare gets the default window", snapshot: &ExecConfig{Provider: &namedProvider{name: "route-default"}, Model: "undeclared"},
			want: types.CapabilitySnapshot{Provider: "route-default", Model: "undeclared", ContextTokens: DefaultContextTokens}},
		{name: "the output ceiling the call names", snapshot: &ExecConfig{Provider: &namedProvider{name: "route-snapshot"}, Model: "snap-model"},
			opts: types.OpOptions{MaxOutputTokens: 321}, want: types.CapabilitySnapshot{Provider: "route-snapshot", Model: "snap-model", MaxOutputTokens: 321, ContextTokens: 1111}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			swapNoProvider(t)
			withDefaultExec(t, tc.dflt)
			if tc.hook != nil {
				setLLMCaller(tc.hook)
			}
			if tc.process != nil {
				SetDefaultProvider(tc.process)
			}
			ctx := context.Background()
			if tc.snapshot != nil {
				ctx = WithSharedExecConfig(ctx, tc.snapshot)
			}
			if tc.ctxProv != nil {
				ctx = WithProvider(ctx, tc.ctxProv)
			}
			want := tc.want
			if want.MaxOutputTokens == 0 {
				want.MaxOutputTokens = config.GetMaxTokens(tc.opts.Intelligence)
			}

			plan, err := PreflightFor(ctx, widgetOp(), widgets(3), types.PlanRequest{Options: tc.opts})
			if err != nil {
				t.Fatalf("PreflightFor: %v", err)
			}
			if plan.Capability != want {
				t.Errorf("the plan is made for %+v, want %+v", plan.Capability, want)
			}
			if got := capabilitySnapshot(ctx, tc.opts); got != want {
				t.Errorf("capabilitySnapshot = %+v, want %+v", got, want)
			}
		})
	}
}

// With no client on the context the plan is the one Preflight makes for the
// process default.
func TestPreflightWithNoContextIsUnchanged(t *testing.T) {
	withoutReinforcement(t)
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "process-model")
	planWindows(t, map[[2]string]int{{"planclient-process", "process-model"}: 100000})
	swapNoProvider(t)
	SetDefaultProvider(&scriptedCapabilityProvider{provider: "planclient-process"})
	req := types.PlanRequest{Options: types.OpOptions{MaxOutputTokens: 100000}}

	plan, err := Preflight(widgetOp(), widgets(200), req)
	if err != nil || !plan.Eligible {
		t.Fatalf("Preflight: %v (%s)", err, plan.IneligibleReason)
	}
	if want := (types.CapabilitySnapshot{Provider: "planclient-process", Model: "process-model", MaxOutputTokens: 100000, ContextTokens: 100000}); plan.Capability != want {
		t.Errorf("capability = %+v, want %+v", plan.Capability, want)
	}
	if got := chunkSizes(plan); !sameInts(got, []int{200}) {
		t.Errorf("chunks = %v, want one of 200", got)
	}
	if plan.MaxCalls != 1 || plan.Shape != types.ShapeMDSP {
		t.Errorf("MaxCalls = %d, Shape = %v, want 1 call, MDSP", plan.MaxCalls, plan.Shape)
	}
	// One request holds the 200 widgets, and what it carries is what the batch
	// renders: the 36 byte system prompt (9 tokens), and the user prompt, the
	// 21 byte lead line and the tagged items as JSON, 21+2+200*52+199 = 10622 bytes
	// (2656 tokens; an item is 46 bytes with a sequential id of 8 characters and 52
	// with the random one of 14). Estimator 2 gave 2400 prompt tokens: each item's
	// JSON and a share of protocol, the system prompt not at all. The answer is 24
	// tokens each.
	if got := plan.EstimatedCost; got.EstimatedPromptTokens != 9+2656 || got.EstimatedCompletionTokens != 200*24 {
		t.Errorf("estimate = %+v, want 2665 prompt and 4800 completion tokens", got)
	}

	// The same plan, byte for byte, from the entry that takes a context.
	again, err := PreflightFor(context.Background(), widgetOp(), widgets(200), req)
	if err != nil {
		t.Fatalf("PreflightFor: %v", err)
	}
	first, _ := plan.Serialize()
	second, _ := again.Serialize()
	if string(first) != string(second) {
		t.Errorf("Preflight and PreflightFor(context.Background()) differ:\n%s\n%s", first, second)
	}
	// A context carrying only a deadline or a value is not a client.
	type unrelated struct{}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), unrelated{}, 1), time.Hour)
	defer cancel()
	third, _ := PreflightFor(ctx, widgetOp(), widgets(200), req)
	if serialized, _ := third.Serialize(); string(serialized) != string(first) {
		t.Errorf("a context with no client changed the plan:\n%s\n%s", first, serialized)
	}
}

// RunOpMany on a client's context runs the plan PreflightFor makes for that
// client: the plan it returns is that plan, and the calls it sends are that
// plan's chunks, to that client's provider.
func TestRunOpManyPlanEqualsPreflightFor(t *testing.T) {
	withoutReinforcement(t)
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "process-model")
	planWindows(t, map[[2]string]int{
		{"planclient-small", "small-model"}:     2000,
		{"planclient-process", "process-model"}: 100000,
	})
	swapNoProvider(t)
	process := &planClientProvider{name: "planclient-process"}
	SetDefaultProvider(process)
	client := &planClientProvider{name: "planclient-small"}
	ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: client, Model: "small-model"})
	req := types.PlanRequest{Options: types.OpOptions{MaxOutputTokens: 100000}}
	items := widgets(200)

	results, plan, err := RunOpMany(ctx, widgetOp(), items, req)
	if err != nil {
		t.Fatalf("RunOpMany: %v", err)
	}
	want, err := PreflightFor(ctx, widgetOp(), items, req)
	if err != nil {
		t.Fatalf("PreflightFor: %v", err)
	}
	got, _ := plan.Serialize()
	wantBytes, _ := want.Serialize()
	if string(got) != string(wantBytes) {
		t.Errorf("RunOpMany ran a plan other than PreflightFor's:\n%s\n%s", got, wantBytes)
	}
	if sizes := chunkSizes(plan); !sameInts(sizes, []int{52, 52, 52, 44}) {
		t.Errorf("chunks = %v, want the client's window to cut 52, 52, 52, 44", sizes)
	}
	if calls := int(client.calls.Load()); calls != plan.MaxCalls {
		t.Errorf("the client's provider got %d calls, the plan said %d", calls, plan.MaxCalls)
	}
	if calls := process.calls.Load(); calls != 0 {
		t.Errorf("the process default got %d calls though the context carried a client", calls)
	}
	if len(results) != len(items) {
		t.Fatalf("%d results for %d items", len(results), len(items))
	}
	for i, result := range results {
		if result.Value.Length != len(items[i].Name) {
			t.Fatalf("result %d = %+v, out of order", i, result.Value)
		}
	}

	// A context with no client is planned as it was: one chunk, for the default.
	_, bare, err := RunOpMany(context.Background(), widgetOp(), items, req)
	if err != nil {
		t.Fatalf("RunOpMany with no client: %v", err)
	}
	if sizes := chunkSizes(bare); !sameInts(sizes, []int{200}) || bare.Capability.Provider != "planclient-process" {
		t.Errorf("no client: chunks %v for %q, want one of 200 for the default", sizes, bare.Capability.Provider)
	}
	if calls := process.calls.Load(); calls != 1 {
		t.Errorf("the process default got %d calls for one chunk", calls)
	}
}

// A default snapshot is a client too: a plan made with nothing on the context
// but a default snapshot set is made for that default, and so are the calls.
func TestRunOpManyPlansForTheDefaultSnapshot(t *testing.T) {
	withoutReinforcement(t)
	planWindows(t, map[[2]string]int{{"planclient-dflt", "dflt-model"}: 4000})
	swapNoProvider(t)
	provider := &planClientProvider{name: "planclient-dflt"}
	withDefaultExec(t, &ExecConfig{Provider: provider, Model: "dflt-model"})

	_, plan, err := RunOpMany(context.Background(), widgetOp(), widgets(200), types.PlanRequest{Options: types.OpOptions{MaxOutputTokens: 100000}})
	if err != nil {
		t.Fatalf("RunOpMany: %v", err)
	}
	if sizes := chunkSizes(plan); !sameInts(sizes, []int{105, 95}) {
		t.Errorf("chunks = %v, want 105 and 95 for the default snapshot's window", sizes)
	}
	if got := provider.calls.Load(); got != 2 {
		t.Errorf("the default snapshot's provider got %d calls, want 2", got)
	}
}

// The client is read once for the whole plan: the provider and the model of one
// plan come from the same snapshot even when the default is replaced while the
// plan is made, which a default read twice would not guarantee.
func TestPreflightForReadsTheDefaultOnce(t *testing.T) {
	planWindows(t, map[[2]string]int{
		{"pin-first", "first-model"}:   1000,
		{"pin-second", "second-model"}: 9000,
	})
	swapNoProvider(t)
	first := &ExecConfig{Provider: &namedProvider{name: "pin-first"}, Model: "first-model"}
	second := &ExecConfig{Provider: &namedProvider{name: "pin-second"}, Model: "second-model"}
	withDefaultExec(t, first)

	ctx, pinned := PinExec(context.Background())
	if pinned != first {
		t.Fatalf("PinExec returned %p, want the default %p", pinned, first)
	}
	SetDefaultExec(second)
	plan, err := PreflightFor(ctx, widgetOp(), widgets(3), types.PlanRequest{})
	if err != nil {
		t.Fatalf("PreflightFor: %v", err)
	}
	if want := (types.CapabilitySnapshot{Provider: "pin-first", Model: "first-model", MaxOutputTokens: config.GetMaxTokens(types.OpOptions{}.Intelligence), ContextTokens: 1000}); plan.Capability != want {
		t.Errorf("a plan on a pinned client = %+v, want %+v", plan.Capability, want)
	}
	// Unpinned, it reads the default at the moment it is called, once.
	plan, err = PreflightFor(context.Background(), widgetOp(), widgets(3), types.PlanRequest{})
	if err != nil {
		t.Fatalf("PreflightFor: %v", err)
	}
	if plan.Capability.Provider != "pin-second" || plan.Capability.Model != "second-model" || plan.Capability.ContextTokens != 9000 {
		t.Errorf("an unpinned plan = %+v, want the new default's route and window", plan.Capability)
	}
}

// Planning is still free of provider calls under a client.
func TestPreflightForMakesZeroProviderCalls(t *testing.T) {
	planWindows(t, map[[2]string]int{{"planclient-free", "free-model"}: 2000})
	swapNoProvider(t)
	provider := &planClientProvider{name: "planclient-free"}
	ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider, Model: "free-model"})
	if _, err := PreflightFor(ctx, widgetOp(), widgets(300), types.PlanRequest{}); err != nil {
		t.Fatalf("PreflightFor: %v", err)
	}
	if got := provider.calls.Load(); got != 0 {
		t.Errorf("planning sent %d requests", got)
	}
}

// swapOnName replaces the default snapshot the first time it is asked its name,
// which is the one moment between a plan reading the provider and reading the
// model.
type swapOnName struct {
	scriptedCapabilityProvider
	once sync.Once
	next *ExecConfig
}

func (p *swapOnName) Name() string {
	p.once.Do(func() { SetDefaultExec(p.next) })
	return p.provider
}

// A default replaced between the plan's two reads of the client does not give
// the plan the provider of one client and the model of the other.
func TestPreflightForPinsTheDefaultAgainstASwapBetweenReads(t *testing.T) {
	planWindows(t, map[[2]string]int{
		{"swap-first", "first-model"}:   1000,
		{"swap-second", "second-model"}: 9000,
	})
	swapNoProvider(t)
	second := &ExecConfig{Provider: &namedProvider{name: "swap-second"}, Model: "second-model"}
	first := &ExecConfig{Provider: &swapOnName{scriptedCapabilityProvider: scriptedCapabilityProvider{provider: "swap-first"}, next: second}, Model: "first-model"}
	withDefaultExec(t, first)

	plan, err := PreflightFor(context.Background(), widgetOp(), widgets(3), types.PlanRequest{})
	if err != nil {
		t.Fatalf("PreflightFor: %v", err)
	}
	if plan.Capability.Provider != "swap-first" || plan.Capability.Model != "first-model" || plan.Capability.ContextTokens != 1000 {
		t.Errorf("a plan across a swap = %+v, want the first client's route whole", plan.Capability)
	}
}

// planBenchmarkSizes are the item counts the planner's cost is measured at. A
// planner whose cost per item stays flat from the first to the last is linear;
// one whose cost per item grows with the count is not.
var planBenchmarkSizes = []int{100, 1000, 10000}

// The planner's own cost per planned item, atomic: one rendering of each item's
// prompt and no more. allocs/item is the figure to compare across sizes.
func BenchmarkPreflightAtomicPerItem(b *testing.B) {
	for _, n := range planBenchmarkSizes {
		b.Run(fmt.Sprintf("items=%d", n), func(b *testing.B) {
			items := widgets(n)
			req := types.PlanRequest{Options: types.OpOptions{Model: "gpt-4"}, Force: types.ShapeAtomic}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if plan, err := Preflight(widgetOp(), items, req); err != nil || !plan.Eligible {
					b.Fatalf("Preflight: %v (%s)", err, plan.IneligibleReason)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(n), "ns/item")
		})
	}
}

// The same, batched: the items are measured once, packed, and each planned
// chunk rendered once.
func BenchmarkPreflightBatchedPerItem(b *testing.B) {
	for _, n := range planBenchmarkSizes {
		b.Run(fmt.Sprintf("items=%d", n), func(b *testing.B) {
			items := widgets(n)
			req := types.PlanRequest{Options: types.OpOptions{Model: "gpt-4"}, Force: types.ShapeMDSP}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if plan, err := Preflight(widgetOp(), items, req); err != nil || !plan.Eligible {
					b.Fatalf("Preflight: %v (%s)", err, plan.IneligibleReason)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(n), "ns/item")
		})
	}
}

// --- One estimator: a plan prices what the descriptor renders.

// withoutReinforcement turns the reinforcement block off for the test, so that a
// plan's prompt estimate is the arithmetic of what the descriptor renders and not
// that and the block the call path puts in front of every system prompt. The test
// that counts the block, and the steering, against the requests a run sends is
// TestPreflightEstimateIsWhatTheCallPathSends.
func withoutReinforcement(t *testing.T) {
	t.Helper()
	configtest.Setenv(t, config.PromptReinforcementVariable, "off")
}

// widgetOpWithSystem is widgetOp with system as the system prompt of both the
// single-item rendering and the batch's, so that a plan's prompt estimate has a
// system prompt of a known size to count once for every request.
func widgetOpWithSystem(system string) Op[widget, widgetSummary] {
	op := widgetOp()
	op.BuildPrompt = func(w widget, _ types.OpOptions) (string, string) { return system, w.Name }
	op.Batch = NewIDBatchAlgebra[widget, widgetSummary](types.BatchItemwise, types.AlgebraIndependent, system,
		BatchItemCodec[widget, widgetSummary]{
			DecodeItem: func(raw json.RawMessage) (widgetSummary, error) {
				var s widgetSummary
				return s, json.Unmarshal(raw, &s)
			},
		})
	return op
}

// An atomic plan's prompt estimate is what BuildPrompt renders for each item,
// system prompt included, counted the way a call counts it. It was the size of
// the item's JSON with the system prompt counted as nothing, so a descriptor
// with a long system prompt was under-estimated by that prompt once per request.
func TestPreflightPricesWhatEachItemRenders(t *testing.T) {
	withoutReinforcement(t)
	// A widget's user prompt is its name, "widget-000": ten bytes, three tokens.
	const userTokens = 3
	cases := []struct {
		name        string
		systemBytes int
		items       int
		wantPerItem int
	}{
		{"no system prompt, one item", 0, 1, userTokens},
		{"no system prompt, five items", 0, 5, userTokens},
		{"a one byte system prompt", 1, 5, 1 + userTokens},
		{"three bytes round up to a token", 3, 5, 1 + userTokens},
		{"four bytes are one token", 4, 5, 1 + userTokens},
		{"five bytes are two", 5, 5, 2 + userTokens},
		{"a four hundred byte system prompt", 400, 5, 100 + userTokens},
		{"a long system prompt over a hundred items", 4000, 100, 1000 + userTokens},
		{"a very long system prompt over three items", 40000, 3, 10000 + userTokens},
		{"a system prompt that is not a multiple of four", 4001, 7, 1001 + userTokens},
		{"no items", 4000, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := widgetOpWithSystem(strings.Repeat("s", tc.systemBytes))
			req := types.PlanRequest{Options: types.OpOptions{Model: "gpt-4"}, Force: types.ShapeAtomic}
			plan, err := Preflight(op, widgets(tc.items), req)
			if err != nil || !plan.Eligible {
				t.Fatalf("Preflight: %v (%s)", err, plan.IneligibleReason)
			}
			if got, want := plan.EstimatedCost.EstimatedPromptTokens, tc.items*tc.wantPerItem; got != want {
				t.Errorf("EstimatedPromptTokens = %d, want %d (%d per item)", got, want, tc.wantPerItem)
			}
			// The answer is still priced at the model's ceiling, once per item.
			if got, want := plan.EstimatedCost.EstimatedCompletionTokens, tc.items*plan.Capability.MaxOutputTokens; got != want {
				t.Errorf("EstimatedCompletionTokens = %d, want %d", got, want)
			}
			if plan.MaxCalls != tc.items {
				t.Errorf("MaxCalls = %d, want %d", plan.MaxCalls, tc.items)
			}
		})
	}
}

// batchedPromptOf is what the batch descriptor renders for the plan's chunks and
// for its oversized items sent alone, counted as a call counts them. It is worked
// out from the plan's own chunk boundaries, with the descriptor's own functions.
func batchedPromptOf(t *testing.T, op Op[widget, widgetSummary], items []widget, plan types.Plan, opt types.OpOptions) int {
	t.Helper()
	oversized := map[int]bool{}
	total := 0
	for _, o := range plan.OversizedItems {
		oversized[o.Index] = true
		system, user := op.BuildPrompt(items[o.Index], opt)
		total += estimateTokens(system) + estimateTokens(user)
	}
	var chunked []widget
	for i, item := range items {
		if !oversized[i] {
			chunked = append(chunked, item)
		}
	}
	offset := 0
	for _, chunk := range plan.Chunks {
		system, user, err := op.Batch.Encode(chunked[offset:offset+chunk.ItemCount], opt)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		total += estimateTokens(system) + estimateTokens(user)
		offset += chunk.ItemCount
	}
	if offset != len(chunked) {
		t.Fatalf("the plan's chunks hold %d of %d chunked items", offset, len(chunked))
	}
	return total
}

// A batched plan's prompt estimate is the batch's system prompt and the encoding
// of each planned chunk, once per chunk: what each request will carry.
func TestPreflightPricesTheRenderedChunksOfABatch(t *testing.T) {
	withoutReinforcement(t)
	cases := []struct {
		name        string
		systemBytes int
		items       int
		perCall     int
	}{
		{"no system prompt, a few items", 0, 7, 0},
		{"a short system prompt, a few items", 40, 7, 0},
		{"a long system prompt, one chunk", 4000, 40, 0},
		{"a long system prompt, chunks of three", 4000, 40, 3},
		{"a long system prompt, chunks of fifty over a thousand items", 4000, 1000, 50},
		{"a very long system prompt, chunks of ten", 40000, 100, 10},
		{"two items", 400, 2, 0},
		{"a thousand items in the default chunks", 400, 1000, 0},
		{"chunks of one item", 400, 5, 1},
		{"an item count that does not divide the chunk size", 400, 23, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := widgetOpWithSystem(strings.Repeat("s", tc.systemBytes))
			items := widgets(tc.items)
			opt := types.OpOptions{Model: "gpt-4"}
			plan, err := Preflight(op, items, types.PlanRequest{Options: opt, Force: types.ShapeMDSP, MaxItemsPerCall: tc.perCall})
			if err != nil || !plan.Eligible {
				t.Fatalf("Preflight: %v (%s)", err, plan.IneligibleReason)
			}
			if want := batchedPromptOf(t, op, items, plan, opt); plan.EstimatedCost.EstimatedPromptTokens != want {
				t.Errorf("EstimatedPromptTokens = %d over %d chunks, want %d (the system prompt and the encoding of each chunk)",
					plan.EstimatedCost.EstimatedPromptTokens, len(plan.Chunks), want)
			}
			// The answer side is the planning assumption it was.
			if want := tc.items * outputTokensPerItemDefault; plan.EstimatedCost.EstimatedCompletionTokens != want {
				t.Errorf("EstimatedCompletionTokens = %d, want %d", plan.EstimatedCost.EstimatedCompletionTokens, want)
			}
		})
	}
}

// An item that fits no chunk is sent alone, and is priced as it will be sent:
// its own prompt, with the single-item system prompt, and the model's ceiling.
func TestPreflightPricesAnOversizedItemByItsOwnPrompt(t *testing.T) {
	withoutReinforcement(t)
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "oversize-model")
	planWindows(t, map[[2]string]int{{"oversize-route", "oversize-model"}: 400})
	swapNoProvider(t)
	SetDefaultProvider(&scriptedCapabilityProvider{provider: "oversize-route"})

	op := widgetOpWithSystem(strings.Repeat("s", 80))
	items := widgets(6)
	// 450 tokens of its own in a window of 400: it fits no chunk, and nothing
	// else is as large.
	items[2] = widget{Name: strings.Repeat("big", 600)}
	opt := types.OpOptions{MaxOutputTokens: 1000}
	plan, err := Preflight(op, items, types.PlanRequest{Options: opt, Force: types.ShapeMDSP})
	if err != nil || !plan.Eligible {
		t.Fatalf("Preflight: %v (%s)", err, plan.IneligibleReason)
	}
	if len(plan.OversizedItems) != 1 || plan.OversizedItems[0].Index != 2 {
		t.Fatalf("the fixture was meant to have item 2 oversized, got %+v", plan.OversizedItems)
	}
	if want := batchedPromptOf(t, op, items, plan, opt); plan.EstimatedCost.EstimatedPromptTokens != want {
		t.Errorf("EstimatedPromptTokens = %d, want %d", plan.EstimatedCost.EstimatedPromptTokens, want)
	}
	if want := (len(items)-1)*outputTokensPerItemDefault + 1000; plan.EstimatedCost.EstimatedCompletionTokens != want {
		t.Errorf("EstimatedCompletionTokens = %d, want %d: the oversized item at the model's ceiling", plan.EstimatedCost.EstimatedCompletionTokens, want)
	}
}

// A request carries the system prompt as well as the items, so the room a
// chunk has in the model's window is what is left of it once the system prompt
// is in. The packer read the system prompt as zero tokens and cut chunks that
// did not fit.
func TestPreflightCutsChunksToLeaveRoomForTheSystemPrompt(t *testing.T) {
	withoutReinforcement(t)
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "room-model")
	planWindows(t, map[[2]string]int{{"room-route", "room-model"}: 2000})
	swapNoProvider(t)
	SetDefaultProvider(&scriptedCapabilityProvider{provider: "room-route"})
	req := types.PlanRequest{Options: types.OpOptions{MaxOutputTokens: 100000}}

	// A widget costs 38 tokens in a chunk (6 of its own, 8 of protocol, 24 of
	// answer; the protocol's id is 14 characters, two tokens a widget more than the
	// sequential ids it had). With no system prompt a window of 2000 holds 52 of
	// them; with one of 100 tokens the 1900 that are left hold 50, and with one of a
	// thousand the remaining thousand hold 26.
	cases := []struct {
		name        string
		systemBytes int
		want        []int
	}{
		{"no system prompt", 0, []int{52, 52, 52, 44}},
		{"a hundred tokens of system prompt", 400, []int{50, 50, 50, 50}},
		{"a thousand tokens of system prompt", 4000, []int{26, 26, 26, 26, 26, 26, 26, 18}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := Preflight(widgetOpWithSystem(strings.Repeat("s", tc.systemBytes)), widgets(200), req)
			if err != nil || !plan.Eligible {
				t.Fatalf("Preflight: %v (%s)", err, plan.IneligibleReason)
			}
			if got := chunkSizes(plan); !sameInts(got, tc.want) {
				t.Errorf("chunks = %v, want %v", got, tc.want)
			}
		})
	}
}

// BuildPrompt and Encode are the caller's functions, and a plan calls each as
// often as it needs and no more: BuildPrompt once for each item an atomic plan
// sends or an oversized one is sent alone, and Encode once for each chunk.
func TestPreflightCallsTheDescriptorsFunctionsOncePerItemAndPerChunk(t *testing.T) {
	type call struct {
		first string
		count int
	}
	instrument := func(op Op[widget, widgetSummary]) (Op[widget, widgetSummary], func() (map[string]int, []call)) {
		var mu sync.Mutex
		builds := map[string]int{}
		var encodes []call
		build, encode := op.BuildPrompt, op.Batch.Encode
		op.BuildPrompt = func(w widget, opt types.OpOptions) (string, string) {
			mu.Lock()
			builds[w.Name]++
			mu.Unlock()
			return build(w, opt)
		}
		op.Batch.Encode = func(items []widget, opt types.OpOptions) (string, string, error) {
			mu.Lock()
			encodes = append(encodes, call{first: items[0].Name, count: len(items)})
			mu.Unlock()
			return encode(items, opt)
		}
		return op, func() (map[string]int, []call) { return builds, encodes }
	}

	t.Run("atomic: BuildPrompt once for each item, Encode never", func(t *testing.T) {
		op, calls := instrument(widgetOpWithSystem("system"))
		plan, err := Preflight(op, widgets(50), types.PlanRequest{Force: types.ShapeAtomic})
		if err != nil || !plan.Eligible {
			t.Fatalf("Preflight: %v (%s)", err, plan.IneligibleReason)
		}
		builds, encodes := calls()
		if len(builds) != 50 || len(encodes) != 0 {
			t.Fatalf("%d items rendered and %d chunks encoded, want 50 and 0", len(builds), len(encodes))
		}
		for name, n := range builds {
			if n != 1 {
				t.Errorf("BuildPrompt was called %d times for %s, want once", n, name)
			}
		}
	})

	t.Run("batched: Encode once for each chunk, BuildPrompt never", func(t *testing.T) {
		op, calls := instrument(widgetOpWithSystem("system"))
		plan, err := Preflight(op, widgets(50), types.PlanRequest{Force: types.ShapeMDSP, MaxItemsPerCall: 8})
		if err != nil || !plan.Eligible {
			t.Fatalf("Preflight: %v (%s)", err, plan.IneligibleReason)
		}
		builds, encodes := calls()
		if len(plan.Chunks) != 7 || len(encodes) != len(plan.Chunks) || len(builds) != 0 {
			t.Fatalf("%d chunks planned, %d encoded, %d items rendered alone; want 7, 7 and 0", len(plan.Chunks), len(encodes), len(builds))
		}
	})

	t.Run("batched, with a system prompt that changes the chunks: no chunk of the plan is encoded twice", func(t *testing.T) {
		configtest.Setenv(t, "SCHEMAFLUX_MODEL", "once-model")
		planWindows(t, map[[2]string]int{{"once-route", "once-model"}: 2000})
		swapNoProvider(t)
		SetDefaultProvider(&scriptedCapabilityProvider{provider: "once-route"})
		op, calls := instrument(widgetOpWithSystem(strings.Repeat("s", 400)))
		plan, err := Preflight(op, widgets(200), types.PlanRequest{Options: types.OpOptions{MaxOutputTokens: 100000}})
		if err != nil || !plan.Eligible {
			t.Fatalf("Preflight: %v (%s)", err, plan.IneligibleReason)
		}
		_, encodes := calls()
		seen := map[call]bool{}
		for _, c := range encodes {
			if seen[c] {
				t.Errorf("a chunk of %d items starting at %s was encoded twice", c.count, c.first)
			}
			seen[c] = true
		}
		// One extra call at most: the first chunk, cut before the system prompt's
		// size was known, which is where that size is read from.
		if len(encodes) > len(plan.Chunks)+1 {
			t.Errorf("%d chunks planned and %d encoded", len(plan.Chunks), len(encodes))
		}
	})

	t.Run("an oversized item is rendered once, alone, and the chunks around it once each", func(t *testing.T) {
		configtest.Setenv(t, "SCHEMAFLUX_MODEL", "over-once-model")
		planWindows(t, map[[2]string]int{{"over-once-route", "over-once-model"}: 400})
		swapNoProvider(t)
		SetDefaultProvider(&scriptedCapabilityProvider{provider: "over-once-route"})
		op, calls := instrument(widgetOpWithSystem(strings.Repeat("s", 80)))
		items := widgets(6)
		items[2] = widget{Name: strings.Repeat("big", 600)}
		plan, err := Preflight(op, items, types.PlanRequest{Options: types.OpOptions{MaxOutputTokens: 1000}, Force: types.ShapeMDSP})
		if err != nil || !plan.Eligible {
			t.Fatalf("Preflight: %v (%s)", err, plan.IneligibleReason)
		}
		builds, encodes := calls()
		if len(plan.OversizedItems) != 1 || len(builds) != 1 || builds[items[2].Name] != 1 {
			t.Errorf("oversized %+v, rendered alone %d items", plan.OversizedItems, len(builds))
		}
		if len(encodes) < len(plan.Chunks) || len(encodes) > len(plan.Chunks)+1 {
			t.Errorf("%d chunks planned and %d encoded", len(plan.Chunks), len(encodes))
		}
	})
}

// --- Why a plan is refused.

// planOpWhoseBuildPromptPanics renders every widget but widget-002, which it
// panics on with marker.
func planOpWhoseBuildPromptPanics(marker string) Op[widget, widgetSummary] {
	op := widgetOp()
	op.BuildPrompt = func(w widget, _ types.OpOptions) (string, string) {
		if w.Name == "widget-002" {
			panic(marker)
		}
		return "summarize", w.Name
	}
	return op
}

// planOpWhoseEncodeFails is widgetOp with an Encode that does what failure does.
func planOpWhoseEncodeFails(failure func() (string, string, error)) Op[widget, widgetSummary] {
	op := widgetOp()
	op.Batch.Encode = func([]widget, types.OpOptions) (string, string, error) { return failure() }
	return op
}

// opaque has a field no JSON encoder can write, so an op over it cannot be sent
// in a batch.
type opaque struct {
	Callback func()
}

func opaqueOp() Op[opaque, string] {
	return Op[opaque, string]{
		ID:        types.OperationID{Name: "opaque", Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityExperimental},
		Contract:  OutputContract[string]{Decode: func(body string) (string, error) { return body, nil }},
		Batch: NewIDBatchAlgebra[opaque, string](types.BatchItemwise, types.AlgebraIndependent, "system",
			BatchItemCodec[opaque, string]{DecodeItem: func(raw json.RawMessage) (string, error) { return string(raw), nil }}),
		BuildPrompt: func(opaque, types.OpOptions) (string, string) { return "system", "user" },
	}
}

// Every way the planner refuses a plan says what kind of refusal it is, beside
// the reason in words: a plan over its budget is a budget, an item that cannot be
// encoded is an invalid request, and a descriptor that cannot render a prompt
// is neither a request for a different one nor a budget.
func TestEveryRefusalOfAPlanNamesItsKind(t *testing.T) {
	const marker = "SCHEMAFLUX-MARKER-7d41"
	noBuild := widgetOp()
	noBuild.BuildPrompt = nil
	noDecode := widgetOp()
	noDecode.Contract.Decode = nil

	cases := []struct {
		name       string
		run        func() (types.Plan, error)
		wantKind   types.ErrorKind
		wantReason string
	}{
		{"no BuildPrompt", func() (types.Plan, error) { return Preflight(noBuild, widgets(3), types.PlanRequest{}) },
			types.KindConfiguration, "no BuildPrompt"},
		{"no Decode", func() (types.Plan, error) { return Preflight(noDecode, widgets(3), types.PlanRequest{}) },
			types.KindConfiguration, "no Decode"},
		{"a forced shape the operation cannot run", func() (types.Plan, error) {
			return Preflight(atomicOnlyOp(), []string{"a", "b"}, types.PlanRequest{Force: types.ShapeMDSP})
		}, types.KindConfiguration, "not legal"},
		{"a forced global shape on an itemwise operation", func() (types.Plan, error) {
			return Preflight(widgetOp(), widgets(3), types.PlanRequest{Force: types.ShapeGlobal})
		}, types.KindConfiguration, "not legal"},
		{"a forced staged shape on one operation", func() (types.Plan, error) {
			return Preflight(widgetOp(), widgets(3), types.PlanRequest{Force: types.ShapeSDMP})
		}, types.KindConfiguration, "staged plan"},
		{"an item that cannot be encoded for a batch", func() (types.Plan, error) {
			return Preflight(opaqueOp(), []opaque{{}, {}}, types.PlanRequest{Force: types.ShapeMDSP})
		}, types.KindInvalidRequest, "item 0 cannot be encoded"},
		{"over the budget, atomic", func() (types.Plan, error) {
			return Preflight(widgetOp(), widgets(50), types.PlanRequest{Options: types.OpOptions{Model: "gpt-4"}, Force: types.ShapeAtomic, Budget: 1e-9})
		}, types.KindBudgetExceeded, "over the budget"},
		{"over the budget, batched", func() (types.Plan, error) {
			return Preflight(widgetOp(), widgets(50), types.PlanRequest{Options: types.OpOptions{Model: "gpt-4"}, Force: types.ShapeMDSP, Budget: 1e-9})
		}, types.KindBudgetExceeded, "over the budget"},
		{"over the budget, unforced", func() (types.Plan, error) {
			return Preflight(widgetOp(), widgets(50), types.PlanRequest{Options: types.OpOptions{Model: "gpt-4"}, Budget: 1e-9})
		}, types.KindBudgetExceeded, "over the budget"},
		{"a budget on a model with no price", func() (types.Plan, error) {
			return Preflight(widgetOp(), widgets(50), types.PlanRequest{Options: types.OpOptions{Model: "definitely-not-a-real-model-xyz"}, Force: types.ShapeAtomic, Budget: 5})
		}, types.KindBudgetExceeded, "no price"},
		{"a BuildPrompt that panics", func() (types.Plan, error) {
			return Preflight(planOpWhoseBuildPromptPanics(marker), widgets(5), types.PlanRequest{Force: types.ShapeAtomic})
		}, types.KindInvariantViolation, "BuildPrompt"},
		{"an Encode that fails with no kind", func() (types.Plan, error) {
			return Preflight(planOpWhoseEncodeFails(func() (string, string, error) { return "", "", errors.New(marker) }), widgets(5), types.PlanRequest{Force: types.ShapeMDSP})
		}, types.KindConfiguration, "Encode"},
		{"an Encode that fails with a kind of its own", func() (types.Plan, error) {
			return Preflight(planOpWhoseEncodeFails(func() (string, string, error) {
				return "", "", types.Newf(types.KindInvalidRequest, "encode", "%s", marker)
			}), widgets(5), types.PlanRequest{Force: types.ShapeMDSP})
		}, types.KindInvalidRequest, "Encode"},
		{"an Encode that panics", func() (types.Plan, error) {
			return Preflight(planOpWhoseEncodeFails(func() (string, string, error) { panic(marker) }), widgets(5), types.PlanRequest{Force: types.ShapeMDSP})
		}, types.KindInvariantViolation, "Encode"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := tc.run()
			if err != nil {
				t.Fatalf("Preflight returned an error beside the plan: %v", err)
			}
			if plan.Eligible {
				t.Fatalf("the plan is eligible, want a refusal of kind %s", tc.wantKind)
			}
			if plan.IneligibleKind != tc.wantKind {
				t.Errorf("IneligibleKind = %s, want %s (reason %q)", plan.IneligibleKind, tc.wantKind, plan.IneligibleReason)
			}
			if !strings.Contains(plan.IneligibleReason, tc.wantReason) {
				t.Errorf("IneligibleReason = %q, want it to contain %q", plan.IneligibleReason, tc.wantReason)
			}
			if strings.Contains(plan.IneligibleReason, marker) {
				t.Errorf("the reason carries what the caller's function produced: %q", plan.IneligibleReason)
			}
		})
	}

	t.Run("an eligible plan has no refusal kind", func(t *testing.T) {
		for _, force := range []types.ExecutionShape{types.ShapeUnspecified, types.ShapeAtomic, types.ShapeMDSP} {
			plan, err := Preflight(widgetOp(), widgets(5), types.PlanRequest{Force: force})
			if err != nil || !plan.Eligible || plan.IneligibleKind != types.KindUnknown {
				t.Errorf("force %s: eligible %v, kind %s, err %v", force, plan.Eligible, plan.IneligibleKind, err)
			}
		}
	})
}

// RunOpMany refuses the plan with the plan's own kind, makes no provider call to
// do it, and returns the plan beside the error so the caller can read why.
func TestRunOpManyReturnsThePlansRefusalKind(t *testing.T) {
	var calls atomic.Int32
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls.Add(1)
		return `{}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	cases := []struct {
		name string
		run  func() ([]types.Result[widgetSummary], types.Plan, error)
		want error
	}{
		{"over the budget", func() ([]types.Result[widgetSummary], types.Plan, error) {
			return RunOpMany(context.Background(), widgetOp(), widgets(40), types.PlanRequest{Options: types.OpOptions{Model: "gpt-4"}, Budget: 1e-9})
		}, types.ErrBudgetExceeded},
		{"a budget on a model with no price", func() ([]types.Result[widgetSummary], types.Plan, error) {
			return RunOpMany(context.Background(), widgetOp(), widgets(40), types.PlanRequest{Options: types.OpOptions{Model: "definitely-not-a-real-model-xyz"}, Budget: 1})
		}, types.ErrBudgetExceeded},
		{"a forced shape the operation cannot run", func() ([]types.Result[widgetSummary], types.Plan, error) {
			return RunOpMany(context.Background(), widgetOp(), widgets(40), types.PlanRequest{Force: types.ShapeGlobal})
		}, types.ErrConfiguration},
		{"a BuildPrompt that panics", func() ([]types.Result[widgetSummary], types.Plan, error) {
			return RunOpMany(context.Background(), planOpWhoseBuildPromptPanics("x"), widgets(5), types.PlanRequest{Force: types.ShapeAtomic})
		}, types.ErrInvariantViolation},
		{"an Encode that fails", func() ([]types.Result[widgetSummary], types.Plan, error) {
			return RunOpMany(context.Background(), planOpWhoseEncodeFails(func() (string, string, error) { return "", "", errors.New("x") }), widgets(5), types.PlanRequest{Force: types.ShapeMDSP})
		}, types.ErrConfiguration},
		{"an item that cannot be encoded", func() ([]types.Result[widgetSummary], types.Plan, error) {
			_, plan, err := RunOpMany(context.Background(), opaqueOp(), []opaque{{}, {}}, types.PlanRequest{Force: types.ShapeMDSP})
			return nil, plan, err
		}, types.ErrInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results, plan, err := tc.run()
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v (kind %s), want kind %v", err, types.KindOf(err), tc.want)
			}
			if types.KindOf(err) != plan.IneligibleKind {
				t.Errorf("the error is of kind %s and the plan's refusal of kind %s", types.KindOf(err), plan.IneligibleKind)
			}
			if results != nil {
				t.Errorf("results %+v beside a refused plan", results)
			}
			if plan.Eligible || plan.IneligibleReason == "" {
				t.Errorf("the plan returned beside the error is eligible %v, reason %q", plan.Eligible, plan.IneligibleReason)
			}
		})
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("refusing a plan made %d provider calls", got)
	}
}

// A BuildPrompt or an Encode that panics, or an Encode that fails, is that
// plan's refusal: not the process's end, and not a plan priced without it,
// whatever the caller's function put in its panic or its error.
func TestPreflightRefusesWhenTheDescriptorCannotRenderAndNamesNoData(t *testing.T) {
	const marker = "SCHEMAFLUX-MARKER-9be2"
	cases := []struct {
		name  string
		op    Op[widget, widgetSummary]
		items []widget
		req   types.PlanRequest
	}{
		{"BuildPrompt panics with a string", planOpWhoseBuildPromptPanics(marker), widgets(5), types.PlanRequest{Force: types.ShapeAtomic}},
		{"BuildPrompt panics for the only item", planOpWhoseBuildPromptPanics(marker), []widget{{Name: "widget-002"}}, types.PlanRequest{}},
		{"Encode panics with an error value", planOpWhoseEncodeFails(func() (string, string, error) { panic(errors.New(marker)) }), widgets(5), types.PlanRequest{Force: types.ShapeMDSP}},
		{"Encode panics with a struct", planOpWhoseEncodeFails(func() (string, string, error) { panic(widget{Name: marker}) }), widgets(5), types.PlanRequest{Force: types.ShapeMDSP}},
		{"Encode panics with nil", planOpWhoseEncodeFails(func() (string, string, error) { panic(nil) }), widgets(5), types.PlanRequest{Force: types.ShapeMDSP}},
		{"Encode fails with a message holding data", planOpWhoseEncodeFails(func() (string, string, error) { return "", "", fmt.Errorf("cannot encode %s", marker) }), widgets(5), types.PlanRequest{Force: types.ShapeMDSP}},
		{"Encode fails with a classified error holding data", planOpWhoseEncodeFails(func() (string, string, error) {
			return "", "", types.Newf(types.KindSchemaViolation, "encode", "%s", marker)
		}), widgets(5), types.PlanRequest{Force: types.ShapeMDSP}},
		{"Encode fails under an unforced plan of many items", planOpWhoseEncodeFails(func() (string, string, error) { return "", "", errors.New(marker) }), widgets(20), types.PlanRequest{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := Preflight(tc.op, tc.items, tc.req)
			if err != nil {
				t.Fatalf("Preflight: %v", err)
			}
			if plan.Eligible || plan.IneligibleReason == "" || plan.IneligibleKind == types.KindUnknown {
				t.Fatalf("eligible %v, reason %q, kind %s: want a classified refusal", plan.Eligible, plan.IneligibleReason, plan.IneligibleKind)
			}
			if strings.Contains(plan.IneligibleReason, marker) {
				t.Errorf("the reason carries the caller's text: %q", plan.IneligibleReason)
			}
			if plan.EstimatedCost != (types.CostEstimate{}) || plan.MaxCalls != 0 {
				t.Errorf("a refused plan carries an estimate or a call ceiling: %+v, %d", plan.EstimatedCost, plan.MaxCalls)
			}
			if encoded, _ := plan.Serialize(); strings.Contains(string(encoded), marker) {
				t.Errorf("the serialized plan carries the caller's text: %s", encoded)
			}
		})
	}
}

// A batched plan sends an oversized item alone, so it renders it with
// BuildPrompt, and a BuildPrompt that panics on it refuses the plan as it
// refuses an atomic one.
func TestPreflightRefusesAnOversizedItemWhoseBuildPromptPanics(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "over-panic-model")
	planWindows(t, map[[2]string]int{{"over-panic-route", "over-panic-model"}: 400})
	swapNoProvider(t)
	SetDefaultProvider(&scriptedCapabilityProvider{provider: "over-panic-route"})

	op := widgetOp()
	op.BuildPrompt = func(w widget, _ types.OpOptions) (string, string) {
		if len(w.Name) > 100 {
			panic("marker")
		}
		return "summarize", w.Name
	}
	items := widgets(5)
	items[2] = widget{Name: strings.Repeat("big", 600)}
	plan, err := Preflight(op, items, types.PlanRequest{Options: types.OpOptions{MaxOutputTokens: 1000}, Force: types.ShapeMDSP})
	if err != nil || plan.Eligible || plan.IneligibleKind != types.KindInvariantViolation {
		t.Fatalf("eligible %v, kind %s, err %v (%s)", plan.Eligible, plan.IneligibleKind, err, plan.IneligibleReason)
	}
}

// The planner's own cost is linear in the items: what it allocates for each item
// does not grow with how many there are, and is small. A plan that rendered each
// chunk against all the items before it, or marshalled every item again for each
// chunk, would show here as a figure that grows with the count.
func TestPreflightAllocatesLessThanOnceForEachItemAndDoesNotGrowWithTheirNumber(t *testing.T) {
	perItem := func(force types.ExecutionShape, n int) float64 {
		items := widgets(n)
		op := widgetOp()
		req := types.PlanRequest{Options: types.OpOptions{Model: "gpt-4"}, Force: force}
		return testing.AllocsPerRun(3, func() {
			if plan, err := Preflight(op, items, req); err != nil || !plan.Eligible {
				t.Fatalf("Preflight: %v (%s)", err, plan.IneligibleReason)
			}
		}) / float64(n)
	}
	for _, shape := range []types.ExecutionShape{types.ShapeAtomic, types.ShapeMDSP} {
		t.Run(shape.String(), func(t *testing.T) {
			small, large := perItem(shape, 1000), perItem(shape, 10000)
			if large > 1.5 {
				t.Errorf("%.2f allocations per item at 10000 items", large)
			}
			if large > small*1.1+0.01 {
				t.Errorf("allocations per item grew from %.3f at 1000 items to %.3f at 10000", small, large)
			}
		})
	}
}

// sentRequestsProvider answers as the provider it wraps does and keeps every request
// the call path sent it, as the call path sent it.
type sentRequestsProvider struct {
	llm.Provider
	mu       sync.Mutex
	requests []llm.CompletionRequest
}

func (p *sentRequestsProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	return p.Provider.Complete(ctx, req)
}

// A plan counts what is sent. The call path puts a reinforcement block in front of
// the system prompt and the caller's steering, delimited, in front of the user
// prompt, and the estimate counts both through the functions the call path uses, so
// that it is the prompt tokens of the requests the run makes, as the scheduler admits
// them, and not the descriptor's own prompts alone. The run here is the oracle:
// the requests it sends are counted as they went.
func TestPreflightEstimateIsWhatTheCallPathSends(t *testing.T) {
	cases := []struct {
		name          string
		opt           types.OpOptions
		shape         types.ExecutionShape
		reinforcement bool
	}{
		{"atomic, the block on", types.OpOptions{}, types.ShapeAtomic, true},
		{"atomic, the block off", types.OpOptions{}, types.ShapeAtomic, false},
		{"atomic, steered", types.OpOptions{Steering: "answer briefly and cite nothing"}, types.ShapeAtomic, true},
		{"atomic, steered, the block off", types.OpOptions{Steering: "answer briefly and cite nothing"}, types.ShapeAtomic, false},
		{"atomic, in JSON", types.OpOptions{ResponseFormat: "json"}, types.ShapeAtomic, true},
		{"batched, the block on", types.OpOptions{}, types.ShapeMDSP, true},
		{"batched, the block off", types.OpOptions{}, types.ShapeMDSP, false},
		{"batched, steered", types.OpOptions{Steering: "answer briefly and cite nothing"}, types.ShapeMDSP, true},
		{"batched, in JSON, steered", types.OpOptions{ResponseFormat: "json", Steering: "be exact"}, types.ShapeMDSP, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.reinforcement {
				withoutReinforcement(t)
			}
			provider := &sentRequestsProvider{Provider: newRecoverFakeProvider("fake-model")}
			ctx := recoverCtx(t, provider)
			_, plan, err := RunOpMany(ctx, recoverWidgetOp(), recoverWidgets(11), types.PlanRequest{Options: tc.opt, Force: tc.shape, MaxItemsPerCall: 4})
			if err != nil || !plan.Eligible {
				t.Fatalf("RunOpMany: %v (%s)", err, plan.IneligibleReason)
			}
			sent := 0
			for _, request := range provider.requests {
				sent += requestTokens(request.SystemPrompt, request.UserPrompt)
			}
			if len(provider.requests) != plan.MaxCalls {
				t.Fatalf("%d requests were sent, the plan said %d", len(provider.requests), plan.MaxCalls)
			}
			if got := plan.EstimatedCost.EstimatedPromptTokens; got != sent {
				t.Errorf("the plan estimated %d prompt tokens, the %d requests carried %d", got, len(provider.requests), sent)
			}
		})
	}

	// The block is real: with it on, the same plan is larger.
	on, _ := Preflight(widgetOp(), widgets(5), types.PlanRequest{Force: types.ShapeAtomic})
	withoutReinforcement(t)
	off, _ := Preflight(widgetOp(), widgets(5), types.PlanRequest{Force: types.ShapeAtomic})
	if on.EstimatedCost.EstimatedPromptTokens <= off.EstimatedCost.EstimatedPromptTokens {
		t.Errorf("a plan with the reinforcement block counts %d prompt tokens, one without it %d", on.EstimatedCost.EstimatedPromptTokens, off.EstimatedCost.EstimatedPromptTokens)
	}
}

// The batch protocol's ids are random and 14 characters long, and the planner prices
// the scaffolding they add to every item from that length.
func TestProtocolOverheadIsPricedFromTheLengthOfAnID(t *testing.T) {
	_, user, err := widgetOp().Batch.Encode(widgets(1), types.OpOptions{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := len(`{"id":"`) + batchIDLength + len(`","item":},`)
	if protocolOverheadBytes != want {
		t.Errorf("protocolOverheadBytes = %d, want %d", protocolOverheadBytes, want)
	}
	// One item is the lead line, the list's brackets, the scaffolding and the item.
	item, _ := json.Marshal(widgets(1)[0])
	if got, want := len(user), len(batchPromptPrefix)+2+protocolOverheadBytes-1+len(item); got != want {
		t.Errorf("a one-item request is %d bytes, want %d from the overhead and the item", got, want)
	}
}
