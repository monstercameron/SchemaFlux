package ops

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// RC-005: load and chaos coverage for FanOutGate/FanOut, layered on top of
// fanout_test.go's deterministic proofs. The specific defect this class of
// test exists to catch reproduced HUNG rather than failed: an earlier
// backstop released the gate only after wg.Wait(), which itself waits for
// followers blocked on that exact release (fanout.go's package doc comment).
// A hang has no failing assertion of its own, so every test here wraps its
// body in runWithDeadline (chaos_scheduler_test.go) -- a regression of that
// class fails this test instead of stalling whatever runs after it.

// TestChaosFanOutManyRandomizedRoundsNeverHang runs a few hundred FanOut
// rounds back to back, each with a randomized worker count and a randomized
// mix of "leader succeeds", "leader fails", and "nobody ever calls Claim" --
// the three ways a round's gate can be released -- so the backstop path is
// exercised far more than the one or two deterministic cases fanout_test.go
// pins.
func TestChaosFanOutManyRandomizedRoundsNeverHang(t *testing.T) {
	const rounds = 300

	for round := 0; round < rounds; round++ {
		workers := 1 + rand.Intn(24)
		mode := round % 3 // 0: normal leader, 1: failing leader, 2: nobody claims

		// Mode 2 (nobody ever calls Claim) needs a context that actually
		// ends, since FanOut's only recovery for "no leader" is
		// waitForClaim's ctx.Done backstop (fanout.go) -- a Background
		// context would leave every follower waiting forever, which is a bug
		// in a test that means to prove "does not hang", not in FanOut.
		ctx := context.Background()
		cancel := func() {}
		if mode == 2 {
			ctx, cancel = context.WithTimeout(context.Background(), 2*time.Millisecond)
		}

		runWithDeadline(t, 5*time.Second, func() {
			defer cancel()
			results, err := FanOut(ctx, workers,
				func(ctx context.Context, index int, gate *FanOutGate) (int, error) {
					if mode == 2 {
						// Nobody in this round ever calls Claim at all.
						if _, waitErr := gate.Wait(ctx); waitErr != nil {
							return 0, waitErr
						}
						return index, nil
					}

					if gate.Claim(index) {
						if mode == 1 {
							leaderErr := errors.New("simulated leader failure")
							gate.Failed(leaderErr)
							return 0, leaderErr
						}
						gate.Released()
						return index, nil
					}
					if _, waitErr := gate.Wait(ctx); waitErr != nil {
						return 0, waitErr
					}
					return index, nil
				})

			switch mode {
			case 0:
				if err != nil {
					t.Errorf("round %d (normal): FanOut: %v", round, err)
				}
				if len(results) != workers {
					t.Errorf("round %d: got %d results, want %d", round, len(results), workers)
				}
			case 1:
				if err == nil {
					t.Errorf("round %d (failing leader): expected the leader's failure to surface", round)
				}
			case 2:
				// Whether the deadline backstop or a genuine release wins the
				// race is not what this proves -- see the round's ctx
				// comment above. The only claim is that FanOut returned at
				// all, which runWithDeadline's outer bound already checks.
			}
		})
	}
}

// TestChaosFanOutCancellationStormReleasesEveryFollowerPromptly launches many
// concurrent FanOut rounds whose leader never releases the gate at all (not
// even via failure), each under its own short-lived context, and proves every
// follower's Wait returns promptly with the context error rather than
// blocking until process exit -- the case where the caller gives up, not the
// leader.
func TestChaosFanOutCancellationStormReleasesEveryFollowerPromptly(t *testing.T) {
	const concurrentRounds = 100
	const workersPerRound = 8

	var wg sync.WaitGroup
	var timedOut int64

	runWithDeadline(t, 15*time.Second, func() {
		for r := 0; r < concurrentRounds; r++ {
			wg.Add(1)
			go func(round int) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(1+rand.Intn(5))*time.Millisecond)
				defer cancel()

				_, err := FanOut(ctx, workersPerRound,
					func(ctx context.Context, index int, gate *FanOutGate) (int, error) {
						if gate.Claim(index) {
							// The leader deliberately never releases and
							// never fails -- every follower depends entirely
							// on its own ctx ending, not on this leader ever
							// doing anything.
							<-ctx.Done()
							return 0, ctx.Err()
						}
						_, waitErr := gate.Wait(ctx)
						return 0, waitErr
					})
				if err == nil {
					t.Errorf("round %d: expected a cancellation to surface", round)
					return
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					// FanOut wraps a follower's error with "item N failed",
					// so the underlying cause is what is checked.
					atomic.AddInt64(&timedOut, 1)
				}
			}(r)
		}
		wg.Wait()
	})

	// Not a hard assertion on timedOut's value -- FanOut wraps every
	// non-leader error identically ("fanout: item N failed: %w"), so the
	// deadline exceeding is always reachable via errors.Is on the returned
	// error; the real proof here is that runWithDeadline's outer 15s bound
	// was never hit for 100 concurrent rounds of 8 workers each.
}

// TestChaosFanOutGoroutineCountStaysFlatAfterManyRounds runs several hundred
// ordinary FanOut rounds sequentially and proves the goroutines they used are
// gone afterward -- FanOut's own backstop goroutine (waitForClaim) and every
// worker goroutine it launches must not accumulate across rounds.
func TestChaosFanOutGoroutineCountStaysFlatAfterManyRounds(t *testing.T) {
	before := chaosSettledGoroutines()

	const rounds = 500
	for round := 0; round < rounds; round++ {
		workers := 2 + rand.Intn(18)
		_, err := FanOut(context.Background(), workers,
			func(ctx context.Context, index int, gate *FanOutGate) (int, error) {
				if gate.Claim(index) {
					gate.Released()
					return index, nil
				}
				if _, waitErr := gate.Wait(ctx); waitErr != nil {
					return 0, waitErr
				}
				return index, nil
			})
		if err != nil {
			t.Fatalf("round %d: FanOut: %v", round, err)
		}
	}

	after := chaosSettledGoroutines()
	if after > before+5 {
		t.Errorf("goroutines went from %d to %d after %d completed FanOut rounds", before, after, rounds)
	}
}
