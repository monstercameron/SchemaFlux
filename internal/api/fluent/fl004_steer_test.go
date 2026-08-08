package fluent

import "testing"

// FL-004 / F-04. "Steer assigns rather than appends, so two .Steer(...)
// calls silently drop the first." The fix belongs in
// internal/ops/options.go's WithSteering (CommonOptions.WithSteering,
// ExtractOptions.WithSteering, and friends all do `c.Steering = steering;
// return c`) and in the op that joins caller steering with its own
// generated clauses -- both outside this task's edit scope (options.go is
// explicitly excluded; the join happens in files this task cannot touch
// either). This test documents the bug is still present rather than
// silently leaving FL-004 unmentioned; it PASSES because it asserts
// today's actual (unfixed) behavior.
func TestFL004SteerStillAssignsRatherThanAccumulates(t *testing.T) {
	built := Extracting[map[string]any]("input").
		Steer("first instruction").
		Steer("second instruction")

	got := built.opts.CommonOptions.Steering
	if got != "second instruction" {
		t.Errorf("after two .Steer() calls, Steering = %q; want %q if F-04 has been fixed to accumulate (update this test), or %q if it still assigns",
			got, "first instruction; second instruction (or similar)", "second instruction")
	}
	if got == "first instruction" {
		t.Errorf("unexpected: the FIRST call won instead of the second -- that is a different bug than F-04 describes")
	}
}
