package exp

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/ops"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type conformAllocationTarget struct {
	Name string `json:"name"`
}

const conformAllocationAnswer = `{"conformed":{"name":"A"},"adjustments":[],"violations":[],"compliance":1,"standard":"USPS"}`

func TestConformAllocationsMatchTheInternalCall(t *testing.T) {
	p := &schemafluxtest.Provider{}
	p.Reply(conformAllocationAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p})
	o := ConformOptions{}
	c, err := conformCommon(o)
	if err != nil {
		t.Fatal(err)
	}
	api := testing.AllocsPerRun(20, func() { _, _ = Conform[conformAllocationTarget](ctx, conformAllocationTarget{}, "USPS") })
	in := testing.AllocsPerRun(20, func() {
		_, _ = ops.Conform[conformAllocationTarget](conformAllocationTarget{}, "USPS", conformInternal(ctx, o, c))
	})
	t.Logf("allocations with client: API %.2f, internal %.2f", api, in)
	if api != in+1 {
		t.Errorf("with client: API %.2f internal %.2f; want one public result conversion allocation", api, in)
	}
	p2 := &schemafluxtest.Provider{}
	p2.Reply(conformAllocationAnswer)
	schemafluxtest.Install(t, p2)
	pinned, _ := ops.PinExec(context.Background())
	apiDefault := testing.AllocsPerRun(20, func() {
		_, _ = Conform[conformAllocationTarget](context.Background(), conformAllocationTarget{}, "USPS")
	})
	inDefault := testing.AllocsPerRun(20, func() {
		_, _ = ops.Conform[conformAllocationTarget](conformAllocationTarget{}, "USPS", conformInternal(pinned, o, c))
	})
	t.Logf("allocations on default client: API %.2f, internal pinned %.2f", apiDefault, inDefault)
	if apiDefault != inDefault+2 {
		t.Errorf("default: API %.2f internal %.2f; want conversion and client context lookup", apiDefault, inDefault)
	}
}
