package parity

import (
	"context"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

const expArbitrateAnswer = `{"options":{"o1":{"r1":{"passed":true,"score":0.8,"reasoning":"good"}},"o2":{"r1":{"passed":true,"score":0.6,"reasoning":"less good"}}},"reasoning":"compared","confidence":0.7}`

func expArbitrateCase(name string, old oldroot.ArbitrateOptions, v2 exp.ArbitrateOptions) parityCase {
	items := []string{"a", "b"}
	rules := []string{"good"}
	return parityCase{name: "Arbitrate/" + name, op: "exp.arbitrate", replies: []string{expArbitrateAnswer}, floor: schemaflux.ContractSchemaAndInvariantChecked,
		old: func(ctx context.Context) error {
			old.Context = ctx
			old.Rules = rules
			_, err := oldroot.Arbitrate(items, old)
			return err
		},
		v2: func(ctx context.Context) error { _, err := exp.Arbitrate(ctx, items, rules, v2); return err },
	}
}

func TestExpArbitrateSendsTheRequestTheOldRootSends(t *testing.T) {
	base := func() oldroot.ArbitrateOptions { return oldroot.ArbitrateOptions{} }
	weights := base()
	weights.Weights = []float64{2}
	require := base()
	require.RequireAllRules = true
	reason := base()
	reason.IncludeReasoning = true
	tiebreak := base()
	tiebreak.Tiebreaker = "first"
	cases := []parityCase{
		expArbitrateCase("default", base(), exp.ArbitrateOptions{}),
		expArbitrateCase("Weights", weights, exp.ArbitrateOptions{Weights: []float64{2}}),
		expArbitrateCase("RequireAllRules", require, exp.ArbitrateOptions{RequireAllRules: true}),
		expArbitrateCase("IncludeReasoning", reason, exp.ArbitrateOptions{IncludeReasoning: true}),
		expArbitrateCase("Tiebreaker", tiebreak, exp.ArbitrateOptions{Tiebreaker: "first"}),
	}
	for _, c := range commonCases {
		old := base()
		old.Intelligence = types.Speed(c.v2.Tier)
		old.Effort = types.Effort(c.v2.Effort)
		old.Mode = types.Mode(c.v2.Mode)
		old.Model, old.Steering = c.v2.Model, c.v2.Steering
		old.MaxOutputTokens, old.WebSearch = c.v2.MaxOutputTokens, c.v2.WebSearch
		cases = append(cases, expArbitrateCase(c.name, old, exp.ArbitrateOptions{Options: c.v2}))
	}
	runParity(t, cases)
}
