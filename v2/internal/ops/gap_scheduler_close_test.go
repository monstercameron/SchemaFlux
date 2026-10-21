package ops

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Close must not race a request that is being admitted.
//
// The panic this pins is "sync: WaitGroup is reused before previous Wait has
// returned", and it was reachable because wg.Add(1) sat in Submit with no lock
// held, after admission. That allowed: a request passes admission; Close sets
// closed and starts `go s.wg.Wait()`; the counter is still zero so Wait returns;
// the request then calls wg.Add(1) and the runtime panics.
//
// Close is the worst place for a crash — it runs on the way out, so the panic
// lands during shutdown where it reads as something else entirely.
//
// Be precise about what this test does and does not establish. It drives many
// submissions against a concurrent Close, which is the SHAPE that produced the
// panic under load — but it does NOT reproduce it: run against the old
// placement it passed too, over 6 rounds of 25. The window between admission
// and the Add is small enough that hitting it needs load this test does not
// create, and widening it would mean editing production code to make a test
// fail.
//
// So the fix rests on the lock argument rather than on a failing test: holding
// s.mu across the Add means Close cannot begin waiting between a request being
// admitted and being counted. That is the standard fix for this WaitGroup
// misuse, and it is sound on inspection — but this test is a smoke check on the
// concurrent shape, not proof the bug is gone. Said plainly because a test
// described as catching something it never caught is worse than no test.
//
// It carries its own deadline so a regression that deadlocks fails rather than
// hanging CI.
func TestClosingWhileRequestsAreBeingAdmittedDoesNotPanic(t *testing.T) {
	for round := 0; round < 25; round++ {
		func() {
			s := NewScheduler(SchedulerLimits{MaxConcurrent: 8, MaxQueued: 512})

			var wg sync.WaitGroup
			start := make(chan struct{})

			// Submitters pile in the instant `start` closes, so many of them are
			// mid-admission when Close arrives.
			const submitters = 40
			for i := 0; i < submitters; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					// Every outcome is acceptable here: admitted and run,
					// refused because the scheduler closed, or cancelled. What
					// is not acceptable is a panic, which no error return can
					// express.
					_, _ = Submit(context.Background(), s, SubmitRequest{Tenant: "t"},
						func(ctx context.Context) (int, error) { return 1, nil })
				}()
			}

			closed := make(chan struct{})
			go func() {
				defer close(closed)
				<-start
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = s.Close(ctx)
			}()

			close(start)

			done := make(chan struct{})
			go func() { wg.Wait(); <-closed; close(done) }()

			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("Close and the submitters did not settle within 20s; a regression here deadlocks rather than panicking")
			}
		}()
	}
}

// Closing twice must not panic either. The second Close finds the counter
// already drained, which is the same reuse hazard from the other direction.
func TestClosingTwiceIsSafe(t *testing.T) {
	s := NewScheduler(SchedulerLimits{MaxConcurrent: 2, MaxQueued: 8})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.Close(ctx); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(ctx); err != nil {
		t.Errorf("second Close returned %v; closing an already-closed scheduler should be a no-op, not an error", err)
	}
}
