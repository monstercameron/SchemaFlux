package types

import "testing"

// The whole point: an explicitly set zero is not the same as an unset field.
func TestSetZeroIsDistinguishableFromUnset(t *testing.T) {
	explicit := Set(0.0)
	silent := Opt[float64]{}

	if !explicit.IsSet() {
		t.Error("Set(0.0) reports unset; that is the bug this type exists for")
	}
	if silent.IsSet() {
		t.Error("the zero value reports set")
	}
	if got := explicit.Or(0.7); got != 0.0 {
		t.Errorf("Or on an explicit zero = %v, want 0", got)
	}
	if got := silent.Or(0.7); got != 0.7 {
		t.Errorf("Or on an unset value = %v, want the fallback 0.7", got)
	}
}

func TestGetReportsBothValueAndPresence(t *testing.T) {
	cases := []struct {
		name      string
		opt       Opt[int]
		wantValue int
		wantSet   bool
	}{
		{"unset", Opt[int]{}, 0, false},
		{"explicit zero", Set(0), 0, true},
		{"explicit value", Set(42), 42, true},
		{"explicit negative", Set(-1), -1, true},
		{"Unset helper", Unset[int](), 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, ok := tc.opt.Get()
			if value != tc.wantValue || ok != tc.wantSet {
				t.Errorf("Get() = (%v, %v), want (%v, %v)", value, ok, tc.wantValue, tc.wantSet)
			}
		})
	}
}

// Merge is what the hand-written guards were approximating, and the case they
// got wrong is the first one here.
func TestMergePrefersAnExplicitValueIncludingZero(t *testing.T) {
	defaults := Set(0.7)

	cases := []struct {
		name string
		user Opt[float64]
		want float64
	}{
		{"explicit zero wins", Set(0.0), 0.0},
		{"explicit value wins", Set(0.9), 0.9},
		{"unset falls back", Opt[float64]{}, 0.7},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.user.Merge(defaults).Value(); got != tc.want {
				t.Errorf("Merge = %v, want %v", got, tc.want)
			}
		})
	}
}

// Merging two unset values stays unset, so a chain of merges cannot invent a
// decision nobody made.
func TestMergingUnsetValuesStaysUnset(t *testing.T) {
	if Unset[int]().Merge(Unset[int]()).IsSet() {
		t.Error("merging two unset values produced a set value")
	}
}

// Value is the escape hatch for callers who have already decided a zero is
// fine, and it must not lie about presence.
func TestValueReturnsTheZeroForUnset(t *testing.T) {
	if got := Unset[string]().Value(); got != "" {
		t.Errorf("Value() = %q, want the empty string", got)
	}
	if got := Set("x").Value(); got != "x" {
		t.Errorf("Value() = %q, want x", got)
	}
}

// It works for the other kinds a merge guard covers, not just floats.
func TestOptWorksForOtherComparableTypes(t *testing.T) {
	if got := Set(false).Or(true); got != false {
		t.Error("an explicit false was overridden by the default true")
	}
	if got := Set("").Or("fallback"); got != "" {
		t.Errorf("an explicit empty string was overridden: %q", got)
	}
	if got := Unset[bool]().Or(true); got != true {
		t.Error("an unset bool did not fall back")
	}
}
