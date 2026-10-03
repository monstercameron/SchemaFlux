package exp

import (
	"context"
	"reflect"
	"testing"

	"github.com/monstercameron/schemaflux/internal/ops"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func TestResolveOptionMergeCopiesEveryField(t *testing.T) {
	first := ResolveOptions{
		Options:  schemaflux.Options{Model: "first", Tier: schemaflux.TierSmart, Effort: schemaflux.EffortLow, Mode: schemaflux.ModeStrict, Steering: "first steering", MaxOutputTokens: 41, WebSearch: true},
		Strategy: "merge", AuthoritativeSource: 1, FieldPriorities: map[string]int{"name": 0}, ConflictThreshold: 0.3,
	}
	later := ResolveOptions{
		Options:  schemaflux.Options{Model: "second", Tier: schemaflux.TierQuick, Effort: schemaflux.EffortHigh, Mode: schemaflux.ModeCreative, Steering: "later steering", MaxOutputTokens: 73},
		Strategy: "newest", AuthoritativeSource: 2, FieldPriorities: map[string]int{"age": 1}, ConflictThreshold: 0.8,
	}
	want := ResolveOptions{
		Options:  schemaflux.Options{Model: "second", Tier: schemaflux.TierQuick, Effort: schemaflux.EffortHigh, Mode: schemaflux.ModeCreative, Steering: "later steering", MaxOutputTokens: 73, WebSearch: true},
		Strategy: "newest", AuthoritativeSource: 2, FieldPriorities: map[string]int{"age": 1}, ConflictThreshold: 0.8,
	}
	if got := mergeResolveOptions([]ResolveOptions{first, later}); !reflect.DeepEqual(got, want) {
		t.Fatalf("merged options = %+v, want %+v", got, want)
	}
}

type resolveAllocationTarget struct {
	Name string `json:"name"`
}

const resolveAllocationAnswer = `{"resolved":{"name":"merged"},"conflicts":[],"source_contributions":{"0":["name"],"1":[]},"confidence":0.75}`

func TestResolveAllocationsMatchTheInternalCall(t *testing.T) {
	model := &schemafluxtest.Provider{}
	model.Reply(resolveAllocationAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	o := ResolveOptions{}
	c, err := commonSettings(o.Options)
	if err != nil {
		t.Fatal(err)
	}
	sources := []resolveAllocationTarget{{"a"}, {"b"}}
	api := testing.AllocsPerRun(20, func() { _, _ = Resolve[resolveAllocationTarget](ctx, sources) })
	in := testing.AllocsPerRun(20, func() { _, _ = ops.Resolve[resolveAllocationTarget](sources, resolveInternal(ctx, o, c)) })
	t.Logf("allocations with client: API %.2f, internal %.2f", api, in)
	if api != in+1 {
		t.Errorf("with client: API %.2f allocations, internal %.2f; want one result conversion allocation", api, in)
	}

	defaultModel := &schemafluxtest.Provider{}
	defaultModel.Reply(resolveAllocationAnswer)
	schemafluxtest.Install(t, defaultModel)
	pinned, _ := ops.PinExec(context.Background())
	apiDefault := testing.AllocsPerRun(20, func() { _, _ = Resolve[resolveAllocationTarget](context.Background(), sources) })
	inDefault := testing.AllocsPerRun(20, func() { _, _ = ops.Resolve[resolveAllocationTarget](sources, resolveInternal(pinned, o, c)) })
	t.Logf("allocations on default client: API %.2f, internal %.2f", apiDefault, inDefault)
	if apiDefault != inDefault+2 {
		t.Errorf("default client: API %.2f allocations, internal %.2f; want two additional allocations", apiDefault, inDefault)
	}
}
