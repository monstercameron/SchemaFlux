package ops

// This file drives redact_llm.go (RedactLLM's Validate, RedactLLM itself,
// buildRedactSystemPrompt, buildRedactUserPrompt, containsCategory) and
// negotiate.go (Negotiate/Settle, mergeNegotiateOptions, normalizeFloat,
// NegotiateAdversarial/SettleAdversarial), all of which showed 0.0% coverage
// (mergeNegotiateOptions was partially covered, at 61.9%) before this file.
// RedactLLM specifically had no test anywhere in the package: `RedactLLM(`
// does not appear in any other _test.go file.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// erroringProvider always fails the call, for exercising an operation's
// refusal path when the provider itself cannot answer.
type erroringProvider struct{ err error }

func (p *erroringProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, p.err
}
func (p *erroringProvider) Name() string                               { return "erroring" }
func (p *erroringProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *erroringProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }

func jsonNumber(s string) json.Number { return json.Number(s) }

// --- RedactLLMOptions.Validate ---

func TestRedactLLMOptionsValidate(t *testing.T) {
	cases := []struct {
		name    string
		opts    RedactLLMOptions
		wantErr bool
	}{
		{"defaults are valid", NewRedactLLMOptions(), false},
		{"empty categories refused", NewRedactLLMOptions().WithCategories(nil), true},
		{"zero MinMask refused", NewRedactLLMOptions().WithMinMask(0), true},
		{"negative MinMask refused", NewRedactLLMOptions().WithMinMask(-1), true},
		{"MinMask of 1 is valid", NewRedactLLMOptions().WithMinMask(1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.opts.Validate()
			if tc.wantErr && err == nil {
				t.Error("Validate() = nil, want an error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

// --- RedactLLM ---

func TestRedactLLMRefusesInvalidOptions(t *testing.T) {
	_, err := RedactLLM(context.Background(), "some text", NewRedactLLMOptions().WithCategories(nil))
	if err == nil {
		t.Fatal("RedactLLM accepted options that fail Validate")
	}
}

func TestRedactLLMEmptyTextIsANoOp(t *testing.T) {
	sawCall := false
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		sawCall = true
		return `{"spans": []}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	result, err := RedactLLM(context.Background(), "   ", NewRedactLLMOptions())
	if err != nil {
		t.Fatalf("RedactLLM: %v", err)
	}
	if sawCall {
		t.Error("RedactLLM called the model for whitespace-only text")
	}
	if len(result.Spans) != 0 {
		t.Errorf("Spans = %v, want none for empty input", result.Spans)
	}
}

func TestRedactLLMHappyPath(t *testing.T) {
	text := "Contact john@email.com for details"
	setLLMCaller(func(_ context.Context, _, user string, _ types.OpOptions) (string, error) {
		if !strings.Contains(user, "john@email.com") {
			t.Errorf("the user prompt does not carry the text to scan: %q", user)
		}
		return `{"spans": [{"text": "john@email.com", "start": 8, "category": "email"}]}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	result, err := RedactLLM(context.Background(), text, NewRedactLLMOptions())
	if err != nil {
		t.Fatalf("RedactLLM: %v", err)
	}
	if strings.Contains(result.Text, "john@email.com") {
		t.Errorf("the redacted text still contains the sensitive value: %q", result.Text)
	}
	if result.Categories["email"] != 1 {
		t.Errorf("Categories[email] = %d, want 1", result.Categories["email"])
	}
}

// TestRedactLLMRefusesOnProviderError is the refusal case for the LLM call
// itself: a provider failure must not come back as an empty, "nothing found"
// result -- that would be indistinguishable from a document that genuinely
// had nothing sensitive in it.
func TestRedactLLMRefusesOnProviderError(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", errors.New("provider unavailable")
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	_, err := RedactLLM(context.Background(), "some text with data", NewRedactLLMOptions())
	if err == nil {
		t.Fatal("RedactLLM swallowed a provider error")
	}
}

// TestRedactLLMRefusesAMalformedResponse: an unparseable response must fail
// rather than silently redacting nothing, which would look identical to "the
// model found nothing sensitive."
func TestRedactLLMRefusesAMalformedResponse(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "not json", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	_, err := RedactLLM(context.Background(), "some text with data", NewRedactLLMOptions())
	if err == nil {
		t.Fatal("RedactLLM accepted an unparseable response")
	}
}

// TestRedactLLMDropsAHallucinatedSpan: a span whose text is not actually in
// the document must not be applied -- the wrapper's own doc comment names
// this as the reason offsets stopped being trusted.
func TestRedactLLMDropsAHallucinatedSpan(t *testing.T) {
	text := "Nothing sensitive here."
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{"spans": [{"text": "555-000-1234", "start": 0, "category": "phone"}]}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	result, err := RedactLLM(context.Background(), text, NewRedactLLMOptions())
	if err != nil {
		t.Fatalf("RedactLLM: %v", err)
	}
	if result.Text != text {
		t.Errorf("Text = %q, want the original text unchanged (hallucinated span must be dropped)", result.Text)
	}
	if len(result.Spans) != 0 {
		t.Errorf("Spans = %v, want none", result.Spans)
	}
}

// --- buildRedactSystemPrompt / buildRedactUserPrompt ---

func TestBuildRedactSystemPromptNamesRequestedCategories(t *testing.T) {
	all := buildRedactSystemPrompt(NewRedactLLMOptions().WithCategories([]string{"all"}))
	if !strings.Contains(all, "email") || !strings.Contains(all, "ssn") {
		t.Errorf("the 'all' prompt does not enumerate the standard categories: %q", all)
	}

	custom := buildRedactSystemPrompt(NewRedactLLMOptions().WithCategories([]string{"widget_id"}))
	if !strings.Contains(custom, "widget_id") {
		t.Errorf("a custom category was not named in the prompt: %q", custom)
	}
	if strings.Contains(custom, "ssn") {
		t.Errorf("a custom category list still enumerated the built-in categories: %q", custom)
	}
}

func TestBuildRedactUserPromptCarriesTheText(t *testing.T) {
	prompt := buildRedactUserPrompt("the exact text to scan", NewRedactLLMOptions())
	if !strings.Contains(prompt, "the exact text to scan") {
		t.Errorf("the user prompt does not carry the input text: %q", prompt)
	}
}

// --- mergeOverlappingSpans ---

// TestMergeOverlappingSpansExtendsAndConcatenatesCategories exercises the
// branch parseRedactResponse's own tests never reach through the public
// entry point: a second span that overlaps the first and extends past its
// end must widen the merged span and concatenate both categories, since
// neither original category alone still describes the merged range.
func TestMergeOverlappingSpansExtendsAndConcatenatesCategories(t *testing.T) {
	in := []RedactSpan{
		{Start: 5, End: 10, Category: "email"},
		{Start: 8, End: 15, Category: "name"}, // overlaps and extends past the first
		{Start: 20, End: 25, Category: "ssn"}, // does not overlap either
	}
	merged := mergeOverlappingSpans(in)
	if len(merged) != 2 {
		t.Fatalf("merged = %+v, want 2 spans (the first two merge, the third stands alone)", merged)
	}
	if merged[0].Start != 5 || merged[0].End != 15 {
		t.Errorf("merged[0] = %+v, want [5:15]", merged[0])
	}
	if merged[0].Category != "email,name" {
		t.Errorf("merged[0].Category = %q, want %q", merged[0].Category, "email,name")
	}
	if merged[0].Original != "" {
		t.Errorf("merged[0].Original = %q, want empty (cannot preserve original text across a merge)", merged[0].Original)
	}
	if merged[1].Start != 20 || merged[1].End != 25 {
		t.Errorf("merged[1] = %+v, want [20:25] unchanged", merged[1])
	}
}

// TestMergeOverlappingSpansContainedSpanDoesNotShrink: a span fully inside
// an earlier, larger one must not shrink the merged span's end.
func TestMergeOverlappingSpansContainedSpanDoesNotShrink(t *testing.T) {
	in := []RedactSpan{
		{Start: 0, End: 20, Category: "address"},
		{Start: 5, End: 10, Category: "name"}, // fully contained
	}
	merged := mergeOverlappingSpans(in)
	if len(merged) != 1 {
		t.Fatalf("merged = %+v, want a single span", merged)
	}
	if merged[0].End != 20 {
		t.Errorf("merged[0].End = %d, want 20 (a contained span must not shrink it)", merged[0].End)
	}
	if merged[0].Category != "address" {
		t.Errorf("merged[0].Category = %q, want %q (a contained span's category is not appended when it adds no range)", merged[0].Category, "address")
	}
}

func TestMergeOverlappingSpansEmptyAndSingle(t *testing.T) {
	if got := mergeOverlappingSpans(nil); len(got) != 0 {
		t.Errorf("mergeOverlappingSpans(nil) = %v, want empty", got)
	}
	one := []RedactSpan{{Start: 0, End: 3, Category: "x"}}
	if got := mergeOverlappingSpans(one); len(got) != 1 {
		t.Errorf("mergeOverlappingSpans(single) = %v, want the one span unchanged", got)
	}
}

// --- maskText ---

func TestMaskTextRevealsFirstAndLast(t *testing.T) {
	opts := NewRedactLLMOptions().WithShowFirst(2).WithShowLast(2).WithMinMask(3)
	got := maskText("1234567890", opts)
	if !strings.HasPrefix(got, "12") || !strings.HasSuffix(got, "90") {
		t.Errorf("maskText = %q, want to keep the first 2 and last 2 characters", got)
	}
}

// TestMaskTextShowingEverythingMasksInstead: when ShowFirst+ShowLast would
// reveal the whole value, the function must fall back to masking all of it
// rather than revealing everything.
func TestMaskTextShowingEverythingMasksInstead(t *testing.T) {
	opts := NewRedactLLMOptions().WithShowFirst(3).WithShowLast(3).WithMinMask(1)
	got := maskText("abcde", opts) // len 5, showFirst+showLast (6) >= len
	if strings.Contains(got, "abc") || strings.Contains(got, "cde") {
		t.Errorf("maskText = %q, want no original characters revealed", got)
	}
}

func TestMaskTextClampsNegativeShowValues(t *testing.T) {
	opts := NewRedactLLMOptions()
	opts.ShowFirst = -5
	opts.ShowLast = -5
	got := maskText("secret", opts)
	if strings.Contains(got, "secret") {
		t.Errorf("maskText = %q, still contains the original value", got)
	}
}

// --- locateSpan ---

func TestLocateSpanPicksNearestToHint(t *testing.T) {
	text := "aaXbbXccXdd" // "X" at indices 2, 5, 8
	// The hint sits exactly on the middle occurrence, which must win over the
	// first and last even though the first is also a valid candidate.
	if got := locateSpan(text, "X", 5, 0); got != 5 {
		t.Fatalf("locateSpan = %d, want 5 (the occurrence matching the hint exactly)", got)
	}
	// A hint that lands between two candidates picks the nearer one.
	if got := locateSpan(text, "X", 7, 0); got != 8 {
		t.Fatalf("locateSpan = %d, want 8 (nearer to hint 7 than candidate 5)", got)
	}
}

func TestLocateSpanReturnsMinusOneWhenNotFound(t *testing.T) {
	if got := locateSpan("no match here", "zzz", 0, 0); got != -1 {
		t.Errorf("locateSpan = %d, want -1", got)
	}
}

func TestLocateSpanEmptyValueReturnsMinusOne(t *testing.T) {
	if got := locateSpan("some text", "", 0, 0); got != -1 {
		t.Errorf("locateSpan = %d, want -1 for an empty value", got)
	}
}

// --- containsCategory ---

func TestContainsCategory(t *testing.T) {
	cases := []struct {
		name       string
		categories []string
		target     string
		want       bool
	}{
		{"exact match", []string{"email", "phone"}, "email", true},
		{"case-insensitive match", []string{"Email"}, "email", true},
		{"no match", []string{"phone", "ssn"}, "email", false},
		{"empty list", nil, "email", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := containsCategory(tc.categories, tc.target); got != tc.want {
				t.Errorf("containsCategory(%v, %q) = %v, want %v", tc.categories, tc.target, got, tc.want)
			}
		})
	}
}

// =============================================================================
// negotiate.go
// =============================================================================

type negotiatedPlan struct {
	DurationWeeks int      `json:"duration_weeks"`
	Features      []string `json:"features"`
}

// --- Settle / Negotiate ---

func TestSettleHappyPath(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	provider := &scriptedProvider{bodies: []string{
		`{"solution": {"duration_weeks": 6, "features": ["auth"]}, "satisfaction": {"budget": 0.8}, "overall_satisfaction": 0.8, "tradeoffs": [{"sacrificed": "scope", "gained": "speed", "impact": "medium"}], "alternatives": [{"duration_weeks": 8, "features": ["auth", "analytics"]}], "reasoning": "balanced", "confidence": 0.75}`,
	}}
	ctx := WithProvider(context.Background(), provider)

	result, err := Settle[negotiatedPlan](map[string]any{"max_budget": 100}, NegotiateOptions{
		Context:         ctx,
		Priorities:      map[string]float64{"cost": 0.6, "speed": 0.4},
		Constraints:     []string{"must ship in Q1"},
		Strategy:        "pareto",
		MinSatisfaction: 0.5,
		MaxAlternatives: 2,
		Steering:        "favor cost savings",
	})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if result.Solution.DurationWeeks != 6 {
		t.Errorf("Solution.DurationWeeks = %d, want 6", result.Solution.DurationWeeks)
	}
	if result.Satisfaction["budget"] != 0.8 {
		t.Errorf("Satisfaction[budget] = %v, want 0.8", result.Satisfaction["budget"])
	}
	if len(result.Alternatives) != 1 {
		t.Errorf("Alternatives = %v, want 1", result.Alternatives)
	}
	if len(result.Tradeoffs) != 1 {
		t.Errorf("Tradeoffs = %v, want 1", result.Tradeoffs)
	}
}

// TestNegotiateDelegatesToSettle exercises the deprecated alias: it must
// produce the same answer, not a second implementation that can drift.
func TestNegotiateDelegatesToSettle(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	// Above NegotiateOptions' default MinSatisfaction of 0.6, which Settle now
	// enforces -- this test is about the delegation, not about the floor.
	provider := &scriptedProvider{bodies: []string{
		`{"solution": {"duration_weeks": 4, "features": []}, "satisfaction": {}, "overall_satisfaction": 0.8, "confidence": 0.5}`,
	}}
	ctx := WithProvider(context.Background(), provider)

	result, err := Negotiate[negotiatedPlan](map[string]any{"deadline": "soon"}, NegotiateOptions{Context: ctx})
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}
	if result.Solution.DurationWeeks != 4 {
		t.Errorf("Solution.DurationWeeks = %d, want 4", result.Solution.DurationWeeks)
	}
}

// TestSettleRefusesOnProviderError: the constraints could not be settled, and
// that has to come back as an error, not a zero-value plan that looks like a
// deliberate "no changes needed" answer.
func TestSettleRefusesOnProviderError(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	provider := &erroringProvider{err: errors.New("provider down")}
	ctx := WithProvider(context.Background(), provider)

	_, err := Settle[negotiatedPlan](map[string]any{"x": 1}, NegotiateOptions{Context: ctx})
	if err == nil {
		t.Fatal("Settle swallowed a provider error")
	}
}

// TestSettleRefusesAMalformedResponse: a response that doesn't parse must
// not produce a zero-value "solution" silently.
func TestSettleRefusesAMalformedResponse(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	provider := &scriptedProvider{bodies: []string{"not json"}}
	ctx := WithProvider(context.Background(), provider)

	_, err := Settle[negotiatedPlan](map[string]any{"x": 1}, NegotiateOptions{Context: ctx})
	if err == nil {
		t.Fatal("Settle accepted an unparseable response")
	}
}

// TestSettleRefusesUnmarshalableConstraints: constraints that cannot be
// marshaled to JSON at all (a channel field) must fail before any call is
// made, not send a broken request.
func TestSettleRefusesUnmarshalableConstraints(t *testing.T) {
	type unmarshalable struct {
		Ch chan int
	}
	_, err := Settle[negotiatedPlan](unmarshalable{Ch: make(chan int)}, NegotiateOptions{})
	if err == nil {
		t.Fatal("Settle accepted constraints that cannot be marshaled to JSON")
	}
}

// --- mergeNegotiateOptions ---

func TestMergeNegotiateOptionsUserOverridesEveryField(t *testing.T) {
	defaults := NegotiateOptions{
		MinSatisfaction: 0.6,
		MaxAlternatives: 3,
		Strategy:        "balanced",
		Mode:            types.TransformMode,
		Intelligence:    types.Fast,
	}
	user := NegotiateOptions{
		Priorities:      map[string]float64{"cost": 1.0},
		Constraints:     []string{"c1"},
		MinSatisfaction: 0.9,
		MaxAlternatives: 5,
		Strategy:        "maximize_primary",
		Steering:        "steer this",
		Mode:            types.Strict,
		Intelligence:    types.Quick,
		Model:           "pinned",
		Context:         context.Background(),
	}

	merged := mergeNegotiateOptions(defaults, user)

	if merged.MinSatisfaction != 0.9 || merged.MaxAlternatives != 5 || merged.Strategy != "maximize_primary" {
		t.Errorf("scalar overrides not applied: %+v", merged)
	}
	if merged.Priorities["cost"] != 1.0 || len(merged.Constraints) != 1 {
		t.Errorf("collection overrides not applied: %+v", merged)
	}
	if merged.Steering != "steer this" || merged.Mode != types.Strict || merged.Intelligence != types.Quick {
		t.Errorf("common-field overrides not applied: %+v", merged)
	}
	if merged.Model != "pinned" || merged.Context == nil {
		t.Errorf("model/context overrides not applied: %+v", merged)
	}
}

func TestMergeNegotiateOptionsZeroUserLeavesDefaults(t *testing.T) {
	defaults := NegotiateOptions{MinSatisfaction: 0.6, MaxAlternatives: 3, Strategy: "balanced"}
	merged := mergeNegotiateOptions(defaults, NegotiateOptions{})
	if merged.MinSatisfaction != 0.6 || merged.MaxAlternatives != 3 || merged.Strategy != "balanced" {
		t.Errorf("a zero-value user override clobbered the defaults: %+v", merged)
	}
}

// --- normalizeFloat ---

func TestNormalizeFloat(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  float64
		ok    bool
	}{
		{"float64", float64(1.5), 1.5, true},
		{"float32", float32(2.5), 2.5, true},
		{"int", int(3), 3, true},
		{"int32", int32(4), 4, true},
		{"int64", int64(5), 5, true},
		{"json.Number valid", jsonNumber("6.5"), 6.5, true},
		{"json.Number invalid", jsonNumber("not-a-number"), 0, false},
		{"string valid", "7.5", 7.5, true},
		{"string with whitespace", "  8  ", 8, true},
		{"string invalid", "not-a-number", 0, false},
		{"unsupported type", []int{1}, 0, false},
		{"nil", nil, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normalizeFloat(tc.value)
			if ok != tc.ok {
				t.Fatalf("normalizeFloat(%v) ok = %v, want %v", tc.value, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("normalizeFloat(%v) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// --- SettleAdversarial / NegotiateAdversarial ---

type salaryTerms struct {
	BaseSalary int `json:"base_salary"`
}

func TestSettleAdversarialHappyPath(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	provider := &scriptedProvider{bodies: []string{
		`{"deal": {"base_salary": 150000}, "deal_reached": true, "term_movements": [{"term": "base_salary", "our_ask": 160000, "their_offer": 130000, "final_value": 150000, "movement": "split"}], "who_conceded_more": "they", "our_satisfaction": 0.8, "their_satisfaction": 0.6, "confidence": 0.7}`,
	}}
	ctx := WithProvider(context.Background(), provider)

	adversarialCtx := AdversarialContext[salaryTerms]{
		Ours:        AdversarialPosition[salaryTerms]{Position: salaryTerms{BaseSalary: 160000}},
		Theirs:      AdversarialPosition[salaryTerms]{Position: salaryTerms{BaseSalary: 130000}},
		OurLeverage: "strong",
	}

	result, err := SettleAdversarial[salaryTerms](adversarialCtx, AdversarialOptions{Context: ctx, Strategy: "aggressive"})
	if err != nil {
		t.Fatalf("SettleAdversarial: %v", err)
	}
	if !result.DealReached {
		t.Error("DealReached = false on a response that reported deal_reached: true")
	}
	if result.Deal.BaseSalary != 150000 {
		t.Errorf("Deal.BaseSalary = %d, want 150000", result.Deal.BaseSalary)
	}
	if len(result.TermMovements) != 1 {
		t.Errorf("TermMovements = %v, want 1", result.TermMovements)
	}
}

// TestNegotiateAdversarialDelegatesToSettleAdversarial exercises the
// deprecated alias.
func TestNegotiateAdversarialDelegatesToSettleAdversarial(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	provider := &scriptedProvider{bodies: []string{
		`{"deal": {"base_salary": 140000}, "deal_reached": false, "term_movements": [], "who_conceded_more": "equal", "our_satisfaction": 0.5, "their_satisfaction": 0.5, "confidence": 0.5}`,
	}}
	ctx := WithProvider(context.Background(), provider)

	adversarialCtx := AdversarialContext[salaryTerms]{
		Ours:   AdversarialPosition[salaryTerms]{Position: salaryTerms{BaseSalary: 150000}},
		Theirs: AdversarialPosition[salaryTerms]{Position: salaryTerms{BaseSalary: 130000}},
	}
	result, err := NegotiateAdversarial[salaryTerms](adversarialCtx, AdversarialOptions{Context: ctx})
	if err != nil {
		t.Fatalf("NegotiateAdversarial: %v", err)
	}
	if result.DealReached {
		t.Error("DealReached = true on a response that reported deal_reached: false")
	}
}

// TestSettleAdversarialRefusesOnProviderError.
func TestSettleAdversarialRefusesOnProviderError(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	provider := &erroringProvider{err: errors.New("provider down")}
	ctx := WithProvider(context.Background(), provider)

	adversarialCtx := AdversarialContext[salaryTerms]{
		Ours:   AdversarialPosition[salaryTerms]{Position: salaryTerms{BaseSalary: 150000}},
		Theirs: AdversarialPosition[salaryTerms]{Position: salaryTerms{BaseSalary: 130000}},
	}
	_, err := SettleAdversarial[salaryTerms](adversarialCtx, AdversarialOptions{Context: ctx})
	if err == nil {
		t.Fatal("SettleAdversarial swallowed a provider error")
	}
}

// TestSettleAdversarialRefusesAMalformedResponse.
func TestSettleAdversarialRefusesAMalformedResponse(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	provider := &scriptedProvider{bodies: []string{"not json"}}
	ctx := WithProvider(context.Background(), provider)

	adversarialCtx := AdversarialContext[salaryTerms]{
		Ours:   AdversarialPosition[salaryTerms]{Position: salaryTerms{BaseSalary: 150000}},
		Theirs: AdversarialPosition[salaryTerms]{Position: salaryTerms{BaseSalary: 130000}},
	}
	_, err := SettleAdversarial[salaryTerms](adversarialCtx, AdversarialOptions{Context: ctx})
	if err == nil {
		t.Fatal("SettleAdversarial accepted an unparseable response")
	}
}
