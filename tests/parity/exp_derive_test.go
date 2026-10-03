package parity

import (
	"context"
	oldroot "github.com/monstercameron/schemaflux"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"math"
	"testing"
)

type deriveParityTarget struct {
	Label string `json:"label"`
}

const expDeriveAnswer = `{"derived":{"label":"known"},"derivations":[],"field_confidence":{"label":0.9},"overall_confidence":0.9}`

func expDeriveCase(name string, old oldroot.DeriveOptions, v2 exp.DeriveOptions) parityCase {
	input := map[string]string{"label": "known"}
	return parityCase{name: "Derive/" + name, op: "exp.derive", replies: []string{expDeriveAnswer}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			if name == "default" {
				old.IncludeReasoning = true
			}
			old.Context = ctx
			_, e := oldroot.Derive[map[string]string, deriveParityTarget](input, old)
			return e
		},
		v2: func(ctx context.Context) error { _, e := exp.Derive[deriveParityTarget](ctx, input, v2); return e }}
}
func TestExpDeriveSendsTheRequestTheOldRootSends(t *testing.T) {
	fields := []string{"label"}
	rules := map[string]string{"label": "copy"}
	cases := []parityCase{
		expDeriveCase("default", oldroot.DeriveOptions{}, exp.DeriveOptions{}),
		expDeriveCase("Fields", oldroot.DeriveOptions{Fields: fields, IncludeReasoning: true}, exp.DeriveOptions{Fields: fields}),
		expDeriveCase("Rules", oldroot.DeriveOptions{Rules: rules, IncludeReasoning: true}, exp.DeriveOptions{Rules: rules}),
		expDeriveCase("OmitReasoning", oldroot.DeriveOptions{IncludeReasoning: false}, exp.DeriveOptions{OmitReasoning: true}),
		expDeriveCase("MinConfidence", oldroot.DeriveOptions{MinConfidence: .8, IncludeReasoning: true}, exp.DeriveOptions{MinConfidence: .8}),
		expDeriveCase("MinConfidence negative means no floor", oldroot.DeriveOptions{MinConfidence: math.SmallestNonzeroFloat64, IncludeReasoning: true}, exp.DeriveOptions{MinConfidence: -1}),
	}
	for _, c := range commonCases {
		o := oldroot.DeriveOptions{Model: c.v2.Model, Intelligence: oldroot.Speed(c.v2.Tier), Effort: oldroot.Effort(c.v2.Effort), Mode: oldroot.Mode(c.v2.Mode), Steering: c.v2.Steering, MaxOutputTokens: c.v2.MaxOutputTokens, WebSearch: c.v2.WebSearch, IncludeReasoning: true}
		cases = append(cases, expDeriveCase(c.name, o, exp.DeriveOptions{Options: c.v2}))
	}
	runParity(t, cases)
}
