package schemaflux_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// textContact is the structured value the Normalize and Merge tests use.
type textContact struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
}

var normalizeInput = textContact{Name: "ada  lovelace", Phone: "5550100"}

const (
	normalizeAnswer = `{"normalized":{"name":"Ada Lovelace","phone":"+1 555 0100"},"changes":[` +
		`{"field":"name","original":"ada  lovelace","normalized":"Ada Lovelace","reason":"case and spacing","confidence":0.9},` +
		`{"field":"phone","original":"5550100","normalized":"+1 555 0100","reason":"format","confidence":0.8}],"warnings":["no country given"]}`
	normalizePhoneOnly = `{"normalized":{"name":"ada  lovelace","phone":"+1 555 0100"},"changes":[{"field":"phone","original":"5550100","normalized":"+1 555 0100","reason":"format","confidence":0.8}],"warnings":[]}`
)

func init() {
	payload := func(canary string) schemaflux.NormalizeOptions {
		return schemaflux.NormalizeOptions{
			Options:  schemaflux.Options{Steering: "steer " + canary},
			Standard: "standard " + canary, Locale: "locale " + canary,
			Rules:             map[string]string{"rule key " + canary: "rule " + canary},
			CanonicalMappings: map[string]string{"from " + canary: "to " + canary},
			Fields:            []string{"name", "phone", "field " + canary}, SkipFields: []string{"skip " + canary},
		}
	}
	textForms = append(textForms, textForm{
		op: "normalize", level: schemaflux.ContractSchemaConstrained, body: normalizeAnswer, badBody: "not json",
		call: func(ctx context.Context, canary string) error {
			_, err := schemaflux.Normalize(ctx, textContact{Name: "ada " + canary, Phone: "5550100"}, payload(canary))
			return err
		},
		refused: func(ctx context.Context, canary string) error {
			o := payload(canary)
			o.Case = "x" + canary
			_, err := schemaflux.Normalize(ctx, textContact{Name: "ada " + canary, Phone: "5550100"}, o)
			return err
		},
	})
}

func TestNormalizeReturnsTheNormalizedValueWithTheModelsAccountAndWhatActuallyDiffers(t *testing.T) {
	t.Parallel()
	model := answeringWith(normalizeAnswer)
	got, err := schemaflux.Normalize(operationClient(model), normalizeInput)
	if err != nil {
		t.Fatal(err)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	want := schemaflux.NormalizeResult[textContact]{
		Normalized: textContact{Name: "Ada Lovelace", Phone: "+1 555 0100"},
		Changes: []schemaflux.NormalizeChange{
			{Field: "name", Original: "ada  lovelace", Normalized: "Ada Lovelace", Reason: "case and spacing", ModelConfidence: 0.9},
			{Field: "phone", Original: "5550100", Normalized: "+1 555 0100", Reason: "format", ModelConfidence: 0.8},
		},
		ChangedFields: []string{"name", "phone"}, TotalChanges: 2, Warnings: []string{"no country given"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	req := model.Last()
	wantText(t, req, "data normalization and standardization")
	wantText(t, req, "Normalize this data:")
	if req.Schema == nil || req.SchemaName != "normalize_response" || req.Format != "json" {
		t.Errorf("schema %v name %q format %q: a normalization of a struct is asked for as a declared schema", req.Schema != nil, req.SchemaName, req.Format)
	}
}

func TestNormalizeNamesTheChangedFieldsTheModelDidNotMentionAndCountsTheDifferences(t *testing.T) {
	t.Parallel()
	body := `{"normalized":{"name":"Ada Lovelace","phone":"+1 555 0100"},"changes":[{"field":"phone","original":"5550100","normalized":"+1 555 0100","reason":"format","confidence":0.8}],"warnings":[]}`
	got, err := schemaflux.Normalize(operationClient(answeringWith(body)), normalizeInput)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.ChangedFields, []string{"name", "phone"}) || !reflect.DeepEqual(got.Unreported, []string{"name"}) || got.TotalChanges != 2 || len(got.Changes) != 1 {
		t.Errorf("changed %v unreported %v total %d changes %d", got.ChangedFields, got.Unreported, got.TotalChanges, len(got.Changes))
	}
}

func TestNormalizeOfAValueThatDidNotChangeReportsNoChange(t *testing.T) {
	t.Parallel()
	body := `{"normalized":{"name":"ada  lovelace","phone":"5550100"},"changes":[],"warnings":[]}`
	got, err := schemaflux.Normalize(operationClient(answeringWith(body)), normalizeInput)
	if err != nil || got.TotalChanges != 0 || len(got.ChangedFields) != 0 || got.Normalized != normalizeInput {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestNormalizeOfATextReportsOneChangeNamedByADollarSign(t *testing.T) {
	t.Parallel()
	body := `{"normalized":"Hello world","changes":[{"field":"$","original":"  hELLO   wORLD ","normalized":"Hello world","reason":"case","confidence":0.9}],"warnings":[]}`
	got, err := schemaflux.Normalize(operationClient(answeringWith(body)), "  hELLO   wORLD ", schemaflux.NormalizeOptions{Case: "sentence"})
	if err != nil || got.Normalized != "Hello world" || !reflect.DeepEqual(got.ChangedFields, []string{"$"}) || got.TotalChanges != 1 || len(got.Unreported) != 0 {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestNormalizeWithNoOptionsFixesTyposAndSpacingAtTheFastTier(t *testing.T) {
	t.Parallel()
	model := answeringWith(normalizeAnswer)
	if _, err := schemaflux.Normalize(operationClient(model), normalizeInput); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	wantText(t, req, "Fix typos and spelling errors")
	wantText(t, req, "Normalize whitespace (remove extra spaces, trim)")
	for _, absent := range []string{"standard formatting", "Normalize case", "Canonical mappings", "Only normalize", "Skip these fields", "Locale", "Strict mode"} {
		wantNoText(t, req, absent)
	}
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" || req.WebSearch || req.Effort != "" {
		t.Errorf("temperature %v ceiling %d model %q web %v effort %q", req.Temperature, req.MaxTokens, req.Model, req.WebSearch, req.Effort)
	}
}

func TestNormalizeEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.NormalizeOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Standard", schemaflux.NormalizeOptions{Standard: "ISO_8601"}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, `Apply "ISO_8601" standard formatting`) }},
		{"Rules", schemaflux.NormalizeOptions{Rules: map[string]string{"phone": "E.164", "name": "title case"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `For "name": "title case"`)
			wantText(t, req, `For "phone": "E.164"`)
		}},
		{"CanonicalMappings", schemaflux.NormalizeOptions{CanonicalMappings: map[string]string{"USA": "United States"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Canonical mappings:")
			wantText(t, req, `"USA" -> "United States"`)
		}},
		{"Case", schemaflux.NormalizeOptions{Case: "title"}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Normalize case to title") }},
		{"Case lower", schemaflux.NormalizeOptions{Case: "lower"}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Normalize case to lower") }},
		{"NoFixTypos", schemaflux.NormalizeOptions{NoFixTypos: true}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Fix typos")
			wantText(t, req, "Normalize whitespace")
		}},
		{"NoNormalizeWhitespace", schemaflux.NormalizeOptions{NoNormalizeWhitespace: true}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Normalize whitespace")
			wantText(t, req, "Fix typos")
		}},
		{"Fields", schemaflux.NormalizeOptions{Fields: []string{"name", "phone"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Only normalize these fields: ["name","phone"]`)
		}},
		{"SkipFields", schemaflux.NormalizeOptions{SkipFields: []string{"id"}}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, `Skip these fields: ["id"]`) }},
		{"Locale", schemaflux.NormalizeOptions{Locale: "de-DE"}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, `Locale: "de-DE"`) }},
		{"Cautious", schemaflux.NormalizeOptions{Cautious: true}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Strict mode: if uncertain about normalization")
		}},
		{"Steering", schemaflux.NormalizeOptions{Options: schemaflux.Options{Steering: "keep accents"}}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "keep accents") }},
		{"Model", schemaflux.NormalizeOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"MaxOutputTokens", schemaflux.NormalizeOptions{Options: schemaflux.Options{MaxOutputTokens: 66}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 66 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.NormalizeOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"ModeCreative", schemaflux.NormalizeOptions{Options: schemaflux.Options{Mode: schemaflux.ModeCreative}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.7) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(normalizeAnswer)
			if _, err := schemaflux.Normalize(operationClient(model), normalizeInput, tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestNormalizeEnforcesFieldsAndSkipFieldsAgainstWhatActuallyChanged(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		opts    schemaflux.NormalizeOptions
		answer  string
		refused bool
	}{
		{"every changed field is allowed", schemaflux.NormalizeOptions{Fields: []string{"name", "phone"}}, normalizeAnswer, false},
		{"a changed field outside Fields", schemaflux.NormalizeOptions{Fields: []string{"name"}}, normalizePhoneOnly, true},
		{"Fields compares names without regard to case", schemaflux.NormalizeOptions{Fields: []string{"PHONE"}}, normalizePhoneOnly, false},
		{"a changed field named in SkipFields", schemaflux.NormalizeOptions{SkipFields: []string{"phone"}}, normalizePhoneOnly, true},
		{"SkipFields compares names without regard to case", schemaflux.NormalizeOptions{SkipFields: []string{"Phone"}}, normalizePhoneOnly, true},
		{"a skipped field that did not change", schemaflux.NormalizeOptions{SkipFields: []string{"name"}}, normalizePhoneOnly, false},
		{"neither list allows any change", schemaflux.NormalizeOptions{}, normalizeAnswer, false},
		{"a changed field both outside Fields and in SkipFields", schemaflux.NormalizeOptions{Fields: []string{"name"}, SkipFields: []string{"phone"}}, normalizePhoneOnly, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Normalize(operationClient(answeringWith(tc.answer)), normalizeInput, tc.opts)
			if !tc.refused {
				if err != nil {
					t.Fatalf("Normalize failed: %v", err)
				}
				return
			}
			wantError(t, err, schemaflux.ErrInvalidAnswer, "normalize")
			if !reflect.DeepEqual(got, schemaflux.NormalizeResult[textContact]{}) {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestNormalizeRefusesAnAnswerThatIsNotANormalization(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"text":                      `Ada Lovelace`,
		"a list":                    `["a"]`,
		"no normalized member":      `{"changes":[],"warnings":[]}`,
		"no changes member":         `{"normalized":{"name":"a","phone":"b"},"warnings":[]}`,
		"no warnings member":        `{"normalized":{"name":"a","phone":"b"},"changes":[]}`,
		"a normalized of the wrong": `{"normalized":[1],"changes":[],"warnings":[]}`,
		"changes of the wrong type": `{"normalized":{"name":"a","phone":"b"},"changes":"none","warnings":[]}`,
		"empty":                     ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Normalize(operationClient(answeringWith(body)), normalizeInput)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "normalize")
			if !reflect.DeepEqual(got, schemaflux.NormalizeResult[textContact]{}) {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestNormalizeRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	const caseMessage = "Case is not one of lower, upper, title and sentence"
	cases := map[string]struct {
		opts    schemaflux.NormalizeOptions
		message string
	}{
		"a case that is not one":        {schemaflux.NormalizeOptions{Case: "camel"}, caseMessage},
		"a case in another case":        {schemaflux.NormalizeOptions{Case: "Lower"}, caseMessage},
		"a negative output ceiling":     {schemaflux.NormalizeOptions{Options: schemaflux.Options{MaxOutputTokens: -1}}, "MaxOutputTokens must not be negative"},
		"a tier that is not one":        {schemaflux.NormalizeOptions{Options: schemaflux.Options{Tier: 5}}, "Tier is not one of TierSmart, TierFast and TierQuick"},
		"a mode that is not one":        {schemaflux.NormalizeOptions{Options: schemaflux.Options{Mode: 5}}, "Mode is not one of ModeStrict, ModeTransform and ModeCreative"},
		"a bad setting among good ones": {schemaflux.NormalizeOptions{Standard: "ISO_8601", Case: "x", Locale: "de"}, caseMessage},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(normalizeAnswer)
			got, err := schemaflux.Normalize(operationClient(model), normalizeInput, tc.opts)
			e := wantError(t, err, schemaflux.ErrConfiguration, "normalize")
			if e.Message != tc.message || !reflect.DeepEqual(got, schemaflux.NormalizeResult[textContact]{}) || model.Calls() != 0 {
				t.Errorf("message %q, value %+v, %d requests", e.Message, got, model.Calls())
			}
		})
	}
}

func TestNormalizeMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later value wins and a later zero leaves the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(normalizeAnswer)
		_, err := schemaflux.Normalize(operationClient(model), normalizeInput,
			schemaflux.NormalizeOptions{Case: "lower", Standard: "US_POSTAL", Locale: "en-US", Options: schemaflux.Options{Model: "first"}},
			schemaflux.NormalizeOptions{Case: "upper"},
			schemaflux.NormalizeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "Normalize case to upper")
		wantText(t, req, `"US_POSTAL"`)
		wantText(t, req, `Locale: "en-US"`)
		if req.Model != "first" {
			t.Errorf("model = %q", req.Model)
		}
	})
	t.Run("a bool turned on stays on", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(normalizeAnswer)
		_, err := schemaflux.Normalize(operationClient(model), normalizeInput, schemaflux.NormalizeOptions{NoFixTypos: true}, schemaflux.NormalizeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		wantNoText(t, model.Last(), "Fix typos")
	})
	t.Run("a later map replaces the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(normalizeAnswer)
		_, err := schemaflux.Normalize(operationClient(model), normalizeInput,
			schemaflux.NormalizeOptions{Rules: map[string]string{"first": "a"}}, schemaflux.NormalizeOptions{Rules: map[string]string{"second": "b"}})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), `"second"`)
		wantNoText(t, model.Last(), `"first"`)
	})
	t.Run("a bad setting in a later value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Normalize(operationClient(answeringWith(normalizeAnswer)), normalizeInput,
			schemaflux.NormalizeOptions{}, schemaflux.NormalizeOptions{Case: "x"})
		wantError(t, err, schemaflux.ErrConfiguration, "normalize")
	})
}

func TestNormalizeSendsTheSameRequestWhateverRecordsTheCall(t *testing.T) {
	t.Parallel()
	plain := answeringWith(normalizeAnswer)
	if _, err := schemaflux.Normalize(operationClient(plain), normalizeInput); err != nil {
		t.Fatal(err)
	}
	var recorder schemaflux.Recorder
	recorded := answeringWith(normalizeAnswer)
	if _, err := schemaflux.Normalize(operationClient(recorded, recordingTo(&recorder)), normalizeInput); err != nil {
		t.Fatal(err)
	}
	floored := namedModel(normalizeAnswer)
	if _, err := schemaflux.Normalize(operationClient(floored, underFloor(schemaflux.ContractSchemaConstrained)), normalizeInput); err != nil {
		t.Fatal(err)
	}
	for name, other := range map[string]schemaflux.Request{"a recorder": recorded.Last(), "a floor": floored.Last()} {
		if requestText(plain.Last()) != requestText(other) || plain.Last().SchemaName != other.SchemaName || plain.Last().Format != other.Format {
			t.Errorf("%s changed what the model is asked", name)
		}
	}
}

func TestNormalizeResultMarshalsWithTheTagsOfTheInternalResult(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(schemaflux.NormalizeResult[string]{
		Normalized: "n", Changes: []schemaflux.NormalizeChange{{Field: "$", Original: "o", Normalized: "n", Reason: "r", ModelConfidence: 0.5}},
		ChangedFields: []string{"$"}, Unreported: []string{"u"}, TotalChanges: 1, Warnings: []string{"w"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	_ = json.Unmarshal(got, &a)
	_ = json.Unmarshal([]byte(`{"normalized":"n","changes":[{"field":"$","original":"o","normalized":"n","reason":"r","confidence":0.5}],"changed_fields":["$"],"unreported":["u"],"total_changes":1,"warnings":["w"]}`), &b)
	if !reflect.DeepEqual(a, b) || strings.Contains(string(got), "metadata") {
		t.Errorf("got %s", got)
	}
	empty, _ := json.Marshal(schemaflux.NormalizeResult[string]{})
	for _, omitted := range []string{"changed_fields", "unreported", "warnings"} {
		if strings.Contains(string(empty), omitted) {
			t.Errorf("an empty %s is written: %s", omitted, empty)
		}
	}
	bare, _ := json.Marshal(schemaflux.NormalizeChange{Field: "f"})
	if strings.Contains(string(bare), "reason") || strings.Contains(string(bare), "confidence") {
		t.Errorf("an empty reason or confidence is written: %s", bare)
	}
}

func TestNormalizeOfAMapIsDeliveredAsWellFormedAndHeldToThatLevelByAFloor(t *testing.T) {
	t.Parallel()
	wantLevelFollowsTheType(t, levelCall{
		op:       "normalize",
		mapReply: `{"normalized":{"name":"Ada"},"changes":[],"warnings":[]}`, structReply: normalizeAnswer,
		intoMap: func(ctx context.Context) error {
			_, err := schemaflux.Normalize(ctx, map[string]string{"name": "ada"})
			return err
		},
		intoStruct: func(ctx context.Context) error {
			_, err := schemaflux.Normalize(ctx, normalizeInput)
			return err
		},
	})
	// The answer holds the value inside an envelope, one level deeper than the
	// value, so a chain of six structs, which has a schema alone, has none here.
	type six struct {
		A struct {
			B struct {
				C struct {
					D struct {
						E struct {
							F string `json:"f"`
						} `json:"e"`
					} `json:"d"`
				} `json:"c"`
			} `json:"b"`
		} `json:"a"`
	}
	t.Run("a value nested one level short of the limit is refused under a schema-constrained floor", func(t *testing.T) {
		model := namedModel(`{"normalized":{},"changes":[],"warnings":[]}`)
		_, err := schemaflux.Normalize(operationClient(model, underFloor(schemaflux.ContractSchemaConstrained)), six{})
		wantError(t, err, schemaflux.ErrPolicy, "normalize")
		if model.Calls() != 0 {
			t.Errorf("%d requests were sent", model.Calls())
		}
		// The same value has a schema for Compress, which puts no envelope above it.
		if _, err := schemaflux.Compress(operationClient(namedModel(`{"compressed":{},"preserved_info":[],"removed_info":[]}`), underFloor(schemaflux.ContractSchemaConstrained)), six{}); errors.Is(err, schemaflux.ErrPolicy) {
			t.Errorf("Compress was refused under the floor it meets: %v", err)
		}
	})
}
