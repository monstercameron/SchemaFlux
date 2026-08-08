package ops

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// RC-005: load and chaos coverage for CircuitBreaker and Bulkhead
// (resilience.go) -- "the bulkheads" the task names alongside the scheduler,
// the retry classifier, and the partial-result machinery. resilience_test.go
// already pins each mechanism's state machine one transition at a time,
// keyed by a single breaker or bulkhead key under a controlled interleaving;
// these tests run many keys and many goroutines at once, which is the
// setting a shared mutex guarding a map of per-key state (breaker map,
// Bulkhead's cap/used/wake maps) actually has to hold up under.

// TestChaosCircuitBreakerManyKeysConcurrentStormAccountsForEveryCall storms
// 40 independently-keyed breakers with 20,000 concurrent Allow/report calls,
// a mix of simulated successes and failures, and proves every call is
// accounted for as either allowed or refused -- never both, never neither --
// and that every key's breaker settles into a state State() can report
// without panicking, which a data race on breakerState's unguarded fields
// would risk under this much concurrent map access even though the map
// itself is protected by one mutex.
func TestChaosCircuitBreakerManyKeysConcurrentStormAccountsForEveryCall(t *testing.T) {
	clk := newBreakerClock() // frozen: no Open->HalfOpen transition mid-storm to race against
	b := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 5, SuccessThreshold: 2, HalfOpenMaxCalls: 2,
		OpenDuration: time.Hour, Now: clk.Now,
	})

	const keys = 40
	const total = 20000

	var allowed, refused int64
	rng := rand.New(rand.NewSource(3))
	var rngMu sync.Mutex

	runWithDeadline(t, 30*time.Second, func() {
		var wg sync.WaitGroup
		for i := 0; i < total; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				key := fmt.Sprintf("key-%d", i%keys)

				rngMu.Lock()
				succeed := rng.Float64() > 0.3
				rngMu.Unlock()

				report, err := b.Allow(key)
				if err != nil {
					if errKind(t, err) != types.KindCircuitOpen {
						t.Errorf("call %d: unexpected error kind: %v", i, err)
					}
					atomic.AddInt64(&refused, 1)
					return
				}
				atomic.AddInt64(&allowed, 1)
				report(succeed)
			}(i)
		}
		wg.Wait()
	})

	if sum := allowed + refused; sum != total {
		t.Fatalf("allowed(%d)+refused(%d) = %d, want %d -- some call vanished", allowed, refused, sum, total)
	}
	if allowed == 0 {
		t.Fatal("nothing was allowed -- the storm never exercised the accept path")
	}
	if refused == 0 {
		t.Fatal("nothing was refused -- the storm never exercised a tripped breaker")
	}

	for i := 0; i < keys; i++ {
		key := fmt.Sprintf("key-%d", i)
		switch st := b.State(key); st {
		case CircuitClosed, CircuitOpen, CircuitHalfOpen:
			// Any of the three is a legitimate resting state; the check is
			// that reading it does not panic or hang.
		default:
			t.Errorf("key %s: State() = %v, not one of the three recognised states", key, st)
		}
	}
}

// TestChaosCircuitBreakerHalfOpenProbeBoundHoldsUnderConcurrentStorm scales
// resilience_test.go's TestCircuitBreakerHalfOpenBoundsConcurrentProbes: many
// more goroutines race to Allow the instant the breaker's fake clock crosses
// into HalfOpen, and the observed PEAK concurrently-admitted probe count
// (tracked the same "raise a shared atomic, only lower it once the
// simulated call finishes" way scheduler_test.go's gatedWork proves a
// concurrency bound, not merely believes it) must never exceed
// HalfOpenMaxCalls, however many goroutines lost the race to get in.
func TestChaosCircuitBreakerHalfOpenProbeBoundHoldsUnderConcurrentStorm(t *testing.T) {
	clk := newBreakerClock()
	const halfOpenMax = 3
	b := NewCircuitBreaker(CircuitBreakerConfig{
		// SuccessThreshold must exceed the racer count below, or the breaker
		// closes partway through and the measurement stops meaning anything:
		// once it is Closed, Allow admits everything, those calls are not
		// probes, and counting them reports a "probe concurrency" of whatever
		// the goroutine scheduler happened to do. At 100 against 200 racers it
		// did exactly that and reported 4 against a bound of 3 -- a real
		// overshoot of the wrong quantity.
		FailureThreshold: 1, SuccessThreshold: 100000, // never closes on its own mid-storm
		HalfOpenMaxCalls: halfOpenMax, OpenDuration: time.Minute, Now: clk.Now,
	})

	const key = "flaky"
	// Trip the breaker, then cross into HalfOpen.
	report, err := b.Allow(key)
	if err != nil {
		t.Fatalf("Allow (priming failure): %v", err)
	}
	report(false)
	if b.State(key) != CircuitOpen {
		t.Fatalf("State = %v, want Open after the priming failure", b.State(key))
	}
	clk.advance(time.Minute)
	if b.State(key) != CircuitHalfOpen {
		t.Fatalf("State = %v, want HalfOpen after OpenDuration elapsed", b.State(key))
	}

	var current, peak int64
	release := make(chan struct{})

	runWithDeadline(t, 15*time.Second, func() {
		var wg sync.WaitGroup
		const racers = 200
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				report, err := b.Allow(key)
				if err != nil {
					return // refused: not a probe, nothing to hold open
				}
				n := atomic.AddInt64(&current, 1)
				for {
					p := atomic.LoadInt64(&peak)
					if n <= p || atomic.CompareAndSwapInt64(&peak, p, n) {
						break
					}
				}
				<-release
				atomic.AddInt64(&current, -1)
				report(true)
			}()
		}

		// Give every goroutine a chance to reach Allow before releasing.
		deadline := time.Now().Add(5 * time.Second)
		for atomic.LoadInt64(&current) == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		close(release)
		wg.Wait()
	})

	if got := atomic.LoadInt64(&peak); got > halfOpenMax {
		t.Fatalf("peak concurrent HalfOpen probes = %d, want at most %d", got, halfOpenMax)
	}
	if atomic.LoadInt64(&peak) == 0 {
		t.Fatal("no probe was ever admitted -- this run did not exercise HalfOpen admission at all")
	}
}

// TestChaosBulkheadCancellationStormAccountsForEverySubmission storms a
// single bulkhead key with a mix of non-blocking TryAcquire calls and
// context-bounded Acquire calls, many of which time out before a slot frees,
// and proves every submission is accounted for as acquired, refused
// (TryAcquire on a full bulkhead), or cancelled (Acquire's context ending
// first) -- and that the bulkhead's own usage counter returns to zero
// afterward, so a cancelled Acquire never leaks the reservation it never
// actually held.
func TestChaosBulkheadCancellationStormAccountsForEverySubmission(t *testing.T) {
	bh := NewBulkhead()
	const key = "k"
	const limit = 6
	const total = 3000

	var acquired, refused, canceled, other int64

	runWithDeadline(t, 30*time.Second, func() {
		var wg sync.WaitGroup
		for i := 0; i < total; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()

				hold := time.Duration(rand.Intn(2)) * time.Millisecond
				if i%2 == 0 {
					release, err := bh.TryAcquire(key, limit)
					if err != nil {
						if errKind(t, err) != types.KindAdmissionRejected {
							t.Errorf("submission %d: unexpected TryAcquire error: %v", i, err)
						}
						atomic.AddInt64(&refused, 1)
						return
					}
					time.Sleep(hold)
					release()
					atomic.AddInt64(&acquired, 1)
					return
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(rand.Intn(3))*time.Millisecond)
				defer cancel()
				release, err := bh.Acquire(ctx, key, limit)
				if err != nil {
					var opErr *types.OperationError
					if e, ok := err.(*types.OperationError); ok {
						opErr = e
					}
					if opErr == nil {
						atomic.AddInt64(&other, 1)
						t.Errorf("submission %d: unclassified Acquire error: %v", i, err)
						return
					}
					atomic.AddInt64(&canceled, 1)
					return
				}
				time.Sleep(hold)
				release()
				atomic.AddInt64(&acquired, 1)
			}(i)
		}
		wg.Wait()
	})

	sum := acquired + refused + canceled + other
	if sum != total {
		t.Fatalf("acquired(%d)+refused(%d)+canceled(%d)+other(%d) = %d, want %d -- some submission vanished",
			acquired, refused, canceled, other, sum, total)
	}
	if other != 0 {
		t.Fatalf("%d submissions returned an unclassified error", other)
	}
	if acquired == 0 {
		t.Fatal("nothing was ever acquired -- the storm did not exercise the accept path")
	}

	bh.mu.Lock()
	used := bh.used[key]
	bh.mu.Unlock()
	if used != 0 {
		t.Fatalf("bulkhead usage for %q settled at %d, want 0 -- a reservation leaked", key, used)
	}
}
