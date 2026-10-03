package exp_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleInterpolate() {
	model := &schemafluxtest.Provider{}
	model.Reply(interpolateAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	result, err := exp.Interpolate[interpolationPoint](ctx, []interpolationPoint{{1}, {0}, {3}}, exp.InterpolateOptions{GapIndices: []int{1}})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(result.Complete[1].Value)
	// Output: 2
}
