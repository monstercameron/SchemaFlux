package ops

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
)

// Review round 12, the route an answer came by.

// r12bProvider answers each call with the next scripted response, under a name
// of its choosing, and says which model it sends when it is given a rule for
// that.
type r12bProvider struct {
	name      string
	responses []llm.CompletionResponse
	// sends maps a requested model to the model the provider sends for it. A
	// provider with no map sends what it is asked for and does not implement
	// llm.ModelSender through this type (see r12bSender).
	sends map[string]string

	mu       sync.Mutex
	calls    int
	received []string
}

func (p *r12bProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	index := p.calls
	if index >= len(p.responses) {
		index = len(p.responses) - 1
	}
	p.calls++
	p.received = append(p.received, req.Model)
	return p.responses[index], nil
}
func (p *r12bProvider) Name() string                               { return p.name }
func (p *r12bProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *r12bProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Nanosecond }
func (p *r12bProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// r12bSender is an r12bProvider that, like the Anthropic provider, sends a
// model other than the one a request names.
type r12bSender struct{ *r12bProvider }

func (p r12bSender) ModelSentFor(requested string) string {
	if sent, ok := p.sends[requested]; ok {
		return sent
	}
	return requested
}

// r12bStream is a streaming provider that ends with one final chunk.
type r12bStream struct {
	name string
	resp llm.CompletionResponse
}

func (p *r12bStream) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, errors.New("Complete must not be called by the streaming path")
}
func (p *r12bStream) Name() string                               { return p.name }
func (p *r12bStream) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *r12bStream) RetryPolicy() (int, time.Duration)          { return 0, 0 }
func (p *r12bStream) CompleteStream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return func(yield func(llm.StreamChunk, error) bool) {
		if yield(llm.StreamChunk{Delta: p.resp.Content}, nil) {
			yield(llm.StreamChunk{Done: true, Response: p.resp}, nil)
		}
	}
}

// r12bBudget records what a client's budget is told.
type r12bBudget struct {
	mu      sync.Mutex
	records []bool
}

func (b *r12bBudget) Check() error { return nil }
func (b *r12bBudget) Record(_ float64, priced bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.records = append(b.records, priced)
}
func (b *r12bBudget) told() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.records)
}

func r12bContext(policy types.DataPolicy, budget BudgetChecker) context.Context {
	config := &ExecConfig{DataPolicy: policy, DataPolicyDeclared: true}
	if budget != nil {
		config.Budget = budget
	}
	return WithExecConfig(context.Background(), config)
}

func r12bEventually(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("gave up waiting for %s", what)
}

// r12bRoutes declares the routes the tests below use.
func r12bRoutes(t *testing.T) {
	t.Helper()
	llm.ResetCapabilityRegistryForTest()
	t.Cleanup(llm.ResetCapabilityRegistryForTest)
	eu, us := []string{"eu"}, []string{"us"}
	llm.DeclareRoute("openai", "gpt-4o", eu, 0)
	llm.DeclareRoute("openai", "gpt-4o-us", us, 0)
	llm.DeclareRoute("openai", "gpt-4-turbo-preview", eu, 0)
	llm.DeclareRoute("openai", "gpt-4", eu, 0)
	llm.DeclareRoute("openai", "gpt-4-32k", us, 0)
	llm.DeclareRoute("anthropic", "claude-3-5-sonnet-latest", eu, 0)
	llm.DeclareRoute("anthropic", "claude-3-5-sonnet-20240620", eu, 0)
	llm.DeclareRoute("openrouter", "openai/gpt-4o", eu, 0)
}

// An allowed route was refused after payment when the provider answered under
// a name that was not the asked name with a version tag: a "-latest" alias
// answered by its snapshot, a pinned snapshot answered by its alias, and a
// gateway's "vendor/model" answered bare.
func TestAnAnswerUnderAnotherNameForTheSameModelIsAccepted(t *testing.T) {
	policy := types.DataPolicy{AllowedProviders: []string{"openai", "anthropic", "openrouter"}, AllowedRegions: []string{"eu"}}
	cases := []struct {
		name     string
		provider string
		asked    string
		answered string
		refused  bool
	}{
		{"a latest alias answered by a snapshot", "anthropic", "claude-3-5-sonnet-latest", "claude-3-5-sonnet-20241022", false},
		{"a latest alias answered by its bare name", "anthropic", "claude-3-5-sonnet-latest", "claude-3-5-sonnet", false},
		{"a snapshot answered by its alias", "openai", "gpt-4o-mini-2024-07-18", "gpt-4o-mini", false},
		{"a prefixed request answered by a snapshot", "openrouter", "openai/gpt-4o", "gpt-4o-2024-08-06", false},
		{"a prefixed request answered bare", "openrouter", "openai/gpt-4o", "gpt-4o", false},
		{"a prefixed request answered with its prefix and a date", "openrouter", "openai/gpt-4o", "openai/gpt-4o-2024-08-06", false},
		{"the model asked", "openai", "gpt-4o", "gpt-4o", false},
		{"a version of the model asked", "openai", "gpt-4o", "gpt-4o-2024-08-06", false},
		// What must still be refused: another model, however alike the name.
		{"another model with a longer name", "openai", "gpt-4", "gpt-4-32k", true},
		{"a model declared in another region", "openai", "gpt-4o", "gpt-4o-us", true},
		{"a mini model with a date", "openai", "gpt-4o", "gpt-4o-mini-2024-07-18", true},
		{"a latest alias answered by another model", "anthropic", "claude-3-5-sonnet-latest", "claude-3-5-haiku-20241022", true},
		{"a prefixed request answered by another model", "openrouter", "openai/gpt-4o", "gpt-4o-mini", true},
		// Recorded limits, refused: a preview rename and a deployment name are not
		// the same model by their names, and a policy cannot take them on trust.
		{"a preview rename", "openai", "gpt-4-turbo-preview-x", "gpt-4-0125-preview", true},
		{"a deployment name", "openai", "my-deployment", "gpt-35-turbo-0613", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r12bRoutes(t)
			provider := &r12bProvider{name: tc.provider, responses: []llm.CompletionResponse{{}}}
			err := checkAnswerRoute(r12bContext(policy, nil), provider, tc.asked, llm.CompletionResponse{Provider: tc.provider, Model: tc.answered})
			if tc.refused != (err != nil) {
				t.Fatalf("checkAnswerRoute(%q answered %q) = %v, want a refusal: %v", tc.asked, tc.answered, err, tc.refused)
			}
		})
	}

	// The limits are limits of the names, not of the policy: an operator who
	// declares the answered route has it allowed.
	t.Run("a preview rename, once its answered name is declared", func(t *testing.T) {
		r12bRoutes(t)
		llm.DeclareRoute("openai", "gpt-4-0125-preview", []string{"eu"}, 0)
		provider := &r12bProvider{name: "openai", responses: []llm.CompletionResponse{{}}}
		err := checkAnswerRoute(r12bContext(policy, nil), provider, "gpt-4-turbo-preview", llm.CompletionResponse{Provider: "openai", Model: "gpt-4-0125-preview"})
		if err != nil {
			t.Errorf("refused a route that was declared: %v", err)
		}
	})
}

// A gateway that says which provider served the request is a further hop, and
// is held to the policy as one. The check read only the provider the request
// was handed to, so for OpenRouter it could never see where the request went.
func TestAnAnswerIsHeldToTheProviderBehindTheGateway(t *testing.T) {
	eu, us := []string{"eu"}, []string{"us"}
	policy := types.DataPolicy{AllowedProviders: []string{"openrouter", "openai", "together", "anthropic"}, AllowedRegions: eu}
	cases := []struct {
		name     string
		ctx      func() context.Context
		asked    string
		resp     llm.CompletionResponse
		refused  bool
		mentions string
	}{
		{"no upstream named", nil, "gpt-4o", llm.CompletionResponse{Provider: "openrouter", Model: "gpt-4o"}, false, ""},
		{"the upstream is the gateway itself", nil, "gpt-4o", llm.CompletionResponse{Provider: "openrouter", Model: "gpt-4o", UpstreamProvider: "OpenRouter"}, false, ""},
		{"an upstream whose route is declared", nil, "gpt-4o", llm.CompletionResponse{Provider: "openrouter", Model: "gpt-4o", UpstreamProvider: "openai"}, false, ""},
		{"an upstream in other capitals", nil, "gpt-4o", llm.CompletionResponse{Provider: "openrouter", Model: "gpt-4o", UpstreamProvider: "OpenAI"}, false, ""},
		{"an upstream, the model with the gateway's prefix", nil, "openai/gpt-4o",
			llm.CompletionResponse{Provider: "openrouter", Model: "openai/gpt-4o", UpstreamProvider: "OpenAI"}, false, ""},
		{"an upstream, the model with a prefix and a date", nil, "openai/gpt-4o",
			llm.CompletionResponse{Provider: "openrouter", Model: "openai/gpt-4o-2024-08-06", UpstreamProvider: "OpenAI"}, false, ""},
		{"an upstream, the model with a date and no prefix", nil, "gpt-4o",
			llm.CompletionResponse{Provider: "openrouter", Model: "gpt-4o-2024-08-06", UpstreamProvider: "OpenAI"}, false, ""},
		{"an upstream the policy does not allow", nil, "gpt-4o",
			llm.CompletionResponse{Provider: "openrouter", Model: "gpt-4o", UpstreamProvider: "mistral"}, true, "which the client's data policy does not allow"},
		{"an upstream the policy allows and nobody declared", nil, "gpt-4o",
			llm.CompletionResponse{Provider: "openrouter", Model: "gpt-4o", UpstreamProvider: "together"}, true, "DeclareRoute"},
		{"an upstream whose route is in another region", nil, "gpt-4o",
			llm.CompletionResponse{Provider: "openrouter", Model: "gpt-4o-us", UpstreamProvider: "openai"}, true, "DeclareRoute"},
		{"a provider that is refused by name is not told to declare a route", nil, "gpt-4o",
			llm.CompletionResponse{Provider: "openrouter", Model: "gpt-4o", UpstreamProvider: "deepinfra"}, true, "deepinfra"},
		{"an upstream and no model named: the model asked is used", nil, "gpt-4o",
			llm.CompletionResponse{Provider: "openrouter", UpstreamProvider: "openai"}, false, ""},
		{"an upstream and no model named, and the model asked is not declared for it", nil, "claude-3-5-sonnet-latest",
			llm.CompletionResponse{Provider: "openrouter", UpstreamProvider: "openai"}, true, "DeclareRoute"},
		{"an upstream beside an answer by the same provider asked, a model declared elsewhere", nil, "gpt-4o",
			llm.CompletionResponse{Provider: "openrouter", Model: "gpt-4o-us", UpstreamProvider: "openai"}, true, ""},
		{"an upstream when the provider named nothing", nil, "gpt-4o",
			llm.CompletionResponse{Model: "gpt-4o", UpstreamProvider: "mistral"}, true, "mistral"},
		{"an upstream under no policy", func() context.Context { return context.Background() }, "gpt-4o",
			llm.CompletionResponse{Provider: "openrouter", Model: "gpt-4o", UpstreamProvider: "mistral"}, false, ""},
		{"a blank upstream", nil, "gpt-4o", llm.CompletionResponse{Provider: "openrouter", Model: "gpt-4o", UpstreamProvider: "   "}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r12bRoutes(t)
			llm.DeclareRoute("openai", "gpt-4o-us", us, 0)
			llm.DeclareRoute("openai", "openai/gpt-4o", eu, 0)
			ctx := r12bContext(policy, nil)
			if tc.ctx != nil {
				ctx = tc.ctx()
			}
			gateway := &r12bProvider{name: "openrouter", responses: []llm.CompletionResponse{{}}}
			err := checkAnswerRoute(ctx, gateway, tc.asked, tc.resp)
			if tc.refused != (err != nil) {
				t.Fatalf("err = %v, want a refusal: %v", err, tc.refused)
			}
			if err == nil {
				return
			}
			if !errors.Is(err, types.ErrPolicyViolation) || !errors.Is(err, ErrDataPolicyRefusal) {
				t.Errorf("the refusal is not classified as the policy's: %v", err)
			}
			if tc.mentions != "" && !strings.Contains(err.Error(), tc.mentions) {
				t.Errorf("the refusal does not say %q: %v", tc.mentions, err)
			}
			if tc.resp.UpstreamProvider != "" && strings.EqualFold(strings.TrimSpace(tc.resp.UpstreamProvider), "mistral") {
				var classified *types.OperationError
				if !errors.As(err, &classified) || !strings.EqualFold(classified.Provider, "mistral") {
					t.Errorf("the refusal does not name the provider the request went to: %v", err)
				}
				if strings.Contains(err.Error(), "DeclareRoute") {
					t.Errorf("a provider that is not allowed is told to declare a route: %v", err)
				}
			}
		})
	}
}

// The names in a refusal come from the server's answer, and are cut short and
// stripped of anything that is not printable before they reach an error.
func TestTheNamesInARefusalAreBoundedAndPrintable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"a plain name", "gpt-4o", "gpt-4o"},
		{"empty", "", ""},
		{"a name with a space", "Google AI Studio", "Google AI Studio"},
		{"a newline", "gpt\nINJECTED", "gptINJECTED"},
		{"a carriage return and a tab", "a\r\tb", "ab"},
		{"an escape sequence", "gpt\x1b[31mred", "gpt[31mred"},
		{"a NUL", "gpt\x00-4", "gpt-4"},
		{"a bidirectional override", "gpt‮4o", "gpt4o"},
		{"a zero-width space", "gp​t", "gpt"},
		{"a line separator", "a b", "ab"},
		{"invalid UTF-8", "gpt-\xff\xfe4o", "gpt-4o"},
		{"only control characters", "\x01\x02\x03", ""},
		{"printable non-ASCII", "modèle-日本", "modèle-日本"},
		{"exactly the limit", strings.Repeat("a", 64), strings.Repeat("a", 64)},
		{"one past the limit", strings.Repeat("a", 65), strings.Repeat("a", 64) + "..."},
		{"far past the limit", strings.Repeat("a", 100000), strings.Repeat("a", 64) + "..."},
		{"control characters do not count toward the limit", strings.Repeat("a\n", 64), strings.Repeat("a", 64)},
		{"a long name with controls", strings.Repeat("ab\x1b", 100), strings.Repeat("ab", 32) + "..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := r12bShownName(tc.in); got != tc.want {
				t.Errorf("r12bShownName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// Through the refusal: neither the message nor the fields carry what the
	// server put there.
	hostile := "evil\n\x1b[2Jprovider" + strings.Repeat("x", 5000)
	for _, tc := range []struct {
		name string
		resp llm.CompletionResponse
	}{
		{"a hostile provider", llm.CompletionResponse{Provider: hostile, Model: "gpt-4o"}},
		{"a hostile model", llm.CompletionResponse{Provider: "openai", Model: hostile}},
		{"a hostile provider and model", llm.CompletionResponse{Provider: hostile, Model: hostile}},
		{"a hostile upstream", llm.CompletionResponse{Provider: "openrouter", Model: "gpt-4o", UpstreamProvider: hostile}},
		{"a hostile upstream, and a hostile model", llm.CompletionResponse{Provider: "openrouter", Model: hostile, UpstreamProvider: hostile}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r12bRoutes(t)
			policy := types.DataPolicy{AllowedProviders: []string{"openai", "openrouter"}, AllowedRegions: []string{"eu"}}
			asked := &r12bProvider{name: "openai", responses: []llm.CompletionResponse{{}}}
			if tc.resp.UpstreamProvider != "" {
				asked = &r12bProvider{name: "openrouter", responses: []llm.CompletionResponse{{}}}
			}
			err := checkAnswerRoute(r12bContext(policy, nil), asked, "gpt-4o", tc.resp)
			if err == nil {
				t.Fatal("the answer was accepted")
			}
			var classified *types.OperationError
			if !errors.As(err, &classified) {
				t.Fatalf("not an OperationError: %v", err)
			}
			for what, text := range map[string]string{"message": err.Error(), "provider": classified.Provider, "model": classified.Model} {
				if len(text) > 400 {
					t.Errorf("the %s is %d bytes long", what, len(text))
				}
				for _, r := range text {
					if !unicode.IsPrint(r) {
						t.Errorf("the %s carries the unprintable %U", what, r)
						break
					}
				}
			}
			if len(classified.Provider) > 70 || len(classified.Model) > 70 {
				t.Errorf("fields of %d and %d bytes", len(classified.Provider), len(classified.Model))
			}
		})
	}
}

// The route a request takes is the provider and the model it sends. The
// Anthropic provider sends its own default for an empty model and for a "gpt"
// one, and the policy checked the name in the request: content went over a route
// that was never checked, and the answer was then refused for naming it.
func TestThePolicyChecksTheModelTheProviderWillSend(t *testing.T) {
	policy := types.DataPolicy{AllowedProviders: []string{"anthropic"}, AllowedRegions: []string{"eu"}}
	newProvider := func(response llm.CompletionResponse) r12bSender {
		return r12bSender{&r12bProvider{
			name: "anthropic", responses: []llm.CompletionResponse{response},
			sends: map[string]string{"": "claude-default", "gpt-4o": "claude-default", "gpt-6-luna": "claude-default"},
		}}
	}
	cases := []struct {
		name     string
		declared map[string][]string // route -> regions
		model    string
		answers  string
		refused  bool // refused before anything is sent
		answered bool // refused after the call
	}{
		{"a gpt name, the model sent is declared", map[string][]string{"claude-default": {"eu"}}, "gpt-4o", "claude-default", false, false},
		{"a gpt name, only the name asked is declared", map[string][]string{"gpt-4o": {"eu"}}, "gpt-4o", "claude-default", true, false},
		{"a gpt name, the model sent is in another region", map[string][]string{"claude-default": {"us"}, "gpt-4o": {"eu"}}, "gpt-4o", "claude-default", true, false},
		{"no model, the model sent is declared", map[string][]string{"claude-default": {"eu"}}, "", "claude-default", false, false},
		{"no model, nothing declared", nil, "", "claude-default", true, false},
		{"a Claude model, declared", map[string][]string{"claude-haiku": {"eu"}}, "claude-haiku", "claude-haiku", false, false},
		{"a Claude model, declared in another region", map[string][]string{"claude-haiku": {"us"}}, "claude-haiku", "claude-haiku", true, false},
		{"a Claude model answered as its snapshot", map[string][]string{"claude-haiku": {"eu"}}, "claude-haiku", "claude-haiku-20241022", false, false},
		{"a gpt name, answered as the model sent", map[string][]string{"claude-default": {"eu"}}, "gpt-6-luna", "claude-default", false, false},
		{"a gpt name, answered as another model", map[string][]string{"claude-default": {"eu"}, "claude-other": {"us"}}, "gpt-6-luna", "claude-other", false, true},
		{"a gpt name, answered with its own name", map[string][]string{"claude-default": {"eu"}, "gpt-6-luna": {"us"}}, "gpt-6-luna", "gpt-6-luna", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			for route, regions := range tc.declared {
				llm.DeclareRoute("anthropic", route, regions, 0)
			}
			provider := newProvider(llm.CompletionResponse{Content: "{}", FinishReason: "stop", Provider: "anthropic", Model: tc.answers})
			ctx := r12bContext(policy, nil)

			if err := checkDataPolicy(ctx, provider, tc.model); tc.refused != (err != nil) {
				t.Fatalf("checkDataPolicy = %v, want a refusal: %v", err, tc.refused)
			}
			if tc.refused {
				// Through a call: refused before the provider is sent anything.
				_, err := CallLLM(WithProvider(ctx, provider), provider, "system", "user", types.OpOptions{Model: tc.model, ResponseFormat: "json"})
				if !errors.Is(err, types.ErrPolicyViolation) || provider.callCount() != 0 {
					t.Errorf("err %v after %d calls; want a policy refusal and no call", err, provider.callCount())
				}
				return
			}
			err := checkAnswerRoute(ctx, provider, tc.model, llm.CompletionResponse{Provider: "anthropic", Model: tc.answers})
			if tc.answered != (err != nil) {
				t.Errorf("checkAnswerRoute = %v, want a refusal: %v", err, tc.answered)
			}
		})
	}
}

// A provider that sends what it is asked for is checked under the name in the
// request, as it was.
func TestAProviderThatSendsWhatItIsAskedForIsCheckedUnderThatName(t *testing.T) {
	policy := types.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"eu"}}
	for _, tc := range []struct {
		model   string
		refused bool
	}{
		{"gpt-4o", false}, {"gpt-4o-us", true}, {"gpt-4", false}, {"gpt-4-32k", true}, {"undeclared", true}, {"", true},
	} {
		r12bRoutes(t)
		provider := &r12bProvider{name: "openai", responses: []llm.CompletionResponse{{}}}
		if err := checkDataPolicy(r12bContext(policy, nil), provider, tc.model); tc.refused != (err != nil) {
			t.Errorf("checkDataPolicy(%q) = %v, want a refusal: %v", tc.model, err, tc.refused)
		}
	}
}

// An answer that is produced and then refused for its route was paid for: the
// client's budget and the process-wide ledger are both told, and the call is not
// retried. Nothing asserted either, so the charge could be dropped.
func TestARefusedAnswerIsStillChargedToBothLedgers(t *testing.T) {
	usage := types.TokenUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500}
	policy := types.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"eu"}}
	cases := []struct {
		name     string
		resp     llm.CompletionResponse
		refused  bool
		recorded int
		grew     bool
	}{
		{"an answer by the route asked", llm.CompletionResponse{Content: "{}", FinishReason: "stop", Provider: "openai", Model: "gpt-6-luna", Usage: usage}, false, 1, true},
		{"an answer from a model the policy forbids", llm.CompletionResponse{Content: "{}", FinishReason: "stop", Provider: "openai", Model: "gpt-5-mini-2025-08-07", Usage: usage}, true, 1, true},
		{"an answer from a provider the policy forbids", llm.CompletionResponse{Content: "{}", FinishReason: "stop", Provider: "mistral", Model: "gpt-6-luna", Usage: usage}, true, 1, true},
		{"a refused answer that reports no usage is unknown spend", llm.CompletionResponse{Content: "{}", FinishReason: "stop", Provider: "openai", Model: "gpt-5-mini-2025-08-07"}, true, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r12bRoutes(t)
			llm.DeclareRoute("openai", "gpt-6-luna", []string{"eu"}, 0)
			provider := &r12bProvider{name: "openai", responses: []llm.CompletionResponse{tc.resp}}
			budget := &r12bBudget{}
			since := time.Now().Add(-time.Millisecond)
			before := pricing.GetTotalCost(since, nil)

			_, err := CallLLM(r12bContext(policy, budget), provider, "system", "user", types.OpOptions{Model: "gpt-6-luna", ResponseFormat: "json"})
			if tc.refused != (err != nil) {
				t.Fatalf("err = %v, want a refusal: %v", err, tc.refused)
			}
			if tc.refused && !errors.Is(err, types.ErrPolicyViolation) {
				t.Errorf("err = %v, want a policy refusal", err)
			}
			if provider.callCount() != 1 {
				t.Errorf("%d calls: a refused answer is not retried", provider.callCount())
			}
			if got := budget.told(); got != tc.recorded {
				t.Errorf("the client's budget was told %d times, want %d", got, tc.recorded)
			}
			grew := pricing.GetTotalCost(since, nil)-before > 0
			if tc.refused && grew != tc.grew {
				t.Errorf("the process ledger grew = %v, want %v", grew, tc.grew)
			}
		})
	}
}

// The stream's answer is held to the route it names as the buffered path's is:
// the same provider naming another model is refused, a snapshot of the model
// asked is not, and a gateway's upstream is a hop. A refused stream's answer was
// paid for and is charged. Only a different provider was exercised.
func TestAStreamAnswerIsHeldToTheRouteItNames(t *testing.T) {
	usage := types.TokenUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500}
	policy := types.DataPolicy{AllowedProviders: []string{"openai", "openrouter"}, AllowedRegions: []string{"eu"}}
	cases := []struct {
		name    string
		stream  string
		resp    llm.CompletionResponse
		refused bool
	}{
		{"the route asked", "openai", llm.CompletionResponse{Provider: "openai", Model: "gpt-4o"}, false},
		{"a snapshot of the model asked", "openai", llm.CompletionResponse{Provider: "openai", Model: "gpt-4o-2024-08-06"}, false},
		{"no provider and no model named", "openai", llm.CompletionResponse{}, false},
		{"the same provider, a model declared elsewhere", "openai", llm.CompletionResponse{Provider: "openai", Model: "gpt-4o-us"}, true},
		{"the same provider, a model nobody declared", "openai", llm.CompletionResponse{Provider: "openai", Model: "gpt-mystery"}, true},
		{"the same provider, a longer name that is another model", "openai", llm.CompletionResponse{Provider: "openai", Model: "gpt-4o-mini-2024-07-18"}, true},
		{"no provider named, a model declared elsewhere", "openai", llm.CompletionResponse{Model: "gpt-4o-us"}, true},
		{"another provider the policy does not allow", "openai", llm.CompletionResponse{Provider: "mistral", Model: "gpt-4o"}, true},
		{"a gateway whose upstream is declared", "openrouter", llm.CompletionResponse{Provider: "openrouter", Model: "gpt-4o", UpstreamProvider: "openai"}, false},
		{"a gateway whose upstream the policy forbids", "openrouter", llm.CompletionResponse{Provider: "openrouter", Model: "gpt-4o", UpstreamProvider: "mistral"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r12bRoutes(t)
			// The gateway's own route, which the request is checked under before it is sent.
			llm.DeclareRoute("openrouter", "gpt-4o", []string{"eu"}, 0)
			resp := tc.resp
			resp.Content, resp.FinishReason, resp.Usage = "all of it", "stop", usage
			budget := &r12bBudget{}
			ctx := WithProvider(r12bContext(policy, budget), &r12bStream{name: tc.stream, resp: resp})
			opts := types.OpOptions{Model: "gpt-4o", ResponseFormat: "text", Context: ctx}

			stream, err := streamLLM(ctx, "system", "user", opts)
			if err != nil {
				t.Fatalf("the stream: %v", err)
			}
			text, err := stream.Text()
			if tc.refused != (err != nil) {
				t.Fatalf("Text = %q, %v; want a refusal: %v", text, err, tc.refused)
			}
			if tc.refused {
				if !errors.Is(err, types.ErrPolicyViolation) || text != "" {
					t.Errorf("Text = %q, %v; want a policy refusal and no text", text, err)
				}
				if strings.Contains(err.Error(), "all of it") {
					t.Errorf("the refusal carries the answer: %v", err)
				}
			}
			// What was produced was paid for, whether or not it can be used.
			r12bEventually(t, "the client's budget to be told", func() bool { return budget.told() >= 1 })
		})
	}
}

// CheckRoutePolicy reads where a route runs and what it keeps from the
// registry as one resolved answer, so what a family declares, and what is
// declared over it, is what the policy sees.
func TestCheckRoutePolicyReadsTheResolvedRoute(t *testing.T) {
	eu := types.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"eu"}, MaxRetentionDays: 7}
	cases := []struct {
		name    string
		setup   func()
		model   string
		refused bool
	}{
		{"nothing declared", func() {}, "gpt-x", true},
		{"a family in the allowed region", func() {
			llm.RegisterCapabilityFamily("openai", "gpt-", llm.ProviderCapabilities{Regions: []string{"eu"}})
		}, "gpt-x", false},
		{"a family in another region", func() {
			llm.RegisterCapabilityFamily("openai", "gpt-", llm.ProviderCapabilities{Regions: []string{"us"}})
		}, "gpt-x", true},
		{"a family re-registered to tighten its regions", func() {
			llm.RegisterCapabilityFamily("openai", "gpt-", llm.ProviderCapabilities{Regions: []string{"eu", "us"}})
			llm.RegisterCapabilityFamily("openai", "gpt-", llm.ProviderCapabilities{Regions: []string{"eu"}})
		}, "gpt-x", false},
		{"a family re-registered to loosen its regions", func() {
			llm.RegisterCapabilityFamily("openai", "gpt-", llm.ProviderCapabilities{Regions: []string{"eu"}})
			llm.RegisterCapabilityFamily("openai", "gpt-", llm.ProviderCapabilities{Regions: []string{"eu", "us"}})
		}, "gpt-x", true},
		{"a family re-registered to tighten its retention", func() {
			llm.RegisterCapabilityFamily("openai", "gpt-", llm.ProviderCapabilities{Regions: []string{"eu"}, RetentionDays: 30})
			llm.RegisterCapabilityFamily("openai", "gpt-", llm.ProviderCapabilities{Regions: []string{"eu"}, RetentionDays: 3})
		}, "gpt-x", false},
		{"a family that retains too long", func() {
			llm.RegisterCapabilityFamily("openai", "gpt-", llm.ProviderCapabilities{Regions: []string{"eu"}, RetentionDays: 30})
		}, "gpt-x", true},
		{"a route declared over a family", func() {
			llm.RegisterCapabilityFamily("openai", "gpt-", llm.ProviderCapabilities{Regions: []string{"us"}, RetentionDays: 30})
			llm.DeclareRoute("openai", "gpt-x", []string{"eu"}, 0)
		}, "gpt-x", false},
		{"a family tightened after a route was declared over it", func() {
			llm.RegisterCapabilityFamily("openai", "gpt-", llm.ProviderCapabilities{Regions: []string{"eu"}})
			llm.DeclareRoute("openai", "gpt-x", []string{"eu"}, 0)
			llm.RegisterCapabilityFamily("openai", "gpt-", llm.ProviderCapabilities{Regions: []string{"us"}})
		}, "gpt-y", true},
		{"a declared route with no region", func() {
			llm.RegisterCapabilityFamily("openai", "gpt-", llm.ProviderCapabilities{Regions: []string{"eu"}})
			llm.DeclareRoute("openai", "gpt-x", []string{}, 0)
		}, "gpt-x", true},
		{"a retention below zero", func() { llm.DeclareRoute("openai", "gpt-x", []string{"eu"}, -1) }, "gpt-x", true},
		{"a route registered whole ignores its family", func() {
			llm.RegisterCapabilityFamily("openai", "gpt-", llm.ProviderCapabilities{Regions: []string{"eu"}})
			llm.RegisterCapabilities(llm.ProviderCapabilities{Provider: "openai", Model: "gpt-x"})
		}, "gpt-x", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			tc.setup()
			err := CheckRoutePolicy(r12bContext(eu, nil), "openai", tc.model)
			if tc.refused != (err != nil) {
				t.Errorf("CheckRoutePolicy = %v, want a refusal: %v", err, tc.refused)
			}
		})
	}
	// Without a policy nothing is read at all.
	if err := CheckRoutePolicy(context.Background(), "openai", "gpt-x"); err != nil {
		t.Errorf("a call with no policy was refused: %v", err)
	}
}

// A route is held to the declaration made under the most specific name it has.
// "openai/gpt-4o" from a gateway, declared with a retention the policy forbids,
// was allowed because the vendor's own "gpt-4o" was declared with none.
func TestTheMostSpecificDeclaredNameDecidesAnAnsweredRoute(t *testing.T) {
	policy := types.DataPolicy{AllowedProviders: []string{"openrouter"}, AllowedRegions: []string{"eu"}, MaxRetentionDays: 30}
	cases := []struct {
		name     string
		declared map[string]int // model name -> retention days, all in region eu
		answered string
		allowed  bool
	}{
		{"the exact name is declared and refused, the short one allowed", map[string]int{"openai/gpt-4o": 365, "gpt-4o": 0}, "openai/gpt-4o", false},
		{"the exact name is declared and allowed, the short one refused", map[string]int{"openai/gpt-4o": 0, "gpt-4o": 365}, "openai/gpt-4o", true},
		{"only the exact name, allowed", map[string]int{"openai/gpt-4o": 0}, "openai/gpt-4o", true},
		{"only the exact name, refused", map[string]int{"openai/gpt-4o": 365}, "openai/gpt-4o", false},
		{"only the vendor's name, allowed", map[string]int{"gpt-4o": 0}, "openai/gpt-4o", true},
		{"only the vendor's name, refused", map[string]int{"gpt-4o": 365}, "openai/gpt-4o", false},
		{"nothing declared", nil, "openai/gpt-4o", false},
		{"a dated answer, the undated name declared and allowed", map[string]int{"openai/gpt-4o": 0}, "openai/gpt-4o-2024-08-06", true},
		{"a dated answer, the dated name refused and the undated allowed", map[string]int{"openai/gpt-4o-2024-08-06": 365, "openai/gpt-4o": 0}, "openai/gpt-4o-2024-08-06", false},
		{"a dated answer, the vendor's undated name refused", map[string]int{"gpt-4o": 365}, "openai/gpt-4o-2024-08-06", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			for name, days := range tc.declared {
				llm.DeclareRoute("openrouter", name, []string{"eu"}, days)
			}
			if got := r12bRouteAllowed(r12bContext(policy, nil), "openrouter", tc.answered); got != tc.allowed {
				t.Errorf("r12bRouteAllowed(%q) = %v, want %v", tc.answered, got, tc.allowed)
			}
		})
	}
}
