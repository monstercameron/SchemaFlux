package ops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// M12/SC-002, SC-004. Two things Scheduler deliberately leaves out because
// they are not admission control: whether a given endpoint is healthy enough
// to keep sending it work (SC-002's circuit breaker and bulkhead), and
// whether a request that failed ambiguously was already served (SC-004's
// idempotency). types.KindCircuitOpen and types.KindAdmissionRejected have
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
			// Open -> HalfOpen transition and resets the probe counters.
			st.state = CircuitHalfOpen
			st.consecutiveOK = 0
			st.halfOpenInFlight = 0
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
	case CircuitClosed:
		// falls through to reporting below
	}
	b.mu.Unlock()

	return func(success bool) { b.report(key, success) }, nil
}

// report records a call's outcome against key's state.
func (b *CircuitBreaker) report(key string, success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	st, ok := b.breaker[key]
	if !ok {
		return // Allow was never called for this key; nothing to update.
	}

	switch st.state {
	case CircuitHalfOpen:
		if st.halfOpenInFlight > 0 {
			st.halfOpenInFlight--
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

// Do runs fn for key through the breaker: refused with a classified error
// when the breaker will not allow it, otherwise run and its outcome
// reported automatically. fn's own error, if any, is returned unchanged --
// the breaker's refusal and fn's failure are never confused with each other.
func Do[T any](b *CircuitBreaker, key string, fn func() (T, error)) (T, error) {
	var zero T
	report, err := b.Allow(key)
	if err != nil {
		return zero, err
	}
	result, fnErr := fn()
	report(fnErr == nil)
	return result, fnErr
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
	cap := bh.cap[key]
	if bh.used[key] >= cap {
		return nil, &types.OperationError{
			Kind:    types.KindAdmissionRejected,
			Op:      "resilience.Bulkhead",
			Message: fmt.Sprintf("bulkhead %q is full (%d/%d)", key, bh.used[key], cap),
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

// --- SC-004: idempotency and ambiguity -------------------------------------

// LogicalRequestID identifies one caller-visible request across every
// attempt made on its behalf. AttemptID identifies exactly one of those
// attempts. Both are opaque, randomly generated strings -- not sequence
// numbers -- so nothing about them is guessable or comparable across a
// process restart, which matters if a caller ever forwards one to a
// provider's idempotency-key header.
type LogicalRequestID string
type AttemptID string

// AttemptRecord is what a caller retrying a logical request needs to tell
// its attempts apart, and to know which ones may have already reached the
// provider.
type AttemptRecord struct {
	Logical LogicalRequestID
	Attempt AttemptID

	// Number is this attempt's 1-based position within the logical request.
	Number int

	// Ambiguous marks an attempt whose failure does not prove the request
	// was never served -- a timeout is the paradigm case: the request may
	// have reached the provider and produced a real, billed effect that
	// this process never learned about. It is not set for a failure that
	// provably never reached the provider (e.g. an admission refusal, which
	// never attempted a call at all).
	Ambiguous bool

	Err error
}

// idGenerator produces the random bytes behind an ID. A field rather than a
// direct crypto/rand.Read call so a test can inject a deterministic sequence
// instead of asserting only "the string is nonempty and unique", which is a
// weaker check than "these two calls produced these exact, different IDs".
type idGenerator func() string

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is a process-level emergency this library
		// cannot repair; falling back to a fixed string would make every
		// logical request collide, which is worse than the panic.
		panic(fmt.Sprintf("resilience: crypto/rand unavailable: %v", err))
	}
	return hex.EncodeToString(b[:])
}

// IdempotencyTracker hands out logical request IDs and, per logical
// request, a monotonically numbered attempt for every retry -- the
// structure SC-004 asks for: "stable logical request IDs across every
// attempt, a unique attempt ID per provider call".
type IdempotencyTracker struct {
	mu      sync.Mutex
	nextNum map[LogicalRequestID]int
	gen     idGenerator
}

// NewIdempotencyTracker builds a tracker using crypto/rand IDs.
func NewIdempotencyTracker() *IdempotencyTracker {
	return newIdempotencyTrackerWithGen(randomID)
}

func newIdempotencyTrackerWithGen(gen idGenerator) *IdempotencyTracker {
	return &IdempotencyTracker{nextNum: make(map[LogicalRequestID]int), gen: gen}
}

// Begin starts a new logical request and returns its ID.
func (t *IdempotencyTracker) Begin() LogicalRequestID {
	id := LogicalRequestID(t.gen())
	t.mu.Lock()
	t.nextNum[id] = 0
	t.mu.Unlock()
	return id
}

// NextAttempt allocates the next attempt for an existing logical request.
// Calling it for an ID Begin never returned still works -- it starts that
// ID's counter at zero -- because a caller integrating this into an
// existing retry loop may not always route through Begin first, and
// refusing to number an attempt is a worse failure mode than numbering one
// for an ID this tracker did not mint.
func (t *IdempotencyTracker) NextAttempt(id LogicalRequestID) AttemptRecord {
	t.mu.Lock()
	t.nextNum[id]++
	n := t.nextNum[id]
	t.mu.Unlock()
	return AttemptRecord{
		Logical: id,
		Attempt: AttemptID(t.gen()),
		Number:  n,
	}
}

// RunWithIdempotency runs fn, retrying up to maxAttempts times while an
// error is retryable per types.OperationError.Retryable() (the shared
// taxonomy every other retrying layer in this module uses -- see the
// package doc comment). It returns the full attempt history alongside the
// result, so a caller can see exactly which attempts happened and which of
// them are ambiguous: a *types.OperationError with KindTimeout is marked
// Ambiguous, because a client-observed timeout does not prove the provider
// never received or acted on the request, and the library performs no side
// effects itself but its callers do (AGENTS.md's "never fail open" applies
// here in its narrower SC-004 form -- reporting a timed-out attempt as
// cleanly failed would hide that ambiguity from a caller who needs it to
// decide whether repeating the side effect is safe).
func RunWithIdempotency[T any](
	ctx context.Context,
	tracker *IdempotencyTracker,
	maxAttempts int,
	fn func(ctx context.Context, attempt AttemptRecord) (T, error),
) (T, LogicalRequestID, []AttemptRecord, error) {
	var zero T
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	logical := tracker.Begin()
	var history []AttemptRecord

	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		if err := ctx.Err(); err != nil {
			classified := classifyBlockingErr(err)
			rec := tracker.NextAttempt(logical)
			rec.Err = classified
			history = append(history, rec)
			return zero, logical, history, classified
		}

		rec := tracker.NextAttempt(logical)
		result, err := fn(ctx, rec)
		if err == nil {
			rec.Err = nil
			history = append(history, rec)
			return result, logical, history, nil
		}

		opErr, isOp := err.(*types.OperationError)
		if !isOp {
			opErr = &types.OperationError{Kind: types.KindUnknown, Cause: err}
		}
		if opErr.Kind == types.KindTimeout {
			rec.Ambiguous = true
		}
		rec.Err = err
		history = append(history, rec)
		lastErr = err

		if !opErr.Retryable() || i == maxAttempts-1 {
			break
		}
	}
	return zero, logical, history, lastErr
}
