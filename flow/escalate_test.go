package flow_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// terminalKinds are the kinds whose failure a stronger model would repeat, or that
// no model can repair (a defect), so that Escalate returns them and does not spend
// money on the second step.
var terminalKinds = []schemaflux.Kind{
	schemaflux.ErrConfiguration, schemaflux.ErrInvalidRequest, schemaflux.ErrUnauthorized, schemaflux.ErrPolicy,
	schemaflux.ErrRefused, schemaflux.ErrBudgetExceeded, schemaflux.ErrUnsupported, schemaflux.ErrCanceled,
	schemaflux.ErrClosed, schemaflux.ErrReviewRequired, schemaflux.ErrInternal,
}

// retryableKinds are the kinds a different route could plausibly survive.
var retryableKinds = []schemaflux.Kind{
	schemaflux.ErrRateLimited, schemaflux.ErrUnavailable, schemaflux.ErrTimeout, schemaflux.ErrBusy,
	schemaflux.ErrInvalidAnswer, schemaflux.ErrTooLarge, schemaflux.ErrTruncated,
}

func TestEscalateReturnsTheFirstAnswerAndRunsNothingElseWhenItIsAccepted(t *testing.T) {
	accept := func(s string) bool { return s == "good" }
	cases := []struct {
		name   string
		accept func(string) bool
	}{
		{"with no accept function", nil},
		{"with an accept function that says yes", accept},
		{"with an accept function that always says yes", func(string) bool { return true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var first, stronger atomic.Int32
			got, record, err := flow.Escalate(context.Background(), counts(&first, "good"), counts(&stronger, "better"), tc.accept)
			if err != nil || got != "good" {
				t.Fatalf("Escalate = %q, %v", got, err)
			}
			if record != (flow.EscalationRecord{}) || first.Load() != 1 || stronger.Load() != 0 {
				t.Errorf("record = %+v, first ran %d times, stronger %d", record, first.Load(), stronger.Load())
			}
		})
	}
}

func TestEscalateRunsTheStrongerStepWhenTheAnswerIsRejected(t *testing.T) {
	var firstRuns, strongerRuns atomic.Int32
	var accepted []string
	got, record, err := flow.Escalate(context.Background(), counts(&firstRuns, "weak"), counts(&strongerRuns, "strong"), func(s string) bool {
		accepted = append(accepted, s)
		return s == "strong"
	})
	if err != nil || got != "strong" {
		t.Fatalf("Escalate = %q, %v", got, err)
	}
	want := flow.EscalationRecord{Escalated: true, Reason: "rejected"}
	if record != want {
		t.Errorf("record = %+v, want %+v", record, want)
	}
	if firstRuns.Load() != 1 || strongerRuns.Load() != 1 || len(accepted) != 1 || accepted[0] != "weak" {
		t.Errorf("first ran %d, stronger %d, accept saw %v; want one run each and accept called once, with the first answer", firstRuns.Load(), strongerRuns.Load(), accepted)
	}
}

func TestEscalateTrustsTheStrongerAnswerWithoutAskingAcceptAgain(t *testing.T) {
	var asked atomic.Int32
	got, _, err := flow.Escalate(context.Background(), returns(1), returns(2), func(int) bool {
		asked.Add(1)
		return false
	})
	if err != nil || got != 2 || asked.Load() != 1 {
		t.Errorf("Escalate = %d, %v, accept called %d times; the stronger answer is the result whatever accept would say", got, err, asked.Load())
	}
}

func TestEscalateRunsTheStrongerStepWhenTheFirstFailsInAWayAnotherRouteCouldSurvive(t *testing.T) {
	failures := []error{errStep, fmt.Errorf("wrapped: %w", errStep)}
	for _, kind := range retryableKinds {
		failures = append(failures, &schemaflux.Error{Kind: kind, Op: "extract"})
	}
	for _, failure := range failures {
		name := fmt.Sprintf("%v", failure)
		t.Run(name, func(t *testing.T) {
			var stronger atomic.Int32
			got, record, err := flow.Escalate(context.Background(), fails[string](failure), counts(&stronger, "strong"), nil)
			if err != nil || got != "strong" || stronger.Load() != 1 {
				t.Fatalf("Escalate = %q, %v; stronger ran %d times", got, err, stronger.Load())
			}
			if !record.Escalated || record.Reason != "error" || record.FirstError != failure {
				t.Errorf("record = %+v; want Escalated, Reason error and the first step's error as it was given", record)
			}
		})
	}
}

func TestEscalateReturnsATerminalFailureAsItWasGivenAndDoesNotEscalate(t *testing.T) {
	for _, kind := range terminalKinds {
		t.Run(kind.Error(), func(t *testing.T) {
			failure := &schemaflux.Error{Kind: kind, Op: "extract"}
			var stronger atomic.Int32
			got, record, err := flow.Escalate(context.Background(), fails[string](failure), counts(&stronger, "strong"), nil)
			if err != error(failure) || got != "" {
				t.Fatalf("Escalate = %q, %v; want the first step's error as it was given", got, err)
			}
			if record.Escalated || record.FirstError != error(failure) || stronger.Load() != 0 {
				t.Errorf("record = %+v, stronger ran %d times", record, stronger.Load())
			}
		})
	}
}

func TestEscalateReadsTheKindFromTheErrorsBeneathAnErrorToo(t *testing.T) {
	budget := &schemaflux.Error{Kind: schemaflux.ErrBudgetExceeded, Err: types.Newf(types.KindBudgetExceeded, "extract", "the budget is spent")}
	unavailable := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Err: types.Newf(types.KindProviderUnavailable, "extract", "down")}
	cases := []struct {
		name     string
		failure  error
		escalate bool
	}{
		{"a budget error holding the internal error", budget, false},
		{"a budget error wrapped by the caller", fmt.Errorf("while extracting: %w", budget), false},
		{"an unavailable error holding the internal error", unavailable, true},
		{"a context error", context.Canceled, false},
		{"a deadline error", context.DeadlineExceeded, true},
		{"an error nothing can read", errStep, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stronger atomic.Int32
			_, record, err := flow.Escalate(context.Background(), fails[string](tc.failure), counts(&stronger, "strong"), nil)
			if tc.escalate != (stronger.Load() == 1) || record.Escalated != tc.escalate {
				t.Errorf("stronger ran %d times, record %+v; escalate = %v", stronger.Load(), record, tc.escalate)
			}
			if !tc.escalate && err != tc.failure {
				t.Errorf("err = %v, want the very value %v", err, tc.failure)
			}
		})
	}
}

func TestEscalateReturnsTheStrongerStepsErrorAsItWasGivenWhenBothFail(t *testing.T) {
	first := errors.New("the cheap route failed")
	stronger := fmt.Errorf("the expensive route failed: %w", errStep)
	got, record, err := flow.Escalate(context.Background(), fails[string](first), fails[string](stronger), nil)
	if err != stronger || got != "" {
		t.Errorf("Escalate = %q, %v; want the stronger step's error as it was given", got, err)
	}
	if !record.Escalated || record.Reason != "error" || record.FirstError != first {
		t.Errorf("record = %+v; the cheap route's error is kept in FirstError", record)
	}
}

func TestEscalateReturnsTheStrongerStepsErrorWhenItFailsAfterARejection(t *testing.T) {
	got, record, err := flow.Escalate(context.Background(), returns("weak"), fails[string](errStep), func(string) bool { return false })
	if err != errStep || got != "" {
		t.Errorf("Escalate = %q, %v", got, err)
	}
	if !record.Escalated || record.Reason != "rejected" || record.FirstError != nil {
		t.Errorf("record = %+v", record)
	}
}

func TestEscalateRefusesANilStepAndRunsNothing(t *testing.T) {
	var ran atomic.Int32
	cases := []struct {
		name            string
		first, stronger schemaflux.Step[string]
	}{
		{"no first step", nil, counts(&ran, "b")},
		{"no stronger step", counts(&ran, "a"), nil},
		{"neither step", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, record, err := flow.Escalate(context.Background(), tc.first, tc.stronger, nil)
			e := wantKind(t, err, schemaflux.ErrInvalidRequest)
			if e.Op != "flow.escalate" || got != "" || record != (flow.EscalationRecord{}) || ran.Load() != 0 {
				t.Errorf("Op = %q, got = %q, record = %+v, steps run = %d", e.Op, got, record, ran.Load())
			}
		})
	}
}

func TestEscalateRefusesANilContextAndAContextThatIsOverAndRunsNothing(t *testing.T) {
	var ran atomic.Int32
	var nilContext context.Context
	_, _, err := flow.Escalate(nilContext, counts(&ran, "a"), counts(&ran, "b"), nil)
	wantKind(t, err, schemaflux.ErrInvalidRequest)
	for _, done := range doneContexts() {
		got, record, err := flow.Escalate(done.ctx, counts(&ran, "a"), counts(&ran, "b"), nil)
		wantKind(t, err, done.kind)
		if !errors.Is(err, done.is) || got != "" || record != (flow.EscalationRecord{}) {
			t.Errorf("%s: err = %v, got = %q, record = %+v", done.name, err, got, record)
		}
	}
	if ran.Load() != 0 {
		t.Errorf("%d steps ran", ran.Load())
	}
}

func TestEscalateDoesNotRunTheStrongerStepWhenTheContextEndsBetweenTheTwo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stronger atomic.Int32
	first := func(context.Context) (string, error) {
		cancel()
		return "weak", nil
	}
	got, record, err := flow.Escalate(ctx, first, counts(&stronger, "strong"), func(string) bool { return false })
	wantKind(t, err, schemaflux.ErrCanceled)
	if !errors.Is(err, context.Canceled) || got != "" || stronger.Load() != 0 {
		t.Errorf("err = %v, got = %q, stronger ran %d times", err, got, stronger.Load())
	}
	if record.Escalated || record.Reason != "rejected" {
		t.Errorf("record = %+v; the stronger step was called for and did not run", record)
	}
}

func TestEscalateHandsBothStepsTheCallersContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), contextKey{}, "mine")
	read := func(ctx context.Context) (string, error) { return fmt.Sprint(ctx.Value(contextKey{})), nil }
	got, _, err := flow.Escalate(ctx, read, read, func(s string) bool { return false })
	if err != nil || got != "mine" {
		t.Errorf("Escalate = %q, %v", got, err)
	}
}

func TestEscalateRunsTheStepsOnTheCallersGoroutineSoAPanicReachesTheCaller(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != "from the first step" {
			t.Errorf("recovered %v", recovered)
		}
	}()
	_, _, _ = flow.Escalate(context.Background(), panics[string]("from the first step"), returns("b"), nil)
	t.Error("Escalate returned after a panic in a step")
}

func TestEscalateLeavesNoGoroutineBehind(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		leaks := noLeak(t)
		_, _, _ = flow.Escalate(context.Background(), wait(time.Second, "a"), wait(time.Second, "b"), func(string) bool { return false })
		_, _, _ = flow.Escalate(context.Background(), fails[string](errStep), wait(time.Second, "b"), nil)
		leaks()
	})
}

func TestEscalateNestsAndKeepsTheInnermostErrorAsItWasGiven(t *testing.T) {
	inner := func(ctx context.Context) (string, error) {
		v, _, err := flow.Escalate(ctx, fails[string](errStep), fails[string](errStep), nil)
		return v, err
	}
	_, _, err := flow.Escalate(context.Background(), returns("weak"), inner, func(string) bool { return false })
	if err != errStep {
		t.Errorf("err = %v, want the innermost error as it was given", err)
	}
}

func TestUntilReturnsTheFirstAnswerThatSatisfiesTheConditionAndHowManyAttemptsItTook(t *testing.T) {
	for attempt := 1; attempt <= 6; attempt++ {
		t.Run(fmt.Sprint("on attempt ", attempt), func(t *testing.T) {
			var runs atomic.Int32
			step := func(context.Context) (int, error) { return int(runs.Add(1)), nil }
			got, made, err := flow.Until(context.Background(), step, func(n int) bool { return n >= attempt }, 6)
			if err != nil || got != attempt || made != attempt || runs.Load() != int32(attempt) {
				t.Errorf("Until = %d, %d, %v after %d runs; want %d, %d, nil", got, made, err, runs.Load(), attempt, attempt)
			}
		})
	}
}

func TestUntilFailsWhenNoAttemptSatisfiesTheConditionAndHandsBackNoAnswer(t *testing.T) {
	for _, attempts := range []int{1, 2, 3, 5} {
		t.Run(fmt.Sprint(attempts, " attempts"), func(t *testing.T) {
			var runs atomic.Int32
			got, made, err := flow.Until(context.Background(), counts(&runs, "draft"), func(string) bool { return false }, attempts)
			e := wantKind(t, err, schemaflux.ErrInvalidAnswer)
			if e.Op != "flow.until" || got != "" || made != attempts || runs.Load() != int32(attempts) {
				t.Errorf("Op = %q, got = %q, attempts made = %d, runs = %d", e.Op, got, made, runs.Load())
			}
		})
	}
}

func TestUntilTriesAgainAfterAFailureThatIsNotTerminal(t *testing.T) {
	var runs atomic.Int32
	step := func(context.Context) (string, error) {
		if runs.Add(1) < 3 {
			return "", &schemaflux.Error{Kind: schemaflux.ErrUnavailable}
		}
		return "ok", nil
	}
	got, made, err := flow.Until(context.Background(), step, func(s string) bool { return s == "ok" }, 5)
	if err != nil || got != "ok" || made != 3 {
		t.Errorf("Until = %q, %d, %v", got, made, err)
	}
}

func TestUntilStopsAtATerminalFailureAndReturnsItAsItWasGiven(t *testing.T) {
	for _, kind := range terminalKinds {
		t.Run(kind.Error(), func(t *testing.T) {
			failure := &schemaflux.Error{Kind: kind, Op: "extract"}
			var runs atomic.Int32
			step := func(context.Context) (string, error) {
				runs.Add(1)
				return "", failure
			}
			got, made, err := flow.Until(context.Background(), step, func(string) bool { return true }, 5)
			if err != error(failure) || got != "" || made != 1 || runs.Load() != 1 {
				t.Errorf("Until = %q, %d, %v after %d runs; want the error as it was given after one attempt", got, made, err, runs.Load())
			}
		})
	}
}

func TestUntilReturnsTheLastAttemptsErrorAsItWasGivenWhenEveryAttemptFailed(t *testing.T) {
	var runs atomic.Int32
	errs := []error{errors.New("first"), errors.New("second"), fmt.Errorf("third: %w", errStep)}
	step := func(context.Context) (string, error) { return "", errs[runs.Add(1)-1] }
	got, made, err := flow.Until(context.Background(), step, func(string) bool { return true }, 3)
	if err != errs[2] || got != "" || made != 3 {
		t.Errorf("Until = %q, %d, %v; want the last attempt's error as it was given", got, made, err)
	}
}

func TestUntilReturnsNothingBesideAnError(t *testing.T) {
	type draft struct{ Words int }
	step := func(context.Context) (draft, error) { return draft{Words: 400}, nil }
	got, _, err := flow.Until(context.Background(), step, func(d draft) bool { return d.Words < 200 }, 2)
	if err == nil || got != (draft{}) {
		t.Errorf("Until = %+v, %v; the rejected answer is not handed back", got, err)
	}
}

func TestUntilRefusesAnArgumentItCannotUseAndRunsNothing(t *testing.T) {
	var ran atomic.Int32
	always := func(string) bool { return true }
	cases := []struct {
		name     string
		step     schemaflux.Step[string]
		pred     func(string) bool
		attempts int
	}{
		{"no step", nil, always, 3},
		{"no condition", counts(&ran, "a"), nil, 3},
		{"no attempts", counts(&ran, "a"), always, 0},
		{"a negative number of attempts", counts(&ran, "a"), always, -2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, made, err := flow.Until(context.Background(), tc.step, tc.pred, tc.attempts)
			e := wantKind(t, err, schemaflux.ErrInvalidRequest)
			if e.Op != "flow.until" || got != "" || made != 0 || ran.Load() != 0 {
				t.Errorf("Op = %q, got = %q, attempts made = %d, steps run = %d", e.Op, got, made, ran.Load())
			}
		})
	}
}

func TestUntilRefusesANilContextAndAContextThatIsOverAndRunsNothing(t *testing.T) {
	var ran atomic.Int32
	var nilContext context.Context
	_, _, err := flow.Until(nilContext, counts(&ran, "a"), func(string) bool { return true }, 3)
	wantKind(t, err, schemaflux.ErrInvalidRequest)
	for _, done := range doneContexts() {
		_, made, err := flow.Until(done.ctx, counts(&ran, "a"), func(string) bool { return true }, 3)
		wantKind(t, err, done.kind)
		if !errors.Is(err, done.is) || made != 0 {
			t.Errorf("%s: err = %v, attempts made = %d", done.name, err, made)
		}
	}
	if ran.Load() != 0 {
		t.Errorf("%d steps ran", ran.Load())
	}
}

func TestUntilStopsWhenTheContextEndsBetweenAttemptsAndSaysHowManyWereMade(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var runs atomic.Int32
	step := func(context.Context) (int, error) {
		if runs.Add(1) == 2 {
			cancel()
		}
		return 0, nil
	}
	got, made, err := flow.Until(ctx, step, func(int) bool { return false }, 10)
	wantKind(t, err, schemaflux.ErrCanceled)
	if !errors.Is(err, context.Canceled) || got != 0 || made != 2 || runs.Load() != 2 {
		t.Errorf("err = %v, got = %d, attempts made = %d, runs = %d", err, got, made, runs.Load())
	}
}

func TestUntilCallsTheConditionOnceForEachAnswerAndNeverForAFailure(t *testing.T) {
	var runs, checked atomic.Int32
	step := func(context.Context) (int, error) {
		if runs.Add(1) == 2 {
			return 0, errStep
		}
		return int(runs.Load()), nil
	}
	_, made, err := flow.Until(context.Background(), step, func(n int) bool {
		checked.Add(1)
		return n == 4
	}, 4)
	if err != nil || made != 4 || checked.Load() != 3 {
		t.Errorf("attempts = %d, err = %v, condition called %d times; want 3 calls for 3 answers", made, err, checked.Load())
	}
}

func TestUntilRunsTheStepOnTheCallersGoroutineSoAPanicReachesTheCaller(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != "from the step" {
			t.Errorf("recovered %v", recovered)
		}
	}()
	_, _, _ = flow.Until(context.Background(), panics[string]("from the step"), func(string) bool { return true }, 2)
	t.Error("Until returned after a panic in the step")
}

func TestUntilLeavesNoGoroutineBehind(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		leaks := noLeak(t)
		_, _, _ = flow.Until(context.Background(), wait(time.Second, 1), func(int) bool { return false }, 3)
		_, _, _ = flow.Until(context.Background(), fails[int](errStep), func(int) bool { return true }, 3)
		leaks()
	})
}

func TestUntilNestsAndKeepsTheInnermostErrorAsItWasGiven(t *testing.T) {
	inner := func(ctx context.Context) (string, error) {
		v, _, err := flow.Until(ctx, fails[string](&schemaflux.Error{Kind: schemaflux.ErrPolicy}), func(string) bool { return true }, 2)
		return v, err
	}
	_, _, err := flow.Until(context.Background(), inner, func(string) bool { return true }, 3)
	var e *schemaflux.Error
	if !errors.As(err, &e) || e.Kind != schemaflux.ErrPolicy || e.Op != "" {
		t.Errorf("err = %v; want the innermost *Error, untouched", err)
	}
}
