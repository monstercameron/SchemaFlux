package bind

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// filledOpOptions is a types.OpOptions with every field that is not one of the
// seven common settings set to a value, so that a test can see that Apply left
// each of them alone, and with the seven set to values a Common can replace.
func filledOpOptions() types.OpOptions {
	return types.OpOptions{
		Steering:        "old steering",
		Threshold:       0.25,
		Mode:            types.Strict,
		ExactFields:     true,
		CompleteFields:  true,
		Intelligence:    types.Smart,
		Context:         context.WithValue(context.Background(), struct{}{}, 1),
		RequestID:       "req_old",
		CorrelationID:   "cor_old",
		JSONSchema:      map[string]any{"type": "object"},
		SchemaName:      "Name",
		Model:           "old-model",
		Effort:          types.EffortLow,
		WebSearch:       false,
		SchemaID:        "id",
		ResponseFormat:  "json",
		CacheIdentity:   "cache",
		MaxOutputTokens: 100,
	}
}

func TestCommonHasTheSevenSettingsOfOptions(t *testing.T) {
	want := []struct {
		name string
		typ  reflect.Type
	}{
		{"Model", reflect.TypeFor[string]()},
		{"Tier", reflect.TypeFor[types.Speed]()},
		{"Effort", reflect.TypeFor[types.Effort]()},
		{"Mode", reflect.TypeFor[types.Mode]()},
		{"Steering", reflect.TypeFor[string]()},
		{"MaxOutputTokens", reflect.TypeFor[int]()},
		{"WebSearch", reflect.TypeFor[bool]()},
	}
	typ := reflect.TypeFor[Common]()
	if typ.NumField() != len(want) {
		t.Fatalf("Common has %d fields, want %d", typ.NumField(), len(want))
	}
	for i, w := range want {
		f := typ.Field(i)
		if f.Name != w.name || f.Type != w.typ {
			t.Errorf("field %d is %s %v, want %s %v", i, f.Name, f.Type, w.name, w.typ)
		}
	}
}

func TestApplyAssignsEachSettingOnItsOwn(t *testing.T) {
	cases := []struct {
		name   string
		common Common
		check  func(t *testing.T, got, before types.OpOptions)
	}{
		{"Model", Common{Model: "new-model"}, func(t *testing.T, got, before types.OpOptions) {
			if got.Model != "new-model" {
				t.Errorf("Model = %q", got.Model)
			}
		}},
		{"Tier", Common{Tier: types.Quick}, func(t *testing.T, got, before types.OpOptions) {
			if got.Intelligence != types.Quick {
				t.Errorf("Intelligence = %v, want the Tier", got.Intelligence)
			}
		}},
		{"Effort", Common{Effort: types.EffortHigh}, func(t *testing.T, got, before types.OpOptions) {
			if got.Effort != types.EffortHigh {
				t.Errorf("Effort = %q", got.Effort)
			}
		}},
		{"Mode", Common{Mode: types.Creative}, func(t *testing.T, got, before types.OpOptions) {
			if got.Mode != types.Creative {
				t.Errorf("Mode = %v", got.Mode)
			}
		}},
		{"Steering", Common{Steering: "new steering"}, func(t *testing.T, got, before types.OpOptions) {
			if got.Steering != "new steering" {
				t.Errorf("Steering = %q", got.Steering)
			}
		}},
		{"MaxOutputTokens", Common{MaxOutputTokens: 7}, func(t *testing.T, got, before types.OpOptions) {
			if got.MaxOutputTokens != 7 {
				t.Errorf("MaxOutputTokens = %d", got.MaxOutputTokens)
			}
		}},
		{"WebSearch", Common{WebSearch: true}, func(t *testing.T, got, before types.OpOptions) {
			if !got.WebSearch {
				t.Error("WebSearch was not turned on")
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := filledOpOptions()
			got := filledOpOptions()
			c.common.Apply(&got)
			c.check(t, got, before)
			// Every other field of the options is what it was, the other six
			// settings included.
			want := before
			switch c.name {
			case "Model":
				want.Model = got.Model
			case "Tier":
				want.Intelligence = got.Intelligence
			case "Effort":
				want.Effort = got.Effort
			case "Mode":
				want.Mode = got.Mode
			case "Steering":
				want.Steering = got.Steering
			case "MaxOutputTokens":
				want.MaxOutputTokens = got.MaxOutputTokens
			case "WebSearch":
				want.WebSearch = got.WebSearch
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Apply of %s changed another field:\n got %+v\nwant %+v", c.name, got, want)
			}
		})
	}
}

func TestApplyOfTheZeroCommonChangesNothing(t *testing.T) {
	got := filledOpOptions()
	Common{}.Apply(&got)
	if !reflect.DeepEqual(got, filledOpOptions()) {
		t.Errorf("a zero Common changed the options: %+v", got)
	}
	var empty types.OpOptions
	Common{}.Apply(&empty)
	if !reflect.DeepEqual(empty, types.OpOptions{}) {
		t.Errorf("a zero Common wrote into zero options: %+v", empty)
	}
}

func TestApplyOfAFullCommonReplacesAllSevenAndNothingElse(t *testing.T) {
	full := Common{
		Model: "m", Tier: types.Fast, Effort: types.EffortMax, Mode: types.TransformMode,
		Steering: "s", MaxOutputTokens: 9, WebSearch: true,
	}
	got := filledOpOptions()
	full.Apply(&got)
	want := filledOpOptions()
	want.Model, want.Intelligence, want.Effort, want.Mode = "m", types.Fast, types.EffortMax, types.TransformMode
	want.Steering, want.MaxOutputTokens, want.WebSearch = "s", 9, true
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestApplyNeverTurnsAWebSearchOff(t *testing.T) {
	dst := types.OpOptions{WebSearch: true}
	Common{WebSearch: false}.Apply(&dst)
	if !dst.WebSearch {
		t.Error("a false WebSearch turned off a true one")
	}
}

func TestApplyTwiceIsApplyOnce(t *testing.T) {
	c := Common{Model: "m", Tier: types.Smart, Effort: types.EffortLow, Mode: types.Strict, Steering: "s", MaxOutputTokens: 3, WebSearch: true}
	once, twice := filledOpOptions(), filledOpOptions()
	c.Apply(&once)
	c.Apply(&twice)
	c.Apply(&twice)
	if !reflect.DeepEqual(once, twice) {
		t.Errorf("applying twice differs from once:\n%+v\n%+v", once, twice)
	}
}

func TestApplyLaterCommonOverEarlierKeepsWhatTheLaterLeavesZero(t *testing.T) {
	dst := types.OpOptions{}
	Common{Model: "first", Steering: "first", MaxOutputTokens: 1}.Apply(&dst)
	Common{Model: "second", Effort: types.EffortMedium}.Apply(&dst)
	want := types.OpOptions{Model: "second", Steering: "first", MaxOutputTokens: 1, Effort: types.EffortMedium}
	if !reflect.DeepEqual(dst, want) {
		t.Errorf("got %+v, want %+v", dst, want)
	}
}

func TestApplyWritesANegativeCeilingForCheckToHaveRefused(t *testing.T) {
	var dst types.OpOptions
	Common{MaxOutputTokens: -1}.Apply(&dst)
	if dst.MaxOutputTokens != -1 {
		t.Errorf("MaxOutputTokens = %d: a non-zero ceiling is written as it is", dst.MaxOutputTokens)
	}
}

func TestApplyAllocatesNothing(t *testing.T) {
	c := Common{Model: "m", Tier: types.Fast, Effort: types.EffortMax, Mode: types.Creative, Steering: "s", MaxOutputTokens: 9, WebSearch: true}
	var dst types.OpOptions
	if got := testing.AllocsPerRun(100, func() { c.Apply(&dst) }); got != 0 {
		t.Errorf("Apply allocates %v times, want 0", got)
	}
}

func TestCheckRefusesWhatNoCallCanUse(t *testing.T) {
	cases := []struct {
		name    string
		common  Common
		wantMsg string
	}{
		{"the zero Common", Common{}, ""},
		{"every setting valid", Common{Model: "m", Tier: types.Smart, Effort: types.EffortHigh, Mode: types.Strict, Steering: "s", MaxOutputTokens: 5, WebSearch: true}, ""},
		{"the fast tier", Common{Tier: types.Fast}, ""},
		{"the quick tier", Common{Tier: types.Quick}, ""},
		{"the transform mode", Common{Mode: types.TransformMode}, ""},
		{"the creative mode", Common{Mode: types.Creative}, ""},
		{"an effort word the route may not take", Common{Effort: "bogus"}, ""},
		{"a negative ceiling", Common{MaxOutputTokens: -1}, "MaxOutputTokens must not be negative"},
		{"a very negative ceiling", Common{MaxOutputTokens: -1 << 40}, "MaxOutputTokens must not be negative"},
		{"a tier that is not one", Common{Tier: types.Speed(99)}, "Tier is not one of TierSmart, TierFast and TierQuick"},
		{"a negative tier", Common{Tier: types.Speed(-1)}, "Tier is not one of TierSmart, TierFast and TierQuick"},
		{"a mode that is not one", Common{Mode: types.Mode(99)}, "Mode is not one of ModeStrict, ModeTransform and ModeCreative"},
		{"a negative mode", Common{Mode: types.Mode(-1)}, "Mode is not one of ModeStrict, ModeTransform and ModeCreative"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			unregister()
			t.Cleanup(unregister)
			err := c.common.Check("classify")
			if c.wantMsg == "" {
				if err != nil {
					t.Fatalf("Check = %v, want nil", err)
				}
				return
			}
			var oe *types.OperationError
			if !errors.As(err, &oe) {
				t.Fatalf("Check = %T %v, want an internal error before registration", err, err)
			}
			if oe.Kind != types.KindConfiguration || oe.Op != "classify" || oe.Message != c.wantMsg {
				t.Errorf("Check = kind %v op %q message %q", oe.Kind, oe.Op, oe.Message)
			}
		})
	}
}

func TestCheckGivesTheRootsErrorWhenTheRootHasRegistered(t *testing.T) {
	registerStandIn(t)
	err := Common{MaxOutputTokens: -5}.Check("filter")
	var c *converted
	if !errors.As(err, &c) {
		t.Fatalf("Check = %T, want what Convert made", err)
	}
	if c.op != "filter" {
		t.Errorf("Convert was given op %q, want filter", c.op)
	}
	var oe *types.OperationError
	if !errors.As(err, &oe) || oe.Kind != types.KindConfiguration {
		t.Errorf("the converted error does not wrap the configuration error: %v", err)
	}
}

func TestCheckOfAValidCommonAllocatesNothing(t *testing.T) {
	c := Common{Model: "m", Tier: types.Smart, Effort: types.EffortHigh, Mode: types.Strict, Steering: "s", MaxOutputTokens: 5, WebSearch: true}
	if got := testing.AllocsPerRun(100, func() { _ = c.Check("op") }); got != 0 {
		t.Errorf("Check allocates %v times, want 0", got)
	}
}
