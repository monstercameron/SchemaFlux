package parity

import (
	"context"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type synthesisParityTarget struct {
	Summary string `json:"summary"`
}

const expSynthesizeAnswer = `{"synthesized":{"summary":"combined"},"facts":[],"insights":[],"conflicts":[],"source_coverage":{}}`

func expSynthesizeCase(name string, old oldroot.SynthesizeOptions, v2 exp.SynthesizeOptions) parityCase {
	sources := []any{"first source", "second source"}
	return parityCase{name: "Synthesize/" + name, op: "exp.synthesize", replies: []string{expSynthesizeAnswer}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.Synthesize[synthesisParityTarget](sources, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := exp.Synthesize[synthesisParityTarget](ctx, sources, v2)
			return err
		},
	}
}

func TestExpSynthesizeSendsTheRequestTheOldRootSends(t *testing.T) {
	base := oldroot.NewSynthesizeOptions
	priorities := []int{1, 0}
	focus := []string{"risks"}
	exclude := []string{"names"}
	cases := []parityCase{
		expSynthesizeCase("default", base(), exp.SynthesizeOptions{}),
		expSynthesizeCase("Strategy", base().WithStrategy("compare"), exp.SynthesizeOptions{Strategy: "compare"}),
		expSynthesizeCase("ConflictResolution", base().WithConflictResolution("majority"), exp.SynthesizeOptions{ConflictResolution: "majority"}),
		expSynthesizeCase("SourcePriorities", base().WithConflictResolution("source-priority").WithSourcePriorities(priorities), exp.SynthesizeOptions{ConflictResolution: "source-priority", SourcePriorities: priorities}),
		expSynthesizeCase("OmitCitations", base().WithCiteSources(false), exp.SynthesizeOptions{OmitCitations: true}),
		expSynthesizeCase("OmitInsights", base().WithGenerateInsights(false), exp.SynthesizeOptions{OmitInsights: true}),
		expSynthesizeCase("OutputStructure", base().WithOutputStructure("brief"), exp.SynthesizeOptions{OutputStructure: "brief"}),
		expSynthesizeCase("FocusAreas", base().WithFocusAreas(focus), exp.SynthesizeOptions{FocusAreas: focus}),
		expSynthesizeCase("ExcludeAspects", base().WithExcludeAspects(exclude), exp.SynthesizeOptions{ExcludeAspects: exclude}),
	}
	for _, c := range commonCases {
		old := base()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, expSynthesizeCase(c.name, old, exp.SynthesizeOptions{Options: c.v2}))
	}
	runParity(t, cases)
}
