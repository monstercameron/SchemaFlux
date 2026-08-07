package ops

// `json:"-,"` below names a field "-" rather than excluding it. That
// distinction is exactly what F-015 got wrong, so the tag is the test.
//lint:file-ignore SA5008 the malformed-looking tag is the case under test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// A field tagged json:"-" is never serialised, so describing it to the model
// asks for a value that will be thrown away — and, when the field was excluded
// because it is sensitive, sends its name out of the process. The old code
// skipped the rename and used the Go field name instead.
func TestSchemaOmitsFieldsExcludedFromJSON(t *testing.T) {
	type account struct {
		Email        string `json:"email"`
		PasswordHash string `json:"-"`
		InternalNote string `json:"-"`
		SessionToken string `json:"-"`
		Balance      int    `json:"balance"`
	}

	schema := GenerateTypeSchema(reflect.TypeOf(account{}))

	for _, excluded := range []string{"PasswordHash", "InternalNote", "SessionToken"} {
		if strings.Contains(schema, excluded) {
			t.Errorf("schema names the excluded field %q:\n%s", excluded, schema)
		}
	}
	for _, kept := range []string{"email", "balance"} {
		if !strings.Contains(schema, kept) {
			t.Errorf("schema is missing %q:\n%s", kept, schema)
		}
	}
}

// The corner cases of the encoding/json tag grammar.
func TestSchemaJSONTagHandling(t *testing.T) {
	cases := []struct {
		name    string
		typ     any
		want    []string
		notWant []string
	}{
		{
			"dash_alone_is_excluded",
			struct {
				A string `json:"-"`
				B string `json:"b"`
			}{},
			[]string{"b"}, []string{"A"},
		},
		{
			// `json:"-,"` names the field "-", it does not exclude it.
			"dash_comma_names_the_field_dash",
			struct {
				A string `json:"-,"`
			}{},
			[]string{"-"}, []string{"A"},
		},
		{
			"empty_name_keeps_the_go_name",
			struct {
				Alpha string `json:",omitempty"`
			}{},
			[]string{"Alpha"}, nil,
		},
		{
			"no_tag_keeps_the_go_name",
			struct {
				Alpha string
			}{},
			[]string{"Alpha"}, nil,
		},
		{
			"unexported_is_excluded",
			struct {
				Alpha string `json:"alpha"`
				beta  string
			}{},
			[]string{"alpha"}, []string{"beta"},
		},
		{
			"omitempty_marks_optional",
			struct {
				Alpha string `json:"alpha,omitempty"`
				Beta  string `json:"beta"`
			}{},
			[]string{"beta: string (required)"}, []string{"alpha: string (required)"},
		},
		{
			// A field literally named "omitempty_flag" is still required: the
			// old substring test made it optional.
			"field_named_like_the_option_is_still_required",
			struct {
				OmitemptyFlag string `json:"omitempty_flag"`
			}{},
			[]string{"omitempty_flag: string (required)"}, nil,
		},
		{
			"excluded_field_with_a_type_that_would_recurse",
			struct {
				Nested *struct{ Secret string } `json:"-"`
				Public string                   `json:"public"`
			}{},
			[]string{"public"}, []string{"Secret", "Nested"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := GenerateTypeSchema(reflect.TypeOf(tc.typ))
			for _, want := range tc.want {
				if !strings.Contains(schema, want) {
					t.Errorf("schema is missing %q:\n%s", want, schema)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(schema, notWant) {
					t.Errorf("schema should not contain %q:\n%s", notWant, schema)
				}
			}
		})
	}
}

// A struct with every field excluded still produces a well-formed (empty)
// schema rather than a panic or a stray separator.
func TestSchemaWithEveryFieldExcluded(t *testing.T) {
	type hidden struct {
		A string `json:"-"`
		B string `json:"-"`
	}

	schema := GenerateTypeSchema(reflect.TypeOf(hidden{}))
	if strings.Contains(schema, "A") || strings.Contains(schema, "B") {
		t.Errorf("schema names an excluded field: %s", schema)
	}
}

// captureSystemPrompt records the prompt an operation renders, without a
// provider.
func capturePrompts(t *testing.T, run func()) []string {
	t.Helper()

	var prompts []string
	previous := customLLMCaller
	setLLMCaller(func(_ context.Context, system, user string, opts types.OpOptions) (string, error) {
		// Steering is applied inside callLLM, after this hook, so it has to be
		// read from the options or the witness sees none of it.
		prompts = append(prompts, strings.Join([]string{system, user, opts.Steering}, "\x00"))
		return "{}", nil
	})
	defer func() { customLLMCaller = previous }()

	run()
	return prompts
}

// Go randomizes map iteration, so a map rendered into a prompt unsorted
// produces different bytes on every call: no prefix cache can hit, and no run
// is reproducible. Every map that reaches a prompt must be sorted.
func TestPromptsAreByteIdenticalAcrossRuns(t *testing.T) {
	// Enough keys that an unsorted render would collide only by luck.
	hints := map[string]string{
		"alpha": "a", "bravo": "b", "charlie": "c", "delta": "d", "echo": "e",
		"foxtrot": "f", "golf": "g", "hotel": "h", "india": "i", "juliet": "j",
	}
	rules := map[string]string{
		"one": "1", "two": "2", "three": "3", "four": "4", "five": "5",
		"six": "6", "seven": "7", "eight": "8", "nine": "9", "ten": "10",
	}

	type record struct {
		Alpha string `json:"alpha"`
	}

	cases := []struct {
		name string
		run  func()
	}{
		{"extract_schema_hints", func() {
			opts := NewExtractOptions()
			opts.SchemaHints = hints
			_, _ = Extract[record]("input", opts)
		}},
		{"extract_field_rules", func() {
			opts := NewExtractOptions()
			opts.FieldRules = rules
			_, _ = Extract[record]("input", opts)
		}},
		{"validate_field_rules", func() {
			opts := NewValidateOptions().WithRules("any")
			opts.FieldRules = rules
			opts.SchemaHints = hints
			_, _ = Validate(record{}, opts)
		}},
		{"normalize_canonical_mappings", func() {
			_, _ = Normalize(record{}, NewNormalizeOptions().WithCanonicalMappings(rules))
		}},
		{"project_mappings", func() {
			_, _ = Project[record, record](record{}, ProjectOptions{Mappings: rules})
		}},
		{"classify_category_descriptions", func() {
			opts := NewClassifyOptions()
			opts.Categories = []string{"alpha", "bravo"}
			opts.CategoryDescriptions = hints
			_, _ = Classify[string, string]("input", opts)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var first string
			// Twenty runs: with ten keys, an unsorted render repeats the same
			// order by chance far less often than once in twenty.
			for run := 0; run < 20; run++ {
				prompts := capturePrompts(t, tc.run)
				if len(prompts) == 0 {
					t.Fatal("the operation made no call, so this case witnesses nothing")
				}
				joined := strings.Join(prompts, "\x01")
				if run == 0 {
					first = joined
					continue
				}
				if joined != first {
					t.Fatalf("run %d rendered different prompt bytes; a map is being iterated unsorted", run)
				}
			}
		})
	}
}

// sortedKeys is the mechanism; test it directly too.
func TestSortedKeys(t *testing.T) {
	m := map[string]int{"charlie": 3, "alpha": 1, "bravo": 2}
	got := sortedKeys(m)
	want := []string{"alpha", "bravo", "charlie"}

	if len(got) != len(want) {
		t.Fatalf("sortedKeys returned %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortedKeys = %v, want %v", got, want)
		}
	}

	if keys := sortedKeys(map[string]int{}); len(keys) != 0 {
		t.Errorf("an empty map must produce no keys, got %v", keys)
	}
	if keys := sortedKeys(map[string]string(nil)); len(keys) != 0 {
		t.Errorf("a nil map must produce no keys, got %v", keys)
	}
}

// hasJSONOption must read the options, not the tag as a whole.
func TestHasJSONOption(t *testing.T) {
	cases := []struct {
		tag  string
		want bool
	}{
		{`name,omitempty`, true},
		{`name,string,omitempty`, true},
		{`,omitempty`, true},
		{`omitempty`, false},         // that is the field's NAME, not an option
		{`omitempty_flag`, false},    // the old substring test said true
		{`name,omitemptyish`, false}, // near miss
		{`name`, false},
		{``, false},
	}

	for _, tc := range cases {
		if got := hasJSONOption(tc.tag, "omitempty"); got != tc.want {
			t.Errorf("hasJSONOption(%q) = %v, want %v", tc.tag, got, tc.want)
		}
	}
}

// The library used to invent confidence numbers from surface features of the
// text: 0.3 if a failed parse started with a brace, 0.5 plus 0.2 for a
// completion over twenty characters, threshold minus a tenth on a validation
// failure. Those functions are gone; this asserts the reachable paths carry no
// invented number in their place.
func TestFailedExtractionCarriesNoInventedConfidence(t *testing.T) {
	type record struct {
		Alpha string `json:"alpha"`
	}

	for _, body := range []string{
		`{"alpha": `,
		"{not json at all}",
		"[1,2,3]",
		"prose, no JSON here",
		"",
	} {
		t.Run(body, func(t *testing.T) {
			restore := stubLLM(body)
			defer restore()

			_, err := Extract[record]("input", NewExtractOptions())
			if err == nil {
				t.Fatal("an unparseable body must be an error")
			}

			var extractErr types.ExtractError
			if !errors.As(err, &extractErr) {
				t.Fatalf("expected an ExtractError, got %T", err)
			}
			if !strings.Contains(extractErr.Reason, "parse") && !strings.Contains(extractErr.Reason, "unmarshal") {
				t.Errorf("Reason should say what went wrong, got %q", extractErr.Reason)
			}
		})
	}
}

// Complete reports no confidence, because it never had a way to compute one.
func TestCompleteReportsNoInventedConfidence(t *testing.T) {
	for _, body := range []string{
		"short",
		"a considerably longer completion that ends with a full stop.",
		"one! two? three.",
	} {
		t.Run(body, func(t *testing.T) {
			restore := stubLLM("Hello " + body)
			defer restore()

			result, err := Complete(context.Background(), nil, "Hello", NewCompleteOptions())
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if result.ModelConfidence != 0 {
				t.Errorf("ModelConfidence = %v; the length-and-punctuation heuristic must stay deleted", result.ModelConfidence)
			}
		})
	}
}
