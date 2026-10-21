package flow_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"
)

// sequence is a step that returns the given answers one after another, one per
// call, and counts its calls. It is for a run with Concurrency 1, where the order
// of the calls is the order of the samples.
func sequence[T any](answers ...T) (schemaflux.Step[T], *atomic.Int32) {
	var calls atomic.Int32
	return func(context.Context) (T, error) {
		return answers[int(calls.Add(1))-1], nil
	}, &calls
}

func TestVoteReturnsTheWinnerOfTheLargestGroupAndCountsTheSamples(t *testing.T) {
	cases := []struct {
		name    string
		answers []string
		minimum int
		want    string
		agree   int
		rate    float64
	}{
		{"two of three agree", []string{"a", "b", "a"}, 2, "a", 2, 2.0 / 3},
		{"all of three agree", []string{"a", "a", "a"}, 3, "a", 3, 1},
		{"the answer that came last wins", []string{"b", "a", "a"}, 2, "a", 2, 2.0 / 3},
		{"four of five", []string{"x", "x", "y", "x", "x"}, 4, "x", 4, 0.8},
		{"one sample", []string{"only"}, 1, "only", 1, 1},
		{"a tie goes to the answer that came first", []string{"b", "a", "a", "b"}, 2, "b", 2, 0.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			step, _ := sequence(tc.answers...)
			got, record, err := flow.Vote(context.Background(), len(tc.answers), step, flow.ExactAgreement[string](tc.minimum), flow.VoteOptions{Concurrency: 1})
			if err != nil || got != tc.want {
				t.Fatalf("Vote = %q, %v", got, err)
			}
			want := flow.VoteRecord{Requested: len(tc.answers), Succeeded: len(tc.answers), Failed: 0, Agree: tc.agree, AgreementRate: tc.rate}
			if record != want {
				t.Errorf("record = %+v, want %+v", record, want)
			}
		})
	}
}

func TestVoteStartsTheSamplesInOrderAndBoundsHowManyRunAtOnce(t *testing.T) {
	cases := []struct {
		name        string
		samples     int
		concurrency int
		peak        int32
	}{
		{"the default is four", 10, 0, 4},
		{"one at a time", 5, 1, 1},
		{"two at a time", 6, 2, 2},
		{"as many as there are samples", 3, 8, 3},
		{"fewer samples than the default", 2, 0, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var running, peak, calls atomic.Int32
				gate := make(chan struct{})
				step := func(context.Context) (int, error) {
					calls.Add(1)
					now := running.Add(1)
					for {
						old := peak.Load()
						if now <= old || peak.CompareAndSwap(old, now) {
							break
						}
					}
					<-gate
					running.Add(-1)
					return 1, nil
				}
				var wg sync.WaitGroup
				wg.Go(func() {
					_, _, err := flow.Vote(context.Background(), tc.samples, step, flow.ExactAgreement[int](1), flow.VoteOptions{Concurrency: tc.concurrency})
					if err != nil {
						t.Error(err)
					}
				})
				// Release the samples one wave at a time until all have run.
				for i := 0; i < tc.samples; i++ {
					synctest.Wait()
					if running.Load() > tc.peak {
						t.Fatalf("%d samples running at once, want at most %d", running.Load(), tc.peak)
					}
					gate <- struct{}{}
				}
				wg.Wait()
				if peak.Load() != tc.peak || calls.Load() != int32(tc.samples) {
					t.Errorf("peak %d with %d calls; want a peak of %d and %d calls", peak.Load(), calls.Load(), tc.peak, tc.samples)
				}
			})
		})
	}
}

func TestVoteKeepsGoingWhenASampleFailsAndReconcilesTheOnesThatSucceeded(t *testing.T) {
	var calls atomic.Int32
	step := func(context.Context) (string, error) {
		switch calls.Add(1) {
		case 2, 4:
			return "", errStep
		}
		return "same", nil
	}
	var seen []string
	reconcile := func(_ context.Context, samples []string) (flow.ReconcileOutcome[string], error) {
		seen = samples
		return flow.ReconcileOutcome[string]{Winner: samples[0], Agree: len(samples)}, nil
	}
	got, record, err := flow.Vote(context.Background(), 5, step, reconcile, flow.VoteOptions{Concurrency: 1})
	if err != nil || got != "same" {
		t.Fatalf("Vote = %q, %v", got, err)
	}
	if len(seen) != 3 {
		t.Errorf("the reconciler saw %d samples, want the 3 that succeeded", len(seen))
	}
	want := flow.VoteRecord{Requested: 5, Succeeded: 3, Failed: 2, Agree: 3, AgreementRate: 1}
	if record != want {
		t.Errorf("record = %+v, want %+v", record, want)
	}
}

func TestVoteHandsTheReconcilerTheSamplesInTheOrderTheyWereStarted(t *testing.T) {
	step, _ := sequence(10, 20, 30, 40)
	var seen []int
	_, _, err := flow.Vote(context.Background(), 4, step, func(_ context.Context, samples []int) (flow.ReconcileOutcome[int], error) {
		seen = append([]int(nil), samples...)
		return flow.ReconcileOutcome[int]{Winner: samples[0], Agree: 1}, nil
	}, flow.VoteOptions{Concurrency: 1})
	if err != nil || !reflect.DeepEqual(seen, []int{10, 20, 30, 40}) {
		t.Errorf("err = %v, the reconciler saw %v", err, seen)
	}
}

func TestVoteReturnsTheFirstSamplesErrorAsItWasGivenWhenEverySampleFailsAndAsksNoReconciler(t *testing.T) {
	var calls, reconciled atomic.Int32
	errs := []error{errors.New("sample one"), fmt.Errorf("sample two: %w", errStep), errors.New("sample three")}
	step := func(context.Context) (string, error) { return "", errs[calls.Add(1)-1] }
	got, record, err := flow.Vote(context.Background(), 3, step, func(context.Context, []string) (flow.ReconcileOutcome[string], error) {
		reconciled.Add(1)
		return flow.ReconcileOutcome[string]{}, nil
	}, flow.VoteOptions{Concurrency: 1})
	if err != errs[0] || got != "" {
		t.Errorf("Vote = %q, %v; want the first sample's error as it was given", got, err)
	}
	if reconciled.Load() != 0 || record != (flow.VoteRecord{Requested: 3, Failed: 3}) {
		t.Errorf("reconciler called %d times, record = %+v", reconciled.Load(), record)
	}
}

func TestVoteReturnsAReviewRequestWithTheCandidateWhenTheReconcilerAbstains(t *testing.T) {
	step, _ := sequence("a", "b", "c")
	got, record, err := flow.Vote(context.Background(), 3, step, flow.ExactAgreement[string](2), flow.VoteOptions{Concurrency: 1})
	e := wantKind(t, err, schemaflux.ErrReviewRequired)
	if got != "" {
		t.Errorf("Vote returned %q beside a review request", got)
	}
	if e.Op != "flow.vote" {
		t.Errorf("Op = %q", e.Op)
	}
	var review *flow.ReviewError[string]
	if !errors.As(err, &review) {
		t.Fatalf("errors.As to *flow.ReviewError[string] fails for %v", err)
	}
	if !errors.Is(review, schemaflux.ErrReviewRequired) {
		t.Error("errors.Is(review, ErrReviewRequired) is false")
	}
	r := review.Review
	if r.Candidate != "a" || r.Attempts != 3 || !reflect.DeepEqual(r.FailedChecks, []string{"agreement below minimum"}) {
		t.Errorf("review = %+v", r)
	}
	if len(r.Evidence) != 1 || r.Evidence[0] != "1 of 3 samples agreed with the reconciler's candidate" {
		t.Errorf("evidence = %v", r.Evidence)
	}
	if r.SuggestedAction == "" || r.InputRefs != nil {
		t.Errorf("suggested action %q, input refs %v", r.SuggestedAction, r.InputRefs)
	}
	if record.Requested != 3 || record.Succeeded != 3 || record.Agree != 1 {
		t.Errorf("record = %+v; a vote that was refused is still reported", record)
	}
}

func TestVoteReviewRequestNamesNothingOfTheCandidateInItsMessage(t *testing.T) {
	const canary = "candidate-4471-canary"
	step, _ := sequence(canary, "other")
	_, _, err := flow.Vote(context.Background(), 2, step, flow.ExactAgreement[string](2), flow.VoteOptions{Concurrency: 1})
	wantKind(t, err, schemaflux.ErrReviewRequired)
	var review *flow.ReviewError[string]
	if !errors.As(err, &review) {
		t.Fatal("no review")
	}
	for _, text := range []string{err.Error(), review.Error(), fmt.Sprintf("%v %+v", err, err)} {
		if strings.Contains(text, canary) {
			t.Errorf("the candidate is in %q", text)
		}
	}
	if review.Review.Candidate != canary {
		t.Errorf("the candidate is not in the review: %+v", review.Review)
	}
}

func TestVoteReturnsTheReconcilersErrorAsItWasGivenWithTheRecord(t *testing.T) {
	step, _ := sequence(1, 2, 3)
	failure := fmt.Errorf("the validator failed: %w", errStep)
	got, record, err := flow.Vote(context.Background(), 3, step, func(context.Context, []int) (flow.ReconcileOutcome[int], error) {
		return flow.ReconcileOutcome[int]{Winner: 9, Agree: 3}, failure
	}, flow.VoteOptions{Concurrency: 1})
	if err != failure || got != 0 {
		t.Errorf("Vote = %d, %v; want the reconciler's error as it was given", got, err)
	}
	if record.Requested != 3 || record.Succeeded != 3 || record.Agree != 0 {
		t.Errorf("record = %+v", record)
	}
}

func TestVoteRefusesAReconcilerThatCountsMoreAgreementThanThereAreSamples(t *testing.T) {
	for _, agree := range []int{-1, 4, 100} {
		t.Run(fmt.Sprint("agree ", agree), func(t *testing.T) {
			step, _ := sequence(1, 2, 3)
			got, record, err := flow.Vote(context.Background(), 3, step, func(context.Context, []int) (flow.ReconcileOutcome[int], error) {
				return flow.ReconcileOutcome[int]{Winner: 1, Agree: agree}, nil
			}, flow.VoteOptions{Concurrency: 1})
			wantKind(t, err, schemaflux.ErrConfiguration)
			if got != 0 || record.Agree != 0 || record.AgreementRate != 0 {
				t.Errorf("got = %d, record = %+v; an impossible count is not reported as agreement", got, record)
			}
		})
	}
}

func TestVoteRefusesAnArgumentItCannotUseAndRunsNothing(t *testing.T) {
	var ran atomic.Int32
	reconcile := flow.ExactAgreement[string](1)
	cases := []struct {
		name      string
		samples   int
		step      schemaflux.Step[string]
		reconcile flow.Reconciler[string]
		kind      schemaflux.Kind
		options   []flow.VoteOptions
	}{
		{"no step", 3, nil, reconcile, schemaflux.ErrInvalidRequest, nil},
		{"no reconciler", 3, counts(&ran, "a"), nil, schemaflux.ErrInvalidRequest, nil},
		{"no samples", 0, counts(&ran, "a"), reconcile, schemaflux.ErrInvalidRequest, nil},
		{"a negative number of samples", -3, counts(&ran, "a"), reconcile, schemaflux.ErrInvalidRequest, nil},
		{"a negative concurrency", 3, counts(&ran, "a"), reconcile, schemaflux.ErrConfiguration, []flow.VoteOptions{{Concurrency: -1}}},
		{"a negative concurrency among several options", 3, counts(&ran, "a"), reconcile, schemaflux.ErrConfiguration, []flow.VoteOptions{{Concurrency: 2}, {Concurrency: -2}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, record, err := flow.Vote(context.Background(), tc.samples, tc.step, tc.reconcile, tc.options...)
			e := wantKind(t, err, tc.kind)
			if e.Op != "flow.vote" || got != "" || record != (flow.VoteRecord{}) || ran.Load() != 0 {
				t.Errorf("Op = %q, got = %q, record = %+v, steps run = %d", e.Op, got, record, ran.Load())
			}
		})
	}
}

func TestVoteRefusesANilContextAndAContextThatIsOverAndRunsNothing(t *testing.T) {
	var ran atomic.Int32
	var nilContext context.Context
	_, _, err := flow.Vote(nilContext, 3, counts(&ran, "a"), flow.ExactAgreement[string](1))
	wantKind(t, err, schemaflux.ErrInvalidRequest)
	for _, done := range doneContexts() {
		_, record, err := flow.Vote(done.ctx, 3, counts(&ran, "a"), flow.ExactAgreement[string](1))
		wantKind(t, err, done.kind)
		if !errors.Is(err, done.is) || record != (flow.VoteRecord{}) {
			t.Errorf("%s: err = %v, record = %+v", done.name, err, record)
		}
	}
	if ran.Load() != 0 {
		t.Errorf("%d samples ran", ran.Load())
	}
}

func TestVoteStopsWhenTheCallerCancelsAndReturnsTheContextsErrorWithNoPartialAnswer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var calls atomic.Int32
		step := func(ctx context.Context) (string, error) {
			if calls.Add(1) == 1 {
				return "done", nil
			}
			<-ctx.Done()
			return "", ctx.Err()
		}
		type outcome struct {
			got    string
			record flow.VoteRecord
			err    error
		}
		result := make(chan outcome)
		go func() {
			got, record, err := flow.Vote(ctx, 4, step, flow.ExactAgreement[string](1), flow.VoteOptions{Concurrency: 2})
			result <- outcome{got, record, err}
		}()
		synctest.Wait()
		cancel()
		r := <-result
		wantKind(t, r.err, schemaflux.ErrCanceled)
		if !errors.Is(r.err, context.Canceled) || r.got != "" {
			t.Errorf("err = %v, got = %q", r.err, r.got)
		}
	})
}

func TestVoteCountsASampleThatPanicsAsFailedAndHoldsNothingOfThePanic(t *testing.T) {
	const canary = "panic-payload-4471"
	var calls atomic.Int32
	step := func(context.Context) (string, error) {
		if calls.Add(1) == 2 {
			panic(canary)
		}
		return "same", nil
	}
	got, record, err := flow.Vote(context.Background(), 3, step, flow.ExactAgreement[string](2), flow.VoteOptions{Concurrency: 1})
	if err != nil || got != "same" || record.Failed != 1 || record.Succeeded != 2 {
		t.Fatalf("Vote = %q, %+v, %v; a sample that panics is a failed sample", got, record, err)
	}

	calls.Store(0)
	allPanic := func(context.Context) (string, error) { panic(canary) }
	_, _, err = flow.Vote(context.Background(), 2, allPanic, flow.ExactAgreement[string](1))
	e := wantKind(t, err, schemaflux.ErrInternal)
	if strings.Contains(err.Error(), canary) || strings.Contains(fmt.Sprintf("%+v %v", e, e.Err), canary) {
		t.Errorf("the panic's value is in the error: %v", err)
	}
}

func TestVoteTurnsASampleThatEndsItsGoroutineIntoAFailedSample(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, record, err := flow.Vote(context.Background(), 2, goexit[string], flow.ExactAgreement[string](1))
		wantKind(t, err, schemaflux.ErrInternal)
		if record.Failed != 2 {
			t.Errorf("record = %+v", record)
		}
	})
}

func TestVoteMergesSeveralOptionsLeftToRightALaterNonZeroFieldWinning(t *testing.T) {
	cases := []struct {
		name    string
		options []flow.VoteOptions
		peak    int32
	}{
		{"no options", nil, 4},
		{"one zero value", []flow.VoteOptions{{}}, 4},
		{"one value", []flow.VoteOptions{{Concurrency: 2}}, 2},
		{"a later value wins", []flow.VoteOptions{{Concurrency: 2}, {Concurrency: 3}}, 3},
		{"a later zero value does not blank an earlier one", []flow.VoteOptions{{Concurrency: 2}, {}}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var running, peak atomic.Int32
				gate := make(chan struct{})
				step := func(context.Context) (int, error) {
					now := running.Add(1)
					for {
						old := peak.Load()
						if now <= old || peak.CompareAndSwap(old, now) {
							break
						}
					}
					<-gate
					running.Add(-1)
					return 1, nil
				}
				var wg sync.WaitGroup
				wg.Go(func() {
					_, _, _ = flow.Vote(context.Background(), 12, step, flow.ExactAgreement[int](1), tc.options...)
				})
				for i := 0; i < 12; i++ {
					synctest.Wait()
					gate <- struct{}{}
				}
				wg.Wait()
				if peak.Load() != tc.peak {
					t.Errorf("peak = %d, want %d", peak.Load(), tc.peak)
				}
			})
		})
	}
}

func TestVoteHandsTheStepsAndTheReconcilerTheCallersContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), contextKey{}, "mine")
	read := func(ctx context.Context) (string, error) { return fmt.Sprint(ctx.Value(contextKey{})), nil }
	var inReconciler string
	got, _, err := flow.Vote(ctx, 2, read, func(ctx context.Context, samples []string) (flow.ReconcileOutcome[string], error) {
		inReconciler = fmt.Sprint(ctx.Value(contextKey{}))
		return flow.ReconcileOutcome[string]{Winner: samples[0], Agree: 2}, nil
	})
	if err != nil || got != "mine" || inReconciler != "mine" {
		t.Errorf("Vote = %q, %v; the reconciler saw %q", got, err, inReconciler)
	}
}

func TestVoteLeavesNoGoroutineBehind(t *testing.T) {
	cases := []struct {
		name string
		step schemaflux.Step[string]
	}{
		{"every sample succeeds", wait(time.Second, "a")},
		{"every sample fails", fails[string](errStep)},
		{"every sample panics", panics[string]("x")},
		{"every sample ignores cancellation", stubborn(time.Minute, "a")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				leaks := noLeak(t)
				_, _, _ = flow.Vote(context.Background(), 9, tc.step, flow.ExactAgreement[string](1))
				leaks()
			})
		})
	}
}

func TestExactAgreementPicksTheLargestGroupAndAbstainsBelowTheMinimum(t *testing.T) {
	cases := []struct {
		name    string
		minimum int
		samples []string
		winner  string
		agree   int
		abstain bool
	}{
		{"a clear majority", 2, []string{"a", "b", "a"}, "a", 2, false},
		{"unanimous", 3, []string{"a", "a", "a"}, "a", 3, false},
		{"a majority below the minimum", 3, []string{"a", "b", "a"}, "a", 2, true},
		{"no two alike and a minimum of two", 2, []string{"a", "b", "c"}, "a", 1, true},
		{"no two alike and a minimum of one", 1, []string{"a", "b", "c"}, "a", 1, false},
		{"a tie goes to the first to occur", 2, []string{"b", "a", "a", "b"}, "b", 2, false},
		{"a tie that the first leads", 2, []string{"a", "b", "b", "a"}, "a", 2, false},
		{"one sample and a minimum of one", 1, []string{"x"}, "x", 1, false},
		{"one sample and a minimum of two", 2, []string{"x"}, "x", 1, true},
		{"no samples", 1, nil, "", 0, true},
		{"a minimum above the number of samples", 5, []string{"a", "a"}, "a", 2, true},
		{"empty strings agree", 2, []string{"", "", "a"}, "", 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := flow.ExactAgreement[string](tc.minimum)(context.Background(), tc.samples)
			if err != nil {
				t.Fatal(err)
			}
			if got.Winner != tc.winner || got.Agree != tc.agree || got.Abstain != tc.abstain {
				t.Errorf("outcome = %+v, want winner %q, agree %d, abstain %v", got, tc.winner, tc.agree, tc.abstain)
			}
			if tc.abstain && (len(got.FailedChecks) == 0 || got.SuggestedAction == "") {
				t.Errorf("an abstention names what failed and what to do: %+v", got)
			}
			if !tc.abstain && (len(got.FailedChecks) != 0 || got.SuggestedAction != "") {
				t.Errorf("an agreement carries no failed checks: %+v", got)
			}
		})
	}
}

func TestExactAgreementWorksOverAnyComparableType(t *testing.T) {
	type point struct{ X, Y int }
	got, err := flow.ExactAgreement[point](2)(context.Background(), []point{{1, 2}, {3, 4}, {1, 2}})
	if err != nil || got.Winner != (point{1, 2}) || got.Agree != 2 || got.Abstain {
		t.Errorf("outcome = %+v, %v", got, err)
	}
	gotInt, err := flow.ExactAgreement[int](2)(context.Background(), []int{7, 7, 9})
	if err != nil || gotInt.Winner != 7 {
		t.Errorf("outcome = %+v, %v", gotInt, err)
	}
	gotBool, err := flow.ExactAgreement[bool](2)(context.Background(), []bool{true, false, true})
	if err != nil || !gotBool.Winner {
		t.Errorf("outcome = %+v, %v", gotBool, err)
	}
}

func TestExactAgreementRefusesAMinimumBelowOneEachTimeItIsCalled(t *testing.T) {
	for _, minimum := range []int{0, -1, -100} {
		reconcile := flow.ExactAgreement[string](minimum)
		for call := 0; call < 2; call++ {
			got, err := reconcile(context.Background(), []string{"a", "a"})
			e := wantKind(t, err, schemaflux.ErrInvalidRequest)
			if e.Op != "flow.exactagreement" || got.Agree != 0 || got.Winner != "" || got.Abstain {
				t.Errorf("minimum %d: Op = %q, outcome = %+v", minimum, e.Op, got)
			}
		}
	}
}

func TestExactAgreementsRefusalReachesVoteAsTheReconcilersErrorAsItWasGiven(t *testing.T) {
	step, _ := sequence("a", "a")
	_, _, err := flow.Vote(context.Background(), 2, step, flow.ExactAgreement[string](0), flow.VoteOptions{Concurrency: 1})
	e := wantKind(t, err, schemaflux.ErrInvalidRequest)
	if e.Op != "flow.exactagreement" {
		t.Errorf("Op = %q; the reconciler's own error comes back as it was given", e.Op)
	}
}

func TestExactAgreementDoesNotChangeTheSamplesItIsGiven(t *testing.T) {
	samples := []string{"b", "a", "a"}
	_, _ = flow.ExactAgreement[string](2)(context.Background(), samples)
	if !reflect.DeepEqual(samples, []string{"b", "a", "a"}) {
		t.Errorf("samples = %v", samples)
	}
}
