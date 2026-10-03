package parity

import (
	"context"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type decomposeParityTarget struct {
	Name string `json:"name"`
}

const expDecomposeAnswer = `{"parts":[{"id":"root","name":"Root","content":{"name":"input"},"depth":1,"order":0}],"root_parts":["root"]}`

func expDecomposeCase(name string, old oldroot.DecomposeOptions, v2 exp.DecomposeOptions) parityCase {
	in := decomposeParityTarget{Name: "input"}
	return parityCase{name: "Decompose/" + name, op: "exp.decompose", replies: []string{expDecomposeAnswer}, floor: schemaflux.ContractSchemaConstrained,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.Decompose(in, old)
			return err
		},
		v2: func(ctx context.Context) error { _, err := exp.Decompose(ctx, in, v2); return err }}
}

func TestExpDecomposeSendsTheRequestTheOldRootSends(t *testing.T) {
	base := oldroot.NewDecomposeOptions
	cases := []parityCase{
		expDecomposeCase("default", base(), exp.DecomposeOptions{}),
		expDecomposeCase("Strategy", base().WithStrategy("sequential"), exp.DecomposeOptions{Strategy: "sequential"}),
		expDecomposeCase("By", base().WithDecomposeBy("chapters"), exp.DecomposeOptions{By: "chapters"}),
		expDecomposeCase("MaxDepth", base().WithMaxDepth(2), exp.DecomposeOptions{MaxDepth: 2}),
		expDecomposeCase("MinGranularity", base().WithMinGranularity("paragraph"), exp.DecomposeOptions{MinGranularity: "paragraph"}),
		expDecomposeCase("TargetParts", base().WithTargetParts(4), exp.DecomposeOptions{TargetParts: 4}),
		expDecomposeCase("OmitDependencies", base().WithIncludeDependencies(false), exp.DecomposeOptions{OmitDependencies: true}),
		expDecomposeCase("NoHierarchy", base().WithPreserveHierarchy(false), exp.DecomposeOptions{NoHierarchy: true}),
		expDecomposeCase("IncludeEstimates", base().WithIncludeEstimates(true), exp.DecomposeOptions{IncludeEstimates: true}),
	}
	for _, c := range commonCases {
		old := base()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, expDecomposeCase(c.name, old, exp.DecomposeOptions{Options: c.v2}))
	}
	runParity(t, cases)
}

func expDecomposeToSliceCase(name string, old oldroot.DecomposeOptions, v2 exp.DecomposeToSliceOptions) parityCase {
	in := decomposeParityTarget{Name: "input"}
	return parityCase{name: "DecomposeToSlice/" + name, op: "exp.decomposetoslice", replies: []string{`[1,2]`}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.DecomposeToSlice[decomposeParityTarget, int](in, old)
			return err
		},
		v2: func(ctx context.Context) error { _, err := exp.DecomposeToSlice[int](ctx, in, v2); return err },
	}
}

func TestExpDecomposeToSliceSendsTheRequestTheOldRootSends(t *testing.T) {
	base := oldroot.NewDecomposeOptions
	cases := []parityCase{
		expDecomposeToSliceCase("default", base(), exp.DecomposeToSliceOptions{}),
		expDecomposeToSliceCase("Strategy", base().WithStrategy("parallel"), exp.DecomposeToSliceOptions{Strategy: "parallel"}),
		expDecomposeToSliceCase("TargetParts", base().WithTargetParts(3), exp.DecomposeToSliceOptions{TargetParts: 3}),
	}
	for _, c := range commonCases {
		old := base()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, expDecomposeToSliceCase(c.name, old, exp.DecomposeToSliceOptions{Options: c.v2}))
	}
	runParity(t, cases)
}
