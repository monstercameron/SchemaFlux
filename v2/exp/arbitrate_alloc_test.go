package exp

import (
	"context"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

const arbitrateAllocationAnswer = `{"options":{"o1":{"r1":{"passed":true,"score":0.8,"reasoning":"good"}},"o2":{"r1":{"passed":true,"score":0.6,"reasoning":"less good"}}},"reasoning":"compared","confidence":0.7}`

func TestArbitrateAllocationsMatchTheInternalCall(t *testing.T) {
	p := &schemafluxtest.Provider{}
	p.Reply(arbitrateAllocationAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p})
	o := ArbitrateOptions{}
	c, err := arbitrateCommon(o)
	if err != nil {
		t.Fatal(err)
	}
	values := []string{"a", "b"}
	rules := []string{"good"}
	api := testing.AllocsPerRun(20, func() { _, _ = Arbitrate(ctx, values, rules) })
	internal := testing.AllocsPerRun(20, func() { in := arbitrateInternal(ctx, o, c); in.Rules = rules; _, _ = ops.Arbitrate(values, in) })
	t.Logf("with client: API %.2f internal %.2f", api, internal)
	internalResult, err := ops.Arbitrate(values, func() ops.ArbitrateOptions { in := arbitrateInternal(ctx, o, c); in.Rules = rules; return in }())
	if err != nil {
		t.Fatal(err)
	}
	conversion := testing.AllocsPerRun(20, func() { _ = arbitrateResult(internalResult) })
	commonCheck := testing.AllocsPerRun(20, func() { _, _ = arbitrateCommon(o) })
	// Three result allocations are the public slices for Evaluations and each
	// nonempty RuleResults list. The common-settings conversion adds one measured
	// allocation; keep both costs exact so conversion work stays visible.
	t.Logf("with client: API %.2f internal %.2f result conversion %.2f common check %.2f", api, internal, conversion, commonCheck)
	if api != internal+conversion+commonCheck {
		t.Errorf("with client: API %.2f internal %.2f plus result conversion %.2f and common check %.2f", api, internal, conversion, commonCheck)
	}

	defaultModel := &schemafluxtest.Provider{}
	defaultModel.Reply(arbitrateAllocationAnswer)
	schemafluxtest.Install(t, defaultModel)
	pinned, _ := ops.PinExec(context.Background())
	apiDefault := testing.AllocsPerRun(20, func() { _, _ = Arbitrate(context.Background(), values, rules) })
	internalDefault := testing.AllocsPerRun(20, func() { in := arbitrateInternal(pinned, o, c); in.Rules = rules; _, _ = ops.Arbitrate(values, in) })
	t.Logf("default client: API %.2f internal pinned %.2f plus conversion %.2f, common check %.2f and context lookup", apiDefault, internalDefault, conversion, commonCheck)
	if apiDefault != internalDefault+conversion+commonCheck+1 {
		t.Errorf("default client allocations: API %.2f internal pinned %.2f conversion %.2f common check %.2f; want one context lookup", apiDefault, internalDefault, conversion, commonCheck)
	}
}
