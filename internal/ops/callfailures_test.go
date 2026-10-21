package ops

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/pricing"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// scriptedFailure is a provider that answers every request the same way: with
// an error, or with a response.
type scriptedFailure struct {
	mu   sync.Mutex
	err  error
	resp llm.CompletionResponse
	sent int
}

func (p *scriptedFailure) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent++
	if p.err != nil {
		return llm.CompletionResponse{}, p.err
	}
	resp := p.resp
	if resp.Provider == "" {
		resp.Provider = "scripted-failure"
	}
	return resp, nil
}
func (p *scriptedFailure) Name() string                               { return "scripted-failure" }
func (p *scriptedFailure) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *scriptedFailure) RetryPolicy() (int, time.Duration)          { return 0, time.Microsecond }

// refusingLedger is a client budget that is spent.
type refusingLedger struct{}

func (refusingLedger) Check() error         { return ErrClientBudgetExhausted }
func (refusingLedger) Record(float64, bool) {}

// callFailures are the ways a request can go wrong, each as the setup of the
// default snapshot and the provider behind it. A call of any operation under
// any of them has to end in an error that has a kind, or in no error.
func callFailures(t *testing.T) []struct {
	name  string
	setup func() *ExecConfig
} {
	apiError := func(status int) *scriptedFailure {
		return &scriptedFailure{err: llm.NewAPIError("scripted-failure", "m", status, "the vendor's words")}
	}
	respond := func(resp llm.CompletionResponse) *scriptedFailure { return &scriptedFailure{resp: resp} }
	with := func(p llm.Provider) func() *ExecConfig {
		return func() *ExecConfig { return &ExecConfig{Provider: p} }
	}
	closed := NewScheduler(SchedulerLimits{})
	_ = closed.Close(context.Background())
	return []struct {
		name  string
		setup func() *ExecConfig
	}{
		{"http 400", with(apiError(http.StatusBadRequest))},
		{"http 401", with(apiError(http.StatusUnauthorized))},
		{"http 404", with(apiError(http.StatusNotFound))},
		{"http 429", with(apiError(http.StatusTooManyRequests))},
		{"http 500", with(apiError(http.StatusInternalServerError))},
		{"http 503", with(apiError(http.StatusServiceUnavailable))},
		{"a network error", with(&scriptedFailure{err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}})},
		{"a plain error", with(&scriptedFailure{err: errors.New("the transport fell over")})},
		{"a cancelled context", with(&scriptedFailure{err: context.Canceled})},
		{"a passed deadline", with(&scriptedFailure{err: context.DeadlineExceeded})},
		{"an empty answer", with(respond(llm.CompletionResponse{Content: "", FinishReason: "stop"}))},
		{"a truncated answer", with(respond(llm.CompletionResponse{Content: `{"partial":`, FinishReason: "length"}))},
		{"a filtered answer", with(respond(llm.CompletionResponse{Content: "", FinishReason: "content_filter"}))},
		{"an answer of the wrong shape", with(respond(llm.CompletionResponse{Content: `[1,2,3]`, FinishReason: "stop"}))},
		{"an answer that is not JSON", with(respond(llm.CompletionResponse{Content: "I would rather not.", FinishReason: "stop"}))},
		{"a spent client budget", func() *ExecConfig {
			return &ExecConfig{Provider: respond(llm.CompletionResponse{Content: "{}", FinishReason: "stop"}), Budget: refusingLedger{}}
		}},
		{"a closed scheduler", func() *ExecConfig {
			return &ExecConfig{Provider: respond(llm.CompletionResponse{Content: "{}", FinishReason: "stop"}), Scheduler: closed}
		}},
		{"a policy that forbids the route", func() *ExecConfig {
			return &ExecConfig{
				Provider:           respond(llm.CompletionResponse{Content: "{}", FinishReason: "stop"}),
				DataPolicy:         types.DataPolicy{AllowedProviders: []string{"nobody"}},
				DataPolicyDeclared: true,
			}
		}},
		{"no provider", func() *ExecConfig { return &ExecConfig{} }},
		{"a client that cannot call", func() *ExecConfig {
			return &ExecConfig{Err: types.Newf(types.KindConfiguration, "client", "the client was built wrong")}
		}},
	}
}

func TestEveryFailureHasAKind(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "failure-model")
	failures := callFailures(t)
	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			for _, call := range operationCalls() {
				t.Run(call.row, func(t *testing.T) {
					swapNoProvider(t)
					withDefaultExec(t, failure.setup())
					err := call.run(context.Background())
					if err == nil {
						return
					}
					if types.KindOf(err) == types.KindUnknown {
						t.Errorf("an error with no kind left the operation: %v", err)
					}
				})
			}
		})
	}

	t.Run("a spent process budget", func(t *testing.T) {
		pricing.ResetCostTracking()
		pricing.ResetBudget()
		t.Cleanup(func() {
			pricing.ResetCostTracking()
			pricing.ResetBudget()
		})
		pricing.SetBudget(0.10, 0, 0, nil)
		pricing.SetBudgetEnforcement(true)
		pricing.TrackCost(&types.CostInfo{TotalCost: 0.25, Currency: "USD", Priced: true},
			&types.ResultMetadata{RequestID: "req-earlier", EndTime: time.Now()})
		for _, call := range operationCalls() {
			t.Run(call.row, func(t *testing.T) {
				withDefaultExec(t, &ExecConfig{Provider: &scriptedFailure{resp: llm.CompletionResponse{Content: "{}", FinishReason: "stop"}}})
				if err := call.run(context.Background()); err != nil && types.KindOf(err) == types.KindUnknown {
					t.Errorf("an error with no kind left the operation: %v", err)
				}
			})
		}
	})
}

// The call path's own failures, one by one: the kind each leaves with, and that
// the text and the sentinel it had are what they were.
func TestCallLLMFailuresLeaveWithTheirKind(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "failure-model")
	cases := []struct {
		name     string
		err      error
		wantKind types.ErrorKind
	}{
		{"a bad request", llm.NewAPIError("p", "m", 400, "body"), types.KindInvalidRequest},
		{"an unauthorised request", llm.NewAPIError("p", "m", 401, "body"), types.KindAuthentication},
		{"a rate limit", llm.NewAPIError("p", "m", 429, "body"), types.KindRateLimited},
		{"a server error", llm.NewAPIError("p", "m", 500, "body"), types.KindProviderUnavailable},
		{"a context cancelled", context.Canceled, types.KindCanceled},
		{"a deadline", context.DeadlineExceeded, types.KindTimeout},
		{"a plain error", errors.New("boom"), types.KindProviderUnavailable},
		{"a wrapped plain error", fmt.Errorf("sending: %w", errors.New("boom")), types.KindProviderUnavailable},
		{"an error that has a kind is not re-kinded", types.Newf(types.KindBudgetExceeded, "mw", "spent"), types.KindBudgetExceeded},
		{"a network error", &net.OpError{Op: "read", Err: errors.New("reset")}, types.KindProviderUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &scriptedFailure{err: tc.err}
			_, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{})
			if err == nil {
				t.Fatal("no error")
			}
			if got := types.KindOf(err); got != tc.wantKind {
				t.Errorf("kind = %v, want %v (%v)", got, tc.wantKind, err)
			}
			if !errors.Is(err, tc.err) {
				t.Error("the provider's own error is no longer in the chain")
			}
			if tc.wantKind == types.KindProviderUnavailable || types.KindOf(tc.err) == types.KindUnknown {
				name, model, _, _, ok := types.CallOf(err)
				if types.KindOf(tc.err) == types.KindUnknown && (!ok || name != "scripted-failure" || model != "failure-model") {
					t.Errorf("the route of the failure = %q %q %v", name, model, ok)
				}
			}
		})
	}
}

// The text of every error whose message has to stay as it is.
func TestErrorTextsAreUnchanged(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"no provider", noProvider, ErrNoProvider.Error()},
		{"an empty completion", validateLLMCompletion(llm.CompletionResponse{}), "provider returned empty completion content"},
		{"a violated trust boundary", verifyTrustBoundary("<<<UNTRUSTED CONTENT: x", "steer me please, at length, to be sure"), ""},
		{"an untrusted segment", func() error {
			_, err := BuildSystemPrompt(PromptSegment{Trust: TrustApplicationData, Content: "x"})
			return err
		}(), `ops: untrusted prompt segment may not be placed in the system prompt: segment 0 has trust level "untrusted application data"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil {
				t.Fatal("no error")
			}
			if tc.want != "" && tc.err.Error() != tc.want {
				t.Errorf("text = %q, want %q", tc.err.Error(), tc.want)
			}
			if tc.name == "a violated trust boundary" && !strings.HasPrefix(tc.err.Error(), ErrTrustBoundaryViolated.Error()+": ") {
				t.Errorf("text = %q", tc.err.Error())
			}
		})
	}
	t.Run("the kinds and sentinels", func(t *testing.T) {
		_, untrusted := BuildSystemPrompt(PromptSegment{Trust: TrustApplicationData, Content: "x"})
		if !errors.Is(untrusted, ErrUntrustedInSystemPrompt) || types.KindOf(untrusted) != types.KindInvariantViolation {
			t.Errorf("untrusted segment: %v, kind %v", untrusted, types.KindOf(untrusted))
		}
		boundary := verifyTrustBoundary("<<<UNTRUSTED CONTENT: x", "steer me please, at length, to be sure")
		if !errors.Is(boundary, ErrTrustBoundaryViolated) || types.KindOf(boundary) != types.KindInvariantViolation {
			t.Errorf("trust boundary: %v, kind %v", boundary, types.KindOf(boundary))
		}
		empty := validateLLMCompletion(llm.CompletionResponse{})
		if types.KindOf(empty) != types.KindMalformedOutput || !errors.Is(empty, errEmptyCompletion) {
			t.Errorf("empty completion: %v, kind %v", empty, types.KindOf(empty))
		}
	})
}

// An answer with no content is sent for again, as it always was, though its
// kind says a malformed output is not worth another request.
func TestEmptyCompletionIsStillRetried(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "failure-model")
	provider := &captureProvider{responses: []llm.CompletionResponse{
		{Content: " ", Model: "m", FinishReason: "stop"},
		{Content: "the answer", Model: "m", FinishReason: "stop"},
	}}
	// captureProvider fills empty content with {"ok":true}, so a blank one is
	// spaces.
	got, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{})
	if err != nil || got != "the answer" || provider.attempts != 2 {
		t.Fatalf("got %q, %v after %d attempts", got, err, provider.attempts)
	}

	t.Run("and when it keeps coming it ends with its kind", func(t *testing.T) {
		stubborn := &scriptedFailure{resp: llm.CompletionResponse{Content: "  ", FinishReason: "stop"}}
		_, err := CallLLM(context.Background(), stubborn, "system", "user", types.OpOptions{})
		if types.KindOf(err) != types.KindMalformedOutput || !errors.Is(err, errEmptyCompletion) {
			t.Errorf("err = %v, kind %v", err, types.KindOf(err))
		}
	})
	t.Run("isRetryableLLMError says so", func(t *testing.T) {
		if !isRetryableLLMError(errEmptyCompletion) {
			t.Error("an empty completion is not retryable")
		}
		if !isRetryableLLMError(fmt.Errorf("wrapped: %w", errEmptyCompletion)) {
			t.Error("a wrapped empty completion is not retryable")
		}
		if isRetryableLLMError(types.Newf(types.KindMalformedOutput, "x", "other")) {
			t.Error("another malformed output became retryable")
		}
	})
}

// An answer that is empty because the provider's filter withheld it is a policy
// violation, as it was named before the empty answer had a kind of its own.
func TestAWithheldAnswerKeepsItsPolicyKind(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "failure-model")
	cases := []struct {
		name    string
		resp    llm.CompletionResponse
		kind    types.ErrorKind
		empty   bool
		retried bool
	}{
		{"filtered and empty", llm.CompletionResponse{Content: "", FinishReason: "content_filter"}, types.KindPolicyViolation, true, true},
		{"filtered and blank", llm.CompletionResponse{Content: "  ", FinishReason: "content_filter"}, types.KindPolicyViolation, true, true},
		{"stopped and empty", llm.CompletionResponse{Content: "", FinishReason: "stop"}, types.KindMalformedOutput, true, true},
		{"no finish and empty", llm.CompletionResponse{Content: ""}, types.KindMalformedOutput, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &scriptedFailure{resp: tc.resp}
			_, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{})
			if got := types.KindOf(err); got != tc.kind {
				t.Errorf("kind = %v, want %v (%v)", got, tc.kind, err)
			}
			if !errors.Is(err, errEmptyCompletion) {
				t.Error("the empty completion left the chain")
			}
			// What the provider's filter withheld is a refusal, and the mark
			// says so to a boundary that tells a refusal from the other policy
			// violations; the other empty answers are not.
			wantMark := types.NarrowNone
			if tc.kind == types.KindPolicyViolation {
				wantMark = types.NarrowRefusal
			}
			if got := types.NarrowingOf(err); got != wantMark {
				t.Errorf("mark = %d, want %d", got, wantMark)
			}
			if !strings.Contains(err.Error(), "provider returned empty completion content") {
				t.Errorf("the error's text changed: %q", err.Error())
			}
		})
	}
}
