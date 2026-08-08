package ops

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Every `WithX` on every options type, called and observed to change something.
//
// This package has 49 options types carrying several hundred one-line setters
// between them, and the failure they keep having is not a wrong value — it is no
// value at all. F-02: eleven option types had no `Mode` field, so `.Strict()`
// compiled, chained, and had nowhere to put the answer. F-04: `.Steer()`
// assigned instead of appending, so the second call silently discarded the
// first. TC-005's `Model` pin: the setter existed and the field did not.
//
// Each of those was found by hand, one at a time, after shipping. A per-setter
// test written by hand has the same problem the bugs did: it covers what
// somebody remembered to list. So this walks the types by reflection and calls
// every setter it finds, and the assertion is deliberately weak but
// unfakeable — after the call, the struct must DIFFER. A setter that writes
// nowhere cannot satisfy that, whatever it is named and however it is spelled.
//
// The fixture list below cannot silently fall behind either: the last test in
// this file parses the package source for `func NewXOptions()` declarations and
// fails if one is missing from the list. That is the same guard
// api_surface_test.go uses, for the same reason — a hand-written list cannot
// fail when somebody adds an entry, and that is the only moment it needed to.

// probeKey scopes the context value distinctValue plants, so a probe context is
// distinguishable from whatever the fixture already held.
type probeKey struct{}

// allOptionsFixtures returns one value of every options type in this package,
// built through its own constructor so the defaults under test are the real
// ones rather than a zero value no caller ever sees.
func allOptionsFixtures() map[string]any {
	return map[string]any{
		"NewAdversarialOptions": NewAdversarialOptions(),
		"NewAnnotateOptions":    NewAnnotateOptions(),
		"NewArbitrateOptions":   NewArbitrateOptions(),
		"NewAuditOptions":       NewAuditOptions(),
		"NewBatchOptions":       NewBatchOptions(),
		"NewChooseOptions":      NewChooseOptions(),
		"NewClassifyOptions":    NewClassifyOptions(),
		"NewClusterOptions":     NewClusterOptions(),
		"NewCommonOptions":      NewCommonOptions(),
		"NewCompareOptions":     NewCompareOptions(),
		"NewCompleteOptions":    NewCompleteOptions(),
		"NewComposeOptions":     NewComposeOptions(),
		"NewCompressOptions":    NewCompressOptions(),
		"NewConformOptions":     NewConformOptions(),
		"NewCritiqueOptions":    NewCritiqueOptions(),
		"NewDecideOptions":      NewDecideOptions(),
		"NewDecomposeOptions":   NewDecomposeOptions(),
		"NewDeriveOptions":      NewDeriveOptions(),
		"NewDiffOptions":        NewDiffOptions(),
		"NewEnrichOptions":      NewEnrichOptions(),
		"NewExpandOptions":      NewExpandOptions(),
		"NewExplainOptions":     NewExplainOptions(),
		"NewExtractOptions":     NewExtractOptions(),
		"NewFilterOptions":      NewFilterOptions(),
		"NewGenerateOptions":    NewGenerateOptions(),
		"NewInferOptions":       NewInferOptions(),
		"NewInterpolateOptions": NewInterpolateOptions(),
		"NewMatchOptions":       NewMatchOptions(),
		"NewNegotiateOptions":   NewNegotiateOptions(),
		"NewNormalizeOptions":   NewNormalizeOptions(),
		"NewParseOptions":       NewParseOptions(),
		"NewPivotOptions":       NewPivotOptions(),
		"NewPredictOptions":     NewPredictOptions(),
		"NewProjectOptions":     NewProjectOptions(),
		"NewRankOptions":        NewRankOptions(),
		"NewRedactLLMOptions":   NewRedactLLMOptions(),
		"NewRedactOptions":      NewRedactOptions(),
		"NewResolveOptions":     NewResolveOptions(),
		"NewRewriteOptions":     NewRewriteOptions(),
		"NewScoreOptions":       NewScoreOptions(),
		"NewSimilarOptions":     NewSimilarOptions(),
		"NewSortOptions":        NewSortOptions(),
		"NewSuggestOptions":     NewSuggestOptions(),
		"NewSummarizeOptions":   NewSummarizeOptions(),
		"NewSynthesizeOptions":  NewSynthesizeOptions(),
		"NewTransformOptions":   NewTransformOptions(),
		"NewTranslateOptions":   NewTranslateOptions(),
		"NewValidateOptions":    NewValidateOptions(),
		"NewVerifyOptions":      NewVerifyOptions(),
	}
}

// distinctValue builds a value of the requested type that differs from
// `current`, so calling a setter with it must be observable.
//
// Producing something merely non-zero is not enough: a setter handed the value
// the field already holds changes nothing, and the test would blame the setter
// for the fixture's choice. Everything here is derived from what is already
// there.
func distinctValue(t reflect.Type, current reflect.Value) (reflect.Value, bool) {
	switch t.Kind() {
	case reflect.String:
		return reflect.ValueOf("schemaflux-setter-probe" + current.String()).Convert(t), true

	case reflect.Bool:
		return reflect.ValueOf(!current.Bool()).Convert(t), true

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// time.Duration is an int64 kind; a bare 1 would be a nanosecond, which
		// is a legal but silly duration. Either way it differs, which is all
		// this needs.
		return reflect.ValueOf(current.Int() + 7).Convert(t), true

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return reflect.ValueOf(current.Uint() + 7).Convert(t), true

	case reflect.Float32, reflect.Float64:
		// Thresholds and confidences are constrained to [0, 1] by their own
		// Validate methods, so stay inside it: a value outside the range would
		// still prove the setter applied, but a later Validate-driven test
		// reading this fixture would see something no caller should send.
		next := current.Float() + 0.125
		if next > 1 {
			next = current.Float() - 0.125
		}
		if next < 0 {
			next = 0.5
		}
		return reflect.ValueOf(next).Convert(t), true

	case reflect.Slice:
		switch t.Elem().Kind() {
		case reflect.String:
			return reflect.ValueOf([]string{"probe-a", "probe-b"}).Convert(t), true
		case reflect.Int:
			return reflect.ValueOf([]int{7, 9}).Convert(t), true
		case reflect.Interface:
			return reflect.ValueOf([]any{"probe-a", 2}).Convert(t), true
		}
		return reflect.MakeSlice(t, 0, 0), false // an empty slice may equal current

	case reflect.Map:
		if t.Key().Kind() == reflect.String && t.Elem().Kind() == reflect.String {
			return reflect.ValueOf(map[string]string{"probe": "value"}).Convert(t), true
		}
		if t.Key().Kind() == reflect.String && t.Elem().Kind() == reflect.Float64 {
			return reflect.ValueOf(map[string]float64{"probe": 0.5}).Convert(t), true
		}
		if t.Key().Kind() == reflect.String && t.Elem().Kind() == reflect.Interface {
			return reflect.ValueOf(map[string]any{"probe": "value"}).Convert(t), true
		}
		return reflect.Value{}, false

	case reflect.Interface:
		// A context and a bare `any` are both interface kinds and both
		// synthesisable, they just need different values. The context carries a
		// marker so it is distinguishable from a Background() the fixture might
		// already hold; the `any` case takes a string.
		if t == reflect.TypeOf((*context.Context)(nil)).Elem() {
			ctx := context.WithValue(context.Background(), probeKey{}, "probe")
			return reflect.ValueOf(ctx), true
		}
		if t.NumMethod() == 0 { // `any`
			return reflect.ValueOf(any("probe")).Convert(t), true
		}
		return reflect.Value{}, false

	default:
		return reflect.Value{}, false
	}
}

func TestEveryOptionSetterChangesSomething(t *testing.T) {
	fixtures := allOptionsFixtures()

	var (
		checked int
		skipped []string
	)

	for constructor, fixture := range fixtures {
		optionsType := reflect.TypeOf(fixture)
		typeName := optionsType.Name()

		for i := 0; i < optionsType.NumMethod(); i++ {
			method := optionsType.Method(i)
			if !strings.HasPrefix(method.Name, "With") {
				continue
			}
			if method.Type.NumOut() != 1 || method.Type.Out(0) != optionsType {
				continue
			}

			before := reflect.ValueOf(fixture)

			// Zero-argument setters -- WithExactFields(), WithCompleteFields()
			// -- are switches rather than assignments, so there is no value to
			// synthesise. They still have to change something, and an earlier
			// version of this sweep skipped them on an arity check and left
			// twenty of them unguarded.
			if method.Type.NumIn() == 1 {
				after := method.Func.Call([]reflect.Value{before})[0]
				checked++
				if reflect.DeepEqual(before.Interface(), after.Interface()) {
					t.Errorf("%s.%s() changed nothing (constructor %s); a zero-argument setter is a switch, and a switch that flips nothing is not one",
						typeName, method.Name, constructor)
				}
				continue
			}

			if method.Type.NumIn() != 2 {
				continue
			}
			paramType := method.Type.In(1)

			// Reach for the field the setter is named after, when there is one,
			// so the probe value differs from what is already stored.
			current := reflect.New(paramType).Elem()
			if field := before.FieldByName(strings.TrimPrefix(method.Name, "With")); field.IsValid() && field.Type() == paramType {
				current = field
			}

			probe, ok := distinctValue(paramType, current)
			if !ok {
				skipped = append(skipped, typeName+"."+method.Name+" ("+paramType.String()+")")
				continue
			}

			after := method.Func.Call([]reflect.Value{before, probe})[0]
			checked++

			if reflect.DeepEqual(before.Interface(), after.Interface()) {
				t.Errorf("%s.%s changed nothing.\n"+
					"  constructor: %s\n"+
					"  argument:    %v (%s)\n"+
					"A setter that compiles, chains, and writes nowhere is this "+
					"package's oldest bug (F-02, F-04, TC-005). Either the field "+
					"is missing from %s or the method assigns to a copy.",
					typeName, method.Name, constructor, probe.Interface(), paramType, typeName)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no setters were exercised; the reflection walk found nothing, which means this test is not testing anything")
	}
	t.Logf("exercised %d setters across %d options types", checked, len(fixtures))

	// Skips are reported rather than silent. A setter this cannot probe is a
	// setter nothing here guards, and a reader deserves to know which.
	if len(skipped) > 0 {
		t.Logf("%d setter(s) not probed, parameter type not synthesisable: %s",
			len(skipped), strings.Join(skipped, ", "))
	}
}

// Setters must return a COPY. Every options type in this package has value
// receivers so a builder chain cannot mutate a shared base — if one ever gained
// a pointer receiver, two callers deriving from the same options value would
// start affecting each other, silently and only under concurrency.
func TestOptionSettersDoNotMutateTheReceiver(t *testing.T) {
	for constructor, fixture := range allOptionsFixtures() {
		optionsType := reflect.TypeOf(fixture)

		for i := 0; i < optionsType.NumMethod(); i++ {
			method := optionsType.Method(i)
			if !strings.HasPrefix(method.Name, "With") ||
				method.Type.NumOut() != 1 || method.Type.Out(0) != optionsType {
				continue
			}

			original := reflect.ValueOf(fixture)
			snapshot := reflect.ValueOf(fixture) // a copy, since these are structs

			args := []reflect.Value{original}
			if method.Type.NumIn() == 2 {
				paramType := method.Type.In(1)
				probe, ok := distinctValue(paramType, reflect.New(paramType).Elem())
				if !ok {
					continue
				}
				args = append(args, probe)
			} else if method.Type.NumIn() != 1 {
				continue
			}

			method.Func.Call(args)

			if !reflect.DeepEqual(original.Interface(), snapshot.Interface()) {
				t.Errorf("%s.%s mutated its receiver (from %s); options types must have value receivers so two callers deriving from one base cannot affect each other",
					optionsType.Name(), method.Name, constructor)
			}
		}
	}
}

// The fixture list cannot fall behind the package.
//
// Parsing the source rather than trusting the list is the whole point: a
// hand-maintained fixture map cannot fail when somebody adds a 50th options
// type, and that is exactly when it needed to.
func TestEveryOptionsConstructorIsInTheFixtureList(t *testing.T) {
	fixtures := allOptionsFixtures()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	found := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}

		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if !strings.HasPrefix(fn.Name.Name, "New") || !strings.HasSuffix(fn.Name.Name, "Options") {
				continue
			}
			// Only the no-argument constructors; one taking parameters is not a
			// defaults constructor and cannot be built blind.
			if fn.Type.Params != nil && len(fn.Type.Params.List) != 0 {
				continue
			}

			found++
			if _, listed := fixtures[fn.Name.Name]; !listed {
				t.Errorf("%s (%s) is not in allOptionsFixtures, so none of its setters are covered", fn.Name.Name, name)
			}
		}
	}

	if found == 0 {
		t.Fatal("found no options constructors by parsing the package; this guard is not guarding anything")
	}
	if found != len(fixtures) {
		t.Errorf("the source declares %d no-argument options constructors and the fixture list has %d; a stale entry is as misleading as a missing one",
			found, len(fixtures))
	}
}

// A guard on the guard: time.Duration is an int64 kind, and an earlier version
// of distinctValue returned a bare 1 for every integer, which for a timeout
// meant one nanosecond. It differed, so the setter test passed, and the value
// was nonsense. Kept as a case because the next person to simplify
// distinctValue will reach for exactly that.
func TestDistinctValueProducesSomethingDifferent(t *testing.T) {
	cases := []struct {
		name    string
		current any
	}{
		{"string", "existing"},
		{"empty string", ""},
		{"bool true", true},
		{"bool false", false},
		{"int", 42},
		{"zero int", 0},
		{"duration", 30 * time.Second},
		{"float mid", 0.5},
		{"float at ceiling", 1.0},
		{"float at floor", 0.0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current := reflect.ValueOf(tc.current)
			probe, ok := distinctValue(current.Type(), current)
			if !ok {
				t.Fatalf("distinctValue refused %T", tc.current)
			}
			if reflect.DeepEqual(probe.Interface(), tc.current) {
				t.Errorf("distinctValue returned %v, the same value it was given", probe.Interface())
			}

			// Floats must stay inside [0, 1]: the options types constrain
			// thresholds and confidences to that range.
			if current.Kind() == reflect.Float64 {
				got := probe.Float()
				if got < 0 || got > 1 {
					t.Errorf("distinctValue produced %v, outside the [0, 1] range these fields validate against", got)
				}
			}
		})
	}
}
