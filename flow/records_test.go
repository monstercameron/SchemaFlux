package flow_test

import (
	"context"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"
)

// Only a function that asks a model adds a Record, and the steps of these
// combinators are the caller's, so a recorder on the context sees nothing of them.
func TestTheCombinatorsAddNoRecord(t *testing.T) {
	var recorder schemaflux.Recorder
	ctx := schemaflux.WithRecorder(context.Background(), &recorder)
	var store flow.MemoryStore
	text, yes, label := returns("x"), returns(true), returns("a")
	routes := []schemaflux.Route[string]{schemaflux.To("a", text), schemaflux.To("b", text)}
	accept := func(string) bool { return true }
	gate := func(context.Context, string) (flow.ApprovalOutcome, error) {
		return flow.ApprovalOutcome{Approved: true}, nil
	}

	_, _ = flow.BranchAhead(ctx, yes, text, text)
	_, _ = flow.BranchBoth(ctx, yes, text, text)
	_, _ = flow.RouteAhead(ctx, label, "a", routes...)
	_, _, _ = flow.Escalate(ctx, text, text, accept)
	_, _, _ = flow.Escalate(ctx, fails[string](errStep), text, nil)
	_, _, _ = flow.Until(ctx, text, accept, 2)
	_, _, _ = flow.Vote(ctx, 3, text, flow.ExactAgreement[string](2))
	_, _, _ = flow.Checkpoint(ctx, &store, "r", "s", 1, text)
	_, _ = flow.Approve(ctx, text, gate, nil)
	_, _, _ = flow.MapReduce(ctx, []int{1, 2, 3}, func(context.Context, []int) (int, error) { return 0, nil })
	_ = flow.Guard("state", func(string) (bool, string) { return true, "" })

	if records := recorder.Records(); len(records) != 0 {
		t.Errorf("%d records for combinators that asked no model", len(records))
	}
}
