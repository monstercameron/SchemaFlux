package exp_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleDecompose() {
	model := &schemafluxtest.Provider{}
	model.Reply(decomposeAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	result, err := exp.Decompose[decomposeTarget](ctx, decomposeTarget{Name: "input"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(result.Parts[0].Name)
	// Output: Root
}

func ExampleDecomposeToSlice() {
	model := &schemafluxtest.Provider{}
	model.Reply(`["first","second"]`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	parts, err := exp.DecomposeToSlice[string](ctx, decomposeTarget{Name: "input"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(len(parts))
	// Output: 2
}
