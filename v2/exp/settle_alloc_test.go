package exp

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/ops"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type settleAllocationTarget struct {
	Answer string `json:"answer"`
}

const settleAllocationAnswer = `{"solution":{"answer":"ok"},"satisfaction":{},"overall_satisfaction":0.8,"tradeoffs":[],"alternatives":[],"reasoning":"","confidence":0.9}`

func TestSettleAllocationsMatchTheInternalCall(t *testing.T) {
	p := &schemafluxtest.Provider{}
	p.Reply(settleAllocationAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p})
	o := SettleOptions{}
	c, err := settleCommon("exp.settle", o.Options)
	if err != nil {
		t.Fatal(err)
	}
	input := "constraint"
	api := testing.AllocsPerRun(20, func() { _, _ = Settle[settleAllocationTarget](ctx, input) })
	internal := testing.AllocsPerRun(20, func() {
		_, _ = ops.Settle[settleAllocationTarget](input, settleInternal[settleAllocationTarget](ctx, o, c))
	})
	t.Logf("allocations: API %.2f internal %.2f", api, internal)
	if api != internal+1 {
		t.Errorf("API %.2f internal %.2f", api, internal)
	}
	defaultProvider := &schemafluxtest.Provider{}
	defaultProvider.Reply(settleAllocationAnswer)
	schemafluxtest.Install(t, defaultProvider)
	pinned, _ := ops.PinExec(context.Background())
	apiDefault := testing.AllocsPerRun(20, func() { _, _ = Settle[settleAllocationTarget](context.Background(), input) })
	internalDefault := testing.AllocsPerRun(20, func() {
		_, _ = ops.Settle[settleAllocationTarget](input, settleInternal[settleAllocationTarget](pinned, o, c))
	})
	t.Logf("default client allocations: API %.2f internal pinned %.2f", apiDefault, internalDefault)
	if apiDefault != internalDefault+2 {
		t.Errorf("default client: API %.2f internal pinned %.2f; want two additional allocations", apiDefault, internalDefault)
	}
}

func TestSettleAdversarialAllocationsMatchTheInternalCall(t *testing.T) {
	p := &schemafluxtest.Provider{}
	p.Reply(`{"deal":{"answer":"ok"},"deal_reached":true,"term_movements":[],"who_conceded_more":"neither","our_satisfaction":0.8,"their_satisfaction":0.7,"reasoning":"","confidence":0.9}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p})
	o := SettleAdversarialOptions{}
	c, err := settleCommon("exp.settleadversarial", o.Options)
	if err != nil {
		t.Fatal(err)
	}
	parties := AdversarialContext[settleAllocationTarget]{
		Ours:   AdversarialPosition[settleAllocationTarget]{Position: settleAllocationTarget{Answer: "ours"}},
		Theirs: AdversarialPosition[settleAllocationTarget]{Position: settleAllocationTarget{Answer: "theirs"}},
	}
	internalParties := ops.AdversarialContext[settleAllocationTarget]{
		Ours:   ops.AdversarialPosition[settleAllocationTarget]{Position: parties.Ours.Position},
		Theirs: ops.AdversarialPosition[settleAllocationTarget]{Position: parties.Theirs.Position},
	}
	api := testing.AllocsPerRun(20, func() { _, _ = SettleAdversarial(ctx, parties) })
	internal := testing.AllocsPerRun(20, func() {
		_, _ = ops.SettleAdversarial(internalParties, settleAdversarialInternal(ctx, o, c))
	})
	t.Logf("allocations: API %.2f internal %.2f", api, internal)
	if api != internal+1 {
		t.Errorf("API %.2f internal %.2f; want one additional public result allocation", api, internal)
	}
	defaultProvider := &schemafluxtest.Provider{}
	defaultProvider.Reply(`{"deal":{"answer":"ok"},"deal_reached":true,"term_movements":[],"who_conceded_more":"neither","our_satisfaction":0.8,"their_satisfaction":0.7,"reasoning":"","confidence":0.9}`)
	schemafluxtest.Install(t, defaultProvider)
	pinned, _ := ops.PinExec(context.Background())
	apiDefault := testing.AllocsPerRun(20, func() { _, _ = SettleAdversarial(context.Background(), parties) })
	internalDefault := testing.AllocsPerRun(20, func() {
		_, _ = ops.SettleAdversarial(internalParties, settleAdversarialInternal(pinned, o, c))
	})
	t.Logf("default client allocations: API %.2f internal pinned %.2f", apiDefault, internalDefault)
	if apiDefault != internalDefault+2 {
		t.Errorf("default client: API %.2f internal pinned %.2f; want two additional allocations", apiDefault, internalDefault)
	}
}
