package types

import "testing"

func TestModeString(t *testing.T) {
	cases := []struct {
		m    Mode
		want string
	}{
		{ModeUnset, "unset"},
		{Strict, "strict"},
		{TransformMode, "transform"},
		{Creative, "creative"},
		{Mode(99), "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.m.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSpeedString(t *testing.T) {
	cases := []struct {
		s    Speed
		want string
	}{
		{TierUnset, "unset"},
		{Smart, "smart"},
		{Fast, "fast"},
		{Quick, "quick"},
		{Speed(99), "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.s.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The rule PricingQuality exists to enforce: a boolean cannot distinguish
// "no rate card" from "genuinely free", so Known must say unknown is
// unknown and free is known -- collapsing the two would let a zero-cost,
// unpriced call read as free.
func TestPricingQualityKnownDistinguishesUnknownFromFree(t *testing.T) {
	cases := []struct {
		q    PricingQuality
		want bool
	}{
		{PricingUnknown, false},
		{PricingExact, true},
		{PricingEstimated, true},
		{PricingFree, true},
		{PricingQuality("garbage"), false},
	}
	for _, tc := range cases {
		t.Run(string(tc.q), func(t *testing.T) {
			if got := tc.q.Known(); got != tc.want {
				t.Errorf("Known() = %v, want %v", got, tc.want)
			}
		})
	}

	if PricingUnknown == PricingFree {
		t.Fatal("PricingUnknown and PricingFree must be distinct values")
	}
}
