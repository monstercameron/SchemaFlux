package ops

import (
	"reflect"
	"testing"
)

// Operations that wrap the caller's type in an envelope of their own put the
// type's schema into the envelope as it is, and the type's plan holds one copy
// of that schema for every call. These tests run each such operation twice and
// require that the schema the second call sends is the schema the first sent,
// and that the plan's own copy is what it was before either call: nothing the
// operation does with the schema after building the envelope may change it.

type sharedSchemaItem struct {
	Name  string   `json:"name"`
	Count int      `json:"count"`
	Tags  []string `json:"tags"`
}

type sharedSchemaCase struct {
	name string
	// types are the types whose plans the operation puts into its envelope.
	types []reflect.Type
	run   func() error
	// declares is whether the operation declares a schema for the type it is
	// given. Enrich's envelope holds maps, which strict mode cannot express, so
	// it sends none.
	declares bool
}

func sharedSchemaCases() []sharedSchemaCase {
	item := sharedSchemaItem{Name: "a", Count: 1, Tags: []string{"x"}}
	itemType := reflect.TypeFor[sharedSchemaItem]()
	return []sharedSchemaCase{
		{"Compress", []reflect.Type{itemType}, func() error {
			_, err := Compress(item, NewCompressOptions())
			return err
		}, true},
		{"Decompose", []reflect.Type{itemType}, func() error {
			_, err := Decompose(item, NewDecomposeOptions())
			return err
		}, true},
		{"Enrich", nil, func() error {
			_, err := Enrich[sharedSchemaItem, sharedSchemaItem](item, NewEnrichOptions())
			return err
		}, false},
		{"Question", []reflect.Type{reflect.TypeFor[string]()}, func() error {
			_, err := Question[sharedSchemaItem, string](item, NewQuestionOptions("what is it?"))
			return err
		}, true},
		{"Normalize", nil, func() error {
			_, err := Normalize(item, NewNormalizeOptions())
			return err
		}, true},
		{"Predict", []reflect.Type{itemType, reflect.TypeFor[PredictionInterval](), reflect.TypeFor[[]PredictionFactor]()}, func() error {
			_, err := Predict[sharedSchemaItem]([]sharedSchemaItem{item, item}, NewPredictOptions())
			return err
		}, true},
		{"Validate with a correction", []reflect.Type{itemType}, func() error {
			opts := NewValidateOptions()
			opts.Rules = "the name is not empty"
			_, err := Validate(item, opts)
			return err
		}, true},
	}
}

func TestASecondCallSeesTheSchemaTheFirstSaw(t *testing.T) {
	for _, tc := range sharedSchemaCases() {
		t.Run(tc.name, func(t *testing.T) {
			calls := answerAndRecord(t, `{}`)
			snapshots := make([]map[string]any, len(tc.types))
			for i, kind := range tc.types {
				shared := typePlanFor(kind).sharedSchema()
				if shared == nil {
					t.Fatalf("the plan of %v holds no schema", kind)
				}
				snapshots[i] = cloneSchema(shared)
			}

			_ = tc.run()
			if len(*calls) != 1 {
				t.Fatalf("the first call sent %d requests", len(*calls))
			}
			if declared := (*calls)[0].opts.JSONSchema != nil; declared != tc.declares {
				t.Fatalf("the call declared a schema = %v, want %v", declared, tc.declares)
			}
			first := cloneSchema((*calls)[0].opts.JSONSchema)

			_ = tc.run()
			if len(*calls) != 2 {
				t.Fatalf("%d requests after two calls", len(*calls))
			}
			if second := (*calls)[1].opts.JSONSchema; !reflect.DeepEqual(second, first) {
				t.Errorf("the second call sent a schema other than the first's:\nfirst:  %v\nsecond: %v", first, second)
			}
			for i, kind := range tc.types {
				if !reflect.DeepEqual(typePlanFor(kind).sharedSchema(), snapshots[i]) {
					t.Errorf("the schema the plan of %v holds changed in two calls", kind)
				}
				if !reflect.DeepEqual(GenerateJSONSchema(kind), snapshots[i]) {
					t.Errorf("GenerateJSONSchema(%v) gives a schema other than the plan's was", kind)
				}
			}
		})
	}
}

// What the envelope costs in allocations once the schemas of the types in it
// are read from their plans and not copied or worked out again. Each budget is
// what was measured plus two; before, Compress made 73, Decompose 102, Question
// 64, Normalize 79, Predict 143 and Validate 100, and Enrich 42 (it sends no
// schema, since its envelope holds maps).
func TestEnvelopeSchemaAllocationsStayWithinBudget(t *testing.T) {
	typePlanFreshCache(t)
	budgets := map[string]float64{
		"Compress":                   61,
		"Decompose":                  90,
		"Enrich":                     44,
		"Question":                   63,
		"Normalize":                  35,
		"Predict":                    101,
		"Validate with a correction": 88,
	}
	typePlanQuietLogs(t)
	for _, tc := range sharedSchemaCases() {
		t.Run(tc.name, func(t *testing.T) {
			budget, ok := budgets[tc.name]
			if !ok {
				t.Fatalf("no budget is recorded for %s", tc.name)
			}
			answerAndRecord(t, `{}`)
			_ = tc.run()
			allocs := testing.AllocsPerRun(50, func() { _ = tc.run() })
			t.Logf("%s: %.0f allocations (budget %.0f)", tc.name, allocs, budget)
			if allocs > budget {
				t.Errorf("%s makes %.0f allocations, over its budget of %.0f", tc.name, allocs, budget)
			}
		})
	}
}
