package ops

import (
	"sort"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

func TestEnrichOptions(t *testing.T) {
	t.Run("NewEnrichOptions creates valid defaults", func(t *testing.T) {
		opts := NewEnrichOptions()
		if err := opts.Validate(); err != nil {
			t.Errorf("default options should be valid: %v", err)
		}
	})

	t.Run("WithDeriveFields sets fields to derive", func(t *testing.T) {
		opts := NewEnrichOptions().WithDeriveFields([]string{"summary", "keywords"})
		if len(opts.DeriveFields) != 2 {
			t.Errorf("expected 2 fields, got %d", len(opts.DeriveFields))
		}
	})

	t.Run("WithDerivationRules sets rules", func(t *testing.T) {
		rules := map[string]string{
			"summary": "A brief summary of the content",
		}
		opts := NewEnrichOptions().WithDerivationRules(rules)
		if len(opts.DerivationRules) != 1 {
			t.Errorf("expected 1 rule, got %d", len(opts.DerivationRules))
		}
	})

	t.Run("WithDomain sets domain", func(t *testing.T) {
		opts := NewEnrichOptions().WithDomain("finance")
		if opts.Domain != "finance" {
			t.Errorf("expected finance, got %s", opts.Domain)
		}
	})

	t.Run("WithIncludeConfidence enables confidence", func(t *testing.T) {
		opts := NewEnrichOptions().WithIncludeConfidence(true)
		if !opts.IncludeConfidence {
			t.Error("expected IncludeConfidence to be true")
		}
	})
}

type enrichArticle struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

type enrichedArticle struct {
	Title    string   `json:"title"`
	Content  string   `json:"content"`
	Summary  string   `json:"summary"`
	Keywords []string `json:"keywords"`
}

var enrichSourceArticle = enrichArticle{Title: "Go", Content: "Go is a statically typed language."}

func TestEnrichAddsDerivedFieldsAndComputesWhatWasAdded(t *testing.T) {
	coreAnswer(t, `{"enriched":{"title":"Go","content":"Go is a statically typed language.","summary":"typed","keywords":["go"]},
		"added_fields":["summary"],"confidence":{"summary":0.9},"derivations":{"summary":"read it"}}`)

	result, err := Enrich[enrichArticle, enrichedArticle](enrichSourceArticle,
		NewEnrichOptions().WithDeriveFields([]string{"summary", "keywords"}))
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if result.Enriched.Summary != "typed" {
		t.Errorf("Enriched = %+v", result.Enriched)
	}
	// Computed from the two field sets, not taken from the model's claim.
	if strings.Join(result.AddedFields, ",") != "keywords,summary" {
		t.Errorf("AddedFields = %v", result.AddedFields)
	}
	if strings.Join(result.ModelClaimedAddedFields, ",") != "summary" {
		t.Errorf("ModelClaimedAddedFields = %v", result.ModelClaimedAddedFields)
	}
}

// enrichCompleteAnswer is an answer that carries everything the prompt asks for
// when confidence scores are not requested.
const enrichCompleteAnswer = `{"enriched":{"title":"Go","content":"Go is a statically typed language.","summary":"typed","keywords":["go"]},"added_fields":["summary"],"derivations":{"summary":"read it"}}`

func TestEnrichRefusesAnAnswerMissingARequiredMember(t *testing.T) {
	members := [][]any{{"enriched"}, {"added_fields"}, {"derivations"}}
	refuseEachMissingMember(t, enrichCompleteAnswer, members, func(response string) (any, error) {
		coreAnswer(t, response)
		result, err := Enrich[enrichArticle, enrichedArticle](enrichSourceArticle, NewEnrichOptions())
		return result, err
	})
}

// The confidence map is asked for only under IncludeConfidence, so an answer
// may leave it out otherwise. When the option asks for scores, an added field
// that holds a value without one is refused, and that is what an answer
// without the map comes to.
func TestEnrichRequiresTheConfidenceMapOnlyWhenScoresWereAsked(t *testing.T) {
	coreAnswer(t, enrichCompleteAnswer)
	if result, err := Enrich[enrichArticle, enrichedArticle](enrichSourceArticle, NewEnrichOptions()); err != nil || result.ModelConfidence == nil {
		t.Errorf("no scores asked, none given: %+v, %v", result, err)
	}
	result, err := Enrich[enrichArticle, enrichedArticle](enrichSourceArticle, NewEnrichOptions().WithIncludeConfidence(true))
	wantKind(t, err, types.KindSchemaViolation)
	if result.Enriched.Title != "" || result.Metadata != nil {
		t.Errorf("result = %+v beside an error", result)
	}

	// A null where scores are optional is the model leaving them out.
	coreAnswer(t, answerWithMembers(t, enrichCompleteAnswer, map[string]string{"confidence": "null"}))
	if _, err := Enrich[enrichArticle, enrichedArticle](enrichSourceArticle, NewEnrichOptions()); err != nil {
		t.Errorf("a null confidence map, scores not asked for: %v", err)
	}
}

func TestEnrichRefusesAnAnswerWithoutTheEnrichedValue(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"only added_fields", `{"added_fields":[]}`},
		{"enriched is null", `{"enriched":null,"added_fields":["summary"],"derivations":{}}`},
		{"only a confidence map", `{"confidence":{"summary":0.9}}`},
		{"an unrelated object", `{"other":1}`},
		{"an empty object", `{}`},
		{"not JSON", `sorry`},
		{"enriched of the wrong type", `{"enriched":"text","added_fields":[],"derivations":{}}`},
		{"enriched is a list", `{"enriched":[1],"added_fields":[],"derivations":{}}`},
		{"truncated", `{"enriched":{"title":"Go"`},
		{"an empty answer", ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			coreAnswer(t, c.body)
			result, err := Enrich[enrichArticle, enrichedArticle](enrichSourceArticle, NewEnrichOptions())
			if err == nil {
				t.Fatalf("accepted: %+v", result)
			}
			if result.Enriched.Title != "" || result.Metadata != nil || result.ModelConfidence != nil {
				t.Errorf("result = %+v beside an error", result)
			}
		})
	}
}

// The enriched value is the answer. Left out, or null, it is refused for being
// absent, whatever else the response reports. With AddOnly on, which is the
// default, the zero value it would decode to changes every filled field and is
// refused for that instead, which hides whether the value itself was checked.
func TestEnrichRefusesAMissingOrNullEnrichedValueWhenNothingElseWouldRefuseIt(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"enriched is null", `{"enriched":null,"added_fields":["summary"],"derivations":{}}`},
		{"enriched is null beside a confidence map", `{"enriched":null,"confidence":{"summary":0.9},"added_fields":[],"derivations":{}}`},
		{"enriched is missing", `{"added_fields":["summary"],"derivations":{}}`},
		{"enriched is missing beside a confidence map", `{"added_fields":[],"confidence":{"summary":0.9},"derivations":{}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			coreAnswer(t, c.body)
			result, err := Enrich[enrichArticle, enrichedArticle](enrichSourceArticle, NewEnrichOptions().WithAddOnly(false))
			if err == nil {
				t.Fatalf("accepted: %+v", result)
			}
			if got := types.KindOf(err); got != types.KindSchemaViolation {
				t.Errorf("kind = %v, want %v (%v)", got, types.KindSchemaViolation, err)
			}
			if result.Enriched.Title != "" || result.Metadata != nil || result.ModelConfidence != nil {
				t.Errorf("result = %+v beside an error", result)
			}
		})
	}
}

func TestEnrichInPlaceAsksForTheFilledFieldsToBeLeftAloneOnlyUnderAddOnly(t *testing.T) {
	const instruction = "fill only the fields that are empty"
	doc := enrichedArticle{Title: "Go", Content: "text"}
	for _, addOnly := range []bool{true, false} {
		model := coreAnswer(t, `{"title":"Go","content":"text","summary":"s"}`)
		if _, err := EnrichInPlace(doc, NewEnrichOptions().WithAddOnly(addOnly)); err != nil {
			t.Fatalf("AddOnly %v: %v", addOnly, err)
		}
		if got := strings.Contains(model.last().system, instruction); got != addOnly {
			t.Errorf("AddOnly %v: the prompt asks to leave filled fields alone = %v:\n%s", addOnly, got, model.last().system)
		}
	}
}

func TestEnrichChecksAddOnlyExcludeFieldsAndTheScores(t *testing.T) {
	changedTitle := `{"enriched":{"title":"Changed","content":"Go is a statically typed language.","summary":"s"},"added_fields":[],"derivations":{}}`
	changedContent := `{"enriched":{"title":"Go","content":"changed","summary":"s"},"added_fields":[],"derivations":{}}`
	untouched := `{"enriched":{"title":"Go","content":"Go is a statically typed language.","summary":"s"},"confidence":{"summary":0.9},"added_fields":[],"derivations":{}}`

	cases := []struct {
		name      string
		answer    string
		opts      EnrichOptions
		wantError bool
	}{
		{"AddOnly, an existing field changed", changedTitle, NewEnrichOptions(), true},
		{"AddOnly off, an existing field changed", changedTitle, NewEnrichOptions().WithAddOnly(false), false},
		{"AddOnly, nothing changed", untouched, NewEnrichOptions(), false},
		{"ExcludeFields, the excluded field changed", changedTitle, NewEnrichOptions().WithAddOnly(false).WithExcludeFields([]string{"title"}), true},
		{"ExcludeFields, matched without regard to case", changedTitle, NewEnrichOptions().WithAddOnly(false).WithExcludeFields([]string{"TITLE"}), true},
		{"ExcludeFields, another field changed", changedContent, NewEnrichOptions().WithAddOnly(false).WithExcludeFields([]string{"title"}), false},
		{"ExcludeFields naming a field the answer lacks", untouched, NewEnrichOptions().WithExcludeFields([]string{"nothing"}), false},
		{"a confidence above one", `{"enriched":{"title":"Go","content":"c","summary":"s"},"confidence":{"summary":7},"added_fields":[],"derivations":{}}`, NewEnrichOptions().WithAddOnly(false), true},
		{"a negative confidence", `{"enriched":{"title":"Go","content":"c","summary":"s"},"confidence":{"summary":-1},"added_fields":[],"derivations":{}}`, NewEnrichOptions().WithAddOnly(false), true},
		{"IncludeConfidence, every added field scored", untouched, NewEnrichOptions().WithIncludeConfidence(true), false},
		{"IncludeConfidence, an added field not scored", `{"enriched":{"title":"Go","content":"Go is a statically typed language.","summary":"s"},"confidence":{},"added_fields":[],"derivations":{}}`, NewEnrichOptions().WithIncludeConfidence(true), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			coreAnswer(t, c.answer)
			result, err := Enrich[enrichArticle, enrichedArticle](enrichSourceArticle, c.opts)
			if c.wantError {
				if err == nil {
					t.Fatalf("accepted: %+v", result)
				}
				if result.Enriched.Title != "" {
					t.Errorf("result = %+v beside an error", result)
				}
				return
			}
			if err != nil {
				t.Fatalf("Enrich: %v", err)
			}
		})
	}
}

func TestEnrichInPlaceFillsEmptyFieldsAndRefusesChangesToFilledOnes(t *testing.T) {
	doc := enrichedArticle{Title: "Go", Content: "text"}

	cases := []struct {
		name      string
		answer    string
		opts      EnrichOptions
		wantError bool
	}{
		{"an empty field is filled", `{"title":"Go","content":"text","summary":"s","keywords":["go"]}`, NewEnrichOptions(), false},
		{"a filled field changes", `{"title":"Other","content":"text","summary":"s"}`, NewEnrichOptions(), true},
		{"a filled field changes with AddOnly off", `{"title":"Other","content":"text","summary":"s"}`, NewEnrichOptions().WithAddOnly(false), false},
		{"one recognised key zeroes every other field", `{"keywords":["x"]}`, NewEnrichOptions(), true},
		{"one recognised key with AddOnly off", `{"keywords":["x"]}`, NewEnrichOptions().WithAddOnly(false), false},
		{"an excluded empty field is filled", `{"title":"Go","content":"text","summary":"s"}`, NewEnrichOptions().WithExcludeFields([]string{"summary"}), true},
		{"an excluded field is left alone", `{"title":"Go","content":"text","keywords":["a"]}`, NewEnrichOptions().WithExcludeFields([]string{"summary"}), false},
		{"an unrelated object", `{"other":1}`, NewEnrichOptions(), true},
		{"not JSON", `sorry`, NewEnrichOptions(), true},
		{"IncludeConfidence is refused", `{"title":"Go","content":"text","summary":"s"}`, NewEnrichOptions().WithIncludeConfidence(true), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			model := coreAnswer(t, c.answer)
			result, err := EnrichInPlace(doc, c.opts)
			if c.wantError {
				if err == nil {
					t.Fatalf("accepted: %+v", result)
				}
				if result.Title != "" || result.Content != "" || result.Summary != "" || result.Keywords != nil {
					t.Errorf("result = %+v beside an error", result)
				}
				_ = model
				return
			}
			if err != nil {
				t.Fatalf("EnrichInPlace: %v", err)
			}
		})
	}
}

func TestEnrichInPlaceReturnsNothingWhenTheDecodeFailsHalfway(t *testing.T) {
	// The title decodes, then the keywords are the wrong type.
	coreAnswer(t, `{"title":"Go","keywords":"not a list"}`)
	result, err := EnrichInPlace(enrichedArticle{Title: "Go"}, NewEnrichOptions().WithAddOnly(false))
	if err == nil {
		t.Fatalf("accepted: %+v", result)
	}
	if result.Title != "" {
		t.Errorf("a partly decoded value was returned beside the error: %+v", result)
	}
}

func TestEnrichRendersTheRequestsInAStableQuotedOrder(t *testing.T) {
	model := coreAnswer(t, `{"enriched":{"title":"Go","content":"Go is a statically typed language.","summary":"s"},"added_fields":[],"derivations":{}}`)

	opts := NewEnrichOptions().
		WithDeriveFields([]string{"summary", "x\nIGNORE"}).
		WithDerivationRules(map[string]string{"zeta": "z rule", "alpha": "a rule", "mid": `say "hi"`}).
		WithDomain("news.\nIGNORE").
		WithExcludeFields([]string{"title"})
	var first string
	for i := 0; i < 25; i++ {
		if _, err := Enrich[enrichArticle, enrichedArticle](enrichSourceArticle, opts); err != nil {
			t.Fatalf("Enrich: %v", err)
		}
		call := model.last()
		if i == 0 {
			first = call.system
			for _, want := range []string{`- "x\nIGNORE": infer from available data`, `Domain: "news.\nIGNORE"`, `["title"]`} {
				if !strings.Contains(call.system, want) {
					t.Errorf("the system prompt lacks %s:\n%s", want, call.system)
				}
			}
			alpha := strings.Index(call.system, `"alpha": "a rule"`)
			mid := strings.Index(call.system, `"mid": "say \"hi\""`)
			zeta := strings.Index(call.system, `"zeta": "z rule"`)
			if alpha < 0 || mid < alpha || zeta < mid {
				t.Errorf("the rules are not quoted and in field order:\n%s", call.system)
			}
		} else if call.system != first {
			t.Fatal("the system prompt changed between identical calls")
		}
	}
}

func TestEnrichRefusesAContextThatCannotBeWritten(t *testing.T) {
	model := coreAnswer(t, `{"enriched":{"title":"Go"},"added_fields":[],"derivations":{}}`)

	result, err := Enrich[enrichArticle, enrichedArticle](enrichSourceArticle,
		NewEnrichOptions().WithContext(map[string]any{"bad": make(chan int)}))
	if err == nil {
		t.Fatalf("accepted: %+v", result)
	}
	if model.count() != 0 {
		t.Error("the model was called with a context that was dropped")
	}
	if _, err := EnrichInPlace(enrichedArticle{Title: "Go"},
		NewEnrichOptions().WithContext(map[string]any{"bad": make(chan int)})); err == nil {
		t.Error("EnrichInPlace accepted a context that cannot be written")
	}
}

func TestJSONValueIsEmptyNamesWhatHoldsNothing(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  bool
	}{
		{"null", nil, true},
		{"an empty string", "", true},
		{"a space", " ", false},
		{"text", "x", false},
		{"zero", float64(0), true},
		{"a number", 3.5, false},
		{"a negative number", float64(-1), false},
		{"false", false, true},
		{"true", true, false},
		{"an empty list", []any{}, true},
		{"a list", []any{1.0}, false},
		{"a list of empties", []any{nil}, false},
		{"an empty object", map[string]any{}, true},
		{"an object", map[string]any{"a": nil}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := jsonValueIsEmpty(c.value); got != c.want {
				t.Errorf("jsonValueIsEmpty(%#v) = %v, want %v", c.value, got, c.want)
			}
		})
	}
}

func TestChangedFilledFieldAndChangedNamedField(t *testing.T) {
	type record struct {
		A string   `json:"a"`
		B int      `json:"b"`
		C []string `json:"c"`
	}
	cases := []struct {
		name       string
		before     record
		after      record
		allowFill  bool
		wantField  string
		wantChange bool
	}{
		{"nothing changes", record{"x", 1, nil}, record{"x", 1, nil}, true, "", false},
		{"a filled field changes", record{"x", 1, nil}, record{"y", 1, nil}, true, "a", true},
		{"an empty string is filled", record{"", 1, nil}, record{"y", 1, nil}, true, "", false},
		{"an empty string is filled but fills are not allowed", record{"", 1, nil}, record{"y", 1, nil}, false, "a", true},
		{"a zero number is filled", record{"x", 0, nil}, record{"x", 5, nil}, true, "", false},
		{"a nil list is filled", record{"x", 1, nil}, record{"x", 1, []string{"k"}}, true, "", false},
		{"a filled list is emptied", record{"x", 1, []string{"k"}}, record{"x", 1, nil}, true, "c", true},
		{"a filled number is zeroed", record{"x", 1, nil}, record{"x", 0, nil}, true, "b", true},
		{"the first change in key order is named", record{"x", 1, []string{"k"}}, record{"y", 2, nil}, true, "a", true},
		{"everything is zeroed", record{"x", 1, []string{"k"}}, record{}, true, "a", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			field, changed := changedFilledField(c.before, c.after, nil, c.allowFill)
			if field != c.wantField || changed != c.wantChange {
				t.Errorf("changedFilledField = %q, %v; want %q, %v", field, changed, c.wantField, c.wantChange)
			}
		})
	}

	before, after := record{"x", 1, nil}, record{"y", 2, nil}
	if field, changed := changedNamedField(before, after, []string{"B"}); !changed || field != "b" {
		t.Errorf("changedNamedField(B) = %q, %v", field, changed)
	}
	if _, changed := changedNamedField(before, after, []string{"c"}); changed {
		t.Error("an unchanged named field was reported")
	}
	if _, changed := changedNamedField(before, after, nil); changed {
		t.Error("no names must report nothing")
	}
	if _, changed := changedNamedField("not an object", after, []string{"a"}); changed {
		t.Error("a non-object input has no fields to protect")
	}
}

// A list is enriched as a list, and what was added is read from the fields its
// elements carry. A value that is a slice has no field names of its own, so the
// fields were read as none and nothing was ever reported as added.
func TestEnrichReportsTheFieldsAddedToEveryElementOfAList(t *testing.T) {
	// The fields the answer adds are optional on the type, so that an element
	// that was not given one does not carry it once it is written.
	type enrichedItem struct {
		Title    string   `json:"title"`
		Content  string   `json:"content"`
		Summary  string   `json:"summary,omitempty"`
		Keywords []string `json:"keywords,omitempty"`
	}
	two := []enrichArticle{
		{Title: "Go", Content: "Go is typed."},
		{Title: "Rust", Content: "Rust is typed too."},
	}
	cases := []struct {
		name  string
		input any
		body  string
		want  string
	}{
		{"every element gains two fields",
			two,
			`{"enriched":[{"title":"Go","content":"Go is typed.","summary":"s1","keywords":["a"]},{"title":"Rust","content":"Rust is typed too.","summary":"s2","keywords":["b"]}],"added_fields":["summary"],"derivations":{}}`,
			"keywords,summary"},
		{"only the first element gains a field",
			two,
			`{"enriched":[{"title":"Go","content":"Go is typed.","summary":"s1"},{"title":"Rust","content":"Rust is typed too."}],"added_fields":[],"derivations":{}}`,
			"summary"},
		{"an element without a field the others have",
			two,
			`{"enriched":[{"title":"Go","content":"Go is typed.","keywords":["a"]},{"title":"Rust","content":"Rust is typed too.","summary":"s2"}],"added_fields":[],"derivations":{}}`,
			"keywords,summary"},
		{"nothing added",
			two,
			`{"enriched":[{"title":"Go","content":"Go is typed."},{"title":"Rust","content":"Rust is typed too."}],"added_fields":[],"derivations":{}}`,
			""},
		{"a single-element list",
			two[:1],
			`{"enriched":[{"title":"Go","content":"Go is typed.","summary":"s1"}],"added_fields":["summary"],"derivations":{}}`,
			"summary"},
		{"field names are compared without regard to case",
			two,
			`{"enriched":[{"Title":"Go","Content":"Go is typed.","Summary":"s1"},{"title":"Rust","content":"Rust is typed too."}],"added_fields":[],"derivations":{}}`,
			"summary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			coreAnswer(t, tc.body)
			var added []string
			switch input := tc.input.(type) {
			case []enrichArticle:
				result, err := Enrich[[]enrichArticle, []enrichedItem](input, NewEnrichOptions().WithDeriveFields([]string{"summary", "keywords"}))
				if err != nil {
					t.Fatalf("Enrich: %v", err)
				}
				added = result.AddedFields
			default:
				t.Fatalf("unexpected input type %T", tc.input)
			}
			if got := strings.Join(added, ","); got != tc.want {
				t.Errorf("AddedFields = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFieldNamesOfAValueReadsListsAndObjects(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"a struct", enrichArticle{}, "content,title"},
		{"a struct with an omitted field", struct {
			A string `json:"a"`
			B string `json:"b,omitempty"`
		}{A: "x"}, "a"},
		{"a list of structs", []enrichArticle{{}, {}}, "content,title"},
		{"a list of structs of one type, one element", []enrichedArticle{{}}, "content,keywords,summary,title"},
		{"a list of maps with different keys", []map[string]any{{"a": 1}, {"b": 2}}, "a,b"},
		{"a map", map[string]any{"Key": 1}, "key"},
		{"an empty list", []enrichArticle{}, ""},
		{"a nil list", []enrichArticle(nil), ""},
		{"a list of numbers", []int{1, 2, 3}, ""},
		{"a string", "text", ""},
		{"a number", 7, ""},
		{"a value that cannot be written", make(chan int), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			names := fieldNamesOfValue(tc.value)
			got := make([]string, 0, len(names))
			for name := range names {
				got = append(got, name)
			}
			sort.Strings(got)
			if joined := strings.Join(got, ","); joined != tc.want {
				t.Errorf("fieldNamesOfValue = %q, want %q", joined, tc.want)
			}
		})
	}
}
