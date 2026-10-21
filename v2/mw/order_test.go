package mw

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// These tests hold the order rule of the package comment, pair by pair. Each
// builds the order the comment gives and the reverse, and shows the difference
// the rule is there for. Chain lists the outermost middleware first, so
// Chain(p, A, B) is A around B around p.

func TestTheRedactorGoesOutside(t *testing.T) {
	t.Parallel()

	t.Run("what is inside it sees redacted text, and what is outside it sees the original", func(t *testing.T) {
		t.Parallel()
		var base schemafluxtest.Provider
		var inside, outside []string
		chain := Chain(&base, recorder(&outside), Redact(), recorder(&inside))
		if _, err := chain.Complete(t.Context(), ask("mail "+testEmail)); err != nil {
			t.Fatal(err)
		}
		if len(inside) != 1 || strings.Contains(inside[0], testEmail) {
			t.Errorf("a layer inside the redactor saw %q", inside)
		}
		if len(outside) != 1 || !strings.Contains(outside[0], testEmail) {
			t.Errorf("a layer outside the redactor saw %q", outside)
		}
	})

	t.Run("every route of a fallback is sent redacted text only when the redactor is outside the fallback", func(t *testing.T) {
		t.Parallel()
		request := ask("mail " + testEmail)

		alternate, alternateP := route("alternate", "x")
		primary := failingProvider("primary", unavailable())
		if _, err := Chain(primary, Redact(), Fallback([]Route{alternate})).Complete(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		mustNotContain(t, "the primary's request", primary.Last().User, testEmail)
		mustNotContain(t, "the alternate's request", alternateP.Last().User, testEmail)

		alternate2, alternate2P := route("alternate2", "x")
		primary2 := failingProvider("primary", unavailable())
		if _, err := Chain(primary2, Fallback([]Route{alternate2}), Redact()).Complete(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		mustNotContain(t, "the primary's request", primary2.Last().User, testEmail)
		if !strings.Contains(alternate2P.Last().User, testEmail) {
			t.Errorf("the alternate was sent %q: a redactor inside the fallback should not have covered it", alternate2P.Last().User)
		}
	})

	t.Run("both halves of a decision pair are sent redacted text only when the redactor is outside the pair", func(t *testing.T) {
		t.Parallel()
		decision := yesNo(`"mail `+testEmail+`"`, "is it "+testEmail+"?")

		generator, decider := pair(schemafluxtest.Yes(0.2))
		redacting := Redact()(Decisions(generator, decider, DecisionOptions{Floor: 0.9}))
		if _, err := redacting.Complete(t.Context(), schemaflux.Request{User: "u " + testEmail, Decision: decision}); err != nil {
			t.Fatal(err)
		}
		d, g := decider.Last().Decision, generator.Last()
		mustNotContain(t, "the decider's decision", d.State+d.Questions[0].Ask, testEmail)
		mustNotContain(t, "the generator's request", g.User+g.Decision.State+g.Decision.Questions[0].Ask, testEmail)

		generator2, decider2 := pair(schemafluxtest.Yes(0.2))
		around := Decisions(Redact()(generator2), decider2, DecisionOptions{Floor: 0.9})
		if _, err := around.Complete(t.Context(), schemaflux.Request{User: "u " + testEmail, Decision: decision}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(decider2.Last().Decision.State, testEmail) {
			t.Error("a redactor around the generator alone redacted what the decider was sent")
		}
	})

	t.Run("the cache keys on what the provider is sent, so requests that differ only in what was redacted share an answer", func(t *testing.T) {
		t.Parallel()
		redactorOutside, outsideCalls := answering("answer")
		shared := Chain(redactorOutside, Redact(), Cache())
		shared.Complete(t.Context(), ask("mail alice@example.com"))
		shared.Complete(t.Context(), ask("mail bob@example.com"))
		if outsideCalls.Load() != 1 {
			t.Errorf("with the redactor outside the cache the provider was asked %d times for two addresses, want 1: it was sent one text", outsideCalls.Load())
		}

		cacheOutside, insideCalls := answering("answer")
		keyed := Chain(cacheOutside, Cache(), Redact())
		keyed.Complete(t.Context(), ask("mail alice@example.com"))
		keyed.Complete(t.Context(), ask("mail bob@example.com"))
		if insideCalls.Load() != 2 {
			t.Errorf("with the cache outside the redactor the provider was asked %d times, want 2", insideCalls.Load())
		}
	})

	t.Run("a typed answer is given back as the caller's own option with the cache on either side", func(t *testing.T) {
		t.Parallel()
		for name, build := range map[string]func(schemaflux.Provider) schemaflux.Provider{
			"redactor outside": func(p schemaflux.Provider) schemaflux.Provider { return Chain(p, Redact(), Cache()) },
			"cache outside":    func(p schemaflux.Provider) schemaflux.Provider { return Chain(p, Cache(), Redact()) },
		} {
			d := new(schemafluxtest.Decider)
			d.Respond(func(_ int, decision schemaflux.Decision) ([]schemaflux.DecisionAnswer, error) {
				return []schemaflux.DecisionAnswer{schemafluxtest.Chose(decision.Questions[0].Options[1], 0.9)}, nil
			})
			chain := build(d)
			for call := 1; call <= 3; call++ {
				resp, err := chain.Complete(t.Context(), schemaflux.Request{User: "u", Decision: choice("which?", "plain", testEmail)})
				if err != nil || resp.Answers[0].Label != testEmail {
					t.Fatalf("%s, call %d: %+v, %v", name, call, resp.Answers, err)
				}
			}
			if d.Calls() != 1 {
				t.Errorf("%s: the decision provider was asked %d times, want 1", name, d.Calls())
			}
		}
	})
}

func TestTheCacheGoesOutsideTheLimitersAndTheBudget(t *testing.T) {
	t.Parallel()

	t.Run("a hit takes no slot of a rate limit when the cache is outside it", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name      string
			build     func(schemaflux.Provider) schemaflux.Provider
			wantError bool
		}{
			{"cache outside", func(p schemaflux.Provider) schemaflux.Provider {
				return Chain(p, Cache(), RateLimit(1, time.Hour, RateLimitOptions{Reject: true}))
			}, false},
			{"cache inside", func(p schemaflux.Provider) schemaflux.Provider {
				return Chain(p, RateLimit(1, time.Hour, RateLimitOptions{Reject: true}), Cache())
			}, true},
		} {
			p, _ := answering("answer")
			chain := tc.build(p)
			var last error
			for range 3 {
				_, last = chain.Complete(t.Context(), ask("same"))
			}
			if (last != nil) != tc.wantError {
				t.Errorf("%s: the third identical request returned %v, want an error = %v", tc.name, last, tc.wantError)
			}
		}
	})

	t.Run("a spent budget still serves what is in the cache when the cache is outside it", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name      string
			build     func(schemaflux.Provider) schemaflux.Provider
			wantError bool
		}{
			{"cache outside", func(p schemaflux.Provider) schemaflux.Provider {
				return Chain(p, Cache(), Budget(1.5, BudgetOptions{Cost: priced(1)}))
			}, false},
			{"cache inside", func(p schemaflux.Provider) schemaflux.Provider {
				return Chain(p, Budget(1.5, BudgetOptions{Cost: priced(1)}), Cache())
			}, true},
		} {
			p, _ := answering("answer")
			chain := tc.build(p)
			chain.Complete(t.Context(), ask("same"))
			_, err := chain.Complete(t.Context(), ask("same"))
			if (err != nil) != tc.wantError {
				t.Errorf("%s: a repeat after the budget was spent returned %v, want an error = %v", tc.name, err, tc.wantError)
			}
		}
	})

	bubble(t, "a hedge outside a cache hedges nothing: the second copy joins the first, and the provider is asked once", func(t *testing.T) {
		slow := func() *copies {
			return &copies{steps: []copyStep{{delay: 500 * time.Millisecond, body: "slow"}, {delay: time.Millisecond, body: "quick"}}}
		}

		cacheOutside := slow()
		start := time.Now()
		resp, err := Chain(cacheOutside, Cache(), Hedge(10*time.Millisecond)).Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "quick" || time.Since(start) != 11*time.Millisecond || cacheOutside.n.Load() != 2 {
			t.Errorf("cache outside the hedge: %q after %v, %d requests, %v; want the quick copy's answer after 11ms", resp.Content, time.Since(start), cacheOutside.n.Load(), err)
		}

		hedgeOutside := slow()
		start = time.Now()
		resp, err = Chain(hedgeOutside, Hedge(10*time.Millisecond), Cache()).Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "slow" || time.Since(start) != 500*time.Millisecond || hedgeOutside.n.Load() != 1 {
			t.Errorf("hedge outside the cache: %q after %v, %d requests, %v; want the slow answer after 500ms from one request", resp.Content, time.Since(start), hedgeOutside.n.Load(), err)
		}
		// Both cache calls finish when the one provider call finishes. Either
		// can win the hedge's result race: the leader is not marked cached,
		// while the coalesced follower is. Both outcomes must be hedged.
		if !resp.Hedged {
			t.Error("hedge outside the cache: the answer was not marked hedged")
		}
	})

	t.Run("a call that took several attempts is stored once, and the repeats cost nothing", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			var p schemafluxtest.Provider
			p.FailThen(2, unavailable(), "answer")
			chain := Chain(&p, Cache(), Retry())
			for range 4 {
				if resp, err := chain.Complete(t.Context(), ask("q")); err != nil || resp.Content != "answer" {
					t.Fatalf("%q, %v", resp.Content, err)
				}
			}
			if p.Calls() != 3 {
				t.Errorf("the provider was asked %d times, want 3: the attempts of the first request", p.Calls())
			}
		})
	})
}

func TestTheLimitersGoInsideTheRetryAndTheHedge(t *testing.T) {
	t.Parallel()

	bubble(t, "a rate limit inside a retry counts each attempt, and one outside it counts the call", func(t *testing.T) {
		quick := RetryOptions{BaseDelay: 10 * time.Millisecond, MaxDelay: 10 * time.Millisecond}

		var inside schemafluxtest.Provider
		inside.FailThen(1, unavailable(), "answer")
		start := time.Now()
		_, err := Chain(&inside, Retry(quick), RateLimit(1, time.Second, RateLimitOptions{Reject: true})).Complete(t.Context(), ask("q"))
		if err != nil || time.Since(start) < time.Second {
			t.Errorf("limiter inside the retry: %v after %v, want the repeat to wait for the limiter's refill", err, time.Since(start))
		}

		var outside schemafluxtest.Provider
		outside.FailThen(1, unavailable(), "answer")
		start = time.Now()
		_, err = Chain(&outside, RateLimit(1, time.Second, RateLimitOptions{Reject: true}), Retry(quick)).Complete(t.Context(), ask("q"))
		if err != nil || time.Since(start) != 10*time.Millisecond {
			t.Errorf("limiter outside the retry: %v after %v, want only the retry's own 10ms: the repeat went past the limiter", err, time.Since(start))
		}
	})

	bubble(t, "a retry outside a hedge hedges each attempt: two attempts send four requests", func(t *testing.T) {
		c := &copies{steps: []copyStep{{delay: 50 * time.Millisecond, err: unavailable()}}}
		_, err := Chain(c, Retry(RetryOptions{Retries: 1, BaseDelay: time.Millisecond}), Hedge(10*time.Millisecond)).Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrUnavailable)
		if c.n.Load() != 4 {
			t.Errorf("%d requests were sent, want 2 attempts x 2 copies", c.n.Load())
		}
	})

	bubble(t, "a bulkhead inside a hedge counts both copies, and one outside it counts the request", func(t *testing.T) {
		outside := &copies{steps: []copyStep{{hang: true}, {delay: time.Millisecond, body: "second"}}}
		resp, err := Chain(outside, Bulkhead(1, BulkheadOptions{Reject: true}), Hedge(10*time.Millisecond)).Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "second" {
			t.Errorf("bulkhead outside the hedge: %q, %v, want the second copy's answer", resp.Content, err)
		}

		inside := &copies{steps: []copyStep{{delay: 100 * time.Millisecond, body: "first"}}}
		resp, err = Chain(inside, Hedge(10*time.Millisecond), Bulkhead(1, BulkheadOptions{Reject: true})).Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "first" || inside.n.Load() != 1 {
			t.Errorf("bulkhead inside the hedge: %q, %v, %d requests reached the provider, want the second copy refused as busy and one request sent", resp.Content, err, inside.n.Load())
		}
	})

	bubble(t, "a limiter outside a bulkhead holds no slot while it waits, and one inside holds the slot", func(t *testing.T) {
		second := func(chain schemaflux.Provider) error {
			chain.Complete(t.Context(), ask("takes the only token"))
			first := started(t.Context(), chain)
			synctest.Wait()
			_, err := chain.Complete(t.Context(), ask("q"))
			<-first
			return err
		}
		var limiterOutside schemafluxtest.Provider
		if err := second(Chain(&limiterOutside, RateLimit(1, time.Second), Bulkhead(1, BulkheadOptions{Reject: true}))); err != nil {
			t.Errorf("limiter outside the bulkhead: the second request failed with %v, want it to wait for a token", err)
		}
		var limiterInside schemafluxtest.Provider
		wantKind(t, second(Chain(&limiterInside, Bulkhead(1, BulkheadOptions{Reject: true}), RateLimit(1, time.Second))), schemaflux.ErrBusy)
	})
}

func TestTheBreakerGoesInsideTheRateLimit(t *testing.T) {
	t.Parallel()

	bubble(t, "a rate limit that rejects, inside a breaker, opens the circuit on a healthy provider", func(t *testing.T) {
		settings := BreakerOptions{Failures: 2, OpenFor: time.Hour}
		limit := RateLimitOptions{Reject: true}

		var inside schemafluxtest.Provider
		limiterInside := Chain(&inside, CircuitBreaker(settings), RateLimit(1, time.Second, limit))
		for range 3 {
			limiterInside.Complete(t.Context(), ask("q"))
		}
		time.Sleep(2 * time.Second)
		_, err := limiterInside.Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrUnavailable)

		var outside schemafluxtest.Provider
		limiterOutside := Chain(&outside, RateLimit(1, time.Second, limit), CircuitBreaker(settings))
		for range 3 {
			limiterOutside.Complete(t.Context(), ask("q"))
		}
		time.Sleep(2 * time.Second)
		if _, err := limiterOutside.Complete(t.Context(), ask("q")); err != nil {
			t.Errorf("limiter outside the breaker: %v, want the healthy provider answered once the limiter refilled", err)
		}
	})
}

// The order the package comment gives has the Retry between the Fallback and the
// CircuitBreaker. A Retry that repeated the breaker's refusal would hold the
// Fallback off the alternate for a wait on every request while the primary is down.
func TestTheFallbackGoesOutsideTheRetryAndTheBreaker(t *testing.T) {
	t.Parallel()

	bubble(t, "an open circuit is left for the alternate at once, with a retry between them", func(t *testing.T) {
		primary := failingProvider("primary", unavailable())
		alternate, alternateP := route("alternate", "alternate's")
		chain := Chain(primary,
			Fallback([]Route{alternate}),
			Retry(RetryOptions{Retries: 3, BaseDelay: 10 * time.Second}),
			CircuitBreaker(BreakerOptions{Failures: 1, OpenFor: time.Hour}))

		// The first request opens the circuit with the failure it has, and is
		// repeated once into the open circuit: that is the one wait it pays.
		start := time.Now()
		if resp, err := chain.Complete(t.Context(), ask("q")); err != nil || resp.Content != "alternate's" {
			t.Fatalf("first request: %q, %v", resp.Content, err)
		}
		if waited := time.Since(start); waited < 10*time.Second || waited > 30*time.Second {
			t.Errorf("the first request waited %v, want the one backoff before the repeat that found the circuit open", waited)
		}

		// Every one after it finds the circuit open and goes on to the alternate with
		// no wait at all.
		start = time.Now()
		for range 5 {
			if resp, err := chain.Complete(t.Context(), ask("q")); err != nil || resp.Content != "alternate's" {
				t.Fatalf("%q, %v", resp.Content, err)
			}
		}
		if waited := time.Since(start); waited != 0 {
			t.Errorf("requests while the circuit was open waited %v, want 0: the Retry repeated the breaker's refusal", waited)
		}
		if primary.Calls() != 1 || alternateP.Calls() != 6 {
			t.Errorf("the primary was asked %d times and the alternate %d, want 1 and 6", primary.Calls(), alternateP.Calls())
		}
	})

	bubble(t, "a retry outside the breaker, inside nothing, returns its refusal without a wait", func(t *testing.T) {
		primary := failingProvider("primary", unavailable())
		chain := Chain(primary,
			Retry(RetryOptions{Retries: 3, BaseDelay: 10 * time.Second}),
			CircuitBreaker(BreakerOptions{Failures: 1, OpenFor: time.Hour}))
		chain.Complete(t.Context(), ask("q"))
		start := time.Now()
		_, err := chain.Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrUnavailable)
		if waited := time.Since(start); waited != 0 {
			t.Errorf("a request that found the circuit open waited %v, want 0", waited)
		}
	})
}

func TestTheFallbackGoesOutsideTheBreaker(t *testing.T) {
	t.Parallel()

	t.Run("an open circuit sends the request to the alternate unsent, and the primary is not asked again", func(t *testing.T) {
		t.Parallel()
		primary := failingProvider("primary", unavailable())
		alternate, alternateP := route("alternate", "alternate's")
		chain := Chain(primary, Fallback([]Route{alternate}), CircuitBreaker(BreakerOptions{Failures: 1}))
		for range 4 {
			if resp, err := chain.Complete(t.Context(), ask("q")); err != nil || resp.Content != "alternate's" {
				t.Fatalf("%q, %v", resp.Content, err)
			}
		}
		if primary.Calls() != 1 || alternateP.Calls() != 4 {
			t.Errorf("the primary was asked %d times and the alternate %d, want 1 and 4", primary.Calls(), alternateP.Calls())
		}
	})

	t.Run("a breaker outside the fallback sees only the alternate's successes and never opens", func(t *testing.T) {
		t.Parallel()
		primary := failingProvider("primary", unavailable())
		alternate, _ := route("alternate", "alternate's")
		chain := Chain(primary, CircuitBreaker(BreakerOptions{Failures: 1}), Fallback([]Route{alternate}))
		for range 4 {
			chain.Complete(t.Context(), ask("q"))
		}
		if primary.Calls() != 4 {
			t.Errorf("the primary was asked %d times, want 4: the breaker never learned it was down", primary.Calls())
		}
	})
}

// pricedAnswerOf reports whether a cost function was asked to price an answer of
// total tokens. It is asked about the request too, before it is sent, and that
// estimate is not an answer.
func pricedAnswerOf(seen []int, total int) bool { return slices.Contains(seen, total) }

func TestTheBudgetGoesOutsideTheFallbackAndAroundADecisionProviderForItsCalls(t *testing.T) {
	t.Parallel()

	t.Run("a budget outside a fallback counts the route that answered", func(t *testing.T) {
		t.Parallel()
		alternate, _ := route("alternate", "x")
		// A request is reserved at 1 and costs 1: one fits under 1.5 and a second would
		// take the ledger to 2.
		chain := Chain(failingProvider("primary", unavailable()), Budget(1.5, BudgetOptions{Cost: priced(1)}), Fallback([]Route{alternate}))
		if _, err := chain.Complete(t.Context(), ask("q")); err != nil {
			t.Fatal(err)
		}
		_, err := chain.Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrBudgetExceeded)
	})

	t.Run("a budget inside the primary route does not count the alternate", func(t *testing.T) {
		t.Parallel()
		alternate, alternateP := route("alternate", "x")
		chain := Chain(failingProvider("primary", unavailable()), Fallback([]Route{alternate}), Budget(1.5, BudgetOptions{Cost: priced(1)}))
		for range 3 {
			if _, err := chain.Complete(t.Context(), ask("q")); err != nil {
				t.Fatalf("the alternate's answers were refused by a budget that sees only the primary: %v", err)
			}
		}
		if alternateP.Calls() != 3 {
			t.Errorf("the alternate was asked %d times, want 3", alternateP.Calls())
		}
	})

	for _, outsideTheHedge := range []bool{true, false} {
		name := "a budget inside a hedge counts the answer that was used and not the copy that was canceled"
		if outsideTheHedge {
			name = "a budget outside a hedge counts the answer that was used and not the copy that was canceled"
		}
		bubble(t, name, func(t *testing.T) {
			c := &copies{steps: []copyStep{{hang: true}, {delay: time.Millisecond, body: "second"}, {delay: time.Millisecond, body: "quick"}}}
			// A request is reserved at 1 and costs 1 when it is answered: two fit under
			// 2.5, and a third would take the ledger to 3. A copy that was counted as
			// well would take the second.
			ledger := Budget(2.5, BudgetOptions{Cost: priced(1)})
			var chain schemaflux.Provider
			if outsideTheHedge {
				chain = Chain(c, ledger, Hedge(10*time.Millisecond))
			} else {
				chain = Chain(c, Hedge(10*time.Millisecond), ledger)
			}
			for i := 1; i <= 2; i++ {
				if _, err := chain.Complete(t.Context(), ask("q")); err != nil {
					t.Fatalf("request %d was refused, so a copy that was canceled was counted: %v", i, err)
				}
				// The copy that lost is canceled and ends on its own goroutine; the
				// reservation it held goes back when it does.
				synctest.Wait()
			}
			_, err := chain.Complete(t.Context(), ask("q"))
			wantKind(t, err, schemaflux.ErrBudgetExceeded)
		})
	}

	t.Run("a budget outside a decision pair counts the generating provider's answer and a budget around the decision provider counts its own", func(t *testing.T) {
		t.Parallel()
		// The two providers report different token counts, so the count tells whose
		// answer the ledger priced.
		generatorUsage := schemaflux.Usage{Input: 380, Output: 40, Total: 420}
		deciderUsage := schemaflux.Usage{Input: 300, Output: 40, Total: 340}
		seen := func(into *[]int) func(schemaflux.Usage, string, string) schemaflux.Cost {
			var mu sync.Mutex
			return func(u schemaflux.Usage, _, _ string) schemaflux.Cost {
				mu.Lock()
				defer mu.Unlock()
				*into = append(*into, u.Total)
				return schemaflux.Cost{Total: 0.1, Priced: true}
			}
		}

		var outsideSaw []int
		generator, decider := pair(schemafluxtest.Yes(0.3))
		generator.SetUsage(generatorUsage)
		decider.SetUsage(deciderUsage)
		outside := Budget(100, BudgetOptions{Cost: seen(&outsideSaw)})(Decisions(generator, decider, DecisionOptions{Floor: 0.9}))
		if resp, err := outside.Complete(t.Context(), deciding(yesNo(`"x"`, "is it?"))); err != nil || resp.Escalation == nil {
			t.Fatalf("%+v, %v", resp, err)
		}
		if !pricedAnswerOf(outsideSaw, 420) || pricedAnswerOf(outsideSaw, 340) {
			t.Errorf("a budget outside the pair priced %v tokens, want the generating provider's 420 and not the decision provider's 340", outsideSaw)
		}

		var aroundSaw []int
		generator, decider = pair(schemafluxtest.Yes(0.3))
		generator.SetUsage(generatorUsage)
		decider.SetUsage(deciderUsage)
		around := Decisions(generator, Budget(100, BudgetOptions{Cost: seen(&aroundSaw)})(decider), DecisionOptions{Floor: 0.9})
		if resp, err := around.Complete(t.Context(), deciding(yesNo(`"x"`, "is it?"))); err != nil || resp.Escalation == nil {
			t.Fatalf("%+v, %v", resp, err)
		}
		if !pricedAnswerOf(aroundSaw, 340) || pricedAnswerOf(aroundSaw, 420) {
			t.Errorf("a budget around the decision provider priced %v tokens, want its own 340 and not the generating provider's 420", aroundSaw)
		}
	})
}
