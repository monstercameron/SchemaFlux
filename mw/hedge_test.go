package mw

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/provider"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// copyStep is what one request to a scripted provider does: it waits for
// delay and then answers with body or fails with err, or hangs until its
// context ends.
type copyStep struct {
	delay time.Duration
	body  string
	err   error
	hang  bool
	panic any
}

// copies is a provider whose nth request does steps[n], and that records when
// each request arrived and which were canceled.
type copies struct {
	steps    []copyStep
	n        atomic.Int32
	canceled atomic.Int32

	mu       sync.Mutex
	arrived  []time.Time
	requests []schemaflux.Request
}

func (c *copies) Name() string { return "copies" }

func (c *copies) Complete(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	i := int(c.n.Add(1)) - 1
	c.mu.Lock()
	c.arrived = append(c.arrived, time.Now())
	c.requests = append(c.requests, req)
	c.mu.Unlock()
	step := c.steps[min(i, len(c.steps)-1)]
	if step.panic != nil {
		panic(step.panic)
	}
	var timer <-chan time.Time
	if !step.hang {
		timer = time.After(step.delay)
	}
	select {
	case <-timer:
		if step.err != nil {
			return schemaflux.Response{}, step.err
		}
		return schemaflux.Response{Content: step.body}, nil
	case <-ctx.Done():
		c.canceled.Add(1)
		return schemaflux.Response{}, ctx.Err()
	}
}

func TestHedge(t *testing.T) {
	t.Parallel()

	bubble(t, "a request that answers within the delay is sent once and is not hedged", func(t *testing.T) {
		c := &copies{steps: []copyStep{{delay: 10 * time.Millisecond, body: "first"}}}
		resp, err := Hedge(time.Second)(c).Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "first" || resp.Hedged {
			t.Errorf("%q, hedged %v, %v", resp.Content, resp.Hedged, err)
		}
		if c.n.Load() != 1 {
			t.Errorf("%d requests were sent, want 1", c.n.Load())
		}
	})

	bubble(t, "a second copy is sent after the delay, the quick one answers, and the answer says it was hedged", func(t *testing.T) {
		c := &copies{steps: []copyStep{{hang: true}, {delay: 20 * time.Millisecond, body: "second"}}}
		start := time.Now()
		resp, err := Hedge(100*time.Millisecond)(c).Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "second" || !resp.Hedged {
			t.Fatalf("%q, hedged %v, %v", resp.Content, resp.Hedged, err)
		}
		if got := c.arrived[1].Sub(start); got != 100*time.Millisecond {
			t.Errorf("the second copy was sent after %v, want 100ms", got)
		}
		if took := time.Since(start); took != 120*time.Millisecond {
			t.Errorf("the request took %v, want the delay plus the quick answer, 120ms", took)
		}
	})

	bubble(t, "the first answer is used even when it is the first copy's, and the other is canceled", func(t *testing.T) {
		c := &copies{steps: []copyStep{{delay: 150 * time.Millisecond, body: "first"}, {delay: 100 * time.Millisecond, body: "second"}}}
		resp, err := Hedge(100*time.Millisecond)(c).Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "first" || !resp.Hedged {
			t.Fatalf("%q, hedged %v, %v: the first copy answered at 150ms, the second would at 200ms", resp.Content, resp.Hedged, err)
		}
		time.Sleep(time.Second)
		if c.canceled.Load() != 1 {
			t.Errorf("%d copies were canceled, want 1", c.canceled.Load())
		}
	})

	bubble(t, "a copy that loses is canceled and no more than two are ever sent", func(t *testing.T) {
		c := &copies{steps: []copyStep{{hang: true}, {delay: time.Millisecond, body: "second"}}}
		if _, err := Hedge(10*time.Millisecond)(c).Complete(t.Context(), ask("q")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		if c.canceled.Load() != 1 || c.n.Load() != 2 {
			t.Errorf("%d canceled, %d sent, want 1 and 2", c.canceled.Load(), c.n.Load())
		}
	})

	bubble(t, "when both copies fail the first copy's error is returned", func(t *testing.T) {
		c := &copies{steps: []copyStep{
			{delay: 200 * time.Millisecond, err: &schemaflux.Error{Kind: schemaflux.ErrTimeout}},
			{delay: 10 * time.Millisecond, err: unavailable()},
		}}
		_, err := Hedge(100*time.Millisecond)(c).Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrTimeout)
	})

	bubble(t, "a failure before the delay is returned as it is and no second copy is sent", func(t *testing.T) {
		c := &copies{steps: []copyStep{{delay: 10 * time.Millisecond, err: unavailable()}, {body: "never"}}}
		_, err := Hedge(time.Second)(c).Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrUnavailable)
		if c.n.Load() != 1 {
			t.Errorf("%d requests were sent, want 1: trying a failure again is Retry's", c.n.Load())
		}
	})

	bubble(t, "a copy that fails does not end the request while the other may still answer", func(t *testing.T) {
		c := &copies{steps: []copyStep{{delay: 300 * time.Millisecond, body: "first"}, {delay: 10 * time.Millisecond, err: unavailable()}}}
		resp, err := Hedge(100*time.Millisecond)(c).Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "first" || !resp.Hedged {
			t.Errorf("%q, hedged %v, %v", resp.Content, resp.Hedged, err)
		}
	})

	bubble(t, "a delay of zero sends every request once", func(t *testing.T) {
		c := &copies{steps: []copyStep{{delay: time.Hour, body: "only"}}}
		resp, err := Hedge(0)(c).Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "only" || resp.Hedged || c.n.Load() != 1 {
			t.Errorf("%q, hedged %v, %v, %d requests", resp.Content, resp.Hedged, err, c.n.Load())
		}
	})

	t.Run("a delay below zero fails every request as a configuration error", func(t *testing.T) {
		t.Parallel()
		for _, after := range []time.Duration{-1, -time.Second, time.Duration(-1 << 63)} {
			c := &copies{steps: []copyStep{{body: "only"}}}
			_, err := Hedge(after)(c).Complete(t.Context(), ask("q"))
			e := wantKind(t, err, schemaflux.ErrConfiguration)
			if e.Op != "mw.hedge" || c.n.Load() != 0 {
				t.Errorf("after %v: Op %q, %d requests", after, e.Op, c.n.Load())
			}
		}
	})

	bubble(t, "both copies are the same request", func(t *testing.T) {
		c := &copies{steps: []copyStep{{hang: true}, {delay: time.Millisecond, body: "second"}}}
		want := schemaflux.Request{Model: "m", System: "s", User: "u", MaxTokens: 5}
		if _, err := Hedge(10*time.Millisecond)(c).Complete(t.Context(), want); err != nil {
			t.Fatal(err)
		}
		if c.requests[0].User != want.User || c.requests[1].User != want.User || c.requests[0].Model != c.requests[1].Model || c.requests[1].MaxTokens != 5 {
			t.Errorf("the copies were %+v and %+v", c.requests[0], c.requests[1])
		}
	})

	bubble(t, "a copy that panics is an internal error with nothing of the panic in it", func(t *testing.T) {
		c := &copies{steps: []copyStep{{panic: "secret value of the panic " + testEmail}}}
		_, err := Hedge(time.Second)(c).Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrInternal)
		mustNotContain(t, "the error", err.Error(), "secret value", testEmail)
	})

	bubble(t, "a second copy that panics does not take down a first copy that answers", func(t *testing.T) {
		c := &copies{steps: []copyStep{{delay: 50 * time.Millisecond, body: "first"}, {panic: "bug"}}}
		resp, err := Hedge(10*time.Millisecond)(c).Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "first" || !resp.Hedged {
			t.Errorf("%q, hedged %v, %v", resp.Content, resp.Hedged, err)
		}
	})

	bubble(t, "when the copy that panics is the only one that failed and the other fails too, the first's error is returned", func(t *testing.T) {
		c := &copies{steps: []copyStep{{delay: 50 * time.Millisecond, err: unavailable()}, {panic: "bug"}}}
		_, err := Hedge(10*time.Millisecond)(c).Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrUnavailable)
	})

	bubble(t, "a context that ends before the delay ends the request with its error", func(t *testing.T) {
		c := &copies{steps: []copyStep{{hang: true}}}
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		_, err := Hedge(time.Second)(c).Complete(ctx, ask("q"))
		wantKind(t, err, schemaflux.ErrTimeout)
		if c.n.Load() != 1 {
			t.Errorf("%d requests were sent, want 1: no second copy goes out on a dead context", c.n.Load())
		}
	})

	bubble(t, "a context that ends after the second copy was sent returns the context's error", func(t *testing.T) {
		c := &copies{steps: []copyStep{{hang: true}, {hang: true}}}
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		_, err := Hedge(100*time.Millisecond)(c).Complete(ctx, ask("q"))
		wantKind(t, err, schemaflux.ErrTimeout)
		if c.n.Load() != 2 {
			t.Errorf("%d requests were sent, want 2", c.n.Load())
		}
	})

	bubble(t, "Response.Hedged says it through a Retry outside the hedge, and only for the call that hedged", func(t *testing.T) {
		c := &copies{steps: []copyStep{{delay: time.Millisecond, body: "quick"}}}
		chain := Chain(c, Retry(), Hedge(time.Second))
		if resp, err := chain.Complete(t.Context(), ask("q")); err != nil || resp.Hedged {
			t.Fatalf("a quick call: hedged %v, %v", resp.Hedged, err)
		}
		slow := &copies{steps: []copyStep{{hang: true}, {delay: time.Millisecond, body: "second"}}}
		chain = Chain(slow, Retry(), Hedge(10*time.Millisecond))
		if resp, err := chain.Complete(t.Context(), ask("q")); err != nil || !resp.Hedged {
			t.Errorf("a slow call: hedged %v, %v", resp.Hedged, err)
		}
	})

	bubble(t, "a cache hit of a hedged answer says it was cached and not that it was hedged", func(t *testing.T) {
		c := &copies{steps: []copyStep{{hang: true}, {delay: time.Millisecond, body: "second"}}}
		chain := Chain(c, Cache(), Hedge(10*time.Millisecond))
		miss, err := chain.Complete(t.Context(), ask("q"))
		if err != nil || !miss.Hedged {
			t.Fatalf("the miss: hedged %v, %v", miss.Hedged, err)
		}
		hit, err := chain.Complete(t.Context(), ask("q"))
		if err != nil || !hit.Cached || hit.Hedged {
			t.Errorf("the hit: cached %v, hedged %v, %v: no copy was sent for it", hit.Cached, hit.Hedged, err)
		}
	})

	bubble(t, "the model, name and retry settings are the wrapped provider's", func(t *testing.T) {
		c := &copies{steps: []copyStep{{body: "x"}}}
		hedged := Hedge(time.Second)(c)
		if hedged.Name() != "copies" {
			t.Errorf("Name = %q", hedged.Name())
		}

		wrapped := Hedge(time.Second)(retrier{Provider: new(schemafluxtest.Provider), retries: 6, backoff: 3 * time.Second})
		if retries, backoff := wrapped.(provider.Retrier).RetryPolicy(); retries != 6 || backoff != 3*time.Second {
			t.Errorf("RetryPolicy = %d, %v, want 6, 3s", retries, backoff)
		}
		tiered, ok := bind.Optional[interface{ ModelFor(schemaflux.Tier) string }](wrapped)
		if !ok || tiered.ModelFor(schemaflux.TierFast) != "test-model" {
			t.Errorf("the model of a tier: found %v, ok %v, want the wrapped provider's", tiered, ok)
		}
	})
}
