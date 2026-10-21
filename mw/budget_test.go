package mw

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/pricing"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// served sends n requests and returns how many were answered before the first
// refusal, and that refusal.
func served(t *testing.T, p schemaflux.Provider, n int) (answered int, refusal error) {
	t.Helper()
	for range n {
		if _, err := p.Complete(t.Context(), ask("q")); err != nil {
			return answered, err
		}
		answered++
	}
	return answered, nil
}

// tokenCost is a cost function that prices every token, input and output alike,
// at perToken, and knows every model. A request that sends one byte and names an
// output ceiling of 99 is estimated at 100 tokens by it, which is 1.0 at a cent
// a token.
func tokenCost(perToken float64) func(schemaflux.Usage, string, string) schemaflux.Cost {
	return func(u schemaflux.Usage, _, _ string) schemaflux.Cost {
		return schemaflux.Cost{Total: float64(u.Input+u.Output) * perToken, Priced: true, Currency: "USD"}
	}
}

// hundredTokens is a request the budget estimates at 100 tokens.
func hundredTokens() schemaflux.Request {
	return schemaflux.Request{User: "q", MaxTokens: 99}
}

// reporting is a provider that answers with usage, and counts the requests.
func reporting(usage schemaflux.Usage) (*schemafluxtest.Provider, *atomic.Int32) {
	var n atomic.Int32
	p := new(schemafluxtest.Provider)
	p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
		n.Add(1)
		return schemaflux.Response{Content: "answer", Model: "test-model", Usage: usage}, nil
	})
	return p, &n
}

func TestBudget(t *testing.T) {
	t.Parallel()

	t.Run("requests are served while what is spent and the request's estimate are within the limit", func(t *testing.T) {
		t.Parallel()
		p, _ := answering("answer")
		answered, err := served(t, Budget(10, BudgetOptions{Cost: priced(1)})(p), 5)
		if answered != 5 || err != nil {
			t.Errorf("answered %d, err %v, want 5 and none", answered, err)
		}
	})

	t.Run("a request that would cross the limit is refused before it is sent", func(t *testing.T) {
		t.Parallel()
		p, calls := answering("answer")
		// A call is estimated at 1 and costs 1. Two fit under 2.5 and the third
		// would take the ledger to 3.
		answered, err := served(t, Budget(2.5, BudgetOptions{Cost: priced(1)})(p), 10)
		wantKind(t, err, schemaflux.ErrBudgetExceeded)
		if answered != 2 || calls.Load() != 2 {
			t.Errorf("answered %d, provider asked %d times, want 2 and 2", answered, calls.Load())
		}
	})

	t.Run("a request whose own estimate is over the limit is not sent at all", func(t *testing.T) {
		t.Parallel()
		p, calls := answering("answer")
		_, err := Budget(0.5, BudgetOptions{Cost: priced(1)})(p).Complete(t.Context(), ask("q"))
		e := wantKind(t, err, schemaflux.ErrBudgetExceeded)
		if calls.Load() != 0 {
			t.Errorf("the provider was asked %d times, want 0", calls.Load())
		}
		if e.Message == "" || e.Op != "mw.budget" {
			t.Errorf("the refusal is %+v, want a message and the op of the layer", e)
		}
	})

	t.Run("the output ceiling the request names is part of its estimate", func(t *testing.T) {
		t.Parallel()
		p, calls := reporting(schemaflux.Usage{Input: 1, Output: 99})
		budget := Budget(5, BudgetOptions{Cost: tokenCost(0.01)})(p)
		if _, err := budget.Complete(t.Context(), schemaflux.Request{User: "q", MaxTokens: 99}); err != nil {
			t.Fatalf("a request estimated at 1.0 under a limit of 5: %v", err)
		}
		_, err := budget.Complete(t.Context(), schemaflux.Request{User: "q", MaxTokens: 999})
		wantKind(t, err, schemaflux.ErrBudgetExceeded)
		if calls.Load() != 1 {
			t.Errorf("the provider was asked %d times, want 1: the request with a ceiling of 999 reserves 10", calls.Load())
		}
	})

	t.Run("a refusal is made before the provider is asked", func(t *testing.T) {
		t.Parallel()
		p, calls := answering("answer")
		budget := Budget(1.5, BudgetOptions{Cost: priced(1)})(p)
		served(t, budget, 2)
		before := calls.Load()
		for range 3 {
			_, err := budget.Complete(t.Context(), ask("q"))
			wantKind(t, err, schemaflux.ErrBudgetExceeded)
		}
		if calls.Load() != before {
			t.Errorf("the provider was asked %d times after the refusals began, want 0", calls.Load()-before)
		}
	})

	t.Run("a limit of zero is no limit and nothing is counted", func(t *testing.T) {
		t.Parallel()
		p, _ := answering("answer")
		answered, err := served(t, Budget(0, BudgetOptions{Cost: priced(1000)})(p), 50)
		if answered != 50 || err != nil {
			t.Errorf("answered %d, err %v, want 50 and none", answered, err)
		}
	})

	t.Run("a limit that is negative, not a number or infinite fails every request as a configuration error", func(t *testing.T) {
		t.Parallel()
		for _, limit := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1)} {
			var p schemafluxtest.Provider
			_, err := Budget(limit)(&p).Complete(t.Context(), ask("q"))
			e := wantKind(t, err, schemaflux.ErrConfiguration)
			if e.Op != "mw.budget" || p.Calls() != 0 {
				t.Errorf("limit %v: Op %q, %d requests", limit, e.Op, p.Calls())
			}
		}
	})

	t.Run("a request that fails is not counted", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.FailThen(3, unavailable(), "answer")
		budget := Budget(2.5, BudgetOptions{Cost: priced(1)})(&p)
		for range 3 {
			if _, err := budget.Complete(t.Context(), ask("q")); err == nil {
				t.Fatal("a scripted failure succeeded")
			}
		}
		answered, err := served(t, budget, 2)
		if answered != 2 || err != nil {
			t.Errorf("answered %d after three failures, err %v, want 2 and none: failures were counted", answered, err)
		}
	})

	t.Run("a request that panics is an internal error and is not counted", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int32
		p := Func("p", func(context.Context, schemaflux.Request) (schemaflux.Response, error) {
			if calls.Add(1) <= 3 {
				panic("provider bug")
			}
			return schemaflux.Response{Content: "answer", Usage: schemaflux.Usage{Input: 1, Output: 1}}, nil
		})
		budget := Budget(2.5, BudgetOptions{Cost: priced(1)})(p)
		for range 3 {
			_, err := budget.Complete(t.Context(), ask("q"))
			wantKind(t, err, schemaflux.ErrInternal)
		}
		if answered, err := served(t, budget, 2); answered != 2 || err != nil {
			t.Errorf("answered %d after three panics, err %v, want 2 and none: the reservations were kept", answered, err)
		}
	})

	t.Run("a call nobody can price is refused before it is sent", func(t *testing.T) {
		t.Parallel()
		unpriced := func(schemaflux.Usage, string, string) schemaflux.Cost { return schemaflux.Cost{Total: 99} }
		notANumber := func(schemaflux.Usage, string, string) schemaflux.Cost {
			return schemaflux.Cost{Total: math.NaN(), Priced: true}
		}
		infinite := func(schemaflux.Usage, string, string) schemaflux.Cost {
			return schemaflux.Cost{Total: math.Inf(1), Priced: true}
		}
		negative := func(schemaflux.Usage, string, string) schemaflux.Cost {
			return schemaflux.Cost{Total: -5, Priced: true}
		}
		panics := func(schemaflux.Usage, string, string) schemaflux.Cost { panic("cost bug") }
		for name, cost := range map[string]func(schemaflux.Usage, string, string) schemaflux.Cost{
			"a cost with Priced false":     unpriced,
			"a total that is not a number": notANumber,
			"an infinite total":            infinite,
			"a negative total":             negative,
			"a cost function that panics":  panics,
		} {
			p, calls := answering("answer")
			_, err := Budget(1, BudgetOptions{Cost: cost})(p).Complete(t.Context(), ask("q"))
			e := wantKind(t, err, schemaflux.ErrBudgetExceeded)
			if calls.Load() != 0 {
				t.Errorf("%s: the provider was asked %d times, want 0", name, calls.Load())
			}
			mustNotContain(t, name+": the refusal", e.Error(), "test-model")
		}
	})

	t.Run("with AllowUnpriced a call nobody can price goes through and counts as nothing", func(t *testing.T) {
		t.Parallel()
		unpriced := func(schemaflux.Usage, string, string) schemaflux.Cost { return schemaflux.Cost{Total: 99} }
		panics := func(schemaflux.Usage, string, string) schemaflux.Cost { panic("cost bug") }
		for name, cost := range map[string]func(schemaflux.Usage, string, string) schemaflux.Cost{
			"a cost with Priced false":    unpriced,
			"a cost function that panics": panics,
		} {
			p, _ := answering("answer")
			answered, err := served(t, Budget(1, BudgetOptions{Cost: cost, AllowUnpriced: true})(p), 20)
			if answered != 20 || err != nil {
				t.Errorf("%s: answered %d, err %v, want all 20", name, answered, err)
			}
		}
	})

	t.Run("an answer that reports no usage is counted at its reservation", func(t *testing.T) {
		t.Parallel()
		// A cost function prices zero tokens at zero, which is a figure for a call
		// nobody measured and not a measurement: such an answer is counted at what it
		// was reserved at, so a ceiling around a provider that reports nothing still
		// refuses.
		p, calls := reporting(schemaflux.Usage{})
		answered, err := servedRequest(t, Budget(10, BudgetOptions{Cost: tokenCost(0.01)})(p), hundredTokens(), 30)
		wantKind(t, err, schemaflux.ErrBudgetExceeded)
		if answered != 10 || calls.Load() != 10 {
			t.Errorf("answered %d, provider asked %d times, want 10 and 10: an answer with no usage was counted as free", answered, calls.Load())
		}
	})

	t.Run("with AllowUnpriced an answer that reports no usage counts as nothing", func(t *testing.T) {
		t.Parallel()
		p, _ := reporting(schemaflux.Usage{})
		answered, err := servedRequest(t, Budget(10, BudgetOptions{Cost: tokenCost(0.01), AllowUnpriced: true})(p), hundredTokens(), 30)
		if answered != 30 || err != nil {
			t.Errorf("answered %d, err %v, want all 30", answered, err)
		}
	})

	t.Run("Cost is given the usage, the model and the provider the answer names", func(t *testing.T) {
		t.Parallel()
		var mu sync.Mutex
		var gotUsage schemaflux.Usage
		var gotModel, gotProvider string
		cost := func(u schemaflux.Usage, model, provider string) schemaflux.Cost {
			mu.Lock()
			defer mu.Unlock()
			gotUsage, gotModel, gotProvider = u, model, provider
			return schemaflux.Cost{Priced: true}
		}
		var p schemafluxtest.Provider
		p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
			return schemaflux.Response{Content: "a", Model: "the-model", Provider: "the-provider",
				Usage: schemaflux.Usage{Input: 11, Output: 7, Total: 18, Cached: 3, Reasoning: 2}}, nil
		})
		if _, err := Budget(5, BudgetOptions{Cost: cost})(&p).Complete(t.Context(), ask("q")); err != nil {
			t.Fatal(err)
		}
		want := schemaflux.Usage{Input: 11, Output: 7, Total: 18, Cached: 3, Reasoning: 2}
		if gotUsage != want || gotModel != "the-model" || gotProvider != "the-provider" {
			t.Errorf("Cost was given %+v, %q, %q", gotUsage, gotModel, gotProvider)
		}
	})

	t.Run("Cost is asked for the estimate with the request's own model and the tokens the request holds", func(t *testing.T) {
		t.Parallel()
		var mu sync.Mutex
		var first schemaflux.Usage
		var firstModel string
		seen := 0
		cost := func(u schemaflux.Usage, model, _ string) schemaflux.Cost {
			mu.Lock()
			defer mu.Unlock()
			if seen++; seen == 1 {
				first, firstModel = u, model
			}
			return schemaflux.Cost{Priced: true}
		}
		var p schemafluxtest.Provider
		p.Reply("answer")
		req := schemaflux.Request{Model: "asked-for", System: "12345678", User: "1234567890", MaxTokens: 40}
		if _, err := Budget(5, BudgetOptions{Cost: cost})(&p).Complete(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		// 18 bytes at four to a token is 5 tokens, and the ceiling is 40.
		if first.Input != 5 || first.Output != 40 || firstModel != "asked-for" {
			t.Errorf("the estimate was asked of %+v and model %q, want 5 tokens in, 40 out and the request's model", first, firstModel)
		}
	})

	t.Run("a nil Cost is the price table", func(t *testing.T) {
		t.Parallel()
		const model = "mw-budget-priced-model"
		// $1000 per million tokens is a dollar for a thousand: a call of 10 input and
		// 5 output tokens costs 1.5 cents, and a request of one byte with a ceiling
		// of 5 is estimated at 6 tokens, 0.6 of a cent.
		restore, err := pricing.Register(pricing.Model{Provider: "mw-budget-test", Name: model, Input: 1000, Output: 1000})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(restore)
		var p schemafluxtest.Provider
		p.SetName("mw-budget-test")
		p.SetUsage(schemaflux.Usage{Input: 10, Output: 5, Total: 15})
		budget := Budget(0.02)(&p)
		var refusal error
		answered := 0
		for range 5 {
			_, err := budget.Complete(t.Context(), schemaflux.Request{Model: model, User: "q", MaxTokens: 5})
			if err != nil {
				refusal = err
				break
			}
			answered++
		}
		wantKind(t, refusal, schemaflux.ErrBudgetExceeded)
		if answered != 1 {
			t.Errorf("answered %d, want 1: 0.015 is spent, and 0.006 more would be over 0.02", answered)
		}
	})

	t.Run("a model the price table does not know is refused before it is sent", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.SetUsage(schemaflux.Usage{Input: 1000, Output: 1000, Total: 2000})
		_, err := Budget(0.000001)(&p).Complete(t.Context(), schemaflux.Request{Model: "no-such-model-in-the-table", User: "q"})
		wantKind(t, err, schemaflux.ErrBudgetExceeded)
		if p.Calls() != 0 {
			t.Errorf("the provider was asked %d times, want 0", p.Calls())
		}
	})

	t.Run("with AllowUnpriced a model the price table does not know counts as nothing", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.SetUsage(schemaflux.Usage{Input: 1000, Output: 1000, Total: 2000})
		answered, err := served(t, Budget(0.000001, BudgetOptions{AllowUnpriced: true})(&p), 10)
		if answered != 10 || err != nil {
			t.Errorf("answered %d, err %v", answered, err)
		}
	})

	t.Run("the ledger belongs to the provider the middleware is applied to", func(t *testing.T) {
		t.Parallel()
		budget := Budget(1.5, BudgetOptions{Cost: priced(1)})
		a, _ := answering("a")
		b, _ := answering("b")
		if answered, _ := served(t, budget(a), 10); answered != 1 {
			t.Fatalf("the first provider answered %d, want 1", answered)
		}
		if answered, _ := served(t, budget(b), 10); answered != 1 {
			t.Errorf("the second provider answered %d, want 1: it has a ledger of its own", answered)
		}
	})

	t.Run("a refusal is not repeated by Retry and says nothing of the request", func(t *testing.T) {
		t.Parallel()
		p, calls := answering("answer")
		chain := Chain(p, Retry(), Budget(1.5, BudgetOptions{Cost: priced(1)}))
		served(t, chain, 1)
		before := calls.Load()
		_, err := chain.Complete(t.Context(), ask("the secret text "+testEmail))
		e := wantKind(t, err, schemaflux.ErrBudgetExceeded)
		mustNotContain(t, "the error", err.Error(), "secret", testEmail)
		if e.Message == "" || calls.Load() != before {
			t.Errorf("message %q, %d further provider calls", e.Message, calls.Load()-before)
		}
	})

	t.Run("option values merge left to right with a later Cost that is not nil winning", func(t *testing.T) {
		t.Parallel()
		p, _ := answering("answer")
		answered, _ := served(t, Budget(2.5, BudgetOptions{Cost: priced(100)}, BudgetOptions{}, BudgetOptions{Cost: priced(1)})(p), 10)
		if answered != 2 {
			t.Errorf("answered %d, want 2: the last Cost prices a call at 1", answered)
		}
	})

	t.Run("AllowUnpriced loosens the budget, so it is refused in any value but the first", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		_, err := Budget(1, BudgetOptions{}, BudgetOptions{AllowUnpriced: true})(&p).Complete(t.Context(), ask("q"))
		e := wantKind(t, err, schemaflux.ErrConfiguration)
		if e.Op != "mw.budget" || p.Calls() != 0 {
			t.Errorf("Op %q, %d requests", e.Op, p.Calls())
		}
		first, _ := answering("answer")
		if answered, err := served(t, Budget(1, BudgetOptions{AllowUnpriced: true}, BudgetOptions{})(first), 5); answered != 5 || err != nil {
			t.Errorf("AllowUnpriced in the first value: answered %d, err %v", answered, err)
		}
	})
}

// servedRequest is served for requests of one shape.
func servedRequest(t *testing.T, p schemaflux.Provider, req schemaflux.Request, n int) (answered int, refusal error) {
	t.Helper()
	for range n {
		if _, err := p.Complete(t.Context(), req); err != nil {
			return answered, err
		}
		answered++
	}
	return answered, nil
}

// A thousand callers at the ceiling at once. The ledger holds the reservations of
// the requests in flight, so no more are admitted than the limit has room for, and
// the spend passes the limit by no more than the admitted requests cost beyond
// what they were estimated at: the bound the doc comment of Budget states.
func TestBudgetBoundsTheSpendUnderAThousandConcurrentCallers(t *testing.T) {
	t.Parallel()
	const callers, limit = 1000, 10.0
	cases := []struct {
		name string
		// usage is what each answer reports. The request is estimated at 100 tokens,
		// which is 1.0 at a cent a token.
		usage schemaflux.Usage
		// span is the most the spend can be, and reached says it is.
		span    float64
		reached bool
	}{
		{"estimates that are exact", schemaflux.Usage{Input: 1, Output: 99}, limit, true},
		{"estimates that are too high", schemaflux.Usage{Input: 1, Output: 49}, limit, false},
		// Ten requests are in flight, each reserved at 1.0 and costing 3.0.
		{"estimates that are too low", schemaflux.Usage{Input: 100, Output: 200}, limit + 10*(3-1), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			release := make(chan struct{})
			var inProvider, refused, answered atomic.Int64
			var p schemafluxtest.Provider
			p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
				inProvider.Add(1)
				<-release
				return schemaflux.Response{Content: "answer", Model: "m", Usage: tc.usage}, nil
			})
			budget := Budget(limit, BudgetOptions{Cost: tokenCost(0.01)})(&p)

			var wg sync.WaitGroup
			for range callers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := budget.Complete(t.Context(), hundredTokens()); err != nil {
						refused.Add(1)
						return
					}
					answered.Add(1)
				}()
			}
			deadline := time.Now().Add(20 * time.Second)
			for inProvider.Load()+refused.Load() < callers {
				if time.Now().After(deadline) {
					t.Fatalf("%d in the provider and %d refused of %d callers", inProvider.Load(), refused.Load(), callers)
				}
				time.Sleep(time.Millisecond)
			}
			close(release)
			wg.Wait()

			if got := inProvider.Load(); got > int64(limit) {
				t.Errorf("%d requests were in the provider at once, want at most %d: the limit has room to reserve no more", got, int(limit))
			}
			if got := answered.Load() + refused.Load(); got != callers {
				t.Errorf("%d answered and refused of %d callers", got, callers)
			}
			// Every answer costs the same, so the spend is the answers' count times it.
			total := float64(answered.Load()) * float64(tc.usage.Input+tc.usage.Output) * 0.01
			if total > tc.span+1e-9 {
				t.Errorf("spent %v, want at most %v", total, tc.span)
			}
			if tc.reached && total < tc.span-1e-9 {
				t.Errorf("spent %v, want the bound %v reached", total, tc.span)
			}
		})
	}
}
