package schemaflux

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/pricing"
)

// budgetModel is priced at two dollars a million input tokens, so that a request
// that reports a million tokens costs two dollars.
const budgetModel = "budget-edge-model"

func init() { registerPriceForTest(budgetModel, 2, 0) }

// spending is a provider whose every answer reports the tokens of a call that
// cost two dollars on budgetModel.
func spending() *answerer {
	return &answerer{name: "budget-edge", answer: func(int, Request) (Response, error) {
		return Response{Content: `{"number":"A","total":1}`, Model: budgetModel, FinishReason: "stop",
			Usage: Usage{Input: 1_000_000, Total: 1_000_000}}, nil
	}}
}

func callOn(client Client) error {
	_, err := Extract[limitsInvoice](WithClient(context.Background(), client), "invoice")
	return err
}

// withProcessCeiling holds the process to a daily ceiling for the length of the
// test. It is process state, so the tests that use it are not parallel.
func withProcessCeiling(t *testing.T, dollars float64) {
	t.Helper()
	pricing.ResetCostTracking()
	pricing.SetBudget(dollars, 0, 0, nil)
	pricing.SetBudgetEnforcement(true)
	t.Cleanup(func() {
		pricing.SetBudgetEnforcement(false)
		pricing.ResetBudget()
		pricing.ResetCostTracking()
	})
}

func TestAClientBudgetNeverLiftsTheProcessCeiling(t *testing.T) {
	withProcessCeiling(t, 0.5)
	provider := spending()
	if err := callOn(Client{Provider: provider, Model: budgetModel}); err != nil {
		t.Fatalf("the first call, under the ceiling: %v", err)
	}
	clients := map[string]Client{
		"no budget":                           {Provider: provider, Model: budgetModel},
		"a budget that only counts":           {Provider: provider, Model: budgetModel, Budget: &Budget{}},
		"a budget with a limit":               {Provider: provider, Model: budgetModel, Budget: &Budget{Limit: 100}},
		"a budget that allows unpriced calls": {Provider: provider, Model: budgetModel, Budget: &Budget{Limit: 100, AllowUnpriced: true}},
	}
	for name, client := range clients {
		before := provider.count()
		err := callOn(client)
		var e *Error
		if !errors.As(err, &e) || e.Kind != ErrBudgetExceeded {
			t.Errorf("%s: the call after the ceiling was reached returned %v, want ErrBudgetExceeded", name, err)
		}
		if provider.count() != before {
			t.Errorf("%s: a request was sent past the process ceiling", name)
		}
	}
}

func TestAClientBudgetStillHoldsItsOwnLimitUnderAProcessCeiling(t *testing.T) {
	withProcessCeiling(t, 1000)
	provider := spending()
	budget := &Budget{Limit: 3}
	client := Client{Provider: provider, Model: budgetModel, Budget: budget}
	for call := 1; call <= 2; call++ {
		if err := callOn(client); err != nil {
			t.Fatalf("call %d: %v", call, err)
		}
	}
	err := callOn(client)
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("the call past the limit returned %v, want ErrBudgetExceeded", err)
	}
	if spent, complete := budget.Spent(); spent != 4 || !complete {
		t.Errorf("Spent = %v, %v, want 4 and complete", spent, complete)
	}
}

func TestABudgetWithNoLimitCountsAndLimitsNothing(t *testing.T) {
	t.Parallel()
	provider := spending()
	budget := &Budget{}
	for call := 1; call <= 5; call++ {
		if err := callOn(Client{Provider: provider, Model: budgetModel, Budget: budget}); err != nil {
			t.Fatalf("call %d: %v", call, err)
		}
	}
	if spent, complete := budget.Spent(); spent != 10 || !complete {
		t.Errorf("Spent = %v, %v, want 10 and complete", spent, complete)
	}
	// An unpriced model is no reason to refuse a budget that has no limit.
	unpriced := &answerer{name: "budget-edge-unpriced"}
	if err := callOn(Client{Provider: unpriced, Model: "budget-unpriced-model-a", Budget: &Budget{}}); err != nil && errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("a budget with no limit refused an unpriced model: %v", err)
	}
}

func TestAnUnpricedModelUnderALimitIsRefusedBeforeItIsSent(t *testing.T) {
	t.Parallel()
	const model = "budget-unpriced-model-b"
	provider := &answerer{name: "budget-edge-unpriced", answer: func(int, Request) (Response, error) {
		return Response{Content: `{"number":"A","total":1}`, Model: model, FinishReason: "stop", Usage: Usage{Input: 10, Total: 10}}, nil
	}}
	budget := &Budget{Limit: 0.0001}
	err := callOn(Client{Provider: provider, Model: model, Budget: budget})
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrBudgetExceeded {
		t.Fatalf("got %v, want ErrBudgetExceeded", err)
	}
	if provider.count() != 0 {
		t.Errorf("%d requests were sent to a model that cannot be priced", provider.count())
	}
	if !strings.Contains(e.Error(), model) || !strings.Contains(e.Error(), "no price") {
		t.Errorf("the error does not say that %s has no price: %v", model, e)
	}
	if spent, complete := budget.Spent(); spent != 0 || !complete {
		t.Errorf("a refused call was counted: %v, %v", spent, complete)
	}
}

func TestAnUnpricedModelIsAllowedWhenTheBudgetSaysSo(t *testing.T) {
	t.Parallel()
	const model = "budget-unpriced-model-c"
	provider := &answerer{name: "budget-edge-unpriced", answer: func(int, Request) (Response, error) {
		return Response{Content: `{"number":"A","total":1}`, Model: model, FinishReason: "stop", Usage: Usage{Input: 10, Total: 10}}, nil
	}}
	budget := &Budget{Limit: 0.0001, AllowUnpriced: true}
	for call := 1; call <= 3; call++ {
		if err := callOn(Client{Provider: provider, Model: model, Budget: budget}); err != nil {
			t.Fatalf("call %d: %v", call, err)
		}
	}
	if spent, complete := budget.Spent(); spent != 0 || complete {
		t.Errorf("Spent = %v, %v, want 0 and a floor", spent, complete)
	}
}

func TestTheModelNamedInABudgetRefusalIsBounded(t *testing.T) {
	t.Parallel()
	hostile := "evil\nforged line " + strings.Repeat("m", 400)
	provider := &answerer{name: "budget-edge-unpriced"}
	err := callOn(Client{Provider: provider, Model: hostile, Budget: &Budget{Limit: 1}})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("got %v, want ErrBudgetExceeded", err)
	}
	if strings.Contains(err.Error(), "\n") || strings.Contains(err.Error(), strings.Repeat("m", 200)) {
		t.Errorf("the model name was written whole: %.300q", err.Error())
	}
}
