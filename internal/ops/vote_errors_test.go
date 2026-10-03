package ops

import (
	"context"
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Every failure of Vote's own has a kind, with the text it had before kinds
// were declared; a failure that wraps a caller's own keeps that error in its
// chain and the kind it carried.
func TestVoteFailuresHaveKinds(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	step, _ := sampleStep(1, 2, 3)
	same := Reconciler[int](func(_ context.Context, samples []int) (ReconcileOutcome[int], error) {
		return ReconcileOutcome[int]{Winner: samples[0], Agree: len(samples)}, nil
	})
	unavailable := &types.OperationError{Kind: types.KindProviderUnavailable, Op: "theirs", Message: "down"}
	plain := errors.New("a failure of the caller's own")
	cases := []struct {
		name     string
		run      func() error
		text     string
		kind     types.ErrorKind
		sentinel error
		narrow   types.Narrowing
	}{
		{"no step", func() error { _, _, err := Vote(context.Background(), 3, nil, same, VoteOptions{}); return err },
			"vote: no step to sample", types.KindConfiguration, types.ErrConfiguration, types.NarrowNone},
		{"no reconciler", func() error { _, _, err := Vote(context.Background(), 3, step, nil, VoteOptions{}); return err },
			"vote: no reconciler; agreement is a policy the caller must supply", types.KindConfiguration, types.ErrConfiguration, types.NarrowNone},
		{"no samples", func() error { _, _, err := Vote(context.Background(), 0, step, same, VoteOptions{}); return err },
			"vote: 0 is not a number of samples", types.KindConfiguration, types.ErrConfiguration, types.NarrowNone},
		{"a negative number of samples", func() error { _, _, err := Vote(context.Background(), -2, step, same, VoteOptions{}); return err },
			"vote: -2 is not a number of samples", types.KindConfiguration, types.ErrConfiguration, types.NarrowNone},
		{"a cancelled context", func() error { _, _, err := Vote(cancelled, 3, step, same, VoteOptions{}); return err },
			"context canceled", types.KindCanceled, types.ErrCanceled, types.NarrowNone},
		{"a reconciler that counts more agreeing samples than there are", func() error {
			_, _, err := Vote(context.Background(), 3, step, func(context.Context, []int) (ReconcileOutcome[int], error) {
				return ReconcileOutcome[int]{Winner: 1, Agree: 9}, nil
			}, VoteOptions{Concurrency: 1})
			return err
		}, "vote: the reconciler reported 9 samples agreeing out of 3 that succeeded", types.KindConfiguration, types.ErrConfiguration, types.NarrowNone},
		{"a reconciler that counts a negative number of agreeing samples", func() error {
			_, _, err := Vote(context.Background(), 2, step, func(context.Context, []int) (ReconcileOutcome[int], error) {
				return ReconcileOutcome[int]{Winner: 1, Agree: -1}, nil
			}, VoteOptions{Concurrency: 1})
			return err
		}, "vote: the reconciler reported -1 samples agreeing out of 2 that succeeded", types.KindConfiguration, types.ErrConfiguration, types.NarrowNone},
		{"ExactAgreement with a minimum of zero", func() error {
			_, _, err := Vote(context.Background(), 3, step, ExactAgreement[int](0), VoteOptions{Concurrency: 1})
			return err
		}, "vote: reconciler failed: vote: ExactAgreement needs a minimum of at least 1, got 0", types.KindConfiguration, types.ErrConfiguration, types.NarrowNone},
		{"every sample failing with a classified error", func() error {
			_, _, err := Vote(context.Background(), 3, Step[int](func(context.Context) (int, error) { return 0, unavailable }), same, VoteOptions{Concurrency: 1})
			return err
		}, "vote: all 3 samples failed, first error: " + unavailable.Error(), types.KindProviderUnavailable, types.ErrProviderUnavailable, types.NarrowNone},
		{"every sample panicking", func() error {
			_, _, err := Vote(context.Background(), 2, Step[int](func(context.Context) (int, error) { panic("secret") }), same, VoteOptions{Concurrency: 1})
			return err
		}, "vote: all 2 samples failed, first error: mapreduce: invariant violation: the operation panicked on chunk 0", types.KindInvariantViolation, types.ErrInvariantViolation, types.NarrowPanic},
		{"a reconciler that fails with a classified error", func() error {
			_, _, err := Vote(context.Background(), 3, step, func(context.Context, []int) (ReconcileOutcome[int], error) {
				return ReconcileOutcome[int]{}, unavailable
			}, VoteOptions{Concurrency: 1})
			return err
		}, "vote: reconciler failed: " + unavailable.Error(), types.KindProviderUnavailable, types.ErrProviderUnavailable, types.NarrowNone},
		{"a vote that abstains", func() error {
			differing, _ := sampleStep(1, 2, 3)
			_, _, err := Vote(context.Background(), 3, differing, ExactAgreement[int](3), VoteOptions{Concurrency: 1})
			return err
		}, "", types.KindReviewRequired, types.ErrReviewRequired, types.NarrowNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("no error")
			}
			if tc.text != "" && err.Error() != tc.text {
				t.Errorf("text = %q, want %q", err.Error(), tc.text)
			}
			if kind := types.KindOf(err); kind != tc.kind {
				t.Errorf("kind = %v, want %v", kind, tc.kind)
			}
			if !errors.Is(err, tc.sentinel) {
				t.Errorf("errors.Is(err, %v) is false", tc.sentinel)
			}
			if got := types.NarrowingOf(err); got != tc.narrow {
				t.Errorf("narrowing = %v, want %v", got, tc.narrow)
			}
		})
	}

	t.Run("a sample's own errors are in the chain of the error that gives up on them", func(t *testing.T) {
		_, record, err := Vote(context.Background(), 2, Step[int](func(context.Context) (int, error) { return 0, plain }), same, VoteOptions{Concurrency: 1})
		if !errors.Is(err, plain) || record.Failed != 2 || record.Requested != 2 {
			t.Errorf("err = %v, record = %+v", err, record)
		}
	})
	t.Run("a reconciler's own error is in the chain", func(t *testing.T) {
		_, _, err := Vote(context.Background(), 2, step, func(context.Context, []int) (ReconcileOutcome[int], error) {
			return ReconcileOutcome[int]{}, plain
		}, VoteOptions{Concurrency: 1})
		if !errors.Is(err, plain) || err.Error() != "vote: reconciler failed: a failure of the caller's own" {
			t.Errorf("err = %v", err)
		}
	})
}
