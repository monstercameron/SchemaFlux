package ops

// This file drives redact_llm.go (RedactLLM's Validate, RedactLLM itself,
// buildRedactSystemPrompt, buildRedactUserPrompt, containsCategory) and
// negotiate.go (Negotiate/Settle, mergeNegotiateOptions,
// NegotiateAdversarial/SettleAdversarial), all of which showed 0.0% coverage
// (mergeNegotiateOptions was partially covered, at 61.9%) before this file.
// RedactLLM specifically had no test anywhere in the package: `RedactLLM(`
// does not appear in any other _test.go file.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/config/configtest"
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

// TestRedactLLMRefusesASpanItCannotFind replaces a test that asserted the
// opposite: a span whose text is not in the document was dropped, and the call
// returned the text unchanged with a nil error. That is right if the span was
// invented and wrong if it was a real value quoted inexactly, and the two look
// the same from here. A redaction that could not be carried out is reported as
// one (LX-020).
func TestRedactLLMRefusesASpanItCannotFind(t *testing.T) {
	text := "Nothing sensitive here."
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{"spans": [{"text": "555-000-1234", "start": 0, "category": "phone"}]}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	result, err := RedactLLM(context.Background(), text, NewRedactLLMOptions())
	if err == nil {
		t.Fatalf("a span that could not be located was accepted; Text = %q", result.Text)
	}
	if result.Text != "" {
		t.Errorf("Text = %q, want nothing a caller could mistake for a redacted document", result.Text)
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

// --- mergeByteSpans ---

func TestMergeByteSpans(t *testing.T) {
	cases := []struct {
		name string
		in   []byteSpan
		want []byteSpan
	}{
		{"empty", nil, nil},
		{"single", []byteSpan{{0, 3, "x"}}, []byteSpan{{0, 3, "x"}}},
		{"disjoint, already ordered", []byteSpan{{0, 3, "a"}, {5, 8, "b"}}, []byteSpan{{0, 3, "a"}, {5, 8, "b"}}},
		{"disjoint, out of order", []byteSpan{{5, 8, "b"}, {0, 3, "a"}}, []byteSpan{{0, 3, "a"}, {5, 8, "b"}}},
		{"overlap extends and keeps both categories", []byteSpan{{5, 10, "email"}, {8, 15, "name"}, {20, 25, "ssn"}},
			[]byteSpan{{5, 15, "email,name"}, {20, 25, "ssn"}}},
		{"contained span does not shrink the outer one", []byteSpan{{0, 20, "address"}, {5, 10, "name"}},
			[]byteSpan{{0, 20, "address,name"}}},
		{"same category twice is named once", []byteSpan{{0, 10, "email"}, {4, 12, "email"}}, []byteSpan{{0, 12, "email"}}},
		{"touching spans join", []byteSpan{{0, 5, "a"}, {5, 9, "b"}}, []byteSpan{{0, 9, "a,b"}}},
		{"a gap of one byte keeps them apart", []byteSpan{{0, 5, "a"}, {6, 9, "b"}}, []byteSpan{{0, 5, "a"}, {6, 9, "b"}}},
		{"a category that is itself a list is split", []byteSpan{{0, 5, "a, b"}, {3, 9, "b ,c"}}, []byteSpan{{0, 9, "a,b,c"}}},
		{"identical spans collapse", []byteSpan{{3, 7, "a"}, {3, 7, "a"}}, []byteSpan{{3, 7, "a"}}},
		{"an empty category does not add a comma", []byteSpan{{0, 10, "email"}, {4, 12, ""}}, []byteSpan{{0, 12, "email"}}},
		{"an empty first category takes the second", []byteSpan{{0, 10, ""}, {4, 12, "name"}}, []byteSpan{{0, 12, "name"}}},
		{"three-way chain", []byteSpan{{0, 5, "a"}, {4, 9, "b"}, {8, 12, "c"}}, []byteSpan{{0, 12, "a,b,c"}}},
		{"same start, longer one first", []byteSpan{{2, 4, "short"}, {2, 9, "long"}}, []byteSpan{{2, 9, "long,short"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeByteSpans(append([]byteSpan(nil), tc.in...))
			if len(got) != len(tc.want) {
				t.Fatalf("merged = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("merged[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
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

	// A negative count must not cancel a positive one in the sum that decides
	// whether the whole value would show: the sum here is 4, under the length of
	// 12, yet clamped it is 14 and the value is masked entirely. RedactLLM
	// refuses negative counts before it gets here, so only a direct call sees this.
	opts = NewRedactLLMOptions()
	opts.ShowFirst, opts.ShowLast, opts.MinMask = -10, 14, 1
	text := "abcdefghijkl"
	if got, want := maskText(text, opts), strings.Repeat("*", len(text)); got != want {
		t.Errorf("maskText = %q, want %q", got, want)
	}
}

// --- occurrencesOf ---

func TestOccurrencesOf(t *testing.T) {
	cases := []struct {
		name, text, value string
		want              [][2]int
	}{
		{"one exact", "call 555-0142 now", "555-0142", [][2]int{{5, 13}}},
		{"three exact", "aaXbbXccXdd", "X", [][2]int{{2, 3}, {5, 6}, {8, 9}}},
		{"overlapping candidates are all found", "aaaa", "aa", [][2]int{{0, 2}, {1, 3}, {2, 4}}},
		{"not there", "no match here", "zzz", nil},
		{"empty value", "some text", "", nil},
		{"whitespace-only value", "some text", "   ", nil},
		{"offsets are bytes: one two-byte letter before", "Zo\u00eb zoe@example.com", "zoe@example.com", [][2]int{{5, 20}}},
		{"spacing differs", "Call 555  014   2298 today", "555 014 2298", [][2]int{{5, 20}}},
		{"line break in the text", "Name: Dana\nKim, age 40", "Dana Kim", [][2]int{{6, 14}}},
		{"case differs", "Email DANA@EXAMPLE.COM now", "dana@example.com", [][2]int{{6, 22}}},
		{"a looser copy is found beside an exact one", "Dana Kim and dana  kim", "Dana Kim", [][2]int{{0, 8}, {13, 22}}},
		{"the end of a longer word is skipped when one stands alone", "Al, Hal", "Al", [][2]int{{0, 2}}},
		{"the start of a longer word is not skipped", "Al, Also", "Al", [][2]int{{0, 2}, {4, 6}}},
		{"the end of a longer word is kept when none stands alone", "Hal", "Al", [][2]int{{1, 3}}},
		{"a no-break space matches a space", "Dana\u00a0Kim", "Dana Kim", [][2]int{{0, 9}}},
		{"regex characters in the value are literal", "cost is $5.00 (net)", "$5.00 (net)", [][2]int{{8, 19}}},
		{"regex characters with different spacing", "a.b  c", "A.B C", [][2]int{{0, 6}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := occurrencesOf(tc.text, tc.value)
			if len(got) != len(tc.want) {
				t.Fatalf("occurrencesOf = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("occurrence %d = %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// --- spansInCharacters ---

func TestSpansInCharacters(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		located []byteSpan
		want    []RedactSpan
	}{
		{"ascii", "ab cd ef", []byteSpan{{3, 5, "x"}}, []RedactSpan{{Start: 3, End: 5, Category: "x", Original: "cd"}}},
		{"a two-byte letter before", "Zo\u00eb cd", []byteSpan{{5, 7, "x"}}, []RedactSpan{{Start: 4, End: 6, Category: "x", Original: "cd"}}},
		{"a four-byte emoji before", "\U0001F4DE cd", []byteSpan{{5, 7, "x"}}, []RedactSpan{{Start: 2, End: 4, Category: "x", Original: "cd"}}},
		{"the span itself is multi-byte", "a J\u00fcrgen b", []byteSpan{{2, 9, "name"}}, []RedactSpan{{Start: 2, End: 8, Category: "name", Original: "J\u00fcrgen"}}},
		{"two spans, multi-byte between them", "ab \u00e9\u00e9 cd", []byteSpan{{0, 2, "a"}, {8, 10, "b"}},
			[]RedactSpan{{Start: 0, End: 2, Category: "a", Original: "ab"}, {Start: 6, End: 8, Category: "b", Original: "cd"}}},
		{"at the very start", "\u00e9x", []byteSpan{{0, 2, "a"}}, []RedactSpan{{Start: 0, End: 1, Category: "a", Original: "\u00e9"}}},
		{"at the very end", "x\u00e9", []byteSpan{{1, 3, "a"}}, []RedactSpan{{Start: 1, End: 2, Category: "a", Original: "\u00e9"}}},
		{"whole text", "\u00e9\u00e9", []byteSpan{{0, 4, "a"}}, []RedactSpan{{Start: 0, End: 2, Category: "a", Original: "\u00e9\u00e9"}}},
		{"none", "abc", nil, nil},
		{"cjk", "\u9023\u7d61 ab", []byteSpan{{7, 9, "a"}}, []RedactSpan{{Start: 3, End: 5, Category: "a", Original: "ab"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := spansInCharacters(tc.text, tc.located)
			if len(got) != len(tc.want) {
				t.Fatalf("spans = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("span %d = %+v, want %+v", i, got[i], tc.want[i])
				}
				if selected := string([]rune(tc.text)[got[i].Start:got[i].End]); selected != got[i].Original {
					t.Fatalf("span %d selects %q in characters, but Original is %q", i, selected, got[i].Original)
				}
			}
		})
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
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
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
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
	// Above NegotiateOptions' default MinSatisfaction of 0.6, which Settle now
	// enforces -- this test is about the delegation, not about the floor.
	provider := &scriptedProvider{bodies: []string{
		`{"solution": {"duration_weeks": 4, "features": []}, "satisfaction": {}, "overall_satisfaction": 0.8, "tradeoffs": [], "alternatives": [], "reasoning": "balanced", "confidence": 0.5}`,
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
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
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
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
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

// --- SettleAdversarial / NegotiateAdversarial ---

type salaryTerms struct {
	BaseSalary int `json:"base_salary"`
}

func TestSettleAdversarialHappyPath(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
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
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
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
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
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
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
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
