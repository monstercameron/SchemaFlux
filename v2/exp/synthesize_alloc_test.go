package exp

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/ops"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type synthesisAllocationTarget struct {
	Summary string `json:"summary"`
}

const synthesisAllocationAnswer = `{"synthesized":{"summary":"combined"},"facts":[],"insights":[],"conflicts":[],"source_coverage":{}}`

func TestSynthesizeAllocationsMatchTheInternalCall(t *testing.T) {
	model := &schemafluxtest.Provider{}
	model.Reply(synthesisAllocationAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	o := SynthesizeOptions{}
	c, err := synthesizeCommon(o)
	if err != nil {
		t.Fatal(err)
	}
	sources := []any{"source"}
	api := testing.AllocsPerRun(20, func() { _, _ = Synthesize[synthesisAllocationTarget](ctx, sources) })
	internal := testing.AllocsPerRun(20, func() { _, _ = ops.Synthesize[synthesisAllocationTarget](sources, synthesizeInternal(ctx, o, c)) })
	t.Logf("allocations with client: API %.2f, internal %.2f", api, internal)
	// The public result has a distinct type. Returning its value requires one
	// result object beyond the internal operation's result.
	if api != internal+1 {
		t.Errorf("with client: API %.2f allocations, internal %.2f; want one result conversion allocation", api, internal)
	}

	defaultModel := &schemafluxtest.Provider{}
	defaultModel.Reply(synthesisAllocationAnswer)
	schemafluxtest.Install(t, defaultModel)
	pinned, _ := ops.PinExec(context.Background())
	apiDefault := testing.AllocsPerRun(20, func() { _, _ = Synthesize[synthesisAllocationTarget](context.Background(), sources) })
	internalDefault := testing.AllocsPerRun(20, func() { _, _ = ops.Synthesize[synthesisAllocationTarget](sources, synthesizeInternal(pinned, o, c)) })
	t.Logf("allocations on default client: API %.2f, internal with a pinned context %.2f", apiDefault, internalDefault)
	if apiDefault != internalDefault+2 {
		t.Errorf("default client: API %.2f allocations, internal with pinned context %.2f; want one result conversion allocation and one default-client context lookup", apiDefault, internalDefault)
	}
}
