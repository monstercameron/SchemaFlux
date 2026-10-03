package exp

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/ops"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type interpolateAllocationPoint struct {
	Value int `json:"value"`
}

const interpolateAllocationAnswer = `{"complete":[{"value":1},{"value":2}],"filled":[],"gap_count":0,"method":"auto","average_confidence":0}`

func TestInterpolateAllocationsMatchTheInternalCall(t *testing.T) {
	model := &schemafluxtest.Provider{}
	model.Reply(interpolateAllocationAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	o := InterpolateOptions{}
	c, err := interpolateCommon(o)
	if err != nil {
		t.Fatal(err)
	}
	items := []interpolateAllocationPoint{{1}, {2}}
	api := testing.AllocsPerRun(20, func() { _, _ = Interpolate[interpolateAllocationPoint](ctx, items) })
	internal := testing.AllocsPerRun(20, func() { _, _ = ops.Interpolate[interpolateAllocationPoint](items, interpolateInternal(ctx, o, c)) })
	t.Logf("with client: API %.2f, internal %.2f", api, internal)
	// The public result has a distinct FilledItem element type, so conversion
	// retains one allocation even when the model reports no filled entries.
	if api != internal+1 {
		t.Errorf("with client: API %.2f allocations, internal %.2f; want one result conversion allocation", api, internal)
	}
	defaultModel := &schemafluxtest.Provider{}
	defaultModel.Reply(interpolateAllocationAnswer)
	schemafluxtest.Install(t, defaultModel)
	pinned, _ := ops.PinExec(context.Background())
	apiDefault := testing.AllocsPerRun(20, func() { _, _ = Interpolate[interpolateAllocationPoint](context.Background(), items) })
	internalDefault := testing.AllocsPerRun(20, func() { _, _ = ops.Interpolate[interpolateAllocationPoint](items, interpolateInternal(pinned, o, c)) })
	t.Logf("default client: API %.2f, pinned internal %.2f", apiDefault, internalDefault)
	if apiDefault != internalDefault+2 {
		t.Errorf("default client: API %.2f allocations, pinned internal %.2f; want one result conversion and one context lookup", apiDefault, internalDefault)
	}
}
