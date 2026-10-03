package ops

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
)

// Direct cases for the helpers review round 9 found with none of their own.

func TestMentionsANumber(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"", false},
		{"plain words only", false},
		{"7", true},
		{"a7b", true},
		{"ends with a digit 9", true},
		{"½", true},
		{"②", true},
		{"Ⅷ", true},
		{"x³", true},
		{"٤", true},
		{"zero defects", true},
		{"the third attempt", true},
		{"last quarter", true},
		{"one fifth of them", true},
		{"a trillion", true},
		{"thrice", true},
		{"quadruple", true},
		{"a tenth", true},
		{"three fourths", true},
		{"a sixth of the price", true},
		{"one seventh", true},
		{"an eighth", true},
		{"two ninths", true},
		{"fourthly", false},
		{"贰", true},
		{"叁拾", true},
		{"陆", true},
		{"廿", true},
		{"卅", true},
		{"半", true},
		{"請求書", false},
		{"かなとカナ", false},
		{"network, often, canine, tenant, heighten", false},
		{"one and only one", false},
		{"thirdly", false},
		{"quartermaster", false},
		{"TWO", true},
		{"Two", true},
		{"(two)", true},
		{"two.", true},
		{"two\n", true},
		{"twó", false},
		{"étwo", false},
		{"twoé", false},
		{"請two求", true},
		{"the longest word on the list: percentage", true},
		{"a letter longer than the longest: percentages", false},
		{"\xff\xfe invalid bytes", false},
		{"truncated rune at the end \xe4\xb8", false},
		{"invalid bytes then a digit \xff 3", true},
		{strings.Repeat("a", 5000), false},
		{strings.Repeat("a", 5000) + " two", true},
	}
	for _, tc := range cases {
		if got := mentionsANumber(tc.text); got != tc.want {
			shown := tc.text
			if len(shown) > 40 {
				shown = shown[:40] + "..."
			}
			t.Errorf("mentionsANumber(%q) = %v, want %v", shown, got, tc.want)
		}
	}
}

func TestIsQuantityWord(t *testing.T) {
	cases := []struct {
		word string
		want bool
	}{
		{"two", true}, {"ten", true}, {"six", true}, {"zero", true}, {"half", true},
		{"percentage", true}, {"thousands", true}, {"seventeen", true}, {"quarter", true}, {"thrice", true},
		{"one", false}, {"to", false}, {"a", false}, {"", false}, {"tw", false},
		{"twos", false}, {"tens", false}, {"second", false}, {"first", false}, {"many", false},
		{"percentages", false}, {"Two", false}, {"TEN", false},
	}
	for _, tc := range cases {
		var buffer [longestQuantityWord]byte
		length := len(tc.word)
		copy(buffer[:], tc.word)
		if got := isQuantityWord(buffer[:], length); got != tc.want {
			t.Errorf("isQuantityWord(%q) = %v, want %v", tc.word, got, tc.want)
		}
	}
	// A length past the buffer is a word too long to be one.
	var buffer [longestQuantityWord]byte
	copy(buffer[:], "percentage")
	if isQuantityWord(buffer[:], longestQuantityWord+1) {
		t.Error("a word longer than the buffer was taken for a quantity word")
	}
}

func TestIsHanNumeral(t *testing.T) {
	for _, r := range "一二三四五六七八九十百千万萬億亿兆零两兩壹貳參肆伍陸柒捌玖拾佰仟贰叁陆壱弐参廿卅卌半" {
		if !isHanNumeral(r) {
			t.Errorf("isHanNumeral(%q) = false", r)
		}
	}
	for _, r := range "請求書間違人口日月山川a1 あア" {
		if isHanNumeral(r) {
			t.Errorf("isHanNumeral(%q) = true", r)
		}
	}
}

func TestLowerVerdict(t *testing.T) {
	cases := []struct {
		name      string
		start     ClaimVerification
		steps     [][2]string // verdict, why
		verdict   string
		model     string
		noteParts []string
	}{
		{"once", ClaimVerification{Verdict: "verified"}, [][2]string{{"unverifiable", "its confidence is below the floor"}},
			"unverifiable", "verified", []string{"reported as unverifiable, not verified: its confidence is below the floor"}},
		{"twice keeps the first verdict as the model's", ClaimVerification{Verdict: "verified"},
			[][2]string{{"unverifiable", "a"}, {"false", "b"}},
			"false", "verified", []string{"reported as unverifiable, not verified: a", "; then reported as false, not unverifiable: b"}},
		{"three times", ClaimVerification{Verdict: "verified"},
			[][2]string{{"partially_true", "a"}, {"unverifiable", "b"}, {"false", "c"}},
			"false", "verified", []string{"not verified: a", "not partially_true: b", "not unverifiable: c"}},
		{"from partly true", ClaimVerification{Verdict: "partially_true"}, [][2]string{{"false", "x"}},
			"false", "partially_true", []string{"reported as false, not partially_true: x"}},
		{"from misleading", ClaimVerification{Verdict: "misleading"}, [][2]string{{"false", "x"}},
			"false", "misleading", []string{"reported as false, not misleading: x"}},
		{"a note already there is kept", ClaimVerification{Verdict: "unverifiable", ModelVerdict: "verified", Note: "earlier"},
			[][2]string{{"false", "x"}}, "false", "verified", []string{"earlier; then reported as false, not unverifiable: x"}},
		{"an empty reason", ClaimVerification{Verdict: "verified"}, [][2]string{{"unverifiable", ""}},
			"unverifiable", "verified", []string{"reported as unverifiable, not verified: "}},
		{"the same verdict again", ClaimVerification{Verdict: "false"}, [][2]string{{"false", "x"}},
			"false", "false", []string{"reported as false, not false: x"}},
		{"reasoning and evidence are untouched", ClaimVerification{Verdict: "verified", Reasoning: "r", Evidence: []string{"e"}, ModelConfidence: 0.4},
			[][2]string{{"unverifiable", "x"}}, "unverifiable", "verified", []string{"not verified: x"}},
		{"no steps", ClaimVerification{Verdict: "verified"}, nil, "verified", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claim := tc.start
			for _, step := range tc.steps {
				lowerVerdict(&claim, step[0], step[1])
			}
			if claim.Verdict != tc.verdict || claim.ModelVerdict != tc.model {
				t.Errorf("Verdict = %q, ModelVerdict = %q; want %q, %q", claim.Verdict, claim.ModelVerdict, tc.verdict, tc.model)
			}
			at := 0
			for _, part := range tc.noteParts {
				found := strings.Index(claim.Note[at:], part)
				if found < 0 {
					t.Fatalf("Note = %q, want %q after position %d", claim.Note, part, at)
				}
				at += found
			}
			if claim.Reasoning != tc.start.Reasoning || claim.ModelConfidence != tc.start.ModelConfidence || !reflect.DeepEqual(claim.Evidence, tc.start.Evidence) {
				t.Errorf("the model's own fields were changed: %+v", claim)
			}
		})
	}
}

func TestReviewFaults(t *testing.T) {
	logic := func(n int) []LogicIssue { return make([]LogicIssue, n) }
	consistency := func(n int) []ConsistencyIssue { return make([]ConsistencyIssue, n) }
	below := map[string]any{"below_min_confidence": true}

	cases := []struct {
		name   string
		result VerifyResult
		want   string
	}{
		{"nothing", VerifyResult{}, ""},
		{"nil metadata", VerifyResult{Metadata: nil}, ""},
		{"the flag false", VerifyResult{Metadata: map[string]any{"below_min_confidence": false}}, ""},
		{"the flag of another type", VerifyResult{Metadata: map[string]any{"below_min_confidence": "yes"}}, ""},
		{"one reasoning issue", VerifyResult{LogicIssues: logic(1)}, " (1 reasoning issue)"},
		{"two reasoning issues", VerifyResult{LogicIssues: logic(2)}, " (2 reasoning issues)"},
		{"one consistency issue", VerifyResult{ConsistencyIssues: consistency(1)}, " (1 consistency issue)"},
		{"three consistency issues", VerifyResult{ConsistencyIssues: consistency(3)}, " (3 consistency issues)"},
		{"both", VerifyResult{LogicIssues: logic(1), ConsistencyIssues: consistency(2)}, " (1 reasoning issue, 2 consistency issues)"},
		{"below the floor", VerifyResult{Metadata: below}, " (its confidence is below the confidence floor)"},
		{"all three", VerifyResult{LogicIssues: logic(2), ConsistencyIssues: consistency(1), Metadata: below},
			" (2 reasoning issues, 1 consistency issue, its confidence is below the confidence floor)"},
	}
	for _, tc := range cases {
		if got := reviewFaults(tc.result); got != tc.want {
			t.Errorf("%s: reviewFaults = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestOptionsForReading(t *testing.T) {
	deadlined, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	extract := func(set func(*ExtractOptions)) BaseOptions {
		opts := NewExtractOptions()
		set(&opts)
		return opts
	}
	cases := []struct {
		name        string
		options     BaseOptions
		steering    string
		requestID   string
		correlation string
		hasDeadline bool
	}{
		{"nothing set", extract(func(*ExtractOptions) {}), "", "", "", false},
		{"steering on CommonOptions", extract(func(o *ExtractOptions) { o.CommonOptions.Steering = "s" }), "s", "", "", false},
		{"steering on the embedded half", extract(func(o *ExtractOptions) { o.OpOptions.Steering = "s" }), "s", "", "", false},
		{"a request id on CommonOptions", extract(func(o *ExtractOptions) { o.CommonOptions.RequestID = "r" }), "", "r", "", false},
		{"a request id on the embedded half", extract(func(o *ExtractOptions) { o.OpOptions.RequestID = "r" }), "", "r", "", false},
		{"a request id on both", extract(func(o *ExtractOptions) { o.CommonOptions.RequestID = "c"; o.OpOptions.RequestID = "e" }), "", "c", "", false},
		{"a correlation id on the embedded half", extract(func(o *ExtractOptions) { o.OpOptions.CorrelationID = "x" }), "", "", "x", false},
		{"a timeout is not applied", extract(func(o *ExtractOptions) { o.CommonOptions.Timeout = time.Minute }), "", "", "", false},
		{"a deadline on CommonOptions' context", extract(func(o *ExtractOptions) { o.CommonOptions.Context = deadlined }), "", "", "", true},
		{"a deadline on the embedded half's context", extract(func(o *ExtractOptions) { o.OpOptions.Context = deadlined }), "", "", "", true},
		{"CommonOptions on its own", CommonOptions{Steering: "s", RequestID: "r", Timeout: time.Minute}, "s", "r", "", false},
		{"CommonOptions with a deadline", CommonOptions{Context: deadlined}, "", "", "", true},
		{"a converted options value", ConvertOpOptions(types.OpOptions{Steering: "s", RequestID: "r"}, "something else"), "s", "r", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged, callers := optionsForReading(tc.options)
			if merged.Steering != tc.steering || merged.RequestID != tc.requestID || merged.CorrelationID != tc.correlation {
				t.Errorf("steering %q, request %q, correlation %q; want %q, %q, %q",
					merged.Steering, merged.RequestID, merged.CorrelationID, tc.steering, tc.requestID, tc.correlation)
			}
			if callers == nil {
				t.Fatal("the caller's context came back nil")
			}
			if _, has := callers.Deadline(); has != tc.hasDeadline {
				t.Errorf("the caller's context has a deadline: %v, want %v", has, tc.hasDeadline)
			}
			// The merged context has a deadline only when the caller's does:
			// reading the options does not apply the timeout.
			if _, has := merged.Context.Deadline(); has != tc.hasDeadline {
				t.Errorf("the merged context has a deadline: %v, want %v", has, tc.hasDeadline)
			}
		})
	}
}

func TestFirstSet(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""}, {[]string{""}, ""}, {[]string{"", ""}, ""}, {[]string{"a"}, "a"}, {[]string{"a", "b"}, "a"},
		{[]string{"", "b"}, "b"}, {[]string{"", "", "c"}, "c"}, {[]string{" ", "b"}, " "}, {[]string{"a", ""}, "a"},
		{[]string{"", "b", "c"}, "b"},
	}
	for _, tc := range cases {
		if got := firstSet(tc.in...); got != tc.want {
			t.Errorf("firstSet(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCheckBudget(t *testing.T) {
	spent := errors.New("this client's budget is spent")
	cases := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"no client", context.Background(), nil},
		{"a client with no budget", WithExecConfig(context.Background(), &ExecConfig{}), nil},
		{"a client's budget that allows", WithExecConfig(context.Background(), &ExecConfig{Budget: &allowingBudget{allow: 1}}), nil},
		{"a client's budget that refuses", WithExecConfig(context.Background(), &ExecConfig{Budget: &allowingBudget{allow: 0}}), spent},
		{"a client's real budget, unspent", WithExecConfig(context.Background(), &ExecConfig{Budget: NewClientBudget(1)}), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkBudget(tc.ctx)
			if (err != nil) != (tc.want != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}

	// A client's budget is the only one asked: it is asked once per check and
	// the process-wide one is left alone.
	budget := &allowingBudget{allow: 5}
	ctx := WithExecConfig(context.Background(), &ExecConfig{Budget: budget})
	for range 5 {
		if err := checkBudget(ctx); err != nil {
			t.Fatalf("checkBudget: %v", err)
		}
	}
	if budget.checked != 5 {
		t.Errorf("the client's budget was asked %d times in five checks", budget.checked)
	}
	if err := checkBudget(ctx); err == nil {
		t.Error("the sixth check passed a budget that allows five")
	}

	// A spent real budget refuses.
	real := NewClientBudget(0.001)
	real.Record(1, true)
	if err := checkBudget(WithExecConfig(context.Background(), &ExecConfig{Budget: real})); err == nil {
		t.Error("a client's budget that is spent allowed a call")
	}
}

func TestTrackRefusedAnswer(t *testing.T) {
	provider := &answersProvider{}
	usage := types.TokenUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500}
	priced := pricing.CalculateCost(&usage, "gpt-6-luna", "openai").TotalCost
	if priced <= 0 {
		t.Fatal("gpt-6-luna is not priced, and this test cannot measure")
	}

	cases := []struct {
		name  string
		model string
		resp  llm.CompletionResponse
		grows float64
	}{
		{"usage on a priced model", "gpt-6-luna", llm.CompletionResponse{Model: "gpt-6-luna", Provider: "openai", Usage: usage}, priced},
		{"no usage", "gpt-6-luna", llm.CompletionResponse{Model: "gpt-6-luna", Provider: "openai"}, 0},
		{"served from a cache", "gpt-6-luna", llm.CompletionResponse{Model: "gpt-6-luna", Provider: "openai", Usage: usage, Cached: true}, 0},
		{"served from a cache with no usage", "gpt-6-luna", llm.CompletionResponse{Model: "gpt-6-luna", Provider: "openai", Cached: true}, 0},
		{"the response names no model", "gpt-6-luna", llm.CompletionResponse{Provider: "openai", Usage: usage}, priced},
		{"the response names no provider", "gpt-6-luna", llm.CompletionResponse{Model: "gpt-6-luna", Usage: usage}, priced},
		{"the response names neither", "gpt-6-luna", llm.CompletionResponse{Usage: usage}, priced},
		{"a model with no price", "a-model-with-no-price", llm.CompletionResponse{Model: "a-model-with-no-price", Provider: "openai", Usage: usage}, 0},
		{"the model that answered is not the one asked", "some-other-model", llm.CompletionResponse{Model: "gpt-6-luna", Provider: "openai", Usage: usage}, priced},
		{"prompt tokens only", "gpt-6-luna", llm.CompletionResponse{Model: "gpt-6-luna", Provider: "openai", Usage: types.TokenUsage{PromptTokens: 1000}},
			pricing.CalculateCost(&types.TokenUsage{PromptTokens: 1000}, "gpt-6-luna", "openai").TotalCost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			since := time.Now().Add(-time.Millisecond)
			before := pricing.GetTotalCost(since, nil)
			trackRefusedAnswer(provider, tc.model, tc.resp, "req", "corr")
			grew := pricing.GetTotalCost(since, nil) - before
			if diff := grew - tc.grows; diff > priced/1000 || diff < -priced/1000 {
				t.Errorf("the process ledger grew by %v, want %v", grew, tc.grows)
			}
		})
	}
}

func TestEstimatedCost(t *testing.T) {
	price := func(prompt, output int) float64 {
		return pricing.CalculateCost(&types.TokenUsage{PromptTokens: prompt, CompletionTokens: output, TotalTokens: prompt + output}, "gpt-6-luna", "openai").TotalCost
	}
	cases := []struct {
		name            string
		provider, model string
		prompt, output  int
		want            float64
	}{
		{"nothing", "openai", "gpt-6-luna", 0, 0, 0},
		{"a prompt", "openai", "gpt-6-luna", 1000, 0, price(1000, 0)},
		{"an answer", "openai", "gpt-6-luna", 0, 500, price(0, 500)},
		{"both", "openai", "gpt-6-luna", 1000, 500, price(1000, 500)},
		{"a large request", "openai", "gpt-6-luna", 100000, 8000, price(100000, 8000)},
		{"a model with no price", "openai", "a-model-with-no-price", 1000, 500, 0},
		{"a provider with no price", "nobody", "gpt-6-luna", 1000, 500, estimatedCost("nobody", "gpt-6-luna", 1000, 500)},
		{"a local model", "local", "llama", 1000, 500, 0},
		{"an empty model", "openai", "", 1000, 500, 0},
		{"an empty provider", "", "gpt-6-luna", 1000, 500, estimatedCost("", "gpt-6-luna", 1000, 500)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := estimatedCost(tc.provider, tc.model, tc.prompt, tc.output)
			if got < 0 || got != got {
				t.Fatalf("estimatedCost = %v, which a ceiling cannot be compared with", got)
			}
			if diff := got - tc.want; diff > 1e-12 || diff < -1e-12 {
				t.Errorf("estimatedCost = %v, want %v", got, tc.want)
			}
		})
	}
	// It is this library's price and not the provider's: a small Smart call on
	// the default model is well under ten cents.
	if got := estimatedCost("openai", "gpt-6-luna", 500, 4000); got >= 0.10 {
		t.Errorf("a small call on gpt-6-luna is estimated at $%v", got)
	}
}

func TestCheckAnswerRoute(t *testing.T) {
	eu := []string{"eu"}
	policy := declaredPolicy(types.DataPolicy{AllowedProviders: []string{"openai", "azure"}, AllowedRegions: eu})
	asked := &gateProvider{name: "openai"}

	cases := []struct {
		name     string
		ctx      context.Context
		routes   []llm.ProviderCapabilities
		answered string
		model    string // the model the answer names
		refused  bool
	}{
		{"no policy", context.Background(), nil, "anyone", "any", false},
		{"a client that declared no policy", WithExecConfig(context.Background(), &ExecConfig{}), nil, "anyone", "any", false},
		{"the provider that was asked", policy, nil, "openai", "asked-model", false},
		{"the provider that was asked, in other capitals", policy, nil, "OpenAI", "Asked-Model", false},
		{"the provider that was asked, with space around it", policy, nil, "  openai ", " asked-model ", false},
		{"the provider that was asked, under a snapshot's name", policy, nil, "openai", "asked-model-2026-09-15", false},
		{"no provider named", policy, nil, "", "asked-model", false},
		{"only spaces", policy, nil, "   ", "  ", false},
		// Round 11: the provider that was asked, answering as a model that was
		// not, is held to the policy as that route. It used to be accepted on
		// its name.
		{"the provider that was asked, as another model nobody declared", policy, nil, "openai", "m", true},
		{"the provider that was asked, as another model that is allowed", policy,
			[]llm.ProviderCapabilities{{Provider: "openai", Model: "m", Regions: eu}}, "openai", "m", false},
		{"another provider, not allowed", policy, nil, "anthropic", "m", true},
		{"a name that begins the same", policy, nil, "openai-proxy", "m", true},
		{"another allowed provider, its route not declared", policy, nil, "azure", "m", true},
		{"another allowed provider, on a route in a region that is allowed", policy,
			[]llm.ProviderCapabilities{{Provider: "azure", Model: "m", Regions: eu}}, "azure", "m", false},
		{"another allowed provider, on a route in a region that is not", policy,
			[]llm.ProviderCapabilities{{Provider: "azure", Model: "m", Regions: []string{"us"}}}, "azure", "m", true},
		{"another allowed provider, on a route that keeps content", policy,
			[]llm.ProviderCapabilities{{Provider: "azure", Model: "m", Regions: eu, RetentionDays: 30}}, "azure", "m", true},
		// Round 11: this was checked under the model that was asked for, which
		// is the first provider's and says nothing about this one's route.
		{"another allowed provider, naming no model", policy,
			[]llm.ProviderCapabilities{{Provider: "azure", Model: "asked-model", Regions: eu}}, "azure", "", true},
		{"another allowed provider, naming a model its route did not declare", policy,
			[]llm.ProviderCapabilities{{Provider: "azure", Model: "m", Regions: eu}}, "azure", "other", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			for _, route := range tc.routes {
				llm.RegisterCapabilities(route)
			}
			err := checkAnswerRoute(tc.ctx, asked, "asked-model", llm.CompletionResponse{Provider: tc.answered, Model: tc.model, Content: "the answer's own text"})
			if tc.refused != (err != nil) {
				t.Fatalf("err = %v, want a refusal: %v", err, tc.refused)
			}
			if err != nil {
				if !errors.Is(err, types.ErrPolicyViolation) || !errors.Is(err, ErrDataPolicyRefusal) {
					t.Errorf("the refusal is %v", err)
				}
				if strings.Contains(err.Error(), "the answer's own text") {
					t.Errorf("the refusal carries the answer: %v", err)
				}
			}
		})
	}
}
