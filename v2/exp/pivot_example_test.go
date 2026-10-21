package exp_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExamplePivot() {
	p := &schemafluxtest.Provider{}
	p.Reply(`{"pivoted":{"name":"Ada"},"mappings":[],"stats":{"source_fields":2,"target_fields":1,"expansions":0,"compressions":1,"depth_change":0},"data_loss":[]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p})
	got, err := exp.Pivot[pivotInput, pivotOutput](ctx, pivotInput{Name: "Ada", Score: 5})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(got.Pivoted.Name, got.DataLoss)
	// Output: Ada [score]
}
