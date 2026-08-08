package ops

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// RC-005: load and chaos coverage for Scheduler, over and above
// scheduler_test.go's deterministic proofs. Those tests each pin one
// mechanism with a controlled interleaving; these throw a much larger and
// randomized load at the same mechanisms so a regression that only shows up
// under contention -- like the head-of-line defect documented on
// nextAdmittableAtPriorityLocked, which "showed up as an intermittent test
// failure" -- has many more chances to reproduce. Every test here bounds its
// own wait with a timeout: a scheduler that hangs must fail this test, not
// stall the CI run that includes it.
//
// These are fast enough (bounded work, no real I/O, generous but rarely-hit
// timeouts) to run in the ordinary suite; no build tag or env gate. Run with
// -shuffle=on and -cpu=1 in addition to the default settings -- both have
// caught real defects in this package before (see scheduler.go's doc comment
// and AGENTS.md's note on this repository's -shuffle=on history).

// chaosSettledGoroutines waits for the runtime to quiesce and reports the
// count, mirroring slo_test.go's settledGoroutines (package schemaflux_test,
// unexported and therefore unreachable from here) -- this package needs its
// own copy rather than a cross-package call it cannot make.
func chaosSettledGoroutines() int {
	previous := -1
	for i := 0; i < 40; i++ {
		runtime.GC()
		current := runtime.NumGoroutine()
		if current == previous {
			return current
		}
		previous = current
		time.Sleep(10 * time.Millisecond)
	}
	return runtime.NumGoroutine()
}

// schedOutcomeClass buckets a Submit result for accounting. "other" is never
// expected -- it exists so an unrecognised error kind fails the test loudly
// instead of being silently lumped in with an expected bucket.
func schedOutcomeClass(err error) string {
	if err == nil {
		return "succeeded"
	}
	var opErr *types.OperationError
	if errors.As(err, &opErr) {
		switch opErr.Kind {
		case types.KindCanceled, types.KindTimeout:
			return "canceled"
		case types.KindAdmissionRejected, types.KindShutdown:
			return "rejected"
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	return "other"
}

// runWithDeadline fails t if fn does not complete within d, so a hang becomes
// a failed test rather than a stalled build -- the same discipline
// fanout.go's doc comment calls out FanOutGate's real deadlock for missing.
func runWithDeadline(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("did not complete within %s -- looks hung, not slow", d)
	}
}

// TestChaosSchedulerCancellationStormAccountsForEverySubmission fires a large
// number of submissions at a tightly bounded scheduler, cancelling many of
// them at random times (before admission, while queued, while running), and
// proves every submission lands in exactly one of succeeded/canceled/rejected
// -- nothing vanishes -- and that the goroutines the storm used are gone
// afterward.
func TestChaosSchedulerCancellationStormAccountsForEverySubmission(t *testing.T) {
	before := chaosSettledGoroutines()

	s := NewScheduler(SchedulerLimits{MaxConcurrent: 4, MaxQueued: 25})

	const total = 3000
	var succeeded, canceled, rejected, other int64

	runWithDeadline(t, 30*time.Second, func() {
		var wg sync.WaitGroup
		for i := 0; i < total; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()

				ctx := context.Background()
				var cancel context.CancelFunc
				switch i % 4 {
				case 0:
					// Cancelled almost immediately -- likely still queued or
					// not yet admitted.
					ctx, cancel = context.WithCancel(ctx)
					go func() {
						time.Sleep(time.Duration(rand.Intn(2)) * time.Millisecond)
						cancel()
					}()
				case 1:
					// A deadline that may expire before, during, or after the
					// work runs, depending on how loaded the run is.
					ctx, cancel = context.WithTimeout(ctx, time.Duration(rand.Intn(4))*time.Millisecond)
				default:
					// Never cancelled: this is the arm that proves the storm
					// still lets ordinary work through.
				}
				if cancel != nil {
					defer cancel()
				}

				_, err := Submit(ctx, s, SubmitRequest{Tenant: fmt.Sprintf("t%d", i%9), Priority: SchedPriority(i % 3)},
					func(runCtx context.Context) (int, error) {
						select {
						case <-time.After(time.Millisecond):
							return 1, nil
						case <-runCtx.Done():
							return 0, runCtx.Err()
						}
					})

				switch schedOutcomeClass(err) {
				case "succeeded":
					atomic.AddInt64(&succeeded, 1)
				case "canceled":
					atomic.AddInt64(&canceled, 1)
				case "rejected":
					atomic.AddInt64(&rejected, 1)
				default:
					atomic.AddInt64(&other, 1)
					t.Errorf("submission %d: unclassified error: %v", i, err)
				}
			}(i)
		}
		wg.Wait()
	})

	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	sum := succeeded + canceled + rejected + other
	if sum != total {
		t.Fatalf("succeeded(%d)+canceled(%d)+rejected(%d)+other(%d) = %d, want %d -- some submission vanished",
			succeeded, canceled, rejected, other, sum, total)
	}
	if other != 0 {
		t.Fatalf("%d submissions returned an unrecognised error kind", other)
	}
	if succeeded == 0 {
		t.Fatal("nothing succeeded -- the storm did not exercise the accept path at all")
	}
	if canceled == 0 {
		t.Fatal("nothing was cancelled -- the storm did not exercise cancellation at all")
	}

	after := chaosSettledGoroutines()
	if after > before+5 {
		t.Errorf("goroutines went from %d to %d after a %d-submission cancellation storm", before, after, total)
	}
}

// TestChaosSchedulerMixedTenantsAndPrioritiesCompleteUnderLoad submits many
// tenants across every priority class, all at once and in a randomized
// arrival order (each goroutine sleeps a random sub-millisecond jitter before
// submitting), and proves every combination finishes completely rather than
// some being starved out by the fairness rotation or the priority ordering
// under real contention -- SC-003's guarantee, stress-tested rather than
// pinned to one deterministic interleaving.
func TestChaosSchedulerMixedTenantsAndPrioritiesCompleteUnderLoad(t *testing.T) {
	const tenants = 24
	const itemsPerTenantPriority = 10
	priorities := []SchedPriority{SchedPriorityLow, SchedPriorityNormal, SchedPriorityHigh}
	total := tenants * len(priorities) * itemsPerTenantPriority

	s := NewScheduler(SchedulerLimits{MaxConcurrent: 6, MaxQueued: total})
	defer s.Close(context.Background())

	// completed[tenant][priorityIndex] counts completions, so a starved
	// combination shows up as a short count rather than merely a slow test.
	var mu sync.Mutex
	completed := make(map[string][3]int)

	runWithDeadline(t, 30*time.Second, func() {
		var wg sync.WaitGroup
		for tn := 0; tn < tenants; tn++ {
			tenant := fmt.Sprintf("tenant-%02d", tn)
			for pi, p := range priorities {
				for i := 0; i < itemsPerTenantPriority; i++ {
					wg.Add(1)
					go func(tenant string, p SchedPriority, pi int) {
						defer wg.Done()
						time.Sleep(time.Duration(rand.Intn(300)) * time.Microsecond)
						_, err := Submit(context.Background(), s, SubmitRequest{Tenant: tenant, Priority: p},
							func(ctx context.Context) (int, error) { return 1, nil })
						if err != nil {
							t.Errorf("tenant %s priority %d: unexpected error: %v", tenant, p, err)
							return
						}
						mu.Lock()
						row := completed[tenant]
						row[pi]++
						completed[tenant] = row
						mu.Unlock()
					}(tenant, p, pi)
				}
			}
		}
		wg.Wait()
	})

	for tn := 0; tn < tenants; tn++ {
		tenant := fmt.Sprintf("tenant-%02d", tn)
		row := completed[tenant]
		for pi := range priorities {
			if row[pi] != itemsPerTenantPriority {
				t.Errorf("tenant %s priority %d completed %d of %d -- looks starved, not merely slow",
					tenant, priorities[pi], row[pi], itemsPerTenantPriority)
			}
		}
	}
}

// TestChaosSchedulerManyBusyProvidersNeverStallManyIdleOnes scales
// scheduler_test.go's TestABusyProviderDoesNotStallAnIdleOne (the exact
// regression: the dispatcher used to inspect only each queue's head, so a
// provider-saturated request queued ahead of an idle-provider one stalled the
// idle provider too) up to several saturated providers with deep backlogs and
// several idle ones arriving after the backlogs are confirmed queued. Any
// reintroduction of the head-of-line defect times out here well before the
// deadline this test allows.
func TestChaosSchedulerManyBusyProvidersNeverStallManyIdleOnes(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 100, MaxQueued: 1000, PerProviderConcurrency: 1})
	defer s.Close(context.Background())

	const busyProviders = 4
	const backlogPerProvider = 15
	const idleProviders = 6

	release := make(chan struct{})
	var wg sync.WaitGroup

	for p := 0; p < busyProviders; p++ {
		provider := fmt.Sprintf("busy-%d", p)
		for i := 0; i < backlogPerProvider; i++ {
			wg.Add(1)
			go func(provider string) {
				defer wg.Done()
				Submit(context.Background(), s, SubmitRequest{Provider: provider, Tenant: provider},
					func(ctx context.Context) (int, error) {
						select {
						case <-release:
						case <-ctx.Done():
						}
						return 1, ctx.Err()
					})
			}(provider)
		}
	}

	// One admitted per busy provider (PerProviderConcurrency: 1), the rest
	// queued behind it -- the exact state the head-of-line defect needs.
	waitForStat(t, s, func(st SchedulerStats) bool {
		return st.InFlight == busyProviders && st.Queued == busyProviders*(backlogPerProvider-1)
	})

	var idleStarted int64
	for p := 0; p < idleProviders; p++ {
		provider := fmt.Sprintf("idle-%d", p)
		wg.Add(1)
		go func(provider string) {
			defer wg.Done()
			Submit(context.Background(), s, SubmitRequest{Provider: provider, Tenant: provider},
				func(ctx context.Context) (int, error) {
					atomic.AddInt64(&idleStarted, 1)
					select {
					case <-release:
					case <-ctx.Done():
					}
					return 1, ctx.Err()
				})
		}(provider)
	}

	// Before the fix this times out with InFlight stuck at busyProviders.
	waitForStat(t, s, func(st SchedulerStats) bool { return st.InFlight == busyProviders+idleProviders })

	// Stats().InFlight flips the moment the scheduler reserves a slot under
	// its lock, which can be before the admitted goroutine has actually been
	// scheduled onto a thread and incremented idleStarted -- the same timing
	// gap gatedWork's own doc comment (scheduler_test.go) warns about. Poll
	// idleStarted directly rather than asserting on it immediately after
	// waitForStat returns, or this assertion can read a stale value on a
	// loaded machine and fail on nothing but scheduler noise.
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt64(&idleStarted) < idleProviders && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if got := atomic.LoadInt64(&idleStarted); got != idleProviders {
		t.Errorf("idle providers actually started = %d, want %d", got, idleProviders)
	}

	close(release)
	wg.Wait()
}

// TestChaosSchedulerCloseDuringStormAnswersEverySubmission fires a storm of
// submissions with staggered, randomized work durations and closes the
// scheduler partway through, proving every outstanding Submit call -- queued,
// running, or not yet even admitted -- receives an answer promptly rather
// than a goroutine parked forever on a channel nobody signals again (SC-005/
// SC-006's contract under contention, not just the single-request case
// scheduler_test.go already pins).
func TestChaosSchedulerCloseDuringStormAnswersEverySubmission(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 3, MaxQueued: 12})

	const total = 600
	results := make(chan string, total)

	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			time.Sleep(time.Duration(rand.Intn(2)) * time.Millisecond)
			_, err := Submit(context.Background(), s, SubmitRequest{Tenant: fmt.Sprintf("t%d", i%5)},
				func(ctx context.Context) (int, error) {
					select {
					case <-time.After(time.Duration(rand.Intn(3)) * time.Millisecond):
						return 1, nil
					case <-ctx.Done():
						return 0, ctx.Err()
					}
				})
			results <- schedOutcomeClass(err)
		}(i)
	}

	go func() {
		time.Sleep(3 * time.Millisecond)
		s.Close(context.Background())
	}()

	runWithDeadline(t, 30*time.Second, func() { wg.Wait() })
	close(results)

	counts := map[string]int{}
	for class := range results {
		if class == "other" {
			t.Error("a submission returned an unrecognised error kind during a Close storm")
		}
		counts[class]++
	}
	sum := counts["succeeded"] + counts["canceled"] + counts["rejected"]
	if sum != total {
		t.Fatalf("collected %d outcomes for %d submissions -- something never answered", sum, total)
	}
}
