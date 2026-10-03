package exp_test

import (
	"context"
	"fmt"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleDerive() {
	p := &schemafluxtest.Provider{}
	p.Reply(deriveReply)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p})
	r, e := exp.Derive[deriveTarget](ctx, map[string]string{"label": "known"})
	if e != nil {
		fmt.Println("error")
		return
	}
	fmt.Println(r.Derived.Label)
	// Output: known
}
