package schemaflux

import (
	"context"
	"errors"
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

func TestMergeValuesOfNoValuesIsTheZeroValue(t *testing.T) {
	t.Parallel()
	got := mergeValues[ExtractOptions](nil, func(earlier, later ExtractOptions) ExtractOptions {
		t.Error("the merge function was called for no values")
		return earlier
	})
	if !reflect.DeepEqual(got, ExtractOptions{}) {
		t.Errorf("got %+v, want the zero value", got)
	}
}

func TestMergeValuesOfOneValueReturnsItAsItIsWithoutCallingTheMerge(t *testing.T) {
	t.Parallel()
	one := ExtractOptions{StrictSchema: true, SchemaHints: map[string]string{"a": "b"}}
	got := mergeValues([]ExtractOptions{one}, func(earlier, later ExtractOptions) ExtractOptions {
		t.Error("the merge function was called for one value")
		return earlier
	})
	if !reflect.DeepEqual(got, one) {
		t.Errorf("got %+v, want %+v", got, one)
	}
}

func TestMergeValuesOfSeveralValuesLaysEachOverTheLastFromLeftToRight(t *testing.T) {
	t.Parallel()
	type value struct{ trail string }
	lay := func(earlier, later value) value { return value{earlier.trail + later.trail} }
	cases := []struct {
		name   string
		values []value
		want   string
	}{
		{"two", []value{{"a"}, {"b"}}, "ab"},
		{"three", []value{{"a"}, {"b"}, {"c"}}, "abc"},
		{"four", []value{{"1"}, {"2"}, {"3"}, {"4"}}, "1234"},
		{"empty ones in between", []value{{"a"}, {}, {"c"}}, "ac"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := mergeValues(tc.values, lay).trail; got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMergeValuesDoesNotChangeTheValuesItIsGiven(t *testing.T) {
	t.Parallel()
	values := []ExtractOptions{{Model: "first"}, {Model: "second"}}
	mergeValues(values, func(earlier, later ExtractOptions) ExtractOptions {
		earlier.Model = later.Model
		return earlier
	})
	if values[0].Model != "first" || values[1].Model != "second" {
		t.Errorf("the slice was changed: %+v", values)
	}
}

func TestLayAssignsOnlyAValueThatIsNotZero(t *testing.T) {
	t.Parallel()
	t.Run("string", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct{ dst, v, want string }{
			{"", "", ""}, {"keep", "", "keep"}, {"", "set", "set"}, {"old", "new", "new"}, {"x", " ", " "},
		} {
			dst := tc.dst
			lay(&dst, tc.v)
			if dst != tc.want {
				t.Errorf("lay(%q, %q) left %q, want %q", tc.dst, tc.v, dst, tc.want)
			}
		}
	})
	t.Run("int", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct{ dst, v, want int }{
			{0, 0, 0}, {5, 0, 5}, {0, 7, 7}, {5, 7, 7}, {5, -1, -1},
		} {
			dst := tc.dst
			lay(&dst, tc.v)
			if dst != tc.want {
				t.Errorf("lay(%d, %d) left %d, want %d", tc.dst, tc.v, dst, tc.want)
			}
		}
	})
	t.Run("float", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct{ dst, v, want float64 }{
			{0, 0, 0}, {0.5, 0, 0.5}, {0, 0.25, 0.25}, {0.5, 0.25, 0.25},
		} {
			dst := tc.dst
			lay(&dst, tc.v)
			if dst != tc.want {
				t.Errorf("lay(%v, %v) left %v, want %v", tc.dst, tc.v, dst, tc.want)
			}
		}
	})
	t.Run("bool", func(t *testing.T) {
		t.Parallel()
		on, off := true, false
		lay(&on, false)
		if !on {
			t.Error("false turned a true off")
		}
		lay(&off, true)
		if !off {
			t.Error("true did not turn a false on")
		}
	})
}

func TestLaySliceAndLayMapAssignAnythingThatIsNotNil(t *testing.T) {
	t.Parallel()
	t.Run("a nil slice leaves the destination", func(t *testing.T) {
		t.Parallel()
		dst := []string{"keep"}
		laySlice(&dst, nil)
		if !reflect.DeepEqual(dst, []string{"keep"}) {
			t.Errorf("got %v", dst)
		}
	})
	t.Run("an empty slice that is not nil replaces it", func(t *testing.T) {
		t.Parallel()
		dst := []string{"old"}
		laySlice(&dst, []string{})
		if dst == nil || len(dst) != 0 {
			t.Errorf("got %#v, want an empty slice that is not nil", dst)
		}
	})
	t.Run("a slice replaces nil", func(t *testing.T) {
		t.Parallel()
		var dst []int
		laySlice(&dst, []int{1, 2})
		if !reflect.DeepEqual(dst, []int{1, 2}) {
			t.Errorf("got %v", dst)
		}
	})
	t.Run("a nil map leaves the destination", func(t *testing.T) {
		t.Parallel()
		dst := map[string]string{"k": "v"}
		layMap(&dst, nil)
		if dst["k"] != "v" {
			t.Errorf("got %v", dst)
		}
	})
	t.Run("an empty map that is not nil replaces it", func(t *testing.T) {
		t.Parallel()
		dst := map[string]string{"k": "v"}
		layMap(&dst, map[string]string{})
		if dst == nil || len(dst) != 0 {
			t.Errorf("got %#v, want an empty map that is not nil", dst)
		}
	})
	t.Run("a map replaces nil", func(t *testing.T) {
		t.Parallel()
		var dst map[string][]string
		layMap(&dst, map[string][]string{"a": {"b"}})
		if len(dst) != 1 || dst["a"][0] != "b" {
			t.Errorf("got %v", dst)
		}
	})
	t.Run("a named slice type is accepted", func(t *testing.T) {
		t.Parallel()
		type names []string
		var dst names
		laySlice(&dst, names{"x"})
		if len(dst) != 1 {
			t.Errorf("got %v", dst)
		}
	})
}

func TestTurnOffClearsAFlagOnlyWhenTheNegationIsSet(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ start, no, want bool }{
		{true, false, true}, {true, true, false}, {false, false, false}, {false, true, false},
	} {
		flag := tc.start
		turnOff(&flag, tc.no)
		if flag != tc.want {
			t.Errorf("turnOff(%v, %v) left %v, want %v", tc.start, tc.no, flag, tc.want)
		}
	}
}

func TestRefuseBlankRefusesATextThatIsEmptyOrOnlySpaceAndNamesTheArgument(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		value   string
		refused bool
	}{
		{"empty", "", true}, {"a space", " ", true}, {"tabs and newlines", "\t\n ", true}, {"a no-break space", " ", true},
		{"text", "Is it?", false}, {"text with space around it", "  x  ", false}, {"a single letter", "q", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call, err := bind.Begin(context.Background(), "is")
			if err != nil {
				t.Fatal(err)
			}
			err = refuseBlank(&call, "question", tc.value)
			if !tc.refused {
				if err != nil {
					t.Fatalf("refused %q: %v", tc.value, err)
				}
				return
			}
			var e *Error
			if !errors.As(err, &e) || e.Kind != ErrInvalidRequest || e.Op != "is" || e.Message != "question is blank" {
				t.Errorf("got %#v, want ErrInvalidRequest for is, saying the question is blank", err)
			}
		})
	}
}

func TestRefuseEmptyRefusesNoItemsAndNamesTheArgument(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		n       int
		refused bool
	}{{0, true}, {1, false}, {2, false}, {100, false}} {
		call, err := bind.Begin(context.Background(), "classify")
		if err != nil {
			t.Fatal(err)
		}
		err = refuseEmpty(&call, "categories", tc.n)
		if (err != nil) != tc.refused {
			t.Fatalf("n=%d: err = %v", tc.n, err)
		}
		if !tc.refused {
			continue
		}
		var e *Error
		if !errors.As(err, &e) || e.Kind != ErrInvalidRequest || e.Op != "classify" || e.Message != "categories is empty" {
			t.Errorf("got %#v", err)
		}
	}
}

func TestARefusalOfAPositionalArgumentStillGivesTheRecorderItsRecord(t *testing.T) {
	t.Parallel()
	var recorder Recorder
	ctx := WithRecorder(context.Background(), &recorder)
	call, err := bind.Begin(ctx, "which")
	if err != nil {
		t.Fatal(err)
	}
	if err := refuseEmpty(&call, "labels", 0); err == nil {
		t.Fatal("no refusal")
	}
	records := recorder.Records()
	if len(records) != 1 || records[0].Op != "which" || records[0].Requests != 0 {
		t.Fatalf("records = %+v", records)
	}
	var e *Error
	if !errors.As(records[0].Err, &e) || e.Kind != ErrInvalidRequest {
		t.Errorf("record err = %v", records[0].Err)
	}
}

func TestCheckFloatRefusesANumberOutsideItsRangeAndNamesTheField(t *testing.T) {
	t.Parallel()
	inf := math.Inf(1)
	cases := []struct {
		name      string
		v, lo, hi float64
		message   string // empty: accepted
	}{
		{"inside", 0.5, 0, 1, ""},
		{"at the low end", 0, 0, 1, ""},
		{"at the high end", 1, 0, 1, ""},
		{"below", -0.1, 0, 1, "Threshold must be a number from 0 to 1"},
		{"above", 1.1, 0, 1, "Threshold must be a number from 0 to 1"},
		{"not a number", math.NaN(), 0, 1, "Threshold must be a number from 0 to 1"},
		{"positive infinity", inf, 0, 1, "Threshold must be a number from 0 to 1"},
		{"negative infinity", -inf, 0, 1, "Threshold must be a number from 0 to 1"},
		{"open below, a negative number", -5, -inf, 1, ""},
		{"open below, above the end", 2, -inf, 1, "Threshold must be a number up to 1"},
		{"open below, not a number", math.NaN(), -inf, 1, "Threshold must be a number up to 1"},
		{"open below, negative infinity", -inf, -inf, 1, "Threshold must be a number up to 1"},
		{"open above, a large number", 1e9, 0, inf, ""},
		{"open above, below the start", -1, 0, inf, "Threshold must be a number from 0"},
		{"open above, infinity", inf, 0, inf, "Threshold must be a number from 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := checkFloat("sort", "Threshold", tc.v, tc.lo, tc.hi)
			if tc.message == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted %v", tc.v)
			}
			if kind := types.KindOf(err); kind != types.KindConfiguration {
				t.Errorf("kind = %v, want configuration", kind)
			}
			converted := bind.ConvertError("sort", err)
			var e *Error
			if !errors.As(converted, &e) || e.Op != "sort" || e.Kind != ErrConfiguration || e.Message != tc.message {
				t.Errorf("converted = %#v, want message %q", converted, tc.message)
			}
		})
	}
}

func TestCheckCountRefusesANegativeNumberAndNamesTheField(t *testing.T) {
	t.Parallel()
	for _, n := range []int{0, 1, 5, 1 << 30} {
		if err := checkCount("summarize", "TargetLength", n); err != nil {
			t.Errorf("refused %d: %v", n, err)
		}
	}
	for _, n := range []int{-1, -5, -1 << 30} {
		err := checkCount("summarize", "TargetLength", n)
		var e *Error
		if !errors.As(bind.ConvertError("summarize", err), &e) || e.Kind != ErrConfiguration || e.Message != "TargetLength must not be negative" {
			t.Errorf("n=%d: err = %v", n, err)
		}
	}
}

func TestDecisionOptionsCarryTheSevenCommonSettingsAndNothingElse(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		o    Options
		want types.OpOptions
	}{
		"nothing set":    {Options{}, types.OpOptions{}},
		"a model":        {Options{Model: "m"}, types.OpOptions{Model: "m"}},
		"a tier":         {Options{Tier: TierSmart}, types.OpOptions{Intelligence: types.Smart}},
		"the quick tier": {Options{Tier: TierQuick}, types.OpOptions{Intelligence: types.Quick}},
		"an effort":      {Options{Effort: EffortHigh}, types.OpOptions{Effort: types.Effort("high")}},
		"a mode":         {Options{Mode: ModeStrict}, types.OpOptions{Mode: types.Strict}},
		"steering":       {Options{Steering: "be brief"}, types.OpOptions{Steering: "be brief"}},
		"a ceiling":      {Options{MaxOutputTokens: 77}, types.OpOptions{MaxOutputTokens: 77}},
		"web search":     {Options{WebSearch: true}, types.OpOptions{WebSearch: true}},
		"all of them": {
			Options{Model: "m", Tier: TierFast, Effort: EffortLow, Mode: ModeCreative, Steering: "s", MaxOutputTokens: 9, WebSearch: true},
			types.OpOptions{Model: "m", Intelligence: types.Fast, Effort: types.Effort("low"), Mode: types.Creative, Steering: "s", MaxOutputTokens: 9, WebSearch: true},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := decisionOptions(tc.o); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

type settleKey struct{}

func TestSettleHalvesWritesTheCommonSettingsToBothHalvesAndPutsTheContextOnThem(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), settleKey{}, 1)
	o := Options{Model: "m", Tier: TierSmart, Effort: EffortMedium, Mode: ModeStrict, Steering: "s", MaxOutputTokens: 40, WebSearch: true}
	opts := ops.NewExtractOptions()
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)

	if opts.CommonOptions.Context != ctx {
		t.Error("the context is not on the CommonOptions half")
	}
	c, e := opts.CommonOptions, opts.OpOptions
	if c.Model != "m" || e.Model != "m" || c.Intelligence != types.Smart || e.Intelligence != types.Smart ||
		c.Effort != "medium" || e.Effort != "medium" || c.Mode != types.Strict || e.Mode != types.Strict ||
		c.Steering != "s" || e.Steering != "s" || e.MaxOutputTokens != 40 || !e.WebSearch {
		t.Errorf("halves = %+v and %+v", c, e)
	}
}

func TestSettleHalvesLeavesTheConstructorsDefaultsWhereASettingIsZero(t *testing.T) {
	t.Parallel()
	opts := ops.NewExtractOptions()
	settleHalves(context.Background(), Options{}.common(), &opts.CommonOptions, &opts.OpOptions)
	if opts.CommonOptions.Mode != types.TransformMode || opts.CommonOptions.Intelligence != types.Fast {
		t.Errorf("the defaults were blanked: %+v", opts.CommonOptions)
	}
}

// fieldPath names the exported fields of typ, through embedded structs, as the
// promoted names a literal uses.
func promotedFieldNames(typ reflect.Type) []string {
	var names []string
	for i := range typ.NumField() {
		f := typ.Field(i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			names = append(names, promotedFieldNames(f.Type)...)
			continue
		}
		if f.IsExported() {
			names = append(names, f.Name)
		}
	}
	return names
}

// requireMergeCopiesEveryField proves a merge function of M0 for the options
// type O by reflection: for each exported field, promoted ones included, a
// value with only that field set is returned as it is when it is the only
// value, survives a zero value laid over it, fills a zero value it is laid
// over, and is replaced by a different value laid over it. A field added to O
// and left out of the merge fails here.
func requireMergeCopiesEveryField[O any](t *testing.T, merge func([]O) O) {
	t.Helper()
	typ := reflect.TypeFor[O]()
	names := promotedFieldNames(typ)
	if len(names) < 8 {
		t.Fatalf("%s has only %d fields: %v", typ, len(names), names)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			counter := 0
			earlier := reflect.New(typ).Elem()
			fillValue(t, earlier.FieldByName(name), name, &counter)
			later := reflect.New(typ).Elem()
			counter = 50
			fillValue(t, later.FieldByName(name), name+"-later", &counter)
			var zero O
			a, b := earlier.Interface().(O), later.Interface().(O)

			cases := []struct {
				what   string
				values []O
				want   O
			}{
				{"one value", []O{a}, a},
				{"a zero value over it", []O{a, zero}, a},
				{"it over a zero value", []O{zero, a}, a},
				{"a different value over it", []O{a, b}, b},
				{"three values", []O{zero, a, zero}, a},
			}
			for _, tc := range cases {
				if got := merge(tc.values); !reflect.DeepEqual(got, tc.want) {
					t.Errorf("%s: field %s: got %+v, want %+v", tc.what, name, got, tc.want)
				}
			}
		})
	}
}

func TestPromotedFieldNamesListsTheFieldsOfAnEmbeddedStructAsTheLiteralNamesThem(t *testing.T) {
	t.Parallel()
	type own struct {
		Options
		A string
		b string
		C []string
	}
	got := promotedFieldNames(reflect.TypeFor[own]())
	for _, want := range []string{"Model", "Tier", "Effort", "Mode", "Steering", "MaxOutputTokens", "WebSearch", "A", "C"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s is missing from %v", want, got)
		}
	}
	if slices.Contains(got, "b") || slices.Contains(got, "Options") {
		t.Errorf("an unexported field or the embedded struct itself is listed: %v", got)
	}
}
