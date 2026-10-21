package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The fallback of Decide is for a model that could not be reached or whose
// answer could not be used: a failure of kind unavailable, rate limited,
// timeout or invalid answer. It never hides a failure that is the caller's to
// act on (a budget, a policy, a cancellation, a configuration error). A failure
// of the call that nothing classified is a provider's plain error, and is read
// as the provider being unavailable.
func TestDecideFallbackAppliesOnlyToAFailureTheModelCouldNotRecoverFrom(t *testing.T) {
	// One row for every kind in the taxonomy, and for no kind at all.
	kinds := []struct {
		kind    types.ErrorKind
		applies bool
	}{
		{types.KindConfiguration, false},
		{types.KindAuthentication, false},
		{types.KindPermission, false},
		{types.KindInvalidRequest, false},
		{types.KindPolicyViolation, false},
		{types.KindBudgetExceeded, false},
		{types.KindAdmissionRejected, false},
		{types.KindRateLimited, true},
		{types.KindProviderUnavailable, true},
		{types.KindTimeout, true},
		{types.KindCanceled, false},
		{types.KindCircuitOpen, true},
		{types.KindShutdown, false},
		{types.KindContextTooLarge, false},
		{types.KindOutputTruncated, false},
		{types.KindMalformedOutput, true},
		{types.KindSchemaViolation, true},
		{types.KindBatchProtocolViolation, true},
		{types.KindInvariantViolation, true},
		{types.KindEvidenceViolation, true},
		{types.KindUnsupportedCapability, false},
		{types.KindRepairExhausted, true},
		{types.KindReviewRequired, false},
	}
	type failure struct {
		name    string
		err     error
		applies bool
	}
	var failures []failure
	for _, row := range kinds {
		failures = append(failures, failure{
			name:    "kind " + row.kind.String(),
			err:     &types.OperationError{Kind: row.kind, Op: "theirs", Message: "the call failed"},
			applies: row.applies,
		})
	}
	failures = append(failures,
		failure{"no kind, a provider's plain error, read as unavailable", errors.New("an error nobody classified"), true},
		failure{"a recovered panic, which is a failure of the library itself", stepPanicked("gather"), false},
		failure{"a provider failure with its route", types.ClassifiedCall(types.KindProviderUnavailable, errors.New("503"), "p", "m", "req-1", 0), true},
		failure{"a rate limit with its wait", types.ClassifiedCall(types.KindRateLimited, errors.New("429"), "p", "m", "req-1", time.Second), true},
		failure{"a budget behind a wrapper", fmt.Errorf("the call: %w", &types.OperationError{Kind: types.KindBudgetExceeded, Message: "spent"}), false},
		failure{"a policy behind a wrapper", fmt.Errorf("the call: %w", &types.OperationError{Kind: types.KindPolicyViolation, Message: "no"}), false},
		failure{"a classified budget", types.Classified(types.KindBudgetExceeded, errors.New("cost budget exceeded")), false},
		failure{"the bare context cancellation", context.Canceled, false},
		failure{"the bare context deadline", context.DeadlineExceeded, true},
		failure{"a refusal of the model", &types.OperationError{Kind: types.KindPolicyViolation, Narrow: types.NarrowRefusal, Op: "theirs", Message: "declined"}, false},
	)

	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			defer stubLLMError(tc.err)()
			value, result, err := Decide(context.Background(), "a ticket", routes(), NewDecideOptions().WithFallback(2))
			if tc.applies {
				if err != nil || !result.Fallback || result.SelectedIndex != 2 || value != "success" || result.ModelConfidence != 0 {
					t.Errorf("value %q, result %+v, err %v; want the fallback, labelled", value, result, err)
				}
				if !strings.Contains(result.Explanation, "fallback") {
					t.Errorf("the explanation does not say it was a fallback: %q", result.Explanation)
				}
				return
			}
			if err != tc.err {
				t.Errorf("err = %#v, want the very error the call returned", err)
			}
			if value != "" || result.SelectedIndex != -1 || result.Fallback || result.ModelConfidence != 0 {
				t.Errorf("value %q, result %+v; a failure that is not covered returns no branch", value, result)
			}
		})
		t.Run(tc.name+", with no fallback", func(t *testing.T) {
			defer stubLLMError(tc.err)()
			value, result, err := Decide(context.Background(), "a ticket", routes())
			if err != tc.err || value != "" || result.SelectedIndex != -1 || result.Fallback {
				t.Errorf("value %q, result %+v, err %v; want the call's error and no branch", value, result, err)
			}
		})
	}
}

// An answer that cannot be used is an invalid answer, whatever is wrong with it,
// and the fallback covers it; the same call under a cancelled context does not.
func TestDecideFallbackCoversAnAnswerThatCannotBeUsedButNotACancelledCall(t *testing.T) {
	bodies := []string{"", "billing", `{"selected":`, `{"selected":9,"explanation":"x","confidence":0.5}`, `[1]`, `{"explanation":"x","confidence":0.5}`}
	for _, body := range bodies {
		t.Run(fmt.Sprintf("answer %q", body), func(t *testing.T) {
			defer stubLLM(body)()
			value, result, err := Decide(context.Background(), "a ticket", routes(), NewDecideOptions().WithFallback(1))
			if err != nil || !result.Fallback || result.SelectedIndex != 1 || value != "billing" {
				t.Errorf("value %q, result %+v, err %v; want the fallback", value, result, err)
			}
		})
	}
	t.Run("a call whose context is cancelled", func(t *testing.T) {
		defer stubLLM(`{"selected":0,"explanation":"x","confidence":0.5}`)()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		value, result, err := Decide(ctx, "a ticket", routes(), NewDecideOptions().WithFallback(1))
		if !errors.Is(err, context.Canceled) || types.KindOf(err) != types.KindCanceled || value != "" || result.Fallback {
			t.Errorf("value %q, result %+v, err %v; a cancelled call is the caller's to see", value, result, err)
		}
	})
}

// A line written by Decide, Guard or GuardWithSuggestions goes to the logger of
// the client on the call's context, and with no client to the process logger.
func TestProceduralLinesGoToTheCallsLogger(t *testing.T) {
	cases := []struct {
		name  string
		run   func(ctx context.Context)
		lines []string
	}{
		{"Decide, refused before any call", func(ctx context.Context) {
			_, _, _ = Decide(ctx, "a ticket", []Decision[string]{})
		}, []string{"Starting decide operation", "Decide operation failed: no decisions provided"}},
		{"Decide, a call that fails", func(ctx context.Context) {
			defer stubLLMError(errors.New("an error nobody classified"))()
			_, _, _ = Decide(ctx, "a ticket", routes())
		}, []string{"Starting decide operation", "Decide operation LLM call failed"}},
		{"Decide, an answer that cannot be used", func(ctx context.Context) {
			defer stubLLM("billing")()
			_, _, _ = Decide(ctx, "a ticket", routes())
		}, []string{"Decide operation response was not usable"}},
		{"Decide, a good answer", func(ctx context.Context) {
			defer stubLLM(`{"selected":1,"explanation":"x","confidence":0.5}`)()
			_, _, _ = Decide(ctx, "a ticket", routes())
		}, []string{"Decide operation succeeded"}},
		{"Guard", func(ctx context.Context) {
			Guard(ctx, 1, func(int) (bool, string) { return true, "" })
		}, []string{"Starting guard operation"}},
		{"GuardWithSuggestions, suggestions unavailable", func(ctx context.Context) {
			defer stubLLMError(errors.New("an error nobody classified"))()
			GuardWithSuggestions(ctx, 1, types.OpOptions{}, func(int) (bool, string) { return false, "too small" })
		}, []string{"Starting guard operation", "Guard suggestions unavailable; the verdict itself is unaffected"}},
	}
	for _, tc := range cases {
		t.Run(tc.name+", with a snapshot", func(t *testing.T) {
			ctx, snapshot, process := controlFlowSnapshotLog(t)
			tc.run(ctx)
			for _, line := range tc.lines {
				if !strings.Contains(snapshot.String(), line) {
					t.Errorf("the snapshot's logger did not get %q: %q", line, snapshot.String())
				}
			}
			if got := process(); got != "" {
				t.Errorf("the process logger got lines: %q", got)
			}
		})
		t.Run(tc.name+", with no snapshot", func(t *testing.T) {
			process := captureLogs(t)
			tc.run(context.Background())
			for _, line := range tc.lines {
				if !strings.Contains(process(), line) {
					t.Errorf("the process logger did not get %q: %q", line, process())
				}
			}
		})
	}
}
