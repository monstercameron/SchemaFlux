package exp_test

import (
	"context"
	"fmt"

	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleEnrich() {
	model := &schemafluxtest.Provider{}
	model.Reply(enrichReply)
	schemafluxtest.Install(nil, model)
	result, err := exp.Enrich[enrichOutput](context.Background(), enrichInput{Name: "Ada", Email: "ada@example.test"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(result.Enriched.Segment)
	// Output: research
}

func ExampleEnrichInPlace() {
	model := &schemafluxtest.Provider{}
	model.Reply(`{"name":"Ada","email":"ada@example.test"}`)
	schemafluxtest.Install(nil, model)
	result, err := exp.EnrichInPlace(context.Background(), enrichInput{Name: "Ada", Email: "ada@example.test"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(result.Name)
	// Output: Ada
}
