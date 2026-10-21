package schemaflux_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleDeduplicate() {
	var model schemafluxtest.Provider
	model.Reply(`{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.96}]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	leads := []string{"ann@work.test", "Ann at work", "bob@home.test"}
	res, err := schemaflux.Deduplicate(ctx, leads, 0.9)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(res.Unique, res.TotalRemoved)
	fmt.Println(res.Duplicates, res.DuplicateIndices)
	// Output:
	// [ann@work.test bob@home.test] 1
	// [[ann@work.test Ann at work]] [[0 1]]
}
