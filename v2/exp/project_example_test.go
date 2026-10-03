package exp_test

import (
	"context"
	"fmt"

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
	got, err := exp.Project[projectExampleInput, projectExampleOutput](context.Background(), projectExampleInput{Name: "A"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(got.Projected.Name)
	// Output: A
}
