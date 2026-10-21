package flow_test

import (
	"context"
	"fmt"

	"github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleDecide() {
	var model schemafluxtest.Provider
	model.Reply(`{"selected":0,"explanation":"it is about an invoice","confidence":0.9,"alternatives":[]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	got, err := flow.Decide(ctx, "I was charged twice for my order.", []flow.Decision[string]{
		{Value: "billing", Description: "a question about an invoice or a payment"},
		{Value: "general", Description: "anything else", Fallback: true},
	})
	fmt.Println(got.Value, got.Index, got.Fallback, err)
	fmt.Println(got.Explanation)
	// Output:
	// billing 0 false <nil>
	// it is about an invoice
}

// A decision marked Fallback is taken when the model cannot be reached or its
// answer cannot be used, and the result says so. The caller's own cancellation
// is not such a failure: it is returned.
func ExampleDecide_fallback() {
	var model schemafluxtest.Provider
	model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable})
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	got, err := flow.Decide(ctx, "I was charged twice for my order.", []flow.Decision[string]{
		{Value: "billing", Description: "a question about an invoice or a payment"},
		{Value: "general", Description: "anything else", Fallback: true},
	})
	fmt.Println(got.Value, got.Index, got.Fallback, err)
	// Output: general 1 true <nil>
}

// A decision whose Condition holds is taken without asking a model.
func ExampleDecide_condition() {
	var model schemafluxtest.Provider
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	got, err := flow.Decide(ctx, 120, []flow.Decision[string]{
		{Value: "approve", Description: "a small amount", Condition: func(amount any) bool { return amount.(int) < 500 }},
		{Value: "review", Description: "anything else"},
	})
	fmt.Println(got.Value, got.ByCondition, model.Calls(), err)
	// Output: approve true 0 <nil>
}
