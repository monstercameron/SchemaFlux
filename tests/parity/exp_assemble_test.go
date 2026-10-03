package parity

import (
	"context"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type assembleParityTarget struct {
	Name string `json:"name"`
}

const assembleParityAnswer = `{"composed":{"name":"Ada"},"field_sources":[],"conflicts_resolved":0,"gaps_filled":[],"unused_parts":[],"completeness":0.8}`

func expAssembleCase(name string, old oldroot.ComposeOptions, v2 exp.AssembleOptions) parityCase {
	parts := []any{"part"}
	return parityCase{name: "Assemble/" + name, op: "exp.assemble", replies: []string{assembleParityAnswer}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			old.Context = ctx
			_, err := oldroot.Assemble[assembleParityTarget](parts, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := exp.Assemble[assembleParityTarget](ctx, parts, v2)
			return err
		}}
}

func TestExpAssembleSendsTheRequestTheOldRootSends(t *testing.T) {
	base := func() oldroot.ComposeOptions { return ops.NewComposeOptions() }
	cases := []parityCase{
		expAssembleCase("default", base(), exp.AssembleOptions{}),
		expAssembleCase("Template", func() oldroot.ComposeOptions { o := base(); o.Template = "person"; return o }(), exp.AssembleOptions{Template: "person"}),
		expAssembleCase("MergeStrategy", func() oldroot.ComposeOptions { o := base(); o.MergeStrategy = "first"; return o }(), exp.AssembleOptions{MergeStrategy: "first"}),
		expAssembleCase("FillGaps", func() oldroot.ComposeOptions { o := base(); o.FillGaps = true; return o }(), exp.AssembleOptions{FillGaps: true}),
	}
	for _, c := range commonCases {
		old := base()
		if c.v2.Model != "" {
			old.Model = c.v2.Model
		}
		if c.v2.Tier != 0 {
			old.Intelligence = types.Speed(c.v2.Tier)
		}
		if c.v2.Effort != "" {
			old.Effort = types.Effort(c.v2.Effort)
		}
		if c.v2.Mode != 0 {
			old.Mode = types.Mode(c.v2.Mode)
		}
		old.Steering = c.v2.Steering
		old.MaxOutputTokens = c.v2.MaxOutputTokens
		old.WebSearch = c.v2.WebSearch
		cases = append(cases, expAssembleCase(c.name, old, exp.AssembleOptions{Options: c.v2}))
	}
	runParity(t, cases)
}
