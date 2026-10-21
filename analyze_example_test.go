package schemaflux_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// Classify puts a value in one of the categories it is given, and never in
// another. The model is scripted here, so the example runs offline.
func ExampleClassify() {
	var model schemafluxtest.Provider
	model.Reply(`{"category":"negative","confidence":0.92,"alternatives":[{"category":"neutral","confidence":0.05}],"reasoning":"the review complains"}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	res, err := schemaflux.Classify(ctx, "It broke after a day.", []string{"positive", "negative", "neutral"})
	fmt.Println(res.Category, res.ModelConfidence, err)
	// Output: negative 0.92 <nil>
}

// An answer the model is less sure of than MinConfidence is an error, not a
// guess.
func ExampleClassify_minConfidence() {
	var model schemafluxtest.Provider
	model.Reply(`{"category":"negative","confidence":0.6,"alternatives":[],"reasoning":"unclear"}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	_, err := schemaflux.Classify(ctx, "It is fine, I suppose.", []string{"positive", "negative"}, schemaflux.ClassifyOptions{MinConfidence: 0.8})
	fmt.Println(err)
	// Output: classify: invalid answer: invariant violation: the model reported confidence 0.60, below the 0.80 floor this operation was configured with
}
