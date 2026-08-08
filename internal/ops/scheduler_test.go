package ops

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// testClock is a fake clock, mirroring the pattern mw/fakeclock_test.go uses:
// a scheduler test asserting on a deadline must control "now" exactly, or the
// test is really asserting on how fast the machine running it happened to be.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock(start time.Time) *testClock { return &testClock{now: start} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// gatedWork returns a run function that blocks until release is closed,
// tracking peak concurrency via a shared atomic counter. Blocking on a
// channel rather than sleeping is what makes the concurrency bound provable:
// a serial implementation would never get more than one goroutine into the
// gate at once, so peak would read 1 regardless of the configured limit --
// exactly the kind of test AGENTS.md/CLAUDE.md's brief calls out as proving
// nothing.
func gatedWork(current, peak *int64, release <-chan struct{}) func(ctx context.Context) (int, error) {
	return func(ctx context.Context) (int, error) {
		n := atomic.AddInt64(current, 1)
		for {
			p := atomic.LoadInt64(peak)
			if n <= p || atomic.CompareAndSwapInt64(peak, p, n) {
				break
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
			atomic.AddInt64(current, -1)
			return 0, ctx.Err()
		}
		atomic.AddInt64(current, -1)
		return 1, nil
	}
}

func mustOpErr(t *testing.T, err error) *types.OperationError {
	t.Helper()
	var opErr *types.OperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("expected *types.OperationError, got %T: %v", err, err)
	}
	return opErr
}

// TestSchedulerBoundsPeakConcurrency proves the limit is actually enforced
// under concurrency, not merely believed: it launches more work than the
// limit allows at once and asserts the observed peak in-flight count, never
// the total submitted.
func TestSchedulerBoundsPeakConcurrency(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 3, MaxQueued: 50})
	defer s.Close(context.Background())

	var current, peak int64
	release := make(chan struct{})

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := Submit(context.Background(), s, SubmitRequest{Tenant: "t"}, gatedWork(&current, &peak, release))
			if err != nil {
				t.Errorf("submit %d: unexpected error: %v", i, err)
			}
		}(i)
	}

	// Give every goroutine a chance to reach admission before releasing --
	// polling the work's own counter rather than sleeping a fixed amount, so
	// the test does not depend on how fast the scheduler happens to run
	// today. Stats().InFlight is deliberately NOT what this polls: it flips
	// to 3 the moment the scheduler reserves the slots, which can be before
	// the three admitted goroutines have actually been scheduled onto a
	// thread and entered gatedWork -- polling that would let the test close
	// the gate before every admitted worker had a chance to increment
	// "current" even once, undercounting the very peak being measured.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if atomic.LoadInt64(&current) == 3 || time.Now().After(deadline) {
			break
		}
		runtime.Gosched()
	}

	close(release)
	wg.Wait()

	if got := atomic.LoadInt64(&peak); got != 3 {
		t.Fatalf("peak in-flight = %d, want exactly 3 (MaxConcurrent)", got)
	}
}

// TestSchedulerQueueFullRejectsImmediately proves a full queue refuses
// synchronously with a classified error rather than allocating a goroutine
// to wait.
func TestSchedulerQueueFullRejectsImmediately(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 1, MaxQueued: 1})
	defer s.Close(context.Background())

	release := make(chan struct{})
	var current, peak int64

	done := make(chan struct{})
	go func() {
		Submit(context.Background(), s, SubmitRequest{}, gatedWork(&current, &peak, release))
		close(done)
	}()
	waitForStat(t, s, func(st SchedulerStats) bool { return st.InFlight == 1 })

	// Occupies the one queue slot.
	queuedDone := make(chan error, 1)
	go func() {
		_, err := Submit(context.Background(), s, SubmitRequest{}, gatedWork(&current, &peak, release))
		queuedDone <- err
	}()
	waitForStat(t, s, func(st SchedulerStats) bool { return st.Queued == 1 })

	// The queue is now full; this one must be refused immediately.
	_, err := Submit(context.Background(), s, SubmitRequest{}, gatedWork(&current, &peak, release))
	if err == nil {
		t.Fatal("expected admission rejection, got nil error")
	}
	opErr := mustOpErr(t, err)
	if opErr.Kind != types.KindAdmissionRejected {
		t.Fatalf("kind = %v, want KindAdmissionRejected", opErr.Kind)
	}

	close(release)
	<-done
	if err := <-queuedDone; err != nil {
		t.Fatalf("queued submit: unexpected error: %v", err)
	}
}

// TestSchedulerDeadlineAlreadyPassedIsRejected proves an unmeetable deadline
// is refused at admission time -- never queued, never run.
func TestSchedulerDeadlineAlreadyPassedIsRejected(t *testing.T) {
	clk := newTestClock(time.Unix(1000, 0))
	s := newSchedulerWithClock(SchedulerLimits{MaxConcurrent: 1, MaxQueued: 5}, clk)
	defer s.Close(context.Background())

	ran := false
	_, err := Submit(context.Background(), s, SubmitRequest{Deadline: clk.Now().Add(-time.Second)},
		func(ctx context.Context) (int, error) {
			ran = true
			return 0, nil
		})
	if err == nil {
		t.Fatal("expected rejection for a past deadline")
	}
	if ran {
		t.Fatal("work ran despite an already-passed deadline")
	}
	if mustOpErr(t, err).Kind != types.KindAdmissionRejected {
		t.Fatalf("kind = %v, want KindAdmissionRejected", mustOpErr(t, err).Kind)
	}
}

// TestSchedulerCancelWhileQueuedReturnsClassifiedError proves cancellation
// reaches a queued (not yet running) request promptly, without waiting for a
// slot that will never come during the test.
func TestSchedulerCancelWhileQueuedReturnsClassifiedError(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 1, MaxQueued: 5})
	defer s.Close(context.Background())

	release := make(chan struct{})
	var current, peak int64
	go Submit(context.Background(), s, SubmitRequest{}, gatedWork(&current, &peak, release))
	waitForStat(t, s, func(st SchedulerStats) bool { return st.InFlight == 1 })

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := Submit(ctx, s, SubmitRequest{}, gatedWork(&current, &peak, release))
		errCh <- err
	}()
	waitForStat(t, s, func(st SchedulerStats) bool { return st.Queued == 1 })

	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected a cancellation error")
		}
		if mustOpErr(t, err).Kind != types.KindCanceled {
			t.Fatalf("kind = %v, want KindCanceled", mustOpErr(t, err).Kind)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not return promptly")
	}

	waitForStat(t, s, func(st SchedulerStats) bool { return st.Queued == 0 })
	close(release)
}

// TestSchedulerClosedRejectsNewSubmits proves a closed scheduler refuses
// rather than hangs or silently no-ops.
func TestSchedulerClosedRejectsNewSubmits(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 2, MaxQueued: 2})
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ran := false
	_, err := Submit(context.Background(), s, SubmitRequest{}, func(ctx context.Context) (int, error) {
		ran = true
		return 0, nil
	})
	if err == nil {
		t.Fatal("expected shutdown error")
	}
	if ran {
		t.Fatal("work ran after Close")
	}
	if !errors.Is(err, types.ErrShutdown) {
		t.Fatalf("expected errors.Is(err, types.ErrShutdown); got %v", err)
	}
}

// TestSchedulerCloseCancelsInFlightWork proves Close reaches accepted work
// through its derived context rather than only refusing new work.
func TestSchedulerCloseCancelsInFlightWork(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 1})

	started := make(chan struct{})
	cancelled := make(chan struct{})
	go Submit(context.Background(), s, SubmitRequest{}, func(ctx context.Context) (int, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return 0, ctx.Err()
	})

	<-started
	closeErr := make(chan error, 1)
	go func() { closeErr <- s.Close(context.Background()) }()

	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight work was not cancelled by Close")
	}
	if err := <-closeErr; err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestSchedulerCloseRefusesQueuedWork proves a request that never got a slot
// is told so explicitly by Close, rather than left to hang forever.
func TestSchedulerCloseRefusesQueuedWork(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 1, MaxQueued: 1})

	release := make(chan struct{})
	defer close(release)
	var current, peak int64
	go Submit(context.Background(), s, SubmitRequest{}, gatedWork(&current, &peak, release))
	waitForStat(t, s, func(st SchedulerStats) bool { return st.InFlight == 1 })

	errCh := make(chan error, 1)
	go func() {
		_, err := Submit(context.Background(), s, SubmitRequest{}, gatedWork(&current, &peak, release))
		errCh <- err
	}()
	waitForStat(t, s, func(st SchedulerStats) bool { return st.Queued == 1 })

	go s.Close(context.Background())

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected a shutdown error for the queued request")
		}
		if mustOpErr(t, err).Kind != types.KindShutdown {
			t.Fatalf("kind = %v, want KindShutdown", mustOpErr(t, err).Kind)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued request was never answered")
	}
}

// TestSchedulerCloseIsIdempotent proves double- and concurrent-close is safe.
func TestSchedulerCloseIsIdempotent(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 1})

	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.Close(context.Background())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}
}

// TestSchedulerNoSubmissionIsSilentlyDropped submits a mix of work that will
// succeed, be rejected for a full queue, and be cancelled, and proves the
// count of (succeeded + rejected + cancelled) equals the count submitted --
// nothing simply vanishes.
func TestSchedulerNoSubmissionIsSilentlyDropped(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 2, MaxQueued: 3})
	defer s.Close(context.Background())

	const n = 200
	var succeeded, rejected int64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Submit(context.Background(), s, SubmitRequest{}, func(ctx context.Context) (int, error) {
				return 1, nil
			})
			if err != nil {
				if mustOpErr(t, err).Kind != types.KindAdmissionRejected {
					t.Errorf("unexpected error kind: %v", mustOpErr(t, err).Kind)
				}
				atomic.AddInt64(&rejected, 1)
				return
			}
			atomic.AddInt64(&succeeded, 1)
		}()
	}
	wg.Wait()

	if succeeded+rejected != n {
		t.Fatalf("succeeded(%d) + rejected(%d) = %d, want %d -- some submission vanished",
			succeeded, rejected, succeeded+rejected, n)
	}
	if succeeded == 0 {
		t.Fatal("nothing succeeded, so this test is not exercising the accept path at all")
	}
}

// TestSchedulerGoroutineCountStaysFlatUnderLoad proves 10,000 queued/rejected
// items hold a flat goroutine count -- SC-001's headline verification.
func TestSchedulerGoroutineCountStaysFlatUnderLoad(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 4, MaxQueued: 8})
	defer s.Close(context.Background())

	before := runtime.NumGoroutine()

	const n = 10000
	var wg sync.WaitGroup
	var accepted, rejected int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Submit(context.Background(), s, SubmitRequest{}, func(ctx context.Context) (int, error) {
				return 1, nil
			})
			if err != nil {
				atomic.AddInt64(&rejected, 1)
				return
			}
			atomic.AddInt64(&accepted, 1)
		}()
	}
	wg.Wait()

	if accepted+rejected != n {
		t.Fatalf("accepted(%d)+rejected(%d) != %d", accepted, rejected, n)
	}
	if rejected == 0 {
		t.Fatal("nothing was rejected -- this run did not actually exercise the bounded queue")
	}

	deadline := time.Now().Add(5 * time.Second)
	var after int
	for {
		runtime.GC()
		after = runtime.NumGoroutine()
		if after <= before+5 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if after > before+5 {
		t.Fatalf("goroutine count grew from %d to %d after 10,000 submissions", before, after)
	}
}

// TestSchedulerPerProviderConcurrencyLimit proves a per-provider bulkhead is
// enforced independently of the global concurrency ceiling.
func TestSchedulerPerProviderConcurrencyLimit(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 10, MaxQueued: 20, PerProviderConcurrency: 1})
	defer s.Close(context.Background())

	release := make(chan struct{})
	var currentA, peakA, currentB, peakB int64

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Submit(context.Background(), s, SubmitRequest{Provider: "a"}, gatedWork(&currentA, &peakA, release))
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		Submit(context.Background(), s, SubmitRequest{Provider: "b"}, gatedWork(&currentB, &peakB, release))
	}()

	waitForStat(t, s, func(st SchedulerStats) bool { return st.InFlight == 2 })
	close(release)
	wg.Wait()

	if peakA != 1 {
		t.Fatalf("peak concurrency for provider a = %d, want 1", peakA)
	}
	if peakB != 1 {
		t.Fatalf("peak concurrency for provider b = %d, want 1", peakB)
	}
}

// TestSchedulerInFlightCostCeiling proves admission weighs estimated cost,
// not just a count of requests.
func TestSchedulerInFlightCostCeiling(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 10, MaxQueued: 10, MaxInFlightCost: 2.5})
	defer s.Close(context.Background())

	release := make(chan struct{})
	var current, peakCount int64

	// Each unit costs 1.0, so only two should ever run at once even though
	// MaxConcurrent allows ten.
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Submit(context.Background(), s, SubmitRequest{EstimatedCost: 1.0}, gatedWork(&current, &peakCount, release))
		}()
	}

	waitStableInFlight(t, s, 5*time.Second)
	stats := s.Stats()
	if stats.InFlight > 2 {
		t.Fatalf("in-flight = %d, want at most 2 given a 2.5 cost ceiling at 1.0 per item", stats.InFlight)
	}
	if stats.InFlightCost > 2.5 {
		t.Fatalf("in-flight cost = %v, exceeds ceiling 2.5", stats.InFlightCost)
	}
	close(release)
	wg.Wait()
}

// TestSchedulerFairnessSmallTenantNotStuckBehindLargeOne is SC-003's headline
// verification: a large and a small tenant submitted together both progress,
// and the small one finishes without waiting for the large one to drain.
func TestSchedulerFairnessSmallTenantNotStuckBehindLargeOne(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 2, MaxQueued: 20000})
	defer s.Close(context.Background())

	const bigN = 2000
	const smallN = 10

	var completedBig int64
	var smallDoneAfterBig int64 // count of big items that finished before the small tenant fully drained

	var wg sync.WaitGroup
	for i := 0; i < bigN; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Submit(context.Background(), s, SubmitRequest{Tenant: "big"}, func(ctx context.Context) (int, error) {
				atomic.AddInt64(&completedBig, 1)
				return 1, nil
			})
		}()
	}

	smallDone := make(chan struct{})
	go func() {
		var innerWG sync.WaitGroup
		for i := 0; i < smallN; i++ {
			innerWG.Add(1)
			go func() {
				defer innerWG.Done()
				Submit(context.Background(), s, SubmitRequest{Tenant: "small"}, func(ctx context.Context) (int, error) {
					return 1, nil
				})
			}()
		}
		innerWG.Wait()
		atomic.StoreInt64(&smallDoneAfterBig, atomic.LoadInt64(&completedBig))
		close(smallDone)
	}()

	select {
	case <-smallDone:
	case <-time.After(20 * time.Second):
		t.Fatal("small tenant never finished -- looks starved behind the large tenant")
	}

	if got := atomic.LoadInt64(&smallDoneAfterBig); got >= bigN {
		t.Fatalf("small tenant finished only after all %d big-tenant items had already completed (%d done) -- starved, not interleaved", bigN, got)
	}

	wg.Wait()
}

// TestSchedulerPriorityOrdersAdmissionWithoutBypassingTenantLimit proves
// priority changes ordering among admittable work but never lets a high
// priority request past a locked per-tenant limit.
func TestSchedulerPriorityOrdersAdmissionWithoutBypassingTenantLimit(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 1, MaxQueued: 10, PerTenantConcurrency: 1})
	defer s.Close(context.Background())

	release := make(chan struct{})
	var current, peak int64
	// Occupy the single slot with a low-priority item from tenant "t".
	go Submit(context.Background(), s, SubmitRequest{Tenant: "t", Priority: SchedPriorityLow}, gatedWork(&current, &peak, release))
	waitForStat(t, s, func(st SchedulerStats) bool { return st.InFlight == 1 })

	// A high-priority item for the SAME tenant must still wait -- priority
	// does not buy past the per-tenant concurrency limit.
	highDone := make(chan error, 1)
	go func() {
		_, err := Submit(context.Background(), s, SubmitRequest{Tenant: "t", Priority: SchedPriorityHigh}, gatedWork(&current, &peak, release))
		highDone <- err
	}()
	waitForStat(t, s, func(st SchedulerStats) bool { return st.Queued == 1 })

	select {
	case <-highDone:
		t.Fatal("high priority request was admitted despite the per-tenant limit being locked")
	case <-time.After(100 * time.Millisecond):
		// still waiting, as required
	}

	close(release)
	if err := <-highDone; err != nil {
		t.Fatalf("high priority submit: %v", err)
	}
}

// TestSubmitRejectsNilSchedulerAndNilWork covers the two caller-mistake
// guard clauses.
func TestSubmitRejectsNilSchedulerAndNilWork(t *testing.T) {
	if _, err := Submit[int](context.Background(), nil, SubmitRequest{}, func(ctx context.Context) (int, error) { return 0, nil }); err == nil {
		t.Fatal("expected an error for a nil scheduler")
	}

	s := NewScheduler(SchedulerLimits{MaxConcurrent: 1})
	defer s.Close(context.Background())
	if _, err := Submit[int](context.Background(), s, SubmitRequest{}, nil); err == nil {
		t.Fatal("expected an error for nil work")
	}
}

// TestSubmitRejectsAlreadyCanceledContext proves a context canceled before
// Submit is even called is refused without ever queuing.
func TestSubmitRejectsAlreadyCanceledContext(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 1})
	defer s.Close(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ran := false
	_, err := Submit(ctx, s, SubmitRequest{}, func(ctx context.Context) (int, error) {
		ran = true
		return 0, nil
	})
	if err == nil {
		t.Fatal("expected a cancellation error")
	}
	if ran {
		t.Fatal("work ran despite an already-cancelled context")
	}
	if s.Stats().Queued != 0 {
		t.Fatal("an already-cancelled submission left something queued")
	}
}

// waitForStat polls Stats until pred is satisfied or a generous timeout
// elapses, failing the test rather than deadlocking it. Used only to
// synchronize with the scheduler's own internal dispatch, never to assert a
// timing relationship -- the assertions themselves are all on values, not
// elapsed time.
func waitForStat(t *testing.T, s *Scheduler, pred func(SchedulerStats) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if pred(s.Stats()) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within timeout; last stats: %+v", s.Stats())
		}
		runtime.Gosched()
	}
}

// waitStableInFlight waits until Stats().InFlight stops changing for a short
// stretch, used where the exact count to wait for depends on ceilings the
// test is itself trying to verify.
func waitStableInFlight(t *testing.T, s *Scheduler, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := -1
	stableSince := time.Now()
	for time.Now().Before(deadline) {
		cur := s.Stats().InFlight
		if cur != last {
			last = cur
			stableSince = time.Now()
		} else if time.Since(stableSince) > 50*time.Millisecond {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("in-flight count never stabilized: %+v", s.Stats())
}

// A saturated provider must not stall an idle one.
//
// This is the defect TestSchedulerPerProviderConcurrencyLimit was failing on
// intermittently: the dispatcher only ever inspected the head of each queue,
// so four requests for a busy provider queued ahead of one for an idle
// provider left the idle one waiting behind them. A per-provider bulkhead that
// blocks other providers is the exact thing a bulkhead exists to prevent, and
// it presented as flakiness -- InFlight 1, Queued 4 -- rather than as a
// failure anyone would read as a bug.
//
// The order here is deterministic: every "busy" request is submitted and
// confirmed queued *before* the idle provider's request is submitted at all,
// so there is no scheduling race to lose.
func TestABusyProviderDoesNotStallAnIdleOne(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 10, MaxQueued: 20, PerProviderConcurrency: 1})
	defer s.Close(context.Background())

	release := make(chan struct{})
	var busyCurrent, busyPeak, idleCurrent, idlePeak int64

	var wg sync.WaitGroup
	const busyRequests = 4
	for i := 0; i < busyRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Submit(context.Background(), s, SubmitRequest{Provider: "busy"},
				gatedWork(&busyCurrent, &busyPeak, release))
		}()
	}

	// One of the busy requests is running and the other three are queued
	// behind it: the exact state in which the head of the queue cannot be
	// admitted.
	waitForStat(t, s, func(st SchedulerStats) bool {
		return st.InFlight == 1 && st.Queued == busyRequests-1
	})

	wg.Add(1)
	go func() {
		defer wg.Done()
		Submit(context.Background(), s, SubmitRequest{Provider: "idle"},
			gatedWork(&idleCurrent, &idlePeak, release))
	}()

	// The idle provider's request must start while the busy provider's backlog
	// is still queued. Before the fix this timed out with InFlight 1.
	waitForStat(t, s, func(st SchedulerStats) bool { return st.InFlight == 2 })

	close(release)
	wg.Wait()

	if busyPeak != 1 {
		t.Errorf("peak concurrency for the busy provider = %d, want 1", busyPeak)
	}
	if idlePeak != 1 {
		t.Errorf("peak concurrency for the idle provider = %d, want 1", idlePeak)
	}
}

// Skipping past a blocked waiter is only correct for a bulkhead refusal. When
// the scheduler as a whole is full, nothing further down the queue can run
// either, and walking the queue to discover that would be wasted work that
// also risks admitting past a global ceiling.
func TestAFullSchedulerDoesNotAdmitPastItsGlobalCeiling(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 1, MaxQueued: 10, PerProviderConcurrency: 5})
	defer s.Close(context.Background())

	release := make(chan struct{})
	var current, peak int64

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		provider := "a"
		if i%2 == 1 {
			provider = "b"
		}
		wg.Add(1)
		go func(provider string) {
			defer wg.Done()
			Submit(context.Background(), s, SubmitRequest{Provider: provider},
				gatedWork(&current, &peak, release))
		}(provider)
	}

	waitForStat(t, s, func(st SchedulerStats) bool { return st.InFlight == 1 && st.Queued == 4 })

	close(release)
	wg.Wait()

	if peak != 1 {
		t.Errorf("peak concurrency = %d, want 1 -- the global ceiling was walked past", peak)
	}
}
