package exp_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleSynthesize() {
	model := &schemafluxtest.Provider{}
	model.Reply(synthesisAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	result, err := exp.Synthesize[synthesisTarget](ctx, []any{"report A", "report B"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(result.Synthesized.Summary)
	// Output: combined
}
