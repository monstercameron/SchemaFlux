package exp_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type projectExampleInput struct {
	Name string `json:"name"`
}
type projectExampleOutput struct {
	Name string `json:"name"`
}

func ExampleProject() {
	p := &schemafluxtest.Provider{}
	p.Reply(`{"projected":{"name":"A"},"mappings":[],"lost":[],"inferred":[],"confidence":0.8}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p})
	got, err := exp.Project[projectExampleInput, projectExampleOutput](ctx, projectExampleInput{Name: "A"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(got.Projected.Name)
	// Output: A
}
