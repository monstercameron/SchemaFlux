package ops

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// PL-012. fusion.go's own unit tests: the compatibility key's reflection
// property, FusionGroup's basic mechanics, and the correctness guarantee
// fusion is supposed to buy nothing at the cost of.
//
// fusionOp builds a minimal Op[string, string] with the same shape echoOp
// (op_kernel_seam_test.go, this package) uses, plus a stable SchemaName --
// declaredContractLevel reads that to produce a non-default ContractLevel,
// which several tests below need distinguishable from the zero value.
func fusionOp(name, version string) Op[string, string] {
	return Op[string, string]{
		ID:        types.OperationID{Name: name, Version: version},
		Semantics: types.Semantics{Stability: types.StabilityExperimental},
		Contract: OutputContract[string]{
			SchemaName: "widget",
			Decode:     func(body string) (string, error) { return body, nil },
		},
		Batch: BatchAlgebra[string, string]{Class: types.BatchNone},
		BuildPrompt: func(input string, _ types.OpOptions) (string, string) {
			return "system", input
		},
	}
}

// nonZeroFusionKey fills every field with a distinguishable non-zero value,
// the same fixture discipline optionsurvival_test.go's nonZeroOpOptions and
// providerconfig_override_test.go's nonZeroProviderConfig already use for
// exactly this reason: a field left at its zero value is a field no test
// below actually exercises.
func nonZeroFusionKey() FusionKey {
	return FusionKey{
		Operation:  types.OperationID{Name: "widget-op", Version: "v1"},
		SchemaHash: "widget@v1#abc123",
		Route:      RoutePolicy{Model: "gpt-5", Intelligence: types.Smart},
		Steering:   "prefer concise answers",
		Contract:   types.ContractSchemaConstrained,
		DataPolicy: types.DataPolicy{
			Classification:   "restricted",
			AllowedProviders: []string{"openai"},
			AllowedRegions:   []string{"us"},
			MaxRetentionDays: 30,
			ContentLogging:   types.LogMetadataOnly,
		},
		Budget: BudgetSettings{CeilingUSD: 5.0, Scope: "tenant-a"},
	}
}

func TestEveryFusionKeyFieldIsPopulatedInTheFixture(t *testing.T) {
	value := reflect.ValueOf(nonZeroFusionKey())
	structType := value.Type()
	for i := 0; i < value.NumField(); i++ {
		if value.Field(i).IsZero() {
			t.Errorf("nonZeroFusionKey leaves %s at its zero value, so no reflection test below covers it",
				structType.Field(i).Name)
		}
	}
}

// bumpFusionKeyField returns a copy of base with only the field at index i
// changed to a distinguishable different value, so equal() can be checked
// against "everything else held constant." It is written per-kind rather
// than per-field-name deliberately: FusionKey.equal is reflection-driven
// (walks whatever fields the struct has), so its test has to be too, or the
// test would be exactly the hand-written list this task warns against, one
// layer up. A field of a kind this function does not yet handle fails the
// test with a named error rather than being silently skipped -- see the
// default case.
func bumpFusionKeyField(t *testing.T, base FusionKey, i int) FusionKey {
	t.Helper()
	out := base
	v := reflect.ValueOf(&out).Elem().Field(i)
	name := reflect.TypeOf(base).Field(i).Name

	switch name {
	case "Operation":
		v.Set(reflect.ValueOf(types.OperationID{Name: "different-op", Version: "v2"}))
	case "SchemaHash":
		v.SetString(v.String() + "-different")
	case "Route":
		v.Set(reflect.ValueOf(RoutePolicy{Model: "different-model", Intelligence: types.Quick}))
	case "Steering":
		v.SetString(v.String() + " and be different")
	case "Contract":
		v.SetUint(v.Uint() + 1)
	case "DataPolicy":
		dp := v.Interface().(types.DataPolicy)
		dp.Classification = dp.Classification + "-different"
		v.Set(reflect.ValueOf(dp))
	case "Budget":
		b := v.Interface().(BudgetSettings)
		b.CeilingUSD = b.CeilingUSD + 1
		v.Set(reflect.ValueOf(b))
	default:
		t.Fatalf("bumpFusionKeyField does not know how to mutate field %q -- FusionKey grew a field "+
			"this test was not extended to cover; extend this switch before trusting equal() on it", name)
	}
	return out
}

// TestFusionKeyEqualityIsReflectionDrivenPerField is the direct proof for
// this task's own instruction 1: walking FusionKey by reflection (not a
// hand-picked subset of its fields), for every field it constructs a second
// key differing in ONLY that field and asserts equal() reports them
// unequal. Extending FusionKey with an eighth axis extends this test's
// coverage automatically (NumField() grows), and fails loudly via
// bumpFusionKeyField's default case until that field is taught how to
// differ -- the field cannot silently stay unchecked.
func TestFusionKeyEqualityIsReflectionDrivenPerField(t *testing.T) {
	base := nonZeroFusionKey()
	if !base.equal(base) {
		t.Fatal("a key does not equal itself")
	}

	structType := reflect.TypeOf(base)
	for i := 0; i < structType.NumField(); i++ {
		name := structType.Field(i).Name
		t.Run(name, func(t *testing.T) {
			mutated := bumpFusionKeyField(t, base, i)
			if base.equal(mutated) {
				t.Errorf("equal() returned true after only %s changed; this field is being silently ignored by the compatibility key", name)
			}
			if mutated.equal(base) {
				t.Errorf("equal() is not symmetric for a %s-only difference", name)
			}
		})
	}
}

// TestFusionKeyEqualIsReflexiveForTwoIndependentlyBuiltKeys pins that
// equal() is a real value comparison, not identity: two keys built from
// scratch through deriveFusionKey with the same inputs must agree, even
// though they share no memory.
func TestFusionKeyEqualIsReflexiveForTwoIndependentlyBuiltKeys(t *testing.T) {
	op := fusionOp("extract", "v1")
	opt := types.OpOptions{SchemaID: "widget@v1", Model: "gpt-5", Steering: "be terse"}
	fo := FusionOptions{DataPolicy: types.DataPolicy{Classification: "public"}, Budget: BudgetSettings{CeilingUSD: 1}}

	a := deriveFusionKey(op, opt, fo)
	b := deriveFusionKey(op, opt, fo)

	if !a.equal(b) {
		t.Fatalf("two keys derived from identical inputs were not equal: %+v vs %+v", a, b)
	}
}

// TestDeriveFusionKeyReadsSchemaIDNotSchemaName proves axis 2 ("schema
// hash") is opt.SchemaID -- the field types.go documents as "the schema's
// full identity -- name, version, hash, dialect" -- and not op.Contract.
// SchemaName, which is only a short label two different schema versions
// could share.
func TestDeriveFusionKeyReadsSchemaIDNotSchemaName(t *testing.T) {
	op := fusionOp("extract", "v1") // Contract.SchemaName == "widget", fixed
	key := deriveFusionKey(op, types.OpOptions{SchemaID: "widget@v7#deadbeef"}, FusionOptions{})
	if key.SchemaHash != "widget@v7#deadbeef" {
		t.Errorf("SchemaHash = %q, want opt.SchemaID verbatim", key.SchemaHash)
	}
}

// TestDeriveFusionKeyReadsRouteFromModelAndIntelligence proves axis 3.
func TestDeriveFusionKeyReadsRouteFromModelAndIntelligence(t *testing.T) {
	op := fusionOp("extract", "v1")
	key := deriveFusionKey(op, types.OpOptions{Model: "pinned-model", Intelligence: types.Quick}, FusionOptions{})
	want := RoutePolicy{Model: "pinned-model", Intelligence: types.Quick}
	if key.Route != want {
		t.Errorf("Route = %+v, want %+v", key.Route, want)
	}
}

// TestDeriveFusionKeyReadsDeclaredContractLevel proves axis 5: the same
// function RunOpResult itself uses (declaredContractLevel, contractlevel.go)
// governs the contract-level axis, not a second calculation invented here.
func TestDeriveFusionKeyReadsDeclaredContractLevel(t *testing.T) {
	withInvariant := fusionOp("extract", "v1")
	withInvariant.Contract.Invariants = []func(string) error{func(string) error { return nil }}

	plain := fusionOp("extract", "v1")

	keyWith := deriveFusionKey(withInvariant, types.OpOptions{}, FusionOptions{})
	keyPlain := deriveFusionKey(plain, types.OpOptions{}, FusionOptions{})

	if keyWith.Contract != types.ContractSchemaAndInvariantChecked {
		t.Errorf("Contract = %v, want ContractSchemaAndInvariantChecked", keyWith.Contract)
	}
	if keyPlain.Contract != types.ContractSchemaConstrained {
		t.Errorf("Contract = %v, want ContractSchemaConstrained", keyPlain.Contract)
	}
	if keyWith.equal(keyPlain) {
		t.Error("two ops with different declared contract levels produced equal FusionKeys")
	}
}

// --- FusionGroup mechanics.

// TestFusionGroupLenTracksDeferredBuilders is a basic sanity check on the
// bookkeeping every other test in this file and fusion_partition_test.go
// depends on.
func TestFusionGroupLenTracksDeferredBuilders(t *testing.T) {
	g := NewFusionGroup[string, string]()
	op := fusionOp("echo", "v1")
	for i := 0; i < 5; i++ {
		g.Defer(op, "x", types.OpOptions{}, FusionOptions{})
	}
	if g.Len() != 5 {
		t.Fatalf("Len() = %d, want 5", g.Len())
	}
}

// TestUnresolvedHandleReportsNotDone proves Result() before Run is honest
// about not having an answer, rather than returning the zero value dressed
// up as a real one.
func TestUnresolvedHandleReportsNotDone(t *testing.T) {
	g := NewFusionGroup[string, string]()
	h := g.Defer(fusionOp("echo", "v1"), "x", types.OpOptions{}, FusionOptions{})

	_, ok := h.Result()
	if ok {
		t.Fatal("Result() reported done before Run ever executed")
	}
}

// TestZeroValueHandleReportsNotDone: a caller who somehow holds a bare
// FusionHandle{} (never returned by Defer) gets "not done," not a panic or
// a zero-value success.
func TestZeroValueHandleReportsNotDone(t *testing.T) {
	var h FusionHandle[string, string]
	_, ok := h.Result()
	if ok {
		t.Fatal("a zero-value handle reported done")
	}
}

// TestRunOnEmptyGroupSucceeds: a group nothing was deferred to is a
// legitimate, if useless, group -- Run must not error or panic on it.
func TestRunOnEmptyGroupSucceeds(t *testing.T) {
	g := NewFusionGroup[string, string]()
	if err := g.Run(context.Background(), PartialConfig{}); err != nil {
		t.Fatalf("Run() on an empty group = %v, want nil", err)
	}
}

// TestRunningAGroupTwiceIsRefused proves ErrFusionGroupAlreadyRan fires
// rather than silently duplicating (or silently no-op-ing) every builder's
// execution.
func TestRunningAGroupTwiceIsRefused(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	provider := &deterministicEchoProvider{}
	ctx := WithProvider(context.Background(), provider)

	g := NewFusionGroup[string, string]()
	g.Defer(fusionOp("echo", "v1"), "x", types.OpOptions{}, FusionOptions{})

	if err := g.Run(ctx, PartialConfig{}); err != nil {
		t.Fatalf("first Run() = %v, want nil", err)
	}
	firstCalls := provider.callCount()

	err := g.Run(ctx, PartialConfig{})
	if err == nil {
		t.Fatal("second Run() succeeded; a group must be one-shot")
	}
	if err != ErrFusionGroupAlreadyRan {
		t.Errorf("second Run() = %v, want ErrFusionGroupAlreadyRan", err)
	}
	if provider.callCount() != firstCalls {
		t.Errorf("provider was called again on the refused second Run: %d -> %d calls", firstCalls, provider.callCount())
	}
}

// TestHandlesResolveAfterRun proves every handle becomes resolvable once
// Run returns, and each carries its own builder's value -- not one shared
// value, and not resolved by array position (each handle here is deferred
// with a different input, and each must come back with that input's own
// answer).
func TestHandlesResolveAfterRun(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	provider := &deterministicEchoProvider{}
	ctx := WithProvider(context.Background(), provider)

	g := NewFusionGroup[string, string]()
	op := fusionOp("echo", "v1")

	inputs := []string{"alpha", "bravo", "charlie", "delta", "echo-item"}
	handles := make([]FusionHandle[string, string], len(inputs))
	for i, in := range inputs {
		handles[i] = g.Defer(op, in, types.OpOptions{}, FusionOptions{})
	}

	if err := g.Run(ctx, PartialConfig{}); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	for i, h := range handles {
		res, ok := h.Result()
		if !ok {
			t.Fatalf("handle %d not resolved after Run", i)
		}
		if res.Status != types.ItemSucceeded {
			t.Fatalf("handle %d status = %v, want ItemSucceeded (err=%v)", i, res.Status, res.Err)
		}
		if res.Value != inputs[i] {
			t.Fatalf("handle %d value = %q, want %q (its own input echoed back)", i, res.Value, inputs[i])
		}
	}
}

// TestFusionDoesNotChangeResultsRelativeToRunningAlone is PL-012's own
// Verify line, the value half: fifty builders run through one FusionGroup
// (all compatible, so they fuse into a single partition) produce the exact
// same values as the same fifty inputs run one RunOpResult call at a time,
// with no group involved at all.
//
// deterministicEchoProvider's response is a pure function of the request's
// UserPrompt (echoOp/fusionOp's BuildPrompt renders the input verbatim as
// the user prompt), not of call order -- so this is meaningful under
// RunOpManyPartial's concurrent dispatch: nothing about which goroutine
// reaches the provider first can change which input maps to which output.
func TestFusionDoesNotChangeResultsRelativeToRunningAlone(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	const n = 50
	op := fusionOp("echo", "v1")
	opt := types.OpOptions{}

	inputs := make([]string, n)
	for i := range inputs {
		inputs[i] = "item-" + string(rune('a'+i%26)) + "-" + itoa(i)
	}

	// Unfused: one RunOpResult per input, independently.
	unfused := make([]string, n)
	for i, in := range inputs {
		provider := &deterministicEchoProvider{}
		ctx := WithProvider(context.Background(), provider)
		res, err := RunOpResult(ctx, op, in, opt)
		if err != nil {
			t.Fatalf("unfused item %d: %v", i, err)
		}
		unfused[i] = res.Value
	}

	// Fused: all fifty deferred to one group, one partition, one Run.
	provider := &deterministicEchoProvider{}
	ctx := WithProvider(context.Background(), provider)
	g := NewFusionGroup[string, string]()
	handles := make([]FusionHandle[string, string], n)
	for i, in := range inputs {
		handles[i] = g.Defer(op, in, opt, FusionOptions{})
	}
	if err := g.Run(ctx, PartialConfig{}); err != nil {
		t.Fatalf("fused Run(): %v", err)
	}

	for i := range inputs {
		res, ok := handles[i].Result()
		if !ok {
			t.Fatalf("fused item %d never resolved", i)
		}
		if res.Value != unfused[i] {
			t.Errorf("item %d: fused value %q != unfused value %q", i, res.Value, unfused[i])
		}
	}

	// And exactly one partition formed -- fifty identical builders are the
	// case fusion exists to collapse into one call shape.
	if got := len(partitionEntries(collectEntriesForTest(g))); got != 1 {
		t.Errorf("fifty identical builders formed %d partitions, want 1", got)
	}
}

// itoa avoids importing strconv into a file that otherwise has no need for
// it, for a handful of small non-negative ints in a test loop.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}

// collectEntriesForTest reaches into a group's own entries slice, for a
// test that needs to check partitioning directly rather than only through
// Run's externally-visible effects. Defined here rather than exported
// because nothing outside this package's own tests should ever need it --
// partitionEntries/compatible/FusionKey.equal are the seam a caller-facing
// test exercises through Defer/Run, not this.
func collectEntriesForTest[In, Out any](g *FusionGroup[In, Out]) []*fusionEntry[In, Out] {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*fusionEntry[In, Out](nil), g.entries...)
}

// deterministicEchoProvider answers every call with the request's own
// UserPrompt, so its response depends only on which input a call carries --
// never on call order, goroutine scheduling, or how many other calls a
// concurrent RunOpManyPartial dispatch is making at the same time. This is
// what makes TestFusionDoesNotChangeResultsRelativeToRunningAlone and
// TestHandlesResolveAfterRun meaningful under concurrent execution:
// scriptedProvider (op_kernel_seam_test.go), by contrast, answers by call
// ORDER, which is exactly what a concurrent multi-item dispatch does not
// hold still.
type deterministicEchoProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *deterministicEchoProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return llm.CompletionResponse{
		Content:      req.UserPrompt,
		Provider:     "deterministic-echo",
		Model:        "fake-model",
		FinishReason: "stop",
	}, nil
}

func (p *deterministicEchoProvider) Name() string                               { return "deterministic-echo" }
func (p *deterministicEchoProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *deterministicEchoProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }

func (p *deterministicEchoProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}
