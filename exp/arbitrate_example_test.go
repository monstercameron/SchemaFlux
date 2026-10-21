package exp_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleArbitrate() {
	p := &schemafluxtest.Provider{}
	p.Reply(arbitrateAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p})
	result, err := exp.Arbitrate(ctx, []string{"alpha", "beta"}, []string{"meets the requirement"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(result.Winner)
	// Output: alpha
}
