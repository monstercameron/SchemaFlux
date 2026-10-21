package ops

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestSchedulerCloseDuringAdmissionDoesNotPanic drives the interleaving that
// panicked the runtime with "WaitGroup is reused before previous Wait has
// returned".
//
// Admission and registration are two separate critical sections, and Close can
// run entirely in the gap between them. With several requests in flight the
// reachable ordering is: A registers (counter 1); Close sets closed and starts
// waiting; A finishes and the counter drops to 0, so that Wait is unwinding;
// B -- admitted before Close and holding nothing -- calls wg.Add(1) at exactly
// that moment.
//
// Unlike the earlier scheduler Close test in this package, this one DOES
// reproduce the defect it guards, and the round count is measured rather than
// guessed. Against the code with registerInFlight's refusal removed: 40 rounds
// caught nothing, 400 rounds caught it in 1 run out of 10, 4000 rounds caught
// it in 10 out of 10. Hence 4000. With the fix in place the whole test takes
// about a quarter of a second, so the suite does not pay for the certainty.
func TestSchedulerCloseDuringAdmissionDoesNotPanic(t *testing.T) {
	for round := 0; round < 4000; round++ {
		scheduler := NewScheduler(SchedulerLimits{
			MaxConcurrent: 4,
			MaxQueued:     64,
		})

		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// The error is expected and uninteresting: a request racing a
				// Close is allowed to be refused, at admission or afterwards.
				// What must not happen is a panic.
				_, _ = Submit(context.Background(), scheduler, SubmitRequest{
					Tenant:   "t",
					Priority: SchedPriorityNormal,
				}, func(context.Context) (int, error) {
					return 1, nil
				})
			}()
		}

		// Close while those are mid-flight rather than after they settle: the
		// window this guards is between admission and registration.
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = scheduler.Close(closeCtx)
		cancel()

		wg.Wait()
	}
}
