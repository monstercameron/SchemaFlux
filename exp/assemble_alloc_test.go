package exp

import (
	"context"
	"reflect"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type assembleAllocationTarget struct {
	Name string `json:"name"`
}

func TestMergeAssembleOptionsCopiesEveryField(t *testing.T) {
	base := AssembleOptions{Options: schemaflux.Options{Model: "a", Tier: schemaflux.TierFast, Effort: schemaflux.EffortLow, Mode: schemaflux.ModeStrict, Steering: "a", MaxOutputTokens: 1, WebSearch: true}, Template: "a", MergeStrategy: "first", FillGaps: true}
	later := AssembleOptions{Options: schemaflux.Options{Model: "b", Tier: schemaflux.TierSmart, Effort: schemaflux.EffortHigh, Mode: schemaflux.ModeCreative, Steering: "b", MaxOutputTokens: 2}, Template: "b", MergeStrategy: "last"}
	want := later
	want.Options.WebSearch = true
	want.FillGaps = true
	if got := mergeAssembleOptions([]AssembleOptions{base, later}); !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
	if got := mergeAssembleOptions(nil); !reflect.DeepEqual(got, AssembleOptions{}) {
		t.Fatalf("zero merge=%+v", got)
	}
	if got := mergeAssembleOptions([]AssembleOptions{base}); !reflect.DeepEqual(got, base) {
		t.Fatalf("one merge=%+v", got)
	}
}

const assembleAllocationAnswer = `{"composed":{"name":"Ada"},"field_sources":[],"conflicts_resolved":0,"gaps_filled":[],"unused_parts":[],"completeness":0.8}`

func TestAssembleAllocationsMatchInternal(t *testing.T) {
	model := &schemafluxtest.Provider{}
	model.Reply(assembleAllocationAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	o := AssembleOptions{}
	c, err := assembleCommon(o)
	if err != nil {
		t.Fatal(err)
	}
	parts := []any{"part"}
	api := testing.AllocsPerRun(20, func() { _, _ = Assemble[assembleAllocationTarget](ctx, parts) })
	internal := testing.AllocsPerRun(20, func() { _, _ = ops.Assemble[assembleAllocationTarget](parts, assembleInternal(ctx, o, c)) })
	// The API returns AssembleResult[T], which is a distinct public struct from
	// ops.ComposeResult[T]; converting the operation result costs one allocation.
	if api != internal+1 {
		t.Errorf("with client: API %.2f allocations, internal %.2f; want one result conversion", api, internal)
	}
	defaultModel := &schemafluxtest.Provider{}
	defaultModel.Reply(assembleAllocationAnswer)
	schemafluxtest.Install(t, defaultModel)
	pinned, _ := ops.PinExec(context.Background())
	apiDefault := testing.AllocsPerRun(20, func() { _, _ = Assemble[assembleAllocationTarget](context.Background(), parts) })
	internalDefault := testing.AllocsPerRun(20, func() { _, _ = ops.Assemble[assembleAllocationTarget](parts, assembleInternal(pinned, o, c)) })
	if apiDefault != internalDefault+2 {
		t.Errorf("default client: API %.2f allocations, pinned internal %.2f; want result conversion and client pin", apiDefault, internalDefault)
	}
}
