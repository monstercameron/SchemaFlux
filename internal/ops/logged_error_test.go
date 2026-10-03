package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

const loggedErrorCanary = "CANARY_LOGGED_ERROR_44"

func TestALoggedErrorHoldsTheKindOfAFailureAndNothingOfItsText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		kind types.ErrorKind
		mark types.Narrowing
	}{
		{"a plain error is read by the classifier", errors.New("the vendor said " + loggedErrorCanary), types.KindUnknown, types.NarrowNone},
		{"a classified rate limit", types.Newf(types.KindRateLimited, "x", "slow down "+loggedErrorCanary), types.KindRateLimited, types.NarrowNone},
		{"a classified timeout", types.Classified(types.KindTimeout, errors.New(loggedErrorCanary)), types.KindTimeout, types.NarrowNone},
		{"a refusal keeps its mark", types.ClassifiedRefusal(errors.New(loggedErrorCanary)), types.KindPolicyViolation, types.NarrowRefusal},
		{"a context cancellation", fmt.Errorf("while %s: %w", loggedErrorCanary, context.Canceled), types.KindCanceled, types.NarrowNone},
		{"a deadline", fmt.Errorf("%s: %w", loggedErrorCanary, context.DeadlineExceeded), types.KindTimeout, types.NarrowNone},
		{"an API error of 500", &llm.APIError{StatusCode: 500, Message: loggedErrorCanary, Body: loggedErrorCanary, Code: loggedErrorCanary}, types.KindProviderUnavailable, types.NarrowNone},
		{"an API error of 401", &llm.APIError{StatusCode: 401, Message: loggedErrorCanary}, types.KindAuthentication, types.NarrowNone},
		{"a joined error", errors.Join(errors.New(loggedErrorCanary), types.Newf(types.KindBudgetExceeded, "x", "y")), types.KindBudgetExceeded, types.NarrowNone},
		{"a wrapped operation error", fmt.Errorf("outer %s: %w", loggedErrorCanary, types.Newf(types.KindContextTooLarge, "x", "y")), types.KindContextTooLarge, types.NarrowNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := loggedError(c.err)
			if got == nil {
				t.Fatal("loggedError returned nil for an error")
			}
			if strings.Contains(got.Error(), loggedErrorCanary) {
				t.Errorf("the logged form holds the error's text: %q", got.Error())
			}
			if errors.Unwrap(got) != nil {
				t.Errorf("the logged form keeps a cause: %v", errors.Unwrap(got))
			}
			if c.kind != types.KindUnknown && types.KindOf(got) != c.kind {
				t.Errorf("the kind is %v, want %v", types.KindOf(got), c.kind)
			}
			if types.NarrowingOf(got) != c.mark {
				t.Errorf("the mark is %v, want %v", types.NarrowingOf(got), c.mark)
			}
			if want := types.KindOf(got).String(); got.Error() != want {
				t.Errorf("the text is %q, want just the kind %q", got.Error(), want)
			}
		})
	}
	if loggedError(nil) != nil {
		t.Error("loggedError(nil) is not nil")
	}
}
