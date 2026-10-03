package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Shared fixtures for stagedplan_test.go and stagedplan_reuse_test.go: a
// two-stage extract-then-verify plan is the verify line of a staged plan, so both
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

// stagedFakeProvider is the "counting fake provider" the verify line of a staged plan
// asks for: every Complete call is counted, and respond decides what each
// numbered call answers with, so a test can tell an extract call from a
// verify call by its position without inspecting any request content the
// library would otherwise have to log.
type stagedFakeProvider struct {
	mu      sync.Mutex
	calls   int
	respond func(call int, req llm.CompletionRequest) string
}

func (p *stagedFakeProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()

	content := `{"name":"unset"}`
	if p.respond != nil {
		content = p.respond(call, req)
	}

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
		Stages: []Stage[testDatum]{
			{ID: types.OperationID{Name: "extract"}, Op: newExtractStageOp()},
			{ID: types.OperationID{Name: "verify"}, Op: newVerifyStageOp(), DeterministicCheck: verifyCheck},
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
		ID:     types.OperationID{Name: "solo"},
		Stages: []Stage[testDatum]{{Op: newExtractStageOp()}},
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
		Stages: []Stage[testDatum]{
			{Op: newExtractStageOp()},
			{Op: verifyOp},
		},
	}

	source := "the quick brown fox document"
	if _, err := RunStagedPlan(ctx, plan, testDatum{Source: source}, types.OpOptions{Model: "test-model"}); err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}

	if verifyInput.Source == source {
		t.Fatalf("verify stage saw the original source; a staged plan hands on a structured intermediate instead")
	}
	if verifyInput.Name != "Alice" {
		t.Fatalf("verify stage input Name = %q, want the extract stage's structured output %q", verifyInput.Name, "Alice")
	}
}

// 3. A plan with no steps refuses rather than silently returning the input
// unchanged.
func TestRunStagedPlan_EmptyPlanErrors(t *testing.T) {
	provider := &stagedFakeProvider{}
	plan := StagedPlan[testDatum]{ID: types.OperationID{Name: "empty"}}
	result, err := RunStagedPlan(WithProvider(context.Background(), provider), plan, testDatum{}, types.OpOptions{Model: "test-model"})
	if types.KindOf(err) != types.KindInvalidRequest || result.Stages != nil {
		t.Fatalf("a plan with no stages: err = %v (%v), stages %v; want an invalid request and nothing beside it", err, types.KindOf(err), result.Stages)
	}
	if provider.callCount() != 0 {
		t.Errorf("%d requests were sent for a plan with no stages", provider.callCount())
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
		ID:     types.OperationID{Name: "failing"},
		Stages: []Stage[testDatum]{{Op: newExtractStageOp()}},
	}

	result, err := RunStagedPlan(ctx, plan, testDatum{Source: "x"}, types.OpOptions{Model: "test-model"})
	var failure *types.OperationError
	if !errors.As(err, &failure) || failure.Message != "stage 1 of 1 failed" || failure.Op != "stagedplan-test-extract" {
		t.Fatalf("err = %v, want the stage's failure, named by its place and its operation", err)
	}
	if types.KindOf(err) == types.KindUnknown || failure.Cause == nil {
		t.Errorf("the failure has kind %v and cause %v", types.KindOf(err), failure.Cause)
	}
	if len(result.Stages) != 1 || result.Stages[0].Err != err {
		t.Errorf("the outcome of the failed stage does not hold the error the call returned: %+v", result.Stages)
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

	extractID := result.Stages[0].Provenance.ResultID
	verifyParents := result.Stages[1].Provenance.ParentResultIDs

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
		ID:     types.OperationID{Name: "default-id"},
		Stages: []Stage[testDatum]{{Op: op}}, // no Stage.ID set
	}

	result, err := RunStagedPlan(ctx, plan, testDatum{Source: "x"}, types.OpOptions{Model: "test-model"})
	if err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}
	if result.Stages[0].StageID != op.ID {
		t.Fatalf("StageID = %v, want it to default to Op.ID %v", result.Stages[0].StageID, op.ID)
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
		ID:     types.OperationID{Name: "explicit-id"},
		Stages: []Stage[testDatum]{{ID: stageID, Op: op}},
	}

	result, err := RunStagedPlan(ctx, plan, testDatum{Source: "x"}, types.OpOptions{Model: "test-model"})
	if err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}
	if result.Stages[0].StageID != stageID {
		t.Fatalf("StageID = %v, want the explicit %v", result.Stages[0].StageID, stageID)
	}
	if result.Stages[0].StageID == op.ID {
		t.Fatal("explicit Stage.ID collapsed into Op.ID")
	}
}

func TestRunStagedPlan_ConfigurationErrorsAreClassified(t *testing.T) {
	_, err := RunStagedPlan(context.Background(), StagedPlan[testDatum]{ID: types.OperationID{Name: "empty"}}, testDatum{}, types.OpOptions{})
	if types.KindOf(err) != types.KindInvalidRequest {
		t.Errorf("no stages: kind = %v, want an invalid request, as a nil context is", types.KindOf(err))
	}
	_, err = RunStagedPlan(context.Background(), StagedPlan[testDatum]{Stages: []Stage[testDatum]{{}}}, testDatum{}, types.OpOptions{})
	if types.KindOf(err) != types.KindConfiguration {
		t.Errorf("a stage with no operation: kind = %v", types.KindOf(err))
	}
}

// stagePromptOp is a stage whose request carries the given prompt, so that a
// provider can answer by which stage is asking.
func stagePromptOp(name, prompt string) Op[testDatum, testDatum] {
	return Op[testDatum, testDatum]{
		ID:          types.OperationID{Name: name},
		Contract:    OutputContract[testDatum]{SchemaName: "testDatum", Decode: decodeTestDatum},
		BuildPrompt: func(testDatum, types.OpOptions) (string, string) { return "sys", prompt },
	}
}

// A plan has no scheduler of its own. It held one beside the client's, and a stage
// ran inside the plan's slot while the call it made asked the client's scheduler
// for another: given the same scheduler for both, a limit of one held the only slot
// while the call waited for it, and the plan never returned (seen failing on that
// code, with the plan's own field set, as a plan that did not return in three
// seconds). The client's scheduler alone bounds the requests.
func TestRunStagedPlan_OneSchedulerOfOneBoundsEveryStageAndNothingWaitsOnItself(t *testing.T) {
	for _, stages := range []int{1, 2, 3, 5} {
		t.Run(fmt.Sprintf("%d stages", stages), func(t *testing.T) {
			scheduler := NewScheduler(SchedulerLimits{MaxConcurrent: 1})
			t.Cleanup(func() { _ = scheduler.Close(context.Background()) })
			provider := &stagedFakeProvider{respond: func(int, llm.CompletionRequest) string { return `{"name":"ok"}` }}
			ctx := WithExecConfig(context.Background(), &ExecConfig{Provider: provider, Scheduler: scheduler})

			plan := StagedPlan[testDatum]{ID: types.OperationID{Name: "chain"}}
			for i := 0; i < stages; i++ {
				plan.Stages = append(plan.Stages, Stage[testDatum]{Op: stagePromptOp(fmt.Sprintf("s%d", i), "p")})
			}
			done := make(chan error, 1)
			go func() {
				_, err := RunStagedPlan(ctx, plan, testDatum{Source: "x"}, types.OpOptions{Model: "test-model"})
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("RunStagedPlan: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the plan did not return: it waits for a slot of the scheduler that it holds")
			}
			if provider.callCount() != stages {
				t.Errorf("%d requests for %d stages", provider.callCount(), stages)
			}
		})
	}
}

// The plan's own declarations carry no scheduler and no groups.
func TestStagedPlanDeclaresNoSchedulerAndNoGroups(t *testing.T) {
	schedulerType := reflect.TypeOf((*Scheduler)(nil))
	for _, typ := range []reflect.Type{reflect.TypeOf(StagedPlan[testDatum]{}), reflect.TypeOf(Stage[testDatum]{}), reflect.TypeOf(StagedPlanResult[testDatum]{}), reflect.TypeOf(StageOutcome[testDatum]{})} {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Type == schedulerType || field.Name == "Scheduler" || field.Name == "Steps" {
				t.Errorf("%s declares %s", typ.Name(), field.Name)
			}
		}
	}
	if field, _ := reflect.TypeOf(StagedPlan[testDatum]{}).FieldByName("Stages"); field.Type != reflect.TypeOf([]Stage[testDatum]{}) {
		t.Errorf("StagedPlan.Stages is a %v, want a flat list of stages", field.Type)
	}
}

func TestRunStagedPlan_ANilContextIsRefused(t *testing.T) {
	var noContext context.Context
	for _, plan := range []StagedPlan[testDatum]{extractThenVerifyPlan(nil), {}, {Stages: []Stage[testDatum]{{}}}} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("RunStagedPlan panicked on a nil context: %v", r)
				}
			}()
			result, err := RunStagedPlan(noContext, plan, testDatum{Source: "x"}, types.OpOptions{Model: "test-model"})
			if types.KindOf(err) != types.KindInvalidRequest || result.Stages != nil {
				t.Errorf("err = %v, stages = %v; want an invalid request and nothing beside it", err, result.Stages)
			}
		}()
	}
}

// A call that cannot be run is refused before any stage is asked, whichever stage
// is the unusable one, with the empty result.
func TestRunStagedPlan_AnUnusableStageIsRefusedBeforeAnyRequest(t *testing.T) {
	broken := []struct {
		name string
		op   func() Op[testDatum, testDatum]
	}{
		{"no BuildPrompt", func() Op[testDatum, testDatum] { op := stagePromptOp("b", "p"); op.BuildPrompt = nil; return op }},
		{"no Decode", func() Op[testDatum, testDatum] { op := stagePromptOp("b", "p"); op.Contract.Decode = nil; return op }},
	}
	for _, b := range broken {
		for position := 0; position < 3; position++ {
			t.Run(fmt.Sprintf("%s at stage %d", b.name, position+1), func(t *testing.T) {
				provider := &stagedFakeProvider{respond: func(int, llm.CompletionRequest) string { return `{"name":"ok"}` }}
				plan := StagedPlan[testDatum]{ID: types.OperationID{Name: "chain"}}
				for i := 0; i < 3; i++ {
					op := stagePromptOp(fmt.Sprintf("s%d", i), "p")
					if i == position {
						op = b.op()
					}
					plan.Stages = append(plan.Stages, Stage[testDatum]{Op: op})
				}
				result, err := RunStagedPlan(WithProvider(context.Background(), provider), plan, testDatum{}, types.OpOptions{Model: "test-model"})
				if types.KindOf(err) != types.KindConfiguration || result.Stages != nil {
					t.Errorf("err = %v, stages = %v", err, result.Stages)
				}
				if provider.callCount() != 0 {
					t.Errorf("%d requests were sent before the plan was refused", provider.callCount())
				}
			})
		}
	}
}

// A stage that fails ends the plan with a classified error that names the stage by
// its place and its operation's name and nothing the caller supplied as data, and
// the result beside it holds what the stages that ran spent and no value.
func TestRunStagedPlan_AFailedStageReportsWhatWasSpentAndIsNamedWithoutData(t *testing.T) {
	const secret = "a secret customer note"
	for stages := 1; stages <= 4; stages++ {
		for failing := 0; failing < stages; failing++ {
			t.Run(fmt.Sprintf("stage %d of %d fails", failing+1, stages), func(t *testing.T) {
				provider := &stagedFakeProvider{respond: func(_ int, req llm.CompletionRequest) string {
					if strings.Contains(req.UserPrompt, "bad") {
						return `{}`
					}
					return `{"name":"ok"}`
				}}
				plan := StagedPlan[testDatum]{ID: types.OperationID{Name: "chain"}}
				for i := 0; i < stages; i++ {
					prompt := "good"
					if i == failing {
						prompt = "bad"
					}
					plan.Stages = append(plan.Stages, Stage[testDatum]{Op: stagePromptOp(fmt.Sprintf("stage-op-%d", i), prompt)})
				}
				result, err := RunStagedPlan(WithProvider(context.Background(), provider), plan, testDatum{Source: secret}, types.OpOptions{Model: "test-model"})

				classified, ok := err.(*types.OperationError)
				if !ok {
					t.Fatalf("the error is a %T and not an *OperationError: %v", err, err)
				}
				if types.KindOf(err) == types.KindUnknown {
					t.Errorf("the error has no kind: %v", err)
				}
				wantMessage := fmt.Sprintf("stage %d of %d failed", failing+1, stages)
				if classified.Message != wantMessage || classified.Op != fmt.Sprintf("stage-op-%d", failing) {
					t.Errorf("op %q, message %q; want the stage's operation and %q", classified.Op, classified.Message, wantMessage)
				}
				if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "empty name") {
					t.Errorf("the error carries data or the stage's own text: %v", err)
				}
				if classified.Cause == nil {
					t.Error("the stage's failure is not the cause")
				}

				if len(result.Stages) != failing+1 {
					t.Fatalf("%d outcomes, want the %d stages that ran", len(result.Stages), failing+1)
				}
				for i, outcome := range result.Stages {
					if i < failing {
						if outcome.Err != nil || outcome.Meta.Attempts != 1 || outcome.Value.Name != "ok" {
							t.Errorf("stage %d: %+v, want a completed stage with what it spent", i, outcome)
						}
						continue
					}
					if outcome.Err == nil || outcome.Meta.Attempts < 1 || outcome.Value != (testDatum{}) {
						t.Errorf("the failed stage: err %v, attempts %d, value %+v", outcome.Err, outcome.Meta.Attempts, outcome.Value)
					}
				}
				if result.Value != (testDatum{}) {
					t.Errorf("a value came back beside the failure: %+v", result.Value)
				}
			})
		}
	}
}

// A panic in a stage's own functions does not end the process and does not throw
// away what the earlier stages, and the failed one, cost: it is that stage's failure,
// classified as an internal one, with no panic value in it, and the result holds
// every stage that ran with what it spent.
func TestRunStagedPlan_APanicInAStageIsThatStagesFailureAndKeepsWhatWasSpent(t *testing.T) {
	const secret = "the panic value of a customer note"
	// Stage 1 is paid. Stage 2 breaks in one of its caller's functions.
	breakers := []struct {
		name   string
		break_ func(*Stage[testDatum])
		paid   int // requests stage 2 made before the break
	}{
		{"BuildPrompt", func(s *Stage[testDatum]) {
			s.Op.BuildPrompt = func(testDatum, types.OpOptions) (string, string) { panic(secret) }
		}, 0},
		{"Decode", func(s *Stage[testDatum]) {
			s.Op.Contract.Decode = func(string) (testDatum, error) { panic(secret) }
		}, 1},
		{"an invariant", func(s *Stage[testDatum]) {
			s.Op.Contract.Invariants = []func(testDatum) error{func(testDatum) error { panic(secret) }}
		}, 1},
		{"Normalize", func(s *Stage[testDatum]) {
			s.Op.Contract.Normalize = func(testDatum) testDatum { panic(secret) }
		}, 1},
		{"the check", func(s *Stage[testDatum]) {
			s.DeterministicCheck = func(testDatum) (testDatum, types.ContractLevel, bool) { panic(secret) }
		}, 0},
	}
	for _, tc := range breakers {
		t.Run(tc.name, func(t *testing.T) {
			provider := &stagedFakeProvider{respond: respondExtractThenVerify}
			plan := extractThenVerifyPlan(nil)
			tc.break_(&plan.Stages[1])

			var result StagedPlanResult[testDatum]
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("a panic escaped RunStagedPlan: %v", r)
					}
				}()
				result, err = RunStagedPlan(WithProvider(context.Background(), provider), plan, testDatum{Source: "x"}, types.OpOptions{Model: "test-model"})
			}()

			var failure *types.OperationError
			if !errors.As(err, &failure) || types.KindOf(err) != types.KindInvariantViolation || types.NarrowingOf(err) != types.NarrowPanic {
				t.Fatalf("err = %v (%v, %v), want an internal failure marked as a panic", err, types.KindOf(err), types.NarrowingOf(err))
			}
			if failure.Message != "stage 2 of 2 failed" || strings.Contains(err.Error(), secret) || strings.Contains(result.Stages[1].Err.Error(), secret) {
				t.Errorf("the error is %q, and carries the panic's value or not the stage's place", err)
			}
			if len(result.Stages) != 2 || result.Stages[0].Err != nil || result.Stages[0].Meta.Attempts != 1 || result.Stages[0].Value.Name != "Alice" {
				t.Fatalf("stages = %+v, want the paid stage kept and the failed one after it", result.Stages)
			}
			if result.Stages[1].Err == nil || result.Stages[1].Meta.Attempts != tc.paid {
				t.Errorf("the failed stage: err %v, attempts %d, want the %d request it paid for", result.Stages[1].Err, result.Stages[1].Meta.Attempts, tc.paid)
			}
			if result.Value != (testDatum{}) {
				t.Errorf("a value came back beside the failure: %+v", result.Value)
			}
			if want := 1 + tc.paid; provider.callCount() != want {
				t.Errorf("%d requests were sent, want %d: a panic is not repaired by asking again", provider.callCount(), want)
			}
		})
	}
}

// A function that ends its goroutine is caught as well, because a stage's functions
// run on a goroutine of the library's.
func TestRunStagedPlan_AGoexitInAStageIsThatStagesFailure(t *testing.T) {
	for name, set := range map[string]func(*Stage[testDatum]){
		"BuildPrompt": func(s *Stage[testDatum]) {
			s.Op.BuildPrompt = func(testDatum, types.OpOptions) (string, string) { runtime.Goexit(); return "", "" }
		},
		"the check": func(s *Stage[testDatum]) {
			s.DeterministicCheck = func(testDatum) (testDatum, types.ContractLevel, bool) {
				runtime.Goexit()
				return testDatum{}, 0, false
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			plan := extractThenVerifyPlan(nil)
			set(&plan.Stages[1])
			done := make(chan struct{})
			var result StagedPlanResult[testDatum]
			var err error
			go func() {
				defer close(done)
				result, err = RunStagedPlan(WithProvider(context.Background(), &stagedFakeProvider{respond: respondExtractThenVerify}), plan, testDatum{Source: "x"}, types.OpOptions{Model: "test-model"})
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the caller's goroutine ended inside the plan")
			}
			if types.NarrowingOf(err) != types.NarrowPanic || len(result.Stages) != 2 || result.Stages[0].Meta.Attempts != 1 {
				t.Errorf("err = %v, stages = %+v", err, result.Stages)
			}
		})
	}
}

// A stage a check settles still passes the client's contract floor: the level it
// claims is held to it, and below it the stage is asked.
func TestRunStagedPlan_ASettledStageIsHeldToTheClientsContractFloor(t *testing.T) {
	cases := []struct {
		name      string
		level     types.ContractLevel
		floor     types.ContractLevel
		wantAsked bool
	}{
		{"a level that meets the floor", types.ContractSchemaAndInvariantChecked, types.ContractSchemaAndInvariantChecked, false},
		{"a level above the floor", types.ContractEvidenceChecked, types.ContractSchemaConstrained, false},
		{"a level below the floor", types.ContractJSONWellFormed, types.ContractSchemaAndInvariantChecked, true},
		{"no floor", types.ContractJSONWellFormed, types.ContractPromptOnly, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &stagedFakeProvider{respond: respondExtractThenVerify}
			check := func(d testDatum) (testDatum, types.ContractLevel, bool) { d.Verified = true; return d, tc.level, true }
			plan := extractThenVerifyPlan(check)
			plan.Stages[1].RequiredContract = types.ContractJSONWellFormed
			ctx := WithContractPolicy(WithProvider(context.Background(), provider), types.DataPolicy{MinimumContract: tc.floor})
			// The levels above well formed are delivered on a route that enforces the
			// schema the request sends.
			opt := routeThatEnforcesSchemas(t, provider, "fake-model", types.OpOptions{})
			result, err := RunStagedPlan(ctx, plan, testDatum{Source: "x"}, opt)
			if err != nil {
				t.Fatalf("RunStagedPlan: %v", err)
			}
			wantCalls := 1
			if tc.wantAsked {
				wantCalls = 2
			}
			if provider.callCount() != wantCalls || result.Stages[1].Skipped == tc.wantAsked {
				t.Fatalf("%d requests, skipped %v: want %d requests, because a check that claims %s is %s a floor of %s",
					provider.callCount(), result.Stages[1].Skipped, wantCalls, tc.level, map[bool]string{true: "below", false: "within"}[tc.wantAsked], tc.floor)
			}
		})
	}
}

// A level a check attests to that is not a contract level is a configuration error,
// and nothing is skipped on its word.
func TestRunStagedPlan_ACheckThatClaimsALevelThatIsNotOneIsRefused(t *testing.T) {
	check := func(d testDatum) (testDatum, types.ContractLevel, bool) { return d, types.ContractLevel(200), true }
	plan := extractThenVerifyPlan(check)
	provider := &stagedFakeProvider{respond: respondExtractThenVerify}
	result, err := RunStagedPlan(WithProvider(context.Background(), provider), plan, testDatum{Source: "x"}, types.OpOptions{Model: "test-model"})
	if types.KindOf(err) != types.KindConfiguration {
		t.Fatalf("err = %v (%v), want a configuration error", err, types.KindOf(err))
	}
	if len(result.Stages) != 2 || result.Stages[1].Skipped {
		t.Errorf("stages = %+v, want the stage that claimed it failed, not skipped", result.Stages)
	}
}

// A plan whose stage cannot reach the client's floor is refused before anything is
// paid for, when the stage will certainly ask the model.
func TestRunStagedPlan_AFloorNoStageCanReachIsRefusedBeforeTheFirstRequest(t *testing.T) {
	provider := &stagedFakeProvider{respond: respondExtractThenVerify}
	ctx := WithContractPolicy(WithProvider(context.Background(), provider), types.DataPolicy{MinimumContract: types.ContractFullyGoverned})
	opt := routeThatEnforcesSchemas(t, provider, "fake-model", types.OpOptions{})
	result, err := RunStagedPlan(ctx, extractThenVerifyPlan(nil), testDatum{Source: "x"}, opt)
	if types.KindOf(err) != types.KindPolicyViolation || result.Stages != nil {
		t.Fatalf("err = %v (%v), stages %v", err, types.KindOf(err), result.Stages)
	}
	if provider.callCount() != 0 {
		t.Errorf("%d requests were paid for before the floor was refused", provider.callCount())
	}
}

// The caller's own ParentResultIDs are the first stage's parents, and every later
// stage names them and then the stage before it.
func TestRunStagedPlan_KeepsTheCallersParentResultIDs(t *testing.T) {
	provider := &stagedFakeProvider{respond: respondExtractThenVerify}
	opt := types.OpOptions{Model: "test-model", ParentResultIDs: []string{"caller-parent-1", "caller-parent-2"}}
	result, err := RunStagedPlan(WithProvider(context.Background(), provider), extractThenVerifyPlan(nil), testDatum{Source: "x"}, opt)
	if err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}
	first := result.Stages[0].Provenance
	if fmt.Sprint(first.ParentResultIDs) != "[caller-parent-1 caller-parent-2]" {
		t.Errorf("the first stage's parents are %v, want the caller's", first.ParentResultIDs)
	}
	if want := fmt.Sprint([]string{"caller-parent-1", "caller-parent-2", first.ResultID}); fmt.Sprint(result.Stages[1].Provenance.ParentResultIDs) != want {
		t.Errorf("the second stage's parents are %v, want %s", result.Stages[1].Provenance.ParentResultIDs, want)
	}
	if fmt.Sprint(opt.ParentResultIDs) != "[caller-parent-1 caller-parent-2]" {
		t.Errorf("the caller's options were changed: %v", opt.ParentResultIDs)
	}
}

// A check may return an adjusted value, and that value, and not its input, is what
// the next stage reads.
func TestRunStagedPlan_ACheckThatAdjustsTheValueFeedsTheNextStage(t *testing.T) {
	var seen []string
	adjust := func(d testDatum) (testDatum, types.ContractLevel, bool) {
		d.Name = strings.ToUpper(d.Name)
		return d, types.ContractSchemaAndInvariantChecked, true
	}
	provider := &stagedFakeProvider{respond: func(call int, req llm.CompletionRequest) string {
		seen = append(seen, req.UserPrompt)
		return respondExtractThenVerify(call, req)
	}}
	plan := StagedPlan[testDatum]{ID: types.OperationID{Name: "adjust"}, Stages: []Stage[testDatum]{
		{ID: types.OperationID{Name: "extract"}, Op: newExtractStageOp(), DeterministicCheck: func(d testDatum) (testDatum, types.ContractLevel, bool) { return d, 0, false }},
		{ID: types.OperationID{Name: "adjust"}, Op: newVerifyStageOp(), DeterministicCheck: adjust, RequiredContract: types.ContractJSONWellFormed},
		{ID: types.OperationID{Name: "last"}, Op: newVerifyStageOp()},
	}}
	if _, err := RunStagedPlan(WithProvider(context.Background(), provider), plan, testDatum{Source: "x"}, types.OpOptions{Model: "test-model"}); err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}
	if len(seen) != 2 || seen[1] != "ALICE" {
		t.Errorf("the requests carried %q, want the last stage to read the adjusted name", seen)
	}
}

// A call that cannot be run because its context is over runs nothing.
func TestRunStagedPlan_AContextThatIsAlreadyOverRunsNothing(t *testing.T) {
	provider := &stagedFakeProvider{respond: respondExtractThenVerify}
	ctx, cancel := context.WithCancel(WithProvider(context.Background(), provider))
	cancel()
	checked := 0
	plan := extractThenVerifyPlan(func(d testDatum) (testDatum, types.ContractLevel, bool) { checked++; return d, 0, false })
	result, err := RunStagedPlan(ctx, plan, testDatum{Source: "x"}, types.OpOptions{Model: "test-model"})
	if types.KindOf(err) != types.KindCanceled || !errors.Is(err, context.Canceled) || result.Stages != nil {
		t.Fatalf("err = %v (%v), stages %v", err, types.KindOf(err), result.Stages)
	}
	if provider.callCount() != 0 || checked != 0 {
		t.Errorf("%d requests and %d checks ran", provider.callCount(), checked)
	}
}

// Nothing a vendor, the caller's Decode or an invariant says is in a stage's error
// or in its outcome: only the library's own words, with the caller's error kept as
// the cause for errors.Is.
func TestRunStagedPlan_ErrorTextsCarryNoVendorOrCallerText(t *testing.T) {
	const vendor, callerText = "CANARY-VENDOR-quota-for-CANARY-ITEM", "CANARY-CALLER-cannot-read-CANARY-ITEM"
	callerErr := errors.New(callerText)

	failures := map[string]func() (Stage[testDatum], llm.Provider){
		"the provider fails": func() (Stage[testDatum], llm.Provider) {
			return Stage[testDatum]{Op: newExtractStageOp()}, alwaysFailingProvider{err: errors.New(vendor)}
		},
		"the caller's Decode fails": func() (Stage[testDatum], llm.Provider) {
			op := newExtractStageOp()
			op.Contract.Decode = func(string) (testDatum, error) { return testDatum{}, callerErr }
			return Stage[testDatum]{Op: op}, &stagedFakeProvider{respond: respondExtractThenVerify}
		},
		"the caller's invariant fails": func() (Stage[testDatum], llm.Provider) {
			op := newExtractStageOp()
			op.Contract.Invariants = []func(testDatum) error{func(testDatum) error { return callerErr }}
			return Stage[testDatum]{Op: op}, &stagedFakeProvider{respond: respondExtractThenVerify}
		},
	}
	for name, build := range failures {
		t.Run(name, func(t *testing.T) {
			stage, provider := build()
			plan := StagedPlan[testDatum]{ID: types.OperationID{Name: "canary"}, Stages: []Stage[testDatum]{stage}}
			result, err := RunStagedPlan(WithProvider(context.Background(), provider), plan, testDatum{Source: "CANARY-ITEM"}, types.OpOptions{Model: "test-model"})
			if err == nil {
				t.Fatal("the stage did not fail")
			}
			for _, text := range []string{err.Error(), result.Stages[0].Err.Error()} {
				for _, canary := range []string{"CANARY", "Your previous answer", "Return a corrected answer"} {
					if strings.Contains(text, canary) {
						t.Errorf("the text %q carries %q", text, canary)
					}
				}
			}
			if name != "the provider fails" && !errors.Is(err, callerErr) {
				t.Errorf("the caller's own error is not in the chain of %v", err)
			}
		})
	}
}

// alwaysFailingProvider is a provider whose every request fails with err.
type alwaysFailingProvider struct{ err error }

func (p alwaysFailingProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, p.err
}
func (p alwaysFailingProvider) Name() string                               { return "failing" }
func (p alwaysFailingProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p alwaysFailingProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }
