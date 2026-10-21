package exp

import (
	"context"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type pivotAllocationRow struct {
	Name string `json:"name"`
}

const pivotAllocationAnswer = `{"pivoted":{"name":"Ada"},"mappings":[],"stats":{"source_fields":1,"target_fields":1,"expansions":0,"compressions":0,"depth_change":0},"data_loss":[]}`

func TestPivotAllocationsStayCloseToTheInternalCall(t *testing.T) {
	model := &schemafluxtest.Provider{}
	model.Reply(pivotAllocationAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	o := PivotOptions{}
	c, err := commonSettings(o.Options)
	if err != nil {
		t.Fatal(err)
	}
	input := pivotAllocationRow{Name: "Ada"}
	api := testing.AllocsPerRun(20, func() { _, _ = Pivot[pivotAllocationRow, pivotAllocationRow](ctx, input) })
	internal := testing.AllocsPerRun(20, func() {
		_, _ = ops.Pivot[pivotAllocationRow, pivotAllocationRow](input, pivotInternal(ctx, o, c))
	})
	t.Logf("pivot allocations with client: API %.2f, internal %.2f", api, internal)
	if api != internal+1 {
		t.Errorf("with client: API %.2f allocations, internal %.2f; want one public result conversion", api, internal)
	}

	defaultModel := &schemafluxtest.Provider{}
	defaultModel.Reply(pivotAllocationAnswer)
	schemafluxtest.Install(t, defaultModel)
	pinned, _ := ops.PinExec(context.Background())
	apiDefault := testing.AllocsPerRun(20, func() { _, _ = Pivot[pivotAllocationRow, pivotAllocationRow](context.Background(), input) })
	internalDefault := testing.AllocsPerRun(20, func() {
		_, _ = ops.Pivot[pivotAllocationRow, pivotAllocationRow](input, pivotInternal(pinned, o, c))
	})
	t.Logf("pivot allocations on default client: API %.2f, internal %.2f", apiDefault, internalDefault)
	if apiDefault != internalDefault+2 {
		t.Errorf("default client: API %.2f allocations, internal %.2f; want conversion and default-client lookup", apiDefault, internalDefault)
	}
}
