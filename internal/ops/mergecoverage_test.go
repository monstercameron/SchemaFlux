package ops

import (
	"context"
	"reflect"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// The same defect has now shipped twice, and this is the test that would have
// caught both before they did.
//
// Nine options types embed BOTH CommonOptions and types.OpOptions, and the two
// structs declare eight of the same fields. The fluent builders write the
// CommonOptions copy; the operations read the OpOptions one; and
// mergeEmbeddedOpOptions is the single hand-written function that carries each
// field from the first to the second. A field it forgets is not a compile
// error, not a runtime error, and not a wrong-looking answer -- the value is
// simply dropped and the call proceeds on whatever the embedded copy held.
//
//   - ST-010: Steering was forgotten. Every `.Steer(...)` in the fluent API was
//     silently discarded, so operations answered a question the caller had not
//     asked, and the golden prompts recorded the bug as expected output.
//   - DX-001: Model was forgotten. Every `.Model(...)` pin was dropped and the
//     call ran on whatever the speed tier resolved, which is a plausible model,
//     so the only symptoms were a reproduction that would not reproduce and a
//     bill against a model nobody chose.
//
// Both were found by an application integrating this library, not by this
// repository. Two instances of one defect is a class, and a class deserves a
// test that fails on the third instance rather than a third bug report.
//
// The test is behavioural rather than a source inspection: it sets a value on
// the CommonOptions side and asserts the merged OpOptions carries it. A merge
// rewritten in any style still has to pass.

// sharedFieldCheck is how one shared field proves it survives the merge.
//
// Each field needs its own setter and its own comparison because the types
// differ and three of them are not plain values: the two tracking IDs pass
// through requesttracking.Ensure, and Context is wrapped rather than copied. An
// exception written out here is visible; a field silently skipped would recreate
// exactly the hole this test exists to close.
type sharedFieldCheck struct {
	// set puts a distinctive, non-zero value on the CommonOptions side.
	set func(*CommonOptions)

	// ok reports whether the merged options carry what set put there.
	ok func(types.OpOptions) bool

	// why explains what breaks in the product when this field is dropped, so a
	// failure reads as a consequence rather than as a field name.
	why string
}

var sharedFieldChecks = map[string]sharedFieldCheck{
	"Steering": {
		set: func(c *CommonOptions) { c.Steering = "the-sentinel-steering" },
		ok:  func(o types.OpOptions) bool { return o.Steering == "the-sentinel-steering" },
		why: "ST-010: every .Steer(...) is discarded and operations answer a question nobody asked",
	},
	"Threshold": {
		set: func(c *CommonOptions) { c.Threshold = 0.625 },
		ok:  func(o types.OpOptions) bool { return o.Threshold == 0.625 },
		why: "a confidence floor the caller set is ignored, so answers below it are returned as good",
	},
	"Mode": {
		set: func(c *CommonOptions) { c.Mode = types.Strict },
		ok:  func(o types.OpOptions) bool { return o.Mode == types.Strict },
		why: "Strict() stops being strict: unknown fields and empty required fields are accepted",
	},
	"ExactFields": {
		set: func(c *CommonOptions) { c.ExactFields = true },
		ok:  func(o types.OpOptions) bool { return o.ExactFields },
		why: "DX-007: .ExactFields() is discarded, so a property the schema does not name is quietly accepted",
	},
	"CompleteFields": {
		set: func(c *CommonOptions) { c.CompleteFields = true },
		ok:  func(o types.OpOptions) bool { return o.CompleteFields },
		why: "DX-007: .CompleteFields() is discarded, so an answer with an empty required field passes as good",
	},
	"Intelligence": {
		set: func(c *CommonOptions) { c.Intelligence = types.Smart },
		ok:  func(o types.OpOptions) bool { return o.Intelligence == types.Smart },
		why: "Smart()/Fast() stop selecting a tier, so every call runs on the default model and budget",
	},
	"Model": {
		set: func(c *CommonOptions) { c.Model = "the-sentinel-model" },
		ok:  func(o types.OpOptions) bool { return o.Model == "the-sentinel-model" },
		why: "DX-001: every .Model(...) pin is dropped and the call runs on whatever the tier resolved",
	},
	"Context": {
		// Identity cannot be compared: the merge wraps the context with
		// tracking metadata and any invocation timeout, so what comes out is
		// derived from what went in rather than equal to it. A value carried
		// through is the honest check -- it proves the caller's context is the
		// PARENT, which is the whole property (cancellation and deadlines
		// propagate).
		set: func(c *CommonOptions) {
			c.Context = context.WithValue(context.Background(), sentinelKey{}, "carried")
		},
		ok: func(o types.OpOptions) bool {
			return o.Context != nil && o.Context.Value(sentinelKey{}) == "carried"
		},
		why: "the caller's context is not the parent, so cancellation and deadlines never reach the provider",
	},
	"RequestID": {
		set: func(c *CommonOptions) { c.RequestID = "the-sentinel-request-id" },
		ok:  func(o types.OpOptions) bool { return o.RequestID == "the-sentinel-request-id" },
		why: "an explicit request ID is replaced by a generated one, so caller and library logs cannot be joined",
	},
	"CorrelationID": {
		set: func(c *CommonOptions) { c.CorrelationID = "the-sentinel-correlation-id" },
		ok:  func(o types.OpOptions) bool { return o.CorrelationID == "the-sentinel-correlation-id" },
		why: "a correlation ID is replaced, so calls belonging to one user action cannot be grouped",
	},
}

type sentinelKey struct{}

// sharedFieldNames returns the fields declared on BOTH CommonOptions and
// types.OpOptions -- the exact set mergeEmbeddedOpOptions is responsible for.
//
// Derived by reflection rather than listed, so adding a field to both structs
// puts it in scope automatically instead of when somebody remembers to.
func sharedFieldNames() []string {
	inOpOptions := map[string]bool{}
	opType := reflect.TypeOf(types.OpOptions{})
	for i := 0; i < opType.NumField(); i++ {
		inOpOptions[opType.Field(i).Name] = true
	}

	var shared []string
	commonType := reflect.TypeOf(CommonOptions{})
	for i := 0; i < commonType.NumField(); i++ {
		name := commonType.Field(i).Name
		if inOpOptions[name] {
			shared = append(shared, name)
		}
	}
	return shared
}

// Every field the two structs share survives the merge.
func TestMergeCarriesEverySharedField(t *testing.T) {
	for _, name := range sharedFieldNames() {
		check, covered := sharedFieldChecks[name]
		if !covered {
			continue // reported by the completeness test below
		}
		t.Run(name, func(t *testing.T) {
			var common CommonOptions
			check.set(&common)

			merged := mergeEmbeddedOpOptions(common, types.OpOptions{})

			if !check.ok(merged) {
				t.Errorf("mergeEmbeddedOpOptions dropped %s.\n\nWhat this breaks: %s.\n\n"+
					"%s is declared on both CommonOptions and types.OpOptions. The fluent "+
					"builders write the CommonOptions copy and the operations read the "+
					"OpOptions one, so the merge has to carry it across. Add it there, "+
					"beside the fields that are already handled.", name, check.why, name)
			}
		})
	}
}

// A field added to both structs must be added here too.
//
// Without this, the table above degrades into whatever somebody remembered, and
// the next forgotten field is invisible for exactly the reason the last two
// were: nothing fails.
func TestEverySharedFieldIsCovered(t *testing.T) {
	shared := sharedFieldNames()
	if len(shared) < 5 {
		t.Fatalf("only %d shared fields found (%v); the reflection is not seeing the structs, "+
			"so this guard is passing vacuously", len(shared), shared)
	}

	for _, name := range shared {
		if _, covered := sharedFieldChecks[name]; !covered {
			t.Errorf("%s is declared on both CommonOptions and types.OpOptions but has no entry "+
				"in sharedFieldChecks.\n\nEvery shared field is one mergeEmbeddedOpOptions can "+
				"silently drop -- that is ST-010 and DX-001, twice. Add a check saying what "+
				"breaks when this one is dropped.", name)
		}
	}

	// The reverse: an entry for a field that is no longer shared is a check
	// that passes without testing anything.
	isShared := map[string]bool{}
	for _, name := range shared {
		isShared[name] = true
	}
	for name := range sharedFieldChecks {
		if !isShared[name] {
			t.Errorf("sharedFieldChecks covers %q, which is no longer declared on both structs; "+
				"the entry is dead and should go", name)
		}
	}
}

// The embedded side still wins when the CommonOptions side says nothing.
//
// The merge is a fallback, not an overwrite, and a fix for a dropped field
// written as an unconditional assignment would break that -- which is X-07, the
// bug that Mode and Intelligence already carry a comment about. This is the
// other half of the contract, so a future fix cannot trade one for the other.
func TestMergeFallsBackToTheEmbeddedSide(t *testing.T) {
	embedded := types.OpOptions{
		Steering:      "from-embedded",
		Threshold:     0.25,
		Mode:          types.Creative,
		Intelligence:  types.Quick,
		Model:         "model-from-embedded",
		RequestID:     "request-from-embedded",
		CorrelationID: "correlation-from-embedded",
	}

	merged := mergeEmbeddedOpOptions(CommonOptions{}, embedded)

	for _, tc := range []struct {
		field string
		got   any
		want  any
	}{
		{"Steering", merged.Steering, "from-embedded"},
		{"Threshold", merged.Threshold, 0.25},
		{"Mode", merged.Mode, types.Creative},
		{"Intelligence", merged.Intelligence, types.Quick},
		{"Model", merged.Model, "model-from-embedded"},
		{"RequestID", merged.RequestID, "request-from-embedded"},
		{"CorrelationID", merged.CorrelationID, "correlation-from-embedded"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want the embedded value %v; the merge must fall back when the "+
				"CommonOptions side says nothing, not overwrite with a zero", tc.field, tc.got, tc.want)
		}
	}
}
