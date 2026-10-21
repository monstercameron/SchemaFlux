package ops

import (
	"context"
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Every failure of a combinator's own has a kind, with the text it had before
// there were kinds and the sentinel its kind stands for; a context that is over
// is still the context's error to errors.Is.
func TestCombinatorsOwnFailuresHaveKinds(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, expire := context.WithTimeout(context.Background(), 0)
	defer expire()
	ok := constantAnswer("ok")
	accept := func(string) bool { return true }
	cases := []struct {
		name     string
		run      func() error
		text     string
		kind     types.ErrorKind
		sentinel error
		also     error
	}{
		{"Escalate with no first step", func() error { _, _, err := Escalate(context.Background(), nil, ok, nil); return err },
			"escalate: no first step to run", types.KindConfiguration, types.ErrConfiguration, nil},
		{"Escalate with no stronger step", func() error { _, _, err := Escalate(context.Background(), ok, nil, nil); return err },
			"escalate: no stronger step to escalate to", types.KindConfiguration, types.ErrConfiguration, nil},
		{"Escalate with a cancelled context", func() error { _, _, err := Escalate(cancelled, ok, ok, nil); return err },
			"context canceled", types.KindCanceled, types.ErrCanceled, context.Canceled},
		{"Escalate with a context past its deadline", func() error { _, _, err := Escalate(expired, ok, ok, nil); return err },
			"context deadline exceeded", types.KindTimeout, types.ErrTimeout, context.DeadlineExceeded},
		{"Escalate whose context ends while the first step runs", func() error {
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			_, _, err := Escalate(ctx, func(context.Context) (string, error) {
				stop()
				return "weak", nil
			}, ok, func(string) bool { return false })
			return err
		}, "context canceled", types.KindCanceled, types.ErrCanceled, context.Canceled},
		{"Fallback with no primary step", func() error { _, err := Fallback(context.Background(), nil, ok); return err },
			"fallback: no primary step to run", types.KindConfiguration, types.ErrConfiguration, nil},
		{"Fallback with no alternate step", func() error { _, err := Fallback(context.Background(), ok, nil); return err },
			"fallback: no alternate step to fall back to", types.KindConfiguration, types.ErrConfiguration, nil},
		{"Fallback with a cancelled context", func() error { _, err := Fallback(cancelled, ok, ok); return err },
			"context canceled", types.KindCanceled, types.ErrCanceled, context.Canceled},
		{"Until with no step", func() error { _, _, err := Until(context.Background(), nil, accept, 1); return err },
			"until: no step to run", types.KindConfiguration, types.ErrConfiguration, nil},
		{"Until with no predicate", func() error { _, _, err := Until(context.Background(), ok, nil, 1); return err },
			"until: no predicate, so there is no condition to run until", types.KindConfiguration, types.ErrConfiguration, nil},
		{"Until with no attempts", func() error { _, _, err := Until(context.Background(), ok, accept, 0); return err },
			"until: 0 attempts is not a number of attempts", types.KindConfiguration, types.ErrConfiguration, nil},
		{"Until with a negative number of attempts", func() error { _, _, err := Until(context.Background(), ok, accept, -3); return err },
			"until: -3 attempts is not a number of attempts", types.KindConfiguration, types.ErrConfiguration, nil},
		{"Until with a cancelled context", func() error { _, _, err := Until(cancelled, ok, accept, 2); return err },
			"context canceled", types.KindCanceled, types.ErrCanceled, context.Canceled},
		{"Until whose condition is never met", func() error {
			_, _, err := Until(context.Background(), ok, func(string) bool { return false }, 3)
			return err
		}, "until: repair exhausted: the condition was not satisfied in 3 attempts", types.KindRepairExhausted, types.ErrRepairExhausted, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("no error")
			}
			if err.Error() != tc.text {
				t.Errorf("text = %q, want %q", err.Error(), tc.text)
			}
			if kind := types.KindOf(err); kind != tc.kind {
				t.Errorf("kind = %v, want %v", kind, tc.kind)
			}
			if !errors.Is(err, tc.sentinel) {
				t.Errorf("errors.Is(err, %v) is false", tc.sentinel)
			}
			if tc.also != nil && !errors.Is(err, tc.also) {
				t.Errorf("errors.Is(err, %v) is false", tc.also)
			}
		})
	}
}

// An error a caller's step returns comes back as the very value it returned
// when the combinator gives up on it, and an error that wraps two steps' own
// keeps both in its chain, and the kind the stronger one had.
func TestCombinatorsReturnACallersStepErrorAsItIs(t *testing.T) {
	ctx := context.Background()
	budget := &types.OperationError{Kind: types.KindBudgetExceeded, Op: "theirs", Message: "spent"}
	policy := types.Newf(types.KindPolicyViolation, "theirs", "not allowed")
	unavailable := &types.OperationError{Kind: types.KindProviderUnavailable, Op: "theirs", Message: "down"}
	plain := errors.New("a failure of the caller's own")
	ok := constantAnswer("ok")
	accept := func(string) bool { return true }
	reject := func(string) bool { return false }

	for _, terminal := range []error{budget, policy} {
		t.Run("a terminal failure is not escalated: "+terminal.Error(), func(t *testing.T) {
			ran := false
			stronger := Step[string](func(context.Context) (string, error) { ran = true; return "", nil })
			if _, record, err := Escalate(ctx, failingAnswer(terminal), stronger, nil); err != terminal || record.FirstError != terminal || ran {
				t.Errorf("err = %v, record = %+v, stronger ran = %v", err, record, ran)
			}
			if _, err := Fallback(ctx, failingAnswer(terminal), stronger); err != terminal || ran {
				t.Errorf("Fallback err = %v, stronger ran = %v", err, ran)
			}
			if _, _, err := Until(ctx, failingAnswer(terminal), accept, 3); err != terminal {
				t.Errorf("Until err = %v", err)
			}
		})
	}

	for _, second := range []error{budget, plain, unavailable} {
		t.Run("the stronger route's own failure after a rejected answer: "+second.Error(), func(t *testing.T) {
			if _, _, err := Escalate(ctx, ok, failingAnswer(second), reject); err != second {
				t.Errorf("err = %#v, want the stronger step's own error", err)
			}
		})
		t.Run("both routes failed: "+second.Error(), func(t *testing.T) {
			_, record, err := Escalate(ctx, failingAnswer(unavailable), failingAnswer(second), nil)
			if !errors.Is(err, unavailable) || !errors.Is(err, second) {
				t.Errorf("err = %v, want both failures in its chain", err)
			}
			if record.FirstError != unavailable {
				t.Errorf("record.FirstError = %v, want the first step's own error", record.FirstError)
			}
			if got, want := types.KindOf(err), types.KindProviderUnavailable; got != want {
				t.Errorf("kind = %v, want the first failure's kind, %v", got, want)
			}
		})
	}

	t.Run("a step that keeps failing is reported with its own error in the chain", func(t *testing.T) {
		_, attempts, err := Until(ctx, failingAnswer(unavailable), accept, 2)
		if attempts != 2 || !errors.Is(err, unavailable) || types.KindOf(err) != types.KindProviderUnavailable {
			t.Errorf("attempts = %d, err = %v (kind %v)", attempts, err, types.KindOf(err))
		}
		if err.Error() != "until: 2 attempts, the last one failed: "+unavailable.Error() {
			t.Errorf("text = %q", err.Error())
		}
	})
	t.Run("a step that fails with a plain error is reported with that error in the chain", func(t *testing.T) {
		_, _, err := Until(ctx, failingAnswer(plain), accept, 1)
		if !errors.Is(err, plain) || err.Error() != "until: 1 attempts, the last one failed: a failure of the caller's own" {
			t.Errorf("err = %v", err)
		}
	})
}
