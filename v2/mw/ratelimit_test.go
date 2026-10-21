package mw

import (
	"context"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// tick sends one request through p and reports how long it took on the fake
// clock of the bubble.
func tick(t *testing.T, p schemaflux.Provider) (time.Duration, error) {
	t.Helper()
	start := time.Now()
	_, err := p.Complete(t.Context(), ask("q"))
	return time.Since(start), err
}

func TestRateLimit(t *testing.T) {
	t.Parallel()

	bubble(t, "the first n requests go at once", func(t *testing.T) {
		var p schemafluxtest.Provider
		limited := RateLimit(3, time.Second)(&p)
		for i := range 3 {
			if waited, err := tick(t, limited); err != nil || waited != 0 {
				t.Fatalf("request %d: waited %v, err %v", i+1, waited, err)
			}
		}
		if p.Calls() != 3 {
			t.Errorf("the provider was asked %d times, want 3", p.Calls())
		}
	})

	bubble(t, "a request over the limit waits for the slot that is due", func(t *testing.T) {
		var p schemafluxtest.Provider
		limited := RateLimit(1, time.Second)(&p)
		if waited, _ := tick(t, limited); waited != 0 {
			t.Fatalf("the first request waited %v", waited)
		}
		waited, err := tick(t, limited)
		if err != nil || waited != time.Second {
			t.Errorf("the second request waited %v, err %v, want 1s", waited, err)
		}
	})

	bubble(t, "the rate is n per interval: two a second is a slot every half second", func(t *testing.T) {
		var p schemafluxtest.Provider
		limited := RateLimit(2, time.Second)(&p)
		tick(t, limited)
		tick(t, limited)
		if waited, _ := tick(t, limited); waited != 500*time.Millisecond {
			t.Errorf("the third request waited %v, want 500ms", waited)
		}
	})

	bubble(t, "a burst larger than n allows a big first batch and refills at the rate", func(t *testing.T) {
		var p schemafluxtest.Provider
		limited := RateLimit(1, time.Second, RateLimitOptions{Burst: 3})(&p)
		for i := range 3 {
			if waited, _ := tick(t, limited); waited != 0 {
				t.Fatalf("request %d waited %v", i+1, waited)
			}
		}
		if waited, _ := tick(t, limited); waited != time.Second {
			t.Errorf("the fourth request waited %v, want 1s", waited)
		}
	})

	bubble(t, "an idle limiter refills to its burst and no further", func(t *testing.T) {
		var p schemafluxtest.Provider
		limited := RateLimit(1, time.Second, RateLimitOptions{Burst: 2})(&p)
		tick(t, limited)
		tick(t, limited)
		time.Sleep(time.Hour)
		for i := range 2 {
			if waited, _ := tick(t, limited); waited != 0 {
				t.Fatalf("request %d after the idle hour waited %v", i+1, waited)
			}
		}
		if waited, _ := tick(t, limited); waited != time.Second {
			t.Errorf("the third request waited %v, want 1s: the bucket holds two", waited)
		}
	})

	bubble(t, "Reject fails a request over the limit at once with the wait that is due", func(t *testing.T) {
		var p schemafluxtest.Provider
		limited := RateLimit(1, time.Second, RateLimitOptions{Reject: true})(&p)
		tick(t, limited)
		waited, err := tick(t, limited)
		e := wantKind(t, err, schemaflux.ErrRateLimited)
		if waited != 0 {
			t.Errorf("a rejected request waited %v", waited)
		}
		if e.RetryAfter != time.Second {
			t.Errorf("RetryAfter = %v, want 1s", e.RetryAfter)
		}
		if p.Calls() != 1 {
			t.Errorf("the provider was asked %d times, want 1", p.Calls())
		}
	})

	bubble(t, "a refused request takes no slot, so the wait that was stated is enough", func(t *testing.T) {
		var p schemafluxtest.Provider
		limited := RateLimit(1, time.Second, RateLimitOptions{Reject: true})(&p)
		tick(t, limited)
		_, err := tick(t, limited)
		e := wantKind(t, err, schemaflux.ErrRateLimited)
		time.Sleep(e.RetryAfter)
		if _, err := tick(t, limited); err != nil {
			t.Errorf("the request after the stated wait failed: %v", err)
		}
	})

	bubble(t, "Retry reads the refusal's RetryAfter as the wait, as it reads a provider's 429", func(t *testing.T) {
		var p schemafluxtest.Provider
		chain := Chain(&p, Retry(), RateLimit(1, 2*time.Second, RateLimitOptions{Reject: true}))
		tick(t, chain)
		waited, err := tick(t, chain)
		if err != nil || waited != 2*time.Second {
			t.Errorf("the second request waited %v, err %v, want the refusal's 2s", waited, err)
		}
		if p.Calls() != 2 {
			t.Errorf("the provider was asked %d times, want 2", p.Calls())
		}
	})

	bubble(t, "a context that ends during the wait ends the request with the context's error", func(t *testing.T) {
		var p schemafluxtest.Provider
		limited := RateLimit(1, time.Minute)(&p)
		tick(t, limited)
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		_, err := limited.Complete(ctx, ask("q"))
		wantKind(t, err, schemaflux.ErrTimeout)
		if p.Calls() != 1 {
			t.Errorf("the provider was asked %d times, want 1", p.Calls())
		}
	})

	bubble(t, "an interval of zero is a second", func(t *testing.T) {
		var p schemafluxtest.Provider
		chain := RateLimit(1, 0)(&p)
		tick(t, chain)
		if waited, _ := tick(t, chain); waited != time.Second {
			t.Errorf("the second request waited %v, want 1s", waited)
		}
	})

	t.Run("an n below one and an interval below zero fail every request as a configuration error", func(t *testing.T) {
		t.Parallel()
		for name, limited := range map[string]Middleware{
			"n of zero":          RateLimit(0, time.Second),
			"a negative n":       RateLimit(-5, time.Second),
			"a negative n alone": RateLimit(-1, 0),
			"a negative per":     RateLimit(1, -time.Hour),
			"the smallest per":   RateLimit(1, time.Duration(-1<<63)),
		} {
			var p schemafluxtest.Provider
			_, err := limited(&p).Complete(t.Context(), ask("q"))
			e := wantKind(t, err, schemaflux.ErrConfiguration)
			if e.Op != "mw.ratelimit" || p.Calls() != 0 {
				t.Errorf("%s: Op %q, %d requests", name, e.Op, p.Calls())
			}
		}
	})

	t.Run("a huge interval states a wait the library bounds and never one in the past", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		limited := RateLimit(1, time.Duration(1<<63-1), RateLimitOptions{Reject: true})(&p)
		limited.Complete(t.Context(), ask("q"))
		_, err := limited.Complete(t.Context(), ask("q"))
		e := wantKind(t, err, schemaflux.ErrRateLimited)
		if e.RetryAfter <= 0 || e.RetryAfter > 2*time.Minute {
			t.Errorf("RetryAfter = %v, want a wait ahead of at most two minutes", e.RetryAfter)
		}
	})

	bubble(t, "one middleware applied to two providers limits them together", func(t *testing.T) {
		var a, b schemafluxtest.Provider
		limit := RateLimit(1, time.Second)
		tick(t, limit(&a))
		if waited, _ := tick(t, limit(&b)); waited != time.Second {
			t.Errorf("the second provider's request waited %v, want 1s", waited)
		}
	})

	bubble(t, "a negative burst is a configuration error and nothing is sent", func(t *testing.T) {
		var p schemafluxtest.Provider
		_, err := RateLimit(1, time.Second, RateLimitOptions{Burst: -1})(&p).Complete(t.Context(), ask("q"))
		e := wantKind(t, err, schemaflux.ErrConfiguration)
		if e.Op != "mw.ratelimit" || p.Calls() != 0 {
			t.Errorf("Op %q, %d requests", e.Op, p.Calls())
		}
	})

	bubble(t, "option values merge left to right with a later non-zero field winning", func(t *testing.T) {
		var p schemafluxtest.Provider
		limited := RateLimit(1, time.Second, RateLimitOptions{Burst: 5}, RateLimitOptions{Reject: true}, RateLimitOptions{Burst: 2})(&p)
		tick(t, limited)
		tick(t, limited)
		_, err := tick(t, limited)
		wantKind(t, err, schemaflux.ErrRateLimited)
		if p.Calls() != 2 {
			t.Errorf("the provider was asked %d times, want 2: a burst of 2 and Reject", p.Calls())
		}
	})

	bubble(t, "a refusal carries no text of the request", func(t *testing.T) {
		var p schemafluxtest.Provider
		limited := RateLimit(1, time.Second, RateLimitOptions{Reject: true})(&p)
		tick(t, limited)
		_, err := limited.Complete(t.Context(), ask("the secret text "+testEmail))
		wantKind(t, err, schemaflux.ErrRateLimited)
		mustNotContain(t, "the error", err.Error(), "secret", testEmail)
	})
}
