package fluent

import (
	"reflect"
	"testing"
)

// FL-003 / F-03. "eleven fluent entrypoints start from a zero-value options
// struct rather than the NewXOptions() constructor the direct API uses, so
// the two public APIs disagree on defaults." Verify says: "a test that
// constructs each operation both ways and asserts the resolved options are
// equal."
//
// All seventeen fluent entrypoints now route through a NewXOptions()
// constructor. Six already did (Extracting, Transforming, Generating,
// Choosing, Filtering, Sorting). The remaining eleven -- fluent_advanced.go's
// Negotiating, NegotiatingAdversarially, Resolving, Deriving, Conforming,
// Interpolating, Arbitrating, Projecting, Auditing, Assembling, Pivoting --
// previously passed a bare `XOptions{}` literal because none of
// NegotiateOptions, AdversarialOptions, ResolveOptions, DeriveOptions,
// ConformOptions, InterpolateOptions, ArbitrateOptions, ProjectOptions,
// AuditOptions, ComposeOptions, or PivotOptions had a constructor anywhere in
// internal/ops. internal/ops/options_advanced.go adds the eleven missing
// constructors -- each one reproducing the "Apply defaults" literal already
// built inside the corresponding op function (Negotiate, SettleAdversarial,
// Resolve, Derive, Conform, Interpolate, Arbitrate, Project, auditCore,
// Compose, Pivot), so this is genuinely closing the gap rather than picking
// an arbitrary new default -- and fluent_advanced.go's eleven entrypoints now
// call them.
func TestFL003EntrypointsMatchDirectConstructorDefaults(t *testing.T) {
	t.Run("Extracting", func(t *testing.T) {
		fromEntrypoint := Extracting[map[string]any]("input").opts
		fromConstructor := NewExtractOptions()
		if !reflect.DeepEqual(fromEntrypoint, fromConstructor) {
			t.Errorf("Extracting()'s initial opts = %+v, want NewExtractOptions() = %+v", fromEntrypoint, fromConstructor)
		}
	})

	t.Run("Transforming", func(t *testing.T) {
		fromEntrypoint := Transforming[string, string]("input").opts
		fromConstructor := NewTransformOptions()
		if !reflect.DeepEqual(fromEntrypoint, fromConstructor) {
			t.Errorf("Transforming()'s initial opts = %+v, want NewTransformOptions() = %+v", fromEntrypoint, fromConstructor)
		}
	})

	t.Run("Generating", func(t *testing.T) {
		fromEntrypoint := Generating[string]("prompt").opts
		fromConstructor := NewGenerateOptions()
		if !reflect.DeepEqual(fromEntrypoint, fromConstructor) {
			t.Errorf("Generating()'s initial opts = %+v, want NewGenerateOptions() = %+v", fromEntrypoint, fromConstructor)
		}
	})

	t.Run("Choosing", func(t *testing.T) {
		fromEntrypoint := Choosing([]string{"a", "b"}).opts
		fromConstructor := NewChooseOptions()
		if !reflect.DeepEqual(fromEntrypoint, fromConstructor) {
			t.Errorf("Choosing()'s initial opts = %+v, want NewChooseOptions() = %+v", fromEntrypoint, fromConstructor)
		}
	})

	t.Run("Filtering", func(t *testing.T) {
		fromEntrypoint := Filtering([]string{"a", "b"}).opts
		fromConstructor := NewFilterOptions()
		if !reflect.DeepEqual(fromEntrypoint, fromConstructor) {
			t.Errorf("Filtering()'s initial opts = %+v, want NewFilterOptions() = %+v", fromEntrypoint, fromConstructor)
		}
	})

	t.Run("Sorting", func(t *testing.T) {
		fromEntrypoint := Sorting([]string{"a", "b"}).opts
		fromConstructor := NewSortOptions()
		if !reflect.DeepEqual(fromEntrypoint, fromConstructor) {
			t.Errorf("Sorting()'s initial opts = %+v, want NewSortOptions() = %+v", fromEntrypoint, fromConstructor)
		}
	})

	t.Run("Negotiating", func(t *testing.T) {
		fromEntrypoint := Negotiating[map[string]any](nil).opts
		fromConstructor := NewNegotiateOptions()
		if !reflect.DeepEqual(fromEntrypoint, fromConstructor) {
			t.Errorf("Negotiating()'s initial opts = %+v, want NewNegotiateOptions() = %+v", fromEntrypoint, fromConstructor)
		}
	})

	t.Run("NegotiatingAdversarially", func(t *testing.T) {
		fromEntrypoint := NegotiatingAdversarially[map[string]any](AdversarialContext[map[string]any]{}).opts
		fromConstructor := NewAdversarialOptions()
		if !reflect.DeepEqual(fromEntrypoint, fromConstructor) {
			t.Errorf("NegotiatingAdversarially()'s initial opts = %+v, want NewAdversarialOptions() = %+v", fromEntrypoint, fromConstructor)
		}
	})

	t.Run("Resolving", func(t *testing.T) {
		fromEntrypoint := Resolving([]string{"a", "b"}).opts
		fromConstructor := NewResolveOptions()
		if !reflect.DeepEqual(fromEntrypoint, fromConstructor) {
			t.Errorf("Resolving()'s initial opts = %+v, want NewResolveOptions() = %+v", fromEntrypoint, fromConstructor)
		}
	})

	t.Run("Deriving", func(t *testing.T) {
		fromEntrypoint := Deriving[string, map[string]any]("input").opts
		fromConstructor := NewDeriveOptions()
		if !reflect.DeepEqual(fromEntrypoint, fromConstructor) {
			t.Errorf("Deriving()'s initial opts = %+v, want NewDeriveOptions() = %+v", fromEntrypoint, fromConstructor)
		}
	})

	t.Run("Conforming", func(t *testing.T) {
		fromEntrypoint := Conforming(map[string]any{}, "standard").opts
		fromConstructor := NewConformOptions()
		if !reflect.DeepEqual(fromEntrypoint, fromConstructor) {
			t.Errorf("Conforming()'s initial opts = %+v, want NewConformOptions() = %+v", fromEntrypoint, fromConstructor)
		}
	})

	t.Run("Interpolating", func(t *testing.T) {
		fromEntrypoint := Interpolating([]string{"a", "b"}).opts
		fromConstructor := NewInterpolateOptions()
		if !reflect.DeepEqual(fromEntrypoint, fromConstructor) {
			t.Errorf("Interpolating()'s initial opts = %+v, want NewInterpolateOptions() = %+v", fromEntrypoint, fromConstructor)
		}
	})

	t.Run("Arbitrating", func(t *testing.T) {
		fromEntrypoint := Arbitrating([]string{"a", "b"}).opts
		fromConstructor := NewArbitrateOptions()
		if !reflect.DeepEqual(fromEntrypoint, fromConstructor) {
			t.Errorf("Arbitrating()'s initial opts = %+v, want NewArbitrateOptions() = %+v", fromEntrypoint, fromConstructor)
		}
	})

	t.Run("Projecting", func(t *testing.T) {
		fromEntrypoint := Projecting[string, map[string]any]("input").opts
		fromConstructor := NewProjectOptions()
		if !reflect.DeepEqual(fromEntrypoint, fromConstructor) {
			t.Errorf("Projecting()'s initial opts = %+v, want NewProjectOptions() = %+v", fromEntrypoint, fromConstructor)
		}
	})

	t.Run("Auditing", func(t *testing.T) {
		fromEntrypoint := Auditing(map[string]any{}).opts
		fromConstructor := NewAuditOptions()
		if !reflect.DeepEqual(fromEntrypoint, fromConstructor) {
			t.Errorf("Auditing()'s initial opts = %+v, want NewAuditOptions() = %+v", fromEntrypoint, fromConstructor)
		}
	})

	t.Run("Assembling", func(t *testing.T) {
		fromEntrypoint := Assembling[map[string]any](nil).opts
		fromConstructor := NewComposeOptions()
		if !reflect.DeepEqual(fromEntrypoint, fromConstructor) {
			t.Errorf("Assembling()'s initial opts = %+v, want NewComposeOptions() = %+v", fromEntrypoint, fromConstructor)
		}
	})

	t.Run("Pivoting", func(t *testing.T) {
		fromEntrypoint := Pivoting[string, map[string]any]("input").opts
		fromConstructor := NewPivotOptions()
		if !reflect.DeepEqual(fromEntrypoint, fromConstructor) {
			t.Errorf("Pivoting()'s initial opts = %+v, want NewPivotOptions() = %+v", fromEntrypoint, fromConstructor)
		}
	})
}
