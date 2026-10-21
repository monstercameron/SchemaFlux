package ops

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/pricing"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// ceilingProvider is a provider that only has a name: the budget check reads
// nothing else of it.
type ceilingProvider struct{ name string }

func (p ceilingProvider) Name() string { return p.name }

func (ceilingProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, nil
}

func (ceilingProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }

func (ceilingProvider) RetryPolicy() (int, time.Duration) { return -1, 0 }

const ceilingPricedModel = "ceiling-priced-model"

func init() {
	if err := pricing.RegisterPricingModel(pricing.PricingModel{
		Provider: "custom", Model: ceilingPricedModel, PricePerPromptToken: 0.001, PricePerCompletionToken: 0.002, Currency: "USD",
	}); err != nil {
		panic(err)
	}
}

func ctxWithBudget(budget BudgetChecker) context.Context {
	return WithSharedExecConfig(context.Background(), &ExecConfig{Budget: budget})
}

func TestTheFlagsOfALedgerSayWhenAnUnpricedCallIsRefused(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		budget *ClientBudget
		want   bool
	}{
		{"no flag", NewClientBudget(5), false},
		{"the flag and a ceiling", NewClientBudget(5).RefuseUnpriced(), true},
		{"the flag and no ceiling", NewClientBudget(0).RefuseUnpriced(), false},
		{"the flag and a negative ceiling", NewClientBudget(-1).RefuseUnpriced(), false},
		{"the flag and a ceiling that is not a number", NewClientBudget(math.NaN()).RefuseUnpriced(), false},
		{"the flag and an infinite ceiling", NewClientBudget(math.Inf(1)).RefuseUnpriced(), false},
		{"the flag and a tiny ceiling", NewClientBudget(1e-12).RefuseUnpriced(), true},
		{"the other flag alone", NewClientBudget(5).KeepProcessCeiling(), false},
		{"both flags", NewClientBudget(5).KeepProcessCeiling().RefuseUnpriced(), true},
		{"both flags and no ceiling", NewClientBudget(0).KeepProcessCeiling().RefuseUnpriced(), false},
	}
	for _, c := range cases {
		if got := c.budget.refusesUnpricedCalls(); got != c.want {
			t.Errorf("%s: refusesUnpricedCalls = %v, want %v", c.name, got, c.want)
		}
	}
	if NewClientBudget(1).keepsProcess() || !NewClientBudget(1).KeepProcessCeiling().keepsProcess() {
		t.Error("keepsProcess does not follow KeepProcessCeiling")
	}
}

func TestACeilingStopsACallOnAModelThatHasNoPrice(t *testing.T) {
	t.Parallel()
	provider := ceilingProvider{name: "custom"}
	cases := []struct {
		name    string
		budget  BudgetChecker
		model   string
		refused bool
	}{
		{"an unpriced model under a ceiling", NewClientBudget(5).RefuseUnpriced(), "ceiling-unpriced-a", true},
		{"a priced model under a ceiling", NewClientBudget(5).RefuseUnpriced(), ceilingPricedModel, false},
		{"a dated snapshot of a priced model", NewClientBudget(5).RefuseUnpriced(), ceilingPricedModel + "-2025-08-07", false},
		{"an unpriced model when the ledger allows it", NewClientBudget(5), "ceiling-unpriced-b", false},
		{"an unpriced model under no ceiling", NewClientBudget(0).RefuseUnpriced(), "ceiling-unpriced-c", false},
		{"an unpriced model on a ledger with no flags", NewClientBudget(5).KeepProcessCeiling(), "ceiling-unpriced-d", false},
		{"no model at all", NewClientBudget(5).RefuseUnpriced(), "", true},
	}
	for _, c := range cases {
		err := checkBudgetFor(ctxWithBudget(c.budget), provider, c.model)
		if c.refused {
			if types.KindOf(err) != types.KindBudgetExceeded {
				t.Errorf("%s: got %v, want a budget refusal", c.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if err := checkBudgetFor(context.Background(), provider, "ceiling-unpriced-e"); err != nil {
		t.Errorf("a context with no client budget refused an unpriced model: %v", err)
	}
}

func TestTheRefusalOfAnUnpricedModelNamesItBoundedAndPrintsNothingElse(t *testing.T) {
	t.Parallel()
	hostile := "model\nlevel=ERROR forged " + strings.Repeat("m", 500)
	err := checkBudgetFor(ctxWithBudget(NewClientBudget(5).RefuseUnpriced()), ceilingProvider{name: "custom"}, hostile)
	if types.KindOf(err) != types.KindBudgetExceeded {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "\n") || strings.Contains(err.Error(), strings.Repeat("m", 200)) {
		t.Errorf("the model name was written whole: %.300q", err.Error())
	}
	if !strings.Contains(err.Error(), "no price") {
		t.Errorf("the error does not say the model has no price: %v", err)
	}
}

func TestALedgerThatKeepsTheProcessCeilingIsHeldToItAsWell(t *testing.T) {
	// The process ceiling is process state.
	pricing.ResetCostTracking()
	pricing.SetBudget(1, 0, 0, nil)
	pricing.SetBudgetEnforcement(true)
	t.Cleanup(func() {
		pricing.SetBudgetEnforcement(false)
		pricing.ResetBudget()
		pricing.ResetCostTracking()
	})
	pricing.TrackCost(&types.CostInfo{TotalCost: 2, Priced: true, Currency: "USD"}, &types.ResultMetadata{RequestID: "spent", EndTime: time.Now()})

	replaces := ctxWithBudget(NewClientBudget(100))
	keeps := ctxWithBudget(NewClientBudget(100).KeepProcessCeiling())
	counts := ctxWithBudget(NewClientBudget(0).KeepProcessCeiling())
	if err := checkBudget(replaces); err != nil {
		t.Errorf("a ledger that replaces the process ceiling was refused by it: %v", err)
	}
	for name, ctx := range map[string]context.Context{"with a ceiling": keeps, "that only counts": counts, "none": context.Background()} {
		err := checkBudget(ctx)
		if types.KindOf(err) != types.KindBudgetExceeded || !errors.Is(err, pricing.ErrBudgetExceeded) {
			t.Errorf("%s: got %v, want the process ceiling's refusal", name, err)
		}
	}
	own := NewClientBudget(1).KeepProcessCeiling()
	own.Record(1.5, true)
	if err := checkBudget(ctxWithBudget(own)); !errors.Is(err, ErrClientBudgetExhausted) {
		t.Errorf("a ledger at its own ceiling returned %v, want its own refusal first", err)
	}
}

func TestAttachedFloorReadsOnlyThePolicyOnTheContext(t *testing.T) {
	attach := func(level types.ContractLevel) context.Context {
		return WithContractPolicy(context.Background(), types.DataPolicy{MinimumContract: level})
	}
	for _, level := range []types.ContractLevel{types.ContractPromptOnly, types.ContractJSONWellFormed, types.ContractSchemaConstrained,
		types.ContractSchemaAndInvariantChecked, types.ContractEvidenceChecked, types.ContractFullyGoverned} {
		if got, ok := AttachedFloor(attach(level)); !ok || got != level {
			t.Errorf("AttachedFloor(%v) = %v, %v", level, got, ok)
		}
	}
	if _, ok := AttachedFloor(context.Background()); ok {
		t.Error("a context with no policy reported one")
	}
	if _, ok := AttachedFloor(nil); ok { //nolint:staticcheck // the nil context is the case
		t.Error("a nil context reported a policy")
	}
	client := WithSharedExecConfig(context.Background(), &ExecConfig{DataPolicyDeclared: true, DataPolicy: types.DataPolicy{MinimumContract: types.ContractFullyGoverned}})
	if _, ok := AttachedFloor(client); ok {
		t.Error("the client's own policy was reported as one attached to the context")
	}
	if allocs := testing.AllocsPerRun(100, func() { _, _ = AttachedFloor(client) }); allocs != 0 {
		t.Errorf("AttachedFloor allocates %v times", allocs)
	}
}
