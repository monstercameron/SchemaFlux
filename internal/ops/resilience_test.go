package ops

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// breakerClock is a fake clock for CircuitBreaker, so "opens and closes on
// conditions asserted, not elapsed time" holds: every test below advances
// this explicitly rather than sleeping.
type breakerClock struct {
	mu  sync.Mutex
	now time.Time
}

func newBreakerClock() *breakerClock { return &breakerClock{now: time.Unix(0, 0)} }
func (c *breakerClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *breakerClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func errKind(t *testing.T, err error) types.ErrorKind {
	t.Helper()
	var opErr *types.OperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("expected *types.OperationError, got %T: %v", err, err)
	}
	return opErr.Kind
}

// TestCircuitBreakerOpensAfterConsecutiveFailures proves the breaker trips
// exactly at the configured threshold, on the failure count, not on time.
func TestCircuitBreakerOpensAfterConsecutiveFailures(t *testing.T) {
	clk := newBreakerClock()
	b := NewCircuitBreaker(CircuitBreakerConfig{FailureThreshold: 3, Now: clk.Now})

	for i := 0; i < 2; i++ {
		report, err := b.Allow("ep")
		if err != nil {
			t.Fatalf("attempt %d: unexpected refusal: %v", i, err)
		}
		report(false)
	}
	if got := b.State("ep"); got != CircuitClosed {
		t.Fatalf("state after 2 failures = %v, want Closed (threshold is 3)", got)
	}

	report, err := b.Allow("ep")
	if err != nil {
		t.Fatalf("3rd attempt: unexpected refusal: %v", err)
	}
	report(false)

	if got := b.State("ep"); got != CircuitOpen {
		t.Fatalf("state after 3 failures = %v, want Open", got)
	}

	if _, err := b.Allow("ep"); err == nil {
		t.Fatal("expected the open breaker to refuse the next call")
	} else if errKind(t, err) != types.KindCircuitOpen {
		t.Fatalf("kind = %v, want KindCircuitOpen", errKind(t, err))
	}
}

// TestCircuitBreakerSuccessResetsFailureCount proves failures must be
// consecutive: an interleaved success clears the count rather than
// contributing to a running total.
func TestCircuitBreakerSuccessResetsFailureCount(t *testing.T) {
	clk := newBreakerClock()
	b := NewCircuitBreaker(CircuitBreakerConfig{FailureThreshold: 2, Now: clk.Now})

	for i := 0; i < 5; i++ {
		report, err := b.Allow("ep")
		if err != nil {
			t.Fatalf("iteration %d: unexpected refusal: %v", i, err)
		}
		report(false)
		report2, err2 := b.Allow("ep")
		if err2 != nil {
			t.Fatalf("iteration %d: unexpected refusal: %v", i, err2)
		}
		report2(true) // resets the streak before it reaches 2
	}
	if got := b.State("ep"); got != CircuitClosed {
		t.Fatalf("state = %v, want Closed -- every failure streak was interrupted by a success", got)
	}
}

// TestCircuitBreakerHalfOpensOnlyAfterOpenDuration proves the Open ->
// HalfOpen transition is driven by the injected clock, asserted at both
// sides of the boundary.
func TestCircuitBreakerHalfOpensOnlyAfterOpenDuration(t *testing.T) {
	clk := newBreakerClock()
	b := NewCircuitBreaker(CircuitBreakerConfig{FailureThreshold: 1, OpenDuration: 10 * time.Second, Now: clk.Now})

	report, _ := b.Allow("ep")
	report(false)
	if got := b.State("ep"); got != CircuitOpen {
		t.Fatalf("state = %v, want Open", got)
	}

	clk.advance(9999 * time.Millisecond)
	if got := b.State("ep"); got != CircuitOpen {
		t.Fatalf("state at 9.999s = %v, want still Open", got)
	}

	clk.advance(2 * time.Millisecond) // crosses the 10s boundary
	if got := b.State("ep"); got != CircuitHalfOpen {
		t.Fatalf("state past OpenDuration = %v, want HalfOpen", got)
	}
}

// TestCircuitBreakerHalfOpenSuccessCloses proves a clean probe closes the
// breaker.
func TestCircuitBreakerHalfOpenSuccessCloses(t *testing.T) {
	clk := newBreakerClock()
	b := NewCircuitBreaker(CircuitBreakerConfig{FailureThreshold: 1, OpenDuration: time.Second, Now: clk.Now})
	report, _ := b.Allow("ep")
	report(false)
	clk.advance(2 * time.Second)

	probe, err := b.Allow("ep")
	if err != nil {
		t.Fatalf("expected a probe to be allowed once half-open: %v", err)
	}
	probe(true)

	if got := b.State("ep"); got != CircuitClosed {
		t.Fatalf("state after a successful probe = %v, want Closed", got)
	}
}

// TestCircuitBreakerHalfOpenFailureReopens proves a failed probe re-opens
// immediately rather than counting toward a fresh failure streak.
func TestCircuitBreakerHalfOpenFailureReopens(t *testing.T) {
	clk := newBreakerClock()
	b := NewCircuitBreaker(CircuitBreakerConfig{FailureThreshold: 3, OpenDuration: time.Second, Now: clk.Now})
	for i := 0; i < 3; i++ {
		r, err := b.Allow("ep")
		if err != nil {
			break
		}
		r(false)
	}
	if got := b.State("ep"); got != CircuitOpen {
		t.Fatalf("state = %v, want Open before the half-open probe", got)
	}

	clk.advance(2 * time.Second)
	probe, err := b.Allow("ep")
	if err != nil {
		t.Fatalf("expected the probe to be allowed: %v", err)
	}
	probe(false)

	if got := b.State("ep"); got != CircuitOpen {
		t.Fatalf("state after a failed probe = %v, want Open again immediately", got)
	}
}

// TestCircuitBreakerHalfOpenBoundsConcurrentProbes proves only
// HalfOpenMaxCalls probes are allowed through at once, and everything past
// that is refused exactly like Open.
func TestCircuitBreakerHalfOpenBoundsConcurrentProbes(t *testing.T) {
	clk := newBreakerClock()
	b := NewCircuitBreaker(CircuitBreakerConfig{FailureThreshold: 1, OpenDuration: time.Second, HalfOpenMaxCalls: 1, Now: clk.Now})
	report, _ := b.Allow("ep")
	report(false)
	clk.advance(2 * time.Second)

	_, err1 := b.Allow("ep") // takes the one probe slot, left unreported deliberately
	if err1 != nil {
		t.Fatalf("first probe: unexpected refusal: %v", err1)
	}
	_, err2 := b.Allow("ep")
	if err2 == nil {
		t.Fatal("expected a second concurrent probe to be refused")
	}
	if errKind(t, err2) != types.KindCircuitOpen {
		t.Fatalf("kind = %v, want KindCircuitOpen", errKind(t, err2))
	}
}

// TestCircuitBreakerKeysAreIndependent proves one endpoint's failures do not
// affect another's state.
func TestCircuitBreakerKeysAreIndependent(t *testing.T) {
	clk := newBreakerClock()
	b := NewCircuitBreaker(CircuitBreakerConfig{FailureThreshold: 1, Now: clk.Now})

	report, _ := b.Allow("endpoint-a")
	report(false)

	if got := b.State("endpoint-a"); got != CircuitOpen {
		t.Fatalf("endpoint-a state = %v, want Open", got)
	}
	if got := b.State("endpoint-b"); got != CircuitClosed {
		t.Fatalf("endpoint-b state = %v, want Closed (independent of endpoint-a)", got)
	}
}

// TestBulkheadTryAcquireBoundsConcurrency proves TryAcquire refuses once the
// configured limit is in use, and admits again after a release.
func TestBulkheadTryAcquireBoundsConcurrency(t *testing.T) {
	bh := NewBulkhead()
	rel1, err := bh.TryAcquire("provider-a", 2)
	if err != nil {
		t.Fatalf("1st acquire: %v", err)
	}
	rel2, err := bh.TryAcquire("provider-a", 2)
	if err != nil {
		t.Fatalf("2nd acquire: %v", err)
	}
	if _, err := bh.TryAcquire("provider-a", 2); err == nil {
		t.Fatal("expected the 3rd acquire to be refused at capacity 2")
	} else if errKind(t, err) != types.KindAdmissionRejected {
		t.Fatalf("kind = %v, want KindAdmissionRejected", errKind(t, err))
	}

	rel1()
	rel3, err := bh.TryAcquire("provider-a", 2)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	rel2()
	rel3()
}

// TestBulkheadReleaseIsIdempotent proves calling a release function twice
// does not double-free a slot.
func TestBulkheadReleaseIsIdempotent(t *testing.T) {
	bh := NewBulkhead()
	rel, err := bh.TryAcquire("k", 1)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	rel()
	rel() // must not free a second, nonexistent slot

	rel2, err := bh.TryAcquire("k", 1)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if _, err := bh.TryAcquire("k", 1); err == nil {
		t.Fatal("double-release let a second concurrent holder in past capacity 1")
	}
	rel2()
}

// TestBulkheadAcquireBlocksThenAdmitsOnRelease proves Acquire waits for a
// slot rather than refusing immediately, and is woken by a release -- no
// sleep, synchronized entirely through channels.
func TestBulkheadAcquireBlocksThenAdmitsOnRelease(t *testing.T) {
	bh := NewBulkhead()
	rel, err := bh.TryAcquire("k", 1)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		rel2, err := bh.Acquire(context.Background(), "k", 1)
		if err != nil {
			t.Errorf("blocked acquire: %v", err)
			return
		}
		close(acquired)
		rel2()
	}()

	select {
	case <-acquired:
		t.Fatal("blocked Acquire returned before the slot was released")
	case <-time.After(50 * time.Millisecond):
	}

	rel()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked Acquire never admitted after release")
	}
}

// TestBulkheadAcquireHonoursContextCancellation proves a waiting Acquire
// returns the classified cancellation promptly instead of hanging.
func TestBulkheadAcquireHonoursContextCancellation(t *testing.T) {
	bh := NewBulkhead()
	rel, err := bh.TryAcquire("k", 1)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer rel()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := bh.Acquire(ctx, "k", 1)
		errCh <- err
	}()

	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected a cancellation error")
		}
		if errKind(t, err) != types.KindCanceled {
			t.Fatalf("kind = %v, want KindCanceled", errKind(t, err))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Acquire did not return promptly on cancellation")
	}
}

// A probe that reports after the breaker has already re-opened must still give
// its half-open slot back.
//
// Before this, report's state switch reached the decrement only in the
// CircuitHalfOpen case, and its CircuitOpen case deliberately did nothing --
// so a probe whose call outlived the transition consumed a slot permanently.
// Nothing failed loudly: the breaker simply admitted fewer probes on every
// subsequent recovery, until at HalfOpenMaxCalls leaked slots it admitted none
// and the endpoint could never be found healthy again. A circuit breaker that
// silently stops probing is indistinguishable from one that is working.
func TestAProbeReportingAfterAReopenReleasesItsSlot(t *testing.T) {
	clk := newBreakerClock()
	const maxProbes = 2
	b := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 1, SuccessThreshold: 100,
		HalfOpenMaxCalls: maxProbes, OpenDuration: time.Minute, Now: clk.Now,
	})
	const key = "flaky"

	// Trip it, then cross into half-open.
	first, err := b.Allow(key)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	first(false)
	clk.advance(time.Minute)

	// Admit both probes.
	probeA, err := b.Allow(key)
	if err != nil {
		t.Fatalf("first probe refused: %v", err)
	}
	probeB, err := b.Allow(key)
	if err != nil {
		t.Fatalf("second probe refused: %v", err)
	}

	// probeA fails, which re-opens the breaker while probeB is still running.
	probeA(false)
	if state := b.State(key); state != CircuitOpen {
		t.Fatalf("State = %v, want Open after a failed probe", state)
	}

	// probeB now reports into an Open breaker. Its slot has to come back.
	probeB(true)

	// Next recovery window: both slots must be available again.
	clk.advance(time.Minute)
	for i := 0; i < maxProbes; i++ {
		report, err := b.Allow(key)
		if err != nil {
			t.Fatalf("probe %d of %d refused after a full recovery window; a slot leaked when a probe reported into an open breaker: %v",
				i+1, maxProbes, err)
		}
		defer report(true)
	}

	// And the bound still holds -- releasing slots must not have removed it.
	if _, err := b.Allow(key); err == nil {
		t.Error("a third probe was admitted against a bound of two")
	}
}

// report can be called twice: the second call used to release a second slot,
// one another probe was holding.
func TestBreakerReportActsOnlyOnce(t *testing.T) {
	clk := newBreakerClock()
	b := NewCircuitBreaker(CircuitBreakerConfig{FailureThreshold: 1, OpenDuration: time.Second, HalfOpenMaxCalls: 2, SuccessThreshold: 2, Now: clk.Now})
	failing, _ := b.Allow("ep")
	failing(false)
	clk.advance(2 * time.Second)

	first, err := b.Allow("ep")
	if err != nil {
		t.Fatalf("first probe: %v", err)
	}
	second, err := b.Allow("ep")
	if err != nil {
		t.Fatalf("second probe: %v", err)
	}
	// Both slots are held; a third is refused.
	if _, err := b.Allow("ep"); err == nil {
		t.Fatal("a third probe was admitted with HalfOpenMaxCalls 2")
	}
	first(true)
	first(true)
	first(true)
	// Only one slot came back, and one success was counted: the breaker is not
	// closed (SuccessThreshold 2), and room for exactly one probe exists.
	if b.State("ep") != CircuitHalfOpen {
		t.Fatalf("state = %v, want half-open: a repeated report counted as more successes", b.State("ep"))
	}
	third, err := b.Allow("ep")
	if err != nil {
		t.Fatalf("a slot should be free: %v", err)
	}
	if _, err := b.Allow("ep"); err == nil {
		t.Fatal("two slots came free from one probe reporting three times")
	}
	third(true)
	second(true)
}

// The same holds for a call admitted while the breaker is closed: a failure
// reported three times is one failure, so a threshold of two is not reached.
func TestBreakerClosedStateReportActsOnlyOnce(t *testing.T) {
	clk := newBreakerClock()
	b := NewCircuitBreaker(CircuitBreakerConfig{FailureThreshold: 2, OpenDuration: time.Second, Now: clk.Now})
	report, err := b.Allow("ep")
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	report(false)
	report(false)
	report(false)
	if b.State("ep") != CircuitClosed {
		t.Fatalf("state = %v: a repeated report counted as more than one failure", b.State("ep"))
	}
}

// A call admitted while the breaker was closed, still running when it tripped
// and half-opened, said nothing about recovery: its late success counted toward
// closing the breaker with no probe having run.
func TestBreakerLateReportFromAClosedCallDoesNotCloseAHalfOpenBreaker(t *testing.T) {
	clk := newBreakerClock()
	b := NewCircuitBreaker(CircuitBreakerConfig{FailureThreshold: 1, OpenDuration: time.Second, Now: clk.Now})
	slow, err := b.Allow("ep") // admitted while closed, still running
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	failing, _ := b.Allow("ep")
	failing(false)
	if b.State("ep") != CircuitOpen {
		t.Fatal("the breaker did not open")
	}
	clk.advance(2 * time.Second)
	if b.State("ep") != CircuitHalfOpen {
		t.Fatal("the breaker did not half-open")
	}
	// Half-open is entered by the first Allow that sees it.
	probe, err := b.Allow("ep")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	slow(true)
	if b.State("ep") != CircuitHalfOpen {
		t.Fatalf("state = %v: a late success from a call that was not a probe closed the breaker", b.State("ep"))
	}
	probe(false)
	if b.State("ep") != CircuitOpen {
		t.Fatalf("state = %v after a failed probe, want open", b.State("ep"))
	}
	// And a late failure from a non-probe call does not re-open a half-open one.
	b2 := NewCircuitBreaker(CircuitBreakerConfig{FailureThreshold: 1, OpenDuration: time.Second, Now: clk.Now})
	late, _ := b2.Allow("k")
	failing2, _ := b2.Allow("k")
	failing2(false)
	clk.advance(2 * time.Second)
	probe2, _ := b2.Allow("k")
	late(false)
	if b2.State("k") != CircuitHalfOpen {
		t.Fatalf("state = %v: a late failure from a non-probe call re-opened a half-open breaker", b2.State("k"))
	}
	probe2(true)
	if b2.State("k") != CircuitClosed {
		t.Fatalf("state = %v after a clean probe, want closed", b2.State("k"))
	}
}
