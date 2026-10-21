package exp

import (
	"context"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

const predictAllocationAnswer = `{"prediction":3,"confidence":0.7,"interval":{"lower":2,"upper":4,"confidence_level":0.8},"scenarios":[],"factors":[],"reasoning":"trend","assumptions":[],"risks":[]}`

func TestPredictAllocationsStayCloseToTheInternalCall(t *testing.T) {
	model := &schemafluxtest.Provider{}
	model.Reply(predictAllocationAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	o := PredictOptions{}
	c, err := commonSettings(o.Options)
	if err != nil {
		t.Fatal(err)
	}
	history := []int{1, 2}
	api := testing.AllocsPerRun(20, func() { _, _ = Predict[int](ctx, history) })
	internal := testing.AllocsPerRun(20, func() { _, _ = ops.Predict[int](history, predictInternal(ctx, o, c)) })
	t.Logf("predict allocations with client: API %.2f, internal %.2f", api, internal)
	if api != internal+2 {
		t.Errorf("with client: API %.2f allocations, internal %.2f; want interval and public result conversion", api, internal)
	}

	defaultModel := &schemafluxtest.Provider{}
	defaultModel.Reply(predictAllocationAnswer)
	schemafluxtest.Install(t, defaultModel)
	pinned, _ := ops.PinExec(context.Background())
	apiDefault := testing.AllocsPerRun(20, func() { _, _ = Predict[int](context.Background(), history) })
	internalDefault := testing.AllocsPerRun(20, func() { _, _ = ops.Predict[int](history, predictInternal(pinned, o, c)) })
	t.Logf("predict allocations on default client: API %.2f, internal %.2f", apiDefault, internalDefault)
	if apiDefault != internalDefault+3 {
		t.Errorf("default client: API %.2f allocations, internal %.2f; want interval, conversion and default-client lookup", apiDefault, internalDefault)
	}
}
