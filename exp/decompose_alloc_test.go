package exp

import (
	"context"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type decomposeAllocationTarget struct {
	Name string `json:"name"`
}

const decomposeAllocationAnswer = `{"parts":[{"id":"root","name":"Root","content":{"name":"root"},"depth":1,"order":0}],"root_parts":["root"]}`

func TestDecomposeAllocationsMatchInternalCall(t *testing.T) {
	model := &schemafluxtest.Provider{}
	model.Reply(decomposeAllocationAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	o := DecomposeOptions{}
	c, err := decomposeCommon(o.Options, "exp.decompose")
	if err != nil {
		t.Fatal(err)
	}
	input := decomposeAllocationTarget{Name: "x"}
	api := testing.AllocsPerRun(20, func() { _, _ = Decompose(ctx, input) })
	in := testing.AllocsPerRun(20, func() { _, _ = ops.Decompose(input, decomposeInternal(ctx, o, c)) })
	t.Logf("with client: API %.2f internal %.2f", api, in)
	// The public part/result types omit Metadata, so conversion currently needs
	// a part slice and a distinct result value (two allocations).
	if api-in != 2 {
		t.Errorf("with client allocation delta = %.2f, want measured conversion delta 2", api-in)
	}
	defaultModel := &schemafluxtest.Provider{}
	defaultModel.Reply(decomposeAllocationAnswer)
	schemafluxtest.Install(t, defaultModel)
	pinned, _ := ops.PinExec(context.Background())
	apiDefault := testing.AllocsPerRun(20, func() { _, _ = Decompose[decomposeAllocationTarget](context.Background(), input) })
	internalDefault := testing.AllocsPerRun(20, func() { _, _ = ops.Decompose(input, decomposeInternal(pinned, o, c)) })
	t.Logf("default client: API %.2f internal pinned %.2f", apiDefault, internalDefault)
	if apiDefault-internalDefault != 3 {
		t.Errorf("default allocation delta = %.2f, want measured conversion plus context lookup delta 3", apiDefault-internalDefault)
	}
}

func TestDecomposeToSliceAllocationsMatchInternalCall(t *testing.T) {
	const answer = `[1,2]`
	model := &schemafluxtest.Provider{}
	model.Reply(answer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	o := DecomposeToSliceOptions{}
	c, err := decomposeCommon(o.Options, "exp.decomposetoslice")
	if err != nil {
		t.Fatal(err)
	}
	input := decomposeAllocationTarget{Name: "x"}
	api := testing.AllocsPerRun(20, func() { _, _ = DecomposeToSlice[int](ctx, input) })
	in := testing.AllocsPerRun(20, func() {
		_, _ = ops.DecomposeToSlice[decomposeAllocationTarget, int](input, decomposeSliceInternal(ctx, o, c))
	})
	t.Logf("slice with client: API %.2f internal %.2f", api, in)
	if api-in != 1 {
		t.Errorf("slice with client allocation delta = %.2f, want measured output slice conversion delta 1", api-in)
	}
	defaultModel := &schemafluxtest.Provider{}
	defaultModel.Reply(answer)
	schemafluxtest.Install(t, defaultModel)
	pinned, _ := ops.PinExec(context.Background())
	apiDefault := testing.AllocsPerRun(20, func() { _, _ = DecomposeToSlice[int](context.Background(), input) })
	internalDefault := testing.AllocsPerRun(20, func() {
		_, _ = ops.DecomposeToSlice[decomposeAllocationTarget, int](input, decomposeSliceInternal(pinned, o, c))
	})
	t.Logf("slice default client: API %.2f internal pinned %.2f", apiDefault, internalDefault)
	if apiDefault-internalDefault != 2 {
		t.Errorf("slice default allocation delta = %.2f, want output slice conversion plus context lookup delta 2", apiDefault-internalDefault)
	}
}
