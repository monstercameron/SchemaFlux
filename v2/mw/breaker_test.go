package mw

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// switched is a provider that fails every request with err while its switch is
// set, and answers otherwise.
type switched struct {
	*schemafluxtest.Provider
	failing atomic.Bool
}

func newSwitched(err error) *switched {
	s := &switched{Provider: new(schemafluxtest.Provider)}
	s.failing.Store(true)
	s.Provider.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
		if s.failing.Load() {
			return schemaflux.Response{}, err
		}
		return schemaflux.Response{Content: "answer"}, nil
	})
	return s
}

// fail sends n requests and expects each to fail.
func fail(t *testing.T, p schemaflux.Provider, n int) {
	t.Helper()
	for i := range n {
		if _, err := p.Complete(t.Context(), ask("q")); err == nil {
			t.Fatalf("request %d did not fail", i+1)
		}
	}
}

func TestCircuitBreaker(t *testing.T) {
	t.Parallel()

	bubble(t, "the circuit opens after the failures and refuses without sending", func(t *testing.T) {
		s := newSwitched(unavailable())
		breaker := CircuitBreaker(BreakerOptions{Failures: 3})(s)
		fail(t, breaker, 3)
		_, err := breaker.Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrUnavailable)
		if s.Calls() != 3 {
			t.Errorf("the provider was asked %d times, want 3: the fourth was refused unsent", s.Calls())
		}
	})

	bubble(t, "the default is five failures", func(t *testing.T) {
		s := newSwitched(unavailable())
		breaker := CircuitBreaker()(s)
		fail(t, breaker, 4)
		if _, err := breaker.Complete(t.Context(), ask("q")); err == nil || s.Calls() != 5 {
			t.Fatalf("the fifth request: err %v, %d provider calls, want it sent and failed", err, s.Calls())
		}
		fail(t, breaker, 1)
		if s.Calls() != 5 {
			t.Errorf("the provider was asked %d times, want 5: the circuit is open after five", s.Calls())
		}
	})

	bubble(t, "a success clears the count of failures in a row", func(t *testing.T) {
		s := newSwitched(unavailable())
		breaker := CircuitBreaker(BreakerOptions{Failures: 3})(s)
		fail(t, breaker, 2)
		s.failing.Store(false)
		if _, err := breaker.Complete(t.Context(), ask("q")); err != nil {
			t.Fatal(err)
		}
		s.failing.Store(true)
		fail(t, breaker, 2)
		if _, err := breaker.Complete(t.Context(), ask("q")); err == nil || s.Calls() != 6 {
			t.Errorf("err %v, %d provider calls: two failures after a success should not have opened it", err, s.Calls())
		}
	})

	bubble(t, "after OpenFor a probe goes through, and a success closes the circuit", func(t *testing.T) {
		s := newSwitched(unavailable())
		breaker := CircuitBreaker(BreakerOptions{Failures: 1, OpenFor: time.Minute})(s)
		fail(t, breaker, 1)
		fail(t, breaker, 1)
		if s.Calls() != 1 {
			t.Fatalf("the provider was asked %d times, want 1", s.Calls())
		}
		time.Sleep(time.Minute)
		s.failing.Store(false)
		for i := range 3 {
			if _, err := breaker.Complete(t.Context(), ask("q")); err != nil {
				t.Fatalf("request %d after recovery: %v", i+1, err)
			}
		}
		if s.Calls() != 4 {
			t.Errorf("the provider was asked %d times, want 4", s.Calls())
		}
	})

	bubble(t, "a probe that fails opens the circuit again for another OpenFor", func(t *testing.T) {
		s := newSwitched(unavailable())
		breaker := CircuitBreaker(BreakerOptions{Failures: 1, OpenFor: time.Minute})(s)
		fail(t, breaker, 1)
		time.Sleep(time.Minute)
		fail(t, breaker, 1)
		if s.Calls() != 2 {
			t.Fatalf("the provider was asked %d times, want 2: the probe", s.Calls())
		}
		fail(t, breaker, 1)
		if s.Calls() != 2 {
			t.Errorf("the provider was asked %d times, want 2: the circuit is open again", s.Calls())
		}
		time.Sleep(time.Minute)
		s.failing.Store(false)
		if _, err := breaker.Complete(t.Context(), ask("q")); err != nil {
			t.Errorf("the probe after another OpenFor: %v", err)
		}
	})

	bubble(t, "Successes probes in a row are needed to close it", func(t *testing.T) {
		s := newSwitched(unavailable())
		breaker := CircuitBreaker(BreakerOptions{Failures: 1, Successes: 2, OpenFor: time.Minute})(s)
		fail(t, breaker, 1)
		time.Sleep(time.Minute)
		s.failing.Store(false)
		if _, err := breaker.Complete(t.Context(), ask("q")); err != nil {
			t.Fatalf("the first probe: %v", err)
		}
		s.failing.Store(true)
		fail(t, breaker, 1)
		time.Sleep(time.Minute)
		s.failing.Store(false)
		for i := range 2 {
			if _, err := breaker.Complete(t.Context(), ask("q")); err != nil {
				t.Fatalf("probe %d of the second run: %v", i+1, err)
			}
		}
	})

	for _, tc := range []struct{ halfOpen, wantProbes int }{{0, 1}, {1, 1}, {2, 2}, {3, 3}} {
		bubble(t, "HalfOpenCalls of "+string(rune('0'+tc.halfOpen))+" lets that many probes be in flight while half open", func(t *testing.T) {
			var p schemafluxtest.Provider
			release := make(chan struct{})
			p.Respond(func(call int, _ schemaflux.Request) (schemaflux.Response, error) {
				if call == 0 {
					return schemaflux.Response{}, unavailable()
				}
				<-release
				return schemaflux.Response{Content: "answer"}, nil
			})
			breaker := CircuitBreaker(BreakerOptions{Failures: 1, OpenFor: time.Minute, HalfOpenCalls: tc.halfOpen})(&p)
			fail(t, breaker, 1)
			time.Sleep(time.Minute)
			var probes []<-chan error
			for range 5 {
				probes = append(probes, started(t.Context(), breaker))
				synctest.Wait()
			}
			if inProvider := p.Calls() - 1; inProvider != tc.wantProbes {
				t.Errorf("%d probes reached the provider, want %d", inProvider, tc.wantProbes)
			}
			close(release)
			refused := 0
			for _, probe := range probes {
				if err := <-probe; err != nil {
					wantKind(t, err, schemaflux.ErrUnavailable)
					refused++
				}
			}
			if refused != 5-tc.wantProbes {
				t.Errorf("%d probes were refused, want %d", refused, 5-tc.wantProbes)
			}
		})
	}

	bubble(t, "a failure that is the caller's does not count", func(t *testing.T) {
		for _, kind := range []schemaflux.Kind{
			schemaflux.ErrInvalidRequest, schemaflux.ErrUnauthorized, schemaflux.ErrBudgetExceeded,
			schemaflux.ErrPolicy, schemaflux.ErrConfiguration, schemaflux.ErrBusy,
		} {
			s := newSwitched(&schemaflux.Error{Kind: kind})
			breaker := CircuitBreaker(BreakerOptions{Failures: 2})(s)
			fail(t, breaker, 5)
			if s.Calls() != 5 {
				t.Errorf("%v: the provider was asked %d times, want 5: the circuit must stay closed", kind, s.Calls())
			}
		}
	})

	bubble(t, "a canceled context neither counts nor clears the failures", func(t *testing.T) {
		s := newSwitched(unavailable())
		// The request is canceled while the provider has it, and the provider says so.
		var cancelNext atomic.Bool
		ctx, cancel := context.WithCancel(t.Context())
		canceling := Wrap(s, func(ctx context.Context, req schemaflux.Request, next schemaflux.Provider) (schemaflux.Response, error) {
			if cancelNext.Load() {
				cancel()
				return schemaflux.Response{}, &schemaflux.Error{Kind: schemaflux.ErrCanceled}
			}
			return next.Complete(ctx, req)
		})
		breaker := CircuitBreaker(BreakerOptions{Failures: 2})(canceling)
		fail(t, breaker, 1)
		cancelNext.Store(true)
		if _, err := breaker.Complete(ctx, ask("q")); err == nil {
			t.Fatal("a request that was canceled succeeded")
		}
		cancelNext.Store(false)
		if s.Calls() != 1 {
			t.Fatalf("the provider was asked %d times, want 1", s.Calls())
		}
		fail(t, breaker, 1)
		if s.Calls() != 2 {
			t.Fatalf("the provider was asked %d times, want 2", s.Calls())
		}
		_, err := breaker.Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrUnavailable)
		if s.Calls() != 2 {
			t.Errorf("the provider was asked %d times, want 2: two counted failures open it, and the cancellation was neither one of them nor a reset", s.Calls())
		}
	})

	bubble(t, "failures that are the provider's count: a 429, an outage, a timeout and an unclassified error", func(t *testing.T) {
		for name, failure := range map[string]error{
			"a rate limit": rateLimited(0),
			"unavailable":  unavailable(),
			"a timeout":    &schemaflux.Error{Kind: schemaflux.ErrTimeout},
			"unclassified": errPlain,
		} {
			s := newSwitched(failure)
			breaker := CircuitBreaker(BreakerOptions{Failures: 2})(s)
			fail(t, breaker, 2)
			fail(t, breaker, 1)
			if s.Calls() != 2 {
				t.Errorf("%s: the provider was asked %d times, want 2: the circuit should be open", name, s.Calls())
			}
		}
	})

	bubble(t, "PerModel keeps a circuit for each model and the default one for the provider", func(t *testing.T) {
		sent := func(p schemaflux.Provider, model string) error {
			_, err := p.Complete(t.Context(), schemaflux.Request{Model: model, User: "q"})
			return err
		}
		perModel := newSwitched(unavailable())
		breaker := CircuitBreaker(BreakerOptions{Failures: 1, PerModel: true})(perModel)
		_ = sent(breaker, "model-a")
		_ = sent(breaker, "model-a")
		_ = sent(breaker, "model-b")
		if perModel.Calls() != 2 {
			t.Errorf("per model: %d requests reached the provider, want 2 (a failed, a refused, b failed)", perModel.Calls())
		}

		shared := newSwitched(unavailable())
		breaker = CircuitBreaker(BreakerOptions{Failures: 1})(shared)
		_ = sent(breaker, "model-a")
		_ = sent(breaker, "model-b")
		if shared.Calls() != 1 {
			t.Errorf("per provider: %d requests reached the provider, want 1", shared.Calls())
		}
	})

	bubble(t, "providers with different names have circuits of their own", func(t *testing.T) {
		a, b := newSwitched(unavailable()), newSwitched(unavailable())
		a.SetName("a")
		b.SetName("b")
		breaker := CircuitBreaker(BreakerOptions{Failures: 1})
		wa, wb := breaker(a), breaker(b)
		fail(t, wa, 2)
		fail(t, wb, 1)
		if a.Calls() != 1 || b.Calls() != 1 {
			t.Errorf("a was asked %d times and b %d, want one each", a.Calls(), b.Calls())
		}
	})

	bubble(t, "a negative setting is a configuration error and nothing is sent", func(t *testing.T) {
		for _, o := range []BreakerOptions{{Failures: -1}, {Successes: -1}, {HalfOpenCalls: -1}, {OpenFor: -time.Second}} {
			var p schemafluxtest.Provider
			_, err := CircuitBreaker(o)(&p).Complete(t.Context(), ask("q"))
			e := wantKind(t, err, schemaflux.ErrConfiguration)
			if e.Op != "mw.breaker" || p.Calls() != 0 {
				t.Errorf("%+v: Op %q, %d requests", o, e.Op, p.Calls())
			}
		}
	})

	bubble(t, "the refusal names the circuit and carries no text of the request", func(t *testing.T) {
		s := newSwitched(unavailable())
		breaker := CircuitBreaker(BreakerOptions{Failures: 1})(s)
		fail(t, breaker, 1)
		_, err := breaker.Complete(t.Context(), ask("the secret text "+testEmail))
		e := wantKind(t, err, schemaflux.ErrUnavailable)
		if e.Message == "" {
			t.Error("the refusal says nothing of why")
		}
		mustNotContain(t, "the error", err.Error(), "secret", testEmail)
	})

	bubble(t, "Retry repeats an outage and does not repeat a refusal of an open circuit", func(t *testing.T) {
		s := newSwitched(unavailable())
		chain := Chain(s, Retry(RetryOptions{Retries: 5, BaseDelay: time.Second}), CircuitBreaker(BreakerOptions{Failures: 1, OpenFor: time.Hour}))
		start := time.Now()
		_, err := chain.Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrUnavailable)
		// The first attempt is the outage that opens the circuit, the repeat after
		// one backoff finds it open, and that refusal is the result: not five more
		// waits for a circuit that is open for an hour.
		if s.Calls() != 1 {
			t.Errorf("the provider was asked %d times, want 1", s.Calls())
		}
		if waited := time.Since(start); waited < time.Second || waited > 4*time.Second {
			t.Errorf("the request took %v, want the one backoff before the repeat that found the circuit open", waited)
		}
	})
}

// The failures that open a circuit are the ones the doc lists: those that say
// something about the endpoint. A model's bad answer or an answer held for
// review does not.
func TestCircuitBreakerOpensOnTheKindsTheDocListsAndOnNoOther(t *testing.T) {
	t.Parallel()

	opens := []schemaflux.Kind{schemaflux.ErrRateLimited, schemaflux.ErrUnavailable, schemaflux.ErrTimeout, schemaflux.ErrInternal}
	for kind := schemaflux.ErrConfiguration; kind <= schemaflux.ErrInternal; kind++ {
		t.Run(kind.Error(), func(t *testing.T) {
			t.Parallel()
			s := newSwitched(&schemaflux.Error{Kind: kind})
			breaker := CircuitBreaker(BreakerOptions{Failures: 1, OpenFor: time.Hour})(s)
			fail(t, breaker, 2)
			wantCalls := 2
			if slices.Contains(opens, kind) {
				wantCalls = 1
			}
			if s.Calls() != wantCalls {
				t.Errorf("the provider was asked %d times after two failures of %v, want %d", s.Calls(), kind, wantCalls)
			}
		})
	}

	t.Run("a failure no layer classified opens it", func(t *testing.T) {
		t.Parallel()
		s := newSwitched(errPlain)
		fail(t, CircuitBreaker(BreakerOptions{Failures: 1, OpenFor: time.Hour})(s), 2)
		if s.Calls() != 1 {
			t.Errorf("the provider was asked %d times, want 1", s.Calls())
		}
	})
}
