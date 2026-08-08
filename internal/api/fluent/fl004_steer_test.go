package fluent

import (
	"strings"
	"testing"
)

// FL-004 / F-04. "Steer assigns rather than appends, so two .Steer(...)
// calls silently drop the first." Fixed in three places, one per fluent
// builder base:
//
//   - internal/ops/options.go's CommonOptions.WithSteering, which every
//     XOptions.WithSteering wrapper (ExtractOptions.WithSteering and so on)
//     delegates to, and which commonRequest.Steer (fluent_base.go) calls
//     through CommonOptions.WithSteering as well.
//   - opRequest.Steer (fluent_base.go), which writes types.OpOptions.Steering
//     directly rather than through CommonOptions.
//   - directRequest.Steer (fluent_base.go) via the per-entrypoint
//     setSteering closures in fluent_advanced.go.
//
// All three now call the shared accumulateSteering helper (fluent_base.go),
// so a second .Steer(...) call appends (joined by "; ") instead of replacing
// the first, and the caller's two instructions stay separable substrings of
// the result rather than being concatenated into one run-on the caller did
// not write.
func TestFL004SteerAccumulates(t *testing.T) {
	t.Run("commonRequest base (Annotating) accumulates two calls", func(t *testing.T) {
		built := Annotating("input").
			Steer("first instruction").
			Steer("second instruction")
		got := built.opts.CommonOptions.Steering
		assertAccumulated(t, got, "first instruction", "second instruction")
	})

	t.Run("commonRequest base (Extracting) accumulates two calls", func(t *testing.T) {
		built := Extracting[map[string]any]("input").
			Steer("first instruction").
			Steer("second instruction")
		got := built.opts.CommonOptions.Steering
		assertAccumulated(t, got, "first instruction", "second instruction")
	})

	t.Run("opRequest base (CheckingSimilarity) accumulates two calls", func(t *testing.T) {
		built := CheckingSimilarity("left", "right").
			Steer("first instruction").
			Steer("second instruction")
		got := built.opts.OpOptions.Steering
		assertAccumulated(t, got, "first instruction", "second instruction")
	})

	t.Run("directRequest base (Negotiating) accumulates two calls", func(t *testing.T) {
		built := Negotiating[map[string]any](nil).
			Steer("first instruction").
			Steer("second instruction")
		got := built.opts.Steering
		assertAccumulated(t, got, "first instruction", "second instruction")
	})

	t.Run("directRequest base (Resolving) accumulates two calls", func(t *testing.T) {
		built := Resolving([]string{"a", "b"}).
			Steer("first instruction").
			Steer("second instruction")
		got := built.opts.Steering
		assertAccumulated(t, got, "first instruction", "second instruction")
	})

	t.Run("three calls all survive, in order", func(t *testing.T) {
		built := Extracting[map[string]any]("input").
			Steer("one").
			Steer("two").
			Steer("three")
		got := built.opts.CommonOptions.Steering
		want := "one; two; three"
		if got != want {
			t.Errorf("after three .Steer() calls, Steering = %q, want %q", got, want)
		}
	})

	t.Run("a single call is unchanged (no stray separator)", func(t *testing.T) {
		built := Extracting[map[string]any]("input").Steer("only instruction")
		got := built.opts.CommonOptions.Steering
		if got != "only instruction" {
			t.Errorf("after one .Steer() call, Steering = %q, want %q (no separator should appear)", got, "only instruction")
		}
	})

	t.Run("Steer(\"\") is a no-op and does not blank out prior steering", func(t *testing.T) {
		built := Extracting[map[string]any]("input").
			Steer("kept instruction").
			Steer("")
		got := built.opts.CommonOptions.Steering
		if got != "kept instruction" {
			t.Errorf("Steer(\"\") after a real Steer() changed Steering to %q, want it unchanged at %q", got, "kept instruction")
		}
	})

	t.Run("Steer(\"\") first, then a real call, keeps only the real one", func(t *testing.T) {
		built := Extracting[map[string]any]("input").
			Steer("").
			Steer("kept instruction")
		got := built.opts.CommonOptions.Steering
		if got != "kept instruction" {
			t.Errorf("Steering = %q, want %q (empty first call should not leave a leading separator)", got, "kept instruction")
		}
	})

	t.Run("builder is immutable: the original chain link keeps its own value", func(t *testing.T) {
		first := Extracting[map[string]any]("input").Steer("first instruction")
		second := first.Steer("second instruction")
		if first.opts.CommonOptions.Steering != "first instruction" {
			t.Errorf("first builder's Steering mutated to %q after deriving a second chain link -- fluent builders must stay value-immutable", first.opts.CommonOptions.Steering)
		}
		assertAccumulated(t, second.opts.CommonOptions.Steering, "first instruction", "second instruction")
	})

	t.Run("WithOptions still allows a caller to reset steering explicitly", func(t *testing.T) {
		opts := NewExtractOptions()
		opts.CommonOptions.Steering = "explicit reset"
		built := Extracting[map[string]any]("input").
			Steer("will be overridden by WithOptions").
			WithOptions(opts)
		got := built.opts.CommonOptions.Steering
		if got != "explicit reset" {
			t.Errorf("WithOptions()'s explicit Steering was not honoured: got %q, want %q", got, "explicit reset")
		}
	})
}

// assertAccumulated checks both halves of FL-004's bar: neither instruction
// was dropped, and each stays a distinguishable substring (not merged into
// one string a caller could not have written and could not parse back out).
func assertAccumulated(t *testing.T, got, first, second string) {
	t.Helper()
	if !strings.Contains(got, first) {
		t.Errorf("Steering = %q lost the first .Steer() call %q", got, first)
	}
	if !strings.Contains(got, second) {
		t.Errorf("Steering = %q lost the second .Steer() call %q", got, second)
	}
	if got == second {
		t.Errorf("Steering = %q: the first call was silently replaced by the second (the exact FL-004 bug)", got)
	}
	firstIdx := strings.Index(got, first)
	secondIdx := strings.Index(got, second)
	if firstIdx == -1 || secondIdx == -1 || firstIdx > secondIdx {
		t.Errorf("Steering = %q: expected %q to appear before %q", got, first, second)
	}
}
