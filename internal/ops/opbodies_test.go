package ops

// This file drives the operation bodies in extended.go and complete.go that
// optionsetters_test.go and budgetseam_test.go do not touch: FormatWithMetadata,
// MergeWithMetadata, QuestionOptions' fluent setters, ValidateLegacy, and
// CompleteFieldOptions' fluent setters plus buildStructContext. Coverage
// before this file: internal/ops overall 76.1%; every function named below
// showed 0.0% in `go tool cover -func`.

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// --- QuestionOptions fluent setters (extended.go, all 0.0%) ---

// TestQuestionOptionsFluentSettersAllApply is table-driven because the seven
// setters rhyme: each one has to change exactly the field it names and leave
// the rest of the chain intact, the same shape dead_options_test.go polices
// for the setters that file owns.
func TestQuestionOptionsFluentSettersAllApply(t *testing.T) {
	base := NewQuestionOptions("what?")

	withQuestion := base.WithQuestion("changed?")
	if withQuestion.Question != "changed?" {
		t.Fatalf("WithQuestion: Question = %q, want %q", withQuestion.Question, "changed?")
	}

	withEvidence := base.WithIncludeEvidence(false)
	if withEvidence.IncludeEvidence != false {
		t.Fatalf("WithIncludeEvidence(false): IncludeEvidence = %v, want false", withEvidence.IncludeEvidence)
	}

	withConfidence := base.WithIncludeConfidence(false)
	if withConfidence.IncludeConfidence != false {
		t.Fatalf("WithIncludeConfidence(false): IncludeConfidence = %v, want false", withConfidence.IncludeConfidence)
	}

	withReasoning := base.WithIncludeReasoning(false)
	if withReasoning.IncludeReasoning != false {
		t.Fatalf("WithIncludeReasoning(false): IncludeReasoning = %v, want false", withReasoning.IncludeReasoning)
	}

	withSteering := base.WithSteering("be terse")
	if withSteering.CommonOptions.Steering != "be terse" {
		t.Fatalf("WithSteering: Steering = %q, want %q", withSteering.CommonOptions.Steering, "be terse")
	}

	withMode := base.WithMode(types.Strict)
	if withMode.CommonOptions.Mode != types.Strict {
		t.Fatalf("WithMode: Mode = %v, want %v", withMode.CommonOptions.Mode, types.Strict)
	}

	withIntelligence := base.WithIntelligence(types.Quick)
	if withIntelligence.CommonOptions.Intelligence != types.Quick {
		t.Fatalf("WithIntelligence: Intelligence = %v, want %v", withIntelligence.CommonOptions.Intelligence, types.Quick)
	}

	// None of the seven calls above should have mutated base -- QuestionOptions
	// setters are value receivers, and a setter that leaked a mutation back into
	// the receiver would silently corrupt every other branch of a fluent chain
	// built from the same base.
	if base.Question != "what?" || base.IncludeEvidence != true || base.CommonOptions.Steering != "" {
		t.Fatalf("base mutated by fluent setters: %+v", base)
	}
}

// --- CompleteFieldOptions fluent setters (complete.go, all 0.0%) ---

func TestCompleteFieldOptionsFluentSettersAllApply(t *testing.T) {
	base := NewCompleteFieldOptions("Body")

	withField := base.WithFieldName("Title")
	if withField.FieldName != "Title" {
		t.Fatalf("WithFieldName: FieldName = %q, want %q", withField.FieldName, "Title")
	}

	withBackground := base.WithBackground([]string{"note one", "note two"})
	if len(withBackground.Background) != 2 || withBackground.Background[0] != "note one" {
		t.Fatalf("WithBackground: Background = %v, want [note one note two]", withBackground.Background)
	}

	withMaxLength := base.WithMaxLength(42)
	if withMaxLength.MaxLength != 42 {
		t.Fatalf("WithMaxLength: MaxLength = %d, want 42", withMaxLength.MaxLength)
	}

	withTemperature := base.WithTemperature(1.5)
	if withTemperature.Temperature != 1.5 {
		t.Fatalf("WithTemperature: Temperature = %v, want 1.5", withTemperature.Temperature)
	}

	withIntelligence := base.WithIntelligence(types.Quick)
	if withIntelligence.OpOptions.Intelligence != types.Quick {
		t.Fatalf("WithIntelligence: Intelligence = %v, want %v", withIntelligence.OpOptions.Intelligence, types.Quick)
	}

	withMode := base.WithMode(types.Creative)
	if withMode.OpOptions.Mode != types.Creative {
		t.Fatalf("WithMode: Mode = %v, want %v", withMode.OpOptions.Mode, types.Creative)
	}

	if base.FieldName != "Body" || len(base.Background) != 0 || base.MaxLength != 100 {
		t.Fatalf("base mutated by fluent setters: %+v", base)
	}
}

// --- buildStructContext (complete.go, 25.9%) ---

// TestBuildStructContextCoversFieldKinds exercises the branches
// TestCompleteFieldUsesTheConfiguredProvider's single-field record never
// reaches: a slice long enough to truncate, a slice short enough not to, a
// zero-value field that must be skipped, a false bool that must be skipped
// (the "0"/"false" exclusion exists specifically so a legitimate false or
// zero elsewhere in the struct is not reported as if it were unset), an
// unexported field that must never appear, and a json tag that must be
// preferred over the Go field name.
func TestBuildStructContextCoversFieldKinds(t *testing.T) {
	type record struct {
		Title      string `json:"title"`
		unexported string
		Count      int
		Active     bool
		LongList   []string
		ShortList  []int
	}

	r := record{
		Title:      "A Title",
		unexported: "must never appear",
		Count:      0,     // zero value: excluded
		Active:     false, // false: excluded
		LongList:   []string{"a", "b", "c", "d", "e", "f", "g"},
		ShortList:  []int{1, 2},
	}

	ctxLines := buildStructContext(reflect.ValueOf(r), "Title")
	joined := strings.Join(ctxLines, "\n")

	if strings.Contains(joined, "unexported") || strings.Contains(joined, "must never appear") {
		t.Fatalf("buildStructContext leaked an unexported field: %v", ctxLines)
	}
	if strings.Contains(joined, "Count:") || strings.Contains(joined, "count:") {
		t.Fatalf("buildStructContext reported a zero-value int field: %v", ctxLines)
	}
	if strings.Contains(joined, "active:") {
		t.Fatalf("buildStructContext reported a false bool field: %v", ctxLines)
	}
	if !strings.Contains(joined, "and 2 more") {
		t.Fatalf("buildStructContext did not summarize the >5-item slice: %v", ctxLines)
	}
	found := false
	for _, line := range ctxLines {
		if strings.HasPrefix(line, "ShortList: 1, 2") {
			found = true
		}
	}
	if !found {
		t.Fatalf("buildStructContext did not render the short slice in full: %v", ctxLines)
	}
	// The excluded field itself never appears.
	if strings.Contains(joined, "title:") {
		t.Fatalf("buildStructContext included the excluded field itself: %v", ctxLines)
	}
}

// --- FormatWithMetadata (extended.go, 0.0%) ---

func TestFormatWithMetadataHappyPath(t *testing.T) {
	setLLMCaller(func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		return `{"text": "formatted output", "format_applied": "markdown", "transformation_notes": ["shortened"], "confidence": 0.8}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	result, err := FormatWithMetadata(map[string]string{"a": "b"}, "markdown")
	if err != nil {
		t.Fatalf("FormatWithMetadata: %v", err)
	}
	if result.Text != "formatted output" {
		t.Errorf("Text = %q, want %q", result.Text, "formatted output")
	}
	if result.FormatApplied == "" {
		t.Error("FormatApplied is empty on a response that named a format")
	}
}

// TestFormatWithMetadataRefusesAMalformedResponse is the refusal case: the
// doc comment on FormatWithMetadata explicitly says the old fallback -- return
// the raw body as text with an invented 0.7 confidence -- was removed because
// it turned a refusal or an error page into the caller's formatted output. A
// response sharing no field with FormatResult must be an error, not a value.
func TestFormatWithMetadataRefusesAMalformedResponse(t *testing.T) {
	setLLMCaller(func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		return `not json at all`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	result, err := FormatWithMetadata("some data", "a format")
	if err == nil {
		t.Fatalf("FormatWithMetadata accepted an unparseable response, got %+v", result)
	}
	if result.Text != "" {
		t.Errorf("Text = %q on a refused response, want empty", result.Text)
	}
}

// --- MergeWithMetadata (extended.go, 0.0%) ---

func TestMergeWithMetadataEmptySourcesRefused(t *testing.T) {
	_, err := MergeWithMetadata([]Person{}, "any strategy")
	if err == nil {
		t.Fatal("MergeWithMetadata accepted zero sources")
	}
}

func TestMergeWithMetadataSingleSourceShortCircuitsWithoutACall(t *testing.T) {
	sawCall := false
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		sawCall = true
		return `{}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	result, err := MergeWithMetadata([]Person{{Name: "Solo", Age: 40}}, "any strategy")
	if err != nil {
		t.Fatalf("MergeWithMetadata: %v", err)
	}
	if sawCall {
		t.Error("MergeWithMetadata called the model for a single source, which needs no merging")
	}
	if result.Merged.Name != "Solo" {
		t.Errorf("Merged = %+v, want the single source unchanged", result.Merged)
	}
	if len(result.SourcesUsed) != 1 || result.SourcesUsed[0] != 0 {
		t.Errorf("SourcesUsed = %v, want [0]", result.SourcesUsed)
	}
}

func TestMergeWithMetadataHappyPath(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{"merged": {"Name": "Combined", "Age": 33}, "sources_used": [0, 1], "conflicts": [], "confidence": 0.9}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	result, err := MergeWithMetadata([]Person{{Name: "A", Age: 30}, {Name: "B", Age: 36}}, "average")
	if err != nil {
		t.Fatalf("MergeWithMetadata: %v", err)
	}
	if result.Merged.Name != "Combined" {
		t.Errorf("Merged.Name = %q, want %q", result.Merged.Name, "Combined")
	}
	if len(result.SourcesUsed) != 2 {
		t.Errorf("SourcesUsed = %v, want length 2", result.SourcesUsed)
	}
}

// TestMergeWithMetadataFallbackInventsConfidence is a documented finding, not
// an assertion that the behaviour is correct: when the response cannot be
// parsed as the full envelope but does parse directly as T, MergeWithMetadata
// falls back to treating the whole body as the merged value and hard-codes
// ModelConfidence to 0.7 (extended.go, the fallback branch inside
// MergeWithMetadata). AGENTS.md is explicit that an unmeasured number "does
// not exist" and a model's own claim belongs in a Model*-named field, not a
// silently substituted one. This test pins today's actual behaviour so a
// future change to it is a deliberate decision, not a silent regression.
func TestMergeWithMetadataFallbackClaimsNothingItWasNotTold(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		// Recognised as a Person by field name, not as a merge envelope --
		// requireRecognisedField rejects the envelope shape, so this falls
		// through to the "just the merged object" branch.
		return `{"Name": "Fallback", "Age": 21}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	result, err := MergeWithMetadata([]Person{{Name: "A", Age: 1}, {Name: "B", Age: 2}}, "any")
	if err != nil {
		t.Fatalf("MergeWithMetadata: %v", err)
	}
	// The merge itself succeeded on the fallback path, so the value is real.
	if result.Merged.Name != "Fallback" {
		t.Errorf("Merged.Name = %q, want the fallback-parsed value", result.Merged.Name)
	}

	// What must NOT be here is a confidence. This branch used to set 0.7 -- a
	// number no model produced, in a field whose contract is "the model's claim
	// about its own answer". A caller filtering on ModelConfidence >= 0.6 would
	// have accepted every fallback parse on the strength of a constant.
	if result.ModelConfidence != 0 {
		t.Errorf("ModelConfidence = %v, want 0; the envelope carrying the model's confidence is exactly what failed to parse, so there is no confidence to report",
			result.ModelConfidence)
	}

	// Same for the source list: it was filled with every index on the assumption
	// that all sources were used, and the response that would have said so is
	// the one that did not parse.
	if len(result.SourcesUsed) != 0 {
		t.Errorf("SourcesUsed = %v, want empty; assuming every source was used is the same invention in a different field", result.SourcesUsed)
	}
}

func TestMergeWithMetadataRefusesAnUnusableResponse(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `not json and not a person either`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	_, err := MergeWithMetadata([]Person{{Name: "A", Age: 1}, {Name: "B", Age: 2}}, "any")
	if err == nil {
		t.Fatal("MergeWithMetadata accepted a response that was neither a merge envelope nor the target type")
	}
}

// --- ValidateLegacy (extended.go, 0.0%) ---

func TestValidateLegacyHappyPath(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{"valid": true, "issues": [], "confidence": 0.95, "suggestions": []}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	result, err := ValidateLegacy(Person{Name: "Jo", Age: 40}, "age must be positive")
	if err != nil {
		t.Fatalf("ValidateLegacy: %v", err)
	}
	if !result.Valid {
		t.Errorf("Valid = false on a response that reported valid: true")
	}
}

// TestValidateLegacyParseFailureFallsOpen is a documented finding: on a
// response ParseJSONStrict cannot parse, ValidateLegacy does not return an
// error -- it falls back to `strings.Contains(strings.ToLower(response),
// "valid")`, which is the exact substring trap the newer Validate's own doc
// comment says was removed from this codebase, because "invalid" contains
// "valid". ValidateLegacy is marked Deprecated and kept only for backward
// compatibility, so this is not fixed here, but the trap is real: a response
// that says the data is invalid is reported as Valid: true.
// ValidateLegacy must refuse an unparseable answer rather than guess a verdict
// from it.
//
// It used to decide with `strings.Contains(lower, "valid")`, which reports
// Valid on a response saying the data is INVALID -- "invalid" contains "valid".
// A validation gate that fails open on an unparseable answer is worse than one
// that errors, because the caller proceeds believing the check ran. This test
// was written against that behaviour and is now inverted.
func TestValidateLegacyRefusesAnUnparseableVerdict(t *testing.T) {
	for _, body := range []string{
		"the data is invalid because the age is negative",
		"the data is valid",
		"I cannot help with that",
	} {
		setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
			return body, nil
		})

		_, err := ValidateLegacy(Person{Name: "Jo", Age: -1}, "age must be positive")
		if err == nil {
			t.Errorf("ValidateLegacy accepted the unparseable body %q and produced a verdict from it", body)
		}
		setLLMCaller(nil)
	}
}
