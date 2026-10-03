package parity

import (
	"context"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

const deduplicateParityAnswer = `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.95}]}`

func deduplicateParityCase(name string, items []string, threshold float64, old types.OpOptions, v2 schemaflux.Options) parityCase {
	return parityCase{
		name: "Deduplicate/" + name, op: "deduplicate", replies: []string{deduplicateParityAnswer}, floor: schemaflux.ContractSchemaAndInvariantChecked,
		old: func(ctx context.Context) error {
			old.Context = ctx
			_, err := oldroot.Deduplicate(items, threshold, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.Deduplicate(ctx, items, threshold, v2)
			return err
		},
	}
}

func TestDeduplicateSendsTheRequestTheOldRootSends(t *testing.T) {
	two := []string{"one", "two"}
	cases := []parityCase{
		deduplicateParityCase("the default", two, .9, types.OpOptions{}, schemaflux.Options{}),
		deduplicateParityCase("a threshold of zero", two, 0, types.OpOptions{}, schemaflux.Options{}),
		deduplicateParityCase("a threshold of one", two, 1, types.OpOptions{}, schemaflux.Options{}),
		deduplicateParityCase("four items", []string{"one", "two", "three", "four"}, .8, types.OpOptions{}, schemaflux.Options{}),
		deduplicateParityCase("a repeated item is not sent", []string{"one", "two", "one", "three"}, .8, types.OpOptions{}, schemaflux.Options{}),
		deduplicateParityCase("a number among the items", []string{"order 12", "twelve"}, .8, types.OpOptions{}, schemaflux.Options{}),
	}
	for _, c := range commonCases {
		var old types.OpOptions
		c.decision(&old)
		cases = append(cases, deduplicateParityCase(c.name, two, .9, old, c.v2))
	}
	runParity(t, cases)
}
