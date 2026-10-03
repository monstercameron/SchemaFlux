package schemaflux

import (
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

func TestAPricedCostSaysItsCurrency(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cost types.CostInfo
		want Cost
	}{
		{"priced with no currency stated", types.CostInfo{TotalCost: 2, PromptCost: 1, CompletionCost: 1, Priced: true, PricingSource: "m"},
			Cost{Total: 2, Input: 1, Output: 1, Currency: "USD", Priced: true, Source: "m"}},
		{"priced with USD stated", types.CostInfo{TotalCost: 2, Currency: "USD", Priced: true}, Cost{Total: 2, Currency: "USD", Priced: true}},
		{"priced in another currency stated", types.CostInfo{TotalCost: 2, Currency: "EUR", Priced: true}, Cost{Total: 2, Currency: "EUR", Priced: true}},
		{"estimated and priced", types.CostInfo{Priced: true, Quality: types.PricingEstimated}, Cost{Currency: "USD", Priced: true, Estimated: true}},
		{"free and priced", types.CostInfo{Priced: true, Quality: types.PricingFree}, Cost{Currency: "USD", Priced: true}},
		{"unpriced with nothing stated", types.CostInfo{}, Cost{}},
		{"unpriced with a currency stated", types.CostInfo{Currency: "USD"}, Cost{Currency: "USD"}},
		{"cached and reasoning", types.CostInfo{CachedCost: 3, ReasoningCost: 4, Priced: true}, Cost{Cached: 3, Reasoning: 4, Currency: "USD", Priced: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := costFromInternal(c.cost); got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestACostGoesToTheInternalFormAndBack(t *testing.T) {
	t.Parallel()
	cases := []Cost{
		{},
		{Total: 2, Input: 1, Output: 1, Currency: "USD", Priced: true, Source: "m"},
		{Total: 2, Currency: "USD", Priced: true, Estimated: true},
		{Cached: 3, Reasoning: 4, Currency: "USD", Priced: true},
		{Currency: "USD"},
	}
	for _, c := range cases {
		if got := costFromInternal(costToInternal(c)); got != c {
			t.Errorf("%+v came back as %+v", c, got)
		}
	}
	if q := costToInternal(Cost{Priced: true, Estimated: true}).Quality; q != types.PricingEstimated {
		t.Errorf("an estimated cost is %v", q)
	}
	if q := costToInternal(Cost{}).Quality; q != types.PricingUnknown {
		t.Errorf("an unpriced cost is %v", q)
	}
}
