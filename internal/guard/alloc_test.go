package guard_test

import (
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// allocRuns is how many calls an allocation count is averaged over.
const allocRuns = 20

// allocsOf measures the allocations of one call of the row in a state. The rig
// is built once, outside the measurement, and every call gets a new answer
// script position by Reset, so the fake provider's own bookkeeping is the same
// in every state.
func allocsOf(t *testing.T, r *row, st state) float64 {
	t.Helper()
	x := neutral()
	g := newRig(t, withFloor(st, r, scenario{}), 0, func(m *schemafluxtest.Provider) { r.succeeds(m, x, false) })
	// The first call warms what is built once per client, outside the count.
	if _, err := r.call(g.ctx, x); err != nil {
		t.Fatalf("%s: the call failed: %v", st.name, err)
	}
	return testing.AllocsPerRun(allocRuns, func() {
		g.model.Reset()
		if g.rec != nil {
			g.rec.Reset()
		}
		_, _ = r.call(g.ctx, x)
	})
}

// TestNoRecorderNoObserverNoFloorAddsNothing holds the plain state to doing less
// than every other state. The allocation tests of the lanes (v2/*_alloc_test.go)
// compare a function with its internal call in the plain state and are written in
// package schemaflux, so their numbers cannot be read from here: this guard
// asserts what holds over the whole catalogue and needs no number. In the plain
// state no Record is built, so a call allocates strictly less than the same call
// with a recorder, and no observer event is emitted and no policy is consulted,
// so it allocates no more than the same call observed or under a floor.
func TestNoRecorderNoObserverNoFloorAddsNothing(t *testing.T) {
	recorderOnly := state{name: "recorder only", recorder: true}
	floorOnly := state{name: "floor only", floor: schemaflux.ContractJSONWellFormed}
	for _, r := range allRows() {
		t.Run(r.id(), func(t *testing.T) {
			if why, ok := r.na["allocations"]; ok {
				t.Skip(why)
			}
			plain := allocsOf(t, &r, statePlain)
			recorded := allocsOf(t, &r, recorderOnly)
			var floored float64
			if meets(&r) {
				floored = allocsOf(t, &r, floorOnly)
			}
			// The observer is registered for the rest of the subtest, so it is last.
			observed := allocsOf(t, &r, stateObservedOnly)
			if plain >= recorded {
				t.Errorf("the plain call allocates %v, a call with a recorder %v: a Record seems to be built with no recorder, or the recorder costs nothing", plain, recorded)
			}
			if plain > observed {
				t.Errorf("the plain call allocates %v, more than the same call under an observer (%v)", plain, observed)
			}
			if meets(&r) && plain > floored {
				t.Errorf("the plain call allocates %v, more than the same call under a floor (%v)", plain, floored)
			}
		})
	}
}
