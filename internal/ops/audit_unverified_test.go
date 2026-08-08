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

// VerifyOptions.Sources is numbered into the prompt as "[Source 0]", "[Source
// 1]", ... (verify.go:379-387), and the response schema documents a claim's
// "sources" field as the indices into that list the verdict rests on
// (verify.go:458). verifyCore never checks a returned claim.Sources index
// against len(opts.Sources) (verify.go:490-504) before returning success, so
// a model can cite a source that was never given.
func TestAudit_Verify_SourceIndexNotValidated(t *testing.T) {
	withResponse(t, `{
		"overall_verdict": "verified",
		"overall_confidence": 0.95,
		"claims": [
			{"claim": "the sky is blue", "verdict": "verified", "confidence": 0.95, "sources": [7]}
		],
		"summary": "checked",
		"trust_score": 0.9
	}`)

	opts := NewVerifyOptions().WithSources([]any{"only one source"})
	result, err := Verify("the sky is blue", opts)
	if err != nil {
		t.Fatalf("Verify returned an error for a response that merely cited an out-of-range source index: %v", err)
	}
	if len(result.Claims) != 1 {
		t.Fatalf("test setup broken: expected one claim, got %d", len(result.Claims))
	}
	got := result.Claims[0].Sources
	if len(got) != 1 || got[0] < len(opts.Sources) {
		t.Fatalf("test setup broken: expected an out-of-range source index (only %d source(s) given), got %v", len(opts.Sources), got)
	}
	t.Logf("claim cites source index %d, but only %d source(s) were provided; Verify accepted it with a nil error", got[0], len(opts.Sources))
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

// ChooseOptions.TopN is validated as >= 1 and, when > 1, is turned into a
// prompt instruction ("Return top %d options", collection.go:206-208). But
// Choose's return type is a single T (collection.go:174), and the system
// prompt Choose actually sends hard-codes a single-answer format regardless
// of TopN: `Return ONLY a JSON object {"id": "..."}` (collection.go:241),
// "Select the single most appropriate option" (collection.go:239). TopN=3
// produces an internally contradictory prompt, and whatever the model
// returns, Choose reads exactly one id and has nowhere to put the other two
// even if the model tried to supply them. This is not an unchecked
// constraint so much as one the return type makes impossible to honor.
func TestAudit_Choose_TopNGreaterThanOneCannotBeHonoredByReturnType(t *testing.T) {
	withResponse(t, `{"id":"i-000002"}`)

	opts := NewChooseOptions().WithTopN(3)
	result, err := Choose([]string{"apple", "banana", "cherry"}, opts)
	if err != nil {
		t.Fatalf("Choose returned an error for TopN=3: %v", err)
	}
	if result != "banana" {
		t.Fatalf("test setup broken: expected the single id-selected item, got %q", result)
	}
	t.Logf("WithTopN(3) was requested; Choose's system prompt still asked for and accepted exactly one id, returning a single %q with no error and no indication three were requested", result)
}

// TransformOptions.PreserveFields is stated in the prompt as an instruction
// ("Preserve these fields: %s", core.go:324-326), but Transform decodes
// straight from ParseJSONStrict into the target type with no comparison
// back to the corresponding field on the source (core.go:454-469). A field
// named as "preserved" can come back changed and Transform still succeeds.
func TestAudit_Transform_PreserveFieldsNeverChecked(t *testing.T) {
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
	result, err := Transform[source, target](source{ID: "abc123", Name: "Alice"}, opts)
	if err != nil {
		t.Fatalf("Transform returned an error for a response that merely changed a field named in PreserveFields: %v", err)
	}
	if result.ID != "generated-999" {
		t.Fatalf("test setup broken: expected the 'preserved' id to have actually changed, got %q", result.ID)
	}
	t.Logf("PreserveFields named %q; the source's id %q came back as %q, and Transform reported success", opts.PreserveFields, "abc123", result.ID)
}

// RewriteOptions.AvoidWords/IncludeWords are stated in the prompt
// ("Avoid: %s" / "Include: %s", text.go:678-684), but Rewrite returns
// strings.TrimSpace(response) directly with no check (text.go:275-278).
// ExcludesValues already exists in invariants.go for exactly the "must not
// contain" half of this shape and is not called here.
func TestAudit_Rewrite_AvoidWordsNeverChecked(t *testing.T) {
	withResponse(t, "This is a cheap solution.")

	opts := NewRewriteOptions()
	opts.AvoidWords = []string{"cheap"}
	result, err := Rewrite("Describe this product.", opts)
	if err != nil {
		t.Fatalf("Rewrite returned an error for a response that merely used a word AvoidWords excluded: %v", err)
	}
	if !strings.Contains(result, "cheap") {
		t.Fatalf("test setup broken: expected the forbidden word to be present, got %q", result)
	}
	t.Logf("AvoidWords named %q; Rewrite returned %q containing it, with no error", opts.AvoidWords, result)
}

// ResolveOptions.Strategy "authoritative" is documented as "Prefer source at
// index %d" (resolve.go:220-222, interpolating AuthoritativeSource), and
// each Conflict reports its own ChosenSource (resolve.go:56-57) -- a value
// Go can compare against opt.AuthoritativeSource without any model claim
// involved, the same shape Choose's MemberOf check runs over ids. Nothing
// does: parsed.Conflicts is copied straight into the result (resolve.go:270)
// with no check that "authoritative" actually chose the authoritative
// source.
func TestAudit_Resolve_AuthoritativeStrategyChosenSourceNeverChecked(t *testing.T) {
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
	result, err := Resolve([]record{{Name: "from source 0"}, {Name: "from source 1"}}, opts)
	if err != nil {
		t.Fatalf("Resolve returned an error for a response that merely chose the non-authoritative source: %v", err)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].ChosenSource == opts.AuthoritativeSource {
		t.Fatalf("test setup broken: expected the resolved conflict to have chosen a source other than the authoritative one (%d), got %+v", opts.AuthoritativeSource, result.Conflicts)
	}
	t.Logf("Strategy was \"authoritative\" with AuthoritativeSource=%d, and the response chose source %d instead; Resolve accepted it with no error", opts.AuthoritativeSource, result.Conflicts[0].ChosenSource)
}

// VerifyOptions.MinConfidence's prompt is explicit that the floor gates the
// per-claim "verified" verdict, not just the summary number: "Minimum
// confidence for \"verified\" verdict: %.0f%%" (verify.go:438). verifyCore
// does check AtLeastConfidence, but only against
// result.ModelOverallConfidence (verify.go:501) -- the same shape Derive
// had to fix by checking both per-field and overall (derive.go:259-272).
// Verify checks only the aggregate, so a claim can be marked "verified" with
// its own confidence far below the floor as long as the summary number
// clears it.
func TestAudit_Verify_PerClaimConfidenceNeverChecked(t *testing.T) {
	withResponse(t, `{
		"overall_verdict": "verified",
		"overall_confidence": 0.95,
		"claims": [
			{"claim": "the sky is blue", "verdict": "verified", "confidence": 0.1}
		],
		"summary": "checked"
	}`)

	opts := NewVerifyOptions()
	opts.MinConfidence = 0.7
	result, err := Verify("the sky is blue", opts)
	if err != nil {
		t.Fatalf("Verify returned an error for a response whose only weak confidence was on an individual claim: %v", err)
	}
	if len(result.Claims) != 1 || result.Claims[0].Verdict != "verified" || result.Claims[0].ModelConfidence >= opts.MinConfidence {
		t.Fatalf("test setup broken: expected a claim marked verified with confidence below the %.2f floor, got %+v", opts.MinConfidence, result.Claims)
	}
	t.Logf("a claim was marked %q at confidence %.2f against a MinConfidence floor of %.2f (only the overall_confidence of 0.95 cleared it); Verify accepted the call with no error", result.Claims[0].Verdict, result.Claims[0].ModelConfidence, opts.MinConfidence)
}
