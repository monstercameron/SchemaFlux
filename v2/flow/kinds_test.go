package flow_test

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"

	"github.com/monstercameron/schemaflux/internal/types"
)

// kindRules is the table of the package comment, as data: for each kind of
// error a step can fail with, whether Escalate and Until stop at it (the same
// failure from a stronger route, or the same call again, cannot help) or go on to
// the next route or attempt. The other combinators do not read the kind: Approve
// asks a person only for a step whose recovery ran out, and Vote, Checkpoint and
// the speculative combinators hand the error on as it was given.
var kindRules = []struct {
	kind  schemaflux.Kind
	stops bool
}{
	{schemaflux.ErrConfiguration, true},
	{schemaflux.ErrInvalidRequest, true},
	{schemaflux.ErrUnsupported, true},
	{schemaflux.ErrUnauthorized, true},
	{schemaflux.ErrPolicy, true},
	{schemaflux.ErrBudgetExceeded, true},
	{schemaflux.ErrBusy, false},
	{schemaflux.ErrRateLimited, false},
	{schemaflux.ErrUnavailable, false},
	{schemaflux.ErrTimeout, false},
	{schemaflux.ErrCanceled, true},
	{schemaflux.ErrClosed, true},
	{schemaflux.ErrTooLarge, false},
	{schemaflux.ErrTruncated, false},
	{schemaflux.ErrRefused, true},
	{schemaflux.ErrInvalidAnswer, false},
	{schemaflux.ErrReviewRequired, true},
	{schemaflux.ErrInternal, true},
}

// failuresOf is the errors a step can fail with for a kind: the bare *Error and
// the one the library's own call path makes, which holds the internal error it
// was made from. The rule is the kind's, whichever of them the step returns.
func failuresOf(kind schemaflux.Kind) []error {
	failures := []error{&schemaflux.Error{Kind: kind, Op: "extract"}}
	switch kind {
	case schemaflux.ErrInternal:
		failures = append(failures, &schemaflux.Error{Kind: kind, Op: "extract", Err: &types.OperationError{
			Kind: types.KindInvariantViolation, Narrow: types.NarrowPanic, Op: "gather", Message: "a step panicked"}})
	case schemaflux.ErrTimeout:
		failures = append(failures, &schemaflux.Error{Kind: kind, Op: "extract", Err: types.Newf(types.KindTimeout, "extract", "too slow")})
	case schemaflux.ErrInvalidAnswer:
		failures = append(failures, &schemaflux.Error{Kind: kind, Op: "extract", Err: types.Newf(types.KindMalformedOutput, "extract", "not JSON")})
	}
	return failures
}

func TestTheKindTableCoversEveryKindOnce(t *testing.T) {
	seen := map[schemaflux.Kind]bool{}
	for _, rule := range kindRules {
		if seen[rule.kind] {
			t.Errorf("%v is in the table twice", rule.kind)
		}
		seen[rule.kind] = true
	}
	count := 0
	for kind := schemaflux.ErrConfiguration; kind <= schemaflux.ErrInternal; kind++ {
		count++
		if !seen[kind] {
			t.Errorf("%v is not in the table", kind)
		}
	}
	if count != 18 || len(kindRules) != 18 {
		t.Errorf("%d kinds, %d rules; want 18 of each", count, len(kindRules))
	}
}

func TestEscalateStopsOrEscalatesByTheKindOfTheFirstFailureAsTheTableSays(t *testing.T) {
	for _, rule := range kindRules {
		for i, failure := range failuresOf(rule.kind) {
			t.Run(fmt.Sprintf("%v/%d", rule.kind, i), func(t *testing.T) {
				var stronger atomic.Int32
				got, record, err := flow.Escalate(context.Background(), fails[string](failure), counts(&stronger, "strong"), nil)
				if rule.stops {
					if err != failure || got != "" || record.Escalated || record.FirstError != failure || stronger.Load() != 0 {
						t.Errorf("Escalate = %q, %v, %+v after %d stronger runs; want the first error as it was given and no escalation",
							got, err, record, stronger.Load())
					}
					return
				}
				if err != nil || got != "strong" || !record.Escalated || record.Reason != "error" || record.FirstError != failure {
					t.Errorf("Escalate = %q, %v, %+v; want the stronger answer and an escalation", got, err, record)
				}
			})
		}
	}
}

func TestUntilStopsOrTriesAgainByTheKindOfTheFailureAsTheTableSays(t *testing.T) {
	for _, rule := range kindRules {
		for i, failure := range failuresOf(rule.kind) {
			t.Run(fmt.Sprintf("%v/%d", rule.kind, i), func(t *testing.T) {
				var runs atomic.Int32
				step := func(context.Context) (string, error) {
					if runs.Add(1) == 1 {
						return "", failure
					}
					return "ok", nil
				}
				got, made, err := flow.Until(context.Background(), step, func(s string) bool { return s == "ok" }, 3)
				if rule.stops {
					if err != failure || got != "" || made != 1 || runs.Load() != 1 {
						t.Errorf("Until = %q, %d, %v after %d runs; want the error as it was given after one attempt", got, made, err, runs.Load())
					}
					return
				}
				if err != nil || got != "ok" || made != 2 {
					t.Errorf("Until = %q, %d, %v; want the second attempt's answer", got, made, err)
				}
			})
		}
	}
}

func TestApproveAsksForAReviewOnlyWhenRecoveryRanOutAndNeverForAnotherKind(t *testing.T) {
	for _, rule := range kindRules {
		for i, failure := range failuresOf(rule.kind) {
			t.Run(fmt.Sprintf("%v/%d", rule.kind, i), func(t *testing.T) {
				var gate atomic.Int32
				approved := func(context.Context, string) (flow.ApprovalOutcome, error) {
					gate.Add(1)
					return flow.ApprovalOutcome{Approved: true}, nil
				}
				_, err := flow.Approve(context.Background(), fails[string](failure), approved, nil)
				if err != failure || gate.Load() != 0 {
					t.Errorf("Approve = %v, gate asked %d times; want the error as it was given and no gate", err, gate.Load())
				}
				if errors.Is(err, schemaflux.ErrReviewRequired) != (rule.kind == schemaflux.ErrReviewRequired) {
					t.Errorf("errors.Is(err, ErrReviewRequired) = %v for %v", errors.Is(err, schemaflux.ErrReviewRequired), rule.kind)
				}
			})
		}
	}
}

func TestApproveAsksForAReviewWhenTheStepsRecoveryRanOut(t *testing.T) {
	for _, internal := range []types.ErrorKind{types.KindRepairExhausted, types.KindInvariantViolation, types.KindEvidenceViolation, types.KindSchemaViolation} {
		t.Run(internal.String(), func(t *testing.T) {
			failure := &schemaflux.Error{Kind: schemaflux.ErrInvalidAnswer, Err: types.Newf(internal, "extract", "the answer failed")}
			_, err := flow.Approve(context.Background(), fails[string](failure),
				func(context.Context, string) (flow.ApprovalOutcome, error) {
					return flow.ApprovalOutcome{Approved: true}, nil
				}, nil)
			wantKind(t, err, schemaflux.ErrReviewRequired)
		})
	}
}

func TestVoteCountsAFailedSampleByItsKindAndSaysHowManyWereDefects(t *testing.T) {
	for _, rule := range kindRules {
		for i, failure := range failuresOf(rule.kind) {
			defects := 0
			if rule.kind == schemaflux.ErrInternal {
				defects = 1
			}
			t.Run(fmt.Sprintf("%v/%d one sample fails", rule.kind, i), func(t *testing.T) {
				var calls atomic.Int32
				step := func(context.Context) (string, error) {
					if calls.Add(1) == 1 {
						return "", failure
					}
					return "a", nil
				}
				got, record, err := flow.Vote(context.Background(), 3, step, flow.ExactAgreement[string](2), flow.VoteOptions{Concurrency: 1})
				if err != nil || got != "a" {
					t.Fatalf("Vote = %q, %v; a failed sample is not a reason to throw away the rest", got, err)
				}
				want := flow.VoteRecord{Requested: 3, Succeeded: 2, Failed: 1, Internal: defects, Agree: 2, AgreementRate: 1}
				if record != want {
					t.Errorf("record = %+v, want %+v", record, want)
				}
			})
			t.Run(fmt.Sprintf("%v/%d every sample fails", rule.kind, i), func(t *testing.T) {
				got, record, err := flow.Vote(context.Background(), 3, fails[string](failure), flow.ExactAgreement[string](2), flow.VoteOptions{Concurrency: 1})
				if err != failure || got != "" {
					t.Errorf("Vote = %q, %v; want the first sample's error as it was given", got, err)
				}
				want := flow.VoteRecord{Requested: 3, Failed: 3, Internal: 3 * defects}
				if record != want {
					t.Errorf("record = %+v, want %+v", record, want)
				}
			})
		}
	}
}

func TestVoteCountsASamplesPanicAsAFailedSampleThatWasADefect(t *testing.T) {
	var calls atomic.Int32
	step := func(context.Context) (string, error) {
		if calls.Add(1) == 2 {
			panic("a defect")
		}
		return "a", nil
	}
	got, record, err := flow.Vote(context.Background(), 4, step, flow.ExactAgreement[string](3), flow.VoteOptions{Concurrency: 1})
	if err != nil || got != "a" {
		t.Fatalf("Vote = %q, %v", got, err)
	}
	if record.Failed != 1 || record.Internal != 1 || record.Succeeded != 3 {
		t.Errorf("record = %+v, want one failed sample and it a defect", record)
	}
}

func TestVoteDoesNotCountAnErrorOfAnotherKindAsADefect(t *testing.T) {
	_, record, _ := flow.Vote(context.Background(), 2, fails[string](&schemaflux.Error{Kind: schemaflux.ErrUnavailable}),
		flow.ExactAgreement[string](1), flow.VoteOptions{Concurrency: 1})
	if record.Failed != 2 || record.Internal != 0 {
		t.Errorf("record = %+v", record)
	}
}

func TestCheckpointHandsAFailureOfAnyKindOnAsItWasGivenAndKeepsNothing(t *testing.T) {
	for _, rule := range kindRules {
		for i, failure := range failuresOf(rule.kind) {
			t.Run(fmt.Sprintf("%v/%d", rule.kind, i), func(t *testing.T) {
				store := newScriptedStore()
				got, outcome, err := flow.Checkpoint(context.Background(), store, "run", "step", "input", fails[string](failure))
				if err != failure || got != "" || outcome.Resumed {
					t.Errorf("Checkpoint = %q, %+v, %v; want the error as it was given", got, outcome, err)
				}
				if len(store.saved) != 0 {
					t.Errorf("a failed step was saved: %v", store.saved)
				}
			})
		}
	}
}

func TestTheSpeculativeCombinatorsHandAFailureOfAnyKindOnAsItWasGivenWhenItsBranchIsTheOneChosen(t *testing.T) {
	for _, rule := range kindRules {
		for i, failure := range failuresOf(rule.kind) {
			yes := func(context.Context) (string, error) { return "", failure }
			decideYes := func(context.Context) (bool, error) { return true, nil }
			decideLikely := func(context.Context) (string, error) { return "a", nil }
			t.Run(fmt.Sprintf("%v/%d BranchAhead", rule.kind, i), func(t *testing.T) {
				if _, err := flow.BranchAhead(context.Background(), decideYes, yes, returns("no")); err != failure {
					t.Errorf("BranchAhead = %v, want the very error the branch returned", err)
				}
			})
			t.Run(fmt.Sprintf("%v/%d BranchBoth", rule.kind, i), func(t *testing.T) {
				if _, err := flow.BranchBoth(context.Background(), decideYes, yes, returns("no")); err != failure {
					t.Errorf("BranchBoth = %v, want the very error the branch returned", err)
				}
			})
			t.Run(fmt.Sprintf("%v/%d RouteAhead", rule.kind, i), func(t *testing.T) {
				_, err := flow.RouteAhead(context.Background(), decideLikely, "a",
					schemaflux.To("a", yes), schemaflux.To("b", returns("other")))
				if err != failure {
					t.Errorf("RouteAhead = %v, want the very error the route returned", err)
				}
			})
		}
	}
}

func TestEscalateAndUntilDoNotTryAgainWhenTheCallersContextEndsAndSayWhichEndedIt(t *testing.T) {
	for _, done := range doneContexts() {
		t.Run(done.name+"/Escalate", func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			var stronger atomic.Int32
			first := func(context.Context) (string, error) {
				cancel(done.is)
				return "", done.is
			}
			_, record, err := flow.Escalate(ctx, first, counts(&stronger, "strong"), nil)
			if err == nil || stronger.Load() != 0 || record.Escalated {
				t.Errorf("Escalate = %v, %+v after %d stronger runs", err, record, stronger.Load())
			}
		})
		t.Run(done.name+"/Until", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var runs atomic.Int32
			step := func(context.Context) (string, error) {
				runs.Add(1)
				cancel()
				return "", done.is
			}
			_, made, err := flow.Until(ctx, step, func(string) bool { return true }, 5)
			if err == nil || runs.Load() != 1 || made != 1 {
				t.Errorf("Until = %d, %v after %d runs", made, err, runs.Load())
			}
		})
	}
}

func TestEscalateAndUntilStopAtAPlainCancellationButGoOnAfterAPlainDeadlineWhileTheContextIsLive(t *testing.T) {
	for _, failure := range []error{context.Canceled, fmt.Errorf("while waiting: %w", context.Canceled)} {
		t.Run(failure.Error(), func(t *testing.T) {
			var stronger atomic.Int32
			_, record, err := flow.Escalate(context.Background(), fails[string](failure), counts(&stronger, "strong"), nil)
			if err != failure || stronger.Load() != 0 || record.Escalated {
				t.Errorf("Escalate = %v, %+v after %d stronger runs", err, record, stronger.Load())
			}
			var runs atomic.Int32
			step := func(context.Context) (string, error) { runs.Add(1); return "", failure }
			_, made, err := flow.Until(context.Background(), step, func(string) bool { return true }, 3)
			if err != failure || made != 1 || runs.Load() != 1 {
				t.Errorf("Until = %d, %v after %d runs", made, err, runs.Load())
			}
		})
	}
	for _, failure := range []error{context.DeadlineExceeded, fmt.Errorf("while waiting: %w", context.DeadlineExceeded)} {
		t.Run(failure.Error(), func(t *testing.T) {
			var stronger atomic.Int32
			got, record, err := flow.Escalate(context.Background(), fails[string](failure), counts(&stronger, "strong"), nil)
			if err != nil || got != "strong" || stronger.Load() != 1 || !record.Escalated {
				t.Errorf("Escalate = %q, %v, %+v after %d stronger runs; a deadline of the step's own is no signal", got, err, record, stronger.Load())
			}
			var runs atomic.Int32
			step := func(context.Context) (string, error) {
				if runs.Add(1) == 1 {
					return "", failure
				}
				return "ok", nil
			}
			got, made, err := flow.Until(context.Background(), step, func(s string) bool { return s == "ok" }, 3)
			if err != nil || got != "ok" || made != 2 {
				t.Errorf("Until = %q, %d, %v", got, made, err)
			}
		})
	}
}

// TestATimeoutOfTheStepsOwnIsNoSignalWhileTheCallersContextIsLive and the test
// after it tell apart the two things a timeout can be: the step's own, which is
// treated as an unavailable provider is, and the caller's context having ended,
// which stops every combinator whatever the step returned.
func TestATimeoutOfTheStepsOwnIsNoSignalWhileTheCallersContextIsLive(t *testing.T) {
	timeout := &schemaflux.Error{Kind: schemaflux.ErrTimeout, Op: "extract"}
	var stronger atomic.Int32
	got, record, err := flow.Escalate(context.Background(), fails[string](timeout), counts(&stronger, "strong"), nil)
	if err != nil || got != "strong" || !record.Escalated || record.FirstError != error(timeout) {
		t.Errorf("Escalate = %q, %v, %+v", got, err, record)
	}
	var runs atomic.Int32
	step := func(context.Context) (string, error) {
		if runs.Add(1) < 3 {
			return "", timeout
		}
		return "ok", nil
	}
	got, made, err := flow.Until(context.Background(), step, func(s string) bool { return s == "ok" }, 5)
	if err != nil || got != "ok" || made != 3 {
		t.Errorf("Until = %q, %d, %v", got, made, err)
	}
	_, err = flow.Approve(context.Background(), fails[string](timeout),
		func(context.Context, string) (flow.ApprovalOutcome, error) {
			return flow.ApprovalOutcome{Approved: true}, nil
		}, nil)
	if err != error(timeout) {
		t.Errorf("Approve = %v, want the step's error as it was given", err)
	}
}

func TestTheSameTimeoutStopsEverythingOnceTheCallersContextHasEnded(t *testing.T) {
	timeout := &schemaflux.Error{Kind: schemaflux.ErrTimeout, Op: "extract"}
	ends := []struct {
		name string
		make func() (context.Context, context.CancelFunc)
		kind schemaflux.Kind
	}{
		{"cancelled", func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, schemaflux.ErrCanceled},
		{"past its deadline", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), time.Second)
		}, schemaflux.ErrTimeout},
	}
	for _, end := range ends {
		// A step that fails with the timeout at the moment the context ends.
		failing := func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", timeout
		}
		t.Run(end.name+"/Escalate", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := end.make()
				defer cancel()
				if end.kind == schemaflux.ErrCanceled {
					time.AfterFunc(time.Second, cancel)
				}
				var stronger atomic.Int32
				_, record, err := flow.Escalate(ctx, failing, counts(&stronger, "strong"), nil)
				wantKind(t, err, end.kind)
				if stronger.Load() != 0 || record.Escalated {
					t.Errorf("the stronger step ran %d times after the context ended: %+v", stronger.Load(), record)
				}
			})
		})
		t.Run(end.name+"/Until", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := end.make()
				defer cancel()
				if end.kind == schemaflux.ErrCanceled {
					time.AfterFunc(time.Second, cancel)
				}
				var runs atomic.Int32
				step := func(ctx context.Context) (string, error) { runs.Add(1); return failing(ctx) }
				_, made, err := flow.Until(ctx, step, func(string) bool { return true }, 5)
				wantKind(t, err, end.kind)
				if runs.Load() != 1 || made != 1 {
					t.Errorf("the step ran %d times and %d attempts were made after the context ended", runs.Load(), made)
				}
			})
		})
	}
}

// TestThePackageCommentTableNamesEveryKindAndTheCombinatorsItDescribes holds the
// table of the package comment to the table of this file: a kind or a combinator
// that the comment does not name is one it does not say what happens to.
func TestThePackageCommentTableNamesEveryKindAndTheCombinatorsItDescribes(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "doc.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	doc := file.Doc.Text()
	for _, rule := range kindRules {
		name := "Err"
		for _, word := range strings.Fields(rule.kind.Error()) {
			name += strings.ToUpper(word[:1]) + word[1:]
		}
		if !strings.Contains(doc, name) {
			t.Errorf("the package comment does not name %s", name)
		}
	}
	for _, word := range []string{"Escalate", "Until", "Approve", "Vote", "Checkpoint", "BranchAhead", "BranchBoth", "RouteAhead", "VoteRecord.Internal"} {
		if !strings.Contains(doc, word) {
			t.Errorf("the package comment does not name %s", word)
		}
	}
}

func TestVoteStoppedByItsContextHasAnEmptyRecordEvenWhenASampleHadPanickedBefore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	step := func(ctx context.Context) (string, error) {
		if calls.Add(1) == 1 {
			panic("a defect")
		}
		cancel()
		return "", ctx.Err()
	}
	got, record, err := flow.Vote(ctx, 3, step, flow.ExactAgreement[string](1), flow.VoteOptions{Concurrency: 1})
	wantKind(t, err, schemaflux.ErrCanceled)
	if got != "" || record != (flow.VoteRecord{}) {
		t.Errorf("Vote = %q, %+v; a run that was stopped did not finish, and its record is empty", got, record)
	}
}
