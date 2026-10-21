package mw_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/mw"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// A chain is written outermost first. The redactor is outermost so that nothing
// unredacted reaches the cache or the provider, and the cache is outside the
// retry so that a repeat of a request costs nothing.
func ExampleChain() {
	var model schemafluxtest.Provider
	model.Reply("It is a fine day.")

	chain := mw.Chain(&model,
		mw.Redact(),
		mw.Cache(),
		mw.Retry(),
	)

	ctx := context.Background()
	for range 2 {
		resp, err := chain.Complete(ctx, schemaflux.Request{User: "Write to jane.doe@example.com about the weather."})
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(resp.Content, "cached:", resp.Cached)
	}
	fmt.Println("requests that reached the model:", model.Calls())
	fmt.Println("what it was sent:", model.Last().User)
	// Output:
	// It is a fine day. cached: false
	// It is a fine day. cached: true
	// requests that reached the model: 1
	// what it was sent: Write to [redacted:email] about the weather.
}

// Func writes a provider of a program's own: one at the bottom of a chain, with
// nothing underneath it.
func ExampleFunc() {
	echo := mw.Func("echo", func(_ context.Context, req schemaflux.Request) (schemaflux.Response, error) {
		return schemaflux.Response{Content: "you said: " + req.User}, nil
	})
	resp, _ := mw.Chain(echo, mw.Retry()).Complete(context.Background(), schemaflux.Request{User: "hello"})
	fmt.Println(resp.Content)
	// Output:
	// you said: hello
}

// A middleware of a program's own is a function from a provider to a provider,
// and Wrap writes the provider it returns, one that keeps what the provider
// underneath can do.
func ExampleWrap() {
	var model schemafluxtest.Provider
	model.Reply("answer")

	counted := 0
	counting := func(next schemaflux.Provider) schemaflux.Provider {
		return mw.Wrap(next, func(ctx context.Context, req schemaflux.Request, next schemaflux.Provider) (schemaflux.Response, error) {
			counted++
			return next.Complete(ctx, req)
		})
	}

	chain := mw.Chain(&model, counting, mw.Cache())
	chain.Complete(context.Background(), schemaflux.Request{User: "q"})
	chain.Complete(context.Background(), schemaflux.Request{User: "q"})
	fmt.Println("requests that reached the counter:", counted)
	fmt.Println("requests that reached the model:", model.Calls())
	// Output:
	// requests that reached the counter: 2
	// requests that reached the model: 1
}

// A failed request is sent again when repeating can cure it. The model here
// fails twice with an outage and then answers.
func ExampleRetry() {
	var model schemafluxtest.Provider
	model.FailThen(2, &schemaflux.Error{Kind: schemaflux.ErrUnavailable}, "answer")

	retrying := mw.Retry(mw.RetryOptions{Retries: 3, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})(&model)
	resp, err := retrying.Complete(context.Background(), schemaflux.Request{User: "q"})
	fmt.Println(resp.Content, err, "after", model.Calls(), "requests")

	// A negative Retries is none: the failure is returned as it came.
	var down schemafluxtest.Provider
	down.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable})
	once := mw.Retry(mw.RetryOptions{Retries: -1})(&down)
	_, err = once.Complete(context.Background(), schemaflux.Request{User: "q"})
	fmt.Println(errors.Is(err, schemaflux.ErrUnavailable), "after", down.Calls(), "request")
	// Output:
	// answer <nil> after 3 requests
	// true after 1 request
}

// A limiter that rejects refuses a request over the limit at once, and says how
// long until a slot is due, which Retry reads as the wait.
func ExampleRateLimit() {
	var model schemafluxtest.Provider
	limited := mw.RateLimit(1, time.Minute, mw.RateLimitOptions{Reject: true})(&model)

	ctx := context.Background()
	_, first := limited.Complete(ctx, schemaflux.Request{User: "q"})
	_, second := limited.Complete(ctx, schemaflux.Request{User: "q"})

	var refusal *schemaflux.Error
	errors.As(second, &refusal)
	fmt.Println(first, errors.Is(second, schemaflux.ErrRateLimited), refusal.RetryAfter > 59*time.Second)
	// Output:
	// <nil> true true
}

// After enough failures in a row the circuit opens and requests are refused
// without being sent.
func ExampleCircuitBreaker() {
	var model schemafluxtest.Provider
	model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable})
	breaker := mw.CircuitBreaker(mw.BreakerOptions{Failures: 2, OpenFor: time.Hour})(&model)

	ctx := context.Background()
	for range 4 {
		breaker.Complete(ctx, schemaflux.Request{User: "q"})
	}
	_, err := breaker.Complete(ctx, schemaflux.Request{User: "q"})
	fmt.Println("requests that reached the model:", model.Calls())
	fmt.Println("circuit open:", errors.Is(err, schemaflux.ErrUnavailable))
	// Output:
	// requests that reached the model: 2
	// circuit open: true
}

// A budget reserves what a request is estimated to cost before it sends it, and
// refuses the request that would take the spend past the ceiling.
func ExampleBudget() {
	var model schemafluxtest.Provider
	pricePerCall := func(schemaflux.Usage, string, string) schemaflux.Cost {
		return schemaflux.Cost{Total: 1, Priced: true, Currency: "USD"}
	}
	budget := mw.Budget(2.5, mw.BudgetOptions{Cost: pricePerCall})(&model)

	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		_, err := budget.Complete(ctx, schemaflux.Request{User: "q"})
		fmt.Println("request", i, "refused:", errors.Is(err, schemaflux.ErrBudgetExceeded))
	}
	fmt.Println("requests that reached the model:", model.Calls())
	// Output:
	// request 1 refused: false
	// request 2 refused: false
	// request 3 refused: true
	// requests that reached the model: 2
}

// An exact repeat is answered from memory. A partition keeps one tenant's answers
// from another's.
func ExampleCache() {
	var model schemafluxtest.Provider
	model.Reply("answer")

	var events []mw.CacheEvent
	cached := mw.Cache(mw.CacheOptions{OnEvent: func(e mw.CacheEvent) { events = append(events, e) }})(&model)

	ctx := context.Background()
	req := schemaflux.Request{User: "same question"}
	cached.Complete(ctx, req)
	cached.Complete(ctx, req)
	cached.Complete(mw.WithCachePartition(ctx, mw.CachePartition{Tenant: "acme"}), req)

	fmt.Println("requests that reached the model:", model.Calls())
	for _, e := range events {
		fmt.Printf("hit: %v tenant: %q\n", e.Hit, e.Tenant)
	}
	// Output:
	// requests that reached the model: 2
	// hit: false tenant: ""
	// hit: true tenant: ""
	// hit: false tenant: "acme"
}

// A request goes to the next route when the primary fails with an error that is
// about the route. The answer names the route that served it.
func ExampleFallback() {
	var primary schemafluxtest.Provider
	primary.SetName("primary")
	primary.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable})

	var spare schemafluxtest.Provider
	spare.SetName("spare")
	spare.Reply("from the spare")

	fallback := mw.Fallback([]mw.Route{{Provider: &spare}})(&primary)
	resp, err := fallback.Complete(context.Background(), schemaflux.Request{User: "q"})
	fmt.Println(resp.Content, "by", resp.Provider, err)

	// A failure that is not about the route is returned as it is.
	var locked schemafluxtest.Provider
	locked.SetName("locked")
	locked.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnauthorized})
	_, err = mw.Fallback([]mw.Route{{Provider: &spare}})(&locked).Complete(context.Background(), schemaflux.Request{User: "q"})
	fmt.Println(errors.Is(err, schemaflux.ErrUnauthorized))
	// Output:
	// from the spare by spare <nil>
	// true
}

// A hedge sends a second copy of a request that is slow, and the answer says it
// did. This one answers inside the delay, so it was sent once.
func ExampleHedge() {
	var model schemafluxtest.Provider
	model.Reply("answer")

	hedged := mw.Hedge(time.Hour)(&model)
	resp, err := hedged.Complete(context.Background(), schemaflux.Request{User: "q"})
	fmt.Println(resp.Content, err, "hedged:", resp.Hedged, "requests:", model.Calls())
	// Output:
	// answer <nil> hedged: false requests: 1
}

// What must not leave the process is removed before the provider is sent the
// request. The built-in rules cover credentials and email addresses; a program
// adds the shapes of its own domain.
func ExampleRedact() {
	var model schemafluxtest.Provider
	redacting := mw.Redact(mw.RedactOptions{
		Patterns: map[string]*regexp.Regexp{"phone": regexp.MustCompile(`\+\d[\d ]{8,}\d`)},
	})(&model)

	redacting.Complete(context.Background(), schemaflux.Request{User: "Call +1 415 555 0134 or write to jane.doe@example.com."})
	fmt.Println(model.Last().User)
	// Output:
	// Call [redacted:phone] or write to [redacted:email].
}

// A decision goes to the decision provider, which answers it typed. An answer
// under the floor is asked again of the generating provider, and the response
// says so.
func ExampleDecisions() {
	var generator schemafluxtest.Provider
	generator.Reply(`{"verdict":"yes"}`)
	decider := schemafluxtest.NewDecider(schemafluxtest.Yes(0.95), schemafluxtest.No(0.40))

	pair := mw.Decisions(&generator, decider, mw.DecisionOptions{Floor: 0.8})
	ask := func() schemaflux.Request {
		return schemaflux.Request{User: "Is the invoice paid?", Decision: &schemaflux.Decision{
			State:     `"Invoice A-17, paid 2026-09-30"`,
			Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionYesNo, Ask: "Is the invoice paid?"}},
		}}
	}

	confident, _ := pair.Complete(context.Background(), ask())
	fmt.Println("answered by the decision provider:", confident.Answers[0].Yes, confident.Escalation == nil)

	unsure, _ := pair.Complete(context.Background(), ask())
	fmt.Println("passed over:", unsure.Escalation.Reason, unsure.Escalation.ModelConfidence, "answered by", unsure.Content)
	// Output:
	// answered by the decision provider: true true
	// passed over: below the floor 0.4 answered by {"verdict":"yes"}
}
