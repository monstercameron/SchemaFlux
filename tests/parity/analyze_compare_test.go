package parity

import (
	"context"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// compareParityPlan is the kind of value Compare is given.
type compareParityPlan struct {
	Name  string  `json:"name"`
	Price float64 `json:"price"`
}

// compareParityAnswer has a likeness, a difference, a verdict and a score for
// the two aspects the cases name, so that it is good for a call that names them
// and for one that names none.
const compareParityAnswer = `{"similarity_score":0.8,"similarities":[{"aspect":"goal","description":"both cut cost"}],` +
	`"differences":[{"aspect":"price","description":"B costs more","severity":"major"}],` +
	`"verdict":"alike in goal","aspect_scores":{"price":0.4,"risk":0.6}}`

// compareCase is one Compare pair: the old root's options and v2's.
func compareCase(name string, old oldroot.CompareOptions, v2 schemaflux.CompareOptions) parityCase {
	a, b := compareParityPlan{"basic", 10}, compareParityPlan{"pro", 25}
	return parityCase{name: "Compare/" + name, op: "compare", replies: []string{compareParityAnswer}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.Compare(a, b, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.Compare(ctx, a, b, v2)
			return err
		}}
}

func compareCases() []parityCase {
	base := oldroot.NewCompareOptions
	cases := []parityCase{
		compareCase("the default", base(), schemaflux.CompareOptions{}),
		compareCase("Aspects", base().WithComparisonAspects([]string{"price"}), schemaflux.CompareOptions{Aspects: []string{"price"}}),
		compareCase("Aspects, several", base().WithComparisonAspects([]string{"price", "risk"}), schemaflux.CompareOptions{Aspects: []string{"price", "risk"}}),
		// An empty list that is not nil is a value in both roots, and is written as [] and not null.
		compareCase("Aspects, empty", base().WithComparisonAspects([]string{}), schemaflux.CompareOptions{Aspects: []string{}}),
		compareCase("FocusOn differences", base().WithFocusOn("differences"), schemaflux.CompareOptions{FocusOn: "differences"}),
		compareCase("FocusOn similarities", base().WithFocusOn("similarities"), schemaflux.CompareOptions{FocusOn: "similarities"}),
		// The default focus, written out: zero in v2 and "both" in the old root.
		compareCase("FocusOn both", base().WithFocusOn("both"), schemaflux.CompareOptions{FocusOn: "both"}),
		compareCase("Depth 1", withCompareDepth(base(), 1), schemaflux.CompareOptions{Depth: 1}),
		compareCase("Depth 5, the default written out", withCompareDepth(base(), 5), schemaflux.CompareOptions{Depth: 5}),
		compareCase("Depth 10", withCompareDepth(base(), 10), schemaflux.CompareOptions{Depth: 10}),
		compareCase("OmitSimilarity", withCompareSimilarity(base(), false), schemaflux.CompareOptions{OmitSimilarity: true}),
		compareCase("every own setting",
			withCompareSimilarity(withCompareDepth(base().WithComparisonAspects([]string{"price", "risk"}).WithFocusOn("differences"), 8), false),
			schemaflux.CompareOptions{Aspects: []string{"price", "risk"}, FocusOn: "differences", Depth: 8, OmitSimilarity: true}),
	}
	for _, c := range commonCases {
		old := base()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, compareCase(c.name, old, schemaflux.CompareOptions{Options: c.v2}))
	}
	return cases
}

func withCompareDepth(o oldroot.CompareOptions, depth int) oldroot.CompareOptions {
	o.Depth = depth
	return o
}

func withCompareSimilarity(o oldroot.CompareOptions, include bool) oldroot.CompareOptions {
	o.IncludeSimilarity = include
	return o
}

func TestCompareSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, compareCases()) }
