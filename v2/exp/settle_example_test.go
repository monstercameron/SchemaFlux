package exp_test

import (
	"context"
	"fmt"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleSettle() {
	p := &schemafluxtest.Provider{}
	p.Reply(settleAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p})
	r, err := exp.Settle[settleTarget](ctx, "deliver by Friday")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(r.Solution.Answer)
	// Output: ok
}

func ExampleSettleAdversarial() {
	model := &schemafluxtest.Provider{}
	model.Reply(settleAdversarialAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	positions := exp.AdversarialContext[settleTarget]{
		Ours:        exp.AdversarialPosition[settleTarget]{Position: settleTarget{Answer: "ours"}},
		Theirs:      exp.AdversarialPosition[settleTarget]{Position: settleTarget{Answer: "theirs"}},
		OurLeverage: "balanced",
	}
	result, err := exp.SettleAdversarial(ctx, positions)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(result.DealReached)
	// Output: true
}
