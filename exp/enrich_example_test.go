package exp_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleEnrich() {
	model := &schemafluxtest.Provider{}
	model.Reply(enrichReply)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	result, err := exp.Enrich[enrichOutput](ctx, enrichInput{Name: "Ada", Email: "ada@example.test"})
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
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	result, err := exp.EnrichInPlace(ctx, enrichInput{Name: "Ada", Email: "ada@example.test"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(result.Name)
	// Output: Ada
}
