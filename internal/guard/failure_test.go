package guard_test

import (
	"errors"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// allStates are the four states of the call-path table.
var allStates = []state{statePlain, stateRecorder, stateObserver, stateFloor}

func TestEveryFailureIsAnError(t *testing.T) {
	for _, r := range allRows() {
		for _, st := range allStates {
			for _, sc := range failureScenarios {
				t.Run(r.id()+"/"+st.name+"/"+sc.name, func(t *testing.T) {
					if ok, why := applies(&r, sc); !ok {
						t.Skip(why)
					}
					stt := st
					if sc.floor != 0 && st.name != "floor" {
						t.Skip("a floor is a state of its own")
					}
					if st.name == "floor" && !meets(&r) && sc.floor == 0 && !sc.nilCtx {
						t.Skip("under a floor the operation cannot meet, every call is refused as ErrPolicy first (TestEveryOperationUnderEveryFloor)")
					}
					o := run(t, &r, stt, sc, neutral())
					if o.err == nil {
						t.Fatalf("the call returned no error (a value of %s came back); every way of failing returns one", describeValue(o.res))
					}
					e := failureOf(o.err)
					if e == nil {
						t.Fatalf("the error is %T, want errors.AsType to find an *Error", o.err)
					}
					if e.Kind == 0 {
						t.Errorf("the error has the zero Kind")
					}
					if e.Op == "" {
						t.Errorf("the error has no Op (kind %v)", e.Kind)
					}
					if sc.kind != 0 {
						if e.Kind != sc.kind {
							t.Errorf("the error is of kind %v, want %v", e.Kind, sc.kind)
						}
						if !errors.Is(o.err, sc.kind) {
							t.Errorf("errors.Is(err, %v) is false", sc.kind)
						}
					}
					if !isZeroResult(o.res) {
						t.Errorf("a value of type %s came back beside the error, want the zero value", describeValue(o.res))
					}
					if !sc.sends && o.rig.model.Calls() != 0 {
						t.Errorf("%d requests were sent for a call that fails before anything is sent", o.rig.model.Calls())
					}
				})
			}
		}
	}
}

// TestDiffReportsTheModelsFailureInSummaryError holds the one function whose model
// call can fail without failing the call: the comparison is Go, the model writes
// only the summary, and a failure of the summary is DiffResult.SummaryError. That
// error is converted like a returned one (B3 F9): an *Error with the provider's
// kind, an Op, and no payload.
func TestDiffReportsTheModelsFailureInSummaryError(t *testing.T) {
	var diff *row
	for _, r := range allRows() {
		if r.name == "Diff" {
			diff = &r
		}
	}
	if diff == nil {
		t.Skip("no Diff row")
	}
	for _, sc := range failureScenarios {
		if !sc.sends || sc.kind == 0 || sc.kind == schemaflux.ErrInvalidAnswer {
			continue
		}
		t.Run(sc.name, func(t *testing.T) {
			g := newRig(t, stateRecorder, 0, func(m *schemafluxtest.Provider) { sc.script(m, diff, canaried()) })
			x := canaried()
			res, err := schemaflux.Diff(g.ctx, note{Body: x.Text, N: 1}, note{Body: "changed", N: 2}, schemaflux.DiffOptions{Options: x.opts()})
			if err != nil {
				t.Fatalf("the call failed: %v", err)
			}
			e := failureOf(res.SummaryError)
			if e == nil {
				t.Fatalf("SummaryError is %v (%T), want an *Error", res.SummaryError, res.SummaryError)
			}
			if e.Kind != sc.kind {
				t.Errorf("SummaryError is of kind %v, want %v", e.Kind, sc.kind)
			}
			if e.Op == "" {
				t.Errorf("SummaryError has no Op")
			}
			requireNoCanary(t, errorTexts(res.SummaryError, false))
		})
	}
}
