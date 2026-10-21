package ops

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The level of an operation for a type argument. A schema-constrained level
// needs a schema declared on the request, and for the type-bound rows of the
// table the schema is built from the caller's type, so what the call delivers
// depends on whether one could be built. These tests hold ContractLevelFor to
// what the request builders really send.

type levelFlat struct {
	Name string
	Age  int
}

type levelNested struct {
	Owner levelFlat
	Tags  []levelFlat
}

type levelWithMap struct {
	Name  string
	Extra map[string]string
}

type levelRecursive struct {
	Name string
	Next *levelRecursive
}

type levelWithAny struct {
	Name  string
	Extra any
}

type levelStamp struct{ When time.Time }

type levelNamedMap map[string]int

type levelNamedString string

// levelDeepN is a chain of N structs ending in a string. The schema of a type
// is built down to a depth of six, so the chain of six has one and the chain of
// seven has none; Normalize puts the type inside an envelope of its own, which
// is one level deeper, so for it the chain of five is the last that has one.
type (
	levelDeep1 struct{ V string }
	levelDeep2 struct{ In levelDeep1 }
	levelDeep3 struct{ In levelDeep2 }
	levelDeep4 struct{ In levelDeep3 }
	levelDeep5 struct{ In levelDeep4 }
	levelDeep6 struct{ In levelDeep5 }
	levelDeep7 struct{ In levelDeep6 }
	levelDeep8 struct{ In levelDeep7 }
)

// levelDrive is one call of one type-bound operation into the type under test.
type levelDrive struct {
	operation string
	run       func()
}

// levelDrives are the nine type-bound operations, each called once with T in the
// place the type argument is in its signature: the target of Transform, the
// answer of Question, the prediction of Predict, and the value of the rest.
func levelDrives[T any](sample T) []levelDrive {
	return []levelDrive{
		{"extract", func() { _, _ = Extract[T]("input text", NewExtractOptions()) }},
		{"transform", func() { _, _ = Transform[contractSource, T](contractSource{"Ada", 36}, NewTransformOptions()) }},
		{"generate", func() { _, _ = Generate[T]("a thing", NewGenerateOptions()) }},
		{"question", func() { _, _ = Question[string, T]("data", NewQuestionOptions("why?")) }},
		{"compress", func() { _, _ = Compress(sample, NewCompressOptions()) }},
		{"normalize", func() { _, _ = Normalize(sample, NewNormalizeOptions()) }},
		// AutoCorrect, which the constructor turns on, is what makes the schema
		// follow the type.
		{"validate", func() { _, _ = ValidateHybrid(sample, NewValidateOptions().WithRules("every field is set")) }},
		{"exp.predict", func() { _, _ = Predict[T]([]float64{1, 2, 3}, NewPredictOptions()) }},
		{"exp.decompose", func() { _, _ = Decompose(sample, NewDecomposeOptions()) }},
	}
}

// levelWitness counts, for each operation, the calls that sent a request with a
// schema and the calls that sent one without, so that a test that passes
// because no call ever got as far as a request is told from one that passes
// because the level agrees.
type levelWitness struct {
	mu       sync.Mutex
	schema   map[string]int
	noSchema map[string]int
}

var levelWitnessed = &levelWitness{schema: map[string]int{}, noSchema: map[string]int{}}

// levelAgreesWithTheRequest calls every type-bound operation into T and asserts
// that its level says schema constrained exactly when the request declared a
// schema, and is the row's level for it.
func levelAgreesWithTheRequest[T any](t *testing.T, name string, sample T) {
	t.Helper()
	for _, drive := range levelDrives(sample) {
		t.Run(name+"/"+drive.operation, func(t *testing.T) {
			probe := newContractProbe(t, `{}`)
			drive.run()
			if probe.requests() == 0 {
				// Turned away before anything was sent (a type no answer could
				// satisfy). There is no request to hold the level to.
				return
			}
			sent := probe.first(t).JSONSchema != nil
			row, _ := contractRowOf(drive.operation)
			want := row.without
			if sent {
				want = row.level
				if drive.operation == "generate" {
					want = types.ContractSchemaConstrained
				}
			}
			if drive.operation == "generate" && reflect.TypeFor[T]().Kind() == reflect.String {
				want = types.ContractPromptOnly
			}
			got := ContractLevelFor[T](drive.operation)
			if got != want {
				t.Errorf("the request declared a schema: %v, and the level is %s, want %s", sent, got, want)
			}
			if (got >= types.ContractSchemaConstrained) != sent {
				t.Errorf("the level is %s and the request declared a schema: %v", got, sent)
			}
			levelWitnessed.mu.Lock()
			defer levelWitnessed.mu.Unlock()
			if sent {
				levelWitnessed.schema[drive.operation]++
			} else {
				levelWitnessed.noSchema[drive.operation]++
			}
		})
	}
}

func TestContractLevelForAgreesWithWhatEveryTypeBoundOperationSends(t *testing.T) {
	levelAgreesWithTheRequest(t, "a flat struct", levelFlat{"Ada", 36})
	levelAgreesWithTheRequest(t, "a pointer to a struct", &levelFlat{"Ada", 36})
	levelAgreesWithTheRequest(t, "a nested struct", levelNested{Owner: levelFlat{"Ada", 36}, Tags: []levelFlat{{"x", 1}}})
	levelAgreesWithTheRequest(t, "a slice of structs", []levelFlat{{"Ada", 36}})
	levelAgreesWithTheRequest(t, "a struct with a map field", levelWithMap{Name: "a", Extra: map[string]string{"k": "v"}})
	levelAgreesWithTheRequest(t, "a map of strings", map[string]string{"k": "v"})
	levelAgreesWithTheRequest(t, "a map of any", map[string]any{"k": "v"})
	levelAgreesWithTheRequest(t, "a named map", levelNamedMap{"k": 1})
	levelAgreesWithTheRequest(t, "a slice of maps", []map[string]string{{"k": "v"}})
	levelAgreesWithTheRequest(t, "a recursive type", levelRecursive{Name: "a", Next: &levelRecursive{Name: "b"}})
	levelAgreesWithTheRequest(t, "a struct with an any field", levelWithAny{Name: "a", Extra: 1})
	levelAgreesWithTheRequest[any](t, "an interface", map[string]any{"k": "v"})
	levelAgreesWithTheRequest(t, "a string", "text")
	levelAgreesWithTheRequest(t, "a named string", levelNamedString("text"))
	levelAgreesWithTheRequest(t, "an integer", 7)
	levelAgreesWithTheRequest(t, "a boolean", true)
	levelAgreesWithTheRequest(t, "a byte slice", []byte("abc"))
	levelAgreesWithTheRequest(t, "raw JSON", json.RawMessage(`{"a":1}`))
	levelAgreesWithTheRequest(t, "a struct with a time", levelStamp{time.Unix(0, 0).UTC()})
	levelAgreesWithTheRequest(t, "a chain of five structs", levelDeep5{})
	levelAgreesWithTheRequest(t, "a chain of six structs", levelDeep6{})
	levelAgreesWithTheRequest(t, "a chain of seven structs", levelDeep7{})
	levelAgreesWithTheRequest(t, "a chain of eight structs", levelDeep8{})

	// A test that passes because nothing was ever sent proves nothing: each
	// operation was seen to send a schema for some type and none for another.
	levelWitnessed.mu.Lock()
	defer levelWitnessed.mu.Unlock()
	for _, drive := range levelDrives(levelFlat{}) {
		if levelWitnessed.schema[drive.operation] < 5 || levelWitnessed.noSchema[drive.operation] < 3 {
			t.Errorf("%s was seen to send a schema %d times and none %d times", drive.operation,
				levelWitnessed.schema[drive.operation], levelWitnessed.noSchema[drive.operation])
		}
	}
}

// Every row marked type-bound has a drive above, and every drive is a
// type-bound row: a row added to the table without a probe, or a probe for a row
// that is not bound to its type, fails here.
func TestEveryTypeBoundRowIsDrivenAgainstItsRequest(t *testing.T) {
	driven := map[string]bool{}
	for _, drive := range levelDrives(levelFlat{}) {
		driven[drive.operation] = true
	}
	bound := map[string]bool{}
	for i := range contractRows {
		if contractRows[i].typeBound {
			bound[contractRows[i].name] = true
		}
	}
	for name := range bound {
		if !driven[name] {
			t.Errorf("%s is type-bound and no probe sends it a type", name)
		}
	}
	for name := range driven {
		if !bound[name] {
			t.Errorf("%s is probed as type-bound and its row is not", name)
		}
	}
}

func TestContractLevelForTable(t *testing.T) {
	const (
		promptOnly = types.ContractPromptOnly
		wellFormed = types.ContractJSONWellFormed
		schema     = types.ContractSchemaConstrained
		invariants = types.ContractSchemaAndInvariantChecked
	)
	cases := []struct {
		operation string
		name      string
		got       types.ContractLevel
		want      types.ContractLevel
	}{
		{"transform", "a struct", ContractLevelFor[levelFlat]("transform"), schema},
		{"transform", "a map", ContractLevelFor[map[string]any]("transform"), wellFormed},
		{"transform", "an interface", ContractLevelFor[any]("transform"), wellFormed},
		{"question", "a string", ContractLevelFor[string]("question"), schema},
		{"question", "a map", ContractLevelFor[map[string]any]("question"), wellFormed},
		{"question", "a recursive type", ContractLevelFor[levelRecursive]("question"), wellFormed},
		{"compress", "a struct", ContractLevelFor[levelNested]("compress"), schema},
		{"compress", "a map", ContractLevelFor[map[string]string]("compress"), wellFormed},
		{"normalize", "a struct", ContractLevelFor[levelFlat]("normalize"), schema},
		{"normalize", "a map", ContractLevelFor[map[string]string]("normalize"), wellFormed},
		{"normalize", "five structs, the last the envelope leaves room for", ContractLevelFor[levelDeep5]("normalize"), schema},
		{"normalize", "six structs, which the envelope pushes past the limit", ContractLevelFor[levelDeep6]("normalize"), wellFormed},
		{"compress", "six structs, which have no envelope above them", ContractLevelFor[levelDeep6]("compress"), schema},
		{"extract", "seven structs", ContractLevelFor[levelDeep7]("extract"), wellFormed},
		{"validate", "a struct", ContractLevelFor[levelFlat]("validate"), schema},
		{"validate", "a map", ContractLevelFor[map[string]string]("validate"), wellFormed},
		{"validate", "an interface", ContractLevelFor[any]("validate"), wellFormed},
		{"exp.predict", "a number", ContractLevelFor[float64]("exp.predict"), schema},
		{"exp.predict", "a map", ContractLevelFor[map[string]float64]("exp.predict"), wellFormed},
		{"exp.decompose", "a struct", ContractLevelFor[levelFlat]("exp.decompose"), invariants},
		{"exp.decompose", "a map", ContractLevelFor[map[string]string]("exp.decompose"), wellFormed},
		{"extract", "a struct", ContractLevelFor[levelFlat]("extract"), schema},
		{"extract", "a map", ContractLevelFor[map[string]any]("extract"), wellFormed},
		{"generate", "a named string", ContractLevelFor[levelNamedString]("generate"), promptOnly},
		{"generate", "a named map", ContractLevelFor[levelNamedMap]("generate"), wellFormed},
		{"extract", "a pointer to a map", ContractLevelFor[*map[string]string]("extract"), wellFormed},
		// A row that is not bound to its type is the row's level, whatever the type.
		{"filter", "a map", ContractLevelFor[map[string]any]("filter"), wellFormed},
		{"is", "a map", ContractLevelFor[map[string]any]("is"), invariants},
		{"summarize", "a map", ContractLevelFor[map[string]any]("summarize"), promptOnly},
		// Never a level that is not earned: a name that is no operation is the lowest.
		{"nonsense", "a struct", ContractLevelFor[levelFlat]("nonsense"), promptOnly},
		{"", "a struct", ContractLevelFor[levelFlat](""), promptOnly},
		// The alias names the call path knows the operation as.
		{"negotiate", "a struct", ContractLevelFor[levelFlat]("negotiate"), wellFormed},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s into %s: %s, want %s", tc.operation, tc.name, tc.got, tc.want)
		}
	}
	if got := ContractLevelForType("transform", nil); got != wellFormed {
		t.Errorf("no type at all is %s, and has no schema", got)
	}
	if got := ContractLevelForType("transform", reflect.TypeFor[levelFlat]()); got != schema {
		t.Errorf("the run-time form of a struct is %s", got)
	}
}

// Validate's schema follows the type only when a correction is asked for: the
// envelope then holds the corrected value, and one that cannot be written as a
// strict schema leaves the whole request without one. Without a correction the
// schema is sent for any type, a map included.
func TestValidateContractFollowsTheTypeOnlyWhenACorrectionIsAskedFor(t *testing.T) {
	cases := []struct {
		name        string
		autoCorrect bool
		got         func(bool) types.ContractLevel
		want        types.ContractLevel
	}{
		{"a struct, corrected", true, ValidateContract[levelFlat], types.ContractSchemaConstrained},
		{"a struct, not corrected", false, ValidateContract[levelFlat], types.ContractSchemaConstrained},
		{"a map, corrected", true, ValidateContract[map[string]string], types.ContractJSONWellFormed},
		{"a map, not corrected", false, ValidateContract[map[string]string], types.ContractSchemaConstrained},
		{"an interface, corrected", true, ValidateContract[any], types.ContractJSONWellFormed},
		{"an interface, not corrected", false, ValidateContract[any], types.ContractSchemaConstrained},
		{"a recursive type, corrected", true, ValidateContract[levelRecursive], types.ContractJSONWellFormed},
		{"a recursive type, not corrected", false, ValidateContract[levelRecursive], types.ContractSchemaConstrained},
		{"seven structs, corrected", true, ValidateContract[levelDeep7], types.ContractJSONWellFormed},
		{"seven structs, not corrected", false, ValidateContract[levelDeep7], types.ContractSchemaConstrained},
	}
	for _, tc := range cases {
		if got := tc.got(tc.autoCorrect); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}

	// The same map, sent. The level for it has to be what the request carries.
	for _, autoCorrect := range []bool{true, false} {
		probe := newContractProbe(t, `{}`)
		options := NewValidateOptions().WithRules("every field is set")
		options.AutoCorrect = autoCorrect
		_, _ = ValidateHybrid(map[string]string{"k": "v"}, options)
		sent := probe.first(t).JSONSchema != nil
		if level := ValidateContract[map[string]string](autoCorrect); (level >= types.ContractSchemaConstrained) != sent {
			t.Errorf("AutoCorrect %v: the level is %s and the request declared a schema: %v", autoCorrect, level, sent)
		}
	}
}

func TestContractLevelForAllocatesNothingAfterTheFirstCall(t *testing.T) {
	type fresh struct{ Only string }
	for _, operation := range []string{"extract", "transform", "generate", "question", "compress", "normalize", "validate", "exp.predict", "exp.decompose", "filter", "nonsense"} {
		ContractLevelFor[map[string]any](operation)
		ContractLevelFor[fresh](operation)
		ContractLevelFor[levelDeep6](operation)
		for name, measure := range map[string]func(){
			"a map":    func() { ContractLevelFor[map[string]any](operation) },
			"a struct": func() { ContractLevelFor[fresh](operation) },
			"a chain":  func() { ContractLevelFor[levelDeep6](operation) },
			"run time": func() { ContractLevelForType(operation, reflect.TypeFor[fresh]()) },
		} {
			if allocations := testing.AllocsPerRun(1000, measure); allocations != 0 {
				t.Errorf("%s into %s allocated %v times", operation, name, allocations)
			}
		}
	}
	if allocations := testing.AllocsPerRun(1000, func() { ValidateContract[levelFlat](true); ValidateContract[levelFlat](false) }); allocations != 0 {
		t.Errorf("ValidateContract allocated %v times", allocations)
	}
}

// Goroutines that meet a type together are all given the level the request
// builder would give, whichever of them worked it out.
func TestContractLevelForAnswersTheSameToEveryGoroutine(t *testing.T) {
	type late struct {
		Name string
		Bag  map[string]string
	}
	type early struct{ Name string }
	var wait sync.WaitGroup
	results := make([][2]types.ContractLevel, 64)
	for i := range results {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[i] = [2]types.ContractLevel{ContractLevelFor[late]("extract"), ContractLevelFor[early]("extract")}
		}()
	}
	wait.Wait()
	for i, got := range results {
		if got[0] != types.ContractJSONWellFormed || got[1] != types.ContractSchemaConstrained {
			t.Fatalf("goroutine %d was told %s and %s", i, got[0], got[1])
		}
	}
}

// levelForm is a call whose Record states a level, made into a map and into a
// struct.
type levelForm struct {
	name      string
	operation string
	reply     string
	mapForm   func(ctx context.Context) (types.Meta, error)
	structOf  func(ctx context.Context) (types.Meta, error)
	// wantMap is the level a call into the map delivers.
	wantMap types.ContractLevel
}

// What a caller of the old root can observe: a Result form into a type no
// schema can be built for reports the level of a call that sent none, and a
// floor of schema constrained refuses it before anything is sent. The same call
// into a struct reports schema constrained and is admitted.
func TestResultFormsAreHeldToTheLevelOfTheirType(t *testing.T) {
	type record struct {
		Name  string `json:"name"`
		Total int    `json:"total"`
	}
	run := func(t *testing.T, floor types.ContractLevel, reply string, form func(ctx context.Context) (types.Meta, error)) (types.Meta, error, *captureProvider) {
		t.Helper()
		llm.ResetCapabilityRegistryForTest()
		t.Cleanup(llm.ResetCapabilityRegistryForTest)
		provider := &captureProvider{resp: llm.CompletionResponse{Content: reply, FinishReason: "stop", Provider: "local"}}
		llm.DeclareRoute(provider.Name(), "gpt-6-luna", []string{"eu"}, 0)
		configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gpt-6-luna")
		ctx := WithContractPolicy(WithProvider(context.Background(), provider), types.DataPolicy{MinimumContract: floor})
		meta, err := form(ctx)
		return meta, err, provider
	}

	forms := []levelForm{
		{
			"ExtractResult", "extract", `{"a":"b"}`,
			func(ctx context.Context) (types.Meta, error) {
				options := NewExtractOptions()
				options.CommonOptions.Context = ctx
				result, err := ExtractResult[map[string]string]("a, b", options)
				return result.Meta, err
			},
			func(ctx context.Context) (types.Meta, error) {
				options := NewExtractOptions()
				options.CommonOptions.Context = ctx
				result, err := ExtractResult[record]("a, 1", options)
				return result.Meta, err
			},
			types.ContractJSONWellFormed,
		},
		{
			"ExtractResult in strict mode", "extract", `{"a":"b"}`,
			func(ctx context.Context) (types.Meta, error) {
				options := NewExtractOptions().WithMode(types.Strict)
				options.CommonOptions.Context = ctx
				result, err := ExtractResult[map[string]string]("a, b", options)
				return result.Meta, err
			},
			func(ctx context.Context) (types.Meta, error) {
				options := NewExtractOptions().WithMode(types.Strict)
				options.CommonOptions.Context = ctx
				result, err := ExtractResult[record]("a, 1", options)
				return result.Meta, err
			},
			types.ContractJSONWellFormed,
		},
		{
			"GenerateResult", "generate", `{"a":"b"}`,
			func(ctx context.Context) (types.Meta, error) {
				options := NewGenerateOptions()
				options.CommonOptions.Context = ctx
				result, err := GenerateResult[map[string]string]("a map", options)
				return result.Meta, err
			},
			func(ctx context.Context) (types.Meta, error) {
				options := NewGenerateOptions()
				options.CommonOptions.Context = ctx
				result, err := GenerateResult[record]("a record", options)
				return result.Meta, err
			},
			types.ContractJSONWellFormed,
		},
	}
	// The operations that have no Result form of their own are run under
	// Recorded, which is what v2 does for them and which takes the level from
	// ContractLevelFor.
	record2 := func(operation string, level types.ContractLevel, call func(ctx context.Context) error) func(ctx context.Context) (types.Meta, error) {
		return func(ctx context.Context) (types.Meta, error) {
			result, err := Recorded(ctx, operation, level, func(ctx context.Context) (struct{}, error) {
				return struct{}{}, call(ctx)
			})
			return result.Meta, err
		}
	}
	forms = append(forms, []levelForm{
		{"Transform", "transform", `{"a":"b"}`,
			record2("transform", ContractLevelFor[map[string]string]("transform"), func(ctx context.Context) error {
				options := NewTransformOptions()
				options.CommonOptions.Context = ctx
				_, err := Transform[contractSource, map[string]string](contractSource{"a", 1}, options)
				return err
			}),
			record2("transform", ContractLevelFor[record]("transform"), func(ctx context.Context) error {
				options := NewTransformOptions()
				options.CommonOptions.Context = ctx
				_, err := Transform[contractSource, record](contractSource{"a", 1}, options)
				return err
			}), types.ContractJSONWellFormed},
		{"Question", "question", `{"answer":{"a":"b"},"confidence":0.5,"reasoning":"r","evidence":["e"]}`,
			record2("question", ContractLevelFor[map[string]string]("question"), func(ctx context.Context) error {
				options := NewQuestionOptions("why?")
				options.CommonOptions.Context = ctx
				_, err := Question[string, map[string]string]("data", options)
				return err
			}),
			record2("question", ContractLevelFor[record]("question"), func(ctx context.Context) error {
				options := NewQuestionOptions("why?")
				options.CommonOptions.Context = ctx
				_, err := Question[string, record]("data", options)
				return err
			}), types.ContractJSONWellFormed},
		{"Compress", "compress", `{"compressed":{"a":"b"},"preserved_info":[],"removed_info":[]}`,
			record2("compress", ContractLevelFor[map[string]string]("compress"), func(ctx context.Context) error {
				options := NewCompressOptions()
				options.CommonOptions.Context = ctx
				_, err := Compress(map[string]string{"a": "b"}, options)
				return err
			}),
			record2("compress", ContractLevelFor[record]("compress"), func(ctx context.Context) error {
				options := NewCompressOptions()
				options.CommonOptions.Context = ctx
				_, err := Compress(record{"a", 1}, options)
				return err
			}), types.ContractJSONWellFormed},
		{"Normalize", "normalize", `{"normalized":{"a":"b"},"changes":[],"warnings":[]}`,
			record2("normalize", ContractLevelFor[map[string]string]("normalize"), func(ctx context.Context) error {
				options := NewNormalizeOptions()
				options.CommonOptions.Context = ctx
				_, err := Normalize(map[string]string{"a": "b"}, options)
				return err
			}),
			record2("normalize", ContractLevelFor[record]("normalize"), func(ctx context.Context) error {
				options := NewNormalizeOptions()
				options.CommonOptions.Context = ctx
				_, err := Normalize(record{"a", 1}, options)
				return err
			}), types.ContractJSONWellFormed},
		{"Validate", "validate", `{"errors":[],"warnings":[],"info":[],"confidence":0.9,"summary":"ok"}`,
			record2("validate", ContractLevelFor[map[string]string]("validate"), func(ctx context.Context) error {
				options := NewValidateOptions().WithRules("every field is set")
				options.CommonOptions.Context = ctx
				_, err := ValidateHybrid(map[string]string{"a": "b"}, options)
				return err
			}),
			record2("validate", ContractLevelFor[record]("validate"), func(ctx context.Context) error {
				options := NewValidateOptions().WithRules("every field is set")
				options.CommonOptions.Context = ctx
				_, err := ValidateHybrid(record{"a", 1}, options)
				return err
			}), types.ContractJSONWellFormed},
		{"Predict", "exp.predict", `{"prediction":{"a":"b"},"confidence":0.5}`,
			record2("exp.predict", ContractLevelFor[map[string]string]("exp.predict"), func(ctx context.Context) error {
				options := NewPredictOptions()
				options.CommonOptions.Context = ctx
				_, err := Predict[map[string]string]([]int{1, 2}, options)
				return err
			}),
			record2("exp.predict", ContractLevelFor[record]("exp.predict"), func(ctx context.Context) error {
				options := NewPredictOptions()
				options.CommonOptions.Context = ctx
				_, err := Predict[record]([]int{1, 2}, options)
				return err
			}), types.ContractJSONWellFormed},
		{"Decompose", "exp.decompose", `{"parts":[],"root_parts":[]}`,
			record2("exp.decompose", ContractLevelFor[map[string]string]("exp.decompose"), func(ctx context.Context) error {
				options := NewDecomposeOptions()
				options.CommonOptions.Context = ctx
				_, err := Decompose(map[string]string{"a": "b"}, options)
				return err
			}),
			record2("exp.decompose", ContractLevelFor[record]("exp.decompose"), func(ctx context.Context) error {
				options := NewDecomposeOptions()
				options.CommonOptions.Context = ctx
				_, err := Decompose(record{"a", 1}, options)
				return err
			}), types.ContractJSONWellFormed},
	}...)

	for _, form := range forms {
		t.Run(form.name+" into a map is refused under a schema-constrained floor", func(t *testing.T) {
			meta, err, provider := run(t, types.ContractSchemaConstrained, form.reply, form.mapForm)
			if !errors.Is(err, types.ErrPolicyViolation) || !errors.Is(err, ErrContractDegraded) {
				t.Fatalf("err = %v, want the floor's refusal", err)
			}
			if provider.attempts != 0 {
				t.Errorf("the provider was called %d times for a call that could never meet the floor", provider.attempts)
			}
			if meta.RequestedContract != form.wantMap {
				t.Errorf("the record says %s was requested, want %s", meta.RequestedContract, form.wantMap)
			}
		})
		t.Run(form.name+" into a map is admitted under a well-formed floor and reports it", func(t *testing.T) {
			meta, err, provider := run(t, types.ContractJSONWellFormed, form.reply, form.mapForm)
			if errors.Is(err, ErrContractDegraded) {
				t.Fatalf("refused under a floor it meets: %v", err)
			}
			if provider.attempts == 0 {
				t.Fatal("nothing was sent")
			}
			if provider.req.JSONSchema != nil {
				t.Error("a schema was sent for a map")
			}
			if err == nil && meta.DeliveredContract != form.wantMap {
				t.Errorf("delivered %s, want %s", meta.DeliveredContract, form.wantMap)
			}
			if meta.RequestedContract != form.wantMap {
				t.Errorf("requested %s, want %s", meta.RequestedContract, form.wantMap)
			}
		})
		t.Run(form.name+" into a struct is admitted under a schema-constrained floor", func(t *testing.T) {
			meta, err, provider := run(t, types.ContractSchemaConstrained, form.reply, form.structOf)
			if errors.Is(err, ErrContractDegraded) {
				t.Fatalf("refused under a floor it meets: %v", err)
			}
			if provider.attempts == 0 {
				t.Fatal("nothing was sent")
			}
			if provider.req.JSONSchema == nil {
				t.Error("no schema was sent for a struct")
			}
			if meta.RequestedContract < types.ContractSchemaConstrained {
				t.Errorf("requested %s for a struct", meta.RequestedContract)
			}
		})
	}
}
