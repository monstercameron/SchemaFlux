package ops

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// M12/SC-002. Something Scheduler deliberately leaves out because it is not
// admission control: whether a given endpoint is healthy enough to keep
// sending it work (SC-002's circuit breaker and bulkhead).
// types.KindCircuitOpen and types.KindAdmissionRejected have
// existed in the taxonomy since the errorkind.go rebuild, but nothing in the
// module produced them from real state before this file -- there was a
// sentinel with no breaker behind it.
//
// Rate limiting (mw.RateLimit), retry with decorrelated jitter and
// Retry-After (mw.Retry), and per-call budget (mw.Budget) already live at
// the llm.Provider seam and are unchanged here. CircuitBreaker and Bulkhead
// are written as free-standing, generically keyed primitives rather than
// llm.Provider middleware, because Scheduler.Submit is also a legitimate
// place to want them (a plan node, not only a raw completion call) -- an
// mw-only implementation would have to be reinvented at that seam. Thin
// mw wrappers (mw/circuitbreaker.go, mw/bulkhead.go) key them by provider
// name and model at the Handler boundary, the same way mw.RateLimit and
// mw.Retry already key their own state.

// CircuitState is the three-state machine every circuit breaker in this
// package implements: Closed (calls flow), Open (calls are refused without
// being attempted), HalfOpen (a limited number of probes are allowed through
// to decide whether to close again).
type CircuitState int

const (
	CircuitClosed CircuitState = iota
	CircuitOpen
	CircuitHalfOpen
)

func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreakerConfig configures a keyed breaker. Every field is required to
// have a sane value; NewCircuitBreaker fills in defaults for the zero value
// of each so a caller supplying only what they care about still gets a
// working breaker rather than one that never opens or never recovers.
type CircuitBreakerConfig struct {
	// FailureThreshold is the number of consecutive failures, for a single
	// key, that trips the breaker from Closed to Open. Default 5.
	FailureThreshold int

	// SuccessThreshold is the number of consecutive successes a HalfOpen
	// probe run needs before the breaker returns to Closed. Default 1: one
	// clean probe is enough to resume normal traffic, because HalfOpen only
	// allows a small number of probes through in the first place (see
	// HalfOpenMaxCalls) -- demanding many consecutive successes there just
	// delays recovery without buying more confidence.
	SuccessThreshold int

	// HalfOpenMaxCalls bounds how many probe calls may be in flight at once
	// while the breaker is HalfOpen. Default 1: a single probe is the
	// standard pattern, and anything higher risks hammering an endpoint that
	// only just failed with a fresh burst the moment it starts to recover.
	HalfOpenMaxCalls int

	// OpenDuration is how long the breaker stays Open before allowing a
	// HalfOpen probe. Default 30s.
	OpenDuration time.Duration

	// Now is the clock. Defaults to time.Now; tests inject a fake so "the
	// breaker opens and closes on conditions asserted, not elapsed time" (the
	// verification brief) holds -- every open/close decision in this file
	// reads Now() rather than starting a timer, so a test advances a fake
	// clock and asserts the transition rather than sleeping for real.
	Now func() time.Time
}

func (c CircuitBreakerConfig) withDefaults() CircuitBreakerConfig {
	if c.FailureThreshold <= 0 {
		c.FailureThreshold = 5
	}
	if c.SuccessThreshold <= 0 {
		c.SuccessThreshold = 1
	}
	if c.HalfOpenMaxCalls <= 0 {
		c.HalfOpenMaxCalls = 1
	}
	if c.OpenDuration <= 0 {
		c.OpenDuration = 30 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// CircuitBreaker holds one independent state machine per key ("endpoint",
// or "endpoint+model" -- SC-002 asks for both), guarded by a single mutex.
// A key is created lazily on first use and never removed; a caller cycling
// through unbounded key values (see the tenant-key warning on SubmitRequest)
// would leak memory here exactly as it would in Scheduler's tenantOrder,
// and the fix is the same: keys are bounded workload/endpoint identifiers,
// not per-request or per-user values.
type CircuitBreaker struct {
	cfg CircuitBreakerConfig

	mu      sync.Mutex
	breaker map[string]*breakerState
}

type breakerState struct {
	state            CircuitState
	consecutiveFails int
	consecutiveOK    int
	openedAt         time.Time
	halfOpenInFlight int
}

// NewCircuitBreaker builds a breaker with cfg, defaults applied.
func NewCircuitBreaker(cfg CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{cfg: cfg.withDefaults(), breaker: make(map[string]*breakerState)}
}

// State reports key's current state, for tests and operators. A key never
// seen before reports Closed, which is the correct state for something that
// has never failed.
func (b *CircuitBreaker) State(key string) CircuitState {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.breaker[key]
	if !ok {
		return CircuitClosed
	}
	return b.currentStateLocked(st)
}

// currentStateLocked resolves Open -> HalfOpen once OpenDuration has
// elapsed, purely from the state and the clock -- there is no timer running
// in the background; every check re-derives whether the open period has
// passed. Called with mu held.
func (b *CircuitBreaker) currentStateLocked(st *breakerState) CircuitState {
	if st.state == CircuitOpen && b.cfg.Now().Sub(st.openedAt) >= b.cfg.OpenDuration {
		return CircuitHalfOpen
	}
	return st.state
}

// Allow decides whether a call for key may proceed right now. On yes, it
// returns a report function the caller MUST call exactly once with the
// call's outcome; on no, it returns a classified types.KindCircuitOpen error
// and a nil report function.
//
// A HalfOpen breaker allows only cfg.HalfOpenMaxCalls concurrent probes
// through -- everything past that is refused exactly like an Open breaker,
// which is what stops a recovering endpoint from being hit with a fresh
// burst the instant its breaker starts to reconsider it.
func (b *CircuitBreaker) Allow(key string) (report func(success bool), err error) {
	record, err := b.allow(key)
	if err != nil {
		return nil, err
	}
	return func(success bool) {
		if success {
			record(callSucceeded)
		} else {
			record(callFailed)
		}
	}, nil
}

// callOutcome is what a call reports to the breaker. A call can also end with
// no verdict on the endpoint at all: its caller walked away, or the work
// panicked. That ends the call, and so gives back a half-open probe slot, but
// is neither a success nor a failure of the endpoint.
type callOutcome int

const (
	callSucceeded callOutcome = iota
	callFailed
	callNeutral
)

// allow is Allow with a report that takes a callOutcome. The report is safe to
// call more than once and acts only the first time: a second call used to
// release a second probe slot, which another probe was holding.
func (b *CircuitBreaker) allow(key string) (report func(callOutcome), err error) {
	b.mu.Lock()
	st, ok := b.breaker[key]
	if !ok {
		st = &breakerState{state: CircuitClosed}
		b.breaker[key] = st
	}

	switch b.currentStateLocked(st) {
	case CircuitOpen:
		b.mu.Unlock()
		return nil, &types.OperationError{
			Kind:    types.KindCircuitOpen,
			Op:      "resilience.CircuitBreaker",
			Message: fmt.Sprintf("circuit for %q is open", key),
		}
	case CircuitHalfOpen:
		if st.state != CircuitHalfOpen {
			// First observer to notice the open period elapsed performs the
			// Open -> HalfOpen transition.
			//
			// halfOpenInFlight is deliberately NOT reset here, and that is the
			// fix for a real overshoot: probes admitted in a previous half-open
			// window can still be running when the breaker re-opens and then
			// half-opens again, and zeroing the counter handed their slots out a
			// second time. With a bound of 3 that produced 4 concurrent probes
			// against an endpoint the breaker had already decided was fragile --
			// caught by chaos_resilience_test.go's peak-tracking test, which
			// measures the highest concurrency actually reached rather than
			// trusting the counter.
			//
			// The counter is safe to carry across the transition because every
			// admitted probe decrements it exactly once on report, in every
			// state -- see report below. Allow's contract already requires the
			// caller to report exactly once, which is what makes a
			// non-resetting counter correct rather than a leak.
			st.state = CircuitHalfOpen
			st.consecutiveOK = 0
		}
		if st.halfOpenInFlight >= b.cfg.HalfOpenMaxCalls {
			b.mu.Unlock()
			return nil, &types.OperationError{
				Kind:    types.KindCircuitOpen,
				Op:      "resilience.CircuitBreaker",
				Message: fmt.Sprintf("circuit for %q is half-open and already probing", key),
			}
		}
		st.halfOpenInFlight++
		b.mu.Unlock()
		// probe: true -- this call holds one of the bounded half-open slots and
		// must give it back whatever the breaker's state is by then.
		var once sync.Once
		return func(outcome callOutcome) { once.Do(func() { b.report(key, outcome, true) }) }, nil
	case CircuitClosed:
		// falls through to reporting below
	}
	b.mu.Unlock()

	var once sync.Once
	return func(outcome callOutcome) { once.Do(func() { b.report(key, outcome, false) }) }, nil
}

// report records a call's outcome against key's state.
//
// probe says whether this call was admitted as a half-open probe and therefore
// holds one of the bounded slots. It is released FIRST and unconditionally,
// before any state-specific handling, because the state can have changed since
// the probe was admitted -- a probe that started half-open and reports after the
// breaker re-opened still has to give its slot back, and the old code's
// state-switch never reached the decrement in that case.
func (b *CircuitBreaker) report(key string, outcome callOutcome, probe bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	st, ok := b.breaker[key]
	if !ok {
		return // Allow was never called for this key; nothing to update.
	}

	if probe && st.halfOpenInFlight > 0 {
		st.halfOpenInFlight--
	}

	// A call with no verdict on the endpoint gives back its slot and changes
	// nothing else.
	if outcome == callNeutral {
		return
	}
	success := outcome == callSucceeded

	switch st.state {
	case CircuitHalfOpen:
		if !probe {
			// A call admitted while the breaker was closed and still running
			// when it tripped and half-opened says nothing about the recovery a
			// probe tests. Its late success used to count toward closing the
			// breaker with no probe having run, and its failure toward re-opening
			// it.
			return
		}
		if success {
			st.consecutiveOK++
			if st.consecutiveOK >= b.cfg.SuccessThreshold {
				st.state = CircuitClosed
				st.consecutiveFails = 0
				st.consecutiveOK = 0
			}
		} else {
			// A single HalfOpen failure re-opens immediately -- a probe
			// exists to test recovery, and a failed probe is proof there
			// was none, not one data point among several.
			st.state = CircuitOpen
			st.openedAt = b.cfg.Now()
			st.consecutiveOK = 0
		}
	case CircuitClosed:
		if success {
			st.consecutiveFails = 0
			return
		}
		st.consecutiveFails++
		if st.consecutiveFails >= b.cfg.FailureThreshold {
			st.state = CircuitOpen
			st.openedAt = b.cfg.Now()
			st.consecutiveFails = 0
		}
	case CircuitOpen:
		// A report arriving for an already-open breaker is a stale probe
		// racing the transition; nothing to do.
	}
}

// Bulkhead limits how many callers may be inside a named section at once,
// independent of Scheduler's global/per-provider accounting -- for a caller
// that wants a hard concurrency wall around one provider's raw HTTP calls
// without adopting the Scheduler's queueing and fairness machinery at all.
// It never blocks past ctx: a full bulkhead either waits (Acquire) bounded
// by ctx or refuses immediately (TryAcquire), and both report a classified
// types.KindAdmissionRejected rather than a bare error, for the same reason
// Scheduler's refusals are classified -- a caller automating recovery needs
// to tell "this queue is full" apart from every other kind of failure.
type Bulkhead struct {
	mu   sync.Mutex
	cap  map[string]int
	used map[string]int
	wake map[string]chan struct{}
}

// NewBulkhead builds an empty Bulkhead. Capacities are set per key on first
// use via Acquire/TryAcquire's limit argument -- a Bulkhead has no global
// capacity of its own, only whatever each key was first asked to enforce.
// A second call naming the same key with a different limit keeps the
// original: a bulkhead whose capacity could be silently changed out from
// under callers already waiting on it is not a wall.
func NewBulkhead() *Bulkhead {
	return &Bulkhead{cap: make(map[string]int), used: make(map[string]int), wake: make(map[string]chan struct{})}
}

// TryAcquire attempts to take a slot for key without blocking. On success it
// returns a release function the caller must call exactly once.
func (bh *Bulkhead) TryAcquire(key string, limit int) (release func(), err error) {
	bh.mu.Lock()
	defer bh.mu.Unlock()
	return bh.tryLocked(key, limit)
}

func (bh *Bulkhead) tryLocked(key string, limit int) (func(), error) {
	if limit <= 0 {
		limit = 1
	}
	if _, ok := bh.cap[key]; !ok {
		bh.cap[key] = limit
	}
	capacity := bh.cap[key]
	if bh.used[key] >= capacity {
		return nil, &types.OperationError{
			Kind:    types.KindAdmissionRejected,
			Op:      "resilience.Bulkhead",
			Message: fmt.Sprintf("bulkhead %q is full (%d/%d)", key, bh.used[key], capacity),
		}
	}
	bh.used[key]++
	var released sync.Once
	return func() {
		released.Do(func() { bh.release(key) })
	}, nil
}

func (bh *Bulkhead) release(key string) {
	bh.mu.Lock()
	if bh.used[key] > 0 {
		bh.used[key]--
	}
	w := bh.wake[key]
	if w != nil {
		close(w)
		delete(bh.wake, key)
	}
	bh.mu.Unlock()
}

// Acquire blocks until a slot for key is free or ctx ends, whichever comes
// first. A caller cancelling out of a wait gets the classified cancellation
// error, not a zero value pretending to be a slot.
func (bh *Bulkhead) Acquire(ctx context.Context, key string, limit int) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, classifyBlockingErr(err)
	}
	for {
		bh.mu.Lock()
		rel, tryErr := bh.tryLocked(key, limit)
		if tryErr == nil {
			bh.mu.Unlock()
			return rel, nil
		}
		w, ok := bh.wake[key]
		if !ok {
			w = make(chan struct{})
			bh.wake[key] = w
		}
		bh.mu.Unlock()

		select {
		case <-w:
		case <-ctx.Done():
			return nil, classifyBlockingErr(ctx.Err())
		}
	}
}
