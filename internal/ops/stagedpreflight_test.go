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

	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// --- Fixtures: a datum, stages over it, and providers that count or script.

// draft is the one datum the staged plans below take through their stages. A
// stage renders its text as the user prompt.
type draft struct {
	Text string
}

// draftStageOp is a stage's operation: it asks about the draft's text under the
// system prompt it is given, and its answer is a draft.
func draftStageOp(name, system string) Op[draft, draft] {
	return Op[draft, draft]{
		ID: types.OperationID{Name: name, Version: "v1"},
		Contract: OutputContract[draft]{
			SchemaName: "draft",
			Decode: func(body string) (draft, error) {
				var raw struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal([]byte(body), &raw); err != nil {
					return draft{}, err
				}
				return draft{Text: raw.Text}, nil
			},
		},
		BuildPrompt: func(d draft, _ types.OpOptions) (string, string) { return system, d.Text },
	}
}

// draftStages is a staged plan with one stage for each op.
func draftStages(id string, ops ...Op[draft, draft]) StagedPlan[draft] {
	plan := StagedPlan[draft]{ID: types.OperationID{Name: id, Version: "v1"}}
	for _, op := range ops {
		plan.Stages = append(plan.Stages, Stage[draft]{Op: op})
	}
	return plan
}

// forbiddenProvider fails the test it was made for the first time it is asked
// anything but its name: planning has no business reaching a provider.
type forbiddenProvider struct {
	t     *testing.T
	calls atomic.Int32
}

func (p *forbiddenProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls.Add(1)
	p.t.Error("a provider was asked to complete a request while a plan was being made")
	return llm.CompletionResponse{}, errors.New("planning must not call a provider")
}
func (p *forbiddenProvider) Name() string                               { return "openai" }
func (p *forbiddenProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *forbiddenProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }

// recordingProvider answers every request with the same draft and keeps the
// requests it was sent, so a test can read what a run actually asked.
type recordingProvider struct {
	mu       sync.Mutex
	answer   string
	usage    types.TokenUsage
	requests []llm.CompletionRequest
}

func (p *recordingProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	return llm.CompletionResponse{Content: p.answer, Provider: "openai", Model: req.Model, FinishReason: "stop", Usage: p.usage}, nil
}
func (p *recordingProvider) Name() string                               { return "openai" }
func (p *recordingProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *recordingProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }

func (p *recordingProvider) sent() []llm.CompletionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.CompletionRequest(nil), p.requests...)
}

// --- Tests.

// Planning a staged job asks no provider anything, whatever the stages are and
// however many of them have checks. The provider on the context fails the test
// if it is asked.
func TestPreflightStagedPlanMakesNoProviderCalls(t *testing.T) {
	checks := func(d draft) (draft, types.ContractLevel, bool) { return d, types.ContractFullyGoverned, true }
	cases := []struct {
		name string
		plan StagedPlan[draft]
	}{
		{"one stage", draftStages("one", draftStageOp("a", "system"))},
		{"two stages", draftStages("two", draftStageOp("a", "system"), draftStageOp("b", "system"))},
		{"five stages", draftStages("five", draftStageOp("a", "s"), draftStageOp("b", "s"), draftStageOp("c", "s"), draftStageOp("d", "s"), draftStageOp("e", "s"))},
		{"a stage whose check would settle it", StagedPlan[draft]{ID: types.OperationID{Name: "checked"}, Stages: []Stage[draft]{
			{Op: draftStageOp("a", "s")},
			{Op: draftStageOp("b", "s"), DeterministicCheck: checks, RequiredContract: types.ContractPromptOnly},
		}}},
		{"a long system prompt", draftStages("long", draftStageOp("a", strings.Repeat("s", 8000)), draftStageOp("b", strings.Repeat("s", 8000)))},
		{"an empty datum text", draftStages("empty", draftStageOp("a", "s"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &forbiddenProvider{t: t}
			ctx := WithProvider(context.Background(), provider)
			got, err := PreflightStagedPlan(ctx, tc.plan, draft{Text: "the datum"}, types.PlanRequest{Options: types.OpOptions{Model: "gpt-4"}})
			if err != nil || !got.Plan.Eligible {
				t.Fatalf("PreflightStagedPlan: %v (%s)", err, got.Plan.IneligibleReason)
			}
			if n := provider.calls.Load(); n != 0 {
				t.Errorf("planning made %d provider calls", n)
			}
		})
	}
}

// Each stage is priced by the estimator Preflight uses: for the first stage, the
// estimate is what PreflightFor says for the same operation over the datum as one
// item, and so is every later stage's, since it is priced from the datum too.
func TestPreflightStagedPlanPricesEachStageThroughTheOneEstimator(t *testing.T) {
	withoutReinforcement(t)
	cases := []struct {
		name    string
		datum   string
		systems []int // the system prompt size in bytes of each stage
	}{
		{"one stage, a short datum", "short", []int{10}},
		{"one stage, no system prompt", "short", []int{0}},
		{"one stage, an empty datum", "", []int{40}},
		{"one stage, a long datum", strings.Repeat("d", 4000), []int{40}},
		{"two stages", "the datum", []int{40, 400}},
		{"two stages, a long system prompt in the second", "the datum", []int{4, 8000}},
		{"three stages of one prompt", "the datum", []int{100, 100, 100}},
		{"four stages of growing system prompts", "the datum of some length", []int{4, 40, 400, 4000}},
		{"a datum that rounds up", "123456789", []int{3}},
		{"a thousand byte system prompt over a thousand byte datum", strings.Repeat("d", 1000), []int{1000, 1000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stageOps []Op[draft, draft]
			for i, size := range tc.systems {
				stageOps = append(stageOps, draftStageOp(fmt.Sprintf("stage%d", i+1), strings.Repeat("s", size)))
			}
			req := types.PlanRequest{Options: types.OpOptions{Model: "gpt-4"}}
			datum := draft{Text: tc.datum}
			got, err := PreflightStagedPlan(context.Background(), draftStages("job", stageOps...), datum, req)
			if err != nil || !got.Plan.Eligible {
				t.Fatalf("PreflightStagedPlan: %v (%s)", err, got.Plan.IneligibleReason)
			}
			if len(got.Stages) != len(stageOps) {
				t.Fatalf("%d stages planned, want %d", len(got.Stages), len(stageOps))
			}
			promptTotal := 0
			for i, stage := range got.Stages {
				alone, err := PreflightFor(context.Background(), stageOps[i], []draft{datum}, req)
				if err != nil || !alone.Eligible {
					t.Fatalf("PreflightFor: %v (%s)", err, alone.IneligibleReason)
				}
				if stage.Estimate != alone.EstimatedCost {
					t.Errorf("stage %d: estimate %+v, but PreflightFor over the datum says %+v", i+1, stage.Estimate, alone.EstimatedCost)
				}
				if want := tokensOfBytes(tc.systems[i]) + tokensOfBytes(len(tc.datum)); stage.Estimate.EstimatedPromptTokens != want {
					t.Errorf("stage %d: %d prompt tokens, want the %d its prompts are", i+1, stage.Estimate.EstimatedPromptTokens, want)
				}
				if stage.Requests != 1 || stage.Requests != alone.MaxCalls {
					t.Errorf("stage %d: %d requests, PreflightFor says %d", i+1, stage.Requests, alone.MaxCalls)
				}
				promptTotal += stage.Estimate.EstimatedPromptTokens
			}
			if got.Plan.EstimatedCost.EstimatedPromptTokens != promptTotal {
				t.Errorf("the plan's prompt tokens = %d, want the stages' %d", got.Plan.EstimatedCost.EstimatedPromptTokens, promptTotal)
			}
		})
	}
}

// The first stage is asked about the datum itself, and every stage after it about
// an answer that does not exist yet, so its estimate is an upper bound from the
// datum, and the result says which are.
func TestPreflightStagedPlanSaysWhichStagesAreUpperBounds(t *testing.T) {
	for stages := 1; stages <= 10; stages++ {
		t.Run(fmt.Sprintf("%d stages", stages), func(t *testing.T) {
			var stageOps []Op[draft, draft]
			for i := 0; i < stages; i++ {
				stageOps = append(stageOps, draftStageOp(fmt.Sprintf("stage%d", i+1), "s"))
			}
			got, err := PreflightStagedPlan(context.Background(), draftStages("job", stageOps...), draft{Text: "the datum"}, types.PlanRequest{})
			if err != nil || !got.Plan.Eligible {
				t.Fatalf("PreflightStagedPlan: %v (%s)", err, got.Plan.IneligibleReason)
			}
			if len(got.Stages) != stages {
				t.Fatalf("%d stages, want %d", len(got.Stages), stages)
			}
			for i, stage := range got.Stages {
				if want := fmt.Sprintf("stage%d", i+1); stage.ID.Name != want {
					t.Errorf("stage %d is %q, want %q (declared order)", i, stage.ID.Name, want)
				}
				if want := i > 0; stage.UpperBound != want {
					t.Errorf("stage %d (%s): UpperBound = %v, want %v", i, stage.ID.Name, stage.UpperBound, want)
				}
			}
		})
	}
}

// What the plan of a staged job holds: its shape, its call ceiling, its estimate
// as the sum of the stages', and the snapshots it was made under.
func TestPreflightStagedPlanFillsInThePlan(t *testing.T) {
	withoutReinforcement(t)
	ctx := context.Background()
	req := types.PlanRequest{Options: types.OpOptions{Model: "gpt-4", Steering: "caller words that must not reach the plan"}}
	stagedOps := []Op[draft, draft]{draftStageOp("extract", strings.Repeat("e", 400)), draftStageOp("verify", strings.Repeat("v", 800)), draftStageOp("finish", "f")}
	plan := draftStages("op.runstages", stagedOps...)
	plan.ID = types.OperationID{Name: "op.runstages"}

	got, err := PreflightStagedPlan(ctx, plan, draft{Text: "the datum"}, req)
	if err != nil {
		t.Fatalf("PreflightStagedPlan: %v", err)
	}
	p := got.Plan
	if !p.Eligible || p.IneligibleReason != "" || p.IneligibleKind != types.KindUnknown {
		t.Errorf("eligible %v, reason %q, kind %s", p.Eligible, p.IneligibleReason, p.IneligibleKind)
	}
	if p.Shape != types.ShapeSDMP || p.ShapeForced || p.ShapeReason == "" {
		t.Errorf("shape %s, forced %v, reason %q: want the staged shape, chosen, with a reason", p.Shape, p.ShapeForced, p.ShapeReason)
	}
	if p.Operation.Name != "op.runstages" || p.ItemCount != 1 || p.InputShape == "" {
		t.Errorf("operation %v, items %d, input %q", p.Operation, p.ItemCount, p.InputShape)
	}
	if p.MaxCalls != 3 || len(p.Chunks) != 0 || len(p.OversizedItems) != 0 || len(p.Alternatives) != 0 {
		t.Errorf("max calls %d, chunks %d, oversized %d, alternatives %d", p.MaxCalls, len(p.Chunks), len(p.OversizedItems), len(p.Alternatives))
	}
	if p.EstimatorVersion != estimatorVersion {
		t.Errorf("estimator version %q, want %q", p.EstimatorVersion, estimatorVersion)
	}
	wantCapability := capabilitySnapshot(ctx, req.Options)
	if p.Capability != wantCapability || p.CapabilityDigest != wantCapability.Digest() {
		t.Errorf("capability %+v, want %+v", p.Capability, wantCapability)
	}
	if p.Policy != types.PolicySnapshotFrom(req.Options) || p.PolicyDigest != p.Policy.Digest() || !p.Policy.HasSteering {
		t.Errorf("policy %+v", p.Policy)
	}

	var prompt, completion int
	var cost float64
	for _, stage := range got.Stages {
		prompt += stage.Estimate.EstimatedPromptTokens
		completion += stage.Estimate.EstimatedCompletionTokens
		cost += stage.Estimate.EstimatedCost
	}
	e := p.EstimatedCost
	if e.EstimatedPromptTokens != prompt || e.EstimatedCompletionTokens != completion || !e.Priced || e.EstimatedCost != cost || e.Currency != "USD" {
		t.Errorf("estimate %+v, want %d prompt, %d completion tokens and %v from the stages", e, prompt, completion, cost)
	}
	if want := 3 * p.Capability.MaxOutputTokens; completion != want {
		t.Errorf("completion tokens %d, want the model's ceiling for each of the three stages (%d)", completion, want)
	}
	// 100+3, 200+3 and 1+3 tokens, each stage priced from the datum, and the
	// caller's steering that goes in front of every stage's user prompt.
	steering := estimateTokens(applySteering("", req.Options.Steering))
	if want := 103 + 203 + 4 + 3*steering; prompt != want {
		t.Errorf("prompt tokens %d, want %d", prompt, want)
	}

	encoded, _ := p.Serialize()
	if strings.Contains(string(encoded), "caller words") || strings.Contains(string(encoded), "the datum") {
		t.Errorf("the serialized plan carries the caller's text: %s", encoded)
	}
	again, _ := PreflightStagedPlan(ctx, plan, draft{Text: "the datum"}, req)
	if second, _ := again.Plan.Serialize(); string(second) != string(encoded) {
		t.Errorf("two plans of the same job differ:\n%s\n%s", encoded, second)
	}
}

// A stage is named as a run names it: by its own ID, or by its operation's.
func TestPreflightStagedPlanNamesStagesAsARunDoes(t *testing.T) {
	plan := StagedPlan[draft]{ID: types.OperationID{Name: "job"}, Stages: []Stage[draft]{
		{Op: draftStageOp("op-name", "s")},
		{ID: types.OperationID{Name: "own-name"}, Op: draftStageOp("op-name", "s")},
	}}
	got, err := PreflightStagedPlan(context.Background(), plan, draft{Text: "d"}, types.PlanRequest{})
	if err != nil || len(got.Stages) != 2 {
		t.Fatalf("PreflightStagedPlan: %v, %d stages", err, len(got.Stages))
	}
	if got.Stages[0].ID.Name != "op-name" || got.Stages[1].ID.Name != "own-name" {
		t.Errorf("stages named %q and %q, want op-name and own-name", got.Stages[0].ID.Name, got.Stages[1].ID.Name)
	}
}

// A stage whose prompt cannot be rendered makes the plan ineligible, with the
// reason and its kind, and names the stage by its place and its name and nothing
// the caller's function produced. It does not end the program and does not
// price the stages around it as though it could be run.
func TestPreflightStagedPlanRefusesAStageItCannotRender(t *testing.T) {
	const marker = "SCHEMAFLUX-MARKER-3c58"
	panics := draftStageOp("panics", "s")
	panics.BuildPrompt = func(draft, types.OpOptions) (string, string) { panic(marker) }
	noBuild := draftStageOp("nobuild", "s")
	noBuild.BuildPrompt = nil
	noDecode := draftStageOp("nodecode", "s")
	noDecode.Contract.Decode = nil

	cases := []struct {
		name      string
		ops       []Op[draft, draft]
		wantKind  types.ErrorKind
		wantStage string
	}{
		{"the first stage panics", []Op[draft, draft]{panics, draftStageOp("b", "s")}, types.KindInvariantViolation, "stage 1 of 2"},
		{"the second stage panics", []Op[draft, draft]{draftStageOp("a", "s"), panics}, types.KindInvariantViolation, "stage 2 of 2"},
		{"the last of four panics", []Op[draft, draft]{draftStageOp("a", "s"), draftStageOp("b", "s"), draftStageOp("c", "s"), panics}, types.KindInvariantViolation, "stage 4 of 4"},
		{"the only stage panics", []Op[draft, draft]{panics}, types.KindInvariantViolation, "stage 1 of 1"},
		{"a stage with no BuildPrompt", []Op[draft, draft]{draftStageOp("a", "s"), noBuild}, types.KindConfiguration, "stage 2 of 2"},
		{"a stage with no Decode", []Op[draft, draft]{noDecode, draftStageOp("b", "s")}, types.KindConfiguration, "stage 1 of 2"},
		{"two bad stages: the first is named", []Op[draft, draft]{draftStageOp("a", "s"), noBuild, panics}, types.KindConfiguration, "stage 2 of 3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PreflightStagedPlan(context.Background(), draftStages("job", tc.ops...), draft{Text: "the datum"}, types.PlanRequest{})
			if err != nil {
				t.Fatalf("PreflightStagedPlan returned an error beside the plan: %v", err)
			}
			p := got.Plan
			if p.Eligible || p.IneligibleKind != tc.wantKind {
				t.Fatalf("eligible %v, kind %s (%q), want a refusal of kind %s", p.Eligible, p.IneligibleKind, p.IneligibleReason, tc.wantKind)
			}
			if !strings.Contains(p.IneligibleReason, tc.wantStage) {
				t.Errorf("the reason %q does not name %s", p.IneligibleReason, tc.wantStage)
			}
			if strings.Contains(p.IneligibleReason, marker) {
				t.Errorf("the reason carries what the caller's function produced: %q", p.IneligibleReason)
			}
			if p.EstimatedCost != (types.CostEstimate{}) || p.MaxCalls != 0 {
				t.Errorf("a refused plan carries an estimate or a call ceiling: %+v, %d", p.EstimatedCost, p.MaxCalls)
			}
			refused := 0
			for _, stage := range got.Stages {
				if !stage.Eligible {
					refused++
					if stage.IneligibleReason == "" || stage.IneligibleKind == types.KindUnknown || stage.Requests != 0 || stage.Estimate != (types.CostEstimate{}) {
						t.Errorf("a refused stage %+v", stage)
					}
				}
			}
			if refused == 0 {
				t.Errorf("no stage says it was refused: %+v", got.Stages)
			}
		})
	}
}

// What cannot be planned at all is an error, classified, with nothing beside it.
func TestPreflightStagedPlanRefusesAJobItCannotPlan(t *testing.T) {
	var nilContext context.Context
	good := draftStages("job", draftStageOp("a", "s"))
	cases := []struct {
		name string
		ctx  context.Context
		plan StagedPlan[draft]
	}{
		{"a nil context", nilContext, good},
		{"no stages", context.Background(), StagedPlan[draft]{ID: types.OperationID{Name: "job"}}},
		{"an empty list of stages", context.Background(), StagedPlan[draft]{ID: types.OperationID{Name: "job"}, Stages: []Stage[draft]{}}},
		{"a nil context and no stages", nilContext, StagedPlan[draft]{}},
		{"a nil context and a plan with a stage but no name", nilContext, StagedPlan[draft]{Stages: good.Stages}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PreflightStagedPlan(tc.ctx, tc.plan, draft{Text: "d"}, types.PlanRequest{})
			if !errors.Is(err, types.ErrInvalidRequest) || types.KindOf(err) != types.KindInvalidRequest {
				t.Fatalf("err = %v (kind %s), want an invalid request", err, types.KindOf(err))
			}
			if got.Plan.Eligible || got.Stages != nil || got.Plan.Digest() != (types.Plan{}).Digest() {
				t.Errorf("a result beside the error: %+v", got)
			}
		})
	}
}

// A job takes the shape of a staged plan, and no other: the shape it is forced
// to is its own or is refused, never replaced by another.
func TestPreflightStagedPlanTakesNoShapeButItsOwn(t *testing.T) {
	plan := draftStages("job", draftStageOp("a", "s"), draftStageOp("b", "s"))
	cases := []struct {
		name         string
		force        types.ExecutionShape
		reason       string
		wantEligible bool
		wantForced   bool
		wantReason   string
	}{
		{"no shape forced", types.ShapeUnspecified, "", true, false, ""},
		{"the staged shape forced", types.ShapeSDMP, "", true, true, "caller forced this shape"},
		{"the staged shape forced with a reason", types.ShapeSDMP, "the caller's own words", true, true, "the caller's own words"},
		{"atomic forced", types.ShapeAtomic, "", false, false, "not legal"},
		{"batched forced", types.ShapeMDSP, "", false, false, "not legal"},
		{"global forced", types.ShapeGlobal, "", false, false, "not legal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PreflightStagedPlan(context.Background(), plan, draft{Text: "d"}, types.PlanRequest{Force: tc.force, ForceReason: tc.reason})
			if err != nil {
				t.Fatalf("PreflightStagedPlan: %v", err)
			}
			p := got.Plan
			if p.Eligible != tc.wantEligible {
				t.Fatalf("eligible %v (%s), want %v", p.Eligible, p.IneligibleReason, tc.wantEligible)
			}
			if tc.wantEligible {
				if p.Shape != types.ShapeSDMP || p.ShapeForced != tc.wantForced || (tc.wantReason != "" && p.ShapeReason != tc.wantReason) {
					t.Errorf("shape %s, forced %v, reason %q", p.Shape, p.ShapeForced, p.ShapeReason)
				}
				return
			}
			if p.IneligibleKind != types.KindConfiguration || !strings.Contains(p.IneligibleReason, tc.wantReason) {
				t.Errorf("kind %s, reason %q", p.IneligibleKind, p.IneligibleReason)
			}
		})
	}
}

// The budget bounds the whole job: the stages' estimates are summed, and the sum
// is what is held to it.
func TestPreflightStagedPlanHoldsTheWholeJobToTheBudget(t *testing.T) {
	plan := draftStages("job", draftStageOp("a", "s"), draftStageOp("b", "s"), draftStageOp("c", "s"))
	one, err := PreflightFor(context.Background(), draftStageOp("a", "s"), []draft{{Text: "d"}}, types.PlanRequest{Options: types.OpOptions{Model: "gpt-4"}})
	if err != nil || !one.EstimatedCost.Priced {
		t.Fatalf("PreflightFor: %v, %+v", err, one.EstimatedCost)
	}
	each := one.EstimatedCost.EstimatedCost

	cases := []struct {
		name     string
		model    string
		budget   float64
		wantOK   bool
		wantKind types.ErrorKind
		wantText string
	}{
		{"a generous budget", "gpt-4", 100, true, types.KindUnknown, ""},
		{"no budget", "gpt-4", 0, true, types.KindUnknown, ""},
		{"a negative budget is none", "gpt-4", -1, true, types.KindUnknown, ""},
		{"a budget of one stage's cost is not enough for three", "gpt-4", each * 1.5, false, types.KindBudgetExceeded, "over the budget"},
		{"a budget a little over the sum", "gpt-4", each*3 + 0.001, true, types.KindUnknown, ""},
		{"a budget a little under the sum", "gpt-4", each*3 - 0.001, false, types.KindBudgetExceeded, "over the budget"},
		{"a budget nothing could meet", "gpt-4", 1e-9, false, types.KindBudgetExceeded, "over the budget"},
		{"a budget on a model with no price", "definitely-not-a-real-model-xyz", 5, false, types.KindBudgetExceeded, "no price"},
		{"no budget on a model with no price", "definitely-not-a-real-model-xyz", 0, true, types.KindUnknown, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PreflightStagedPlan(context.Background(), plan, draft{Text: "d"}, types.PlanRequest{Options: types.OpOptions{Model: tc.model}, Budget: tc.budget})
			if err != nil {
				t.Fatalf("PreflightStagedPlan: %v", err)
			}
			if got.Plan.Eligible != tc.wantOK || got.Plan.IneligibleKind != tc.wantKind || !strings.Contains(got.Plan.IneligibleReason, tc.wantText) {
				t.Errorf("eligible %v, kind %s, reason %q; want eligible %v, kind %s, reason with %q",
					got.Plan.Eligible, got.Plan.IneligibleKind, got.Plan.IneligibleReason, tc.wantOK, tc.wantKind, tc.wantText)
			}
		})
	}
}

// The plan is made for the client on its context: its provider, its model and
// that model's window, as PreflightFor reads them, and a context with no client
// is planned for the process default.
func TestPreflightStagedPlanPlansForTheClientOnItsContext(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "process-model")
	planWindows(t, map[[2]string]int{
		{"staged-small", "small-model"}:     2000,
		{"staged-large", "large-model"}:     4000,
		{"staged-process", "process-model"}: 100000,
	})
	swapNoProvider(t)
	SetDefaultProvider(&scriptedCapabilityProvider{provider: "staged-process"})
	small := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: &scriptedCapabilityProvider{provider: "staged-small"}, Model: "small-model"})
	large := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: &scriptedCapabilityProvider{provider: "staged-large"}, Model: "large-model"})
	plan := draftStages("job", draftStageOp("a", "s"), draftStageOp("b", "s"))
	req := types.PlanRequest{Options: types.OpOptions{MaxOutputTokens: 777}}

	cases := []struct {
		name string
		ctx  context.Context
		want types.CapabilitySnapshot
	}{
		{"the small client", small, types.CapabilitySnapshot{Provider: "staged-small", Model: "small-model", MaxOutputTokens: 777, ContextTokens: 2000}},
		{"the large client", large, types.CapabilitySnapshot{Provider: "staged-large", Model: "large-model", MaxOutputTokens: 777, ContextTokens: 4000}},
		{"no client", context.Background(), types.CapabilitySnapshot{Provider: "staged-process", Model: "process-model", MaxOutputTokens: 777, ContextTokens: 100000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PreflightStagedPlan(tc.ctx, plan, draft{Text: "d"}, req)
			if err != nil || !got.Plan.Eligible {
				t.Fatalf("PreflightStagedPlan: %v (%s)", err, got.Plan.IneligibleReason)
			}
			if got.Plan.Capability != tc.want {
				t.Errorf("capability %+v, want %+v", got.Plan.Capability, tc.want)
			}
			// What each stage is priced at is the model's ceiling for its own client.
			for i, stage := range got.Stages {
				if stage.Estimate.EstimatedCompletionTokens != 777 {
					t.Errorf("stage %d: %d completion tokens, want the client's ceiling of 777", i+1, stage.Estimate.EstimatedCompletionTokens)
				}
			}
		})
	}
}

// A plan reads the client once: a default replaced while the plan is made does
// not give one stage the provider of one client and another the model of the next.
func TestPreflightStagedPlanReadsTheClientOnce(t *testing.T) {
	planWindows(t, map[[2]string]int{
		{"staged-first", "first-model"}:   1000,
		{"staged-second", "second-model"}: 9000,
	})
	swapNoProvider(t)
	second := &ExecConfig{Provider: &namedProvider{name: "staged-second"}, Model: "second-model"}
	first := &ExecConfig{Provider: &swapOnName{scriptedCapabilityProvider: scriptedCapabilityProvider{provider: "staged-first"}, next: second}, Model: "first-model"}
	withDefaultExec(t, first)

	got, err := PreflightStagedPlan(context.Background(), draftStages("job", draftStageOp("a", "s"), draftStageOp("b", "s")), draft{Text: "d"}, types.PlanRequest{})
	if err != nil || !got.Plan.Eligible {
		t.Fatalf("PreflightStagedPlan: %v (%s)", err, got.Plan.IneligibleReason)
	}
	if c := got.Plan.Capability; c.Provider != "staged-first" || c.Model != "first-model" || c.ContextTokens != 1000 {
		t.Errorf("a plan across a swap = %+v, want the first client's route whole", c)
	}
}

// A plan renders each stage's prompt once and runs no check: a check is the
// caller's code, what it would find is not known until the stage before it has
// answered, and a plan that ran it would decide what a run is to decide.
func TestPreflightStagedPlanRendersEachStageOnceAndRunsNoCheck(t *testing.T) {
	var builds atomic.Int32
	var checks atomic.Int32
	counted := func(name string) Op[draft, draft] {
		op := draftStageOp(name, "system")
		render := op.BuildPrompt
		op.BuildPrompt = func(d draft, opt types.OpOptions) (string, string) {
			builds.Add(1)
			return render(d, opt)
		}
		return op
	}
	check := func(d draft) (draft, types.ContractLevel, bool) {
		checks.Add(1)
		return d, types.ContractFullyGoverned, true
	}
	plan := StagedPlan[draft]{ID: types.OperationID{Name: "job"}, Stages: []Stage[draft]{
		{Op: counted("a"), DeterministicCheck: check},
		{Op: counted("b"), DeterministicCheck: check},
		{Op: counted("c")},
		{Op: counted("d")},
	}}
	got, err := PreflightStagedPlan(context.Background(), plan, draft{Text: "d"}, types.PlanRequest{})
	if err != nil || !got.Plan.Eligible {
		t.Fatalf("PreflightStagedPlan: %v (%s)", err, got.Plan.IneligibleReason)
	}
	if n := builds.Load(); n != 4 {
		t.Errorf("BuildPrompt was called %d times for four stages, want once for each", n)
	}
	if n := checks.Load(); n != 0 {
		t.Errorf("a plan ran %d checks", n)
	}
	wantSettle := []bool{true, true, false, false}
	for i, stage := range got.Stages {
		if stage.MaySettleWithoutAsking != wantSettle[i] {
			t.Errorf("stage %d: MaySettleWithoutAsking = %v, want %v", i+1, stage.MaySettleWithoutAsking, wantSettle[i])
		}
		if stage.Requests != 1 {
			t.Errorf("stage %d: %d requests: a stage with a check is still counted, since a plan cannot know it will settle", i+1, stage.Requests)
		}
	}
}

// Running the plan afterwards asks no more than it said. With the library's own
// reinforcement block left off the system prompt, the first stage's request is
// exactly what the plan priced; with it on, which is what a caller gets unless
// the client says otherwise, the request carries that fixed block as well, which
// a plan does not count, and what holds is the cost: no stage costs more than its
// estimate, nor the run more than the plan's, because the answer is priced at the
// model's ceiling.
func TestRunningAStagedPlanStaysWithinItsEstimate(t *testing.T) {
	for _, omit := range []bool{true, false} {
		t.Run(fmt.Sprintf("omitting the reinforcement block: %v", omit), func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gpt-4")
			provider := &recordingProvider{
				answer: `{"text":"short"}`,
				usage:  types.TokenUsage{PromptTokens: 150, CompletionTokens: 20, TotalTokens: 170},
			}
			ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider, Model: "gpt-4", OmitReinforcement: omit})
			opt := types.OpOptions{Model: "gpt-4"}
			datum := draft{Text: strings.Repeat("the datum is long. ", 40)}
			plan := draftStages("job", draftStageOp("extract", strings.Repeat("e", 600)), draftStageOp("verify", strings.Repeat("v", 300)))

			planned, err := PreflightStagedPlan(ctx, plan, datum, types.PlanRequest{Options: opt})
			if err != nil || !planned.Plan.Eligible {
				t.Fatalf("PreflightStagedPlan: %v (%s)", err, planned.Plan.IneligibleReason)
			}
			if n := len(provider.sent()); n != 0 {
				t.Fatalf("planning sent %d requests", n)
			}

			result, err := RunStagedPlan(ctx, plan, datum, opt)
			if err != nil {
				t.Fatalf("RunStagedPlan: %v", err)
			}
			sent := provider.sent()
			if len(sent) > planned.Plan.MaxCalls || len(sent) != 2 {
				t.Fatalf("%d requests sent, the plan said at most %d (and the run needs both stages)", len(sent), planned.Plan.MaxCalls)
			}

			if omit {
				// The first stage is priced from the datum, which is its input: its
				// request is what was planned, to the token.
				if got, want := requestTokens(sent[0].SystemPrompt, sent[0].UserPrompt), planned.Stages[0].Estimate.EstimatedPromptTokens; got != want {
					t.Errorf("the first request carried %d prompt tokens, the plan priced %d", got, want)
				}
				// The second is priced as an upper bound from the datum, and its input
				// is the short answer of the first.
				if got, bound := requestTokens(sent[1].SystemPrompt, sent[1].UserPrompt), planned.Stages[1].Estimate.EstimatedPromptTokens; got > bound || !planned.Stages[1].UpperBound {
					t.Errorf("the second request carried %d prompt tokens against an upper bound of %d (upper bound marked: %v)", got, bound, planned.Stages[1].UpperBound)
				}
			}
			for i, request := range sent {
				if request.MaxTokens > planned.Plan.Capability.MaxOutputTokens {
					t.Errorf("request %d may answer with %d tokens, over the %d the plan priced it at", i+1, request.MaxTokens, planned.Plan.Capability.MaxOutputTokens)
				}
			}

			var spent float64
			for i, outcome := range result.Stages {
				meta := outcome.Meta
				if meta.Usage.CompletionTokens > planned.Stages[i].Estimate.EstimatedCompletionTokens {
					t.Errorf("stage %d answered with %d tokens, the plan priced at most %d", i+1, meta.Usage.CompletionTokens, planned.Stages[i].Estimate.EstimatedCompletionTokens)
				}
				if !meta.Cost.Priced || !planned.Stages[i].Estimate.Priced {
					t.Fatalf("stage %d: cost priced %v, estimate priced %v: the comparison needs both", i+1, meta.Cost.Priced, planned.Stages[i].Estimate.Priced)
				}
				if meta.Cost.TotalCost > planned.Stages[i].Estimate.EstimatedCost {
					t.Errorf("stage %d cost %v, over its estimate of %v", i+1, meta.Cost.TotalCost, planned.Stages[i].Estimate.EstimatedCost)
				}
				spent += meta.Cost.TotalCost
			}
			if spent > planned.Plan.EstimatedCost.EstimatedCost {
				t.Errorf("the run cost %v, over the plan's estimate of %v", spent, planned.Plan.EstimatedCost.EstimatedCost)
			}
		})
	}
}
