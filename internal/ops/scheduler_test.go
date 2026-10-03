package ops

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
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
	synctest.Test(t, func(t *testing.T) {
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

		// Every goroutine has run as far as it can when Wait returns: the three
		// that were admitted are inside gatedWork and the rest are queued.
		// Stats().InFlight says the slots are reserved, which is not the same as
		// the work having begun; the work's own counter says it.
		synctest.Wait()
		if got := atomic.LoadInt64(&current); got != 3 {
			t.Fatalf("%d admitted workers have started, want 3 (MaxConcurrent)", got)
		}

		close(release)
		wg.Wait()

		if got := atomic.LoadInt64(&peak); got != 3 {
			t.Fatalf("peak in-flight = %d, want exactly 3 (MaxConcurrent)", got)
		}
	})
}

// TestSchedulerQueueFullRejectsImmediately proves a full queue refuses
// synchronously with a classified error rather than allocating a goroutine
// to wait.
func TestSchedulerQueueFullRejectsImmediately(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
	})
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
	synctest.Test(t, func(t *testing.T) {
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
	})
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
	synctest.Test(t, func(t *testing.T) {
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
	})
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

// When Close has returned, the work it waited for is no longer counted as in
// flight. The count is released before the wait group is, so a Close that
// returns as soon as the group is done has never seen a stale count. Repeated
// because the order of the two steps is only visible on some interleavings.
func TestSchedulerStatsAreZeroOnceCloseReturns(t *testing.T) {
	for i := 0; i < 300; i++ {
		s := NewScheduler(SchedulerLimits{})
		started := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = Submit(context.Background(), s, SubmitRequest{EstimatedTokens: 5}, func(run context.Context) (int, error) {
				close(started)
				<-run.Done()
				return 0, run.Err()
			})
		}()
		<-started
		if err := s.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if stats := s.Stats(); stats.InFlight != 0 || stats.InFlightTokens != 0 {
			t.Fatalf("iteration %d: Close returned while the request was still counted in flight: %+v", i, stats)
		}
		<-done
	}
}

// schedulerWaitGroup waits for wg, failing the test if it takes longer than
// the limit: a submission the scheduler dropped never returns, so the plain
// Wait would hang the whole run instead of failing it.
func schedulerWaitGroup(t *testing.T, wg *sync.WaitGroup, limit time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("submissions did not all return within %v -- one was dropped", limit)
	}
}

// TestSchedulerNoSubmissionIsSilentlyDropped submits work that will succeed,
// work that is refused for a full queue, and work whose caller cancels while it
// is queued, and proves each submission came back with exactly the outcome its
// situation calls for. The work is held at a gate, so which submission is in
// which situation is decided by the test, not by timing.
func TestSchedulerNoSubmissionIsSilentlyDropped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			running   = 2
			queuedOK  = 20
			cancelled = 20
			overflow  = 10
		)
		s := NewScheduler(SchedulerLimits{MaxConcurrent: running, MaxQueued: queuedOK + cancelled})
		defer s.Close(context.Background())

		release := make(chan struct{})
		var current, peak int64
		var succeeded, refused, canceled, other int64
		var wg sync.WaitGroup

		record := func(err error) {
			switch {
			case err == nil:
				atomic.AddInt64(&succeeded, 1)
			case mustOpErr(t, err).Kind == types.KindAdmissionRejected:
				atomic.AddInt64(&refused, 1)
			case mustOpErr(t, err).Kind == types.KindCanceled:
				atomic.AddInt64(&canceled, 1)
			default:
				atomic.AddInt64(&other, 1)
			}
		}
		submit := func(ctx context.Context) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := Submit(ctx, s, SubmitRequest{}, gatedWork(&current, &peak, release))
				record(err)
			}()
		}

		for i := 0; i < running; i++ {
			submit(context.Background())
		}
		waitForStat(t, s, func(st SchedulerStats) bool { return st.InFlight == running })

		cancels := make([]context.CancelFunc, 0, cancelled)
		for i := 0; i < cancelled; i++ {
			ctx, cancel := context.WithCancel(context.Background())
			cancels = append(cancels, cancel)
			submit(ctx)
		}
		for i := 0; i < queuedOK; i++ {
			submit(context.Background())
		}
		waitForStat(t, s, func(st SchedulerStats) bool { return st.Queued == queuedOK+cancelled })

		for i := 0; i < overflow; i++ {
			submit(context.Background())
		}
		// The overflow is refused at once, with the queue still full.
		for atomic.LoadInt64(&refused) < overflow {
			runtime.Gosched()
		}

		for _, cancel := range cancels {
			cancel()
		}
		for atomic.LoadInt64(&canceled) < cancelled {
			runtime.Gosched()
		}
		close(release)
		schedulerWaitGroup(t, &wg, 10*time.Second)

		if got := atomic.LoadInt64(&succeeded); got != running+queuedOK {
			t.Errorf("succeeded = %d, want %d", got, running+queuedOK)
		}
		if got := atomic.LoadInt64(&refused); got != overflow {
			t.Errorf("refused for a full queue = %d, want %d", got, overflow)
		}
		if got := atomic.LoadInt64(&canceled); got != cancelled {
			t.Errorf("cancelled while queued = %d, want %d", got, cancelled)
		}
		if got := atomic.LoadInt64(&other); got != 0 {
			t.Errorf("%d submissions came back with an unexpected error kind", got)
		}
		if st := s.Stats(); st.InFlight != 0 || st.Queued != 0 {
			t.Errorf("stats after everything returned = %+v", st)
		}
	})
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
	schedulerWaitGroup(t, &wg, 60*time.Second)

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
	synctest.Test(t, func(t *testing.T) {
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
	})
}

// TestSchedulerInFlightCostCeiling proves admission weighs estimated cost,
// not just a count of requests.
func TestSchedulerInFlightCostCeiling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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

		// With every goroutine settled, the scheduler holds as many as the ceiling
		// lets in and no fewer.
		synctest.Wait()
		stats := s.Stats()
		if stats.InFlight != 2 {
			t.Fatalf("in-flight = %d, want 2: a 2.5 cost ceiling at 1.0 per item admits two and not a third", stats.InFlight)
		}
		if stats.InFlightCost != 2.0 {
			t.Fatalf("in-flight cost = %v, want 2.0", stats.InFlightCost)
		}
		close(release)
		wg.Wait()
	})
}

// TestSchedulerFairnessSmallTenantNotStuckBehindLargeOne is SC-003's headline
// verification: a small tenant that arrives behind a large tenant's backlog is
// served in turn, not after the backlog.
//
// One slot, held at a gate, makes the order the work starts in the order the
// scheduler admitted it, and the whole backlog is queued before anything is let
// through. Nothing depends on timing: a scheduler that served the queue in
// arrival order would run all of the big tenant's work first, and the small
// tenant's last item would start at the end.
func TestSchedulerFairnessSmallTenantNotStuckBehindLargeOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const bigN = 200
		const smallN = 10

		s := NewScheduler(SchedulerLimits{MaxConcurrent: 1, MaxQueued: bigN + smallN + 1})
		defer s.Close(context.Background())

		gate := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			Submit(context.Background(), s, SubmitRequest{Tenant: "holder"}, func(ctx context.Context) (int, error) {
				<-gate
				return 0, nil
			})
		}()
		waitForStat(t, s, func(st SchedulerStats) bool { return st.InFlight == 1 })

		var mu sync.Mutex
		var order []string
		submit := func(tenant string) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				Submit(context.Background(), s, SubmitRequest{Tenant: tenant}, func(ctx context.Context) (int, error) {
					mu.Lock()
					order = append(order, tenant)
					mu.Unlock()
					return 1, nil
				})
			}()
		}
		for i := 0; i < bigN; i++ {
			submit("big")
		}
		waitForStat(t, s, func(st SchedulerStats) bool { return st.Queued == bigN })
		for i := 0; i < smallN; i++ {
			submit("small")
		}
		waitForStat(t, s, func(st SchedulerStats) bool { return st.Queued == bigN+smallN })

		close(gate)
		schedulerWaitGroup(t, &wg, 30*time.Second)

		lastSmall, smallSeen := -1, 0
		for i, tenant := range order {
			if tenant == "small" {
				lastSmall = i
				smallSeen++
			}
		}
		if smallSeen != smallN || len(order) != bigN+smallN {
			t.Fatalf("ran %d small and %d in all, want %d and %d", smallSeen, len(order), smallN, bigN+smallN)
		}
		// Alternating, the small tenant's last item is the twentieth to start; a
		// generous bound still sits far below the 210th that arrival order gives.
		if lastSmall > 3*smallN {
			t.Fatalf("the small tenant's last item started at position %d of %d: it waited behind the backlog", lastSmall+1, len(order))
		}
	})
}

// TestSchedulerPriorityOrdersAdmissionWithoutBypassingTenantLimit proves
// priority changes ordering among admittable work but never lets a high
// priority request past a locked per-tenant limit.
func TestSchedulerPriorityOrdersAdmissionWithoutBypassingTenantLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
	})
}

// TestSubmitRejectsNilSchedulerAndNilWork covers the two caller-mistake
// guard clauses.
func TestSubmitRejectsNilSchedulerAndNilWork(t *testing.T) {
	_, err := Submit[int](context.Background(), nil, SubmitRequest{}, func(ctx context.Context) (int, error) { return 0, nil })
	if err == nil {
		t.Fatal("expected an error for a nil scheduler")
	}
	if kind := types.KindOf(err); kind != types.KindInvalidRequest {
		t.Errorf("nil scheduler: kind = %v, want KindInvalidRequest (err = %v)", kind, err)
	}

	s := NewScheduler(SchedulerLimits{MaxConcurrent: 1})
	defer s.Close(context.Background())
	_, err = Submit[int](context.Background(), s, SubmitRequest{}, nil)
	if err == nil {
		t.Fatal("expected an error for nil work")
	}
	if kind := types.KindOf(err); kind != types.KindInvalidRequest {
		t.Errorf("nil work: kind = %v, want KindInvalidRequest (err = %v)", kind, err)
	}
}

// TestSubmitRejectsAlreadyCanceledContext proves a context canceled before
// Submit is even called is refused without ever queuing.
func TestSubmitRejectsAlreadyCanceledContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
	})
}

// waitForStat returns once every goroutine of the test's bubble has run as far
// as it can, and fails the test if the scheduler's stats are not then what pred
// says. The scheduler is driven by events and starts no timer, so what it holds
// when nothing can run any more is what it will hold until the test lets
// something go. Nothing is polled and nothing is timed.
func waitForStat(t *testing.T, s *Scheduler, pred func(SchedulerStats) bool) {
	t.Helper()
	synctest.Wait()
	if !pred(s.Stats()) {
		t.Fatalf("the stats are not what the test waited for, though every goroutine has settled: %+v", s.Stats())
	}
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
	synctest.Test(t, func(t *testing.T) {
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
	})
}

// Skipping past a blocked waiter is only correct for a bulkhead refusal. When
// the scheduler as a whole is full, nothing further down the queue can run
// either, and walking the queue to discover that would be wasted work that
// also risks admitting past a global ceiling.
func TestAFullSchedulerDoesNotAdmitPastItsGlobalCeiling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
	})
}

// A priority outside the three classes was queued where nothing looks for it:
// the request waited for ever, counted against MaxQueued, and was not answered
// even by Close.
func TestSchedulerRefusesAPriorityOutOfRange(t *testing.T) {
	for _, priority := range []SchedPriority{-1, 3, 5, 1 << 20, -1 << 20} {
		s := NewScheduler(SchedulerLimits{MaxConcurrent: 1, MaxQueued: 2})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ran := false
		_, err := Submit(ctx, s, SubmitRequest{Priority: priority}, func(context.Context) (int, error) {
			ran = true
			return 1, nil
		})
		stillLive := ctx.Err() == nil
		cancel()
		if err == nil {
			t.Fatalf("priority %d was accepted", priority)
		}
		if !stillLive {
			t.Fatalf("priority %d was queued until the context ended instead of being refused", priority)
		}
		if got := mustOpErr(t, err).Kind; got != types.KindInvalidRequest {
			t.Errorf("priority %d: kind = %v, want KindInvalidRequest", priority, got)
		}
		if ran {
			t.Errorf("priority %d: work ran", priority)
		}
		if st := s.Stats(); st.Queued != 0 {
			t.Errorf("priority %d: %d queued after the refusal", priority, st.Queued)
		}
		s.Close(context.Background())
	}

	// The three real classes are accepted.
	s := NewScheduler(SchedulerLimits{})
	defer s.Close(context.Background())
	for _, priority := range []SchedPriority{SchedPriorityLow, SchedPriorityNormal, SchedPriorityHigh} {
		if _, err := Submit(context.Background(), s, SubmitRequest{Priority: priority}, func(context.Context) (int, error) { return 1, nil }); err != nil {
			t.Errorf("priority %d refused: %v", priority, err)
		}
	}
}

// The derived context's cancel was never called when the work returned.
func TestSchedulerCancelsTheRunContextWhenTheWorkReturns(t *testing.T) {
	s := NewScheduler(SchedulerLimits{})
	defer s.Close(context.Background())

	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()

	var seen context.Context
	if _, err := Submit(parent, s, SubmitRequest{}, func(ctx context.Context) (int, error) {
		seen = ctx
		return 1, nil
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if seen.Err() == nil {
		t.Fatal("the run context was left live after the work returned: it stays in its parent's children until the parent ends")
	}

	// Also when the work fails and when it panics.
	_, _ = Submit(parent, s, SubmitRequest{}, func(ctx context.Context) (int, error) {
		seen = ctx
		return 0, errors.New("boom")
	})
	if seen.Err() == nil {
		t.Error("the run context was left live after the work failed")
	}
	func() {
		defer func() { _ = recover() }()
		_, _ = Submit(parent, s, SubmitRequest{}, func(ctx context.Context) (int, error) {
			seen = ctx
			panic("boom")
		})
	}()
	if seen.Err() == nil {
		t.Error("the run context was left live after the work panicked")
	}
	if st := s.Stats(); st.InFlight != 0 {
		t.Errorf("InFlight = %d after the work returned", st.InFlight)
	}
}

// A second Close returned nil at once, even when the first one's grace period
// had elapsed and work was still running.
func TestSchedulerSecondCloseWaitsForTheDrain(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 1})

	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		// Ignores its context, as a caller's misbehaving work might.
		Submit(context.Background(), s, SubmitRequest{}, func(context.Context) (int, error) {
			close(started)
			<-release
			return 1, nil
		})
	}()
	<-started

	short, cancelShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShort()
	if err := s.Close(short); err == nil {
		t.Fatal("the first Close reported success with work still in flight")
	}

	// A retry with a short grace period still reports that work is running.
	short2, cancelShort2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShort2()
	if err := s.Close(short2); err == nil {
		t.Fatal("a second Close reported success with work still in flight")
	} else if mustOpErr(t, err).Kind != types.KindShutdown {
		t.Errorf("kind = %v, want KindShutdown", mustOpErr(t, err).Kind)
	}

	// A retry with room waits for the drain.
	third := make(chan error, 1)
	go func() { third <- s.Close(context.Background()) }()
	select {
	case err := <-third:
		t.Fatalf("a Close returned (%v) while work was still running", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-third:
		if err != nil {
			t.Fatalf("the Close that waited returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the Close that waited never returned after the work finished")
	}
	<-finished
	if err := s.Close(context.Background()); err != nil {
		t.Errorf("Close after the drain = %v", err)
	}
	if st := s.Stats(); st.InFlight != 0 {
		t.Errorf("InFlight = %d after Close returned", st.InFlight)
	}
}

// Deadline was checked only when the request arrived, so one that waited behind
// slow work was admitted and run after the time by which it should have started.
func TestSchedulerRefusesAQueuedRequestWhoseDeadlinePassed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clk := newTestClock(time.Unix(1000, 0))
		s := newSchedulerWithClock(SchedulerLimits{MaxConcurrent: 1, MaxQueued: 5}, clk)
		defer s.Close(context.Background())

		release := make(chan struct{})
		holderDone := make(chan struct{})
		go func() {
			defer close(holderDone)
			Submit(context.Background(), s, SubmitRequest{}, func(context.Context) (int, error) {
				<-release
				return 1, nil
			})
		}()
		waitForStat(t, s, func(st SchedulerStats) bool { return st.InFlight == 1 })

		var ranLate atomic.Bool
		lateResult := make(chan error, 1)
		go func() {
			_, err := Submit(context.Background(), s, SubmitRequest{Deadline: clk.Now().Add(10 * time.Second)},
				func(context.Context) (int, error) {
					ranLate.Store(true)
					return 1, nil
				})
			lateResult <- err
		}()
		var onTimeRan atomic.Bool
		onTimeResult := make(chan error, 1)
		go func() {
			_, err := Submit(context.Background(), s, SubmitRequest{Deadline: clk.Now().Add(time.Hour)},
				func(context.Context) (int, error) {
					onTimeRan.Store(true)
					return 1, nil
				})
			onTimeResult <- err
		}()
		waitForStat(t, s, func(st SchedulerStats) bool { return st.Queued == 2 })

		clk.mu.Lock()
		clk.now = clk.now.Add(30 * time.Second)
		clk.mu.Unlock()
		close(release)

		select {
		case err := <-lateResult:
			if err == nil {
				t.Fatal("a request whose deadline had passed was admitted and run")
			}
			if got := mustOpErr(t, err).Kind; got != types.KindAdmissionRejected {
				t.Errorf("kind = %v, want KindAdmissionRejected", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the expired request was never answered")
		}
		if ranLate.Load() {
			t.Error("the expired request's work ran")
		}
		select {
		case err := <-onTimeResult:
			if err != nil || !onTimeRan.Load() {
				t.Errorf("a request still inside its deadline was refused: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the on-time request was never answered")
		}
		<-holderDone
	})
}

// A tenant with nothing queued left the rotation and the queue map; both held
// every tenant the scheduler had ever seen.
func TestSchedulerForgetsTenantsThatHaveNothingQueued(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := NewScheduler(SchedulerLimits{MaxConcurrent: 1, MaxQueued: 10})
		defer s.Close(context.Background())

		for i := 0; i < 50; i++ {
			tenant := "tenant-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			if _, err := Submit(context.Background(), s, SubmitRequest{Tenant: tenant}, func(context.Context) (int, error) { return 1, nil }); err != nil {
				t.Fatalf("Submit: %v", err)
			}
		}
		s.mu.Lock()
		order, queues := len(s.tenantOrder), len(s.queues)
		s.mu.Unlock()
		if order != 0 || queues != 0 {
			t.Errorf("after 50 tenants finished, tenantOrder has %d and queues has %d entries", order, queues)
		}

		// A tenant's limits are tracked apart from its queue and still hold.
		limited := NewScheduler(SchedulerLimits{PerTenantConcurrency: 1})
		defer limited.Close(context.Background())
		release := make(chan struct{})
		var wg sync.WaitGroup
		var current, peak int64
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				Submit(context.Background(), limited, SubmitRequest{Tenant: "one"}, gatedWork(&current, &peak, release))
			}()
		}
		waitForStat(t, limited, func(st SchedulerStats) bool { return st.InFlight == 1 && st.Queued == 3 })
		close(release)
		schedulerWaitGroup(t, &wg, 10*time.Second)
		if atomic.LoadInt64(&peak) != 1 {
			t.Errorf("peak concurrency for one tenant = %d, want 1", peak)
		}
	})
}

// inFlightCost is a float that is added to and taken from: after reservations
// at 0.1 and 0.2 an idle scheduler held a few ulps above zero, and a request
// estimated at exactly the ceiling could then find no room while nothing ran.
func TestSchedulerIdleCostIsExactlyZero(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxInFlightCost: 0.3})
	defer s.Close(context.Background())

	a, b := SubmitRequest{EstimatedCost: 0.1}, SubmitRequest{EstimatedCost: 0.2}
	s.mu.Lock()
	s.reserveLocked(a)
	s.reserveLocked(b)
	s.unreserveLocked(a)
	s.unreserveLocked(b)
	cost := s.inFlightCost
	s.mu.Unlock()
	if cost != 0 {
		t.Errorf("idle inFlightCost = %v, want exactly 0", cost)
	}
}
