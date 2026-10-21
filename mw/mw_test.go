package mw

import (
	"context"
	"errors"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/provider"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// every is each middleware of the package, in a form that wraps a provider and
// changes nothing it does: no limit is reached and nothing waits.
func every() map[string]Middleware {
	altRoute, _ := route("every-alt", "alternate")
	return map[string]Middleware{
		"Retry":          Retry(),
		"RateLimit":      RateLimit(1000, time.Second),
		"Bulkhead":       Bulkhead(100),
		"CircuitBreaker": CircuitBreaker(),
		"Budget":         Budget(1e9, BudgetOptions{Cost: priced(0)}),
		"Cache":          Cache(),
		"Fallback":       Fallback([]Route{altRoute}),
		"FallbackOn":     Fallback([]Route{altRoute}, FallbackOptions{On: []schemaflux.Kind{schemaflux.ErrRateLimited}}),
		"Hedge":          Hedge(time.Hour),
		"Redact":         Redact(),
		"Wrap": func(next schemaflux.Provider) schemaflux.Provider {
			return Wrap(next, func(ctx context.Context, req schemaflux.Request, next schemaflux.Provider) (schemaflux.Response, error) {
				return next.Complete(ctx, req)
			})
		},
	}
}

func TestChain(t *testing.T) {
	t.Parallel()

	t.Run("the first middleware listed is the outermost", func(t *testing.T) {
		t.Parallel()
		var order []string
		named := func(name string) Middleware {
			return func(next schemaflux.Provider) schemaflux.Provider {
				return Func(next.Name(), func(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
					order = append(order, name+" in")
					resp, err := next.Complete(ctx, req)
					order = append(order, name+" out")
					return resp, err
				})
			}
		}
		var base schemafluxtest.Provider
		if _, err := Chain(&base, named("a"), named("b"), named("c")).Complete(t.Context(), ask("q")); err != nil {
			t.Fatal(err)
		}
		want := []string{"a in", "b in", "c in", "c out", "b out", "a out"}
		if !reflect.DeepEqual(order, want) {
			t.Errorf("order = %v, want %v", order, want)
		}
	})

	t.Run("with no middleware it is the base itself", func(t *testing.T) {
		t.Parallel()
		var base schemafluxtest.Provider
		if got := Chain(&base); got != schemaflux.Provider(&base) {
			t.Errorf("Chain with none returned %v", got)
		}
	})

	t.Run("a nil middleware is skipped", func(t *testing.T) {
		t.Parallel()
		var base schemafluxtest.Provider
		var seen []string
		got := Chain(&base, nil, recorder(&seen), nil)
		if _, err := got.Complete(t.Context(), ask("q")); err != nil || !reflect.DeepEqual(seen, []string{"q"}) {
			t.Errorf("seen %v, err %v", seen, err)
		}
	})

	t.Run("middleware of the package and of the program's own compose", func(t *testing.T) {
		t.Parallel()
		var base schemafluxtest.Provider
		var seen []string
		chain := Chain(&base, Redact(), recorder(&seen), Cache())
		if _, err := chain.Complete(t.Context(), ask("mail "+testEmail)); err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 || strings.Contains(seen[0], "@") {
			t.Errorf("the program's own middleware saw %q: the redactor is outside it", seen)
		}
	})

	t.Run("a chain of every middleware answers as the provider does", func(t *testing.T) {
		t.Parallel()
		p, calls := answering("answer")
		var all []Middleware
		for _, name := range []string{"Redact", "Cache", "Budget", "Fallback", "Retry", "Hedge", "RateLimit", "Bulkhead", "CircuitBreaker"} {
			all = append(all, every()[name])
		}
		resp, err := Chain(p, all...).Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "answer" || calls.Load() != 1 {
			t.Errorf("%q, %v, %d provider calls", resp.Content, err, calls.Load())
		}
	})
}

func TestFunc(t *testing.T) {
	t.Parallel()

	t.Run("it is a provider with the name and the behaviour it was given", func(t *testing.T) {
		t.Parallel()
		p := Func("mine", func(_ context.Context, req schemaflux.Request) (schemaflux.Response, error) {
			return schemaflux.Response{Content: "echo " + req.User}, nil
		})
		resp, err := p.Complete(t.Context(), ask("hi"))
		if err != nil || resp.Content != "echo hi" || p.Name() != "mine" {
			t.Errorf("%q, %v, name %q", resp.Content, err, p.Name())
		}
	})

	t.Run("its error is returned as it is", func(t *testing.T) {
		t.Parallel()
		want := unavailable()
		p := Func("mine", func(context.Context, schemaflux.Request) (schemaflux.Response, error) {
			return schemaflux.Response{}, want
		})
		if _, err := p.Complete(t.Context(), ask("q")); err != want {
			t.Errorf("err = %v, want the function's own", err)
		}
	})

	t.Run("a nil function is a configuration error", func(t *testing.T) {
		t.Parallel()
		_, err := Func("mine", nil).Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrConfiguration)
	})

	t.Run("it can be wrapped by every middleware", func(t *testing.T) {
		t.Parallel()
		for name, middleware := range every() {
			p := Func("mine", func(context.Context, schemaflux.Request) (schemaflux.Response, error) {
				return schemaflux.Response{Content: "ok"}, nil
			})
			if resp, err := middleware(p).Complete(t.Context(), ask("q")); err != nil || resp.Content != "ok" {
				t.Errorf("%s: %q, %v", name, resp.Content, err)
			}
		}
	})
}

// warming is a provider that can be warmed.
type warming struct {
	*schemafluxtest.Provider
	warmed atomic.Int32
}

func (w *warming) Warm(context.Context) error { w.warmed.Add(1); return nil }

func TestEveryMiddlewareKeepsWhatItWraps(t *testing.T) {
	t.Parallel()

	for name, middleware := range every() {
		t.Run(name+" is named for the provider it wraps and unwraps to it", func(t *testing.T) {
			t.Parallel()
			var base schemafluxtest.Provider
			base.SetName("the-wrapped")
			wrapped := middleware(&base)
			if wrapped.Name() != "the-wrapped" {
				t.Errorf("Name = %q", wrapped.Name())
			}
			if got := wrapped.(provider.Unwrapper).Unwrap(); got != schemaflux.Provider(&base) {
				t.Errorf("Unwrap = %v", got)
			}
		})

		t.Run(name+" lets the provider underneath be warmed", func(t *testing.T) {
			t.Parallel()
			w := &warming{Provider: new(schemafluxtest.Provider)}
			wrapped := middleware(w)
			warmer, ok := bind.Optional[provider.Warmer](wrapped)
			if !ok {
				t.Fatal("no Warmer was found through the wrapper")
			}
			if err := warmer.Warm(t.Context()); err != nil || w.warmed.Load() != 1 {
				t.Errorf("Warm: %v, warmed %d times", err, w.warmed.Load())
			}
		})

		t.Run(name+" lets the model of a tier be found underneath", func(t *testing.T) {
			t.Parallel()
			var base schemafluxtest.Provider
			tiered, ok := bind.Optional[interface {
				ModelFor(schemaflux.Tier) string
			}](middleware(&base))
			if !ok || tiered.ModelFor(schemaflux.TierFast) != "test-model" {
				t.Errorf("found %v, ok %v", tiered, ok)
			}
		})

		t.Run(name+" reports the limits of a decision provider underneath", func(t *testing.T) {
			t.Parallel()
			decider := schemafluxtest.NewDecider()
			limits := schemaflux.DecisionLimits{MaxQuestions: 3, MaxOptions: 7, MaxLevels: 9, MaxStateTokens: 1234}
			decider.SetLimits(limits)
			found, ok := bind.Optional[provider.Decider](middleware(decider))
			if !ok || found.DecisionLimits() != limits {
				t.Errorf("limits = %+v, ok %v, want %+v", found, ok, limits)
			}
		})

		t.Run(name+" reports no limits for a provider that generates text", func(t *testing.T) {
			t.Parallel()
			var base schemafluxtest.Provider
			if _, ok := bind.Optional[provider.Decider](middleware(&base)); ok {
				t.Error("a Decider was found over a provider that is not one")
			}
		})

		t.Run(name+" reports the retry settings of the provider underneath", func(t *testing.T) {
			t.Parallel()
			inner := new(schemafluxtest.Provider)
			wrapped := middleware(retrier{Provider: inner, retries: 6, backoff: 3 * time.Second})
			retries, backoff := wrapped.(provider.Retrier).RetryPolicy()
			// A chain that holds a Retry makes the repeats itself and tells whatever
			// calls it to make none: the number of requests is the middleware's alone.
			wantRetries := 6
			if name == "Retry" {
				wantRetries = -1
			}
			if retries != wantRetries || backoff != 3*time.Second {
				t.Errorf("RetryPolicy = %d, %v, want %d, 3s", retries, backoff, wantRetries)
			}
			never := middleware(inner)
			if retries, _ := never.(provider.Retrier).RetryPolicy(); retries >= 0 {
				t.Errorf("a provider that retries nothing reports %d, want a negative number", retries)
			}
		})

		t.Run(name+" does not stream, and says so, and the stream does not go round it", func(t *testing.T) {
			t.Parallel()
			var base schemafluxtest.Provider
			base.Reply("a streamed answer")
			streamer, ok := bind.Optional[provider.Streamer](middleware(&base))
			if !ok {
				t.Fatal("no Streamer was found")
			}
			var got error
			for _, err := range streamer.Stream(t.Context(), ask("q")) {
				got = err
			}
			wantKind(t, got, schemaflux.ErrUnsupported)
			if base.Calls() != 0 {
				t.Errorf("the provider underneath was streamed from %d times: the stream went round the wrapper", base.Calls())
			}
			count := 0
			for _, err := range provider.Streamer(&base).Stream(t.Context(), ask("q")) {
				if err != nil {
					t.Fatal(err)
				}
				count++
			}
			if count == 0 {
				t.Error("the provider itself did not stream")
			}
		})

		t.Run(name+" is given nothing to wrap and fails every request as a configuration error", func(t *testing.T) {
			t.Parallel()
			wrapped := middleware(nil)
			_, err := wrapped.Complete(t.Context(), ask("q"))
			wantKind(t, err, schemaflux.ErrConfiguration)
			_ = wrapped.Name()
		})

		t.Run(name+" passes the request on as it is and the answer back as it is", func(t *testing.T) {
			t.Parallel()
			want := schemaflux.Response{
				Content: "the answer", Model: "the-model", Provider: "the-provider", Upstream: "the-upstream",
				FinishReason: "stop", ServiceTier: "flex",
				Usage:  schemaflux.Usage{Input: 11, Output: 22, Total: 33, Cached: 4, CacheWrite: 5, Reasoning: 6},
				Timing: schemaflux.Timing{Calls: 1},
			}
			var got schemaflux.Request
			var base schemafluxtest.Provider
			base.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
				got = req
				return want, nil
			})
			req := schemaflux.Request{
				Model: "m", System: "sys", User: "usr", Temperature: 0.25, MaxTokens: 77, Format: "json",
				Schema: map[string]any{"type": "object"}, SchemaName: "named", Effort: "low", EffortPreferred: true,
				CacheKey: "key", WebSearch: true,
				Decision: yesNo(`"state"`, "is it?"),
			}
			resp, err := middleware(&base).Complete(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, req) {
				t.Errorf("the provider was sent %+v, want %+v", got, req)
			}
			if !reflect.DeepEqual(resp, want) {
				t.Errorf("the answer is %+v, want %+v", resp, want)
			}
		})

		t.Run(name+" passes the values of the context on", func(t *testing.T) {
			t.Parallel()
			type key struct{}
			var seen atomic.Value
			wrapped := middleware(Func("probe", func(ctx context.Context, _ schemaflux.Request) (schemaflux.Response, error) {
				seen.Store(ctx.Value(key{}))
				return schemaflux.Response{Content: "ok"}, nil
			}))
			ctx := context.WithValue(t.Context(), key{}, "carried")
			if _, err := wrapped.Complete(ctx, ask("q")); err != nil || seen.Load() != "carried" {
				t.Errorf("err %v, the provider saw %v", err, seen.Load())
			}
		})

		t.Run(name+" passes a provider's error on with its kind and the fields it set", func(t *testing.T) {
			t.Parallel()
			var base schemafluxtest.Provider
			base.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnauthorized, Message: "the key was refused", Code: "invalid_key", Status: 401})
			_, err := middleware(&base).Complete(t.Context(), ask("q"))
			e := wantKind(t, err, schemaflux.ErrUnauthorized)
			if e.Message != "the key was refused" || e.Code != "invalid_key" || e.Status != 401 {
				t.Errorf("the error is %+v", e)
			}
		})

		t.Run(name+" never prints a provider's own words or a key", func(t *testing.T) {
			t.Parallel()
			var base schemafluxtest.Provider
			vendor := errors.New("vendor said: your key " + testKey + " for " + testEmail + " is invalid")
			base.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnauthorized, Err: vendor})
			_, err := middleware(&base).Complete(t.Context(), ask("q"))
			if err == nil {
				t.Fatal("no error")
			}
			mustNotContain(t, "the error", err.Error(), testKey, testEmail, "vendor said")
			if e, ok := err.(*schemaflux.Error); ok {
				mustNotContain(t, "the error's detail-free form", e.Error(), testKey, "vendor said")
			}
			if !errors.Is(err, vendor) {
				t.Error("the provider's own error is no longer in the chain")
			}
		})
	}
}

func TestEveryMiddlewareIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	for name, middleware := range every() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p, _ := answering("answer")
			wrapped := middleware(p)
			var wg sync.WaitGroup
			for i := range 40 {
				wg.Go(func() {
					req := ask("request")
					if i%2 == 0 {
						req = ask("another")
					}
					if resp, err := wrapped.Complete(t.Context(), req); err != nil || resp.Content != "answer" {
						t.Errorf("%q, %v", resp.Content, err)
					}
				})
			}
			wg.Wait()
		})
	}
}

// TestATypedDecisionAndItsAnswersPassUnchangedThroughEveryWrapper holds the
// wrappers to what a decision provider needs of them: the decision arrives, and
// the typed answers, their probabilities and any escalation come back as they
// were written.
func TestATypedDecisionAndItsAnswersPassUnchangedThroughEveryWrapper(t *testing.T) {
	t.Parallel()

	decision := &schemaflux.Decision{
		State:    `{"ticket":"printer on fire"}`,
		Guidance: "judge as the on-call engineer",
		Questions: []schemaflux.DecisionQuestion{
			{Kind: schemaflux.DecisionYesNo, Ask: "is it urgent?"},
			{Kind: schemaflux.DecisionChoice, Ask: "which team?", Options: []string{"hardware", "software", "facilities"}, Criteria: []string{"machines", "code", ""}},
			{Kind: schemaflux.DecisionScale, Ask: "how bad?", Low: 1, High: 5},
		},
	}
	wantAnswers := []schemaflux.DecisionAnswer{
		{Kind: schemaflux.DecisionYesNo, Yes: true, ModelConfidence: 0.91, ConfidenceReported: true},
		{Kind: schemaflux.DecisionChoice, Label: "facilities", ModelConfidence: 0.77, ConfidenceReported: true},
		{Kind: schemaflux.DecisionScale, Level: 4},
	}
	wantEscalation := &schemaflux.DecisionEscalation{Provider: "first", Model: "m", Reason: "below the floor", ModelConfidence: 0.3, ConfidenceReported: true}

	run := func(t *testing.T, wrap func(schemaflux.Provider) schemaflux.Provider) {
		var got schemaflux.Request
		var base schemafluxtest.Provider
		base.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
			got = req
			return schemaflux.Response{Answers: wantAnswers, Escalation: wantEscalation, Model: "m", Provider: "p"}, nil
		})
		resp, err := wrap(&base).Complete(t.Context(), schemaflux.Request{System: "sys", User: "usr", Decision: decision})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Decision, decision) {
			t.Errorf("the decision arrived as %+v", got.Decision)
		}
		if !reflect.DeepEqual(resp.Answers, wantAnswers) {
			t.Errorf("the answers came back as %+v, want %+v", resp.Answers, wantAnswers)
		}
		if !reflect.DeepEqual(resp.Escalation, wantEscalation) {
			t.Errorf("the escalation came back as %+v, want %+v", resp.Escalation, wantEscalation)
		}
	}

	for name, middleware := range every() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run(t, middleware)
		})
	}
	t.Run("Decisions as the generator", func(t *testing.T) {
		t.Parallel()
		run(t, func(p schemaflux.Provider) schemaflux.Provider {
			// A decider that takes one question and is sent three refuses nothing
			// and is not asked: the decision goes to the generator.
			decider := schemafluxtest.NewDecider()
			decider.SetLimits(schemaflux.DecisionLimits{MaxQuestions: 1})
			return Decisions(p, decider, DecisionOptions{})
		})
	})
	t.Run("a chain of everything", func(t *testing.T) {
		t.Parallel()
		run(t, func(p schemaflux.Provider) schemaflux.Provider {
			return Chain(p, Redact(), Cache(), Budget(1e9, BudgetOptions{Cost: priced(0)}), Fallback(nil), Retry(), Hedge(time.Hour), RateLimit(1000, time.Second), Bulkhead(9), CircuitBreaker())
		})
	})
}

func TestADecisionProviderAnswersThroughEveryWrapperItIsWrappedIn(t *testing.T) {
	t.Parallel()

	for name, middleware := range every() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			decider := schemafluxtest.NewDecider(schemafluxtest.Yes(0.8), schemafluxtest.Chose("b", 0.6))
			decision := &schemaflux.Decision{State: `"s"`, Questions: []schemaflux.DecisionQuestion{
				{Kind: schemaflux.DecisionYesNo, Ask: "ok?"},
				{Kind: schemaflux.DecisionChoice, Ask: "which?", Options: []string{"a", "b"}},
			}}
			resp, err := middleware(decider).Complete(t.Context(), schemaflux.Request{Decision: decision})
			if err != nil {
				t.Fatal(err)
			}
			want := []schemaflux.DecisionAnswer{
				{Kind: schemaflux.DecisionYesNo, Yes: true, ModelConfidence: 0.8, ConfidenceReported: true},
				{Kind: schemaflux.DecisionChoice, Label: "b", ModelConfidence: 0.6, ConfidenceReported: true},
			}
			if !reflect.DeepEqual(resp.Answers, want) {
				t.Errorf("answers = %+v, want %+v", resp.Answers, want)
			}
		})
	}
}

// A middleware written with Wrap keeps what the library looks for on the provider
// underneath, as every layer of the package does. One written with Func hid it:
// a client with no model of its own could not name the provider's default.
func TestAMiddlewareWrittenWithWrapKeepsWhatTheProviderUnderneathCanDo(t *testing.T) {
	t.Parallel()

	identity := func(next schemaflux.Provider) schemaflux.Provider {
		return Wrap(next, func(ctx context.Context, req schemaflux.Request, next schemaflux.Provider) (schemaflux.Response, error) {
			return next.Complete(ctx, req)
		})
	}
	hidden := func(next schemaflux.Provider) schemaflux.Provider {
		return Func(next.Name(), func(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
			return next.Complete(ctx, req)
		})
	}

	t.Run("a client that names no model reaches the model of the provider through it", func(t *testing.T) {
		t.Parallel()
		var base schemafluxtest.Provider
		base.Reply("A summary.")
		for name, chain := range map[string]schemaflux.Provider{
			"the provider itself":         &base,
			"a layer of this package":     Chain(&base, Retry()),
			"a layer written with Wrap":   Chain(&base, identity),
			"Wrap around a Retry":         Chain(&base, identity, Retry()),
			"a Retry around Wrap":         Chain(&base, Retry(), identity),
			"Wrap around Wrap around two": Chain(&base, identity, identity, Cache(), Redact()),
		} {
			ctx := schemaflux.WithClient(t.Context(), schemaflux.Client{Provider: chain})
			if got, err := schemaflux.Summarize(ctx, "some text"); err != nil || got != "A summary." {
				t.Errorf("%s: %q, %v", name, got, err)
			}
		}
	})

	t.Run("a layer written with Func hides it, which is what the doc says", func(t *testing.T) {
		t.Parallel()
		var base schemafluxtest.Provider
		base.Reply("A summary.")
		ctx := schemaflux.WithClient(t.Context(), schemaflux.Client{Provider: Chain(&base, hidden)})
		_, err := schemaflux.Summarize(ctx, "some text")
		wantKind(t, err, schemaflux.ErrConfiguration)
	})

	t.Run("it is named for what it wraps and unwraps to it", func(t *testing.T) {
		t.Parallel()
		var base schemafluxtest.Provider
		base.SetName("the-wrapped")
		wrapped := identity(&base)
		if wrapped.Name() != "the-wrapped" {
			t.Errorf("Name = %q", wrapped.Name())
		}
		if got := wrapped.(provider.Unwrapper).Unwrap(); got != schemaflux.Provider(&base) {
			t.Errorf("Unwrap = %v", got)
		}
	})

	t.Run("it is given the provider it wraps and the request as it came", func(t *testing.T) {
		t.Parallel()
		var base schemafluxtest.Provider
		base.Reply("answer")
		var gotNext schemaflux.Provider
		var gotReq schemaflux.Request
		wrapped := Wrap(&base, func(ctx context.Context, req schemaflux.Request, next schemaflux.Provider) (schemaflux.Response, error) {
			gotNext, gotReq = next, req
			return next.Complete(ctx, req)
		})
		req := schemaflux.Request{Model: "m", User: "u", MaxTokens: 3}
		if _, err := wrapped.Complete(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		if gotNext != schemaflux.Provider(&base) || !reflect.DeepEqual(gotReq, req) {
			t.Errorf("the function was given %v and %+v", gotNext, gotReq)
		}
	})

	t.Run("a nil provider or a nil function fails every request as a configuration error", func(t *testing.T) {
		t.Parallel()
		var base schemafluxtest.Provider
		noop := func(ctx context.Context, req schemaflux.Request, next schemaflux.Provider) (schemaflux.Response, error) {
			return next.Complete(ctx, req)
		}
		for name, p := range map[string]schemaflux.Provider{
			"no provider": Wrap(nil, noop),
			"no function": Wrap(&base, nil),
			"neither":     Wrap(nil, nil),
		} {
			_, err := p.Complete(t.Context(), ask("q"))
			e := wantKind(t, err, schemaflux.ErrConfiguration)
			if e.Op != "mw.wrap" {
				t.Errorf("%s: Op %q", name, e.Op)
			}
			_ = p.Name()
		}
		if base.Calls() != 0 {
			t.Errorf("the provider was asked %d times", base.Calls())
		}
	})

	t.Run("it does not stream, and the stream does not go round it", func(t *testing.T) {
		t.Parallel()
		var base schemafluxtest.Provider
		base.Reply("a streamed answer")
		streamer, ok := bind.Optional[provider.Streamer](identity(&base))
		if !ok {
			t.Fatal("no Streamer was found")
		}
		var got error
		for _, err := range streamer.Stream(t.Context(), ask("q")) {
			got = err
		}
		wantKind(t, got, schemaflux.ErrUnsupported)
		if base.Calls() != 0 {
			t.Errorf("the provider underneath was streamed from %d times", base.Calls())
		}
	})

	t.Run("it reports the retry settings of the provider underneath", func(t *testing.T) {
		t.Parallel()
		wrapped := identity(retrier{Provider: new(schemafluxtest.Provider), retries: 6, backoff: 3 * time.Second})
		retries, backoff := wrapped.(provider.Retrier).RetryPolicy()
		if retries != 6 || backoff != 3*time.Second {
			t.Errorf("RetryPolicy = %d, %v, want 6, 3s", retries, backoff)
		}
	})
}

// A stream that is refused says which layers would have been skipped, and what to
// do, so that a user is not told to drop a layer without knowing which.
func TestTheRefusalOfAStreamNamesTheLayersAndSaysWhatToDo(t *testing.T) {
	t.Parallel()
	var base schemafluxtest.Provider
	chain := Chain(&base, Redact(), Cache(), Budget(5, BudgetOptions{Cost: priced(1)}), Retry(), Bulkhead(2))
	streamer, ok := bind.Optional[provider.Streamer](chain)
	if !ok {
		t.Fatal("no Streamer was found")
	}
	var got error
	for _, err := range streamer.Stream(t.Context(), ask("q")) {
		got = err
	}
	e := wantKind(t, got, schemaflux.ErrUnsupported)
	for _, layer := range []string{"redact", "cache", "budget", "retry", "bulkhead"} {
		if !strings.Contains(e.Message, layer) {
			t.Errorf("the message %q does not name the layer %s", e.Message, layer)
		}
	}
	if !strings.Contains(e.Message, "give the client the provider without them") {
		t.Errorf("the message %q does not say what to do", e.Message)
	}
	mustNotContain(t, "the message", e.Message, "stream through the provider itself")
}

// A call that is over is not started, whichever layer is outermost: a context that
// is nil is an invalid request, and one that has ended fails with its error before
// any layer or the provider is asked.
func TestAnEndedOrNilContextIsRefusedBeforeAnythingIsAsked(t *testing.T) {
	t.Parallel()
	for name, middleware := range every() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var base schemafluxtest.Provider
			base.Reply("answer")
			wrapped := middleware(&base)

			//lint:ignore SA1012 a nil context is the case under test
			_, err := wrapped.Complete(nil, ask("q"))
			wantKind(t, err, schemaflux.ErrInvalidRequest)

			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err = wrapped.Complete(ctx, ask("q"))
			wantKind(t, err, schemaflux.ErrCanceled)

			expired, stop := context.WithDeadline(t.Context(), time.Unix(0, 0))
			defer stop()
			_, err = wrapped.Complete(expired, ask("q"))
			wantKind(t, err, schemaflux.ErrTimeout)

			if base.Calls() != 0 {
				t.Errorf("the provider was asked %d times for calls that were over", base.Calls())
			}
		})
	}
}

// What a layer of this package refuses carries the layer's name as its Op, so that
// the failure says which wrapper made it. A provider's own failure is not
// renamed.
func TestARefusalNamesTheLayerThatMadeIt(t *testing.T) {
	t.Parallel()

	t.Run("a full bulkhead", func(t *testing.T) {
		t.Parallel()
		bubble(t, "", func(t *testing.T) {
			h := newHeld()
			wall := Bulkhead(1, BulkheadOptions{Reject: true})(h)
			first := started(t.Context(), wall)
			synctest.Wait()
			_, err := wall.Complete(t.Context(), ask("q"))
			e := wantKind(t, err, schemaflux.ErrBusy)
			if e.Op != "mw.bulkhead" {
				t.Errorf("Op = %q", e.Op)
			}
			h.let()
			<-first
		})
	})

	t.Run("a rate limit that rejects", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		limited := RateLimit(1, time.Hour, RateLimitOptions{Reject: true})(&p)
		limited.Complete(t.Context(), ask("q"))
		_, err := limited.Complete(t.Context(), ask("q"))
		if e := wantKind(t, err, schemaflux.ErrRateLimited); e.Op != "mw.ratelimit" {
			t.Errorf("Op = %q", e.Op)
		}
	})

	t.Run("an open circuit", func(t *testing.T) {
		t.Parallel()
		breaker := CircuitBreaker(BreakerOptions{Failures: 1})(failingProvider("p", unavailable()))
		breaker.Complete(t.Context(), ask("q"))
		_, err := breaker.Complete(t.Context(), ask("q"))
		if e := wantKind(t, err, schemaflux.ErrUnavailable); e.Op != "mw.breaker" {
			t.Errorf("Op = %q", e.Op)
		}
	})

	t.Run("a budget that is spent", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		_, err := Budget(0.5, BudgetOptions{Cost: priced(1)})(&p).Complete(t.Context(), ask("q"))
		if e := wantKind(t, err, schemaflux.ErrBudgetExceeded); e.Op != "mw.budget" {
			t.Errorf("Op = %q", e.Op)
		}
	})

	t.Run("a failure of the provider's own keeps its Op through every layer", func(t *testing.T) {
		t.Parallel()
		for name, middleware := range every() {
			p := failingProvider("p", &schemaflux.Error{Kind: schemaflux.ErrUnauthorized, Message: "the key was refused"})
			_, err := middleware(p).Complete(t.Context(), ask("q"))
			if e := wantKind(t, err, schemaflux.ErrUnauthorized); e.Op != "" {
				t.Errorf("%s: Op = %q, want the provider's own, which is none", name, e.Op)
			}
		}
	})
}
