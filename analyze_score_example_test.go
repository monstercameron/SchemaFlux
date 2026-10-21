package schemaflux_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleScore() {
	var model schemafluxtest.Provider
	model.Reply(`{"value":4.5,"breakdown":{"clarity":4.5,"evidence":4},"reasoning":"clear and direct","strengths":["direct"],"weaknesses":[]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	res, err := schemaflux.Score(ctx, "A clear short essay.",
		schemaflux.ScoreOptions{Criteria: []string{"clarity", "evidence"}, ScaleMax: 5})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(res.Value, res.NormalizedValue, res.Breakdown["clarity"])
	fmt.Println(res.Reasoning)
	// Output:
	// 4.5 0.9 4.5
	// clear and direct
}
