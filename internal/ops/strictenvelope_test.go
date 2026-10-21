package ops

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// LX-003. The envelopes Question, Compress, Predict and Decompose wrote by hand
// were refused by the live API in strict mode. strictEnvelope is the one place
// an envelope is now built, and these cases pin the two rules it exists to
// keep: the object is closed, and every property is in required.
func TestStrictEnvelope(t *testing.T) {
	str := map[string]any{"type": "string"}
	num := map[string]any{"type": "number"}
	list := map[string]any{"type": "array", "items": str}
	object := map[string]any{"type": "object", "properties": map[string]any{"a": str}, "required": []string{"a"}, "additionalProperties": false}
	union := map[string]any{"anyOf": []any{str, num}}
	already := map[string]any{"type": []string{"string", "null"}}

	cases := []struct {
		name       string
		properties map[string]any
		required   []string
		// optional maps a property that was not required to the JSON it must
		// have been widened to.
		optional map[string]string
	}{
		{"one required", map[string]any{"answer": str}, []string{"answer"}, nil},
		{"one required, one optional scalar", map[string]any{"answer": str, "confidence": num}, []string{"answer"},
			map[string]string{"confidence": `{"type":["number","null"]}`}},
		{"optional list", map[string]any{"a": str, "items": list}, []string{"a"},
			map[string]string{"items": `{"items":{"type":"string"},"type":["array","null"]}`}},
		{"optional object", map[string]any{"a": str, "inner": object}, []string{"a"},
			map[string]string{"inner": `{"additionalProperties":false,"properties":{"a":{"type":"string"}},"required":["a"],"type":["object","null"]}`}},
		{"optional union without a type keyword", map[string]any{"a": str, "u": union}, []string{"a"},
			map[string]string{"u": `{"anyOf":[{"anyOf":[{"type":"string"},{"type":"number"}]},{"type":"null"}]}`}},
		{"optional that already admits null is left alone", map[string]any{"a": str, "n": already}, []string{"a"},
			map[string]string{"n": `{"type":["string","null"]}`}},
		{"everything required", map[string]any{"a": str, "b": num}, []string{"a", "b"}, nil},
		{"nothing required", map[string]any{"a": str, "b": num}, nil,
			map[string]string{"a": `{"type":["string","null"]}`, "b": `{"type":["number","null"]}`}},
		{"no properties", map[string]any{}, nil, nil},
		{"many properties", map[string]any{"z": str, "m": str, "a": str, "k": str}, []string{"m"},
			map[string]string{"z": `{"type":["string","null"]}`}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The inputs must survive: an envelope builder that widened the
			// caller's own schema map would change the next call's schema.
			before, _ := json.Marshal(tc.properties)

			schema := strictEnvelope(tc.properties, tc.required...)

			if problem := llm.StrictSchemaProblem(schema); problem != "" {
				t.Fatalf("the envelope breaks a strict-mode rule: %s", problem)
			}

			required, _ := schema["required"].([]string)
			want := make([]string, 0, len(tc.properties))
			for name := range tc.properties {
				want = append(want, name)
			}
			sort.Strings(want)
			if len(required) != len(want) || (len(want) > 0 && !reflect.DeepEqual(required, want)) {
				t.Fatalf("required = %v, want every property, sorted: %v", required, want)
			}

			declared, _ := schema["properties"].(map[string]any)
			for name, wantJSON := range tc.optional {
				got, _ := json.Marshal(declared[name])
				if string(got) != wantJSON {
					t.Fatalf("optional property %q = %s, want %s", name, got, wantJSON)
				}
			}
			for _, name := range tc.required {
				if !reflect.DeepEqual(declared[name], tc.properties[name]) {
					t.Fatalf("required property %q was altered", name)
				}
			}

			if after, _ := json.Marshal(tc.properties); string(after) != string(before) {
				t.Fatalf("strictEnvelope modified its input: %s became %s", before, after)
			}
		})
	}
}

// GenerateJSONSchema returns nil for a type strict mode cannot express. Dropped
// into an envelope, that nil went onto the wire as `"field": null` or as a
// union with a null member, which the API rejects and the validator at the time
// did not notice (found in review of LX-003). An envelope with a property it
// cannot describe is not an envelope.
func TestStrictEnvelopeReturnsNilWhenAPropertyHasNoSchema(t *testing.T) {
	str := map[string]any{"type": "string"}
	var typedNil map[string]any
	inexpressible := GenerateJSONSchema(reflect.TypeOf(map[string]any{}))

	cases := []struct {
		name       string
		properties map[string]any
		required   []string
	}{
		{"a required property is a typed nil", map[string]any{"a": typedNil}, []string{"a"}},
		{"an optional property is a typed nil", map[string]any{"a": str, "b": typedNil}, []string{"a"}},
		{"an untyped nil", map[string]any{"a": nil}, nil},
		{"an empty schema", map[string]any{"a": map[string]any{}}, nil},
		{"the schema of a map type", map[string]any{"a": str, "meta": inexpressible}, []string{"a"}},
		{"a value that is not a schema", map[string]any{"a": "string"}, nil},
		{"a list of nothing", map[string]any{"a": str, "items": listOf(nil)}, []string{"a"}},
		{"a list of an inexpressible type", map[string]any{"items": listOf(inexpressible)}, nil},
		{"a nested envelope that was itself nil", map[string]any{"inner": strictEnvelope(map[string]any{"x": typedNil})}, nil},
		{"a list of a nil envelope", map[string]any{"parts": listOf(strictEnvelope(map[string]any{"x": typedNil}))}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if schema := strictEnvelope(tc.properties, tc.required...); schema != nil {
				raw, _ := json.Marshal(schema)
				t.Fatalf("built an envelope around a missing schema: %s", raw)
			}
		})
	}
}

// A required name that is not a property is a mistake in the calling code. It
// used to be ignored, which turned the field the author meant into an optional
// one with no signal.
func TestStrictEnvelopePanicsOnARequiredNameThatIsNotAProperty(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a misspelt required name was accepted")
		}
	}()
	strictEnvelope(map[string]any{"answer": map[string]any{"type": "string"}}, "anwser")
}

func TestAcceptingNull(t *testing.T) {
	cases := []struct {
		name   string
		schema map[string]any
		want   string
	}{
		{"string", map[string]any{"type": "string"}, `{"type":["string","null"]}`},
		{"number", map[string]any{"type": "number"}, `{"type":["number","null"]}`},
		{"integer", map[string]any{"type": "integer"}, `{"type":["integer","null"]}`},
		{"boolean", map[string]any{"type": "boolean"}, `{"type":["boolean","null"]}`},
		{"array", map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, `{"items":{"type":"string"},"type":["array","null"]}`},
		{"keeps other keywords", map[string]any{"type": "string", "format": "date-time"}, `{"format":"date-time","type":["string","null"]}`},
		{"already null", map[string]any{"type": "null"}, `{"type":"null"}`},
		{"already a list with null", map[string]any{"type": []string{"string", "null"}}, `{"type":["string","null"]}`},
		{"a list without null becomes a union", map[string]any{"type": []string{"string", "integer"}}, `{"anyOf":[{"type":["string","integer"]},{"type":"null"}]}`},
		{"no type keyword becomes a union", map[string]any{"anyOf": []any{map[string]any{"type": "string"}}}, `{"anyOf":[{"anyOf":[{"type":"string"}]},{"type":"null"}]}`},
		{"an enum without a type becomes a union", map[string]any{"enum": []string{"a", "b"}}, `{"anyOf":[{"enum":["a","b"]},{"type":"null"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := json.Marshal(tc.schema)
			got, _ := json.Marshal(acceptingNull(tc.schema))
			if string(got) != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
			if after, _ := json.Marshal(tc.schema); string(after) != string(before) {
				t.Fatalf("acceptingNull modified its input")
			}
		})
	}
}

// The render must not depend on map iteration order: it is part of the request
// and of the prompt-cache prefix.
func TestStrictEnvelopeIsDeterministic(t *testing.T) {
	properties := map[string]any{}
	for _, name := range strings.Fields("q w e r t y u i o p a s d f g h j k l") {
		properties[name] = map[string]any{"type": "string"}
	}
	first, _ := json.Marshal(strictEnvelope(properties, "q"))
	for i := 0; i < 50; i++ {
		again, _ := json.Marshal(strictEnvelope(properties, "q"))
		if string(again) != string(first) {
			t.Fatalf("render %d differs from the first", i)
		}
	}
}

// modelFieldNames lists a struct's JSON field names, leaving out the ones this
// library fills itself and never asks a model for.
func modelFieldNames(t reflect.Type, skip ...string) []string {
	skipped := map[string]bool{}
	for _, name := range skip {
		skipped[name] = true
	}
	var names []string
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" && !skipped[name] {
			names = append(names, name)
		}
	}
	return names
}

func schemaProperties(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties: %v", schema)
	}
	return properties
}

func isRequiredNonNull(schema map[string]any, name string) bool {
	properties, _ := schema["properties"].(map[string]any)
	property, _ := properties[name].(map[string]any)
	// Only the property's own type matters: a list whose items have an
	// optional field is still a list that must be present.
	switch kind := property["type"].(type) {
	case string:
		return kind != "null"
	case []string:
		for _, name := range kind {
			if name == "null" {
				return false
			}
		}
		return true
	}
	return false
}

// Predict declares its envelope by hand, because PredictResult carries a
// Metadata map no strict schema can express. A closed schema forbids anything
// it does not declare, so a field added to the struct and not to the schema
// could never be returned. This reads the schema Predict actually sends.
//
// The first version of this test compared the struct with a list written in
// the test, so deleting a property from the real schema left it passing.
func TestPredictSchemaCoversTheEnvelope(t *testing.T) {
	number := map[string]any{"type": "number"}
	schema := predictResponseSchema(number, NewPredictOptions())
	if problem := llm.StrictSchemaProblem(schema); problem != "" {
		t.Fatalf("Predict's schema breaks a strict-mode rule: %s", problem)
	}

	properties := schemaProperties(t, schema)
	for _, name := range modelFieldNames(reflect.TypeOf(PredictResult[float64]{}), "metadata") {
		if _, ok := properties[name]; !ok {
			t.Errorf("PredictResult field %q is not declared in Predict's response schema", name)
		}
	}

	scenarios, _ := properties["scenarios"].(map[string]any)
	scenario, _ := scenarios["items"].(map[string]any)
	scenarioProperties := schemaProperties(t, scenario)
	for _, name := range modelFieldNames(reflect.TypeOf(PredictionScenario{})) {
		if _, ok := scenarioProperties[name]; !ok {
			t.Errorf("PredictionScenario field %q is not declared in the scenario schema", name)
		}
	}
}

// What the caller asked for has to come back as a value, and what they did not
// ask for may be null. The interval is the stated exception.
func TestPredictSchemaRequiresWhatTheCallerAskedFor(t *testing.T) {
	number := map[string]any{"type": "number"}
	cases := []struct {
		name      string
		configure func(PredictOptions) PredictOptions
		field     string
		nonNull   bool
	}{
		{"prediction is always a value", func(o PredictOptions) PredictOptions { return o }, "prediction", true},
		{"confidence is always a value", func(o PredictOptions) PredictOptions { return o }, "confidence", true},
		{"reasoning when asked", func(o PredictOptions) PredictOptions { o.IncludeReasoning = true; return o }, "reasoning", true},
		{"reasoning when not asked", func(o PredictOptions) PredictOptions { o.IncludeReasoning = false; return o }, "reasoning", false},
		{"scenarios when asked", func(o PredictOptions) PredictOptions { o.IncludeScenarios = true; return o }, "scenarios", true},
		{"scenarios when not asked", func(o PredictOptions) PredictOptions { o.IncludeScenarios = false; return o }, "scenarios", false},
		{"the interval may be null even when asked", func(o PredictOptions) PredictOptions { o.IncludeConfidenceInterval = true; return o }, "interval", false},
		{"the interval when not asked", func(o PredictOptions) PredictOptions { o.IncludeConfidenceInterval = false; return o }, "interval", false},
		{"factors may be null", func(o PredictOptions) PredictOptions { return o }, "factors", false},
		{"assumptions may be null", func(o PredictOptions) PredictOptions { return o }, "assumptions", false},
		{"risks may be null", func(o PredictOptions) PredictOptions { return o }, "risks", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := predictResponseSchema(number, tc.configure(NewPredictOptions()))
			if problem := llm.StrictSchemaProblem(schema); problem != "" {
				t.Fatalf("schema breaks a strict-mode rule: %s", problem)
			}
			if got := isRequiredNonNull(schema, tc.field); got != tc.nonNull {
				t.Fatalf("%s must be a real value = %v, want %v", tc.field, got, tc.nonNull)
			}
		})
	}

	if predictResponseSchema(nil, NewPredictOptions()) != nil {
		t.Fatal("a prediction type with no schema still produced an envelope")
	}
}

// Decompose's first strict schema declared eight of DecomposedPart's ten model
// fields. Strict mode forbids the rest, so `estimate` and `children` could no
// longer be returned, and a caller who set IncludeEstimates got empty estimates
// and no error. This reads the schema Decompose actually sends.
func TestDecomposeSchemaCoversEveryPartField(t *testing.T) {
	text := map[string]any{"type": "string"}
	for _, includeEstimates := range []bool{false, true} {
		schema := decomposeResponseSchema(text, includeEstimates)
		if problem := llm.StrictSchemaProblem(schema); problem != "" {
			t.Fatalf("Decompose's schema breaks a strict-mode rule: %s", problem)
		}

		parts, _ := schemaProperties(t, schema)["parts"].(map[string]any)
		part, _ := parts["items"].(map[string]any)
		partProperties := schemaProperties(t, part)
		for _, name := range modelFieldNames(reflect.TypeOf(DecomposedPart[string]{}), "metadata") {
			if _, ok := partProperties[name]; !ok {
				t.Errorf("DecomposedPart field %q is not declared in the part schema", name)
			}
		}

		// depth feeds the MaxDepth check and order feeds the sort: a null
		// there would decode to zero and be acted on.
		for _, name := range []string{"id", "name", "content", "depth", "order"} {
			if !isRequiredNonNull(part, name) {
				t.Errorf("part field %q may be null; it must be a real value", name)
			}
		}
		if got := isRequiredNonNull(part, "estimate"); got != includeEstimates {
			t.Errorf("IncludeEstimates=%v: estimate must be a real value = %v", includeEstimates, got)
		}
		for _, name := range []string{"description", "parent_id", "children", "dependencies"} {
			if isRequiredNonNull(part, name) {
				t.Errorf("part field %q is forced to a value; a root part has no parent and a leaf has no children", name)
			}
		}
	}

	if decomposeResponseSchema(nil, false) != nil {
		t.Fatal("a content type with no schema still produced an envelope")
	}
}

// The schema is what the operation sends, not only what the helper returns.
func TestDecomposeSendsItsSchemaAndHonoursIncludeEstimates(t *testing.T) {
	var seen types.OpOptions
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(_ context.Context, _, _ string, opt types.OpOptions) (string, error) {
		seen = opt
		return `{"parts":[{"id":"p1","name":"Design","description":null,"content":"Design the page","parent_id":null,"children":null,"dependencies":null,"depth":0,"order":1,"estimate":"2 hours"}],"root_parts":["p1"]}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	result, err := Decompose[string]("Design the page.", NewDecomposeOptions().WithIncludeEstimates(true))
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if seen.SchemaName != "decomposition" || len(seen.JSONSchema) == 0 {
		t.Fatalf("Decompose sent schema %q with %d keys", seen.SchemaName, len(seen.JSONSchema))
	}
	if problem := llm.StrictSchemaProblem(seen.JSONSchema); problem != "" {
		t.Fatalf("the schema sent breaks a strict-mode rule: %s", problem)
	}
	if len(result.Parts) != 1 || result.Parts[0].Estimate != "2 hours" {
		t.Fatalf("the estimate the caller asked for did not come back: %+v", result.Parts)
	}
}

// LX-003 review, finding 11. A response with no answer, or a null one, came
// back as a zero-valued Answer and a nil error: "false" for a yes/no question
// the model did not answer.
func TestQuestionRefusesAMissingOrNullAnswer(t *testing.T) {
	type verdict struct {
		Eligible bool `json:"eligible"`
	}
	// What these cases are about is the answer, so the confidence, the reasoning
	// and the evidence are not asked for: a call that asks for them requires them
	// of the reply (TestQuestionRefusesAnAnswerMissingAnyMemberItAsksFor).
	answerOnly := NewQuestionOptions("q?").WithIncludeConfidence(false).WithIncludeReasoning(false).WithIncludeEvidence(false)
	ask := func(run func() error, body string) error {
		installAnalysisResponse(t, body)
		return run()
	}
	boolQ := func() error {
		_, err := Question[string, bool]("data", answerOnly)
		return err
	}
	stringQ := func() error {
		_, err := Question[string, string]("data", answerOnly)
		return err
	}
	structQ := func() error {
		_, err := Question[string, verdict]("data", answerOnly)
		return err
	}
	pointerQ := func() error {
		_, err := Question[string, *verdict]("data", answerOnly)
		return err
	}
	sliceQ := func() error {
		_, err := Question[string, []string]("data", answerOnly)
		return err
	}
	anyQ := func() error {
		_, err := Question[string, any]("data", answerOnly)
		return err
	}
	// A named string type is still text, and an empty one is still no answer.
	type label string
	labelQ := func() error {
		_, err := Question[string, label]("data", answerOnly)
		return err
	}
	stringPointerQ := func() error {
		_, err := Question[string, *string]("data", answerOnly)
		return err
	}
	bytesQ := func() error {
		_, err := Question[string, []byte]("data", answerOnly)
		return err
	}
	rawQ := func() error {
		_, err := Question[string, json.RawMessage]("data", answerOnly)
		return err
	}

	cases := []struct {
		name    string
		run     func() error
		body    string
		wantErr bool
	}{
		{"bool, answered true", boolQ, `{"answer":true,"confidence":0.9}`, false},
		{"bool, answered false", boolQ, `{"answer":false,"confidence":0.9}`, false},
		{"bool, no answer field", boolQ, `{"confidence":0.9}`, true},
		{"bool, null answer", boolQ, `{"answer":null,"confidence":0.9}`, true},
		{"string, answered", stringQ, `{"answer":"forty","confidence":0.9}`, false},
		{"string, an empty string is no answer", stringQ, `{"answer":"","confidence":0.9}`, true},
		// A space is an answer to "which character separates the fields".
		{"string, whitespace is an answer", stringQ, `{"answer":"  ","confidence":0.9}`, false},
		{"named string, answered", labelQ, `{"answer":"urgent","confidence":0.9}`, false},
		{"named string, an empty string is no answer", labelQ, `{"answer":"","confidence":0.9}`, true},
		{"named string, null answer", labelQ, `{"answer":null}`, true},
		// An empty string is no answer whatever it is decoded into. A pointer
		// says there may be none with null, not with "".
		{"string pointer, an empty string is no answer", stringPointerQ, `{"answer":""}`, true},
		{"string pointer, answered", stringPointerQ, `{"answer":"forty"}`, false},
		{"string pointer, null is a value", stringPointerQ, `{"answer":null}`, false},
		{"any, an empty string is no answer", anyQ, `{"answer":""}`, true},
		{"bytes, an empty string is no answer", bytesQ, `{"answer":""}`, true},
		{"raw JSON, an empty string is no answer", rawQ, `{"answer":""}`, true},
		{"raw JSON, answered", rawQ, `{"answer":{"k":1}}`, false},
		// A number or a boolean is an answer in text form. An object or a list
		// is not text, and used to be handed back as its JSON source.
		{"string, a number is its text", stringQ, `{"answer":42}`, false},
		{"string, a boolean is its text", stringQ, `{"answer":true}`, false},
		{"string, an object is not text", stringQ, `{"answer":{}}`, true},
		{"string, a list is not text", stringQ, `{"answer":["a"]}`, true},
		{"named string, a number is its text", labelQ, `{"answer":42}`, false},
		{"named string, an object is not text", labelQ, `{"answer":{"k":1}}`, true},
		{"string, no answer field", stringQ, `{"confidence":0.9,"reasoning":"none"}`, true},
		{"string, null answer", stringQ, `{"answer":null}`, true},
		{"struct, answered", structQ, `{"answer":{"eligible":true}}`, false},
		{"struct, null answer", structQ, `{"answer":null}`, true},
		{"pointer, null is a value", pointerQ, `{"answer":null,"confidence":0.4}`, false},
		{"pointer, no answer field", pointerQ, `{"confidence":0.4}`, true},
		// The schema this operation declares for a slice is a non-null array, so
		// a null is a missing answer from a provider that did not enforce it.
		{"slice, null answer", sliceQ, `{"answer":null,"confidence":0.4}`, true},
		{"slice, an empty list is an answer", sliceQ, `{"answer":[]}`, false},
		{"any, null answer", anyQ, `{"answer":null}`, true},
		{"any, answered", anyQ, `{"answer":{"k":1}}`, false},
		{"slice, answered", sliceQ, `{"answer":["a","b"]}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ask(tc.run, tc.body)
			if tc.wantErr && err == nil {
				t.Fatal("a response with no usable answer was accepted")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Question: %v", err)
			}
		})
	}
}

// Every field in Question's envelope is there because the caller asked for it,
// so each must come back as a value. The first strict version made them unions
// with null, which enforced nothing: a requested confidence returned as null
// decoded to zero.
func TestQuestionSchemaRequiresWhatTheCallerAskedFor(t *testing.T) {
	cases := []struct {
		name                            string
		confidence, reasoning, evidence bool
	}{
		{"answer only", false, false, false},
		{"confidence", true, false, false},
		{"reasoning", false, true, false},
		{"evidence", false, false, true},
		{"confidence and reasoning", true, true, false},
		{"confidence and evidence", true, false, true},
		{"reasoning and evidence", false, true, true},
		{"everything", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen types.OpOptions
			previousCaller, previousProvider := currentHooks()
			setLLMCaller(func(_ context.Context, _, _ string, opt types.OpOptions) (string, error) {
				seen = opt
				return `{"answer":true,"confidence":0.9,"reasoning":"r","evidence":["e"]}`, nil
			})
			t.Cleanup(func() {
				setLLMCaller(previousCaller)
				SetDefaultProvider(previousProvider)
			})

			opts := NewQuestionOptions("q?")
			opts.IncludeConfidence, opts.IncludeReasoning, opts.IncludeEvidence = tc.confidence, tc.reasoning, tc.evidence
			if _, err := Question[string, bool]("data", opts); err != nil {
				t.Fatalf("Question: %v", err)
			}
			if problem := llm.StrictSchemaProblem(seen.JSONSchema); problem != "" {
				t.Fatalf("the schema sent breaks a strict-mode rule: %s", problem)
			}

			properties := schemaProperties(t, seen.JSONSchema)
			want := map[string]bool{"answer": true, "confidence": tc.confidence, "reasoning": tc.reasoning, "evidence": tc.evidence}
			for name, present := range want {
				if _, ok := properties[name]; ok != present {
					t.Errorf("%s declared = %v, want %v", name, ok, present)
				}
				if present && !isRequiredNonNull(seen.JSONSchema, name) {
					t.Errorf("%s may be null although the caller asked for it", name)
				}
			}
		})
	}
}

// IncludeEstimates was a sentence in the prompt. The schema now requires the
// field, but a schema binds only a provider that enforces one, and an empty
// string satisfies it anywhere.
func TestDecomposeRefusesAMissingEstimateWhenEstimatesWereRequested(t *testing.T) {
	// Each part gets an id of its own: the parts of an answer are refused when
	// two share one.
	next := 0
	part := func(estimate string) string {
		next++
		return `{"id":"p` + strconv.Itoa(next) + `","name":"Design","content":"Design the page","depth":0,"order":1,"estimate":` + estimate + `}`
	}
	cases := []struct {
		name      string
		requested bool
		parts     string
		wantErr   bool
	}{
		{"requested and present", true, part(`"2 hours"`), false},
		{"requested and empty", true, part(`""`), true},
		{"requested and whitespace", true, part(`"  "`), true},
		{"requested and null", true, part(`null`), true},
		{"requested and absent", true, `{"id":"p1","name":"Design","content":"Design the page","depth":0,"order":1}`, true},
		{"requested, second part missing it", true, part(`"1 day"`) + `,` + part(`""`), true},
		{"not requested and absent", false, `{"id":"p1","name":"Design","content":"Design the page","depth":0,"order":1}`, false},
		{"not requested and null", false, part(`null`), false},
		{"not requested and present", false, part(`"2 hours"`), false},
		{"requested, two parts both present", true, part(`"1 day"`) + `,` + part(`"3 days"`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installAnalysisResponse(t, `{"parts":[`+tc.parts+`],"root_parts":[]}`)
			_, err := Decompose[string]("Design the page.", NewDecomposeOptions().WithIncludeEstimates(tc.requested))
			if tc.wantErr && err == nil {
				t.Fatal("a part with no estimate was accepted")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Decompose: %v", err)
			}
		})
	}
}

// A number given for a text answer comes back as the number's text, for string
// and for a named string type alike.
func TestQuestionReadsAScalarAsText(t *testing.T) {
	type label string
	answerOnly := NewQuestionOptions("q?").WithIncludeConfidence(false).WithIncludeReasoning(false).WithIncludeEvidence(false)

	installAnalysisResponse(t, `{"answer":42}`)
	plain, err := Question[string, string]("data", answerOnly)
	if err != nil || plain.Answer != "42" {
		t.Errorf("string: Answer = %q, err = %v", plain.Answer, err)
	}
	named, err := Question[string, label]("data", answerOnly)
	if err != nil || named.Answer != "42" {
		t.Errorf("named string: Answer = %q, err = %v", named.Answer, err)
	}

	installAnalysisResponse(t, `{"answer":false}`)
	plain, err = Question[string, string]("data", answerOnly)
	if err != nil || plain.Answer != "false" {
		t.Errorf("string: Answer = %q, err = %v", plain.Answer, err)
	}
}
