package ops

import (
	"context"
	"strings"
	"testing"
)

// This file demonstrates the "asks but never checks" pattern in operations
// that were not part of the already-fixed set (Conform, Interpolate, Cluster,
// ValidateLegacy, Summarize, Classify, Choose/Filter/Sort). Each test feeds a
// response that openly violates a constraint the system prompt states, and
// shows the operation returns it as a success anyway.
//
// These tests document defects; they do not fix them. AUDIT-ONLY, per the
// task that produced this file: no production code in this package is
// touched here.

// Negotiate/Settle's MinSatisfaction is stated in the prompt ("Minimum
// acceptable satisfaction: %.0f%%") but never compared against
// OverallSatisfaction in the response. negotiate.go:263-320.
func TestAudit_Settle_MinSatisfactionNeverChecked(t *testing.T) {
	withResponse(t, `{
		"solution": {"weeks": 5},
		"satisfaction": {"budget": 0.1},
		"overall_satisfaction": 0.1,
		"confidence": 0.9
	}`)

	type plan struct {
		Weeks int `json:"weeks"`
	}

	result, err := Settle[plan](map[string]any{"max_budget": 1000}, NegotiateOptions{
		MinSatisfaction: 0.9,
	})
	if err != nil {
		t.Fatalf("Settle returned an error for a response that merely failed the requested floor: %v", err)
	}
	if result.OverallSatisfaction >= 0.9 {
		t.Fatalf("test setup broken: expected a satisfaction below the floor, got %v", result.OverallSatisfaction)
	}
	// This is the defect: MinSatisfaction was asked for in the prompt and the
	// model openly reported failing it (0.1 against a 0.9 floor), and Settle
	// still returned a nil error and a usable NegotiateResult.
	t.Logf("accepted overall_satisfaction=%.2f against a MinSatisfaction floor of 0.90 with no error", result.OverallSatisfaction)
}

// Negotiate/Settle's MaxAlternatives is stated in the prompt ("alternatives
// provides up to %d Pareto-optimal alternatives") but len(Alternatives) is
// never compared against it. negotiate.go:240,297-303.
func TestAudit_Settle_MaxAlternativesNeverChecked(t *testing.T) {
	withResponse(t, `{
		"solution": {"weeks": 5},
		"overall_satisfaction": 0.8,
		"confidence": 0.9,
		"alternatives": [{"weeks": 1}, {"weeks": 2}, {"weeks": 3}, {"weeks": 4}]
	}`)

	type plan struct {
		Weeks int `json:"weeks"`
	}

	result, err := Settle[plan](map[string]any{"max_budget": 1000}, NegotiateOptions{
		MinSatisfaction: 0.5,
		MaxAlternatives: 1,
	})
	if err != nil {
		t.Fatalf("Settle returned an error for a response that merely exceeded the requested alternatives cap: %v", err)
	}
	if len(result.Alternatives) <= 1 {
		t.Fatalf("test setup broken: expected more alternatives than the cap of 1, got %d", len(result.Alternatives))
	}
	t.Logf("accepted %d alternatives against a MaxAlternatives of 1 with no error", len(result.Alternatives))
}

// SemanticMatch's Threshold is stated in the prompt ("Minimum similarity
// threshold: %.2f") but a match's Score is never compared against it before
// being added to result.Matches. match.go:306-325,355-373.
func TestAudit_SemanticMatch_ThresholdNeverChecked(t *testing.T) {
	withResponse(t, `{
		"matches": [
			{"source_index": 0, "target_index": 0, "score": 0.01, "explanation": "barely related"}
		],
		"unmatched_sources": [],
		"unmatched_targets": []
	}`)

	result, err := SemanticMatch([]string{"apple"}, []string{"orange"}, NewMatchOptions().WithThreshold(0.9))
	if err != nil {
		t.Fatalf("SemanticMatch returned an error for a response that merely scored below the requested threshold: %v", err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("test setup broken: expected exactly one match, got %d", len(result.Matches))
	}
	if result.Matches[0].Score >= 0.9 {
		t.Fatalf("test setup broken: expected a score below the 0.9 threshold, got %v", result.Matches[0].Score)
	}
	t.Logf("accepted a match scored %.2f against a Threshold of 0.90 with no error", result.Matches[0].Score)
}

// SemanticMatch's MaxMatches is stated in the prompt ("Maximum %d matches per
// source item") but the count of matches sharing a SourceIndex is never
// compared against it. match.go:270-272,355-373.
func TestAudit_SemanticMatch_MaxMatchesPerSourceNeverChecked(t *testing.T) {
	withResponse(t, `{
		"matches": [
			{"source_index": 0, "target_index": 0, "score": 0.95},
			{"source_index": 0, "target_index": 1, "score": 0.90},
			{"source_index": 0, "target_index": 2, "score": 0.88}
		],
		"unmatched_sources": [],
		"unmatched_targets": []
	}`)

	result, err := SemanticMatch([]string{"apple"}, []string{"a", "b", "c"}, NewMatchOptions().WithMaxMatches(1))
	if err != nil {
		t.Fatalf("SemanticMatch returned an error for a response that merely exceeded MaxMatches per source: %v", err)
	}
	matchesForSource0 := 0
	for _, m := range result.Matches {
		if m.SourceIndex == 0 {
			matchesForSource0++
		}
	}
	if matchesForSource0 <= 1 {
		t.Fatalf("test setup broken: expected more than one match for source 0, got %d", matchesForSource0)
	}
	t.Logf("accepted %d matches for one source item against a MaxMatches of 1 with no error", matchesForSource0)
}

// Critique's MaxIssues is stated in the prompt ("Limit to top %d issues.")
// but len(result.Issues) is never compared against it -- unlike AtMost in
// invariants.go, which exists for exactly this and is not called here.
// critique.go:362-365,421-425.
func TestAudit_Critique_MaxIssuesNeverChecked(t *testing.T) {
	withResponse(t, `{
		"overall_score": 0.4,
		"issues": [
			{"criterion": "clarity", "severity": "minor", "description": "issue 1"},
			{"criterion": "clarity", "severity": "minor", "description": "issue 2"},
			{"criterion": "clarity", "severity": "minor", "description": "issue 3"}
		],
		"summary": "several issues found"
	}`)

	result, err := Critique("some text", NewCritiqueOptions().WithCriteria([]string{"clarity"}).WithMaxIssues(1))
	if err != nil {
		t.Fatalf("Critique returned an error for a response that merely exceeded MaxIssues: %v", err)
	}
	if len(result.Issues) <= 1 {
		t.Fatalf("test setup broken: expected more issues than the MaxIssues cap of 1, got %d", len(result.Issues))
	}
	t.Logf("accepted %d issues against a MaxIssues of 1 with no error", len(result.Issues))
}

// Critique's SeverityFilter is stated in the prompt ("Only report %s
// issues.") but the severity of returned issues is never checked against it.
// critique.go:357-360,421-425.
func TestAudit_Critique_SeverityFilterNeverChecked(t *testing.T) {
	withResponse(t, `{
		"overall_score": 0.4,
		"issues": [
			{"criterion": "clarity", "severity": "minor", "description": "a minor nit, not critical"}
		],
		"summary": "one minor issue"
	}`)

	result, err := Critique("some text", NewCritiqueOptions().WithCriteria([]string{"clarity"}).WithSeverityFilter("critical"))
	if err != nil {
		t.Fatalf("Critique returned an error for a response that ignored SeverityFilter: %v", err)
	}
	if len(result.Issues) != 1 || result.Issues[0].Severity != "minor" {
		t.Fatalf("test setup broken: expected one minor-severity issue, got %+v", result.Issues)
	}
	t.Logf("accepted a %q-severity issue against a SeverityFilter of %q with no error", result.Issues[0].Severity, "critical")
}

// Compress's MaxOutputSize is documented as a hard cap ("Maximum output
// tokens/words") and interpolated into the prompt as a target size, but the
// compressed output's actual size is never compared against it -- unlike
// WithinLength in invariants.go, which exists for exactly this and is not
// called here. compress.go:216-219,306-334.
func TestAudit_Compress_MaxOutputSizeNeverChecked(t *testing.T) {
	longOutput := strings.Repeat("word ", 500) // ~2500 characters
	withResponse(t, `{"compressed": "`+longOutput+`"}`)

	result, err := Compress("some long input text that should be compressed down", NewCompressOptions().WithMaxOutputSize(20))
	if err != nil {
		t.Fatalf("Compress returned an error for output that merely exceeded MaxOutputSize: %v", err)
	}
	if result.CompressedSize <= 20 {
		t.Fatalf("test setup broken: expected compressed output over the 20-unit cap, got %d", result.CompressedSize)
	}
	t.Logf("accepted %d units of compressed output against a MaxOutputSize of 20 with no error", result.CompressedSize)
}

// Compress's RetainFields is stated in the prompt ("Must retain these
// fields/information") but the compressed output is never checked to
// actually contain them. compress.go:223-225,306-334.
func TestAudit_Compress_RetainFieldsNeverChecked(t *testing.T) {
	type record struct {
		SSN  string `json:"ssn"`
		Name string `json:"name"`
	}
	withResponse(t, `{"compressed": {"name": "Alice"}}`)

	result, err := Compress(record{SSN: "111-22-3333", Name: "Alice"},
		NewCompressOptions().WithRetainFields([]string{"ssn"}))
	if err != nil {
		t.Fatalf("Compress returned an error for output that dropped a RetainFields entry: %v", err)
	}
	if result.Compressed.SSN != "" {
		t.Fatalf("test setup broken: expected ssn to be dropped, got %q", result.Compressed.SSN)
	}
	t.Logf("accepted compressed output with the required field %q entirely absent, no error", "ssn")
}

// Arbitrate's RequireAllRules is stated in the prompt ("disqualify any
// option that fails ANY rule") but the winner's own Evaluations entry is
// never checked for Disqualified before it is returned as the winner.
// arbitrate.go:235-238,313-329.
func TestAudit_Arbitrate_DisqualifiedWinnerNeverChecked(t *testing.T) {
	withResponse(t, `{
		"winner_index": 1,
		"scores": {"0": 0.9, "1": 0.95},
		"evaluations": [
			{"index": 0, "total_score": 0.9, "disqualified": false},
			{"index": 1, "total_score": 0.95, "disqualified": true, "disqualify_reason": "failed rule 1"}
		],
		"reasoning": "option 1 scored highest",
		"confidence": 0.8
	}`)

	result, err := Arbitrate([]string{"option A", "option B"}, ArbitrateOptions{
		Rules:           []string{"must satisfy rule 1"},
		RequireAllRules: true,
	})
	if err != nil {
		t.Fatalf("Arbitrate returned an error for a response that named its own winner disqualified: %v", err)
	}
	if result.WinnerIndex != 1 {
		t.Fatalf("test setup broken: expected winner index 1, got %d", result.WinnerIndex)
	}
	var winnerDisqualified bool
	for _, e := range result.Evaluations {
		if e.Index == result.WinnerIndex {
			winnerDisqualified = e.Disqualified
		}
	}
	if !winnerDisqualified {
		t.Fatalf("test setup broken: expected the winner's own evaluation to report disqualified=true")
	}
	t.Logf("accepted option %d as the winner under RequireAllRules while its own evaluation reported disqualified=true, no error", result.WinnerIndex)
}

// Enrich's AddOnly value-mutation gap (a field the model overwrites was
// invisible to the name-set diff AddedFields used) was found during this
// audit and has since been fixed directly in enrich.go (changedExistingField,
// checked when AddOnly is set) while this file was being written -- the
// production fix and this audit file are running concurrently. No test for
// it remains here because there is no live defect left to demonstrate; see
// the finding note in the task report instead.

// RedactLLM's Categories option is documented as "Categories to detect", and
// buildRedactSystemPrompt only lists the categories the caller asked for
// (redact_llm.go:222-250) -- but parseRedactResponse (redact_llm.go:272-326)
// never checks a returned span's Category against opts.Categories before
// RedactLLM applies it (redact_llm.go:200-205). A caller who restricted
// detection to avoid touching a field they need intact (e.g. requesting only
// "email" so a "name" stays usable for personalization) can have that field
// redacted anyway because the model tagged it with a category nobody asked
// for, with a nil error.
func TestAudit_RedactLLM_CategoriesNotEnforced(t *testing.T) {
	text := "Contact John Smith at john@example.com for details."
	// The caller asked only for "email"; the model answers with an extra
	// "name" span nobody requested.
	withResponse(t, `{"spans":[
		{"text":"john@example.com","start":22,"category":"email"},
		{"text":"John Smith","start":8,"category":"name"}
	]}`)

	opts := NewRedactLLMOptions().WithCategories([]string{"email"})
	result, err := RedactLLM(context.Background(), text, opts)
	if err != nil {
		t.Fatalf("RedactLLM returned an error for a response that merely included an unrequested category: %v", err)
	}
	if strings.Contains(result.Text, "John Smith") {
		t.Fatalf("test setup broken: expected the unrequested 'name' span to have been redacted, proving the defect; got text=%q", result.Text)
	}
	if _, found := result.Categories["name"]; !found {
		t.Fatalf("test setup broken: expected result.Categories to record the unrequested 'name' category, got %v", result.Categories)
	}
	t.Logf("Categories was set to [\"email\"] only, and the answer still redacted a \"name\" span (text now %q) with a nil error", result.Text)
}

// A claim citing source 7 out of a three-source list is citing nothing. The
// indices are the model's, but the bound is the caller's, so this compares a
// claim against a fact rather than against another claim -- and it was not
// being compared at all.
func TestVerify_RefusesAClaimCitingASourceThatDoesNotExist(t *testing.T) {
	withResponse(t, `{
		"claims": [{"claim": "the sky is blue", "verdict": "verified", "confidence": 0.95, "sources": [7]}],
		"overall_verdict": "verified",
		"overall_confidence": 0.95
	}`)

	opts := NewVerifyOptions()
	opts.Sources = []any{"one source"}
	_, err := Verify("the sky is blue", opts)
	if err == nil {
		t.Fatal("Verify accepted a claim citing source 7 out of a one-source list")
	}
	if !strings.Contains(err.Error(), "source 7") {
		t.Fatalf("the error does not name the bad index: %v", err)
	}
}

// Decompose's MaxDepth is stated in the prompt as a hard ceiling ("Maximum
// depth: %d", decompose.go:277,298) -- not phrased as a target the way
// TargetParts is ("Target approximately %d parts.", decompose.go:246) -- but
// the returned parts' Depth values are only used to compute
// result.MaxDepth (the observed maximum), never compared against
// opts.MaxDepth (decompose.go:370-377). A response nesting parts twice as
// deep as requested is returned as success.
func TestAudit_Decompose_MaxDepthNeverChecked(t *testing.T) {
	type step struct {
		Label string `json:"label"`
	}
	withResponse(t, `{
		"parts": [
			{"id": "a", "name": "root", "content": {"label": "root"}, "depth": 0, "order": 1},
			{"id": "b", "name": "child", "content": {"label": "child"}, "parent_id": "a", "depth": 1, "order": 2},
			{"id": "c", "name": "grandchild", "content": {"label": "grandchild"}, "parent_id": "b", "depth": 2, "order": 3},
			{"id": "d", "name": "great-grandchild", "content": {"label": "gg"}, "parent_id": "c", "depth": 3, "order": 4}
		],
		"root_parts": ["a"]
	}`)

	result, err := Decompose(step{Label: "some complex task"}, NewDecomposeOptions().WithMaxDepth(1))
	if err != nil {
		t.Fatalf("Decompose returned an error for a response that merely exceeded MaxDepth: %v", err)
	}
	if result.MaxDepth <= 1 {
		t.Fatalf("test setup broken: expected an observed max depth greater than the MaxDepth cap of 1, got %d", result.MaxDepth)
	}
	t.Logf("accepted parts nested to depth %d against a MaxDepth of 1 with no error", result.MaxDepth)
}

// Suggest's TopN is stated in the prompt as a request ("Return top %d
// suggestions", suggest.go:204), and unlike AtMost in invariants.go -- built
// for exactly this -- an over-limit answer is truncated in place rather than
// rejected (suggest.go:261-262,274-275,305-306). AGENTS.md's own AtMost
// comment names this failure mode directly: silent truncation "turns a model
// that ignored the instruction into a result that looks obedient," which
// means a caller has no way to notice the model is routinely ignoring TopN
// short of counting on every call themselves.
func TestAudit_Suggest_OverLimitAnswerTruncatedNotRejected(t *testing.T) {
	withResponse(t, `{"suggestions": ["a", "b", "c", "d", "e"]}`)

	result, err := Suggest[string]("some input", NewSuggestOptions().WithTopN(2))
	if err != nil {
		t.Fatalf("Suggest returned an error for a response that merely exceeded TopN: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("test setup broken: expected the truncated result to have exactly TopN=2 entries, got %d: %v", len(result), result)
	}
	t.Logf("the model returned 5 suggestions against a TopN of 2; Suggest silently truncated to %v with no error, so a model that routinely ignores TopN is indistinguishable from one that honors it", result)
}

// Score's ScaleMin/ScaleMax are stated in the prompt three times ("Score
// Range: %.1f to %.1f", "Assign an overall score between %.1f and %.1f",
// '"value": overall score (number between %.1f and %.1f)') -- not phrased as
// a hint. It used to be clamped after decode rather than checked, which was
// worse than not checking at all: a model that defaults to a 0-100 scale
// against a caller's 0-10 request returned 95, which clamped to Value: 10,
// NormalizedValue: 1.0 -- a perfect score, not an error, with nothing anywhere
// recording that the model had ignored the scale.
//
// It now refuses. This test pins the refusal.
func TestScore_RefusesAValueOutsideTheRequestedScale(t *testing.T) {
	withResponse(t, `{"value": 95, "breakdown": {}, "reasoning": "great"}`)

	opts := NewScoreOptions()
	opts.ScaleMin = 0
	opts.ScaleMax = 10
	_, err := Score("some input", opts)
	if err == nil {
		t.Fatal("Score accepted 95 against a requested 0-10 scale")
	}
	if !strings.Contains(err.Error(), "scale") {
		t.Fatalf("error does not name the scale violation: %v", err)
	}
}

// The companion case: a value inside the scale still goes through untouched,
// which is what makes the refusal above a real check rather than a blanket
// rejection of everything.
func TestScore_AcceptsAValueInsideTheRequestedScale(t *testing.T) {
	withResponse(t, `{"value": 7, "breakdown": {}, "reasoning": "good"}`)

	opts := NewScoreOptions()
	opts.ScaleMin = 0
	opts.ScaleMax = 10
	result, err := Score("some input", opts)
	if err != nil {
		t.Fatalf("Score refused an in-range value: %v", err)
	}
	if result.Value != 7 {
		t.Fatalf("Value = %v, want 7", result.Value)
	}
	if result.NormalizedValue != 0.7 {
		t.Fatalf("NormalizedValue = %v, want 0.7", result.NormalizedValue)
	}
}

// ScoreOptions.Weights was declared, exported, documented, and read by
// nothing -- not the prompt, not the decode. dead_options_test.go could not
// catch it: TestNoDeadOptionFields matches by bare field name across every
// *Options type (its own comment documents this as a deliberate, conservative
// trade-off), and ArbitrateOptions.Weights is a real, read field with the same
// name, so the walk counted arbitrate.go's use as evidence that this one was
// live too. These two tests cover the blind spot directly, by name.
func TestScore_WeightsReachThePrompt(t *testing.T) {
	opts := NewScoreOptions()
	opts.Criteria = []string{"clarity", "depth"}
	opts.Weights = map[string]float64{"clarity": 3, "depth": 1}

	prompts := capturePrompts(t, func() {
		_, _ = Score("some input", opts)
	})
	if len(prompts) == 0 {
		t.Fatal("Score rendered no prompt")
	}
	if !strings.Contains(prompts[0], "Criterion weights") {
		t.Fatalf("the weights section is absent from the prompt: %s", prompts[0])
	}
	if !strings.Contains(prompts[0], `"clarity":3`) {
		t.Fatalf("the clarity weight is absent from the prompt: %s", prompts[0])
	}
}

// A weight naming a criterion that is not being scored is a caller typo, and
// it is checkable here without asking the model anything -- so it is refused
// rather than silently dropped.
func TestScore_RefusesAWeightForAnUnscoredCriterion(t *testing.T) {
	withResponse(t, `{"value": 5, "reasoning": "ok"}`)

	opts := NewScoreOptions()
	opts.Criteria = []string{"clarity"}
	opts.Weights = map[string]float64{"claritiy": 3} // transposed letters

	_, err := Score("some input", opts)
	if err == nil {
		t.Fatal("Score accepted a weight for a criterion it was never asked to score")
	}
	if !strings.Contains(err.Error(), "claritiy") {
		t.Fatalf("the error does not name the offending key: %v", err)
	}
}

// ChooseOptions.TopN used to be validated as >= 1 and, when > 1, turned into
// a prompt instruction ("Return top %d options"). But Choose returns a single
// T, and the system prompt it actually sends hard-codes a single-answer format
// regardless: "Select the single most appropriate option", `Return ONLY a JSON
// object {"id": "..."}`. TopN=3 produced an internally contradictory prompt,
// and whatever the model returned, Choose read exactly one id and had nowhere
// to put the other two. It is now refused at validation, with a pointer at the
// operation that does return an ordered list.
func TestChoose_RefusesATopNItCannotReturn(t *testing.T) {
	withResponse(t, `{"id": "b"}`)

	_, err := Choose([]string{"a", "b", "c"}, NewChooseOptions().WithTopN(3))
	if err == nil {
		t.Fatal("Choose accepted TopN=3 despite returning a single value")
	}
	if !strings.Contains(err.Error(), "Rank") {
		t.Fatalf("the error does not point at the operation that can honor it: %v", err)
	}
}

// Transform's PreserveFields reached the prompt as "Preserve these fields: ..."
// and was checked nowhere, so a field the caller pinned could come back changed
// with a nil error. The case that motivates this option is an id, and a
// silently regenerated id is a corrupted record.
func TestTransform_RefusesAChangeToAPreservedField(t *testing.T) {
	type source struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type target struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	withResponse(t, `{"id":"generated-999","name":"Alice"}`)

	opts := NewTransformOptions()
	opts.PreserveFields = []string{"id"}
	_, err := Transform[source, target](source{ID: "abc123", Name: "Alice"}, opts)
	if err == nil {
		t.Fatal("Transform accepted a changed id despite PreserveFields naming it")
	}
	if !strings.Contains(err.Error(), "id") {
		t.Fatalf("the error does not name the field: %v", err)
	}
}

// The companion case: the same field, preserved. Without it the check above is
// indistinguishable from refusing every transform that names PreserveFields.
func TestTransform_AcceptsAnUnchangedPreservedField(t *testing.T) {
	type source struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type target struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	withResponse(t, `{"id":"abc123","name":"ALICE"}`)

	opts := NewTransformOptions()
	opts.PreserveFields = []string{"id"}
	got, err := Transform[source, target](source{ID: "abc123", Name: "Alice"}, opts)
	if err != nil {
		t.Fatalf("Transform refused an unchanged preserved field: %v", err)
	}
	if got.ID != "abc123" || got.Name != "ALICE" {
		t.Fatalf("Transform = %+v, want the id preserved and the name reshaped", got)
	}
}

// A named field the target type does not carry is not a violation. Transform's
// whole job is reshaping, and a field that does not survive the target type is
// the caller's schema speaking, not the model ignoring an instruction.
func TestTransform_PreservedFieldAbsentFromTheTargetTypeIsNotAViolation(t *testing.T) {
	type source struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type target struct {
		Name string `json:"name"`
	}
	withResponse(t, `{"name":"Alice"}`)

	opts := NewTransformOptions()
	opts.PreserveFields = []string{"id"}
	if _, err := Transform[source, target](source{ID: "abc123", Name: "Alice"}, opts); err != nil {
		t.Fatalf("Transform refused a target type that simply has no id field: %v", err)
	}
}

// Rewrite's AvoidWords and IncludeWords reached the prompt as "Avoid: ..." and
// "Include: ..." and were checked nowhere -- even though ExcludesValues already
// existed in invariants.go for the first half. Both are decidable against the
// answer without asking the model anything: the word is either in the text or
// it is not.
func TestRewrite_RefusesTextContainingAnAvoidedWord(t *testing.T) {
	withResponse(t, "This is a cheap solution.")

	opts := NewRewriteOptions()
	opts.AvoidWords = []string{"cheap"}
	_, err := Rewrite("Describe this product.", opts)
	if err == nil {
		t.Fatal("Rewrite accepted text containing a word AvoidWords ruled out")
	}
	if !strings.Contains(err.Error(), "cheap") {
		t.Fatalf("the error does not name the offending word: %v", err)
	}
}

func TestRewrite_RefusesTextOmittingARequiredWord(t *testing.T) {
	withResponse(t, "This is a low-cost solution.")

	opts := NewRewriteOptions()
	opts.IncludeWords = []string{"affordable"}
	_, err := Rewrite("Describe this product.", opts)
	if err == nil {
		t.Fatal("Rewrite accepted text omitting a word IncludeWords required")
	}
	if !strings.Contains(err.Error(), "affordable") {
		t.Fatalf("the error does not name the missing word: %v", err)
	}
}

// The match is on word boundaries, not substrings. A caller banning "cat" is
// banning the word; refusing "category" would make the option unusable.
func TestRewrite_AvoidWordsMatchesWholeWordsNotSubstrings(t *testing.T) {
	withResponse(t, "sorted into a category")

	opts := NewRewriteOptions()
	opts.AvoidWords = []string{"cat"}
	got, err := Rewrite("sorted into a group", opts)
	if err != nil {
		t.Fatalf("Rewrite refused \"category\" for containing the substring \"cat\": %v", err)
	}
	if got != "sorted into a category" {
		t.Fatalf("Rewrite = %q", got)
	}
}

// Case is not an escape hatch: a caller who bans "Bob" does not want "bob".
func TestRewrite_AvoidWordsIsCaseInsensitive(t *testing.T) {
	withResponse(t, "ask bob about it")

	opts := NewRewriteOptions()
	opts.AvoidWords = []string{"Bob"}
	if _, err := Rewrite("ask someone about it", opts); err == nil {
		t.Fatal("Rewrite accepted a differently-cased spelling of an avoided word")
	}
}

// RewriteWithMetadata decodes through a different path than Rewrite and had to
// grow the same check. Without this, half the operation is uncovered.
func TestRewriteWithMetadata_RefusesTextContainingAnAvoidedWord(t *testing.T) {
	withResponse(t, `{"text": "our cheap offering", "changes_made": [], "tone_achieved": "casual", "confidence": 0.9}`)

	opts := NewRewriteOptions()
	opts.AvoidWords = []string{"cheap"}
	if _, err := RewriteWithMetadata("our low-cost offering", opts); err == nil {
		t.Fatal("RewriteWithMetadata accepted text containing an avoided word")
	}
}

// The "authoritative" strategy documents exactly one thing -- prefer the source
// at AuthoritativeSource -- and each Conflict reports the index it actually
// chose. That is two integers, comparable in Go with no model claim in between,
// the same shape as Choose's MemberOf check over ids. Nothing was comparing
// them, so the strategy was prose in a prompt and nothing else.
func TestResolve_RefusesAConflictResolvedAgainstTheAuthoritativeSource(t *testing.T) {
	withResponse(t, `{
		"resolved": {"name": "from source 1"},
		"conflicts": [
			{"field": "name", "values": {"0": "from source 0", "1": "from source 1"}, "resolution": "picked", "chosen_source": 1, "chosen_value": "from source 1"}
		],
		"source_contributions": {"1": ["name"]},
		"confidence": 0.9
	}`)

	type record struct {
		Name string `json:"name"`
	}
	opts := ResolveOptions{Strategy: "authoritative", AuthoritativeSource: 0}
	_, err := Resolve([]record{{Name: "from source 0"}, {Name: "from source 1"}}, opts)
	if err == nil {
		t.Fatal("Resolve accepted a conflict resolved from source 1 under an authoritative strategy naming source 0")
	}
	if !strings.Contains(err.Error(), "authoritative") {
		t.Fatalf("the error does not name the violated strategy: %v", err)
	}
}

// The companion case: the same strategy, honored. Without this the check above
// is indistinguishable from refusing every authoritative resolution.
func TestResolve_AcceptsAConflictResolvedFromTheAuthoritativeSource(t *testing.T) {
	withResponse(t, `{
		"resolved": {"name": "from source 0"},
		"conflicts": [
			{"field": "name", "values": {"0": "from source 0", "1": "from source 1"}, "resolution": "picked", "chosen_source": 0, "chosen_value": "from source 0"}
		],
		"source_contributions": {"0": ["name"]},
		"confidence": 0.9
	}`)

	type record struct {
		Name string `json:"name"`
	}
	opts := ResolveOptions{Strategy: "authoritative", AuthoritativeSource: 0}
	result, err := Resolve([]record{{Name: "from source 0"}, {Name: "from source 1"}}, opts)
	if err != nil {
		t.Fatalf("Resolve refused a conflict resolved from the authoritative source: %v", err)
	}
	if result.Resolved.Name != "from source 0" {
		t.Fatalf("Resolved = %+v, want source 0's value", result.Resolved)
	}
}

// A conflict citing a source index outside the supplied list resolved from
// nothing, under any strategy. The index is the model's; the bound is the
// caller's.
func TestResolve_RefusesAConflictCitingASourceThatDoesNotExist(t *testing.T) {
	withResponse(t, `{
		"resolved": {"name": "from nowhere"},
		"conflicts": [
			{"field": "name", "values": {"0": "a"}, "resolution": "picked", "chosen_source": 4, "chosen_value": "from nowhere"}
		],
		"source_contributions": {},
		"confidence": 0.9
	}`)

	type record struct {
		Name string `json:"name"`
	}
	_, err := Resolve([]record{{Name: "a"}, {Name: "b"}}, ResolveOptions{Strategy: "merge"})
	if err == nil {
		t.Fatal("Resolve accepted a conflict resolved from source 4 out of a two-source list")
	}
	if !strings.Contains(err.Error(), "source 4") {
		t.Fatalf("the error does not name the bad index: %v", err)
	}
}

// The confidence floor is per claim, which is how the prompt states it. Only
// the aggregate was checked, so a claim could be marked verified at 0.1 as long
// as the summary number cleared the floor -- and the summary number is a
// separate model claim, so the two do not constrain each other at all.
func TestVerify_RefusesAClaimVerifiedBelowTheFloor(t *testing.T) {
	withResponse(t, `{
		"claims": [{"claim": "a shaky one", "verdict": "verified", "confidence": 0.1}],
		"overall_verdict": "verified",
		"overall_confidence": 0.95
	}`)

	_, err := Verify("a shaky one", NewVerifyOptions().WithMinConfidence(0.7))
	if err == nil {
		t.Fatal("Verify accepted a claim marked verified at confidence 0.1 against a 0.7 floor")
	}
	if !strings.Contains(err.Error(), "claim 0") {
		t.Fatalf("the error does not name the offending claim: %v", err)
	}
}

// The companion case that makes the check above a real gate rather than a
// blanket rejection: a claim the model reports as unverifiable is not asserting
// anything the floor protects, so a low confidence on it is not a violation.
func TestVerify_DoesNotHoldANonVerifiedClaimToTheFloor(t *testing.T) {
	withResponse(t, `{
		"claims": [{"claim": "unknowable", "verdict": "unverifiable", "confidence": 0.1}],
		"overall_verdict": "unverifiable",
		"overall_confidence": 0.95
	}`)

	result, err := Verify("unknowable", NewVerifyOptions().WithMinConfidence(0.7))
	if err != nil {
		t.Fatalf("Verify refused a low-confidence unverifiable claim: %v", err)
	}
	if len(result.Claims) != 1 {
		t.Fatalf("Claims = %+v, want the one claim surfaced", result.Claims)
	}
}
