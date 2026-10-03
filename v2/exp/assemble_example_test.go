package exp_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleAssemble() {
	model := &schemafluxtest.Provider{}
	model.Reply(assembleTestAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	result, err := exp.Assemble[assembleTestValue](ctx, []any{"name: Ada"})
	if err != nil {
		fmt.Println("error")
		return
	}
	fmt.Println(result.Composed.Name)
	// Output: Ada
}
