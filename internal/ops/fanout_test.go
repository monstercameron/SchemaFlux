package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// CA-005. The property under test is ordering, not speed: exactly one worker
// runs ahead, and no follower reaches the provider until that one has begun
// producing. Every test here observes that through recorded events rather than
// through timing, because a timing-based version of this test passes on a
// fast machine and fails on a loaded one for reasons that have nothing to do
// with the code.

// fanOutRecorder records what happened in order, under a lock, so the sequence
// can be asserted rather than inferred.
type fanOutRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *fanOutRecorder) record(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fmt.Sprintf(format, args...))
}

func (r *fanOutRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func TestExactlyOneWorkerLeads(t *testing.T) {
	gate := NewFanOutGate()

	var leaders int
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			if gate.Claim(worker) {
				mu.Lock()
				leaders++
				mu.Unlock()
				gate.Released()
			}
		}(i)
	}
	wg.Wait()

	if leaders != 1 {
		t.Errorf("%d workers were told they lead; exactly one may be", leaders)
	}
}

// The defect this exists to prevent: every worker arrives before any of them
// has written the cache, so all of them miss and the prefix is written N
// times instead of once.
func TestNoFollowerCallsUntilTheLeaderHasProduced(t *testing.T) {
	recorder := &fanOutRecorder{}
	const workers = 5

	_, err := FanOut(context.Background(), workers,
		func(ctx context.Context, index int, gate *FanOutGate) (string, error) {
			if gate.Claim(index) {
				recorder.record("leader-call")
				recorder.record("leader-first-token")
				gate.Released()
				return "leader", nil
			}

			if _, err := gate.Wait(ctx); err != nil {
				return "", err
			}
			recorder.record("follower-call")
			return "follower", nil
		})
	if err != nil {
		t.Fatalf("FanOut: %v", err)
	}

	events := recorder.all()
	if len(events) != workers+1 {
		t.Fatalf("events = %v", events)
	}
	if events[0] != "leader-call" || events[1] != "leader-first-token" {
		t.Fatalf("the leader did not go first: %v", events)
	}
	for _, event := range events[2:] {
		if event != "follower-call" {
			t.Errorf("a follower call appeared before the leader produced: %v", events)
		}
	}
}

func TestResultsComeBackInInputOrder(t *testing.T) {
	results, err := FanOut(context.Background(), 6,
		func(ctx context.Context, index int, gate *FanOutGate) (int, error) {
			if gate.Claim(index) {
				gate.Released()
			} else if _, err := gate.Wait(ctx); err != nil {
				return 0, err
			}
			// Deliberately finish in a different order than they started.
			return index * 10, nil
		})
	if err != nil {
		t.Fatalf("FanOut: %v", err)
	}

	for i, got := range results {
		if got != i*10 {
			t.Errorf("results[%d] = %d, want %d; completion order leaked into the result order", i, got, i*10)
		}
	}
}

// One provider error must not become N stalled workers.
func TestAFailedLeaderStillReleasesTheFollowers(t *testing.T) {
	leaderFailure := errors.New("provider refused the leader")
	recorder := &fanOutRecorder{}

	_, err := FanOut(context.Background(), 4,
		func(ctx context.Context, index int, gate *FanOutGate) (string, error) {
			if gate.Claim(index) {
				gate.Failed(leaderFailure)
				return "", leaderFailure
			}

			leadErr, waitErr := gate.Wait(ctx)
			if waitErr != nil {
				return "", waitErr
			}
			if leadErr == nil {
				recorder.record("released-with-no-leader-error")
			} else {
				recorder.record("released-knowing-the-leader-failed")
			}
			return "follower", nil
		})

	if err == nil {
		t.Fatal("the leader's failure did not surface")
	}
	if !errors.Is(err, leaderFailure) {
		t.Errorf("err = %v, want the leader's failure wrapped", err)
	}

	events := recorder.all()
	if len(events) != 3 {
		t.Fatalf("%d followers ran; three should have been released", len(events))
	}
	for _, event := range events {
		if event != "released-knowing-the-leader-failed" {
			t.Errorf("a follower was released without being told the leader failed: %v", events)
		}
	}
}

// A follower is not itself failed just because the leader was: it is perfectly
// capable of making its own uncached call.
func TestAFollowerIsNotFailedByTheLeadersFailure(t *testing.T) {
	gate := NewFanOutGate()
	leaderFailure := errors.New("nope")

	go func() {
		gate.Claim(0)
		gate.Failed(leaderFailure)
	}()

	leadErr, err := gate.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait returned the leader's failure as the follower's own: %v", err)
	}
	if !errors.Is(leadErr, leaderFailure) {
		t.Errorf("leadErr = %v, want the leader's failure as information", leadErr)
	}
}

func TestWaitHonoursACancelledContext(t *testing.T) {
	gate := NewFanOutGate()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := gate.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// A run function that forgets to release must not strand the others forever.
// FanOut releases once everything has finished, which cannot strand anyone
// because by then nobody is waiting.
func TestAForgetfulLeaderDoesNotStrandTheFanOut(t *testing.T) {
	done := make(chan struct{})

	go func() {
		defer close(done)
		_, _ = FanOut(context.Background(), 3,
			func(ctx context.Context, index int, gate *FanOutGate) (string, error) {
				if gate.Claim(index) {
					// Deliberately never releases.
					return "leader", nil
				}
				if _, err := gate.Wait(ctx); err != nil {
					return "", err
				}
				return "follower", nil
			})
	}()

	<-done // hangs forever if the backstop release is missing
}

func TestReleasingTwiceIsHarmless(t *testing.T) {
	gate := NewFanOutGate()
	gate.Claim(0)
	gate.Released()
	gate.Released() // a second call must not panic on a closed channel

	if _, err := gate.Wait(context.Background()); err != nil {
		t.Errorf("Wait: %v", err)
	}
}

func TestFailedAfterReleasedKeepsTheFirstOutcome(t *testing.T) {
	gate := NewFanOutGate()
	gate.Claim(0)
	gate.Released()
	gate.Failed(errors.New("came too late"))

	leadErr, err := gate.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if leadErr != nil {
		// Failed after a successful release records the error, which is
		// legitimate -- a leader can produce a first token and then fail. What
		// must not happen is the release being undone.
		t.Logf("leadErr recorded after release: %v", leadErr)
	}
}

func TestFanOutRefusesWorkItCannotRun(t *testing.T) {
	if _, err := FanOut(context.Background(), 0, func(context.Context, int, *FanOutGate) (int, error) {
		return 0, nil
	}); !errors.Is(err, ErrNoFanOutWork) {
		t.Errorf("err = %v, want ErrNoFanOutWork", err)
	}
	if _, err := FanOut[int](context.Background(), 3, nil); err == nil {
		t.Error("a nil run function was accepted")
	}
}

func TestAnErrorNamesTheIndexNotTheContent(t *testing.T) {
	_, err := FanOut(context.Background(), 3,
		func(ctx context.Context, index int, gate *FanOutGate) (string, error) {
			if gate.Claim(index) {
				gate.Released()
			} else if _, waitErr := gate.Wait(ctx); waitErr != nil {
				return "", waitErr
			}
			if index == 2 {
				return "", errors.New("failed on ssn 123-45-6789")
			}
			return "fine", nil
		})

	if err == nil {
		t.Fatal("expected a failure")
	}
	if !strings.Contains(err.Error(), "item 2") {
		t.Errorf("the error does not name which item failed: %v", err)
	}
}
