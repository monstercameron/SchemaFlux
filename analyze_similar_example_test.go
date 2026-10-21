package schemaflux_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleSimilar() {
	var model schemafluxtest.Provider
	model.Reply(`{"score":0.86,"explanation":"both ask how to reset a password","aspects":{"a1":{"score":0.95,"reason":"the same question"}}}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	res, err := schemaflux.Similar(ctx, "How do I reset my password?", "I forgot my password, what now?",
		schemaflux.SimilarOptions{Threshold: 0.8, Aspects: []string{"intent"}})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(res.IsSimilar, res.Score, res.Threshold)
	fmt.Println(res.MatchedAspects[0].Aspect, res.MatchedAspects[0].Score)
	// Output:
	// true 0.86 0.8
	// intent 0.95
}
