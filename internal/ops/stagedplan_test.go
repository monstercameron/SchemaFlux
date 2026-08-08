package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Shared fixtures for stagedplan_test.go and stagedplan_reuse_test.go: a
// two-stage extract-then-verify plan is PL-010's own verify line, so both
// files build the same shape of plan against a fake provider rather than
// each inventing a smaller, less representative one.

// testDatum is the "one datum" a StagedPlan carries through its stages.
// Source is the raw input extract reads from; Name and Verified are what the
// two stages establish. A struct rather than a string makes the "structured
// intermediate, not the resent source" claim checkable: verify's stage only
// ever sees Name, never Source.
type testDatum struct {
	Source   string
	Name     string
	Verified bool
}

// decodeTestDatum is the Decode function both stages in these tests share --
// there is one JSON shape a stage answers with, {"name": "...", "verified":
// bool}, and both stages parse it the same way.
func decodeTestDatum(body string) (testDatum, error) {
	var raw struct {
		Name     string `json:"name"`
		Verified bool   `json:"verified"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return testDatum{}, fmt.Errorf("decode: %w", err)
	}
	if raw.Name == "" {
		return testDatum{}, fmt.Errorf("decode: empty name")
	}
	return testDatum{Name: raw.Name, Verified: raw.Verified}, nil
}

// newExtractStageOp builds the "extract" stage's Op: BuildPrompt reads
// input.Source (the only stage in these fixtures that ever does), Decode
// throws Source away -- the extracted testDatum has no Source field set --
// which is what makes the next stage's input a structured intermediate
// rather than the original source resent.
func newExtractStageOp() Op[testDatum, testDatum] {
	return Op[testDatum, testDatum]{
		ID: types.OperationID{Name: "stagedplan-test-extract", Version: "v1"},
		Contract: OutputContract[testDatum]{
			SchemaName: "testDatum",
			Decode:     decodeTestDatum,
			Invariants: []func(testDatum) error{
				func(d testDatum) error {
					if d.Name == "" {
						return fmt.Errorf("extract: name is empty")
					}
					return nil
				},
			},
		},
		BuildPrompt: func(input testDatum, opt types.OpOptions) (string, string) {
			return "extract-system", input.Source
		},
	}
}

// newVerifyStageOp builds the "verify" stage's Op. Its BuildPrompt reads
// input.Name -- never input.Source, which extract's Decode already dropped.
func newVerifyStageOp() Op[testDatum, testDatum] {
	return Op[testDatum, testDatum]{
		ID: types.OperationID{Name: "stagedplan-test-verify", Version: "v1"},
		Contract: OutputContract[testDatum]{
			SchemaName: "testDatum",
			Decode:     decodeTestDatum,
			Invariants: []func(testDatum) error{
				func(d testDatum) error {
					if !d.Verified {
						return fmt.Errorf("verify: not verified")
					}
					return nil
				},
			},
		},
		BuildPrompt: func(input testDatum, opt types.OpOptions) (string, string) {
			return "verify-system", input.Name
		},
	}
}

// stagedFakeProvider is the "counting fake provider" PL-010's verify line
// asks for: every Complete call is counted, and respond decides what each
// numbered call answers with, so a test can tell an extract call from a
// verify call by its position without inspecting any request content the
// library would otherwise have to log.
type stagedFakeProvider struct {
	mu      sync.Mutex
	calls   int
	curConc int
	maxConc int
	respond func(call int, req llm.CompletionRequest) string
	sleep   time.Duration
	barrier *sync.WaitGroup // if set, Complete waits on it before answering
}

func (p *stagedFakeProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.curConc++
	if p.curConc > p.maxConc {
		p.maxConc = p.curConc
	}
	p.mu.Unlock()

	if p.barrier != nil {
		p.barrier.Done()
		p.barrier.Wait()
	}
	if p.sleep > 0 {
		time.Sleep(p.sleep)
	}

	content := `{"name":"unset"}`
	if p.respond != nil {
		content = p.respond(call, req)
	}

	p.mu.Lock()
	p.curConc--
	p.mu.Unlock()

	return llm.CompletionResponse{Content: content, Model: "fake-model", Provider: "fake"}, nil
}

func (p *stagedFakeProvider) Name() string                                   { return "fake" }
func (p *stagedFakeProvider) EstimateCost(req llm.CompletionRequest) float64 { return 0 }
func (p *stagedFakeProvider) RetryPolicy() (int, time.Duration)              { return 0, time.Millisecond }

func (p *stagedFakeProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// extractThenVerifyPlan builds the canonical two-stage plan these tests run:
// step 1 always calls extract (no DeterministicCheck -- extraction has no
// deterministic substitute), step 2 runs verify's check.
func extractThenVerifyPlan(verifyCheck func(testDatum) (testDatum, types.ContractLevel, bool)) StagedPlan[testDatum] {
	return StagedPlan[testDatum]{
		ID: types.OperationID{Name: "extract-then-verify", Version: "v1"},
		Steps: []PlanStep[testDatum]{
			{Stages: []Stage[testDatum]{{ID: types.OperationID{Name: "extract"}, Op: newExtractStageOp()}}},
			{Stages: []Stage[testDatum]{{ID: types.OperationID{Name: "verify"}, Op: newVerifyStageOp(), DeterministicCheck: verifyCheck}}},
		},
	}
}

// respondExtractThenVerify answers call 1 (extract) with an unverified name
// and call 2 (verify, if it happens) with the same name marked verified.
func respondExtractThenVerify(call int, req llm.CompletionRequest) string {
	if call == 1 {
		return `{"name":"Alice"}`
	}
	return `{"name":"Alice","verified":true}`
}

// 1. A single-stage plan calls its Op exactly once and returns its value.
func TestRunStagedPlan_SingleStageCallsOpOnce(t *testing.T) {
	provider := &stagedFakeProvider{respond: func(call int, req llm.CompletionRequest) string {
		return `{"name":"Solo"}`
	}}
	ctx := WithProvider(context.Background(), provider)

	plan := StagedPlan[testDatum]{
		ID:    types.OperationID{Name: "solo"},
		Steps: []PlanStep[testDatum]{{Stages: []Stage[testDatum]{{Op: newExtractStageOp()}}}},
	}

	result, err := RunStagedPlan(ctx, plan, testDatum{Source: "raw"}, types.OpOptions{Model: "test-model"})
	if err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}
	if result.Value.Name != "Solo" {
		t.Fatalf("Value.Name = %q, want Solo", result.Value.Name)
	}
	if provider.callCount() != 1 {
		t.Fatalf("provider called %d times, want 1", provider.callCount())
	}
}

// 2. A two-stage sequential plan passes the structured intermediate (Name)
// to the second stage, never the raw source -- proved by inspecting what
// the verify stage's BuildPrompt actually received.
func TestRunStagedPlan_PassesStructuredIntermediateNotSource(t *testing.T) {
	var verifyInput testDatum
	verifyOp := newVerifyStageOp()
	verifyOp.BuildPrompt = func(input testDatum, opt types.OpOptions) (string, string) {
		verifyInput = input
		return "verify-system", input.Name
	}

	provider := &stagedFakeProvider{respond: respondExtractThenVerify}
	ctx := WithProvider(context.Background(), provider)

	plan := StagedPlan[testDatum]{
		ID: types.OperationID{Name: "extract-then-verify"},
		Steps: []PlanStep[testDatum]{
			{Stages: []Stage[testDatum]{{Op: newExtractStageOp()}}},
			{Stages: []Stage[testDatum]{{Op: verifyOp}}},
		},
	}

	source := "the quick brown fox document"
	if _, err := RunStagedPlan(ctx, plan, testDatum{Source: source}, types.OpOptions{Model: "test-model"}); err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}

	if verifyInput.Source == source {
		t.Fatalf("verify stage saw the original source; PL-010 asks for a structured intermediate instead")
	}
	if verifyInput.Name != "Alice" {
		t.Fatalf("verify stage input Name = %q, want the extract stage's structured output %q", verifyInput.Name, "Alice")
	}
}

// 3. A plan with no steps refuses rather than silently returning the input
// unchanged.
func TestRunStagedPlan_EmptyPlanErrors(t *testing.T) {
	plan := StagedPlan[testDatum]{ID: types.OperationID{Name: "empty"}}
	_, err := RunStagedPlan(context.Background(), plan, testDatum{}, types.OpOptions{Model: "test-model"})
	if err == nil {
		t.Fatal("RunStagedPlan with no steps returned nil error")
	}
}

// 4. A step with no stages refuses -- the same "never fail open" rule
// applied one level down from an empty plan.
func TestRunStagedPlan_EmptyStepErrors(t *testing.T) {
	plan := StagedPlan[testDatum]{
		ID:    types.OperationID{Name: "empty-step"},
		Steps: []PlanStep[testDatum]{{Stages: nil}},
	}
	_, err := RunStagedPlan(context.Background(), plan, testDatum{}, types.OpOptions{Model: "test-model"})
	if err == nil {
		t.Fatal("RunStagedPlan with an empty step returned nil error")
	}
}

// 5. A stage whose Op fails ends the plan with a wrapped error rather than
// a partial, silently-wrong result.
func TestRunStagedPlan_StageOpFailurePropagates(t *testing.T) {
	provider := &stagedFakeProvider{respond: func(call int, req llm.CompletionRequest) string {
		return `{}` // decodeTestDatum rejects an empty name every time
	}}
	ctx := WithProvider(context.Background(), provider)

	plan := StagedPlan[testDatum]{
		ID:    types.OperationID{Name: "failing"},
		Steps: []PlanStep[testDatum]{{Stages: []Stage[testDatum]{{Op: newExtractStageOp()}}}},
	}

	_, err := RunStagedPlan(ctx, plan, testDatum{Source: "x"}, types.OpOptions{Model: "test-model"})
	if err == nil {
		t.Fatal("RunStagedPlan with a failing stage returned nil error")
	}
}

// 6. Lineage: the verify stage's Provenance.ParentResultIDs names exactly
// the extract stage's ResultID -- TC-003's chaining, one stage to the next.
func TestRunStagedPlan_LineageChainsParentResultIDs(t *testing.T) {
	provider := &stagedFakeProvider{respond: respondExtractThenVerify}
	ctx := WithProvider(context.Background(), provider)

	plan := extractThenVerifyPlan(nil) // no check: verify always calls its Op
	result, err := RunStagedPlan(ctx, plan, testDatum{Source: "x"}, types.OpOptions{Model: "test-model"})
	if err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}

	extractID := result.Steps[0][0].Provenance.ResultID
	verifyParents := result.Steps[1][0].Provenance.ParentResultIDs

	if extractID == "" {
		t.Fatal("extract stage produced no ResultID")
	}
	if len(verifyParents) != 1 || verifyParents[0] != extractID {
		t.Fatalf("verify stage ParentResultIDs = %v, want [%s]", verifyParents, extractID)
	}
}

// 7. A stage with no explicit ID defaults to its Op's ID -- the ordinary
// case, where the stage IS the operation.
func TestRunStagedPlan_StageIDDefaultsToOpID(t *testing.T) {
	provider := &stagedFakeProvider{respond: func(call int, req llm.CompletionRequest) string {
		return `{"name":"X"}`
	}}
	ctx := WithProvider(context.Background(), provider)

	op := newExtractStageOp()
	plan := StagedPlan[testDatum]{
		ID:    types.OperationID{Name: "default-id"},
		Steps: []PlanStep[testDatum]{{Stages: []Stage[testDatum]{{Op: op}}}}, // no Stage.ID set
	}

	result, err := RunStagedPlan(ctx, plan, testDatum{Source: "x"}, types.OpOptions{Model: "test-model"})
	if err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}
	if result.Steps[0][0].StageID != op.ID {
		t.Fatalf("StageID = %v, want it to default to Op.ID %v", result.Steps[0][0].StageID, op.ID)
	}
}

// 8. A stage with an explicit ID keeps it, distinct from its Op's ID --
// proves the two identities do not collapse into one when a caller states
// both.
func TestRunStagedPlan_ExplicitStageIDOverridesOpID(t *testing.T) {
	provider := &stagedFakeProvider{respond: func(call int, req llm.CompletionRequest) string {
		return `{"name":"X"}`
	}}
	ctx := WithProvider(context.Background(), provider)

	stageID := types.OperationID{Name: "second-extract-pass", Version: "v2"}
	op := newExtractStageOp() // op.ID stays "stagedplan-test-extract@v1"
	plan := StagedPlan[testDatum]{
		ID:    types.OperationID{Name: "explicit-id"},
		Steps: []PlanStep[testDatum]{{Stages: []Stage[testDatum]{{ID: stageID, Op: op}}}},
	}

	result, err := RunStagedPlan(ctx, plan, testDatum{Source: "x"}, types.OpOptions{Model: "test-model"})
	if err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}
	if result.Steps[0][0].StageID != stageID {
		t.Fatalf("StageID = %v, want the explicit %v", result.Steps[0][0].StageID, stageID)
	}
	if result.Steps[0][0].StageID == op.ID {
		t.Fatal("explicit Stage.ID collapsed into Op.ID")
	}
}

// 9. A group of independent stages runs concurrently and both outcomes come
// back in the plan's declared order, regardless of which goroutine actually
// finished first.
func TestRunStagedPlan_GroupRunsStagesConcurrentlyInDeclaredOrder(t *testing.T) {
	var barrier sync.WaitGroup
	barrier.Add(2) // released only once BOTH stages' Complete calls have started

	checkA := Op[testDatum, testDatum]{
		ID: types.OperationID{Name: "check-a"},
		Contract: OutputContract[testDatum]{
			SchemaName: "testDatum", Decode: decodeTestDatum,
		},
		BuildPrompt: func(input testDatum, opt types.OpOptions) (string, string) { return "a", input.Name },
	}
	checkB := Op[testDatum, testDatum]{
		ID: types.OperationID{Name: "check-b"},
		Contract: OutputContract[testDatum]{
			SchemaName: "testDatum", Decode: decodeTestDatum,
		},
		BuildPrompt: func(input testDatum, opt types.OpOptions) (string, string) { return "b", input.Name },
	}

	provider := &stagedFakeProvider{
		barrier: &barrier,
		respond: func(call int, req llm.CompletionRequest) string { return `{"name":"grouped"}` },
	}
	ctx := WithProvider(context.Background(), provider)

	plan := StagedPlan[testDatum]{
		ID: types.OperationID{Name: "group"},
		Steps: []PlanStep[testDatum]{
			{Stages: []Stage[testDatum]{
				{ID: types.OperationID{Name: "first"}, Op: checkA},
				{ID: types.OperationID{Name: "second"}, Op: checkB},
			}},
		},
	}

	// If the two stages were run sequentially rather than concurrently, the
	// second stage's Complete would never start until the first one's had
	// already returned -- and the first's Complete is blocked on a barrier
	// that only releases once TWO calls have started. A sequential
	// implementation would hang here; WithTimeout below turns that hang into
	// a failed test instead of a stuck test run.
	done := make(chan struct{})
	var result StagedPlanResult[testDatum]
	var err error
	go func() {
		result, err = RunStagedPlan(ctx, plan, testDatum{Name: "seed"}, types.OpOptions{Model: "test-model"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunStagedPlan did not run the group's stages concurrently (timed out on the barrier)")
	}

	if err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}
	if len(result.Steps[0]) != 2 {
		t.Fatalf("group produced %d outcomes, want 2", len(result.Steps[0]))
	}
	if result.Steps[0][0].StageID.Name != "first" || result.Steps[0][1].StageID.Name != "second" {
		t.Fatalf("group outcomes out of declared order: %v, %v", result.Steps[0][0].StageID, result.Steps[0][1].StageID)
	}
}

// 10. A multi-stage group step is independent checks over the current datum
// -- it does not choose a new one. The datum entering the step after a
// group is exactly the datum that entered the group, even though the
// group's own stages returned different values.
func TestRunStagedPlan_GroupDoesNotChangeTheDatumForTheNextStep(t *testing.T) {
	checkA := Op[testDatum, testDatum]{
		ID:          types.OperationID{Name: "check-a"},
		Contract:    OutputContract[testDatum]{SchemaName: "testDatum", Decode: decodeTestDatum},
		BuildPrompt: func(input testDatum, opt types.OpOptions) (string, string) { return "a", input.Name },
	}
	checkB := Op[testDatum, testDatum]{
		ID:          types.OperationID{Name: "check-b"},
		Contract:    OutputContract[testDatum]{SchemaName: "testDatum", Decode: decodeTestDatum},
		BuildPrompt: func(input testDatum, opt types.OpOptions) (string, string) { return "b", input.Name },
	}

	var nextStageInput testDatum
	nextOp := Op[testDatum, testDatum]{
		ID:       types.OperationID{Name: "after-group"},
		Contract: OutputContract[testDatum]{SchemaName: "testDatum", Decode: decodeTestDatum},
		BuildPrompt: func(input testDatum, opt types.OpOptions) (string, string) {
			nextStageInput = input
			return "after", input.Name
		},
	}

	call := 0
	provider := &stagedFakeProvider{respond: func(c int, req llm.CompletionRequest) string {
		call++
		switch call {
		case 1:
			return `{"name":"from-a"}`
		case 2:
			return `{"name":"from-b"}`
		default:
			return `{"name":"from-next"}`
		}
	}}
	ctx := WithProvider(context.Background(), provider)

	plan := StagedPlan[testDatum]{
		ID: types.OperationID{Name: "group-then-next"},
		Steps: []PlanStep[testDatum]{
			{Stages: []Stage[testDatum]{{Op: checkA}, {Op: checkB}}},
			{Stages: []Stage[testDatum]{{Op: nextOp}}},
		},
	}

	seed := testDatum{Name: "seed"}
	if _, err := RunStagedPlan(ctx, plan, seed, types.OpOptions{Model: "test-model"}); err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}

	if nextStageInput.Name != seed.Name {
		t.Fatalf("stage after the group saw Name %q, want the pre-group datum's %q (neither from-a nor from-b)",
			nextStageInput.Name, seed.Name)
	}
}
