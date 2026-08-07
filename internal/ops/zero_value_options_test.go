package ops

import (
	"testing"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/types"
)

// F-01: Strict was Mode(0) and Smart was Speed(0), so every merge guard of the
// form `if user.Mode != 0` read "the caller chose Strict" as "the caller chose
// nothing" -- and the operation default won. On roughly ten operations,
// .Strict() and .Smart() were unrepresentable: there was no value a caller
// could pass that meant them.
//
// ModeUnset and TierUnset occupy zero now. These cases fail against the old
// numbering, which is the point of writing them out per operation rather than
// testing the constants.
func TestExplicitStrictAndSmartSurviveTheMerge(t *testing.T) {
	// Every merge here has non-Strict, non-Smart defaults, which is what made
	// the defect visible: the caller's choice and the default disagree.
	cases := []struct {
		name  string
		merge func() (types.Mode, types.Speed)
	}{
		{"negotiate", func() (types.Mode, types.Speed) {
			out := mergeNegotiateOptions(
				NegotiateOptions{Mode: types.TransformMode, Intelligence: types.Fast},
				NegotiateOptions{Mode: types.Strict, Intelligence: types.Smart})
			return out.Mode, out.Intelligence
		}},
		{"arbitrate", func() (types.Mode, types.Speed) {
			out := mergeArbitrateOptions(
				ArbitrateOptions{Mode: types.TransformMode, Intelligence: types.Fast},
				ArbitrateOptions{Mode: types.Strict, Intelligence: types.Smart})
			return out.Mode, out.Intelligence
		}},
		{"audit", func() (types.Mode, types.Speed) {
			out := mergeAuditOptions(
				AuditOptions{Mode: types.TransformMode, Intelligence: types.Fast},
				AuditOptions{Mode: types.Strict, Intelligence: types.Smart})
			return out.Mode, out.Intelligence
		}},
		{"compose", func() (types.Mode, types.Speed) {
			out := mergeComposeOptions(
				ComposeOptions{Mode: types.TransformMode, Intelligence: types.Fast},
				ComposeOptions{Mode: types.Strict, Intelligence: types.Smart})
			return out.Mode, out.Intelligence
		}},
		{"conform", func() (types.Mode, types.Speed) {
			out := mergeConformOptions(
				ConformOptions{Mode: types.TransformMode, Intelligence: types.Fast},
				ConformOptions{Mode: types.Strict, Intelligence: types.Smart})
			return out.Mode, out.Intelligence
		}},
		{"derive", func() (types.Mode, types.Speed) {
			out := mergeDeriveOptions(
				DeriveOptions{Mode: types.TransformMode, Intelligence: types.Fast},
				DeriveOptions{Mode: types.Strict, Intelligence: types.Smart})
			return out.Mode, out.Intelligence
		}},
		{"interpolate", func() (types.Mode, types.Speed) {
			out := mergeInterpolateOptions(
				InterpolateOptions{Mode: types.TransformMode, Intelligence: types.Fast},
				InterpolateOptions{Mode: types.Strict, Intelligence: types.Smart})
			return out.Mode, out.Intelligence
		}},
		{"pivot", func() (types.Mode, types.Speed) {
			out := mergePivotOptions(
				PivotOptions{Mode: types.TransformMode, Intelligence: types.Fast},
				PivotOptions{Mode: types.Strict, Intelligence: types.Smart})
			return out.Mode, out.Intelligence
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode, tier := tc.merge()
			if mode != types.Strict {
				t.Errorf("Mode = %v, want strict -- the caller's choice was read as silence", mode)
			}
			if tier != types.Smart {
				t.Errorf("Intelligence = %v, want smart -- the caller's choice was read as silence", tier)
			}
		})
	}
}

// The other direction has to keep working: a caller who says nothing still gets
// the operation's default, which is what the guards were there for.
func TestUnsetOptionsStillTakeTheOperationDefault(t *testing.T) {
	out := mergeNegotiateOptions(
		NegotiateOptions{Mode: types.Creative, Intelligence: types.Quick},
		NegotiateOptions{}) // says nothing

	if out.Mode != types.Creative {
		t.Errorf("Mode = %v, want the default creative", out.Mode)
	}
	if out.Intelligence != types.Quick {
		t.Errorf("Intelligence = %v, want the default quick", out.Intelligence)
	}
}

// Zero is now unset for both types, and reports itself as such rather than
// impersonating a real choice.
func TestZeroValuesAreUnset(t *testing.T) {
	var mode types.Mode
	var tier types.Speed

	if mode != types.ModeUnset {
		t.Errorf("the zero Mode is %v, want ModeUnset", mode)
	}
	if tier != types.TierUnset {
		t.Errorf("the zero Speed is %v, want TierUnset", tier)
	}
	if got := mode.String(); got != "unset" {
		t.Errorf("ModeUnset.String() = %q, want unset", got)
	}
	if got := tier.String(); got != "unset" {
		t.Errorf("TierUnset.String() = %q, want unset", got)
	}
	if types.Strict == types.ModeUnset {
		t.Error("Strict still shares the zero value with unset")
	}
	if types.Smart == types.TierUnset {
		t.Error("Smart still shares the zero value with unset")
	}
}

// An unset tier and an unset mode have to resolve to something usable at the
// point of use, or renumbering just moves the defect into the request builder.
func TestUnsetResolvesToAUsableRequest(t *testing.T) {
	if temp := config.GetTemperature(types.ModeUnset); temp <= 0 {
		t.Errorf("temperature for an unset mode = %v, want a usable default", temp)
	}
	if tokens := config.GetMaxTokens(types.TierUnset); tokens <= 0 {
		t.Errorf("max tokens for an unset tier = %v, want a usable default", tokens)
	}
	if model := config.GetModel(types.TierUnset, "openai"); model == "" {
		t.Error("an unset tier resolves to no model; renumbering moved the defect instead of fixing it")
	}
}

// The defect, stated numerically. Under the old numbering Strict and Smart
// *were* the literal zero below, so this is what every merge guard saw when a
// caller asked for them: `if user.Mode != 0` was false, and the operation
// default won. The merge is unchanged -- only the constants moved -- so this
// case is the witness that the old constants could not survive it.
func TestTheOldZeroValuedChoiceIsReadAsSilence(t *testing.T) {
	out := mergeNegotiateOptions(
		NegotiateOptions{Mode: types.Creative, Intelligence: types.Quick},
		NegotiateOptions{Mode: 0, Intelligence: 0}) // Strict and Smart, as numbered before A-005

	if out.Mode != types.Creative || out.Intelligence != types.Quick {
		t.Fatalf("merge honoured a zero-valued choice: Mode=%v Intelligence=%v", out.Mode, out.Intelligence)
	}

	// And the same merge with the current constants keeps them, which is the
	// difference A-005 bought.
	out = mergeNegotiateOptions(
		NegotiateOptions{Mode: types.Creative, Intelligence: types.Quick},
		NegotiateOptions{Mode: types.Strict, Intelligence: types.Smart})

	if out.Mode != types.Strict || out.Intelligence != types.Smart {
		t.Fatalf("merge dropped an explicit choice: Mode=%v Intelligence=%v", out.Mode, out.Intelligence)
	}
}
