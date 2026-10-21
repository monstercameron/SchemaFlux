package exp_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleConform() {
	type address struct {
		Street string `json:"street"`
	}
	p := &schemafluxtest.Provider{}
	p.Reply(`{"conformed":{"street":"10 MAIN ST"},"adjustments":[],"violations":[],"compliance":1,"standard":"USPS"}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p})
	result, err := exp.Conform[address](ctx, address{Street: "10 Main St"}, "USPS")
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Conformed.Street)
	// Output: 10 MAIN ST
}
