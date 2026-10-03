package parity

import (
	"context"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/v2"
)

const similarReply = `{"score":0.8,"explanation":"same","aspects":{"a1":{"score":0.9,"reason":"match"},"a2":{"score":0.4,"reason":"differs"}}}`

func similarParityCase(name string, old oldroot.SimilarOptions, current schemaflux.SimilarOptions) parityCase {
	return parityCase{name: "Similar/" + name, op: "similar", replies: []string{similarReply}, floor: schemaflux.ContractSchemaConstrained,
		old: func(ctx context.Context) error {
			old.OpOptions.Context = ctx
			_, err := oldroot.Similar("alpha", "beta", old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.Similar(ctx, "alpha", "beta", current)
			return err
		},
	}
}

func TestSimilarSendsTheRequestTheOldRootSends(t *testing.T) {
	base := oldroot.NewSimilarOptions()
	one := []string{"meaning"}
	two := []string{"meaning", "tone"}
	spaced := []string{"  Meaning ", "tone"}
	cases := []parityCase{
		similarParityCase("default", base, schemaflux.SimilarOptions{}),
		similarParityCase("Threshold", base.WithSimilarityThreshold(.8), schemaflux.SimilarOptions{Threshold: .8}),
		similarParityCase("a threshold of one", base.WithSimilarityThreshold(1), schemaflux.SimilarOptions{Threshold: 1}),
		similarParityCase("a negative threshold is zero", base.WithSimilarityThreshold(0), schemaflux.SimilarOptions{Threshold: -1}),
		similarParityCase("one aspect", base.WithAspects(one), schemaflux.SimilarOptions{Aspects: one}),
		similarParityCase("two aspects", base.WithAspects(two), schemaflux.SimilarOptions{Aspects: two}),
		similarParityCase("aspects with space and capitals", base.WithAspects(spaced), schemaflux.SimilarOptions{Aspects: spaced}),
		similarParityCase("a threshold and aspects", base.WithSimilarityThreshold(.3).WithAspects(two), schemaflux.SimilarOptions{Threshold: .3, Aspects: two}),
	}
	for _, common := range commonCases {
		old := oldroot.NewSimilarOptions()
		common.decision(&old.OpOptions)
		cases = append(cases, similarParityCase(common.name, old, schemaflux.SimilarOptions{Options: common.v2}))
	}
	runParity(t, cases)
}
