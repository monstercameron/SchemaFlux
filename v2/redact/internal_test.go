package redact

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// The tests of this file call what the package does not export: the merges, the
// checks and the builders of the internal options. They hold each to the internal
// operation it wraps, so that a rule of the internal operation that changes is
// seen here and not in a caller's program.

func TestTheStrategiesAndJumbleModesAreTheInternalOnesByValue(t *testing.T) {
	t.Parallel()
	for declared, internal := range map[Strategy]ops.RedactStrategy{
		Nil: ops.RedactNil, Mask: ops.RedactMask, Remove: ops.RedactRemove, Jumble: ops.RedactJumble, Scramble: ops.RedactScramble,
	} {
		if string(declared) != string(internal) {
			t.Errorf("%q is declared and %q is the internal value", declared, internal)
		}
	}
	for declared, internal := range map[JumbleMode]ops.JumbleMode{
		JumbleBasic: ops.JumbleBasic, JumbleSmart: ops.JumbleSmart, JumbleTypeAware: ops.JumbleTypeAware,
	} {
		if string(declared) != string(internal) {
			t.Errorf("%q is declared and %q is the internal value", declared, internal)
		}
	}
}

func TestTheLongestMaskIsTheInternalOperationsLongestAndBoundsBothFunctions(t *testing.T) {
	t.Parallel()
	if err := ops.NewRedactOptions().WithMaskLength(maxMaskLength).Validate(); err != nil {
		t.Errorf("the internal operation refuses %d: %v", maxMaskLength, err)
	}
	if err := ops.NewRedactOptions().WithMaskLength(maxMaskLength + 1).Validate(); err == nil {
		t.Errorf("the internal operation accepts %d", maxMaskLength+1)
	}
	if maxMinMask != maxMaskLength {
		t.Errorf("MinMask is bounded by %d and MaskLength by %d", maxMinMask, maxMaskLength)
	}
}

func TestTheCategoryRuleOfValueIsTheInternalOperations(t *testing.T) {
	t.Parallel()
	for _, category := range []string{
		"pii", "PII", " pii ", "Secrets", "SECRETS", "financial", " Financial\t", "", " ", "email", "all", "pii,secrets", "p ii", "piii", "financials", "\u0000", "PII\n",
	} {
		internal := ops.NewRedactOptions().WithCategories([]string{category}).Validate() == nil
		ours := checkValueOptions(ValueOptions{Categories: []string{category}}) == nil
		if internal != ours {
			t.Errorf("%q: the internal operation accepts it: %v, this package: %v", category, internal, ours)
		}
	}
}

func TestTheStrategyRuleOfValueIsTheInternalOperations(t *testing.T) {
	t.Parallel()
	for _, strategy := range []Strategy{"", Nil, Mask, Remove, Jumble, Scramble, "NIL", "Mask", "obliterate", " mask", "jumble "} {
		internal := true
		if strategy != "" {
			internal = ops.NewRedactOptions().WithStrategy(ops.RedactStrategy(strategy)).Validate() == nil
		}
		if ours := checkValueOptions(ValueOptions{Strategy: strategy}) == nil; internal != ours {
			t.Errorf("%q: the internal operation accepts it: %v, this package: %v", strategy, internal, ours)
		}
	}
}

func TestTheMaskLengthRuleOfValueIsTheInternalOperations(t *testing.T) {
	t.Parallel()
	for _, length := range []int{-1000, -2, -1, 0, 1, 3, 65535, 65536, 65537, 1 << 20, 1<<31 - 1} {
		internal := true
		if length != 0 {
			internal = ops.NewRedactOptions().WithMaskLength(length).Validate() == nil
		}
		if ours := checkValueOptions(ValueOptions{MaskLength: length}) == nil; internal != ours {
			t.Errorf("%d: the internal operation accepts it: %v, this package: %v", length, internal, ours)
		}
	}
}

func TestTheCategoryRuleOfTextIsTheInternalOperations(t *testing.T) {
	t.Parallel()
	for _, categories := range [][]string{
		nil, {}, {""}, {" "}, {"email"}, {" EMAIL "}, {"a,b"}, {"email", ""}, {"email", "x,y"}, {"all"}, {"project"}, {"\t"}, {"a b"},
	} {
		internal := true
		if categories != nil {
			internal = ops.NewRedactLLMOptions().WithCategories(categories).Validate() == nil
		}
		if ours := checkTextOptions(TextOptions{Categories: categories}) == nil; internal != ours {
			t.Errorf("%q: the internal operation accepts it: %v, this package: %v", categories, internal, ours)
		}
	}
}

func TestTheMaskNumbersOfTextAreTheInternalOperationsExceptTheBoundWeAdd(t *testing.T) {
	t.Parallel()
	for _, n := range []int{-5, -1, 1, 2, 7} {
		first := ops.NewRedactLLMOptions().WithShowFirst(n).Validate() == nil
		last := ops.NewRedactLLMOptions().WithShowLast(n).Validate() == nil
		if ours := checkTextOptions(TextOptions{ShowFirst: n}) == nil; first != ours {
			t.Errorf("ShowFirst %d: internal %v, this package %v", n, first, ours)
		}
		if ours := checkTextOptions(TextOptions{ShowLast: n}) == nil; last != ours {
			t.Errorf("ShowLast %d: internal %v, this package %v", n, last, ours)
		}
	}
	for _, n := range []int{-5, -1, 1, 2, 3, 65536} {
		internal := ops.NewRedactLLMOptions().WithMinMask(n).Validate() == nil
		if ours := checkTextOptions(TextOptions{MinMask: n}) == nil; internal != ours {
			t.Errorf("MinMask %d: internal %v, this package %v", n, internal, ours)
		}
	}
}

// promotedFieldNames names the exported fields of typ, through embedded structs,
// as the promoted names a literal uses.
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

// fillField sets v to a value that is not its zero value and differs with n.
func fillField(t *testing.T, v reflect.Value, n int) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		v.SetString("v" + strings.Repeat("x", n))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(n%3 + 1))
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillField(t, v.Index(0), n)
	default:
		t.Fatalf("a field of kind %s is not filled by this test", v.Kind())
	}
}

// requireMergeCopiesEveryField proves a merge function by reflection: for each
// exported field, promoted ones included, a value with only that field set is
// returned as it is when it is the only value, survives a zero value laid over it,
// fills a zero value it is laid over, and is replaced by a different value laid
// over it. A field added to the type and left out of the merge fails here.
func requireMergeCopiesEveryField[O any](t *testing.T, merge func([]O) O, wantFields int) {
	t.Helper()
	typ := reflect.TypeFor[O]()
	names := promotedFieldNames(typ)
	if len(names) != wantFields {
		t.Fatalf("%s has %d fields (%v), want %d: this test has to be told of a field that was added", typ, len(names), names, wantFields)
	}
	for i, name := range names {
		earlier := reflect.New(typ).Elem()
		fillField(t, earlier.FieldByName(name), 1+i)
		later := reflect.New(typ).Elem()
		fillField(t, later.FieldByName(name), 20+i)
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
	}
}

func TestMergeValueOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeValueOptions, 8)
}

func TestMergeTextOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeTextOptions, 12)
}

func TestMergeCommonOptionsCopiesEveryFieldOfTheRootsOptions(t *testing.T) {
	t.Parallel()
	merge := func(values []schemaflux.Options) schemaflux.Options {
		out := values[0]
		for _, later := range values[1:] {
			out = mergeCommonOptions(out, later)
		}
		return out
	}
	// The root's Options has seven settings. A field the root adds is not copied by
	// this package's merge until it is written there, and this fails until it is.
	requireMergeCopiesEveryField(t, func(values []schemaflux.Options) schemaflux.Options {
		if len(values) == 1 {
			return values[0]
		}
		return merge(values)
	}, 7)
}

// allocProvider is the scripted model of the allocation tests. It answers every
// request with one body and keeps nothing: a list of the requests it was sent
// would grow, and each growth is an allocation of the fixture's own.
type allocProvider struct{ reply string }

func (p *allocProvider) Name() string { return "alloc-test" }

func (p *allocProvider) Complete(context.Context, schemaflux.Request) (schemaflux.Response, error) {
	return schemaflux.Response{
		Content: p.reply, Model: "alloc-model", FinishReason: "stop",
		Usage: schemaflux.Usage{Input: 11, Output: 3, Total: 14},
	}, nil
}

// ModelFor gives every tier one model, so that a call needs no setting to be made.
func (p *allocProvider) ModelFor(schemaflux.Tier) string { return "alloc-model" }

// RetryPolicy is never: the failure of a request is seen once.
func (p *allocProvider) RetryPolicy() (int, time.Duration) { return -1, 0 }

const allocRuns = 50

func TestValueAllocatesAsTheInternalCall(t *testing.T) {
	input := map[string]string{"email": "ada@example.com", "note": "mail ada@example.com", "plan": "gold"}
	cases := map[string]ValueOptions{
		"no options": {},
		"every field": {
			Categories: []string{"pii", "secrets"}, Strategy: Mask, MaskText: "[x]", MaskChar: '#', MaskLength: 5,
			JumbleSeed: 3, JumbleMode: JumbleSmart, CustomPatterns: []string{`ORDER-\d+`},
		},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			call := func() { _, _ = Value(input, o) }
			internal := func() { _, _ = ops.Redact(input, valueInternal(o)) }
			call()
			internal()
			got, want := testing.AllocsPerRun(allocRuns, call), testing.AllocsPerRun(allocRuns, internal)
			t.Logf("Value %v allocations, the internal call %v", got, want)
			if got != want {
				t.Errorf("Value allocates %v times and the internal call %v", got, want)
			}
		})
	}
	t.Run("several options values", func(t *testing.T) {
		first, second := ValueOptions{MaskLength: 5}, ValueOptions{MaskChar: '#'}
		merged := mergeValueOptions([]ValueOptions{first, second})
		call := func() { _, _ = Value(input, first, second) }
		internal := func() { _, _ = ops.Redact(input, valueInternal(merged)) }
		call()
		internal()
		if got, want := testing.AllocsPerRun(allocRuns, call), testing.AllocsPerRun(allocRuns, internal); got != want {
			t.Errorf("Value allocates %v times and the internal call %v", got, want)
		}
	})
}

// measureText returns what Text allocates and what the internal call it wraps
// does, under a client carried by the context, and on the default client against
// the internal call on a context that already holds the default client's
// snapshot, which is the pin a call pays. internal builds the internal call from
// the context it is given.
func measureText(t *testing.T, reply string, call func(ctx context.Context), internal func(ctx context.Context)) (wrapped, direct, wrappedDefault, directPinned float64) {
	t.Helper()
	model := &allocProvider{reply: reply}
	client := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	call(client)
	internal(client)
	wrapped = testing.AllocsPerRun(allocRuns, func() { call(client) })
	direct = testing.AllocsPerRun(allocRuns, func() { internal(client) })

	previous := schemaflux.Default()
	schemaflux.SetDefault(schemaflux.Client{Provider: model})
	t.Cleanup(func() { schemaflux.SetDefault(previous) })
	background := context.Background()
	pinned, _ := ops.PinExec(background)
	wrappedDefault = testing.AllocsPerRun(allocRuns, func() { call(background) })
	directPinned = testing.AllocsPerRun(allocRuns, func() { internal(pinned) })
	t.Logf("under WithClient: Text %v allocations, the internal call %v; on the default client: Text %v, the internal call on a pinned context %v", wrapped, direct, wrappedDefault, directPinned)
	return wrapped, direct, wrappedDefault, directPinned
}

func TestTextAllocatesAsTheInternalCallInThePlainStateWhenNoSpanAndNoCommonSettingIsGiven(t *testing.T) {
	const text = "nothing in this one"
	o := TextOptions{Categories: []string{"email"}, MaskChar: '#', ShowFirst: 1, ShowLast: 1, MinMask: 4}
	wrapped, direct, wrappedDefault, directPinned := measureText(t, `{"spans":[]}`,
		func(ctx context.Context) { _, _ = Text(ctx, text, o) },
		func(ctx context.Context) { _, _ = ops.RedactLLM(ctx, text, textInternal(ctx, o, bind.Common{})) })
	if wrapped != direct || wrappedDefault != directPinned+1 {
		t.Errorf("Text allocates %v times and the internal call %v; on the default client %v and %v plus the pin", wrapped, direct, wrappedDefault, directPinned)
	}
}

// A span the internal result holds is copied into this package's type, which is
// one allocation, and the list of spans is the only list that is copied.
func TestTextWithSpansAllocatesOneMoreThanTheInternalCall(t *testing.T) {
	const text = "mail ada@example.com or 555-123-4567"
	wrapped, direct, wrappedDefault, directPinned := measureText(t, `{"spans":[]}`,
		func(ctx context.Context) { _, _ = Text(ctx, text) },
		func(ctx context.Context) {
			_, _ = ops.RedactLLM(ctx, text, textInternal(ctx, TextOptions{}, bind.Common{}))
		})
	if wrapped != direct+1 || wrappedDefault != directPinned+2 {
		t.Errorf("Text allocates %v times and the internal call %v; on the default client %v and %v: want one more for the copy of the spans, and the pin", wrapped, direct, wrappedDefault, directPinned)
	}
}

// bind.CommonOf takes its argument as an any, which holds a copy of the Options on
// the heap: one allocation that a call with a common setting makes and a call with
// none does not.
func TestTextWithACommonSettingAllocatesOneMoreForTheOptionsHandedToBind(t *testing.T) {
	const text = "nothing in this one"
	o := TextOptions{Options: schemaflux.Options{Steering: "be thorough", Model: "alloc-model"}}
	common, err := bind.CommonOf(o.Options)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, direct, wrappedDefault, directPinned := measureText(t, `{"spans":[]}`,
		func(ctx context.Context) { _, _ = Text(ctx, text, o) },
		func(ctx context.Context) { _, _ = ops.RedactLLM(ctx, text, textInternal(ctx, o, common)) })
	if wrapped != direct+1 || wrappedDefault != directPinned+2 {
		t.Errorf("Text allocates %v times and the internal call %v; on the default client %v and %v: want one more for the Options handed to bind, and the pin", wrapped, direct, wrappedDefault, directPinned)
	}
}

// A blank text sends nothing, so the internal call never reads the client and
// never pins it, and Text pays for the pin alone on the default client.
func TestTextOfABlankTextAllocatesAsTheInternalCallPlusThePinOnTheDefaultClient(t *testing.T) {
	wrapped, direct, wrappedDefault, directPinned := measureText(t, `{"spans":[]}`,
		func(ctx context.Context) { _, _ = Text(ctx, "  ") },
		func(ctx context.Context) {
			_, _ = ops.RedactLLM(ctx, "  ", textInternal(ctx, TextOptions{}, bind.Common{}))
		})
	if wrapped != direct || wrappedDefault != directPinned+1 {
		t.Errorf("Text allocates %v times and the internal call %v; on the default client %v and %v plus the pin", wrapped, direct, wrappedDefault, directPinned)
	}
}
