package exp_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExamplePredict() {
	p := &schemafluxtest.Provider{}
	p.Reply(`{"prediction":{"value":42.5},"confidence":0.7,"interval":{"lower":40,"upper":45,"confidence_level":0.8},"scenarios":[],"factors":[],"reasoning":"trend extrapolation","assumptions":[],"risks":[]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p})
	got, err := exp.Predict[predictOutput](ctx, []float64{35, 38, 41})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(got.Prediction.Value, got.ModelConfidence)
	// Output: 42.5 0.7
}
