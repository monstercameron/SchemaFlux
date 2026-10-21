package mw

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// held is a provider whose requests wait until the test lets them go, and that
// counts how many are in the provider at once.
type held struct {
	*schemafluxtest.Provider
	release  chan struct{}
	inFlight atomic.Int32
	peak     atomic.Int32
}

func newHeld() *held {
	h := &held{Provider: new(schemafluxtest.Provider), release: make(chan struct{})}
	h.Provider.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
		n := h.inFlight.Add(1)
		defer h.inFlight.Add(-1)
		for {
			peak := h.peak.Load()
			if n <= peak || h.peak.CompareAndSwap(peak, n) {
				break
			}
		}
		<-h.release
		return schemaflux.Response{Content: "answer"}, nil
	})
	return h
}

// let releases one request that is waiting in the provider.
func (h *held) let() { h.release <- struct{}{} }

// started runs a request on a goroutine and returns the channel its error comes
// on.
func started(ctx context.Context, p schemaflux.Provider) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := p.Complete(ctx, ask("q"))
		done <- err
	}()
	return done
}

func TestBulkhead(t *testing.T) {
	t.Parallel()

	bubble(t, "no more than the limit are in the provider at once", func(t *testing.T) {
		h := newHeld()
		wall := Bulkhead(2)(h)
		var done []<-chan error
		for range 5 {
			done = append(done, started(t.Context(), wall))
		}
		synctest.Wait()
		if got := h.inFlight.Load(); got != 2 {
			t.Fatalf("%d requests are in the provider, want 2", got)
		}
		for range 5 {
			h.let()
			synctest.Wait()
		}
		for i, d := range done {
			if err := <-d; err != nil {
				t.Errorf("request %d: %v", i+1, err)
			}
		}
		if h.peak.Load() != 2 {
			t.Errorf("the peak was %d, want 2", h.peak.Load())
		}
		if h.Calls() != 5 {
			t.Errorf("%d requests were served, want 5", h.Calls())
		}
	})

	bubble(t, "a request that finds the wall full waits and goes when a slot is given back", func(t *testing.T) {
		h := newHeld()
		wall := Bulkhead(1)(h)
		first := started(t.Context(), wall)
		synctest.Wait()
		second := started(t.Context(), wall)
		synctest.Wait()
		if h.Calls() != 1 {
			t.Fatalf("the provider was asked %d times while the wall was full, want 1", h.Calls())
		}
		h.let()
		synctest.Wait()
		if h.Calls() != 2 {
			t.Fatalf("the provider was asked %d times after a slot was given back, want 2", h.Calls())
		}
		h.let()
		if err := <-first; err != nil {
			t.Error(err)
		}
		if err := <-second; err != nil {
			t.Error(err)
		}
	})

	bubble(t, "Reject fails a request that finds the wall full at once with ErrBusy", func(t *testing.T) {
		h := newHeld()
		wall := Bulkhead(1, BulkheadOptions{Reject: true})(h)
		first := started(t.Context(), wall)
		synctest.Wait()
		_, err := wall.Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrBusy)
		if h.Calls() != 1 {
			t.Errorf("the provider was asked %d times, want 1", h.Calls())
		}
		h.let()
		<-first
	})

	bubble(t, "a slot is given back when the request fails", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.FailThen(1, unavailable(), "answer")
		wall := Bulkhead(1, BulkheadOptions{Reject: true})(&p)
		if _, err := wall.Complete(t.Context(), ask("q")); err == nil {
			t.Fatal("the first request should have failed")
		}
		if _, err := wall.Complete(t.Context(), ask("q")); err != nil {
			t.Errorf("the second request found the wall still full: %v", err)
		}
	})

	bubble(t, "a slot is given back when the provider panics", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Respond(func(call int, _ schemaflux.Request) (schemaflux.Response, error) {
			if call == 0 {
				panic("provider bug")
			}
			return schemaflux.Response{Content: "answer"}, nil
		})
		wall := Bulkhead(1, BulkheadOptions{Reject: true})(&p)
		func() {
			defer func() { _ = recover() }()
			_, _ = wall.Complete(t.Context(), ask("q"))
		}()
		if _, err := wall.Complete(t.Context(), ask("q")); err != nil {
			t.Errorf("the request after the panic found the wall full: %v", err)
		}
	})

	bubble(t, "a waiter whose context ends leaves no slot taken", func(t *testing.T) {
		h := newHeld()
		wall := Bulkhead(1)(h)
		first := started(t.Context(), wall)
		synctest.Wait()
		ctx, cancel := context.WithCancel(t.Context())
		waiter := started(ctx, wall)
		synctest.Wait()
		cancel()
		err := <-waiter
		wantKind(t, err, schemaflux.ErrCanceled)
		h.let()
		<-first
		third := started(t.Context(), wall)
		synctest.Wait()
		h.let()
		if err := <-third; err != nil {
			t.Errorf("the request after the waiter left failed: %v", err)
		}
	})

	bubble(t, "a deadline that passes while waiting for a slot is a timeout", func(t *testing.T) {
		h := newHeld()
		wall := Bulkhead(1)(h)
		first := started(t.Context(), wall)
		synctest.Wait()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		_, err := wall.Complete(ctx, ask("q"))
		wantKind(t, err, schemaflux.ErrTimeout)
		h.let()
		<-first
	})

	t.Run("a limit below one fails every request as a configuration error", func(t *testing.T) {
		t.Parallel()
		for _, limit := range []int{0, -3, -1 << 63} {
			var p schemafluxtest.Provider
			_, err := Bulkhead(limit)(&p).Complete(t.Context(), ask("q"))
			e := wantKind(t, err, schemaflux.ErrConfiguration)
			if e.Op != "mw.bulkhead" || p.Calls() != 0 {
				t.Errorf("limit %d: Op %q, %d requests", limit, e.Op, p.Calls())
			}
		}
	})

	bubble(t, "providers with one name share a limit and providers with two names have one each", func(t *testing.T) {
		wall := Bulkhead(1, BulkheadOptions{Reject: true})

		a, b := newHeld(), newHeld()
		first := started(t.Context(), wall(a))
		synctest.Wait()
		_, err := wall(b).Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrBusy)
		a.let()
		<-first

		c, d := newHeld(), newHeld()
		c.SetName("c")
		d.SetName("d")
		wall = Bulkhead(1, BulkheadOptions{Reject: true})
		firstC := started(t.Context(), wall(c))
		synctest.Wait()
		firstD := started(t.Context(), wall(d))
		synctest.Wait()
		if c.Calls() != 1 || d.Calls() != 1 {
			t.Errorf("c was asked %d times and d %d, want one each", c.Calls(), d.Calls())
		}
		c.let()
		d.let()
		<-firstC
		<-firstD
	})

	bubble(t, "the refusal says the bulkhead is full and carries no text of the request", func(t *testing.T) {
		h := newHeld()
		wall := Bulkhead(1, BulkheadOptions{Reject: true})(h)
		first := started(t.Context(), wall)
		synctest.Wait()
		_, err := wall.Complete(t.Context(), ask("the secret text "+testEmail))
		e := wantKind(t, err, schemaflux.ErrBusy)
		if e.Message == "" {
			t.Error("the refusal says nothing of why")
		}
		mustNotContain(t, "the error", err.Error(), "secret", testEmail)
		h.let()
		<-first
	})

	bubble(t, "Retry does not repeat a refusal of the program's own limit", func(t *testing.T) {
		h := newHeld()
		chain := Chain(h, Retry(), Bulkhead(1, BulkheadOptions{Reject: true}))
		first := started(t.Context(), chain)
		synctest.Wait()
		_, err := chain.Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrBusy)
		h.let()
		<-first
	})

	bubble(t, "many requests at once never pass the limit", func(t *testing.T) {
		h := newHeld()
		wall := Bulkhead(3)(h)
		var wg sync.WaitGroup
		for range 20 {
			wg.Go(func() { _, _ = wall.Complete(t.Context(), ask("q")) })
		}
		for range 20 {
			synctest.Wait()
			h.let()
		}
		wg.Wait()
		if h.peak.Load() > 3 || h.Calls() != 20 {
			t.Errorf("the peak was %d (limit 3) and %d requests were served (want 20)", h.peak.Load(), h.Calls())
		}
	})

	bubble(t, "option values merge left to right with a later non-zero field winning", func(t *testing.T) {
		h := newHeld()
		wall := Bulkhead(1, BulkheadOptions{Reject: true}, BulkheadOptions{})(h)
		first := started(t.Context(), wall)
		synctest.Wait()
		_, err := wall.Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrBusy)
		h.let()
		<-first
	})
}
