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

// Settle/Negotiate's MinSatisfaction was stated in the prompt as "Minimum
// acceptable satisfaction: %.0f%%" and compared against nothing, so a model
// that openly reported failing it -- 0.1 against a 0.9 floor -- came back as a
// usable result with a nil error. The number is the model's claim, but the
// floor is the caller's.
func TestSettle_RefusesASolutionBelowTheSatisfactionFloor(t *testing.T) {
	withResponse(t, `{
		"solution": {"weeks": 5},
		"satisfaction": {"budget": 0.1},
		"overall_satisfaction": 0.1,
		"confidence": 0.9
	}`)

	type plan struct {
		Weeks int `json:"weeks"`
	}

	_, err := Settle[plan](map[string]any{"max_budget": 1000}, NegotiateOptions{MinSatisfaction: 0.9})
	if err == nil {
		t.Fatal("Settle accepted overall satisfaction 0.1 against a 0.9 floor")
	}
	if !strings.Contains(err.Error(), "satisfaction") {
		t.Fatalf("the error does not name the floor it failed: %v", err)
	}
}

// MaxAlternatives was described in the prompt as "up to %d" and never counted.
func TestSettle_RefusesMoreAlternativesThanRequested(t *testing.T) {
	withResponse(t, `{
		"solution": {"weeks": 5},
		"overall_satisfaction": 0.8,
		"confidence": 0.9,
		"alternatives": [{"weeks": 1}, {"weeks": 2}, {"weeks": 3}, {"weeks": 4}]
	}`)

	type plan struct {
		Weeks int `json:"weeks"`
	}

	_, err := Settle[plan](map[string]any{"max_budget": 1000}, NegotiateOptions{
		MinSatisfaction: 0.5,
		MaxAlternatives: 1,
	})
	if err == nil {
		t.Fatal("Settle accepted 4 alternatives against a MaxAlternatives of 1")
	}
}

// The companion case: inside both constraints, the solution comes back.
func TestSettle_AcceptsASolutionInsideBothConstraints(t *testing.T) {
	withResponse(t, `{
		"solution": {"weeks": 5},
		"overall_satisfaction": 0.8,
		"confidence": 0.9,
		"alternatives": [{"weeks": 1}]
	}`)

	type plan struct {
		Weeks int `json:"weeks"`
	}

	result, err := Settle[plan](map[string]any{"max_budget": 1000}, NegotiateOptions{
		MinSatisfaction: 0.5,
		MaxAlternatives: 2,
	})
	if err != nil {
		t.Fatalf("Settle refused a solution inside both constraints: %v", err)
	}
	if result.Solution.Weeks != 5 {
		t.Fatalf("Solution = %+v, want the scripted plan", result.Solution)
	}
}

// SemanticMatch's Threshold was stated in the prompt as "Minimum similarity
// threshold: %.2f" and compared against nothing, so a pair scored 0.01 against
// a 0.9 threshold was returned as a match. A caller who sets a threshold is
// deciding what counts as a match at all.
func TestSemanticMatch_RefusesAPairBelowTheThreshold(t *testing.T) {
	withResponse(t, `{
		"matches": [
			{"source_index": 0, "target_index": 0, "score": 0.01, "explanation": "barely related"}
		],
		"unmatched_sources": [],
		"unmatched_targets": []
	}`)

	_, err := SemanticMatch([]string{"apple"}, []string{"orange"}, NewMatchOptions().WithThreshold(0.9))
	if err == nil {
		t.Fatal("SemanticMatch returned a pair scored 0.01 against a 0.9 threshold")
	}
	if !strings.Contains(err.Error(), "threshold") {
		t.Fatalf("the error does not name the threshold: %v", err)
	}
}

// MaxMatches is stated per source item ("Maximum %d matches per source item"),
// so the count is per SourceIndex rather than over the whole list.
func TestSemanticMatch_RefusesMoreMatchesForOneSourceThanRequested(t *testing.T) {
	withResponse(t, `{
		"matches": [
			{"source_index": 0, "target_index": 0, "score": 0.95},
			{"source_index": 0, "target_index": 1, "score": 0.90},
			{"source_index": 0, "target_index": 2, "score": 0.88}
		],
		"unmatched_sources": [],
		"unmatched_targets": []
	}`)

	_, err := SemanticMatch([]string{"apple"}, []string{"a", "b", "c"}, NewMatchOptions().WithMaxMatches(1))
	if err == nil {
		t.Fatal("SemanticMatch returned 3 matches for one source against a MaxMatches of 1")
	}
	if !strings.Contains(err.Error(), "source 0") {
		t.Fatalf("the error does not name the offending source: %v", err)
	}
}

// The per-source count is genuinely per source: two sources with one match each
// is not a violation of MaxMatches(1), even though the list holds two pairs.
func TestSemanticMatch_MaxMatchesCountsPerSourceNotOverTheWholeList(t *testing.T) {
	withResponse(t, `{
		"matches": [
			{"source_index": 0, "target_index": 0, "score": 0.95},
			{"source_index": 1, "target_index": 1, "score": 0.90}
		],
		"unmatched_sources": [],
		"unmatched_targets": []
	}`)

	result, err := SemanticMatch([]string{"apple", "pear"}, []string{"a", "b"},
		NewMatchOptions().WithMaxMatches(1).WithThreshold(0.5))
	if err != nil {
		t.Fatalf("SemanticMatch refused one match per source under MaxMatches(1): %v", err)
	}
	if len(result.Matches) != 2 {
		t.Fatalf("Matches = %+v, want both pairs", result.Matches)
	}
}

// Critique's MaxIssues was rendered into the prompt as "Limit to top %d
// issues." and compared against nothing, even though AtMost exists in
// invariants.go for exactly this. A caller who caps the list is sizing
// something downstream, and silently getting more is what the cap was for.
func TestCritique_RefusesMoreIssuesThanRequested(t *testing.T) {
	withResponse(t, `{
		"overall_score": 0.4,
		"issues": [
			{"criterion": "clarity", "severity": "minor", "description": "issue 1"},
			{"criterion": "clarity", "severity": "minor", "description": "issue 2"},
			{"criterion": "clarity", "severity": "minor", "description": "issue 3"}
		],
		"summary": "several issues found"
	}`)

	_, err := Critique("some text", NewCritiqueOptions().WithCriteria([]string{"clarity"}).WithMaxIssues(1))
	if err == nil {
		t.Fatal("Critique accepted 3 issues against a MaxIssues of 1")
	}
}

// SeverityFilter narrows what the caller wants reported; an issue carrying a
// different severity is an answer to a question that was not asked.
func TestCritique_RefusesAnIssueOutsideTheRequestedSeverity(t *testing.T) {
	withResponse(t, `{
		"overall_score": 0.4,
		"issues": [
			{"criterion": "clarity", "severity": "minor", "description": "a minor nit, not critical"}
		],
		"summary": "one minor issue"
	}`)

	_, err := Critique("some text", NewCritiqueOptions().WithCriteria([]string{"clarity"}).WithSeverityFilter("critical"))
	if err == nil {
		t.Fatal("Critique accepted a minor issue against a SeverityFilter of \"critical\"")
	}
	if !strings.Contains(err.Error(), "minor") {
		t.Fatalf("the error does not name the offending severity: %v", err)
	}
}

// The companion cases: within the cap and inside the filter, both still pass.
func TestCritique_AcceptsIssuesWithinBothConstraints(t *testing.T) {
	withResponse(t, `{
		"overall_score": 0.4,
		"issues": [
			{"criterion": "clarity", "severity": "critical", "description": "a real problem"}
		],
		"summary": "one critical issue"
	}`)

	result, err := Critique("some text",
		NewCritiqueOptions().WithCriteria([]string{"clarity"}).WithMaxIssues(2).WithSeverityFilter("critical"))
	if err != nil {
		t.Fatalf("Critique refused a response inside both constraints: %v", err)
	}
	if len(result.Issues) != 1 {
		t.Fatalf("Issues = %+v, want the one issue surfaced", result.Issues)
	}
}

// An issue with no severity label is not matched against the filter: refusing
// it would discard a real finding over a missing string.
func TestCritique_UnlabeledSeverityIsNotAFilterViolation(t *testing.T) {
	withResponse(t, `{
		"overall_score": 0.4,
		"issues": [{"criterion": "clarity", "description": "no severity given"}],
		"summary": "one issue"
	}`)

	if _, err := Critique("some text",
		NewCritiqueOptions().WithCriteria([]string{"clarity"}).WithSeverityFilter("critical")); err != nil {
		t.Fatalf("Critique refused an issue that simply carried no severity label: %v", err)
	}
}

// Compress's MaxOutputSize is documented as a maximum and was compared against
// nothing, even though Compress had already measured the output two lines
// earlier. A caller sizing a context window by it got no signal when the answer
// did not fit.
func TestCompress_RefusesOutputAboveTheRequestedMaximum(t *testing.T) {
	longOutput := strings.Repeat("word ", 500) // ~2500 characters
	withResponse(t, `{"compressed": "`+longOutput+`"}`)

	_, err := Compress("some long input text that should be compressed down",
		NewCompressOptions().WithMaxOutputSize(20))
	if err == nil {
		t.Fatal("Compress accepted ~2500 characters against a MaxOutputSize of 20")
	}
	if !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("the error does not name the cap: %v", err)
	}
}

// The companion case, and the one that pins the unit: MaxOutputSize counts
// characters, matching what the prompt states and what CompressedSize measures.
// It used to be documented as "tokens/words", which agreed with neither -- and
// since nothing enforced it, the disagreement never surfaced.
func TestCompress_AcceptsOutputInsideTheRequestedMaximum(t *testing.T) {
	withResponse(t, `{"compressed": "short"}`)

	result, err := Compress("some long input text that should be compressed down",
		NewCompressOptions().WithMaxOutputSize(20))
	if err != nil {
		t.Fatalf("Compress refused output inside the cap: %v", err)
	}
	if result.CompressedSize > 20 {
		t.Fatalf("CompressedSize = %d, which is not inside the cap this test claims to exercise", result.CompressedSize)
	}
}

// CompressionRatio is deliberately NOT enforced: the prompt states it as
// "approximately", it is a target rather than a bound, and beating it is a
// better answer, not a violation.
func TestCompress_BeatingTheCompressionRatioIsNotAViolation(t *testing.T) {
	withResponse(t, `{"compressed": "x"}`)

	input := strings.Repeat("verbose input text ", 20)
	if _, err := Compress(input, NewCompressOptions().WithCompressionRatio(0.5)); err != nil {
		t.Fatalf("Compress refused an answer that compressed further than the target ratio: %v", err)
	}
}

// RetainFields is stated in the prompt as "Must retain these
// fields/information" -- "must", not "prefer" -- and a named field missing from
// the compressed output is the one outcome the option exists to prevent.
func TestCompress_RefusesOutputMissingARetainedField(t *testing.T) {
	type record struct {
		SSN  string `json:"ssn"`
		Note string `json:"note"`
	}
	withResponse(t, `{"compressed": {"note": "kept the wrong one"}}`)

	_, err := Compress(record{SSN: "111-11-1111", Note: "some note"},
		NewCompressOptions().WithRetainFields([]string{"ssn"}))
	if err == nil {
		t.Fatal("Compress accepted output missing a field named in RetainFields")
	}
	if !strings.Contains(err.Error(), "ssn") {
		t.Fatalf("the error does not name the dropped field: %v", err)
	}
}

func TestCompress_AcceptsOutputCarryingEveryRetainedField(t *testing.T) {
	type record struct {
		SSN  string `json:"ssn"`
		Note string `json:"note"`
	}
	withResponse(t, `{"compressed": {"ssn": "111-11-1111"}}`)

	result, err := Compress(record{SSN: "111-11-1111", Note: "some long note"},
		NewCompressOptions().WithRetainFields([]string{"ssn"}))
	if err != nil {
		t.Fatalf("Compress refused output that kept the retained field: %v", err)
	}
	if result.Compressed.SSN != "111-11-1111" {
		t.Fatalf("Compressed = %+v, want the retained field intact", result.Compressed)
	}
}

// Only structured output is checked. When the compressed value is not a JSON
// object there are no named fields to look for, and RetainFields is documented
// as being for structured data -- refusing here would break every text
// compression that happens to set the option.
func TestCompress_RetainFieldsIsNotCheckedAgainstUnstructuredOutput(t *testing.T) {
	withResponse(t, `{"compressed": "a plain compressed sentence"}`)

	if _, err := Compress("a long plain input sentence",
		NewCompressOptions().WithRetainFields([]string{"ssn"})); err != nil {
		t.Fatalf("Compress refused text output over a field name that cannot apply to it: %v", err)
	}
}

// A winner the model itself disqualified is a self-contradictory answer, and it
// was being returned as the decision. This holds under any options: the
// disqualification is the model's own claim, and refusing to act on a claim it
// just made is not second-guessing it.
func TestArbitrate_RefusesAWinnerItDisqualified(t *testing.T) {
	type candidate struct {
		Name string `json:"name"`
	}
	withResponse(t, `{
		"winner_index": 1,
		"scores": {"0": 0.4, "1": 0.9},
		"evaluations": [
			{"index": 0, "total_score": 0.4, "disqualified": false},
			{"index": 1, "total_score": 0.9, "disqualified": true, "disqualify_reason": "fails the income rule"}
		],
		"reasoning": "picked the disqualified one anyway",
		"confidence": 0.9
	}`)

	_, err := Arbitrate([]candidate{{Name: "a"}, {Name: "b"}}, ArbitrateOptions{Rules: []string{"some rule"}})
	if err == nil {
		t.Fatal("Arbitrate returned a winner it had marked disqualified")
	}
	if !strings.Contains(err.Error(), "disqualified") {
		t.Fatalf("the error does not name the contradiction: %v", err)
	}
}

// RequireAllRules is stated in the prompt as "disqualify any option that fails
// ANY rule" and was checked nowhere, so the winner could be an option with a
// failing rule result -- the single thing the flag exists to prevent.
func TestArbitrate_RefusesAWinnerFailingARuleUnderRequireAllRules(t *testing.T) {
	type candidate struct {
		Name string `json:"name"`
	}
	withResponse(t, `{
		"winner_index": 0,
		"scores": {"0": 0.9},
		"evaluations": [
			{"index": 0, "total_score": 0.9, "disqualified": false,
			 "rule_results": [{"rule": "some rule", "passed": false, "reasoning": "did not meet it"}]}
		],
		"reasoning": "best of a bad bunch",
		"confidence": 0.9
	}`)

	_, err := Arbitrate([]candidate{{Name: "a"}, {Name: "b"}},
		ArbitrateOptions{Rules: []string{"some rule"}, RequireAllRules: true})
	if err == nil {
		t.Fatal("Arbitrate returned a winner with a failing rule under RequireAllRules")
	}
	if !strings.Contains(err.Error(), "RequireAllRules") {
		t.Fatalf("the error does not name the flag it violated: %v", err)
	}
}

// The companion case: the same failing rule, without the flag. RequireAllRules
// is what turns a failed rule into a disqualification, so without it the
// evaluation is reported and the winner stands.
func TestArbitrate_AFailedRuleIsNotFatalWithoutRequireAllRules(t *testing.T) {
	type candidate struct {
		Name string `json:"name"`
	}
	withResponse(t, `{
		"winner_index": 0,
		"scores": {"0": 0.9},
		"evaluations": [
			{"index": 0, "total_score": 0.9, "disqualified": false,
			 "rule_results": [{"rule": "some rule", "passed": false, "reasoning": "did not meet it"}]}
		],
		"reasoning": "best of a bad bunch",
		"confidence": 0.9
	}`)

	result, err := Arbitrate([]candidate{{Name: "a"}, {Name: "b"}},
		ArbitrateOptions{Rules: []string{"some rule"}})
	if err != nil {
		t.Fatalf("Arbitrate refused a failed rule with RequireAllRules unset: %v", err)
	}
	if result.Winner.Name != "a" {
		t.Fatalf("Winner = %+v, want the option at index 0", result.Winner)
	}
}

// Enrich's AddOnly value-mutation gap (a field the model overwrites was
// invisible to the name-set diff AddedFields used) was found during this
// audit and has since been fixed directly in enrich.go (changedExistingField,
// checked when AddOnly is set) while this file was being written -- the
// production fix and this audit file are running concurrently. No test for
// it remains here because there is no live defect left to demonstrate; see
// the finding note in the task report instead.

// RedactLLM's Categories is documented as "Categories to detect", and the
// prompt lists only what the caller asked for -- but a returned span's Category
// was never checked against the list before being applied. A caller who
// narrowed detection to "email" so a name stays usable downstream could have
// that name redacted anyway, with a nil error. That is not a harmless
// over-redaction: the narrowing is the whole reason the option exists.
func TestRedactLLM_RefusesASpanOutsideTheRequestedCategories(t *testing.T) {
	text := "Contact John Smith at john@example.com for details."
	withResponse(t, `{"spans":[
		{"text":"john@example.com","start":22,"category":"email"},
		{"text":"John Smith","start":8,"category":"name"}
	]}`)

	opts := NewRedactLLMOptions().WithCategories([]string{"email"})
	_, err := RedactLLM(context.Background(), text, opts)
	if err == nil {
		t.Fatal("RedactLLM applied a \"name\" span to a caller who asked only for \"email\"")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Fatalf("the error does not name the unrequested category: %v", err)
	}
}

// The companion case: spans inside the requested list still redact. Without it
// the check above is indistinguishable from refusing every narrowed request.
func TestRedactLLM_AppliesSpansInsideTheRequestedCategories(t *testing.T) {
	text := "Contact John Smith at john@example.com for details."
	withResponse(t, `{"spans":[
		{"text":"john@example.com","start":22,"category":"email"}
	]}`)

	opts := NewRedactLLMOptions().WithCategories([]string{"email"})
	result, err := RedactLLM(context.Background(), text, opts)
	if err != nil {
		t.Fatalf("RedactLLM refused a span inside the requested categories: %v", err)
	}
	if strings.Contains(result.Text, "john@example.com") {
		t.Fatalf("the email survived redaction: %q", result.Text)
	}
	if !strings.Contains(result.Text, "John Smith") {
		t.Fatalf("the name was redacted despite not being requested: %q", result.Text)
	}
}

// "all" is the default and means what it says, so it opts out of the check
// entirely rather than matching every span against the literal string "all".
func TestRedactLLM_AllCategoriesAcceptsAnySpan(t *testing.T) {
	text := "Contact John Smith at john@example.com for details."
	withResponse(t, `{"spans":[
		{"text":"John Smith","start":8,"category":"name"}
	]}`)

	result, err := RedactLLM(context.Background(), text, NewRedactLLMOptions())
	if err != nil {
		t.Fatalf("the default \"all\" category list refused a span: %v", err)
	}
	if strings.Contains(result.Text, "John Smith") {
		t.Fatalf("the name survived redaction under \"all\": %q", result.Text)
	}
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

// Decompose's MaxDepth is phrased as a ceiling in the prompt, unlike
// TargetParts, which is explicitly a hint -- and it was compared against
// nothing, so the depth Decompose measures for its own result could sail past
// the caller's limit and be reported as the answer.
func TestDecompose_RefusesADecompositionDeeperThanRequested(t *testing.T) {
	withResponse(t, `{
		"parts": [
			{"id": "1", "name": "a", "depth": 1, "content": "part a"},
			{"id": "2", "name": "b", "depth": 3, "content": "part b"}
		],
		"root_parts": ["1"]
	}`)

	_, err := Decompose[string]("something to break down", NewDecomposeOptions().WithMaxDepth(1))
	if err == nil {
		t.Fatal("Decompose accepted a depth-3 part against a MaxDepth of 1")
	}
	if !strings.Contains(err.Error(), "depth") {
		t.Fatalf("the error does not name the depth it exceeded: %v", err)
	}
}

func TestDecompose_AcceptsADecompositionInsideTheDepthLimit(t *testing.T) {
	withResponse(t, `{
		"parts": [
			{"id": "1", "name": "a", "depth": 1, "content": "part a"},
			{"id": "2", "name": "b", "depth": 2, "content": "part b"}
		],
		"root_parts": ["1"]
	}`)

	result, err := Decompose[string]("something to break down", NewDecomposeOptions().WithMaxDepth(2))
	if err != nil {
		t.Fatalf("Decompose refused a decomposition inside its depth limit: %v", err)
	}
	if result.MaxDepth != 2 {
		t.Fatalf("MaxDepth = %d, want the measured depth of 2", result.MaxDepth)
	}
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
