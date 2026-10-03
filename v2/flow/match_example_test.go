package flow_test

import (
	"context"
	"fmt"

	"github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleMatch() {
	var model schemafluxtest.Provider
	model.Reply("false", "true")
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	matched, err := flow.Match(ctx, "My parcel never arrived.",
		flow.When("a billing question", func() { fmt.Println("billing") }),
		flow.When("a question about a delivery", func() { fmt.Println("shipping") }),
		flow.Otherwise(func() { fmt.Println("general") }))
	fmt.Println(matched, err)
	// Output:
	// shipping
	// true <nil>
}

// A condition that is not a string is compared in Go and asks no model.
func ExampleMatch_goConditions() {
	var model schemafluxtest.Provider
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	matched, err := flow.Match(ctx, 42,
		flow.When("", func() { fmt.Println("never: an empty condition says nothing") }),
		flow.When(0, func() { fmt.Println("an int") }))
	fmt.Println(matched, model.Calls(), err)
	// Output:
	// an int
	// true 0 <nil>
}
