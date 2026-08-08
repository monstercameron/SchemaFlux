package ops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// The combinators exist because callers were writing these shapes by hand, and
// the hand-written versions failed open: a retry loop that returns its last
// rejected answer as a success, an escalation that pays for a stronger model
// after a failure no model could survive.

func constantAnswer(value string) Step[string] {
	return func(context.Context) (string, error) { return value, nil }
}

func failingAnswer(err error) Step[string] {
	return func(context.Context) (string, error) { return "", err }
}

func answeringStep(answers ...string) (Step[string], *int) {
	calls := 0
	return func(context.Context) (string, error) {
		calls++
		if calls <= len(answers) {
			return answers[calls-1], nil
		}
		return answers[len(answers)-1], nil
	}, &calls
}

func TestEscalateKeepsAnAcceptedFirstAnswer(t *testing.T) {
	stronger, strongerCalls := answeringStep("expensive")

	value, record, err := Escalate(context.Background(),
		constantAnswer("cheap"), stronger,
		func(s string) bool { return s == "cheap" })

	if err != nil {
		t.Fatalf("Escalate: %v", err)
	}
	if value != "cheap" {
		t.Errorf("value = %q, want the accepted first answer", value)
	}
	if record.Escalated {
		t.Error("escalated despite the first answer being accepted")
	}
	if *strongerCalls != 0 {
		t.Errorf("the stronger step ran %d times; that is real money", *strongerCalls)
	}
}

func TestEscalateRunsTheStrongerStepWhenTheAnswerIsRejected(t *testing.T) {
	value, record, err := Escalate(context.Background(),
		constantAnswer("cheap"), constantAnswer("expensive"),
		func(s string) bool { return s == "good enough" })

	if err != nil {
		t.Fatalf("Escalate: %v", err)
	}
	if value != "expensive" {
		t.Errorf("value = %q", value)
	}
	if !record.Escalated || record.Reason != "rejected" {
		t.Errorf("record = %+v, want an escalation reasoned %q", record, "rejected")
	}
	if record.FirstError != nil {
		t.Errorf("a rejected-but-successful first step reported an error: %v", record.FirstError)
	}
}

func TestEscalateRunsTheStrongerStepAfterARetryableFailure(t *testing.T) {
	first := failingAnswer(&types.OperationError{Kind: types.KindProviderUnavailable, Message: "down"})

	value, record, err := Escalate(context.Background(), first, constantAnswer("expensive"), nil)
	if err != nil {
		t.Fatalf("Escalate: %v", err)
	}
	if value != "expensive" {
		t.Errorf("value = %q", value)
	}
	if record.Reason != "error" {
		t.Errorf("Reason = %q, want %q", record.Reason, "error")
	}
	if record.FirstError == nil {
		t.Error("the first failure was discarded; a caller tuning routes cannot see what the cheap route said")
	}
}

// The money test. A request the provider refused as malformed fails the same
// way on a stronger model, so escalating spends real money to buy nothing.
func TestEscalateDoesNotEscalateATerminalFailure(t *testing.T) {
	terminal := []types.ErrorKind{
		types.KindInvalidRequest,
		types.KindPolicyViolation,
		types.KindBudgetExceeded,
		types.KindAuthentication,
	}

	for _, kind := range terminal {
		t.Run(kind.String(), func(t *testing.T) {
			stronger, strongerCalls := answeringStep("expensive")

			_, record, err := Escalate(context.Background(),
				failingAnswer(&types.OperationError{Kind: kind, Message: "no"}),
				stronger, nil)

			if err == nil {
				t.Fatal("a terminal failure was reported as success")
			}
			if record.Escalated {
				t.Error("escalated a terminal failure")
			}
			if *strongerCalls != 0 {
				t.Errorf("the stronger step ran %d times on a failure no model survives", *strongerCalls)
			}

			var opErr *types.OperationError
			if !errors.As(err, &opErr) || opErr.Kind != kind {
				t.Errorf("the classification was lost on the way out: %v", err)
			}
		})
	}
}

func TestEscalateReportsBothFailuresWhenNeitherRouteWorks(t *testing.T) {
	first := failingAnswer(&types.OperationError{Kind: types.KindTimeout, Message: "first timed out"})
	second := failingAnswer(&types.OperationError{Kind: types.KindTimeout, Message: "second timed out"})

	_, _, err := Escalate(context.Background(), first, second, nil)
	if err == nil {
		t.Fatal("both routes failed and the call succeeded")
	}
	if !strings.Contains(err.Error(), "first timed out") || !strings.Contains(err.Error(), "second timed out") {
		t.Errorf("the error does not carry both failures: %v", err)
	}
}

func TestEscalateRejectsMissingSteps(t *testing.T) {
	if _, _, err := Escalate(context.Background(), nil, constantAnswer("x"), nil); err == nil {
		t.Error("a nil first step was accepted")
	}
	if _, _, err := Escalate[string](context.Background(), constantAnswer("x"), nil, nil); err == nil {
		t.Error("a nil stronger step was accepted")
	}
}

func TestEscalateHonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	step, calls := answeringStep("never")
	if _, _, err := Escalate(ctx, step, constantAnswer("x"), nil); err == nil {
		t.Error("a cancelled context still ran the step")
	}
	if *calls != 0 {
		t.Errorf("the step ran %d times under a cancelled context", *calls)
	}
}

func TestFallbackIsEscalateWithNoAcceptFunction(t *testing.T) {
	value, err := Fallback(context.Background(),
		failingAnswer(&types.OperationError{Kind: types.KindProviderUnavailable}),
		constantAnswer("alternate"))
	if err != nil {
		t.Fatalf("Fallback: %v", err)
	}
	if value != "alternate" {
		t.Errorf("value = %q", value)
	}

	// A successful primary is kept, whatever its content -- Fallback has no
	// opinion about quality, which is what distinguishes it from Escalate.
	value, err = Fallback(context.Background(), constantAnswer("poor but successful"), constantAnswer("alternate"))
	if err != nil {
		t.Fatalf("Fallback: %v", err)
	}
	if value != "poor but successful" {
		t.Errorf("value = %q; Fallback second-guessed a successful primary", value)
	}
}

func TestUntilReturnsTheFirstSatisfyingAnswer(t *testing.T) {
	step, calls := answeringStep("no", "no", "yes")

	value, attempts, err := Until(context.Background(), step,
		func(s string) bool { return s == "yes" }, 5)

	if err != nil {
		t.Fatalf("Until: %v", err)
	}
	if value != "yes" {
		t.Errorf("value = %q", value)
	}
	if attempts != 3 || *calls != 3 {
		t.Errorf("attempts = %d, calls = %d, want 3 and 3", attempts, *calls)
	}
}

// The fail-open this replaces: returning the last answer with no error means
// the caller who wrote the predicate is the one who never finds out it was
// violated.
func TestUntilExhaustedIsAnError(t *testing.T) {
	step, calls := answeringStep("never good")

	value, attempts, err := Until(context.Background(), step,
		func(string) bool { return false }, 4)

	if err == nil {
		t.Fatal("an unsatisfied condition returned success")
	}
	if attempts != 4 || *calls != 4 {
		t.Errorf("attempts = %d, calls = %d, want 4 and 4", attempts, *calls)
	}
	if value != "never good" {
		t.Errorf("the rejected answer was not returned for inspection: %q", value)
	}

	var opErr *types.OperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("exhaustion is not classified: %v", err)
	}
	if opErr.Kind != types.KindRepairExhausted {
		t.Errorf("Kind = %v, want KindRepairExhausted", opErr.Kind)
	}
}

func TestUntilStopsOnATerminalFailure(t *testing.T) {
	calls := 0
	step := func(context.Context) (string, error) {
		calls++
		return "", &types.OperationError{Kind: types.KindPolicyViolation, Message: "refused"}
	}

	_, attempts, err := Until(context.Background(), step, func(string) bool { return true }, 10)
	if err == nil {
		t.Fatal("a policy refusal was retried into a success")
	}
	if calls != 1 || attempts != 1 {
		t.Errorf("calls = %d, attempts = %d; a terminal failure was retried", calls, attempts)
	}
}

func TestUntilRetriesARetryableFailure(t *testing.T) {
	calls := 0
	step := func(context.Context) (string, error) {
		calls++
		if calls < 3 {
			return "", &types.OperationError{Kind: types.KindTimeout}
		}
		return "recovered", nil
	}

	value, attempts, err := Until(context.Background(), step, func(string) bool { return true }, 5)
	if err != nil {
		t.Fatalf("Until: %v", err)
	}
	if value != "recovered" || attempts != 3 {
		t.Errorf("value = %q, attempts = %d", value, attempts)
	}
}

func TestUntilRejectsArgumentsThatWouldSkipTheCheck(t *testing.T) {
	cases := []struct {
		name string
		step Step[string]
		pred func(string) bool
		max  int
	}{
		{"no step", nil, func(string) bool { return true }, 3},
		{"no predicate", constantAnswer("x"), nil, 3},
		{"zero attempts", constantAnswer("x"), func(string) bool { return true }, 0},
		{"negative attempts", constantAnswer("x"), func(string) bool { return true }, -1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := Until(context.Background(), tc.step, tc.pred, tc.max); err == nil {
				t.Errorf("%s was accepted", tc.name)
			}
		})
	}
}

func TestUntilHonoursCancellationBetweenAttempts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	calls := 0
	step := func(context.Context) (string, error) {
		calls++
		if calls == 2 {
			cancel()
		}
		return "not yet", nil
	}

	_, _, err := Until(ctx, step, func(string) bool { return false }, 10)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if calls > 3 {
		t.Errorf("the loop ran %d times after cancellation", calls)
	}
}

// An error this library has never seen gets the second route rather than being
// ruled terminal by a classifier that did not recognise it.
func TestAnUnclassifiableErrorIsNotTreatedAsTerminal(t *testing.T) {
	stronger, strongerCalls := answeringStep("expensive")

	_, record, err := Escalate(context.Background(),
		failingAnswer(errors.New("something nobody has classified")),
		stronger, nil)

	if err != nil {
		t.Fatalf("Escalate: %v", err)
	}
	if !record.Escalated || *strongerCalls != 1 {
		t.Errorf("an unclassified error was ruled terminal: escalated=%v calls=%d",
			record.Escalated, *strongerCalls)
	}
}
