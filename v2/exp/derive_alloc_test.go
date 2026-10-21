package exp

import (
	"context"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"reflect"
	"testing"
)

type deriveAllocationTarget struct {
	Label string `json:"label"`
}

const deriveAllocationAnswer = `{"derived":{"label":"known"},"derivations":[],"field_confidence":{"label":0.9},"overall_confidence":0.9}`

func TestDeriveAllocationsMatchTheInternalCall(t *testing.T) {
	p := &schemafluxtest.Provider{}
	p.Reply(deriveAllocationAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p})
	o := DeriveOptions{}
	c, e := commonSettings(o.Options)
	if e != nil {
		t.Fatal(e)
	}
	input := map[string]string{"label": "known"}
	a := testing.AllocsPerRun(20, func() { _, _ = Derive[deriveAllocationTarget](ctx, input) })
	i := testing.AllocsPerRun(20, func() {
		_, _ = ops.Derive[map[string]string, deriveAllocationTarget](input, deriveInternal[map[string]string, deriveAllocationTarget](ctx, input, o, c))
	})
	t.Logf("API %.2f, internal %.2f", a, i)
	// Returning the distinct public DeriveResult escapes once, even with no derivation slice to convert.
	if a != i+1 {
		t.Errorf("allocations API %.2f, internal %.2f; want the one public result conversion allocation", a, i)
	}
	previous := schemaflux.Default()
	schemaflux.SetDefault(schemaflux.Client{Provider: p})
	t.Cleanup(func() { schemaflux.SetDefault(previous) })
	background := context.Background()
	pinned, _ := ops.PinExec(background)
	wrappedDefault := testing.AllocsPerRun(20, func() { _, _ = Derive[deriveAllocationTarget](background, input) })
	directPinned := testing.AllocsPerRun(20, func() {
		_, _ = ops.Derive[map[string]string, deriveAllocationTarget](input, deriveInternal[map[string]string, deriveAllocationTarget](pinned, input, o, c))
	})
	directDefault := testing.AllocsPerRun(20, func() {
		_, _ = ops.Derive[map[string]string, deriveAllocationTarget](input, deriveInternal[map[string]string, deriveAllocationTarget](background, input, o, c))
	})
	t.Logf("default client API %.2f, internal pinned %.2f, internal unpinned %.2f", wrappedDefault, directPinned, directDefault)
	// The default API pays the result conversion and the client pin; the unpinned internal call pays that pin itself.
	if wrappedDefault != directPinned+2 || wrappedDefault != directDefault+1 {
		t.Errorf("default allocations API %.2f, internal pinned %.2f, internal unpinned %.2f; want result conversion plus pin", wrappedDefault, directPinned, directDefault)
	}
}

func TestMergeDeriveOptionsCopiesEveryField(t *testing.T) {
	first := DeriveOptions{Options: schemaflux.Options{Model: "a", MaxOutputTokens: 20, WebSearch: true}, Fields: []string{"a"}, Rules: map[string]string{"a": "first"}, MinConfidence: .7, OmitReasoning: true}
	second := DeriveOptions{Options: schemaflux.Options{Model: "b", MaxOutputTokens: 30}, Fields: []string{"b"}, Rules: map[string]string{"b": "second"}, MinConfidence: .8}
	want := second
	want.Options.WebSearch = true
	want.OmitReasoning = true
	if got := mergeDeriveOptions([]DeriveOptions{first, second}); !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeDeriveOptions() = %#v, want %#v", got, want)
	}
	if got := mergeDeriveOptions(nil); !reflect.DeepEqual(got, DeriveOptions{}) {
		t.Fatalf("zero merge = %#v", got)
	}
	if got := mergeDeriveOptions([]DeriveOptions{first}); !reflect.DeepEqual(got, first) {
		t.Fatalf("single-value merge = %#v, want %#v", got, first)
	}
}
