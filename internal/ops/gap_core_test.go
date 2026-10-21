package ops

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// This file closes coverage gaps left after an earlier pass took the rest of
// internal/ops to 95%+: options.go's validation/merge logic (the WithX
// setters themselves are already swept by optionsetters_test.go),
// strictdecode.go's error-shaping helpers, resilience.go's CircuitState.String, and text.go's per-operation
// instruction builders. All of these are pure Go -- no provider, no network.

// --- options.go: CommonOptions.WithSteering's three branches -----------

// TestWithSteeringEmptyIsANoOp proves an empty steering string leaves an
// existing value alone rather than clearing it -- WithSteering("") must not
// be indistinguishable from "the caller wants to erase what was set".
func TestWithSteeringEmptyIsANoOp(t *testing.T) {
	c := CommonOptions{Steering: "keep me"}
	c = c.WithSteering("")
	if c.Steering != "keep me" {
		t.Fatalf("WithSteering(\"\") = %q, want the existing value untouched", c.Steering)
	}
}

// TestWithSteeringFirstCallSetsDirectly proves the first non-empty call sets
// the field without a leading separator.
func TestWithSteeringFirstCallSetsDirectly(t *testing.T) {
	var c CommonOptions
	c = c.WithSteering("be concise")
	if c.Steering != "be concise" {
		t.Fatalf("Steering = %q, want %q", c.Steering, "be concise")
	}
}

// TestWithSteeringAccumulatesOnSecondCall is FL-004/F-04's own regression: a
// second WithSteering call used to silently discard the first. It must now
// append, separated by "; ", so both instructions reach the prompt.
func TestWithSteeringAccumulatesOnSecondCall(t *testing.T) {
	c := CommonOptions{}.WithSteering("be concise").WithSteering("cite sources")
	want := "be concise; cite sources"
	if c.Steering != want {
		t.Fatalf("Steering = %q, want %q", c.Steering, want)
	}
}

// --- options.go: lockedLimitsFrom's three outcomes ----------------------

func TestLockedLimitsFromNilContext(t *testing.T) {
	limits, ok := lockedLimitsFrom(nil)
	if ok {
		t.Fatalf("lockedLimitsFrom(nil) = %+v, ok=true, want ok=false", limits)
	}
}

func TestLockedLimitsFromContextWithoutValue(t *testing.T) {
	limits, ok := lockedLimitsFrom(context.Background())
	if ok {
		t.Fatalf("lockedLimitsFrom(no value) = %+v, ok=true, want ok=false", limits)
	}
}

func TestLockedLimitsFromContextWithValue(t *testing.T) {
	want := LockedLimits{MaxOutputTokens: 42}
	ctx := WithLockedLimits(context.Background(), want)
	got, ok := lockedLimitsFrom(ctx)
	if !ok {
		t.Fatal("lockedLimitsFrom did not find the attached limits")
	}
	if got != want {
		t.Fatalf("lockedLimitsFrom = %+v, want %+v", got, want)
	}
}

// --- options.go: modeStrictness's four branches --------------------------

func TestModeStrictnessOrdering(t *testing.T) {
	cases := []struct {
		mode types.Mode
		want int
	}{
		{types.Strict, 3},
		{types.TransformMode, 2},
		{types.Creative, 1},
		{types.ModeUnset, 0},
	}
	for _, tc := range cases {
		if got := modeStrictness(tc.mode); got != tc.want {
			t.Errorf("modeStrictness(%v) = %d, want %d", tc.mode, got, tc.want)
		}
	}
	if modeStrictness(types.Strict) <= modeStrictness(types.TransformMode) {
		t.Fatal("Strict must outrank TransformMode")
	}
	if modeStrictness(types.TransformMode) <= modeStrictness(types.Creative) {
		t.Fatal("TransformMode must outrank Creative")
	}
}

// --- options.go: the Validate() functions the reflection sweep does not
// reach, because they are validation logic rather than setters. ----------

func TestExpandOptionsValidateRejectsSubDefaultExpansionFactor(t *testing.T) {
	opts := NewExpandOptions()
	opts.ExpansionFactor = 0.5
	if err := opts.Validate(); err == nil {
		t.Fatal("expected an error for ExpansionFactor < 1")
	}
}

func TestExpandOptionsValidateRejectsOutOfRangeDetailLevel(t *testing.T) {
	low := NewExpandOptions()
	low.DetailLevel = 0
	if err := low.Validate(); err == nil {
		t.Fatal("expected an error for DetailLevel < 1")
	}
	high := NewExpandOptions()
	high.DetailLevel = 11
	if err := high.Validate(); err == nil {
		t.Fatal("expected an error for DetailLevel > 10")
	}
}

func TestExpandOptionsValidateAcceptsDefaults(t *testing.T) {
	if err := NewExpandOptions().Validate(); err != nil {
		t.Fatalf("defaults should validate: %v", err)
	}
}

func TestExpandOptionsValidatePropagatesCommonOptionsError(t *testing.T) {
	opts := NewExpandOptions()
	opts.CommonOptions.Threshold = 2 // out of [0,1]
	if err := opts.Validate(); err == nil {
		t.Fatal("expected the embedded CommonOptions error to surface")
	}
}

func TestChooseOptionsValidateRejectsTopNBelowOne(t *testing.T) {
	opts := NewChooseOptions().WithTopN(0)
	if err := opts.Validate(); err == nil {
		t.Fatal("expected an error for TopN < 1")
	}
}

func TestChooseOptionsValidateRejectsUnknownStrategy(t *testing.T) {
	opts := NewChooseOptions()
	opts.Strategy = "coin-flip"
	if err := opts.Validate(); err == nil {
		t.Fatal("expected an error for an unrecognised strategy")
	}
}

func TestChooseOptionsValidateAcceptsDefaults(t *testing.T) {
	if err := NewChooseOptions().Validate(); err != nil {
		t.Fatalf("defaults should validate: %v", err)
	}
}

func TestFilterOptionsValidateRequiresCriteria(t *testing.T) {
	opts := NewFilterOptions()
	if err := opts.Validate(); err == nil {
		t.Fatal("expected an error for empty Criteria")
	}
}

func TestFilterOptionsValidateRejectsOutOfRangeMinConfidence(t *testing.T) {
	opts := NewFilterOptions().WithCriteria("cheap").WithMinConfidence(1.5)
	if err := opts.Validate(); err == nil {
		t.Fatal("expected an error for MinConfidence > 1")
	}
}

func TestFilterOptionsValidateAcceptsCriteriaOnly(t *testing.T) {
	opts := NewFilterOptions().WithCriteria("cheap")
	if err := opts.Validate(); err != nil {
		t.Fatalf("expected valid options, got %v", err)
	}
}

func TestSortOptionsValidateRequiresCriteria(t *testing.T) {
	opts := NewSortOptions()
	if err := opts.Validate(); err == nil {
		t.Fatal("expected an error for empty Criteria")
	}
}

func TestSortOptionsValidateRejectsUnknownDirection(t *testing.T) {
	opts := NewSortOptions().WithCriteria("price")
	opts.Direction = "sideways"
	if err := opts.Validate(); err == nil {
		t.Fatal("expected an error for an unrecognised direction")
	}
}

func TestSortOptionsValidateAcceptsCriteriaOnly(t *testing.T) {
	opts := NewSortOptions().WithCriteria("price")
	if err := opts.Validate(); err != nil {
		t.Fatalf("expected valid options, got %v", err)
	}
}

// TestFilterOptionsValidateRejectsALockedLimitViolation covers the
// checkLockedLimits branch of FilterOptions.Validate directly -- the
// existing locked-limits tests in optionscope_test.go exercise
// ExtractOptions and ChooseOptions but not FilterOptions.
func TestFilterOptionsValidateRejectsALockedLimitViolation(t *testing.T) {
	ctx := WithLockedLimits(context.Background(), LockedLimits{MaxOutputTokens: 100})
	opts := NewFilterOptions().WithCriteria("cheap")
	opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
	opts.OpOptions.MaxOutputTokens = 5000

	err := opts.Validate()
	if err == nil {
		t.Fatal("expected FilterOptions.Validate to reject a MaxOutputTokens above the locked ceiling")
	}
	if !stringsContainsAll(err.Error(), "locked policy") {
		t.Errorf("error = %q, want it to name the locked policy", err.Error())
	}
}

// --- strictdecode.go: describeDecodeFailure and secondJSONValue ---------

// TestDescribeDecodeFailureNamesTheFieldNotTheValue proves the message names
// the field's JSON pointer and the type mismatch's shape, but never the
// value itself -- the same never-log-the-payload rule the diagnostic sink
// tests exercise for the repair loop, checked here directly against the
// helper that builds the message.
func TestDescribeDecodeFailureNamesTheFieldNotTheValue(t *testing.T) {
	err := &json.UnmarshalTypeError{Value: "string", Type: reflect.TypeOf(0), Field: "amount.total"}
	got := describeDecodeFailure(err)
	if got == "" {
		t.Fatal("describeDecodeFailure returned an empty message")
	}
	wantSubstr := "/amount/total"
	if !stringsContainsAll(got, wantSubstr) {
		t.Errorf("describeDecodeFailure = %q, want it to name the pointer %q", got, wantSubstr)
	}
}

func TestDescribeDecodeFailureWithNoFieldNamesOnlyTheShape(t *testing.T) {
	err := &json.UnmarshalTypeError{Value: "array", Type: reflect.TypeOf("")}
	got := describeDecodeFailure(err)
	if !stringsContainsAll(got, "does not fit the target type") {
		t.Errorf("describeDecodeFailure = %q, want a generic shape mismatch message", got)
	}
}

func TestDescribeDecodeFailureNamesStrictAsTheCause(t *testing.T) {
	err := errors.New(`json: unknown field "extra"`)
	got := describeDecodeFailure(err)
	if !stringsContainsAll(got, "Strict()") {
		t.Errorf("describeDecodeFailure = %q, want it to name Strict() as the cause", got)
	}
	if !stringsContainsAll(got, "extra") {
		t.Errorf("describeDecodeFailure = %q, want it to name the offending field", got)
	}
}

func TestDescribeDecodeFailureFallsBackForAnUnrecognisedError(t *testing.T) {
	err := errors.New("some other decode problem")
	got := describeDecodeFailure(err)
	if got != "the response does not fit the target type" {
		t.Errorf("describeDecodeFailure = %q, want the generic fallback message", got)
	}
}

func TestSecondJSONValueNotFoundInOriginal(t *testing.T) {
	if _, ok := secondJSONValue("no match here", "{\"a\":1}"); ok {
		t.Fatal("expected false when extracted does not appear in original")
	}
}

func TestSecondJSONValueNothingAfterTheExtractedValue(t *testing.T) {
	original := `{"a":1}`
	if _, ok := secondJSONValue(original, original); ok {
		t.Fatal("expected false when nothing follows the extracted value")
	}
}

func TestSecondJSONValueTrailingProseIsNotASecondValue(t *testing.T) {
	original := `{"a":1} let me know if you need anything else`
	if _, ok := secondJSONValue(original, `{"a":1}`); ok {
		t.Fatal("trailing prose must not be reported as a second JSON value")
	}
}

func TestSecondJSONValueDetectsATrailingObject(t *testing.T) {
	original := `{"a":1} {"b":2}`
	kind, ok := secondJSONValue(original, `{"a":1}`)
	if !ok {
		t.Fatal("expected a second value to be detected")
	}
	if kind != "object" {
		t.Errorf("kind = %q, want %q", kind, "object")
	}
}

func TestSecondJSONValueDetectsATrailingArray(t *testing.T) {
	original := `{"a":1} [1,2,3]`
	kind, ok := secondJSONValue(original, `{"a":1}`)
	if !ok {
		t.Fatal("expected a second value to be detected")
	}
	if kind != "array" {
		t.Errorf("kind = %q, want %q", kind, "array")
	}
}

func TestSecondJSONValueIgnoresAFencedClosingMarker(t *testing.T) {
	original := "{\"a\":1}\n```"
	if _, ok := secondJSONValue(original, `{"a":1}`); ok {
		t.Fatal("a closing fence must not be treated as a second value")
	}
}

// --- resilience.go: CircuitState.String's four branches ------------------

func TestCircuitStateStringNamesEveryValue(t *testing.T) {
	cases := map[CircuitState]string{
		CircuitClosed:    "closed",
		CircuitOpen:      "open",
		CircuitHalfOpen:  "half-open",
		CircuitState(99): "unknown",
	}
	for state, want := range cases {
		if got := state.String(); got != want {
			t.Errorf("CircuitState(%d).String() = %q, want %q", state, got, want)
		}
	}
}

// --- text.go: rewriteInstructions and translateInstructions --------------

// TestRewriteInstructionsCoversEveryClause exercises every branch: the
// default FormalityLevel (5) produces no clause, but every other field --
// set or left at its zero value -- is checked on both sides.
func TestRewriteInstructionsCoversEveryClause(t *testing.T) {
	t.Run("all zero produces no clauses beyond defaults", func(t *testing.T) {
		opts := RewriteOptions{FormalityLevel: 5}
		got := rewriteInstructions(opts)
		if len(got) != 0 {
			t.Errorf("expected no instructions for an all-default RewriteOptions, got %v", got)
		}
	})

	t.Run("every field populated produces every clause", func(t *testing.T) {
		opts := RewriteOptions{
			TargetTone:     "formal",
			FormalityLevel: 8,
			Audience:       "executives",
			StyleGuide:     "AP",
			Changes:        []string{"shorten"},
			AvoidWords:     []string{"very"},
			IncludeWords:   []string{"strategic"},
			PreserveFacts:  true,
		}
		got := rewriteInstructions(opts)
		if len(got) != 8 {
			t.Fatalf("expected 8 instructions, got %d: %v", len(got), got)
		}
	})
}

// TestTranslateInstructionsCoversEveryClause exercises the default-skipping
// branches (Formality=="neutral", CulturalAdaptation==5) as well as the
// populated case, including the Glossary map render.
func TestTranslateInstructionsCoversEveryClause(t *testing.T) {
	t.Run("defaults produce only the mandatory target-language clause", func(t *testing.T) {
		opts := TranslateOptions{TargetLanguage: "fr", Formality: "neutral", CulturalAdaptation: 5}
		got := translateInstructions(opts)
		if len(got) != 1 {
			t.Fatalf("expected exactly 1 instruction (target language), got %d: %v", len(got), got)
		}
	})

	t.Run("every field populated produces every clause", func(t *testing.T) {
		opts := TranslateOptions{
			TargetLanguage:     "fr",
			SourceLanguage:     "en",
			Dialect:            "Quebecois",
			Formality:          "formal",
			CulturalAdaptation: 9,
			PreserveFormatting: true,
			Glossary:           map[string]string{"widget": "gadget"},
		}
		got := translateInstructions(opts)
		// target, source, dialect, formality, cultural, preserve, glossary = 7
		if len(got) != 7 {
			t.Fatalf("expected 7 instructions, got %d: %v", len(got), got)
		}
	})
}

// --- typesupport.go: TypeSupport.String's five branches -------------------

func TestTypeSupportStringNamesEveryValue(t *testing.T) {
	cases := map[TypeSupport]string{
		SupportFull:       "full",
		SupportRestricted: "restricted",
		SupportOpaque:     "opaque",
		SupportRejected:   "rejected",
		TypeSupport(99):   "unknown",
	}
	for support, want := range cases {
		if got := support.String(); got != want {
			t.Errorf("TypeSupport(%d).String() = %q, want %q", support, got, want)
		}
	}
}

// --- procedural.go: Workflow.SetState/GetState -----------------------------

// stringsContainsAll is a tiny local helper so this file does not depend on
// strings.Contains's import placement in other test files.
func stringsContainsAll(s, substr string) bool {
	return len(substr) == 0 || (len(s) >= len(substr) && indexOfSubstring(s, substr) >= 0)
}

func indexOfSubstring(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
