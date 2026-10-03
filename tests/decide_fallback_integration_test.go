package tests

import (
	"context"
	"errors"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Decide's fallback, through the exported API and a provider that fails with
// each kind of failure, is for a model that could not be reached or whose
// answer could not be used, and for nothing the caller has to act on.
func TestIntegrationDecideFallbackCoversOnlyAFailureTheModelCouldNotRecoverFrom(t *testing.T) {
	status := func(code int) error { return &llm.APIError{Provider: "local", StatusCode: code} }
	cases := []struct {
		name    string
		body    string
		err     error
		applies bool
		kind    types.ErrorKind
	}{
		{"a server error", "", status(503), true, types.KindProviderUnavailable},
		{"a gateway timeout", "", status(504), true, types.KindTimeout},
		{"a rate limit", "", status(429), true, types.KindRateLimited},
		{"a plain error of the provider's", "", errors.New("the connection was reset"), true, types.KindUnknown},
		{"prose where an object was asked for", "Billing, I think.", nil, true, types.KindMalformedOutput},
		{"a selection that is not offered", `{"selected":9,"explanation":"x","confidence":0.9}`, nil, true, types.KindSchemaViolation},
		{"a rejected credential", "", status(401), false, types.KindAuthentication},
		{"a forbidden request", "", status(403), false, types.KindPermission},
		{"a request that is too large", "", status(413), false, types.KindContextTooLarge},
		{"a budget that is spent", "", types.Newf(types.KindBudgetExceeded, "theirs", "spent"), false, types.KindBudgetExceeded},
		{"a policy that forbids the route", "", types.Newf(types.KindPolicyViolation, "theirs", "not allowed"), false, types.KindPolicyViolation},
		{"a configuration error", "", types.Newf(types.KindConfiguration, "theirs", "no model"), false, types.KindConfiguration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testfixtures.WithScriptedProvider(t, tc.body, tc.err)
			chosen, result, err := schemaflux.Decide(context.Background(), "an invoice question", departments(),
				schemaflux.NewDecideOptions().WithFallback(2))
			if tc.applies {
				if err != nil || !result.Fallback || result.SelectedIndex != 2 || chosen.Desk != "success" {
					t.Errorf("chosen %q, result %+v, err %v; want the fallback, labelled", chosen.Desk, result, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("the fallback hid the failure: chose %q", chosen.Desk)
			}
			if kind := types.KindOf(err); kind != tc.kind {
				t.Errorf("kind = %v, want %v (%v)", kind, tc.kind, err)
			}
			if chosen.Desk != "" || result.SelectedIndex != -1 || result.Fallback {
				t.Errorf("chosen %q, result %+v; a failure that is not covered returns no branch", chosen.Desk, result)
			}
		})
	}

	t.Run("a call whose context is cancelled", func(t *testing.T) {
		testfixtures.WithScriptedProvider(t, `{"selected":0,"explanation":"x","confidence":0.9}`, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		chosen, result, err := schemaflux.Decide(ctx, "an invoice question", departments(),
			schemaflux.NewDecideOptions().WithFallback(2))
		if !errors.Is(err, context.Canceled) || types.KindOf(err) != types.KindCanceled || chosen.Desk != "" || result.Fallback {
			t.Errorf("chosen %q, result %+v, err %v; a cancelled call is the caller's to see", chosen.Desk, result, err)
		}
	})
}

// A step that panics, run through the exported combinators, is reported as the
// library's own failure: the invariant kind it had, marked as a panic, with
// nothing of the panic in it; and a step's own error is the error returned.
func TestIntegrationCombinatorsMarkAPanicAndReturnAStepsOwnError(t *testing.T) {
	ctx := context.Background()
	theirs := errors.New("the caller's own failure")
	panics := schemaflux.Step[string](func(context.Context) (string, error) { panic("the caller's secret") })
	fails := schemaflux.Step[string](func(context.Context) (string, error) { return "", theirs })

	_, err := schemaflux.Gather(ctx, panics)
	if types.NarrowingOf(err) != types.NarrowPanic || types.KindOf(err) != types.KindInvariantViolation || !errors.Is(err, types.ErrInvariantViolation) {
		t.Errorf("Gather: kind %v, narrowing %v, err %v", types.KindOf(err), types.NarrowingOf(err), err)
	}
	if err != nil && strings.Contains(err.Error(), "secret") {
		t.Error("the panic's value is in the error")
	}
	if _, err := schemaflux.Gather(ctx, fails); err != theirs {
		t.Errorf("Gather: err = %v, want the step's own error", err)
	}
	if _, err := schemaflux.BranchAhead(ctx, func(context.Context) (bool, error) { return true, nil }, fails, panics); err != theirs {
		t.Errorf("BranchAhead: err = %v, want the step's own error", err)
	}

	_, _, err = schemaflux.Escalate(ctx, nil, fails, nil)
	if types.KindOf(err) != types.KindConfiguration || err.Error() != "escalate: no first step to run" {
		t.Errorf("Escalate with no first step: kind %v, err %v", types.KindOf(err), err)
	}
	_, _, err = schemaflux.Vote(ctx, 3, fails, nil, schemaflux.VoteOptions{})
	if types.KindOf(err) != types.KindConfiguration || err.Error() != "vote: no reconciler; agreement is a policy the caller must supply" {
		t.Errorf("Vote with no reconciler: kind %v, err %v", types.KindOf(err), err)
	}
	_, _, err = schemaflux.Until(ctx, fails, func(string) bool { return true }, 0)
	if types.KindOf(err) != types.KindConfiguration || err.Error() != "until: 0 attempts is not a number of attempts" {
		t.Errorf("Until with no attempts: kind %v, err %v", types.KindOf(err), err)
	}
	_, _, err = schemaflux.MapReduce(ctx, []int{1, 2}, schemaflux.MapReduceOptions{ChunkSize: 2},
		func(context.Context, []int) (int, error) { panic("the caller's secret") })
	if types.NarrowingOf(err) != types.NarrowPanic || types.KindOf(err) != types.KindInvariantViolation {
		t.Errorf("MapReduce: kind %v, narrowing %v, err %v", types.KindOf(err), types.NarrowingOf(err), err)
	}
}
