package ops

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// sampleStep returns a Step[int] that returns the next value from values on
// each call (repeating the last once exhausted), and a pointer to the call
// count, so a test can assert the reconciler was actually consulted rather
// than bypassed.
func sampleStep(values ...int) (Step[int], *int32) {
	var calls int32
	return func(context.Context) (int, error) {
		n := atomic.AddInt32(&calls, 1)
		idx := int(n) - 1
		if idx < len(values) {
			return values[idx], nil
		}
		return values[len(values)-1], nil
	}, &calls
}

func TestVoteRejectsMissingArguments(t *testing.T) {
	valid := func(context.Context) (int, error) { return 1, nil }
	reconciler := ExactAgreement[int](1)

	if _, _, err := Vote[int](context.Background(), 3, nil, reconciler, VoteOptions{}); err == nil {
		t.Error("a nil step was accepted")
	}
	if _, _, err := Vote(context.Background(), 3, valid, nil, VoteOptions{}); err == nil {
		t.Error("a nil reconciler was accepted; agreement must not have a silent default")
	}
	if _, _, err := Vote(context.Background(), 0, valid, reconciler, VoteOptions{}); err == nil {
		t.Error("zero samples was accepted")
	}
	if _, _, err := Vote(context.Background(), -1, valid, reconciler, VoteOptions{}); err == nil {
		t.Error("a negative sample count was accepted")
	}
}

func TestVoteHonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	step, calls := sampleStep(1, 1, 1)
	if _, _, err := Vote(ctx, 3, step, ExactAgreement[int](1), VoteOptions{}); err == nil {
		t.Error("a cancelled context still ran a vote")
	}
	if atomic.LoadInt32(calls) != 0 {
		t.Errorf("the step ran %d times under a cancelled context", *calls)
	}
}

func TestVoteUnanimousSamplesAgree(t *testing.T) {
	step, _ := sampleStep(7, 7, 7)

	value, record, err := Vote(context.Background(), 3, step, ExactAgreement[int](2), VoteOptions{})
	if err != nil {
		t.Fatalf("Vote: %v", err)
	}
	if value != 7 {
		t.Errorf("value = %d, want 7", value)
	}
	if record.Requested != 3 || record.Succeeded != 3 || record.Failed != 0 {
		t.Errorf("record = %+v", record)
	}
	if record.Agree != 3 {
		t.Errorf("Agree = %d, want 3", record.Agree)
	}
	if record.AgreementRate != 1.0 {
		t.Errorf("AgreementRate = %v, want 1.0", record.AgreementRate)
	}
}

// A split vote must abstain rather than pick a plurality, when the policy the
// caller supplied says so -- the direct test of "agreement is a policy, not a
// proof".
func TestVoteSplitAbstainsRatherThanPickingAPlurality(t *testing.T) {
	// 2 votes for 1, 1 vote for 2: a plurality exists, but the policy here
	// requires at least 3 agreeing samples before it will certify anything.
	step, _ := sampleStep(1, 1, 2)

	value, record, err := Vote(context.Background(), 3, step, ExactAgreement[int](3), VoteOptions{})

	if !errors.Is(err, types.ErrReviewRequired) {
		t.Fatalf("err = %v, want errors.Is(err, types.ErrReviewRequired)", err)
	}
	if value != 0 {
		t.Errorf("value = %d, want the zero value: an abstained vote must not hand back the plurality answer", value)
	}
	if record.Agree != 2 {
		t.Errorf("Agree = %d, want 2 (the plurality size the reconciler measured)", record.Agree)
	}

	var reviewErr *types.ReviewRequiredError[int]
	if !errors.As(err, &reviewErr) {
		t.Fatalf("errors.As did not reach *types.ReviewRequiredError[int]: %v", err)
	}
	if reviewErr.Packet.Candidate != 1 {
		t.Errorf("Packet.Candidate = %d, want the reconciler's best guess (1)", reviewErr.Packet.Candidate)
	}
	if len(reviewErr.Packet.Evidence) == 0 {
		t.Error("the review packet carries no evidence about what was seen")
	}
	if reviewErr.Packet.Attempts != 3 {
		t.Errorf("Packet.Attempts = %d, want 3 (the successful sample count)", reviewErr.Packet.Attempts)
	}
}

// The reconciler must actually run, and run on the real successful samples --
// not be bypassed by some builtin majority rule.
func TestVoteConsultsThePluggableReconciler(t *testing.T) {
	step, _ := sampleStep(10, 20, 30)

	var received [][]int
	var mu sync.Mutex
	callCount := 0

	reconcile := func(_ context.Context, samples []int) (ReconcileOutcome[int], error) {
		mu.Lock()
		callCount++
		got := append([]int(nil), samples...)
		received = append(received, got)
		mu.Unlock()
		// A distinctive policy no builtin reconciler would produce: sum the
		// samples. If Vote were secretly applying its own rule instead of
		// consulting this function, the returned value would not be 60.
		sum := 0
		for _, s := range samples {
			sum += s
		}
		return ReconcileOutcome[int]{Winner: sum, Agree: len(samples)}, nil
	}

	value, record, err := Vote(context.Background(), 3, step, reconcile, VoteOptions{})
	if err != nil {
		t.Fatalf("Vote: %v", err)
	}
	if value != 60 {
		t.Errorf("value = %d, want 60 (the reconciler's rule); the reconciler was bypassed", value)
	}
	if callCount != 1 {
		t.Errorf("reconciler was called %d times, want exactly 1", callCount)
	}
	if len(received) != 1 || len(received[0]) != 3 {
		t.Fatalf("reconciler received %v, want a single call with 3 samples", received)
	}
	if record.Agree != 3 {
		t.Errorf("Agree = %d, want 3", record.Agree)
	}
}

// A reconciler error that is not the review sentinel is a reconciler
// malfunction, not an abstention, and must not be reported as one.
func TestVoteReconcilerErrorIsNotTreatedAsAbstention(t *testing.T) {
	step, _ := sampleStep(1, 2, 3)
	boom := errors.New("reconciler bug")

	reconcile := func(context.Context, []int) (ReconcileOutcome[int], error) {
		return ReconcileOutcome[int]{}, boom
	}

	_, _, err := Vote(context.Background(), 3, step, reconcile, VoteOptions{})
	if err == nil {
		t.Fatal("a reconciler failure was reported as success")
	}
	if errors.Is(err, types.ErrReviewRequired) {
		t.Error("a reconciler malfunction was reported as an abstention")
	}
	if !errors.Is(err, boom) {
		t.Errorf("the underlying reconciler error was lost: %v", err)
	}
}

func TestVotePartialFailuresStillReconcileTheSuccesses(t *testing.T) {
	calls := 0
	step := func(context.Context) (int, error) {
		calls++
		if calls == 2 {
			return 0, &types.OperationError{Kind: types.KindProviderUnavailable, Message: "down"}
		}
		return 5, nil
	}

	value, record, err := Vote(context.Background(), 3, step, ExactAgreement[int](1), VoteOptions{})
	if err != nil {
		t.Fatalf("Vote: %v", err)
	}
	if value != 5 {
		t.Errorf("value = %d, want 5", value)
	}
	if record.Succeeded != 2 || record.Failed != 1 {
		t.Errorf("record = %+v, want 2 succeeded and 1 failed", record)
	}
}

func TestVoteAllSamplesFailingIsAnError(t *testing.T) {
	step := func(context.Context) (int, error) {
		return 0, &types.OperationError{Kind: types.KindProviderUnavailable, Message: "down"}
	}

	reconciled := false
	reconcile := func(context.Context, []int) (ReconcileOutcome[int], error) {
		reconciled = true
		return ReconcileOutcome[int]{}, nil
	}

	_, record, err := Vote(context.Background(), 3, step, reconcile, VoteOptions{})
	if err == nil {
		t.Fatal("all samples failing was reported as success")
	}
	if reconciled {
		t.Error("the reconciler ran with zero samples")
	}
	if record.Succeeded != 0 || record.Failed != 3 {
		t.Errorf("record = %+v", record)
	}
}

// Bounded concurrency, proven by peak in-flight rather than total calls: with
// exactly `concurrency` workers, a step that blocks until `concurrency` of
// itself are simultaneously in flight can only unblock if the pool really
// bounds to that number -- fewer workers deadlocks (caught by the timeout
// below), more workers would let the block release early with fewer than
// `concurrency` counted, which the assertion on `entered` also catches.
func TestVoteConcurrencyIsBounded(t *testing.T) {
	const concurrency = 3
	const samples = 9

	var active, peak, entered int32
	ready := make(chan struct{})
	var closeOnce sync.Once

	step := func(ctx context.Context) (int, error) {
		n := atomic.AddInt32(&active, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if n <= old {
				break
			}
			if atomic.CompareAndSwapInt32(&peak, old, n) {
				break
			}
		}

		if atomic.AddInt32(&entered, 1) == int32(concurrency) {
			closeOnce.Do(func() { close(ready) })
		}

		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			return 0, fmt.Errorf("deadlock: only %d of %d workers ever became concurrent",
				atomic.LoadInt32(&entered), concurrency)
		}

		atomic.AddInt32(&active, -1)
		return 1, nil
	}

	_, record, err := Vote(context.Background(), samples, step, ExactAgreement[int](1),
		VoteOptions{Concurrency: concurrency})
	if err != nil {
		t.Fatalf("Vote: %v", err)
	}
	if record.Succeeded != samples {
		t.Fatalf("Succeeded = %d, want %d", record.Succeeded, samples)
	}
	if peak != int32(concurrency) {
		t.Errorf("peak in-flight = %d, want exactly %d", peak, concurrency)
	}
}

func TestVoteAgreementRateMeansWhatItsNameSays(t *testing.T) {
	// 2 of 3 agree: AgreementRate must be exactly Agree/Succeeded, not
	// something derived from anything else (string length, a model's own
	// score, or a hardcoded number).
	step, _ := sampleStep(9, 9, 5)

	_, record, err := Vote(context.Background(), 3, step, ExactAgreement[int](1), VoteOptions{})
	if err != nil {
		t.Fatalf("Vote: %v", err)
	}
	if record.Agree != 2 {
		t.Fatalf("Agree = %d, want 2", record.Agree)
	}
	want := 2.0 / 3.0
	if record.AgreementRate != want {
		t.Errorf("AgreementRate = %v, want %v (Agree/Succeeded exactly)", record.AgreementRate, want)
	}
}

func TestExactAgreementBreaksTiesByFirstOccurrence(t *testing.T) {
	// 2, 3, 2, 3: a tie between the two values. First occurrence in the
	// samples slice (2) must win deterministically -- not map iteration order.
	for i := 0; i < 20; i++ {
		outcome, err := ExactAgreement[int](1)(context.Background(), []int{2, 3, 2, 3})
		if err != nil {
			t.Fatalf("ExactAgreement: %v", err)
		}
		if outcome.Winner != 2 {
			t.Fatalf("run %d: Winner = %d, want 2 (deterministic tie-break)", i, outcome.Winner)
		}
		if outcome.Agree != 2 {
			t.Fatalf("run %d: Agree = %d, want 2", i, outcome.Agree)
		}
	}
}

func TestExactAgreementAbstainsBelowMinimum(t *testing.T) {
	outcome, err := ExactAgreement[int](3)(context.Background(), []int{1, 1, 2})
	if err != nil {
		t.Fatalf("ExactAgreement: %v", err)
	}
	if !outcome.Abstain {
		t.Error("agreement of 2 was certified against a minimum of 3")
	}
	if len(outcome.FailedChecks) == 0 {
		t.Error("an abstention carries no reason")
	}
}
