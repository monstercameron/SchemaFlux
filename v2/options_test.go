package schemaflux

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/types"
)

// filledOptions returns an Options with every field set to a value that is not
// its zero value, found by reflection, so that a field added to Options is set
// here without the test being edited.
func filledOptions(t *testing.T) Options {
	t.Helper()
	var o Options
	v := reflect.ValueOf(&o).Elem()
	for i := range v.NumField() {
		field := v.Field(i)
		switch field.Kind() {
		case reflect.String:
			field.SetString("x-" + v.Type().Field(i).Name)
		case reflect.Int:
			field.SetInt(int64(i + 1))
		case reflect.Bool:
			field.SetBool(true)
		default:
			t.Fatalf("Options.%s has the kind %v, which this test does not fill", v.Type().Field(i).Name, field.Kind())
		}
	}
	// Tier and Mode have to be real constants for the conversions below.
	o.Tier, o.Mode = TierQuick, ModeCreative
	return o
}

func TestOptionsHaveSevenFields(t *testing.T) {
	t.Parallel()
	want := []string{"Model", "Tier", "Effort", "Mode", "Steering", "MaxOutputTokens", "WebSearch"}
	typ := reflect.TypeOf(Options{})
	if typ.NumField() != len(want) {
		t.Fatalf("Options has %d fields, want %d", typ.NumField(), len(want))
	}
	for i, name := range want {
		if typ.Field(i).Name != name {
			t.Errorf("field %d is %s, want %s", i, typ.Field(i).Name, name)
		}
	}
}

func TestMergeOptionsLaterNonZeroFieldWins(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		first, later Options
		want         Options
	}{
		{"two zero values stay zero", Options{}, Options{}, Options{}},
		{"a later model replaces an earlier one", Options{Model: "a"}, Options{Model: "b"}, Options{Model: "b"}},
		{"a later zero model leaves the earlier one", Options{Model: "a"}, Options{}, Options{Model: "a"}},
		{"an earlier zero model takes the later one", Options{}, Options{Model: "b"}, Options{Model: "b"}},
		{"a later tier replaces an earlier one", Options{Tier: TierSmart}, Options{Tier: TierQuick}, Options{Tier: TierQuick}},
		{"a later zero tier leaves the earlier one", Options{Tier: TierSmart}, Options{}, Options{Tier: TierSmart}},
		{"a later effort replaces an earlier one", Options{Effort: EffortLow}, Options{Effort: EffortMax}, Options{Effort: EffortMax}},
		{"a later zero effort leaves the earlier one", Options{Effort: EffortLow}, Options{}, Options{Effort: EffortLow}},
		{"a later mode replaces an earlier one", Options{Mode: ModeStrict}, Options{Mode: ModeCreative}, Options{Mode: ModeCreative}},
		{"a later zero mode leaves the earlier one", Options{Mode: ModeStrict}, Options{}, Options{Mode: ModeStrict}},
		{"later steering replaces earlier steering", Options{Steering: "a"}, Options{Steering: "b"}, Options{Steering: "b"}},
		{"later empty steering leaves the earlier", Options{Steering: "a"}, Options{}, Options{Steering: "a"}},
		{"a later ceiling replaces an earlier one", Options{MaxOutputTokens: 100}, Options{MaxOutputTokens: 200}, Options{MaxOutputTokens: 200}},
		{"a later zero ceiling leaves the earlier one", Options{MaxOutputTokens: 100}, Options{}, Options{MaxOutputTokens: 100}},
		{"a later negative ceiling replaces the earlier one, and is refused by check", Options{MaxOutputTokens: 100}, Options{MaxOutputTokens: -1}, Options{MaxOutputTokens: -1}},
		{"web search turned on stays on", Options{WebSearch: true}, Options{}, Options{WebSearch: true}},
		{"web search cannot be turned off by a later value", Options{WebSearch: true}, Options{WebSearch: false, Model: "m"}, Options{WebSearch: true, Model: "m"}},
		{"web search is turned on by a later value", Options{}, Options{WebSearch: true}, Options{WebSearch: true}},
		{"fields that do not meet are all kept", Options{Model: "a", Tier: TierFast}, Options{Effort: EffortHigh, Steering: "s"}, Options{Model: "a", Tier: TierFast, Effort: EffortHigh, Steering: "s"}},
		{"every field of the later value wins", Options{Model: "a", Tier: TierFast, Effort: EffortLow, Mode: ModeStrict, Steering: "a", MaxOutputTokens: 1}, Options{Model: "b", Tier: TierSmart, Effort: EffortMax, Mode: ModeCreative, Steering: "b", MaxOutputTokens: 2, WebSearch: true}, Options{Model: "b", Tier: TierSmart, Effort: EffortMax, Mode: ModeCreative, Steering: "b", MaxOutputTokens: 2, WebSearch: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.first.merge(c.later); got != c.want {
				t.Errorf("merge = %+v, want %+v", got, c.want)
			}
			if got := mergeOptions([]Options{c.first, c.later}); got != c.want {
				t.Errorf("mergeOptions = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestMergeOptionsOfAnyNumberOfValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []Options
		want Options
	}{
		{"none is the zero value", nil, Options{}},
		{"an empty slice is the zero value", []Options{}, Options{}},
		{"one value is used as it is", []Options{{Model: "a", WebSearch: true}}, Options{Model: "a", WebSearch: true}},
		{"one zero value is the zero value", []Options{{}}, Options{}},
		{"three values merge left to right", []Options{{Model: "a", Steering: "x"}, {Model: "b"}, {Steering: "y", Tier: TierFast}}, Options{Model: "b", Steering: "y", Tier: TierFast}},
		{"a zero value in the middle changes nothing", []Options{{Model: "a"}, {}, {Effort: EffortLow}}, Options{Model: "a", Effort: EffortLow}},
		{"the last non-zero value wins", []Options{{Model: "a"}, {Model: "b"}, {Model: "c"}, {}}, Options{Model: "c"}},
		{"web search stays on through later values", []Options{{WebSearch: true}, {}, {Model: "m"}}, Options{WebSearch: true, Model: "m"}},
		{"ten values merge", []Options{{Model: "1"}, {Model: "2"}, {Model: "3"}, {Model: "4"}, {Model: "5"}, {Model: "6"}, {Model: "7"}, {Model: "8"}, {Model: "9"}, {Tier: TierQuick}}, Options{Model: "9", Tier: TierQuick}},
		{"merging is not commutative", []Options{{Model: "b"}, {Model: "a"}}, Options{Model: "a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mergeOptions(c.in); got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestMergeOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	full := filledOptions(t)
	typ := reflect.TypeOf(Options{})
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		t.Run("a later value that sets only "+name+" sets it", func(t *testing.T) {
			var only Options
			reflect.ValueOf(&only).Elem().Field(i).Set(reflect.ValueOf(full).Field(i))
			got := Options{}.merge(only)
			if !reflect.DeepEqual(got, only) {
				t.Errorf("merge lost %s: got %+v, want %+v", name, got, only)
			}
			// And over a value that has every field set, it replaces just that one.
			base := Options{Model: "base", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "base", MaxOutputTokens: 7, WebSearch: true}
			merged := base.merge(only)
			if reflect.ValueOf(merged).Field(i).Interface() != reflect.ValueOf(full).Field(i).Interface() {
				t.Errorf("%s of the later value did not win", name)
			}
		})
	}
	t.Run("a value with every field set replaces them all", func(t *testing.T) {
		if got := (Options{Model: "b"}).merge(full); got != full {
			t.Errorf("got %+v, want %+v", got, full)
		}
	})
}

func TestTierConvertsBothWays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		tier     Tier
		internal types.Speed
	}{
		{0, types.TierUnset},
		{TierSmart, types.Smart},
		{TierFast, types.Fast},
		{TierQuick, types.Quick},
	}
	for _, c := range cases {
		t.Run(c.internal.String(), func(t *testing.T) {
			if got := c.tier.internal(); got != c.internal {
				t.Errorf("internal() = %v, want %v", got, c.internal)
			}
			if got := tierFromInternal(c.internal); got != c.tier {
				t.Errorf("tierFromInternal() = %v, want %v", got, c.tier)
			}
		})
	}
	t.Run("the constants are numbered 1, 2 and 3", func(t *testing.T) {
		if TierSmart != 1 || TierFast != 2 || TierQuick != 3 {
			t.Errorf("got %d %d %d", TierSmart, TierFast, TierQuick)
		}
	})
	t.Run("a value that is not a tier keeps its number", func(t *testing.T) {
		for _, n := range []int{4, 9, -1, 100} {
			if got := Tier(n).internal(); int(got) != n {
				t.Errorf("Tier(%d).internal() = %d", n, got)
			}
			if got := tierFromInternal(types.Speed(n)); int(got) != n {
				t.Errorf("tierFromInternal(%d) = %d", n, got)
			}
		}
	})
}

func TestModeConvertsBothWays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode     Mode
		internal types.Mode
	}{
		{0, types.ModeUnset},
		{ModeStrict, types.Strict},
		{ModeTransform, types.TransformMode},
		{ModeCreative, types.Creative},
	}
	for _, c := range cases {
		t.Run(c.internal.String(), func(t *testing.T) {
			if got := c.mode.internal(); got != c.internal {
				t.Errorf("internal() = %v, want %v", got, c.internal)
			}
			if got := modeFromInternal(c.internal); got != c.mode {
				t.Errorf("modeFromInternal() = %v, want %v", got, c.mode)
			}
		})
	}
	t.Run("the constants are numbered 1, 2 and 3", func(t *testing.T) {
		if ModeStrict != 1 || ModeTransform != 2 || ModeCreative != 3 {
			t.Errorf("got %d %d %d", ModeStrict, ModeTransform, ModeCreative)
		}
	})
	t.Run("a value that is not a mode keeps its number", func(t *testing.T) {
		for _, n := range []int{4, 9, -1, 100} {
			if got := Mode(n).internal(); int(got) != n {
				t.Errorf("Mode(%d).internal() = %d", n, got)
			}
			if got := modeFromInternal(types.Mode(n)); int(got) != n {
				t.Errorf("modeFromInternal(%d) = %d", n, got)
			}
		}
	})
}

func TestEffortConstantsAreTheInternalWords(t *testing.T) {
	t.Parallel()
	cases := []struct {
		effort   Effort
		internal types.Effort
	}{
		{"", types.EffortDefault},
		{EffortNone, types.EffortNone},
		{EffortMinimal, types.EffortMinimal},
		{EffortLow, types.EffortLow},
		{EffortMedium, types.EffortMedium},
		{EffortHigh, types.EffortHigh},
		{EffortXHigh, types.EffortXHigh},
		{EffortMax, types.EffortMax},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%q", string(c.effort)), func(t *testing.T) {
			if string(c.effort) != string(c.internal) {
				t.Errorf("%q is not the internal word %q", c.effort, c.internal)
			}
			got := Options{Effort: c.effort}.common()
			if got.Effort != c.internal {
				t.Errorf("common().Effort = %q, want %q", got.Effort, c.internal)
			}
			if back := optionsFromCommon(got); back.Effort != c.effort {
				t.Errorf("optionsFromCommon().Effort = %q, want %q", back.Effort, c.effort)
			}
		})
	}
}

func TestCommonConvertsEverySettingBothWays(t *testing.T) {
	t.Parallel()
	t.Run("the zero Options is the zero settings", func(t *testing.T) {
		if got := (Options{}).common(); got != (bind.Common{}) {
			t.Errorf("got %+v", got)
		}
		if got := optionsFromCommon(bind.Common{}); got != (Options{}) {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("every field is carried", func(t *testing.T) {
		full := filledOptions(t)
		got := full.common()
		want := bind.Common{
			Model: "x-Model", Tier: types.Quick, Effort: "x-Effort", Mode: types.Creative,
			Steering: "x-Steering", MaxOutputTokens: full.MaxOutputTokens, WebSearch: true,
		}
		if got != want {
			t.Errorf("common() = %+v, want %+v", got, want)
		}
		if back := optionsFromCommon(got); back != full {
			t.Errorf("round trip = %+v, want %+v", back, full)
		}
	})
	t.Run("the settings have the seven fields of Options", func(t *testing.T) {
		if reflect.TypeOf(bind.Common{}).NumField() != reflect.TypeOf(Options{}).NumField() {
			t.Error("bind.Common and Options have a different number of fields")
		}
	})
	typ := reflect.TypeOf(Options{})
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		t.Run("a value that sets only "+name+" reaches the settings", func(t *testing.T) {
			var only Options
			reflect.ValueOf(&only).Elem().Field(i).Set(reflect.ValueOf(filledOptions(t)).Field(i))
			if (only.common() == bind.Common{}) {
				t.Errorf("%s did not reach the settings", name)
			}
			if optionsFromCommon(only.common()) != only {
				t.Errorf("%s did not come back", name)
			}
		})
	}
}

func TestOptionsCheckRefusesWhatNoCallCanUse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		options Options
		message string
	}{
		{"the zero value", Options{}, ""},
		{"every real setting", Options{Model: "m", Tier: TierSmart, Effort: EffortHigh, Mode: ModeStrict, Steering: "s", MaxOutputTokens: 100, WebSearch: true}, ""},
		{"a ceiling of one token", Options{MaxOutputTokens: 1}, ""},
		{"a negative ceiling", Options{MaxOutputTokens: -1}, "MaxOutputTokens must not be negative"},
		{"a very negative ceiling", Options{MaxOutputTokens: -1 << 30}, "MaxOutputTokens must not be negative"},
		{"a tier past the last", Options{Tier: 4}, "Tier is not one of TierSmart, TierFast and TierQuick"},
		{"a negative tier", Options{Tier: -1}, "Tier is not one of TierSmart, TierFast and TierQuick"},
		{"a mode past the last", Options{Mode: 4}, "Mode is not one of ModeStrict, ModeTransform and ModeCreative"},
		{"a negative mode", Options{Mode: -2}, "Mode is not one of ModeStrict, ModeTransform and ModeCreative"},
		{"an effort of a word nobody knows is left to the route", Options{Effort: "turbo"}, ""},
		{"a negative ceiling is reported before a bad tier", Options{MaxOutputTokens: -5, Tier: 9}, "MaxOutputTokens must not be negative"},
		{"a bad tier is reported before a bad mode", Options{Tier: 9, Mode: 9}, "Tier is not one of TierSmart, TierFast and TierQuick"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.options.check("classify")
			if c.message == "" {
				if err != nil {
					t.Fatalf("got %v, want nil", err)
				}
				return
			}
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("got %v, want an *Error", err)
			}
			if e.Kind != ErrConfiguration || e.Op != "classify" || e.Message != c.message {
				t.Errorf("got %+v", e)
			}
			if !errors.Is(err, ErrConfiguration) {
				t.Errorf("errors.Is(err, ErrConfiguration) = false")
			}
			if want := "classify: configuration: " + c.message; e.Error() != want {
				t.Errorf("Error() = %q, want %q", e.Error(), want)
			}
		})
	}
}

func TestMergedOptionsAreCheckedAfterTheMerge(t *testing.T) {
	t.Parallel()
	good := mergeOptions([]Options{{MaxOutputTokens: -1}, {MaxOutputTokens: 5}})
	if err := good.check("x"); err != nil {
		t.Errorf("a later valid ceiling should replace an earlier invalid one: %v", err)
	}
	bad := mergeOptions([]Options{{MaxOutputTokens: 5}, {MaxOutputTokens: -1}})
	if err := bad.check("x"); err == nil {
		t.Error("a later negative ceiling should be refused")
	}
}
