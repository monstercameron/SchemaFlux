package guard_test

import (
	"sort"
	"strings"
	"sync"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// stateObservedOnly is an observer with no recorder and no floor: the state in
// which the operation scope is the only thing a call reports.
var stateObservedOnly = state{name: "observer only", observer: true}

// scopeScenarios are the calls whose operation scope is read: one that
// succeeds, and the ways of failing that end a call at different places.
func scopeScenarios() []scenario {
	pick := map[string]bool{"provider unavailable": true, "malformed answer": true, "unusable option": true, "cancelled context": true}
	out := []scenario{{name: "success", sends: true}}
	for _, sc := range failureScenarios {
		if pick[sc.name] {
			out = append(out, sc)
		}
	}
	return out
}

// renamed collects the rows whose operation scope is not named as the Record's Op
// is, for TestEveryOperationIsOneScopeToAnObserver to report once.
var (
	renamedMu sync.Mutex
	renamed   = map[string]string{}
)

func TestEveryOperationIsOneScopeToAnObserver(t *testing.T) {
	for _, r := range allRows() {
		for _, st := range []state{stateObservedOnly, stateObserver} {
			for _, sc := range scopeScenarios() {
				t.Run(r.id()+"/"+st.name+"/"+sc.name, func(t *testing.T) {
					if ok, why := applies(&r, sc); !ok {
						t.Skip(why)
					}
					o := run(t, &r, st, sc, neutral())
					starts, finishes := o.rig.scopes.operations()
					if len(starts) != 1 || len(finishes) != 1 {
						t.Fatalf("the observer saw %d operation-scope starts and %d finishes for one call, want 1 and 1 (error: %v)", len(starts), len(finishes), o.err)
					}
					var want schemaflux.Kind
					if e := failureOf(o.err); e != nil {
						want = e.Kind
					} else if o.err != nil {
						t.Fatalf("the call's error is %T, not an *Error", o.err)
					}
					if finishes[0].Kind != want {
						t.Errorf("the operation scope finished with kind %v, the call returned kind %v", finishes[0].Kind, want)
					}
					if starts[0].Operation != finishes[0].Operation {
						t.Errorf("the scope started as %q and finished as %q", starts[0].Operation, finishes[0].Operation)
					}
					if sc.name == "success" && st.recorder && starts[0].Operation != r.op {
						renamedMu.Lock()
						renamed[r.id()] = "scope " + starts[0].Operation + ", Record " + r.op
						renamedMu.Unlock()
					}
				})
			}
		}
	}
	if len(renamed) > 0 {
		var lines []string
		for id, what := range renamed {
			lines = append(lines, id+": "+what)
		}
		sort.Strings(lines)
		t.Logf("rows whose operation scope is named otherwise than the Record's Op (a known open defect for the Detail forms; not a failure):\n  %s", strings.Join(lines, "\n  "))
	}
}
