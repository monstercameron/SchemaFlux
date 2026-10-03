package mw

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
)

// warmingProvider is a provider that can be warmed.
type warmingProvider struct {
	fakeProvider
	warmed atomic.Int32
	err    error
}

func (p *warmingProvider) Warm(context.Context) error { p.warmed.Add(1); return p.err }

// A chain is how the library is documented to be composed, and a call that
// needs the provider itself, llm.Warm opening its connection, has to reach it
// through every wrapper.
func TestWarmingReachesTheProviderThroughAChain(t *testing.T) {

	cases := []struct {
		name string
		wrap func(base Handler) Handler
	}{
		{"no middleware", func(base Handler) Handler { return Chain(base) }},
		{"Hedge", func(base Handler) Handler { return Chain(base, Hedge(time.Second)) }},
		{"Cache", func(base Handler) Handler { return Chain(base, Cache()) }},
		{"Retry", func(base Handler) Handler { return Chain(base, Retry()) }},
		{"RateLimit", func(base Handler) Handler { return Chain(base, RateLimit(10, time.Second)) }},
		{"Budget", func(base Handler) Handler { return Chain(base, Budget(5)) }},
		{"Bulkhead", func(base Handler) Handler { return Chain(base, Bulkhead(2)) }},
		{"CircuitBreaker", func(base Handler) Handler {
			return Chain(base, CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 3}))
		}},
		{"RedactEgress", func(base Handler) Handler { return Chain(base, RedactEgress()) }},
		{"Fallback: the primary is warmed", func(base Handler) Handler {
			return Chain(base, Fallback([]FallbackRoute{{Handler: &fakeProvider{name: "other"}}}))
		}},
		{"all of them", func(base Handler) Handler {
			return Chain(base, Cache(), Retry(), RateLimit(10, time.Second), Budget(5), Bulkhead(2), RedactEgress(), Hedge(time.Second))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := &warmingProvider{fakeProvider: fakeProvider{name: "fake"}}
			warmed, err := llm.Warm(context.Background(), tc.wrap(base))
			if err != nil || !warmed {
				t.Fatalf("Warm = %v, %v", warmed, err)
			}
			if base.warmed.Load() != 1 {
				t.Errorf("the provider at the bottom was warmed %d times", base.warmed.Load())
			}
		})
	}

	t.Run("a chain over a provider that cannot warm", func(t *testing.T) {
		warmed, err := llm.Warm(context.Background(), Chain(&fakeProvider{name: "fake"}, Cache(), Hedge(time.Second)))
		if err != nil || warmed {
			t.Errorf("Warm = %v, %v", warmed, err)
		}
	})
}
