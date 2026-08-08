package ops

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// PL-011: PermutationValidate and PartitionValidate reuse invariants.go's
// SameMultiset and CoversExactlyOnce rather than a third and fourth
// implementation of the same two checks -- these tests prove they behave
// exactly like those functions, through the GlobalValidate signature a
// BatchAlgebra actually wires in. RunOpGlobal's tests are the other half:
// proving the single-chunk happy path runs a real whole-set check and the
// multi-chunk case is refused before a single provider call.

// --- PermutationValidate: Sort's contract.

func TestPermutationValidateAcceptsATruePermutation(t *testing.T) {
	validate := PermutationValidate[int]()
	if err := validate([]int{1, 2, 3, 4}, []int{4, 2, 1, 3}); err != nil {
		t.Errorf("a true permutation was rejected: %v", err)
	}
}

func TestPermutationValidateRejectsAMissingItem(t *testing.T) {
	validate := PermutationValidate[int]()
	if err := validate([]int{1, 2, 3}, []int{1, 2}); err == nil {
		t.Error("a short output was accepted as a permutation")
	}
}

func TestPermutationValidateRejectsADuplicatedItem(t *testing.T) {
	validate := PermutationValidate[int]()
	if err := validate([]int{1, 2, 3}, []int{1, 1, 2}); err == nil {
		t.Error("an output with a duplicated value and a missing one was accepted as a permutation")
	}
}

func TestPermutationValidateAcceptsEmptyInput(t *testing.T) {
	validate := PermutationValidate[int]()
	if err := validate(nil, nil); err != nil {
		t.Errorf("empty input/output should be a trivial (empty) permutation: %v", err)
	}
}

// --- PartitionValidate: Cluster's contract.

// clusterGroup is the fixture Out type: one cluster, naming the input
// indices it claims -- the shape CoversExactlyOnce's groups parameter
// expects directly, which is the point: PartitionValidate reads it, never
// invents it.
type clusterGroup struct {
	Indices []int
}

func TestPartitionValidateAcceptsAValidPartition(t *testing.T) {
	validate := PartitionValidate[string, clusterGroup](func(g clusterGroup) []int { return g.Indices })
	groups := []clusterGroup{{Indices: []int{0, 2}}, {Indices: []int{1, 3}}}
	if err := validate([]string{"a", "b", "c", "d"}, groups); err != nil {
		t.Errorf("a valid partition (every index covered exactly once) was rejected: %v", err)
	}
}

func TestPartitionValidateRejectsOverlappingGroups(t *testing.T) {
	validate := PartitionValidate[string, clusterGroup](func(g clusterGroup) []int { return g.Indices })
	groups := []clusterGroup{{Indices: []int{0, 1}}, {Indices: []int{1, 2}}}
	if err := validate([]string{"a", "b", "c"}, groups); err == nil {
		t.Error("index 1 appears in two groups; that is not a partition and should have been rejected")
	}
}

func TestPartitionValidateRejectsAnUncoveredItem(t *testing.T) {
	validate := PartitionValidate[string, clusterGroup](func(g clusterGroup) []int { return g.Indices })
	groups := []clusterGroup{{Indices: []int{0}}}
	if err := validate([]string{"a", "b"}, groups); err == nil {
		t.Error("index 1 was in no group; that is not a partition and should have been rejected")
	}
}

func TestPartitionValidateRejectsAnOutOfRangeIndex(t *testing.T) {
	validate := PartitionValidate[string, clusterGroup](func(g clusterGroup) []int { return g.Indices })
	groups := []clusterGroup{{Indices: []int{0, 5}}}
	if err := validate([]string{"a", "b"}, groups); err == nil {
		t.Error("index 5 is out of range for 2 items and should have been rejected")
	}
}

func TestPartitionValidateAcceptsEmptyInput(t *testing.T) {
	validate := PartitionValidate[string, clusterGroup](func(g clusterGroup) []int { return g.Indices })
	if err := validate(nil, nil); err != nil {
		t.Errorf("empty input with no groups should be a trivial (empty) partition: %v", err)
	}
}

// --- RunOpGlobal: the execution path.

// reverseSortOp is a hand-wired BatchAggregate/AlgebraPermutation op whose
// Merge actually reorders (reverses) its input -- deliberately not built
// through NewIDBatchAlgebra, whose id protocol fixes each id to its input
// position and so cannot represent an output whose POSITION differs from
// its input, which is exactly what a real Sort answer needs to be able to
// do. Encode/Merge here read and write the whole slice directly, the way a
// real Sort operation's algebra would.
func reverseSortOp() Op[int, int] {
	return Op[int, int]{
		ID: types.OperationID{Name: "algebraSortReverse", Version: "v1"},
		Semantics: types.Semantics{
			Category:  types.CategoryTransformation,
			Stability: types.StabilityExperimental,
		},
		Contract: OutputContract[int]{
			Decode: func(string) (int, error) { return 0, nil },
		},
		Batch: BatchAlgebra[int, int]{
			Class: types.BatchAggregate,
			Kind:  types.AlgebraPermutation,
			Encode: func(items []int, _ types.OpOptions) (string, string, error) {
				return "sort these ascending", fmt.Sprintf("%v", items), nil
			},
			Merge: func(_ string, items []int) ([]int, error) {
				out := make([]int, len(items))
				for i, v := range items {
					out[len(items)-1-i] = v
				}
				return out, nil
			},
			GlobalValidate: PermutationValidate[int](),
		},
		BuildPrompt: func(v int, _ types.OpOptions) (string, string) {
			return "sort one", fmt.Sprintf("%d", v)
		},
	}
}

// algebraFakeProvider answers every call with a fixed body -- reverseSortOp's
// Merge ignores the body entirely (it derives its answer from the request
// items), so the content only has to exist, not mean anything.
type algebraFakeProvider struct{ calls int }

func (p *algebraFakeProvider) Complete(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls++
	return llm.CompletionResponse{Content: "{}", Provider: "algebra-fake", Model: "fake-model", FinishReason: "stop"}, nil
}
func (p *algebraFakeProvider) Name() string                               { return "algebra-fake" }
func (p *algebraFakeProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *algebraFakeProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }

func algebraCtx(t *testing.T, provider llm.Provider) context.Context {
	t.Helper()
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	return WithProvider(context.Background(), provider)
}

func TestRunOpGlobalRejectsANonAggregateOp(t *testing.T) {
	op := widgetOp() // BatchItemwise, not BatchAggregate
	items := widgets(3)

	_, plan, err := RunOpGlobal(context.Background(), op, items, types.PlanRequest{})
	if err == nil {
		t.Fatal("a BatchItemwise op should be refused by RunOpGlobal, which is for aggregate algebras only")
	}
	if plan.Eligible {
		t.Error("no plan should have been built for a refusal that happens before Preflight runs")
	}
}

func TestRunOpGlobalSingleChunkReturnsAValidatedPermutation(t *testing.T) {
	op := reverseSortOp()
	items := []int{1, 2, 3, 4, 5}
	provider := &algebraFakeProvider{}
	ctx := algebraCtx(t, provider)

	result, plan, err := RunOpGlobal(ctx, op, items, types.PlanRequest{})
	if err != nil {
		t.Fatalf("single-chunk global run failed: %v", err)
	}
	if len(plan.Chunks) != 1 {
		t.Fatalf("expected a single chunk for 5 small items, got %d", len(plan.Chunks))
	}
	if err := SameMultiset(items, result.Value); err != nil {
		t.Errorf("result is not a permutation of the input: %v", err)
	}
	want := []int{5, 4, 3, 2, 1}
	for i := range want {
		if result.Value[i] != want[i] {
			t.Errorf("Value[%d] = %d, want %d (reverseSortOp reverses)", i, result.Value[i], want[i])
		}
	}
	if provider.calls != 1 {
		t.Errorf("expected exactly one provider call for a single-chunk plan, got %d", provider.calls)
	}
}

func TestRunOpGlobalRefusesAMultiChunkPlanRatherThanConcatenating(t *testing.T) {
	op := reverseSortOp()
	items := make([]int, 12)
	for i := range items {
		items[i] = i
	}
	provider := &algebraFakeProvider{}
	ctx := algebraCtx(t, provider)

	req := types.PlanRequest{MaxItemsPerCall: 5} // forces 3 chunks over 12 items

	result, plan, err := RunOpGlobal(ctx, op, items, req)
	if err == nil {
		t.Fatal("a plan needing more than one chunk should be refused: PL-011 has no cross-chunk merge in this delivery")
	}
	if len(result.Value) != 0 {
		t.Errorf("a refused run must return no partial result, got %d values", len(result.Value))
	}
	if len(plan.Chunks) <= 1 {
		t.Fatalf("expected the plan to need more than one chunk, got %d", len(plan.Chunks))
	}
	if provider.calls != 0 {
		t.Errorf("a refusal must happen before any provider call; got %d calls", provider.calls)
	}
}

func TestRunOpGlobal500ItemsFitsOneChunkAndReturnsAPermutation(t *testing.T) {
	// TODOS.md PL-011's own verify line: "a 500-item Sort at the Quick tier
	// returns a permutation of the input". 500 is defaultMaxItemsPerCall
	// (plan.go)'s item-count ceiling, but packChunks (PL-004) also bounds a
	// chunk by the output-token reserve -- outputTokensPerItemDefault (24)
	// times 500 items is 12,000 completion tokens, which does not fit the
	// small MaxOutputTokens a default Quick-tier capability snapshot
	// assumes. A caller running 500 items in one call has to say so, the
	// same way any other MDSP caller sizing a large batch does -- this test
	// sets Options.MaxOutputTokens generously enough to hold 500 items'
	// worth of output, which is what makes "fits one chunk" true rather than
	// assumed.
	op := reverseSortOp()
	items := make([]int, 500)
	for i := range items {
		items[i] = i
	}
	provider := &algebraFakeProvider{}
	ctx := algebraCtx(t, provider)

	req := types.PlanRequest{Options: types.OpOptions{Intelligence: types.Quick, MaxOutputTokens: 20000}}

	result, plan, err := RunOpGlobal(ctx, op, items, req)
	if err != nil {
		t.Fatalf("500 items should fit in one chunk once the output budget accounts for them: %v", err)
	}
	if len(plan.Chunks) != 1 {
		t.Fatalf("expected exactly one chunk for 500 items at the default cap, got %d", len(plan.Chunks))
	}
	if err := SameMultiset(items, result.Value); err != nil {
		t.Errorf("500-item result is not a permutation of the input: %v", err)
	}
}

func TestRunOpGlobalZeroItemsReturnsEmptyWithoutError(t *testing.T) {
	op := reverseSortOp()
	provider := &algebraFakeProvider{}
	ctx := algebraCtx(t, provider)

	result, plan, err := RunOpGlobal(ctx, op, nil, types.PlanRequest{})
	if err != nil {
		t.Fatalf("zero items should not be an error: %v", err)
	}
	if len(result.Value) != 0 {
		t.Errorf("expected no values for zero items, got %d", len(result.Value))
	}
	if provider.calls != 0 {
		t.Errorf("zero items should make no provider call, got %d", provider.calls)
	}
	_ = plan
}
