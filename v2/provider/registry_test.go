package provider

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

var registeredCount atomic.Int64

// nameFor returns a provider name no other test uses. The registry has no way to
// remove a name, so every test that registers one makes its own.
func nameFor(t testing.TB) string {
	t.Helper()
	return fmt.Sprintf("test-%d-%s", registeredCount.Add(1), strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())))
}

// answering is a factory for a provider that answers with its name and records
// the Config it was given.
func answering(got *Config) Factory {
	return func(cfg Config) (schemaflux.Provider, error) {
		if got != nil {
			*got = cfg
		}
		return &scripted{name: "custom"}, nil
	}
}

func TestRegisterAddsAProviderThatNewBuilds(t *testing.T) {
	t.Parallel()
	name := nameFor(t)
	var got Config
	if err := Register(name, answering(&got)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	p, err := newFrom(lookupOf(), name, Config{APIKey: "k", BaseURL: "https://x.test", OrgID: "o", Headers: map[string]string{"A": "b"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Name() != "custom" {
		t.Errorf("Name() = %q, want the factory's provider", p.Name())
	}
	resp, err := ask(p)
	if err != nil || resp.Content != "scripted" {
		t.Errorf("Complete = %+v, %v", resp, err)
	}
	if got.APIKey != "k" || got.BaseURL != "https://x.test" || got.OrgID != "o" || got.Headers["A"] != "b" {
		t.Errorf("the factory was given %+v, want the Config as it was passed", got)
	}
	if !slices.Contains(Names(), name) {
		t.Errorf("Names() = %v, which lacks %q", Names(), name)
	}
}

func TestRegisterTreatsCaseAndSpaceAsNoDifference(t *testing.T) {
	t.Parallel()
	name := nameFor(t)
	if err := Register("  "+strings.ToUpper(name)+" ", answering(nil)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for _, spelling := range []string{name, strings.ToUpper(name), " " + name + "\n"} {
		if _, err := newFrom(lookupOf(), spelling, Config{}); err != nil {
			t.Errorf("New(%q): %v", spelling, err)
		}
	}
	if err := Register(name, answering(nil)); !errors.Is(err, schemaflux.ErrConfiguration) {
		t.Errorf("registering the same name in lower case gave %v, want a configuration error", err)
	}
}

func TestRegisterRefusesATakenName(t *testing.T) {
	t.Parallel()
	name := nameFor(t)
	first := &scripted{name: "first"}
	if err := Register(name, func(Config) (schemaflux.Provider, error) { return first, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	err := Register(name, func(Config) (schemaflux.Provider, error) { return &scripted{name: "second"}, nil })
	e := errorOf(t, err)
	if e.Kind != schemaflux.ErrConfiguration || e.Op != opRegister {
		t.Errorf("got %+v, want a configuration error of %s", e, opRegister)
	}
	if !strings.Contains(err.Error(), "already a provider") {
		t.Errorf("error %q does not say the name is taken", err)
	}
	if got, _ := newFrom(lookupOf(), name, Config{}); got != schemaflux.Provider(first) {
		t.Errorf("New builds %v, want the first factory's provider to stand", got)
	}
}

func TestRegisterRefusesTheNameOfABuiltInProvider(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"openai", "anthropic", "openrouter", "cerebras", "deepseek", "qwen", "zai", "local", "mock", "jev", "jev-openrouter", "jev-litellm", "OpenAI", " zai "} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := Register(name, answering(nil))
			if !errors.Is(err, schemaflux.ErrConfiguration) {
				t.Fatalf("Register(%q) = %v, want a configuration error", name, err)
			}
			p, err := newFrom(lookupOf(), name, buildable(strings.ToLower(strings.TrimSpace(name)), Config{}))
			if bind.PlaceholderProvider(name) {
				// The two placeholder providers are not offered at all.
				if p != nil || !errors.Is(err, schemaflux.ErrConfiguration) || !strings.Contains(err.Error(), "schemafluxtest") {
					t.Fatalf("New(%q) = %v, %v, want a configuration error that names schemafluxtest", name, p, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("New(%q): %v", name, err)
			}
			if p.Name() == "custom" {
				t.Errorf("New(%q) builds the registered factory's provider", name)
			}
		})
	}
}

func TestRegisterRefusesWhatIsNotAName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		reg     string
		f       Factory
		wantMsg string
	}{
		{"an empty name", "", answering(nil), "name is empty"},
		{"a name of only space", "  \t", answering(nil), "name is empty"},
		{"a nil factory", "nil-factory-" + "x", nil, "factory is nil"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Register(tt.reg, tt.f)
			e := errorOf(t, err)
			if e.Kind != schemaflux.ErrConfiguration || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("got %v, want a configuration error saying %q", err, tt.wantMsg)
			}
		})
	}
	t.Run("a nil factory leaves the name free", func(t *testing.T) {
		t.Parallel()
		name := nameFor(t)
		if err := Register(name, nil); err == nil {
			t.Fatal("Register accepted a nil factory")
		}
		if err := Register(name, answering(nil)); err != nil {
			t.Errorf("the name was left taken by the refused registration: %v", err)
		}
	})
}

func TestNewGivesAFactoryTheDefaultsOfABuiltProviderAndReadsNoVariable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   Config
		want Config
	}{
		{"zero", Config{}, Config{Timeout: 30 * time.Second, RetryBackoff: time.Second, Retries: 3}},
		{"a key", Config{APIKey: "k"}, Config{APIKey: "k", Timeout: 30 * time.Second, RetryBackoff: time.Second, Retries: 3}},
		{"none", Config{Retries: -1}, Config{Timeout: 30 * time.Second, RetryBackoff: time.Second, Retries: -1}},
		{"five", Config{Retries: 5}, Config{Timeout: 30 * time.Second, RetryBackoff: time.Second, Retries: 5}},
		{"a timeout", Config{Timeout: time.Minute}, Config{Timeout: time.Minute, RetryBackoff: time.Second, Retries: 3}},
		{"a backoff", Config{RetryBackoff: time.Hour}, Config{Timeout: 30 * time.Second, RetryBackoff: time.Hour, Retries: 3}},
		{"a store", Config{Store: true}, Config{Store: true, Timeout: 30 * time.Second, RetryBackoff: time.Second, Retries: 3}},
		{"models", Config{Models: Models{Fast: "f"}}, Config{Models: Models{Fast: "f"}, Timeout: 30 * time.Second, RetryBackoff: time.Second, Retries: 3}},
		{"a tier", Config{ServiceTier: "flex"}, Config{ServiceTier: "flex", Timeout: 30 * time.Second, RetryBackoff: time.Second, Retries: 3}},
		{"an organisation", Config{OrgID: "o"}, Config{OrgID: "o", Timeout: 30 * time.Second, RetryBackoff: time.Second, Retries: 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			name := nameFor(t)
			var got Config
			if err := Register(name, answering(&got)); err != nil {
				t.Fatal(err)
			}
			read := func(variable string) (string, bool) {
				t.Errorf("New read the environment variable %s for a registered provider", variable)
				return "", false
			}
			if _, err := newFrom(read, name, tt.in); err != nil {
				t.Fatalf("New: %v", err)
			}
			if got.APIKey != tt.want.APIKey || got.Timeout != tt.want.Timeout || got.RetryBackoff != tt.want.RetryBackoff ||
				got.Retries != tt.want.Retries || got.Store != tt.want.Store || got.Models != tt.want.Models ||
				got.ServiceTier != tt.want.ServiceTier || got.OrgID != tt.want.OrgID {
				t.Errorf("the factory was given %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestNewRefusesABadConfigBeforeCallingAFactory(t *testing.T) {
	t.Parallel()
	name := nameFor(t)
	called := false
	if err := Register(name, func(Config) (schemaflux.Provider, error) { called = true; return &scripted{}, nil }); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []Config{{Timeout: -1}, {RetryBackoff: -1}, {APIKey: "a\nb"}} {
		if _, err := newFrom(lookupOf(), name, cfg); !errors.Is(err, schemaflux.ErrConfiguration) {
			t.Errorf("New with %+v gave %v, want a configuration error", cfg, err)
		}
	}
	if called {
		t.Error("the factory was called with a Config no provider can be built from")
	}
}

func TestAFactoryThatFailsGivesAnErrorOfTheKindItSaid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		kind   schemaflux.Kind
		reason string
	}{
		{"a plain error is a configuration error", errors.New("the file is missing: " + testMarker), schemaflux.ErrConfiguration, "the file is missing"},
		{"an error with a kind keeps it", &schemaflux.Error{Kind: schemaflux.ErrUnauthorized, Message: "the key was refused"}, schemaflux.ErrUnauthorized, "the key was refused"},
		{"a bare kind keeps it", schemaflux.ErrUnavailable, schemaflux.ErrUnavailable, "unavailable"},
		{"a kind under a wrap keeps it", fmt.Errorf("while building: %w", schemaflux.ErrUnsupported), schemaflux.ErrUnsupported, "while building"},
		{"an internal configuration error", types.Newf(types.KindConfiguration, "x", "set it"), schemaflux.ErrConfiguration, "set it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			name := nameFor(t)
			if err := Register(name, func(Config) (schemaflux.Provider, error) { return nil, tt.err }); err != nil {
				t.Fatal(err)
			}
			p, err := newFrom(lookupOf(), name, Config{})
			if p != nil {
				t.Errorf("New returned %v beside its error", p)
			}
			e := errorOf(t, err)
			if e.Kind != tt.kind || e.Op != opNew || e.Provider != name {
				t.Errorf("got %+v, want kind %v from %s for %s", e, tt.kind, opNew, name)
			}
			if !errors.Is(err, tt.err) {
				t.Error("the factory's error is not reachable")
			}
			// The factory's refusal is its own account of what is wrong with the
			// configuration it was given, and the user is told it.
			if !strings.Contains(err.Error(), tt.reason) {
				t.Errorf("the error does not say why the factory refused (%q): %v", tt.reason, err)
			}
		})
	}
	t.Run("no provider and no error", func(t *testing.T) {
		t.Parallel()
		name := nameFor(t)
		if err := Register(name, func(Config) (schemaflux.Provider, error) { return nil, nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := newFrom(lookupOf(), name, Config{}); !errors.Is(err, schemaflux.ErrConfiguration) {
			t.Errorf("got %v, want a configuration error", err)
		}
	})
}

func TestARegisteredNameIsKnownToTheInternalRegistry(t *testing.T) {
	t.Parallel()
	name := nameFor(t)
	seenReq := make(chan schemaflux.Request, 1)
	var got Config
	err := Register(name, func(cfg Config) (schemaflux.Provider, error) {
		got = cfg
		return &scripted{name: name, complete: func(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
			seenReq <- req
			return schemaflux.Response{Content: "from the program's provider", Model: "m1", Usage: schemaflux.Usage{Input: 3, Output: 4}}, nil
		}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	internal, err := llm.BuildProvider(name, llm.ProviderConfig{APIKey: "k"}, 0)
	if err != nil {
		t.Fatalf("llm.BuildProvider: %v", err)
	}
	if internal.Name() != name {
		t.Errorf("Name() = %q, want %q", internal.Name(), name)
	}
	resp, err := internal.Complete(t.Context(), llm.CompletionRequest{Model: "m", SystemPrompt: "s", UserPrompt: "u", ReasoningEffort: "low", PromptCacheKey: "ck"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "from the program's provider" || resp.Model != "m1" || resp.Usage.PromptTokens != 3 || resp.Usage.TotalTokens != 7 {
		t.Errorf("response = %+v", resp)
	}
	req := <-seenReq
	if req.System != "s" || req.User != "u" || req.Effort != schemaflux.EffortLow || req.CacheKey != "ck" || req.Model != "m" {
		t.Errorf("the provider was sent %+v", req)
	}
	if got.APIKey != "k" || got.Retries != 3 || got.Timeout != llm.DefaultHTTPTimeout || got.RetryBackoff != llm.DefaultRetryBackoff {
		t.Errorf("the factory was given %+v, want the key and the defaults of a built provider", got)
	}

	t.Run("none retries arrive as a negative number", func(t *testing.T) {
		if _, err := llm.BuildProvider(name, llm.ProviderConfig{APIKey: "k"}, -1); err != nil {
			t.Fatal(err)
		}
		if got.Retries != -1 {
			t.Errorf("Retries = %d, want -1", got.Retries)
		}
	})
	t.Run("a built provider given to New is the one New builds", func(t *testing.T) {
		built, err := newFrom(lookupOf(), name, Config{})
		if err != nil || built.Name() != name {
			t.Errorf("got %v, %v", built, err)
		}
	})
}

func TestTheInternalAdapterOfAProviderCarriesItsErrorsAsTheKindTheyAre(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		kind       types.ErrorKind
		retryAfter time.Duration
	}{
		{"a rate limit with a wait", &schemaflux.Error{Kind: schemaflux.ErrRateLimited, RetryAfter: 4 * time.Second}, types.KindRateLimited, 4 * time.Second},
		{"unauthorized", &schemaflux.Error{Kind: schemaflux.ErrUnauthorized}, types.KindAuthentication, 0},
		{"a plain error is unavailable", errors.New("down"), types.KindProviderUnavailable, 0},
		{"a canceled context", context.Canceled, types.KindCanceled, 0},
		{"a timeout", &schemaflux.Error{Kind: schemaflux.ErrTimeout}, types.KindTimeout, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			name := nameFor(t)
			err := Register(name, func(Config) (schemaflux.Provider, error) {
				return &scripted{name: name, complete: func(context.Context, schemaflux.Request) (schemaflux.Response, error) {
					return schemaflux.Response{}, tt.err
				}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			internal, err := llm.BuildProvider(name, llm.ProviderConfig{}, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, got := internal.Complete(t.Context(), llm.CompletionRequest{})
			if llm.Classify(got) != tt.kind {
				t.Errorf("the call path classifies it %v, want %v", llm.Classify(got), tt.kind)
			}
			if wait, _ := llm.RetryAfterFrom(got); wait != tt.retryAfter {
				t.Errorf("the call path reads the wait as %v, want %v", wait, tt.retryAfter)
			}
		})
	}
}

// answeringEverything is a provider that is a Warmer, a Streamer, a Retrier and a
// Decider, for the tests of what the internal registry sees through it.
type answeringEverything struct {
	everything
	chunks []schemaflux.Chunk
}

func (a *answeringEverything) Stream(ctx context.Context, req schemaflux.Request) iter.Seq2[schemaflux.Chunk, error] {
	return func(yield func(schemaflux.Chunk, error) bool) {
		for _, chunk := range a.chunks {
			if !yield(chunk, nil) {
				return
			}
		}
	}
}

func TestTheOptionalInterfacesOfARegisteredProviderReachTheInternalRegistry(t *testing.T) {
	t.Parallel()
	t.Run("every interface, through two layers", func(t *testing.T) {
		t.Parallel()
		name := nameFor(t)
		inner := &answeringEverything{everything: everything{scripted: scripted{name: name}},
			chunks: []schemaflux.Chunk{{Delta: "a"}, {Delta: "b"}, {Done: true, Response: schemaflux.Response{Content: "ab", Usage: schemaflux.Usage{Input: 1, Output: 1}}}}}
		if err := Register(name, func(Config) (schemaflux.Provider, error) { return transparent{transparent{inner}}, nil }); err != nil {
			t.Fatal(err)
		}
		internal, err := llm.BuildProvider(name, llm.ProviderConfig{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if warmed, err := llm.Warm(t.Context(), internal); err != nil || !warmed || inner.warmed != 1 {
			t.Errorf("Warm = %v, %v and the provider was warmed %d times, want once", warmed, err, inner.warmed)
		}
		if retries, backoff := internal.RetryPolicy(); retries != 7 || backoff != time.Minute {
			t.Errorf("RetryPolicy() = (%d, %v), want (7, 1m)", retries, backoff)
		}
		if limits, ok := llm.DecisionLimitsOf(internal); !ok || limits.MaxQuestions != 3 {
			t.Errorf("DecisionLimitsOf = %+v, %v, want the provider's limits", limits, ok)
		}
		streaming, ok := internal.(llm.StreamingProvider)
		if !ok {
			t.Fatal("not a StreamingProvider")
		}
		var deltas []string
		var done llm.StreamChunk
		for chunk, err := range streaming.CompleteStream(t.Context(), llm.CompletionRequest{}) {
			if err != nil {
				t.Fatal(err)
			}
			if chunk.Done {
				done = chunk
				break
			}
			deltas = append(deltas, chunk.Delta)
		}
		if strings.Join(deltas, "") != "ab" || done.Response.Content != "ab" || done.Response.Usage.TotalTokens != 2 {
			t.Errorf("deltas %v and last %+v", deltas, done)
		}
	})
	t.Run("a provider with none of them", func(t *testing.T) {
		t.Parallel()
		name := nameFor(t)
		if err := Register(name, func(Config) (schemaflux.Provider, error) { return &scripted{name: name}, nil }); err != nil {
			t.Fatal(err)
		}
		internal, err := llm.BuildProvider(name, llm.ProviderConfig{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := llm.DecisionLimitsOf(internal); ok {
			t.Error("a provider that is not a Decider declares limits")
		}
		if retries, _ := internal.RetryPolicy(); retries != -1 {
			t.Errorf("a provider that is not a Retrier answers %d, want -1, which is not configured", retries)
		}
		if warmed, err := llm.Warm(t.Context(), internal); err != nil || !warmed {
			t.Errorf("Warm = %v, %v: a provider with nothing to warm is not an error", warmed, err)
		}
		var got error
		for _, err := range internal.(llm.StreamingProvider).CompleteStream(t.Context(), llm.CompletionRequest{}) {
			got = err
		}
		if llm.Classify(got) != types.KindUnsupportedCapability {
			t.Errorf("streaming gave %v, want an unsupported capability", got)
		}
	})
	t.Run("a request with a tool choice is refused and not passed on with it dropped", func(t *testing.T) {
		t.Parallel()
		name := nameFor(t)
		if err := Register(name, answering(nil)); err != nil {
			t.Fatal(err)
		}
		internal, _ := llm.BuildProvider(name, llm.ProviderConfig{}, 0)
		_, err := internal.Complete(t.Context(), llm.CompletionRequest{ToolChoice: "auto"})
		if llm.Classify(err) != types.KindUnsupportedCapability {
			t.Errorf("got %v, want an unsupported capability", err)
		}
	})
}

func TestARegisteredFactoryThatReturnsABuiltInProviderIsNotWrappedTwice(t *testing.T) {
	t.Parallel()
	name := nameFor(t)
	local, err := llm.NewLocalProvider(llm.ProviderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := Register(name, func(Config) (schemaflux.Provider, error) { return adapt(local), nil }); err != nil {
		t.Fatal(err)
	}
	internal, err := llm.BuildProvider(name, llm.ProviderConfig{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := internal.(*llm.LocalProvider); !ok {
		t.Errorf("the internal registry gives %T, want the *llm.LocalProvider that was built", internal)
	}
	built, err := newFrom(lookupOf(), name, Config{})
	if err != nil || built.Name() != "local" {
		t.Errorf("New = %v, %v", built, err)
	}
}

func TestAdaptGivesBackTheProviderAnAdapterWasMadeFrom(t *testing.T) {
	t.Parallel()
	plain := &scripted{name: "plain"}
	deciding := &everything{scripted: scripted{name: "deciding"}}
	tests := []struct {
		name string
		p    schemaflux.Provider
	}{
		{"a plain provider", plain},
		{"a decider", deciding},
		{"a provider under a wrapper", transparent{plain}},
		{"a hidden decider", wrapper{deciding}},
		{"a decider under a wrapper", transparent{deciding}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			internal := bind.ToInternal(tt.p)
			if got := adapt(internal); got != tt.p {
				t.Errorf("adapt(bind.ToInternal(p)) = %v, want p", got)
			}
			_, declares := llm.DecisionLimitsOf(internal)
			_, isDecider := bind.Optional[Decider](tt.p)
			if declares != isDecider {
				t.Errorf("the internal provider declares limits = %v, the provider is a Decider = %v", declares, isDecider)
			}
		})
	}
}

func TestRegistrationIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()
	t.Run("distinct names all succeed", func(t *testing.T) {
		t.Parallel()
		const workers = 64
		prefix := nameFor(t)
		var wg sync.WaitGroup
		errs := make([]error, workers)
		for i := range workers {
			wg.Go(func() { errs[i] = Register(fmt.Sprintf("%s-%d", prefix, i), answering(nil)) })
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("Register %d: %v", i, err)
			}
		}
		names := Names()
		for i := range workers {
			if !slices.Contains(names, fmt.Sprintf("%s-%d", prefix, i)) {
				t.Errorf("name %d is missing from Names()", i)
			}
		}
	})
	t.Run("one name registered by many has one winner", func(t *testing.T) {
		t.Parallel()
		const workers = 64
		name := nameFor(t)
		var winners atomic.Int64
		var refused atomic.Int64
		made := make([]*scripted, workers)
		var wg sync.WaitGroup
		for i := range workers {
			made[i] = &scripted{name: fmt.Sprintf("worker-%d", i)}
			wg.Go(func() {
				err := Register(name, func(Config) (schemaflux.Provider, error) { return made[i], nil })
				switch {
				case err == nil:
					winners.Add(1)
				case errors.Is(err, schemaflux.ErrConfiguration):
					refused.Add(1)
				default:
					t.Errorf("Register: %v", err)
				}
			})
		}
		wg.Wait()
		if winners.Load() != 1 || refused.Load() != workers-1 {
			t.Fatalf("%d registrations succeeded and %d were refused, want 1 and %d", winners.Load(), refused.Load(), workers-1)
		}
		p, err := newFrom(lookupOf(), name, Config{})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(made, func(m *scripted) bool { return schemaflux.Provider(m) == p }) {
			t.Errorf("New builds %v, which is none of the registered providers", p)
		}
	})
	t.Run("registering while building and listing", func(t *testing.T) {
		t.Parallel()
		prefix := nameFor(t)
		known := prefix + "-known"
		if err := Register(known, answering(nil)); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		stop := make(chan struct{})
		for range 8 {
			wg.Go(func() {
				for {
					select {
					case <-stop:
						return
					default:
					}
					if _, err := newFrom(lookupOf(), known, Config{}); err != nil {
						t.Errorf("New: %v", err)
						return
					}
					if !slices.IsSorted(Names()) {
						t.Error("Names() is not sorted")
						return
					}
					if _, err := newFrom(lookupOf(), "openai", Config{APIKey: testKey}); err != nil {
						t.Errorf("New(openai): %v", err)
						return
					}
				}
			})
		}
		var registering sync.WaitGroup
		for i := range 32 {
			registering.Go(func() {
				if err := Register(fmt.Sprintf("%s-%d", prefix, i), answering(nil)); err != nil {
					t.Errorf("Register: %v", err)
				}
			})
		}
		registering.Wait()
		close(stop)
		wg.Wait()
	})
}

// panickingProvider is a registered provider whose every call panics.
type panickingProvider struct{}

func (panickingProvider) Name() string { return "panicking-registered" }

func (panickingProvider) Complete(context.Context, schemaflux.Request) (schemaflux.Response, error) {
	panic("boom " + testMarker)
}

func (panickingProvider) Warm(context.Context) error { panic("boom " + testMarker) }

// TestAPanicInARegisteredProviderIsAnErrorWhereverTheInternalRegistryCallsIt
// reaches the provider the way a client that selected it by name does.
func TestAPanicInARegisteredProviderIsAnErrorWhereverTheInternalRegistryCallsIt(t *testing.T) {
	t.Parallel()
	name := nameFor(t)
	if err := Register(name, func(Config) (schemaflux.Provider, error) { return panickingProvider{}, nil }); err != nil {
		t.Fatal(err)
	}
	internal, err := llm.BuildProvider(name, llm.ProviderConfig{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = internal.Complete(t.Context(), llm.CompletionRequest{Model: "m"})
	e := errorOf(t, bind.FromProvider(err))
	if e.Kind != schemaflux.ErrInternal {
		t.Fatalf("got %v, want an error of kind internal", err)
	}
	if strings.Contains(err.Error(), testMarker) || strings.Contains(e.Error(), testMarker) {
		t.Errorf("the thrown value is in the error: %v", err)
	}
	if _, err := llm.Warm(t.Context(), internal); err == nil || strings.Contains(err.Error(), testMarker) {
		t.Errorf("warming: %v, want an error that holds nothing of the value", err)
	}
}
