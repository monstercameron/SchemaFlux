package ops

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// M12/SC-001..SC-006. MapReduce (CF-009) bounds concurrency for one chunked
// call to one operation; it has no notion of a second operation running at
// the same time, a tenant, a token or cost ceiling, or a queue that can be
// full. Every caller sharing a provider connection today shares nothing else
// — two unrelated operations in the same process can together drive as many
// goroutines and as much in-flight cost as the machine allows, and a caller
// that wants a hard ceiling on either has nowhere to set one.
//
// Scheduler is the process-wide admission gate that MapReduce is not: bounded
// concurrency, a bounded queue distinct from that concurrency limit, in-flight
// token and cost ceilings, and per-provider/per-tenant limits, all checked
// atomically before anything runs. It deliberately knows nothing about
// prompts, providers, or retries — Submit takes an opaque function to run and
// returns exactly what that function returns. Rate limiting (mw.RateLimit),
// retry (mw.Retry), and per-call budget (mw.Budget) already exist at the
// llm.Provider seam and are unchanged; this is a layer above all three, for
// bounding how many of *any* of them can be in flight together.

// SchedPriority is a bounded set of scheduling classes, not an arbitrary integer.
// SC-003 asks for "bounded priority classes rather than arbitrary integers"
// specifically because an unbounded priority field is how a caller invents a
// class of urgency nobody else's code checks for, and ends up starving
// everything below it. Three levels is enough to express "this can wait",
// "normal", and "this cannot wait" without becoming a second scheduler.
type SchedPriority int

const (
	SchedPriorityLow SchedPriority = iota
	SchedPriorityNormal
	SchedPriorityHigh
)

// priorityOrder is the order the dispatcher services priority classes in:
// every High-priority tenant queue is drained before any Normal one is even
// considered. SchedPriority only ever changes *ordering* among admittable work —
// see globalCapacityLocked/bulkheadCapacityLocked, which every request is
// checked against regardless of priority —
// never bypasses a concurrency, token, cost, provider, or tenant limit. A
// priority that could buy its way past a locked limit would let a caller
// claim urgency to dodge a tenant cap, which SC-003 explicitly forbids.
var priorityOrder = []SchedPriority{SchedPriorityHigh, SchedPriorityNormal, SchedPriorityLow}

// SchedulerLimits bounds what the scheduler will admit at once. A zero field
// means "no limit on this axis" — a scheduler with every field zero behaves
// as an uncapped pass-through, which is what a caller who only wants the
// fairness and cancellation guarantees, not the ceilings, should get without
// having to invent large numbers.
type SchedulerLimits struct {
	// MaxConcurrent bounds how many Submit calls may be running their work at
	// once, process-wide.
	MaxConcurrent int

	// MaxQueued bounds how many Submit calls may be waiting for a slot at
	// once. This is deliberately a separate number from MaxConcurrent: a
	// caller who wants to smooth a burst needs a queue; a caller who wants to
	// refuse a burst outright sets MaxQueued to zero and gets an immediate
	// ErrAdmissionRejected the moment MaxConcurrent is exhausted, with no
	// goroutine ever parked waiting.
	MaxQueued int

	// MaxInFlightTokens bounds the sum of EstimatedTokens across everything
	// currently running. Zero means unbounded.
	MaxInFlightTokens int64

	// MaxInFlightCost bounds the sum of EstimatedCost across everything
	// currently running. Zero means unbounded.
	MaxInFlightCost float64

	// PerProviderConcurrency bounds how many requests naming the same
	// SubmitRequest.Provider may run at once. Zero means unbounded (subject
	// only to MaxConcurrent).
	PerProviderConcurrency int

	// PerTenantConcurrency bounds how many requests naming the same
	// SubmitRequest.Tenant may run at once. Zero means unbounded.
	PerTenantConcurrency int
}

// SubmitRequest describes one unit of work being asked for a slot.
type SubmitRequest struct {
	// Tenant is a bounded workload identifier used for fairness and the
	// per-tenant concurrency limit — a project ID, an API key ID, a batch
	// job ID. It is never end-user PII: the scheduler logs and reports it in
	// errors and stats, and this library never logs a caller's data (see
	// AGENTS.md, "Never log or embed the caller's payload"). The empty
	// string is a valid tenant — the default, unpartitioned bucket.
	Tenant string

	// Provider names the target this request will call, for the per-provider
	// concurrency limit and for SC-002's bulkhead/breaker keying. The empty
	// string is a valid, unpartitioned provider.
	Provider string

	// Priority is this request's scheduling class. Go's zero value for
	// SchedPriority is SchedPriorityLow (0), not SchedPriorityNormal, so an
	// unset Priority is the *lowest* class rather than silently claiming
	// Normal. A caller who wants Normal states it.
	Priority SchedPriority

	// EstimatedTokens and EstimatedCost are what admission weighs against
	// MaxInFlightTokens and MaxInFlightCost. They are the caller's estimate,
	// not a measurement — nothing here invents a number the caller did not
	// supply; an unset estimate of zero simply does not count against the
	// ceiling, which is correct for a caller who has not chosen to track it.
	EstimatedTokens int64
	EstimatedCost   float64

	// Deadline, if set, is the latest time this request should still start
	// running. A deadline already in the past when Submit is called is an
	// unmeetable deadline — SC-001 asks that this be refused rather than
	// queued, because queuing something that cannot possibly be served in
	// time only occupies a queue slot another request could have used.
	// Deadline does not bound how long the *work itself* runs — that is the
	// caller's ctx.
	Deadline time.Time
}

// ErrSchedulerClosed is the sentinel behind a shutdown refusal, for a caller
// that wants errors.Is without inspecting the classified kind.
var ErrSchedulerClosed = types.ErrShutdown

// clock is the time source the scheduler and its dispatcher use, injected so
// tests can control "the deadline already passed" without a real sleep.
type clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Scheduler is the bounded, cancellable, fair admission gate described above.
// The zero value is not usable; construct with NewScheduler.
type Scheduler struct {
	limits SchedulerLimits
	clock  clock

	mu     sync.Mutex
	closed bool
	stopCh chan struct{}

	// Admitted resource accounting. Protected by mu.
	inFlight         int
	inFlightTokens   int64
	inFlightCost     float64
	providerInFlight map[string]int
	tenantInFlight   map[string]int

	// The bounded queue: per-tenant, per-priority FIFO lists, plus the total
	// count across all of them (what MaxQueued actually bounds) and a
	// round-robin cursor per priority so SC-003's fairness rotates through
	// tenants rather than draining one tenant's backlog before considering
	// the next.
	queuedTotal int
	tenantOrder []string // insertion order of tenants seen, for a stable rotation
	queues      map[string]map[SchedPriority][]*schedWaiter
	cursor      map[SchedPriority]int

	// wake is replaced (closed, then a fresh channel installed) every time
	// admitted state changes in a way that might let a queued waiter in:
	// a completion frees a slot, a new item is queued, or the scheduler
	// closes. The dispatcher blocks on it instead of polling, and every
	// waiter session gets a snapshot taken under the same lock as the state
	// it is trying to react to, so there is no window where a wake is missed.
	wake chan struct{}

	// inFlightCancel holds the cancel function for every admitted request's
	// derived context, keyed by a private sequence number, so Close can
	// cancel accepted-but-still-running work (SC-005, SC-006) without
	// needing the caller's own goroutine to notice on its own.
	inFlightCancel map[uint64]context.CancelFunc
	nextSeq        uint64

	wg sync.WaitGroup
}

// schedWaiter is one queued Submit call.
type schedWaiter struct {
	req      SubmitRequest
	admitted chan schedOutcome // buffered 1
	canceled bool              // set under Scheduler.mu once the waiter has given up
}

type schedOutcome struct {
	err error // nil means admitted
}

// NewScheduler builds a Scheduler honouring limits. limits with every field
// zero is an uncapped pass-through: Submit still enforces fairness and
// cancellation, but nothing is ever queued or refused for being over a
// ceiling that was never set.
func NewScheduler(limits SchedulerLimits) *Scheduler {
	return newSchedulerWithClock(limits, realClock{})
}

func newSchedulerWithClock(limits SchedulerLimits, c clock) *Scheduler {
	if c == nil {
		c = realClock{}
	}
	s := &Scheduler{
		limits:           limits,
		clock:            c,
		stopCh:           make(chan struct{}),
		providerInFlight: make(map[string]int),
		tenantInFlight:   make(map[string]int),
		queues:           make(map[string]map[SchedPriority][]*schedWaiter),
		cursor:           make(map[SchedPriority]int),
		wake:             make(chan struct{}),
		inFlightCancel:   make(map[uint64]context.CancelFunc),
	}
	go s.dispatchLoop()
	return s
}

// Stats is a point-in-time snapshot for tests and operators. It is not part
// of any hot path.
type SchedulerStats struct {
	InFlight       int
	Queued         int
	InFlightTokens int64
	InFlightCost   float64
}

// Stats reports the scheduler's current admitted and queued counts.
func (s *Scheduler) Stats() SchedulerStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SchedulerStats{
		InFlight:       s.inFlight,
		Queued:         s.queuedTotal,
		InFlightTokens: s.inFlightTokens,
		InFlightCost:   s.inFlightCost,
	}
}

// Submit blocks the calling goroutine until req is admitted, cancelled, or
// refused, then runs work and releases req's reservation whether work
// succeeds, fails, or panics.
//
// Refusal is always a classified *types.OperationError with
// types.KindAdmissionRejected (a full queue, a past deadline, or — see
// SC-006 — a closed scheduler, which classifies as types.KindShutdown
// instead, because "the scheduler is gone" and "this particular request did
// not fit" are different things a caller needs to tell apart). Submit never
// returns a zero value silently: an admitted-then-cancelled request returns
// the classified cancellation, and a request that never got a slot returns
// the classified refusal. Nothing submitted is ever just dropped.
func Submit[T any](ctx context.Context, s *Scheduler, req SubmitRequest, run func(ctx context.Context) (T, error)) (T, error) {
	var zero T
	if s == nil {
		return zero, fmt.Errorf("scheduler: Submit called with a nil Scheduler")
	}
	if run == nil {
		return zero, fmt.Errorf("scheduler: Submit called with no work to run")
	}
	if err := ctx.Err(); err != nil {
		return zero, classifyBlockingErr(err)
	}

	waiter, err := s.enqueue(req)
	if err != nil {
		return zero, err
	}

	select {
	case outcome := <-waiter.admitted:
		if outcome.err != nil {
			return zero, outcome.err
		}
		// fallthrough: admitted, run below.
	case <-ctx.Done():
		s.cancelWaiter(waiter)
		return zero, classifyBlockingErr(ctx.Err())
	}

	runCtx, cancel := context.WithCancel(ctx)
	seq := s.registerInFlight(cancel)
	s.wg.Add(1)
	defer func() {
		s.wg.Done()
		s.release(req, seq)
	}()

	result, runErr := run(runCtx)
	return result, runErr
}

// classifyBlockingErr turns a context error observed at a scheduler
// boundary (the queue wait, in this file) into the shared taxonomy, the same
// way every other blocking wait in this module does — llm.Classify is the
// one place that decides what a context.Canceled or context.DeadlineExceeded
// means, so this does not invent a second opinion.
func classifyBlockingErr(err error) error {
	if err == nil {
		return nil
	}
	kind := llm.Classify(err)
	return &types.OperationError{Kind: kind, Op: "scheduler.Submit", Cause: err}
}

// enqueue admits req immediately if the queue is otherwise idle and a slot is
// free, or places it in the bounded per-tenant/priority queue. It never
// blocks: a full queue is refused synchronously, "rather than allocating
// goroutines" per SC-001.
func (s *Scheduler) enqueue(req SubmitRequest) (*schedWaiter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, &types.OperationError{
			Kind:    types.KindShutdown,
			Op:      "scheduler.Submit",
			Message: "scheduler is closed",
		}
	}

	if !req.Deadline.IsZero() && req.Deadline.Before(s.clock.Now()) {
		return nil, &types.OperationError{
			Kind:    types.KindAdmissionRejected,
			Op:      "scheduler.Submit",
			Message: "deadline already passed before admission was attempted",
		}
	}

	if s.limits.MaxQueued > 0 && s.queuedTotal >= s.limits.MaxQueued {
		return nil, &types.OperationError{
			Kind:    types.KindAdmissionRejected,
			Op:      "scheduler.Submit",
			Message: fmt.Sprintf("queue is full (%d waiting, limit %d)", s.queuedTotal, s.limits.MaxQueued),
		}
	}

	w := &schedWaiter{req: req, admitted: make(chan schedOutcome, 1)}
	s.pushLocked(w)
	s.wakeLocked()
	return w, nil
}

func (s *Scheduler) pushLocked(w *schedWaiter) {
	tenant := w.req.Tenant
	if _, ok := s.queues[tenant]; !ok {
		s.queues[tenant] = make(map[SchedPriority][]*schedWaiter)
		s.tenantOrder = append(s.tenantOrder, tenant)
	}
	s.queues[tenant][w.req.Priority] = append(s.queues[tenant][w.req.Priority], w)
	s.queuedTotal++
}

// cancelWaiter removes w from the queue if it is still there and delivers no
// outcome — the caller already gave up via ctx and reports its own
// classified error; a late admission racing with this must not be silently
// lost, so a waiter that the dispatcher admits between the caller's ctx.Done
// firing and this call running is handled by draining (see below).
func (s *Scheduler) cancelWaiter(w *schedWaiter) {
	s.mu.Lock()
	if !w.canceled {
		w.canceled = true
		s.removeFromQueueLocked(w)
	}
	admittedAfterAll := false
	select {
	case outcome := <-w.admitted:
		// The dispatcher admitted this waiter in the window between ctx
		// firing and this lock being taken. Its reservation must not leak:
		// release exactly what admit() reserved.
		if outcome.err == nil {
			admittedAfterAll = true
		}
	default:
	}
	s.mu.Unlock()

	if admittedAfterAll {
		s.releaseReservationOnly(w.req)
	}
}

// removeFromQueueLocked deletes w from its tenant/priority list if present.
// Called with mu held.
func (s *Scheduler) removeFromQueueLocked(w *schedWaiter) {
	byPriority := s.queues[w.req.Tenant]
	if byPriority == nil {
		return
	}
	list := byPriority[w.req.Priority]
	for i, candidate := range list {
		if candidate == w {
			byPriority[w.req.Priority] = append(list[:i], list[i+1:]...)
			s.queuedTotal--
			return
		}
	}
}

// globalCapacityLocked reports whether the scheduler as a whole has room.
//
// It is separated from the bulkheads because the two refusals mean different
// things to the dispatcher: no global room means *nothing* can be admitted and
// scanning further down a queue is wasted work, while a bulkhead refusal is
// specific to one request and the item behind it may well be admittable.
// Conflating them is what produced the head-of-line block described on
// nextAdmittableAtPriorityLocked.
func (s *Scheduler) globalCapacityLocked(req SubmitRequest) bool {
	l := s.limits
	if l.MaxConcurrent > 0 && s.inFlight >= l.MaxConcurrent {
		return false
	}
	if l.MaxInFlightTokens > 0 && s.inFlightTokens+req.EstimatedTokens > l.MaxInFlightTokens {
		return false
	}
	if l.MaxInFlightCost > 0 && s.inFlightCost+req.EstimatedCost > l.MaxInFlightCost {
		return false
	}
	return true
}

// bulkheadCapacityLocked reports whether this request's provider and tenant
// have room.
func (s *Scheduler) bulkheadCapacityLocked(req SubmitRequest) bool {
	l := s.limits
	if l.PerProviderConcurrency > 0 && s.providerInFlight[req.Provider] >= l.PerProviderConcurrency {
		return false
	}
	if l.PerTenantConcurrency > 0 && s.tenantInFlight[req.Tenant] >= l.PerTenantConcurrency {
		return false
	}
	return true
}

func (s *Scheduler) reserveLocked(req SubmitRequest) {
	s.inFlight++
	s.inFlightTokens += req.EstimatedTokens
	s.inFlightCost += req.EstimatedCost
	s.providerInFlight[req.Provider]++
	s.tenantInFlight[req.Tenant]++
}

func (s *Scheduler) unreserveLocked(req SubmitRequest) {
	s.inFlight--
	s.inFlightTokens -= req.EstimatedTokens
	s.inFlightCost -= req.EstimatedCost
	s.providerInFlight[req.Provider]--
	if s.providerInFlight[req.Provider] <= 0 {
		delete(s.providerInFlight, req.Provider)
	}
	s.tenantInFlight[req.Tenant]--
	if s.tenantInFlight[req.Tenant] <= 0 {
		delete(s.tenantInFlight, req.Tenant)
	}
}

// registerInFlight records the cancel function for an admitted request so
// Close can reach it, and returns the sequence number to release it by.
func (s *Scheduler) registerInFlight(cancel context.CancelFunc) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq := s.nextSeq
	s.nextSeq++
	s.inFlightCancel[seq] = cancel
	return seq
}

// release returns req's reservation to the pool and wakes the dispatcher so
// a queued waiter can take the freed capacity.
func (s *Scheduler) release(req SubmitRequest, seq uint64) {
	s.mu.Lock()
	s.unreserveLocked(req)
	delete(s.inFlightCancel, seq)
	s.wakeLocked()
	s.mu.Unlock()
}

// releaseReservationOnly undoes reserveLocked's accounting without touching
// inFlightCancel, for the cancelWaiter race where a waiter was admitted but
// the caller had already stopped waiting on it — Submit's own run/release
// path never started, so there is no cancel function to remove.
func (s *Scheduler) releaseReservationOnly(req SubmitRequest) {
	s.mu.Lock()
	s.unreserveLocked(req)
	s.wakeLocked()
	s.mu.Unlock()
}

// wakeLocked signals the dispatcher. Called with mu held. Replacing the
// channel (rather than sending on it) means any number of state changes
// between two dispatcher wake-ups collapse into a single pass, and nobody
// can block trying to send to a dispatcher that is busy.
func (s *Scheduler) wakeLocked() {
	close(s.wake)
	s.wake = make(chan struct{})
}

// dispatchLoop is the scheduler's one background goroutine. It is the only
// "owned worker" a Scheduler has of its own — every other goroutine
// involved in a Submit call belongs to the caller — which is what makes
// SC-006's Close well-defined: stop this loop, and every accepted
// reservation either already has a caller goroutine running it (cancelled
// via the derived context) or is still queued (rejected outright).
//
// Draining the queue (admitAllLocked) and capturing the channel to block on
// happen inside the SAME lock acquisition, on purpose. An earlier version
// took two separate critical sections — drain, unlock, relock, capture
// wake, unlock, block — and lost wakeups across the gap between them: an
// enqueue arriving in that gap replaced and closed the wake channel before
// this loop had captured it, so the loop went on to block on the *new*
// channel and never learned that anything had been queued at all. The
// symptom was a Submit call parked forever with nothing else running — a
// hang, not a failure, which is worse. Folding both into one critical
// section removes the gap: whatever wakeLocked call was going to signal
// this pass either happened before the lock (so admitAllLocked already saw
// its item) or happens after wake is captured under the same lock (so it
// closes the exact channel this loop is about to block on).
func (s *Scheduler) dispatchLoop() {
	for {
		s.mu.Lock()
		s.admitAllLocked()
		if s.closed {
			s.mu.Unlock()
			return
		}
		wake := s.wake
		s.mu.Unlock()

		select {
		case <-wake:
		case <-s.stopCh:
			return
		}
	}
}

// admitAllLocked admits every currently-admittable queued waiter, in fair
// rotation, until a full pass finds nothing left that fits. Called with mu
// held.
func (s *Scheduler) admitAllLocked() {
	for {
		w, ok := s.nextAdmittableLocked()
		if !ok {
			return
		}
		s.removeFromQueueLocked(w)
		s.reserveLocked(w.req)
		w.admitted <- schedOutcome{}
	}
}

// nextAdmittableLocked scans priority classes high to low; within a class it
// rotates through tenants starting after the last tenant serviced at that
// class, so a tenant with ten thousand queued items and a tenant with ten
// both get a turn each pass instead of the large tenant's backlog draining
// first (SC-003). Canceled waiters found along the way are dropped, not
// counted as a turn. Called with mu held.
func (s *Scheduler) nextAdmittableLocked() (*schedWaiter, bool) {
	for _, p := range priorityOrder {
		if w, ok := s.nextAdmittableAtPriorityLocked(p); ok {
			return w, true
		}
	}
	return nil, false
}

func (s *Scheduler) nextAdmittableAtPriorityLocked(p SchedPriority) (*schedWaiter, bool) {
	n := len(s.tenantOrder)
	if n == 0 {
		return nil, false
	}
	start := s.cursor[p] % n
	for i := 0; i < n; i++ {
		idx := (start + i) % n
		tenant := s.tenantOrder[idx]
		byPriority := s.queues[tenant]
		if byPriority == nil {
			continue
		}
		list := byPriority[p]
		// Drop any canceled waiters at the front so a stale entry never
		// blocks this tenant's turn.
		for len(list) > 0 && list[0].canceled {
			list = list[1:]
			s.queuedTotal--
		}
		byPriority[p] = list
		if len(list) == 0 {
			continue
		}
		// Scan past waiters whose *bulkhead* is full rather than stopping at
		// the head.
		//
		// The head-only version turned a per-provider bulkhead into a
		// per-tenant head-of-line block: four requests for a saturated
		// provider ahead of one for an idle provider left the idle one queued
		// behind them, so one busy provider stalled every other provider's
		// work. That is the precise failure a bulkhead exists to prevent, and
		// it showed up as an intermittent test failure -- InFlight 1, Queued 4
		// -- which reads like flakiness and is not.
		//
		// Skipping is only correct for a bulkhead refusal. If the scheduler
		// has no global room, nothing further down can be admitted either, so
		// the loop stops rather than walking the whole queue to no purpose.
		// FIFO still holds within a provider, because the scan preserves queue
		// order and only steps over requests that could not run yet anyway.
		for _, w := range list {
			if w.canceled {
				continue
			}
			if !s.globalCapacityLocked(w.req) {
				return nil, false
			}
			if s.bulkheadCapacityLocked(w.req) {
				s.cursor[p] = (idx + 1) % n
				return w, true
			}
		}
	}
	return nil, false
}

// Close stops accepting and scheduling new work, refuses everything still
// queued with a classified shutdown error, cancels every admitted request's
// derived context, and waits for accepted work to actually return — up to
// ctx's deadline, if it has one. It is safe to call more than once and from
// more than one goroutine: the second and later calls are no-ops.
//
// Close does not, and cannot, kill the goroutine running a caller's `run`
// function outright — Go has no such primitive. What it guarantees is that
// every in-flight run's context is cancelled, so any run function that
// checks ctx (every provider call in this codebase does) returns promptly.
// A run function that ignores ctx is a bug in the caller, not something
// Close can fix from outside.
func (s *Scheduler) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stopCh)

	// Refuse everything still queued, in one pass, rather than leaving them
	// to time out on their own — an item nobody ever answers is a dropped
	// item wearing a queue's clothing.
	for _, tenant := range s.tenantOrder {
		for _, p := range priorityOrder {
			for _, w := range s.queues[tenant][p] {
				w.admitted <- schedOutcome{err: &types.OperationError{
					Kind:    types.KindShutdown,
					Op:      "scheduler.Submit",
					Message: "scheduler closed while request was queued",
				}}
			}
		}
	}
	s.queues = make(map[string]map[SchedPriority][]*schedWaiter)
	s.queuedTotal = 0

	// Cancel every request that is already running.
	cancels := make([]context.CancelFunc, 0, len(s.inFlightCancel))
	for _, cancel := range s.inFlightCancel {
		cancels = append(cancels, cancel)
	}
	s.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return &types.OperationError{
			Kind:    types.KindShutdown,
			Op:      "scheduler.Close",
			Message: "grace period elapsed with work still in flight",
			Cause:   ctx.Err(),
		}
	}
}
