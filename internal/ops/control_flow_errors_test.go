package ops

import (
	"context"
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// An answer to a condition that is neither true nor false is an answer that
// cannot be used, with the text it always had; a failure of the call is the
// call's own error, wrapped as it was, with the kind it carried.
func TestMatchFailuresHaveKinds(t *testing.T) {
	unavailable := &types.OperationError{Kind: types.KindProviderUnavailable, Op: "theirs", Message: "down"}
	budget := &types.OperationError{Kind: types.KindBudgetExceeded, Op: "theirs", Message: "spent"}
	plain := errors.New("a failure of the caller's own")
	const neither = "match: the model's answer to a condition was neither true nor false"
	cases := []struct {
		name     string
		model    func() func()
		text     string
		kind     types.ErrorKind
		sentinel error
		chain    error
	}{
		{"an answer of maybe", func() func() { return stubLLM("maybe") }, neither, types.KindMalformedOutput, types.ErrMalformedOutput, nil},
		{"an empty answer", func() func() { return stubLLM("") }, neither, types.KindMalformedOutput, types.ErrMalformedOutput, nil},
		{"prose", func() func() { return stubLLM("It depends on the weather.") }, neither, types.KindMalformedOutput, types.ErrMalformedOutput, nil},
		{"JSON", func() func() { return stubLLM(`{"match": true}`) }, neither, types.KindMalformedOutput, types.ErrMalformedOutput, nil},
		{"a number", func() func() { return stubLLM("1") }, neither, types.KindMalformedOutput, types.ErrMalformedOutput, nil},
		{"a failed call with a classified error", func() func() { return stubLLMError(unavailable) },
			"match: evaluating a condition: " + unavailable.Error(), types.KindProviderUnavailable, types.ErrProviderUnavailable, unavailable},
		{"a call refused for its budget", func() func() { return stubLLMError(budget) },
			"match: evaluating a condition: " + budget.Error(), types.KindBudgetExceeded, types.ErrBudgetExceeded, budget},
		{"a failed call with a plain error", func() func() { return stubLLMError(plain) },
			"match: evaluating a condition: a failure of the caller's own", types.KindUnknown, nil, plain}, // a plain error through the test hook has no kind to report
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer tc.model()()
			ran := false
			matched, err := Match(context.Background(), "an input",
				Like("a description of a thing", func() { ran = true }),
				Otherwise(func() { ran = true }))
			if err == nil {
				t.Fatal("no error")
			}
			if matched || ran {
				t.Errorf("matched = %v, an action ran = %v; a failed condition runs nothing", matched, ran)
			}
			if err.Error() != tc.text {
				t.Errorf("text = %q, want %q", err.Error(), tc.text)
			}
			if kind := types.KindOf(err); tc.sentinel != nil && kind != tc.kind {
				t.Errorf("kind = %v, want %v", kind, tc.kind)
			}
			if tc.sentinel != nil && !errors.Is(err, tc.sentinel) {
				t.Errorf("errors.Is(err, %v) is false", tc.sentinel)
			}
			if tc.chain != nil && !errors.Is(err, tc.chain) {
				t.Errorf("the call's own error is not in the chain of %v", err)
			}
		})
	}
}
