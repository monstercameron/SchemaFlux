package ops

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// cover_misc_test.go raises coverage across the remaining files in this
// group's assignment: schemaid.go, match.go, rank.go, resolve.go,
// predict.go, verify.go (VerifyClaim), synthesize.go, suggest.go, and the
// nine under-tested functions in extended.go that opbodies_test.go and
// extended_test.go do not already reach (Question setters, FormatWithMetadata,
// MergeWithMetadata, and ValidateLegacy are covered elsewhere and are not
// repeated here).

func installMiscResponse(t *testing.T, body string) {
	t.Helper()
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return body, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
}

func installMiscFailure(t *testing.T) {
	t.Helper()
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", ErrNoProvider
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
}

// ===================== schemaid.go =====================

func TestSchemaDescriptorStringAnonymousWithNoName(t *testing.T) {
	d := SchemaDescriptor{Hash: "abc123"}
	got := d.String()
	if !strings.HasPrefix(got, "anonymous@") {
		t.Fatalf("String() = %q, want the anonymous@<hash> form", got)
	}
	if !strings.Contains(got, "abc123") {
		t.Fatalf("String() = %q, want it to carry the hash", got)
	}
}

func TestSchemaCacheKeyFillsEmptyPartsWithDash(t *testing.T) {
	key := SchemaCacheKey("", "", SchemaDescriptor{})
	for _, part := range strings.Split(key, ":") {
		if part != "-" {
			t.Fatalf("SchemaCacheKey with all-empty inputs = %q, want every part to be \"-\"", key)
		}
	}
}

func TestCanonicalJSONFallsBackOnMarshalFailure(t *testing.T) {
	// A channel cannot be marshaled by encoding/json; canonicalJSON's error
	// branch falls back to fmt.Sprintf("%v", value) rather than panicking or
	// returning an empty digest input.
	unmarshalable := map[string]any{"c": make(chan int)}
	got := canonicalJSON(unmarshalable)
	if got == "" {
		t.Fatal("canonicalJSON returned an empty string on a marshal failure")
	}
}

func TestNormalizeForHashSortsStringSlices(t *testing.T) {
	got := normalizeForHash([]string{"z", "a", "m"})
	sorted, ok := got.([]string)
	if !ok {
		t.Fatalf("normalizeForHash([]string) returned %T, want []string", got)
	}
	if !reflect.DeepEqual(sorted, []string{"a", "m", "z"}) {
		t.Fatalf("normalizeForHash sorted = %v, want [a m z]", sorted)
	}
}

func TestNormalizeForHashRecursesIntoSlicesAndMaps(t *testing.T) {
	input := []any{
		map[string]any{"z": 1, "a": 2},
		"literal",
	}
	got := normalizeForHash(input)
	normalized, ok := got.([]any)
	if !ok || len(normalized) != 2 {
		t.Fatalf("normalizeForHash([]any) = %#v", got)
	}
	if _, ok := normalized[0].(map[string]any); !ok {
		t.Fatalf("normalizeForHash did not recurse into the nested map: %#v", normalized[0])
	}
	if normalized[1] != "literal" {
		t.Fatalf("normalizeForHash changed a plain literal: %#v", normalized[1])
	}
}

func TestNormalizeForHashDefaultPassesThroughUnknownTypes(t *testing.T) {
	if got := normalizeForHash(42); got != 42 {
		t.Fatalf("normalizeForHash(42) = %v, want 42 unchanged", got)
	}
}

// ===================== match.go =====================

type matchSource struct {
	Term string `json:"term"`
}
type matchTarget struct {
	Name string `json:"name"`
}

func TestSemanticMatchEmptySourcesOrTargetsIsNoOp(t *testing.T) {
	sources := []matchSource{{Term: "a"}, {Term: "b"}}
	result, err := SemanticMatch(sources, []matchTarget{}, NewMatchOptions())
	if err != nil {
		t.Fatalf("SemanticMatch: %v", err)
	}
	if len(result.UnmatchedSources) != 2 {
		t.Fatalf("UnmatchedSources = %v, want both source indices reported unmatched", result.UnmatchedSources)
	}
	if len(result.Matches) != 0 {
		t.Fatalf("Matches = %v, want none when targets is empty", result.Matches)
	}
}

func TestSemanticMatchRejectsInvalidOptions(t *testing.T) {
	opts := NewMatchOptions()
	opts.Threshold = 5 // out of [0,1]
	_, err := SemanticMatch([]matchSource{{Term: "a"}}, []matchTarget{{Name: "b"}}, opts)
	if err == nil {
		t.Fatal("SemanticMatch accepted an out-of-range threshold")
	}
}

func TestSemanticMatchParsesStructuredResponse(t *testing.T) {
	installMiscResponse(t, `{
		"matches": [{"source_index": 0, "target_index": 1, "score": 0.9, "explanation": "close match"}],
		"unmatched_sources": [],
		"unmatched_targets": [0]
	}`)

	sources := []matchSource{{Term: "wireless"}}
	targets := []matchTarget{{Name: "wired"}, {Name: "wireless headphones"}}

	result, err := SemanticMatch(sources, targets, NewMatchOptions().
		WithMatchFields([]string{"term"}).
		WithFieldWeights(map[string]float64{"term": 2}).
		WithMatchCriteria("semantic similarity").
		WithBidirectional(true))
	if err != nil {
		t.Fatalf("SemanticMatch: %v", err)
	}
	if result.TotalMatches != 1 {
		t.Fatalf("TotalMatches = %d, want 1", result.TotalMatches)
	}
	if result.Matches[0].Target.Name != "wireless headphones" {
		t.Fatalf("Matches[0].Target = %+v", result.Matches[0].Target)
	}
	if result.AverageScore != 0.9 {
		t.Fatalf("AverageScore = %v, want 0.9", result.AverageScore)
	}
}

func TestSemanticMatchRefusesUnparseableResponse(t *testing.T) {
	installMiscResponse(t, "not json")
	_, err := SemanticMatch([]matchSource{{Term: "a"}}, []matchTarget{{Name: "b"}}, NewMatchOptions())
	if err == nil {
		t.Fatal("SemanticMatch accepted an unparseable response")
	}
}

func TestSemanticMatchSurfacesLLMFailure(t *testing.T) {
	installMiscFailure(t)
	_, err := SemanticMatch([]matchSource{{Term: "a"}}, []matchTarget{{Name: "b"}}, NewMatchOptions())
	if err == nil {
		t.Fatal("SemanticMatch did not surface the provider's failure")
	}
}

func TestMatchOneDelegatesToSemanticMatch(t *testing.T) {
	installMiscResponse(t, `{
		"matches": [{"source_index": 0, "target_index": 0, "score": 0.75, "explanation": "ok"}],
		"unmatched_sources": [],
		"unmatched_targets": []
	}`)

	matches, err := MatchOne(matchSource{Term: "a"}, []matchTarget{{Name: "b"}}, NewMatchOptions())
	if err != nil {
		t.Fatalf("MatchOne: %v", err)
	}
	if len(matches) != 1 || matches[0].Score != 0.75 {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestMatchOnePropagatesFailure(t *testing.T) {
	installMiscFailure(t)
	_, err := MatchOne(matchSource{Term: "a"}, []matchTarget{{Name: "b"}}, NewMatchOptions())
	if err == nil {
		t.Fatal("MatchOne did not propagate SemanticMatch's failure")
	}
}

// ===================== resolve.go =====================

type resolveCustomer struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func TestResolveNoSourcesIsRefused(t *testing.T) {
	_, err := Resolve[resolveCustomer](nil)
	if err == nil {
		t.Fatal("Resolve accepted zero sources")
	}
}

func TestResolveSingleSourceShortCircuits(t *testing.T) {
	source := resolveCustomer{Name: "Ada", Email: "ada@example.com"}
	result, err := Resolve([]resolveCustomer{source})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result.Resolved != source {
		t.Fatalf("Resolved = %+v, want the single source echoed back", result.Resolved)
	}
	if result.Strategy != "single-source" || result.ModelConfidence != 0 {
		t.Fatalf("Strategy/Confidence = %q/%v, want single-source/0 (no model was asked)", result.Strategy, result.ModelConfidence)
	}
}

func TestResolveMultiSourceParsesStructuredResponse(t *testing.T) {
	installMiscResponse(t, `{
		"resolved": {"name": "Ada Lovelace", "email": "ada@newmail.com"},
		"conflicts": [{"field": "email", "values": {"0": "ada@old.com", "1": "ada@newmail.com"}, "resolution": "prefer newest", "chosen_source": 1, "chosen_value": "ada@newmail.com"}],
		"source_contributions": {"0": ["name"], "1": ["email"]},
		"confidence": 0.8
	}`)

	sources := []resolveCustomer{
		{Name: "Ada Lovelace", Email: "ada@old.com"},
		{Name: "Ada L.", Email: "ada@newmail.com"},
	}
	result, err := Resolve(sources, ResolveOptions{
		Strategy:          "most-complete",
		FieldPriorities:   map[string]int{"email": 1},
		ConflictThreshold: 0.9,
		Steering:          "prefer completeness",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result.Resolved.Email != "ada@newmail.com" {
		t.Fatalf("Resolved = %+v", result.Resolved)
	}
	if len(result.Conflicts) != 1 {
		t.Fatalf("Conflicts = %+v", result.Conflicts)
	}
	if result.SourceContributions[1][0] != "email" {
		t.Fatalf("SourceContributions = %+v, want the string key '1' converted to int 1", result.SourceContributions)
	}
	if result.ModelConfidence != 0.8 {
		t.Fatalf("ModelConfidence = %v, want 0.8", result.ModelConfidence)
	}
}

func TestResolveRefusesUnparseableResponse(t *testing.T) {
	installMiscResponse(t, "not json")
	_, err := Resolve([]resolveCustomer{{Name: "A"}, {Name: "B"}})
	if err == nil {
		t.Fatal("Resolve accepted an unparseable response")
	}
}

func TestResolveSurfacesLLMFailure(t *testing.T) {
	installMiscFailure(t)
	_, err := Resolve([]resolveCustomer{{Name: "A"}, {Name: "B"}})
	if err == nil {
		t.Fatal("Resolve did not surface the provider's failure")
	}
}

func TestMergeResolveOptionsUserOverridesDefaults(t *testing.T) {
	defaults := ResolveOptions{
		Strategy:          "most-complete",
		ConflictThreshold: 0.8,
		Mode:              types.TransformMode,
		Intelligence:      types.Fast,
	}
	user := ResolveOptions{
		Strategy:            "newest",
		AuthoritativeSource: 2,
		FieldPriorities:     map[string]int{"email": 1},
		ConflictThreshold:   0.5,
		Steering:            "custom guidance",
		Mode:                types.Creative,
		Intelligence:        types.Smart,
		Model:               "pinned-model",
		Context:             context.Background(),
	}

	merged := mergeResolveOptions(defaults, user)
	if merged.Strategy != "newest" {
		t.Errorf("Strategy = %q, want newest", merged.Strategy)
	}
	if merged.AuthoritativeSource != 2 {
		t.Errorf("AuthoritativeSource = %d, want 2", merged.AuthoritativeSource)
	}
	if merged.FieldPriorities["email"] != 1 {
		t.Errorf("FieldPriorities = %v", merged.FieldPriorities)
	}
	if merged.ConflictThreshold != 0.5 {
		t.Errorf("ConflictThreshold = %v, want 0.5", merged.ConflictThreshold)
	}
	if merged.Steering != "custom guidance" {
		t.Errorf("Steering = %q", merged.Steering)
	}
	if merged.Mode != types.Creative || merged.Intelligence != types.Smart {
		t.Errorf("Mode/Intelligence = %v/%v", merged.Mode, merged.Intelligence)
	}
	if merged.Model != "pinned-model" {
		t.Errorf("Model = %q", merged.Model)
	}
	if merged.Context == nil {
		t.Error("Context was not carried over from user options")
	}
}

// TestMergeResolveOptionsZeroUserLeavesDefaults is the companion case: a
// user ResolveOptions with every field left at its zero value must not
// clobber any default -- each `if user.X != zero` guard exists precisely so
// an unset field means "no opinion", not "reset to zero".
func TestMergeResolveOptionsZeroUserLeavesDefaults(t *testing.T) {
	defaults := ResolveOptions{
		Strategy:          "most-complete",
		ConflictThreshold: 0.8,
		Mode:              types.TransformMode,
		Intelligence:      types.Fast,
	}
	merged := mergeResolveOptions(defaults, ResolveOptions{})
	if merged.Strategy != defaults.Strategy ||
		merged.AuthoritativeSource != defaults.AuthoritativeSource ||
		merged.ConflictThreshold != defaults.ConflictThreshold ||
		merged.Steering != defaults.Steering ||
		merged.Mode != defaults.Mode ||
		merged.Intelligence != defaults.Intelligence ||
		merged.Model != defaults.Model ||
		merged.Context != defaults.Context {
		t.Fatalf("mergeResolveOptions with a zero user value = %+v, want defaults unchanged: %+v", merged, defaults)
	}
	if merged.FieldPriorities != nil {
		t.Fatalf("FieldPriorities = %v, want nil (defaults never set it and the zero user did not either)", merged.FieldPriorities)
	}
}

// ===================== verify.go: VerifyClaim =====================

// The scripted answer holds a verified claim and then a false one. VerifyClaim
// used to return the first, so this asserted "verified" for content the same
// answer called false (LX-028).
func TestVerifyClaimReportsTheWorstClaimFound(t *testing.T) {
	installVerifyResponse(t)

	result, err := VerifyClaim("Water boils at 100C", NewVerifyOptions().WithMinConfidence(0))
	if err != nil {
		t.Fatalf("VerifyClaim: %v", err)
	}
	if result.Claim != "Water boils at 100C" {
		t.Fatalf("Claim = %q, want the caller's claim", result.Claim)
	}
	if result.Verdict != "false" {
		t.Fatalf("Verdict = %q, want the worst of the claims found", result.Verdict)
	}
	// What comes with the verdict is the false claim's own: its confidence
	// and its evidence. This asserted the lowest confidence and the evidence
	// of both claims, which paired "false" with the verified claim's 0.95 and
	// its supporting evidence (review round 9).
	if result.ModelConfidence != 0.99 {
		t.Fatalf("ModelConfidence = %v, want the false claim's own", result.ModelConfidence)
	}
	if len(result.Evidence) != 1 {
		t.Fatalf("Evidence = %v, want the false claim's own", result.Evidence)
	}
}

// TestVerifyClaimHasNoVerdictWhenNoClaimWasAssessed covers the branch where
// the model returns no per-claim breakdown at all. It used to answer with the
// overall verdict, and this test asserted "verified" for it.
func TestVerifyClaimHasNoVerdictWhenNoClaimWasAssessed(t *testing.T) {
	installMiscResponse(t, `{
		"claims": [], "logic_issues": [], "consistency_issues": [],
		"overall_confidence": 0.9,
		"trust_score": 0.9,
		"summary": "checked as a whole"
	}`)

	// There is no fallback any more. A review that assessed no claim has no
	// verdict for the claim, and this used to hand back the overall one, which
	// could be "verified" (LX-028).
	result, err := VerifyClaim("The sky is blue", NewVerifyOptions().WithMinConfidence(0))
	if err == nil {
		t.Fatalf("VerifyClaim returned %+v for a claim nobody assessed", result)
	}
	if result.Verdict != "" || result.Reasoning != "" {
		t.Fatalf("a refused claim came back with a verdict: %+v", result)
	}
}

func TestVerifyClaimPropagatesFailure(t *testing.T) {
	installMiscFailure(t)
	_, err := VerifyClaim("anything", NewVerifyOptions())
	if err == nil {
		t.Fatal("VerifyClaim did not propagate Verify's failure")
	}
}

// ===================== synthesize.go =====================

type synthReport struct {
	Summary string `json:"summary"`
}

func TestSynthesizeNoSourcesIsRefused(t *testing.T) {
	_, err := Synthesize[synthReport](nil, NewSynthesizeOptions())
	if err == nil {
		t.Fatal("Synthesize accepted zero sources")
	}
}

func TestSynthesizeRejectsInvalidOptions(t *testing.T) {
	opts := NewSynthesizeOptions()
	opts.Strategy = "not-a-strategy"
	_, err := Synthesize[synthReport]([]any{"a source"}, opts)
	if err == nil {
		t.Fatal("Synthesize accepted an unrecognised strategy")
	}
}

func TestSynthesizeParsesStructuredResponse(t *testing.T) {
	installMiscResponse(t, `{
		"synthesized": {"summary": "unified view"},
		"facts": [{"fact": "widely reported", "sources": [0, 1]}],
		"insights": [{"insight": "a pattern", "supporting_sources": [0], "type": "pattern"}],
		"conflicts": [{"topic": "date", "positions": {"0": "Jan", "1": "Feb"}, "resolution": "used majority", "chosen_source": 0}],
		"source_coverage": {"0": 0.9, "1": 0.7}
	}`)

	sources := []any{
		map[string]string{"title": "A"},
		map[string]string{"title": "B"},
	}
	result, err := Synthesize[synthReport](sources, NewSynthesizeOptions().
		WithStrategy("integrate").
		WithConflictResolution("source-priority").
		WithSourcePriorities([]int{1, 0}).
		WithCiteSources(true).
		WithGenerateInsights(true).
		WithFocusAreas([]string{"methodology"}).
		WithExcludeAspects([]string{"tone"}).
		WithOutputStructure("bullet points"))
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if result.Synthesized.Summary != "unified view" {
		t.Fatalf("Synthesized = %+v", result.Synthesized)
	}
	if len(result.Facts) != 1 || len(result.Insights) != 1 || len(result.Conflicts) != 1 {
		t.Fatalf("Facts/Insights/Conflicts = %+v / %+v / %+v", result.Facts, result.Insights, result.Conflicts)
	}
}

// A string target takes a JSON string as the synthesis. It does not take an
// object or a list and read its JSON text as the string, which an earlier
// fallback did; synthesize_test.go has the refusals.
func TestSynthesizeStringTargetAcceptsAStringPayload(t *testing.T) {
	installMiscResponse(t, `{"synthesized":"a unified summary of the sources","source_coverage":{},"facts":[],"insights":[],"conflicts":[]}`)

	result, err := Synthesize[string]([]any{"source"}, NewSynthesizeOptions().WithCiteSources(false))
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if result.Synthesized != "a unified summary of the sources" {
		t.Fatalf("Synthesized = %q", result.Synthesized)
	}
}

func TestSynthesizeRefusesUnparseableResponse(t *testing.T) {
	installMiscResponse(t, "not json")
	_, err := Synthesize[synthReport]([]any{"a"}, NewSynthesizeOptions())
	if err == nil {
		t.Fatal("Synthesize accepted an unparseable response")
	}
}

func TestSynthesizeSurfacesLLMFailure(t *testing.T) {
	installMiscFailure(t)
	_, err := Synthesize[synthReport]([]any{"a"}, NewSynthesizeOptions())
	if err == nil {
		t.Fatal("Synthesize did not surface the provider's failure")
	}
}

// ===================== suggest.go =====================

func TestSuggestParsesDirectArray(t *testing.T) {
	installMiscResponse(t, `["do this", "then that"]`)

	suggestions, err := Suggest[string]("some context", NewSuggestOptions().WithTopN(2))
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if len(suggestions) != 2 {
		t.Fatalf("suggestions = %v, want both parsed out of the direct array", suggestions)
	}
}

// Suggest used to truncate an over-limit answer to TopN and report success,
// which is the anti-pattern AtMost's own doc comment names: it turns a model
// that ignored the instruction into a result that looks obedient. A caller
// asking for two and getting three should find out, rather than receiving a
// plausible two with no sign that the ranking was cut down locally.
func TestSuggestRefusesMoreSuggestionsThanRequested(t *testing.T) {
	installMiscResponse(t, `["do this", "then that", "finally this"]`)

	if _, err := Suggest[string]("some context", NewSuggestOptions().WithTopN(2)); err == nil {
		t.Fatal("Suggest accepted three suggestions against a TopN of 2")
	}
}

func TestSuggestParsesSuggestionsKeyObject(t *testing.T) {
	installMiscResponse(t, `{"suggestions": ["a", "b"]}`)

	suggestions, err := Suggest[string]("ctx", NewSuggestOptions().WithTopN(5))
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if len(suggestions) != 2 {
		t.Fatalf("suggestions = %v", suggestions)
	}
}

// Suggest's third parse strategy: an array of objects, with strings pulled from
// common field names.
//
// This test pinned a bug and is now inverted. The first parse attempt decodes
// into []T, which fails against an array of objects -- but encoding/json
// PARTIALLY POPULATES the slice before returning its error, appending one zero
// value per unconvertible element. Two objects left ["", ""] behind, and the
// object-extraction fallback appended to that same slice, so asking for two
// suggestions returned four with the first two empty and suggestions[0] == "".
//
// Fixed by giving the extraction branch its own accumulator. The assertion is
// on the exact contents rather than just the length, because a length of two
// with an empty first element would be the same bug wearing a smaller number.
func TestSuggestExtractsFromArrayOfObjects(t *testing.T) {
	installMiscResponse(t, `[{"name": "clean data"}, {"text": "dedupe rows"}]`)

	suggestions, err := Suggest[string]("ctx", NewSuggestOptions().WithTopN(5))
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}

	want := []string{"clean data", "dedupe rows"}
	if len(suggestions) != len(want) {
		t.Fatalf("suggestions = %q, want %q", suggestions, want)
	}
	for i := range want {
		if suggestions[i] != want[i] {
			t.Fatalf("suggestions = %q, want %q", suggestions, want)
		}
	}
}

func TestSuggestAcceptsSingleObjectAsOneItemResult(t *testing.T) {
	installMiscResponse(t, `{"suggestion": "just this one"}`)

	suggestions, err := Suggest[string]("ctx", NewSuggestOptions().WithTopN(5))
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if len(suggestions) != 1 || suggestions[0] != "just this one" {
		t.Fatalf("suggestions = %v", suggestions)
	}
}

func TestSuggestRefusesUnrecognisableResponse(t *testing.T) {
	installMiscResponse(t, "prose that is not JSON at all")

	_, err := Suggest[string]("ctx", NewSuggestOptions())
	if err == nil {
		t.Fatal("Suggest accepted a response that matched none of its parse strategies")
	}
}

func TestSuggestStripsMarkdownFences(t *testing.T) {
	installMiscResponse(t, "```json\n[\"a\", \"b\"]\n```")

	suggestions, err := Suggest[string]("ctx", NewSuggestOptions().WithTopN(5))
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if len(suggestions) != 2 {
		t.Fatalf("suggestions = %v, want the fenced array parsed", suggestions)
	}
}

func TestSuggestRejectsInvalidOptions(t *testing.T) {
	opts := NewSuggestOptions()
	opts.Strategy = "not-a-real-strategy"
	_, err := Suggest[string]("ctx", opts)
	if err == nil {
		t.Fatal("Suggest accepted an invalid strategy")
	}
}

func TestSuggestSurfacesLLMFailure(t *testing.T) {
	installMiscFailure(t)
	_, err := Suggest[string]("ctx", NewSuggestOptions())
	if err == nil {
		t.Fatal("Suggest did not surface the provider's failure")
	}
}

// ===================== extended.go: the nine under-covered functions =====================
//
// Validate, ValidateHybrid, ValidateDeterministically, validateResultToJudgment,
// validateHybridCore, Format, Merge, QuestionOptions.Validate, and Question.
// FormatWithMetadata, MergeWithMetadata, ValidateLegacy, and Question's fluent
// setters are opbodies_test.go's territory and are not duplicated here.

// --- ValidateDeterministically: the three refusal shapes that never
// consult a model.

func TestValidateDeterministicallyRefusesFreeTextRules(t *testing.T) {
	type Item struct{ Name string }
	_, err := ValidateDeterministically(Item{Name: "x"}, NewValidateOptions().WithRules("must be tasteful"))
	if err == nil {
		t.Fatal("ValidateDeterministically accepted free-text Rules")
	}
	if !strings.Contains(err.Error(), "ValidateHybrid") {
		t.Fatalf("error = %q, want it to name the escape hatch", err.Error())
	}
}

func TestValidateDeterministicallyRefusesSchemaHints(t *testing.T) {
	type Item struct{ Name string }
	_, err := ValidateDeterministically(Item{Name: "x"}, NewValidateOptions().WithSchemaHints(map[string]string{"Name": "be nice"}))
	if err == nil {
		t.Fatal("ValidateDeterministically accepted SchemaHints")
	}
}

func TestValidateDeterministicallyRefusesUndecidableFieldRule(t *testing.T) {
	type Item struct{ Name string }
	_, err := ValidateDeterministically(Item{Name: "x"},
		NewValidateOptions().WithFieldRules(map[string]string{"Name": "must sound cool"}))
	if err == nil {
		t.Fatal("ValidateDeterministically accepted a field rule Go cannot decide")
	}
}

// TestValidateDeterministicallyPassesWithNoIssues covers the VerdictPass
// path (no consultation of a model, no issues) end to end.
func TestValidateDeterministicallyPassesWithNoIssues(t *testing.T) {
	type Item struct {
		Email string `json:"email"`
	}
	result, err := ValidateDeterministically(Item{Email: "a@example.com"},
		NewValidateOptions().WithFieldRules(map[string]string{"Email": "email"}))
	if err != nil {
		t.Fatalf("ValidateDeterministically: %v", err)
	}
	if result.Verdict != types.VerdictPass {
		t.Fatalf("Verdict = %v, want VerdictPass", result.Verdict)
	}
	if result.ModelConfidence != 0 {
		t.Fatalf("ModelConfidence = %v, want the zero value: no model was consulted", result.ModelConfidence)
	}
}

// --- ValidateHybrid: the error propagation from validateHybridCore.

func TestValidateHybridPropagatesCoreError(t *testing.T) {
	type Item struct{ Name string }
	opts := NewValidateOptions()
	opts.CommonOptions.Threshold = 5 // invalid: CommonOptions.Validate rejects it
	_, err := ValidateHybrid(Item{Name: "x"}, opts)
	if err == nil {
		t.Fatal("ValidateHybrid accepted invalid options")
	}
}

// --- validateResultToJudgment: the two optional-claims branches.

func TestValidateResultToJudgmentCarriesCorrectedAsAClaim(t *testing.T) {
	type Item struct{ Name string }
	corrected := Item{Name: "fixed"}
	vr := ValidateResult[Item]{
		Valid:     false,
		Corrected: &corrected,
		Errors:    []ValidationIssue{{Field: "Name", Message: "bad"}},
	}
	judgment := validateResultToJudgment(Item{Name: "x"}, vr)
	if judgment.Verdict != types.VerdictFail {
		t.Fatalf("Verdict = %v, want VerdictFail", judgment.Verdict)
	}
	if judgment.ModelClaims == nil || judgment.ModelClaims["corrected"] == nil {
		t.Fatalf("ModelClaims = %v, want a corrected claim", judgment.ModelClaims)
	}
}

func TestValidateResultToJudgmentCarriesDeterministicOnlyMetadata(t *testing.T) {
	type Item struct{ Name string }
	vr := ValidateResult[Item]{
		Valid:    true,
		Metadata: map[string]any{"deterministic_only": true},
	}
	judgment := validateResultToJudgment(Item{Name: "x"}, vr)
	if judgment.ModelClaims["deterministic_only"] != true {
		t.Fatalf("ModelClaims = %v, want deterministic_only carried through", judgment.ModelClaims)
	}
}

func TestValidateResultToJudgmentNoClaimsWhenNeitherIsSet(t *testing.T) {
	type Item struct{ Name string }
	vr := ValidateResult[Item]{Valid: true}
	judgment := validateResultToJudgment(Item{Name: "x"}, vr)
	if judgment.ModelClaims != nil {
		t.Fatalf("ModelClaims = %v, want nil when nothing was claimed", judgment.ModelClaims)
	}
}

// --- validateHybridCore (via Validate): FailOn severities and the unknown
// FailOn refusal.

func installValidateResponse(t *testing.T) {
	t.Helper()
	installMiscResponse(t, `{"valid": true, "errors": [], "warnings": [{"field": "Name", "message": "a warning"}], "info": [], "confidence": 0.9,"summary":"s"}`)
}

func TestValidateFailOnWarningTreatsWarningsAsFailure(t *testing.T) {
	installValidateResponse(t)
	type Item struct{ Name string }

	// WithRules matters here and is not decoration: with no Rules, no
	// FieldRules and no SchemaHints, Validate answers deterministically and
	// never calls a provider (see the "Nothing left for the model" shortcut in
	// extended.go). The fake response would go unread and Valid would come back
	// true for a reason that has nothing to do with FailOn -- which is exactly
	// how this test failed when first written.
	opts := NewValidateOptions().WithFailOn("warning").WithRules("the name should look like a name")
	result, err := Validate(Item{Name: "x"}, opts)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Fatal("Valid = true with FailOn=warning and a warning present, want false")
	}
}

// TestValidateFailOnUnknownSeverityIsRefused exercises the refusal, but not
// the switch's own "unknown FailOn" arm in validateHybridCore
// (extended.go, the `default:` case a few lines after the failOn switch
// opens). That arm is unreachable: validateHybridCore's first line calls
// opts.Validate(), and ValidateOptions.Validate already rejects any FailOn
// outside {"", "error", "warning", "info"} before the deterministic pass or
// a model call ever run -- so this refusal is always the earlier check
// firing, never the later one. Left as found; removing the later dead arm
// is a production-code change outside this test file's remit.
func TestValidateFailOnUnknownSeverityIsRefused(t *testing.T) {
	installValidateResponse(t)
	type Item struct{ Name string }

	opts := NewValidateOptions()
	opts.FailOn = "catastrophic"
	_, err := Validate(Item{Name: "x"}, opts)
	if err == nil {
		t.Fatal("Validate accepted an unrecognised FailOn severity")
	}
}

func TestValidateRefusesUnmarshalableData(t *testing.T) {
	_, err := Validate(unmarshalableData{C: make(chan int)}, NewValidateOptions())
	if err == nil {
		t.Fatal("Validate accepted data that cannot be marshaled")
	}
}

// --- Format: the LLM-failure refusal (the happy paths are extended_test.go's).

func TestFormatSurfacesLLMFailure(t *testing.T) {
	installMiscFailure(t)
	_, err := Format(Person{Name: "A", Age: 1}, "a bio")
	if err == nil {
		t.Fatal("Format did not surface the provider's failure")
	}
}

func TestFormatRefusesDataJSONCannotWrite(t *testing.T) {
	// Format used to fall back to fmt.Sprintf("%v", data) on a marshal failure,
	// which prints every field of a struct, one the type leaves out of JSON
	// included. A value JSON cannot write is an invalid request and nothing is
	// sent.
	model := coreAnswer(t, "formatted output")
	out, err := Format(unmarshalableData{C: make(chan int)}, "a bio")
	if types.KindOf(err) != types.KindInvalidRequest || out != "" {
		t.Fatalf("Format = %q, %v; want an invalid request and nothing", out, err)
	}
	if model.count() != 0 {
		t.Errorf("%d requests were made", model.count())
	}
}

// --- Merge: marshal failure, LLM failure, parse failure (the happy/single-
// source/empty-source paths are extended_test.go's).

func TestMergeSurfacesLLMFailure(t *testing.T) {
	installMiscFailure(t)
	_, err := Merge([]Person{{Name: "A"}, {Name: "B"}}, "combine")
	if err == nil {
		t.Fatal("Merge did not surface the provider's failure")
	}
}

func TestMergeRefusesUnparseableResponse(t *testing.T) {
	installMiscResponse(t, "not json")
	_, err := Merge([]Person{{Name: "A"}, {Name: "B"}}, "combine")
	if err == nil {
		t.Fatal("Merge accepted an unparseable response")
	}
}

// --- QuestionOptions.Validate and Question's own refusal paths.

func TestQuestionOptionsValidateRejectsEmptyQuestion(t *testing.T) {
	opts := NewQuestionOptions("   ")
	if err := opts.Validate(); err == nil {
		t.Fatal("QuestionOptions.Validate accepted a whitespace-only question")
	}
}

func TestQuestionOptionsValidateRejectsInvalidCommonOptions(t *testing.T) {
	opts := NewQuestionOptions("a real question")
	opts.CommonOptions.Threshold = 5
	if err := opts.Validate(); err == nil {
		t.Fatal("QuestionOptions.Validate accepted an out-of-range Threshold")
	}
}

func TestQuestionRejectsInvalidOptions(t *testing.T) {
	_, err := Question[Person, string](Person{Name: "A"}, NewQuestionOptions(""))
	if err == nil {
		t.Fatal("Question accepted an empty question")
	}
}

func TestQuestionSurfacesLLMFailure(t *testing.T) {
	installMiscFailure(t)
	_, err := Question[Person, string](Person{Name: "A"}, NewQuestionOptions("how old?"))
	if err == nil {
		t.Fatal("Question did not surface the provider's failure")
	}
}

func TestQuestionRefusesUnmarshalableInputData(t *testing.T) {
	_, err := Question[unmarshalableData, string](unmarshalableData{C: make(chan int)}, NewQuestionOptions("what?"))
	if err == nil {
		t.Fatal("Question accepted input data that cannot be marshaled")
	}
}

func TestQuestionParsesTypedAnswer(t *testing.T) {
	installMiscResponse(t, `{"answer": 42, "confidence": 0.9, "reasoning": "counted directly", "evidence": ["the count field"]}`)

	result, err := Question[Person, int](Person{Name: "A", Age: 42}, NewQuestionOptions("What is the age?"))
	if err != nil {
		t.Fatalf("Question: %v", err)
	}
	if result.Answer != 42 {
		t.Fatalf("Answer = %v, want 42", result.Answer)
	}
	if result.ModelConfidence != 0.9 {
		t.Fatalf("ModelConfidence = %v", result.ModelConfidence)
	}
}
