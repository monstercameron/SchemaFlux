package bind

import (
	"context"
	"reflect"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// filledHalves is the two halves of a flat options type of internal/ops as its
// constructor and a caller leave them: every field set to something, so that a
// test sees which ones ApplyHalves wrote and which it left alone.
func filledHalves() (ops.CommonOptions, types.OpOptions) {
	common := ops.CommonOptions{
		Steering: "old common steering", Threshold: 0.5, Mode: types.TransformMode, ExactFields: true, CompleteFields: true,
		Intelligence: types.Fast, Model: "old-common-model", Effort: types.EffortLow,
		Context: context.WithValue(context.Background(), struct{}{}, 2), Timeout: 7, RequestID: "req", CorrelationID: "cor",
	}
	return common, filledOpOptions()
}

func TestApplyHalvesWritesEachSettingToEveryHalfThatHasTheField(t *testing.T) {
	cases := []struct {
		name string
		c    Common
		// both says the setting is on both halves; otherwise only the embedded one
		// has the field.
		both  func(t *testing.T, common ops.CommonOptions, embedded types.OpOptions)
		alone func(t *testing.T, embedded types.OpOptions)
	}{
		{"the model", Common{Model: "gpt-6-luna"}, func(t *testing.T, c ops.CommonOptions, e types.OpOptions) {
			if c.Model != "gpt-6-luna" || e.Model != "gpt-6-luna" {
				t.Errorf("models = %q and %q", c.Model, e.Model)
			}
		}, nil},
		{"the tier", Common{Tier: types.Quick}, func(t *testing.T, c ops.CommonOptions, e types.OpOptions) {
			if c.Intelligence != types.Quick || e.Intelligence != types.Quick {
				t.Errorf("tiers = %v and %v", c.Intelligence, e.Intelligence)
			}
		}, nil},
		{"the effort", Common{Effort: types.EffortHigh}, func(t *testing.T, c ops.CommonOptions, e types.OpOptions) {
			if c.Effort != types.EffortHigh || e.Effort != types.EffortHigh {
				t.Errorf("efforts = %v and %v", c.Effort, e.Effort)
			}
		}, nil},
		{"the mode", Common{Mode: types.Creative}, func(t *testing.T, c ops.CommonOptions, e types.OpOptions) {
			if c.Mode != types.Creative || e.Mode != types.Creative {
				t.Errorf("modes = %v and %v", c.Mode, e.Mode)
			}
		}, nil},
		{"the steering", Common{Steering: "be brief"}, func(t *testing.T, c ops.CommonOptions, e types.OpOptions) {
			if c.Steering != "be brief" || e.Steering != "be brief" {
				t.Errorf("steering = %q and %q", c.Steering, e.Steering)
			}
		}, nil},
		{"the ceiling, which only the embedded half has", Common{MaxOutputTokens: 321}, nil, func(t *testing.T, e types.OpOptions) {
			if e.MaxOutputTokens != 321 {
				t.Errorf("ceiling = %d", e.MaxOutputTokens)
			}
		}},
		{"web search, which only the embedded half has", Common{WebSearch: true}, nil, func(t *testing.T, e types.OpOptions) {
			if !e.WebSearch {
				t.Error("web search was not turned on")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			common, embedded := filledHalves()
			beforeCommon, beforeEmbedded := common, embedded
			tc.c.ApplyHalves(&common, &embedded)
			if tc.both != nil {
				tc.both(t, common, embedded)
			}
			if tc.alone != nil {
				tc.alone(t, embedded)
			}

			// What the setting does not name is as it was, in both halves.
			wantEmbedded := beforeEmbedded
			tc.c.Apply(&wantEmbedded)
			wantCommon := beforeCommon
			if tc.both != nil {
				wantCommon.Model, wantCommon.Intelligence, wantCommon.Effort, wantCommon.Mode, wantCommon.Steering =
					common.Model, common.Intelligence, common.Effort, common.Mode, common.Steering
			}
			if !reflect.DeepEqual(common, wantCommon) {
				t.Errorf("the CommonOptions half changed beyond the setting:\n got %+v\nwant %+v", common, wantCommon)
			}
			if !reflect.DeepEqual(embedded, wantEmbedded) {
				t.Errorf("the embedded half changed beyond the setting:\n got %+v\nwant %+v", embedded, wantEmbedded)
			}
		})
	}
}

func TestApplyHalvesBeatsTheConstructorsDefaultsOnTheCommonHalf(t *testing.T) {
	// The internal merge lets the CommonOptions half win, and NewXOptions sets
	// Mode and Intelligence on it. A setting written only to the embedded half
	// would be covered by those defaults.
	opts := ops.NewClassifyOptions()
	Common{Mode: types.Strict, Tier: types.Smart}.ApplyHalves(&opts.CommonOptions, &opts.OpOptions)
	if opts.CommonOptions.Mode != types.Strict || opts.CommonOptions.Intelligence != types.Smart {
		t.Errorf("the constructor's defaults survived on the CommonOptions half: %v %v", opts.CommonOptions.Mode, opts.CommonOptions.Intelligence)
	}
	if opts.OpOptions.Mode != types.Strict || opts.OpOptions.Intelligence != types.Smart {
		t.Errorf("the embedded half: %v %v", opts.OpOptions.Mode, opts.OpOptions.Intelligence)
	}
}

func TestApplyHalvesOfTheZeroCommonChangesNeitherHalf(t *testing.T) {
	common, embedded := filledHalves()
	beforeCommon, beforeEmbedded := common, embedded
	Common{}.ApplyHalves(&common, &embedded)
	if !reflect.DeepEqual(common, beforeCommon) || !reflect.DeepEqual(embedded, beforeEmbedded) {
		t.Error("a zero Common changed an options value: an empty setting must never blank a default")
	}
}

func TestApplyHalvesOfAFullCommonReplacesAllSevenAndNothingElse(t *testing.T) {
	full := Common{Model: "m", Tier: types.Smart, Effort: types.EffortMax, Mode: types.Strict, Steering: "s", MaxOutputTokens: 9, WebSearch: true}
	common, embedded := filledHalves()
	beforeCommon, beforeEmbedded := common, embedded
	full.ApplyHalves(&common, &embedded)

	wantCommon := beforeCommon
	wantCommon.Model, wantCommon.Intelligence, wantCommon.Effort, wantCommon.Mode, wantCommon.Steering = "m", types.Smart, types.EffortMax, types.Strict, "s"
	wantEmbedded := beforeEmbedded
	full.Apply(&wantEmbedded)
	if !reflect.DeepEqual(common, wantCommon) {
		t.Errorf("CommonOptions half:\n got %+v\nwant %+v", common, wantCommon)
	}
	if !reflect.DeepEqual(embedded, wantEmbedded) {
		t.Errorf("embedded half:\n got %+v\nwant %+v", embedded, wantEmbedded)
	}
	if embedded.MaxOutputTokens != 9 || !embedded.WebSearch {
		t.Errorf("the two settings only the embedded half has: %d, %v", embedded.MaxOutputTokens, embedded.WebSearch)
	}
	// Fields that are not one of the seven are untouched.
	if common.Threshold != 0.5 || common.Timeout != 7 || common.RequestID != "req" || !common.ExactFields || embedded.SchemaName != "Name" || embedded.CacheIdentity != "cache" {
		t.Error("ApplyHalves wrote a field that is not one of the seven")
	}
}

func TestApplyHalvesSkipsAHalfThatIsNil(t *testing.T) {
	full := Common{Model: "m", Steering: "s", MaxOutputTokens: 3}
	common, embedded := filledHalves()
	full.ApplyHalves(&common, nil)
	if common.Model != "m" || common.Steering != "s" {
		t.Errorf("the CommonOptions half was not written: %+v", common)
	}
	full.ApplyHalves(nil, &embedded)
	if embedded.Model != "m" || embedded.MaxOutputTokens != 3 {
		t.Errorf("the embedded half was not written: %+v", embedded)
	}
	full.ApplyHalves(nil, nil)
}

func TestApplyHalvesTwiceIsOnce(t *testing.T) {
	full := Common{Model: "m", Tier: types.Fast, Mode: types.Creative, Steering: "s", MaxOutputTokens: 2, WebSearch: true}
	common, embedded := filledHalves()
	full.ApplyHalves(&common, &embedded)
	onceCommon, onceEmbedded := common, embedded
	full.ApplyHalves(&common, &embedded)
	if !reflect.DeepEqual(common, onceCommon) || !reflect.DeepEqual(embedded, onceEmbedded) {
		t.Error("a second ApplyHalves changed something")
	}
}

func TestApplyHalvesNeverTurnsAWebSearchOff(t *testing.T) {
	common, embedded := filledHalves()
	embedded.WebSearch = true
	Common{}.ApplyHalves(&common, &embedded)
	if !embedded.WebSearch {
		t.Error("a false WebSearch turned off a true one")
	}
}

func TestApplyHalvesAllocatesNothing(t *testing.T) {
	c := Common{Model: "m", Tier: types.Smart, Effort: types.EffortHigh, Mode: types.Strict, Steering: "s", MaxOutputTokens: 5, WebSearch: true}
	common, embedded := filledHalves()
	if got := testing.AllocsPerRun(100, func() { c.ApplyHalves(&common, &embedded) }); got != 0 {
		t.Errorf("ApplyHalves allocates %v times, want 0", got)
	}
}
