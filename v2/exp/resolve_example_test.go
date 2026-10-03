package exp_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleResolve() {
	model := &schemafluxtest.Provider{}
	model.Reply(resolveAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	result, err := exp.Resolve[resolveTarget](ctx, []resolveTarget{{Name: "A"}, {Name: "B"}})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(result.Resolved.Name, result.ModelAsked)
	// Output: merged true
}
