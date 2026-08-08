package ops

// Coverage gaps in scheduler.go: the real clock, classifyBlockingErr's two
// branches, the nil-clock default in newSchedulerWithClock, the per-tenant
// half of bulkheadCapacityLocked, and cancelWaiter's race branch (a waiter
// the dispatcher admits in the window between the caller's ctx firing and
// cancelWaiter taking the lock) together with releaseReservationOnly, the
// only thing that undoes that admission's reservation. These assert the
// scheduler's own contracts (a race-admitted reservation is never leaked, a
// context error is classified through the shared taxonomy, a tenant at its
// bulkhead limit is refused) rather than re-testing plumbing already covered
// by scheduler_test.go and the chaos suite.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// realClock.Now() must track the wall clock -- it is the only clock a
// caller-visible NewScheduler ever uses, and a scheduler test elsewhere
// asserting on deadlines depends on this being real time, not a stub with a
// bug of its own.
func TestRealClockNowTracksWallClock(t *testing.T) {
	before := time.Now()
	got := realClock{}.Now()
	after := time.Now()

	if got.Before(before) || got.After(after) {
		t.Errorf("realClock.Now() = %v, want between %v and %v", got, before, after)
	}
}

// A nil clock passed to newSchedulerWithClock must default to a real one,
// not leave the scheduler with a nil clock field that panics the moment a
// deadline is checked in enqueue.
func TestNewSchedulerWithClockDefaultsToRealClockWhenNil(t *testing.T) {
	s := newSchedulerWithClock(SchedulerLimits{}, nil)
	defer s.Close(context.Background())

	if s.clock == nil {
		t.Fatal("newSchedulerWithClock(_, nil) left the clock nil")
	}

	before := time.Now()
	got := s.clock.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Errorf("defaulted clock.Now() = %v, want between %v and %v", got, before, after)
	}
}

// classifyBlockingErr(nil) must be nil -- Submit's success path relies on
// this never wrapping a nil into a non-nil *OperationError.
func TestClassifyBlockingErrNilIsNil(t *testing.T) {
	if err := classifyBlockingErr(nil); err != nil {
		t.Errorf("classifyBlockingErr(nil) = %v, want nil", err)
	}
}

// A non-nil context error is classified through the shared taxonomy
// (llm.Classify), producing a *types.OperationError rooted at
// "scheduler.Submit" -- the same shape Submit's own ctx.Done() branch
// returns, asserted directly here rather than only indirectly through a
// full Submit call.
func TestClassifyBlockingErrWrapsContextCancellation(t *testing.T) {
	err := classifyBlockingErr(context.Canceled)
	if err == nil {
		t.Fatal("classifyBlockingErr(context.Canceled) = nil, want a classified error")
	}
	opErr := mustOpErr(t, err)
	if opErr.Op != "scheduler.Submit" {
		t.Errorf("Op = %q, want %q", opErr.Op, "scheduler.Submit")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false; the original cause must still be reachable")
	}
}

// bulkheadCapacityLocked must refuse on the per-tenant limit alone, with no
// per-provider limit configured at all -- the two checks are independent,
// and a defect that only ever exercised the provider half (the common case
// in load tests, which usually vary Provider) would leave this branch
// unproven.
func TestBulkheadCapacityLockedRefusesOnPerTenantLimitAlone(t *testing.T) {
	s := NewScheduler(SchedulerLimits{PerTenantConcurrency: 1})
	defer s.Close(context.Background())

	req := SubmitRequest{Tenant: "t1", Provider: "unbounded-provider"}

	s.mu.Lock()
	before := s.bulkheadCapacityLocked(req)
	s.tenantInFlight["t1"] = 1
	atLimit := s.bulkheadCapacityLocked(req)
	s.mu.Unlock()

	if !before {
		t.Fatal("setup: an idle tenant must have bulkhead room")
	}
	if atLimit {
		t.Error("bulkheadCapacityLocked = true for a tenant already at PerTenantConcurrency, want false")
	}
}

// cancelWaiter's race branch: the dispatcher admits a waiter in the window
// between the caller's ctx firing and cancelWaiter acquiring the lock. The
// real race is nondeterministic by nature, but the branch it exercises is
// not -- driving the admission directly (rather than waiting for the
// background dispatchLoop to happen to win the race) reproduces exactly the
// state cancelWaiter must handle: a waiter that is simultaneously "admitted"
// (its channel already carries an outcome) and "the caller gave up on it".
// releaseReservationOnly is the only code path that undoes such an
// admission's reservation, so this is also the only way to cover it: the
// reservation it releases must not otherwise leak.
func TestCancelWaiterReleasesReservationAdmittedDuringTheRace(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 1})
	defer s.Close(context.Background())

	req := SubmitRequest{Tenant: "t", Provider: "p", EstimatedTokens: 7, EstimatedCost: 1.5}
	w, err := s.enqueue(req)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Admit it directly under the lock, simulating the dispatcher winning
	// the race before the caller's cancellation is processed.
	s.mu.Lock()
	s.admitAllLocked()
	s.mu.Unlock()

	before := s.Stats()
	if before.InFlight != 1 {
		t.Fatalf("setup: want InFlight=1 after direct admission, got %+v", before)
	}

	// The caller gave up anyway -- exactly the sequence Submit's ctx.Done()
	// branch drives in production.
	s.cancelWaiter(w)

	after := s.Stats()
	if after.InFlight != 0 {
		t.Errorf("cancelWaiter did not release a reservation admitted during the race: InFlight = %d, want 0", after.InFlight)
	}
	if after.InFlightTokens != 0 || after.InFlightCost != 0 {
		t.Errorf("token/cost accounting leaked: tokens=%d cost=%v, want 0, 0", after.InFlightTokens, after.InFlightCost)
	}

	// A second cancelWaiter call (the ordinary "still queued, never admitted"
	// path) on a fresh waiter must not touch this one's already-released
	// state, and must not itself call releaseReservationOnly again.
	w2, err := s.enqueue(SubmitRequest{Tenant: "t2"})
	if err != nil {
		t.Fatalf("enqueue w2: %v", err)
	}
	s.cancelWaiter(w2)
	final := s.Stats()
	if final.Queued != 0 {
		t.Errorf("Queued = %d after cancelling the only queued waiter, want 0", final.Queued)
	}
}

// classifyBlockingErr's Op and Kind travel all the way out through Submit
// for a caller that cancels while queued, tying the unit-level assertions
// above to the integration surface Submit callers actually see.
func TestSubmitCancelledWhileQueuedReportsClassifiedError(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 1, MaxQueued: 1})
	defer s.Close(context.Background())

	release := make(chan struct{})
	defer close(release)
	var current, peak int64

	go Submit(context.Background(), s, SubmitRequest{}, gatedWork(&current, &peak, release))
	waitForStat(t, s, func(st SchedulerStats) bool { return st.InFlight == 1 })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Submit(ctx, s, SubmitRequest{}, gatedWork(&current, &peak, release))
		done <- err
	}()
	waitForStat(t, s, func(st SchedulerStats) bool { return st.Queued == 1 })

	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a classified cancellation error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("errors.Is(err, context.Canceled) = false: %v", err)
		}
		opErr := mustOpErr(t, err)
		if opErr.Kind != types.KindCanceled {
			t.Errorf("Kind = %v, want KindCanceled", opErr.Kind)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Submit did not return after its context was cancelled while queued")
	}
}
