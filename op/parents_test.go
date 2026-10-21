package op_test

import (
	"context"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/op"
)

// parentsOf runs a descriptor on ctx and returns the parents the Record's
// Provenance names, and whether it has a Provenance.
func parentsOf(t *testing.T, ctx context.Context, rec *schemaflux.Recorder) ([]string, bool) {
	t.Helper()
	rec.Reset()
	if _, err := op.Run(ctx, invoiceDescriptor(), "x"); err != nil {
		t.Fatal(err)
	}
	p := onlyRecord(t, rec).Provenance
	if p == nil {
		return nil, false
	}
	return p.ParentResultIDs, true
}

func TestWithParentsNamesTheParentsInTheOrderGiven(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	base := clientOn(echoing(), recordingTo(&rec))
	got, ok := parentsOf(t, op.WithParents(base, "a", "b", "c"), &rec)
	if !ok || len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("parents = %v", got)
	}
}

func TestWithParentsTheNearestSettingReplacesTheOuterOne(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	base := clientOn(echoing(), recordingTo(&rec))
	got, _ := parentsOf(t, op.WithParents(op.WithParents(base, "outer"), "inner1", "inner2"), &rec)
	if len(got) != 2 || got[0] != "inner1" {
		t.Errorf("parents = %v, want the inner ones", got)
	}
}

func TestWithParentsOfNoIdsSaysTheCallsHaveNone(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	base := clientOn(echoing(), recordingTo(&rec))
	got, ok := parentsOf(t, op.WithParents(op.WithParents(base, "outer")), &rec)
	if !ok || len(got) != 0 {
		t.Errorf("parents = %v, want none", got)
	}
	got, _ = parentsOf(t, base, &rec)
	if len(got) != 0 {
		t.Errorf("a context with no parents gave %v", got)
	}
}

func TestWithParentsCopiesTheIds(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	ids := []string{"a", "b"}
	ctx := op.WithParents(clientOn(echoing(), recordingTo(&rec)), ids...)
	ids[0] = "changed"
	got, _ := parentsOf(t, ctx, &rec)
	if len(got) != 2 || got[0] != "a" {
		t.Errorf("parents = %v: the caller's slice was kept", got)
	}
}

func TestWithParentsKeepsTheClientOfTheContext(t *testing.T) {
	t.Parallel()
	model := echoing()
	ctx := op.WithParents(clientOn(model), "a")
	if _, err := op.Run(ctx, invoiceDescriptor(), "x"); err != nil {
		t.Fatal(err)
	}
	if model.Calls() != 1 {
		t.Errorf("the client was lost: %d requests", model.Calls())
	}
}

func TestWithParentsOnANilContextPanicsAsContextWithValueDoes(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()
	//lint:ignore SA1012 the test is of a nil context
	op.WithParents(nil, "a") //nolint:staticcheck
}

func TestAChainOfCallsIsTraceableByTheIdsOfTheirRecords(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	base := clientOn(echoing(), recordingTo(&rec))
	if _, err := op.Run(base, invoiceDescriptor(), "first"); err != nil {
		t.Fatal(err)
	}
	firstID := onlyRecord(t, &rec).Provenance.ResultID
	rec.Reset()
	if _, err := op.Run(op.WithParents(base, firstID), invoiceDescriptor(), "second"); err != nil {
		t.Fatal(err)
	}
	p := onlyRecord(t, &rec).Provenance
	if len(p.ParentResultIDs) != 1 || p.ParentResultIDs[0] != firstID || p.ResultID == firstID {
		t.Errorf("provenance = %+v, want the first call's id as the parent and an id of its own", p)
	}
}
