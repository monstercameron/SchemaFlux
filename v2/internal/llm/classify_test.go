package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// A-007. Until this existed, a caller automating recovery had to match
// substrings against an English sentence -- which changes when someone improves
// the wording, and changes per provider.
func TestClassifyMapsProviderFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want types.ErrorKind
	}{
		{"nil", nil, types.KindUnknown},
		{"cancelled context", context.Canceled, types.KindCanceled},
		{"deadline", context.DeadlineExceeded, types.KindTimeout},

		{"401", &APIError{StatusCode: 401}, types.KindAuthentication},
		{"403", &APIError{StatusCode: 403}, types.KindPermission},
		{"400", &APIError{StatusCode: 400}, types.KindInvalidRequest},
		{"404", &APIError{StatusCode: 404}, types.KindInvalidRequest},
		{"413", &APIError{StatusCode: 413}, types.KindContextTooLarge},
		{"408", &APIError{StatusCode: 408}, types.KindTimeout},
		{"500", &APIError{StatusCode: 500}, types.KindProviderUnavailable},
		{"502", &APIError{StatusCode: 502}, types.KindProviderUnavailable},
		{"503", &APIError{StatusCode: 503}, types.KindProviderUnavailable},
		{"an unusual 4xx", &APIError{StatusCode: 418}, types.KindInvalidRequest},
		{"an unusual 5xx", &APIError{StatusCode: 599}, types.KindProviderUnavailable},

		{"429", &RateLimitError{APIError: &APIError{StatusCode: 429}}, types.KindRateLimited},

		{"wrapped", fmt.Errorf("calling the provider: %w", &APIError{StatusCode: 401}), types.KindAuthentication},

		{"no provider", errors.New("no LLM provider configured: call Init"), types.KindConfiguration},
		{"no model", errors.New(`no default model for provider "qwen"`), types.KindConfiguration},
		{"unclassifiable", errors.New("something went sideways"), types.KindUnknown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.err); got != tc.want {
				t.Errorf("Classify = %v, want %v", got, tc.want)
			}
		})
	}
}

// A 500 whose body mentions invalid_request_error is a transient failure, and
// classifying it permanent cost the caller every retry they were entitled to.
// That was P-007's finding, and the status has to keep winning over the prose.
func TestTheStatusBeatsTheProse(t *testing.T) {
	err := &APIError{
		StatusCode: 500,
		Message:    "invalid_request_error: upstream said something unhelpful",
		Type:       "invalid_request_error",
	}

	if got := Classify(err); got != types.KindProviderUnavailable {
		t.Errorf("Classify = %v, want provider unavailable -- the body's prose won", got)
	}

	operationErr := types.Wrap(Classify(err), "extract", err)
	if !operationErr.Retryable() {
		t.Error("a 500 was classified as not retryable")
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestTransportFailuresAreClassified(t *testing.T) {
	var netErr net.Error = timeoutError{}
	if got := Classify(netErr); got != types.KindTimeout {
		t.Errorf("Classify(net timeout) = %v, want timeout", got)
	}

	refused := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	if got := Classify(refused); got != types.KindProviderUnavailable {
		t.Errorf("Classify(dial failure) = %v, want provider unavailable", got)
	}
}

// A 200 is not a logical success: a truncated body arrives with the same status
// as a complete one, and the finish reason is the only thing that says so.
func TestClassifyCompletion(t *testing.T) {
	cases := []struct {
		name string
		resp CompletionResponse
		want types.ErrorKind
	}{
		{"complete", CompletionResponse{Content: `{"ok":true}`, FinishReason: "stop"}, types.KindUnknown},
		{"length", CompletionResponse{Content: `{"ok":`, FinishReason: "length"}, types.KindOutputTruncated},
		{"max_tokens", CompletionResponse{Content: `{"ok":`, FinishReason: "max_tokens"}, types.KindOutputTruncated},
		{"incomplete", CompletionResponse{Content: `{"ok":`, FinishReason: "incomplete"}, types.KindOutputTruncated},
		{"content filter", CompletionResponse{Content: "", FinishReason: "content_filter"}, types.KindPolicyViolation},
		{"empty", CompletionResponse{Content: "   ", FinishReason: "stop"}, types.KindMalformedOutput},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyCompletion(tc.resp); got != tc.want {
				t.Errorf("ClassifyCompletion = %v, want %v", got, tc.want)
			}
		})
	}
}

// The Responses API reports a cut-off answer as "max_output_tokens", which was
// missing from the list this switch matches on -- so against the one provider
// this library targets first, a truncated answer classified as KindUnknown and
// ST-003's truncation handling never fired. The provider test that looked at
// truncation only asserted the raw FinishReason string and never fed it
// through the classifier, which is how a list of four spellings missed the one
// that actually ships.
func TestEveryTruncationSpellingClassifiesAsTruncated(t *testing.T) {
	spellings := []string{
		"length",            // Chat Completions
		"max_tokens",        // Anthropic
		"max_output_tokens", // Responses API -- the one that was missing
		"incomplete",
		"truncated",
		"MAX_OUTPUT_TOKENS", // case is folded
	}

	for _, reason := range spellings {
		t.Run(reason, func(t *testing.T) {
			got := ClassifyCompletion(CompletionResponse{
				FinishReason: reason,
				Content:      "a partial answer",
			})
			if got != types.KindOutputTruncated {
				t.Errorf("ClassifyCompletion(%q) = %v, want KindOutputTruncated", reason, got)
			}
		})
	}
}

// 429 is a rate limit and 503 is the provider being unavailable, whichever type
// carries the error and whichever provider returned it. A RateLimitError is also
// what carries a 503's Retry-After, and it used to turn every 503 into a rate
// limit while kindForStatus said otherwise.
func TestClassifyReadsTheStatusOfARateLimitError(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   types.ErrorKind
	}{
		{"429", http.StatusTooManyRequests, types.KindRateLimited},
		{"503", http.StatusServiceUnavailable, types.KindProviderUnavailable},
		{"no status recorded", 0, types.KindRateLimited},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &RateLimitError{APIError: &APIError{Provider: "p", StatusCode: tc.status}, RetryAfter: time.Second}
			if got := Classify(err); got != tc.want {
				t.Errorf("Classify = %v, want %v", got, tc.want)
			}
			wrapped := fmt.Errorf("calling: %w", err)
			if got := Classify(wrapped); got != tc.want {
				t.Errorf("Classify(wrapped) = %v, want %v", got, tc.want)
			}
		})
	}
}

// Both statuses are retried, and the two are told apart by kind and not by
// retry behaviour.
func TestRateLimitAndUnavailableAreBothRetryable(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		err := &RateLimitError{APIError: &APIError{StatusCode: status}}
		if !(&types.OperationError{Kind: Classify(err)}).Retryable() {
			t.Errorf("status %d is not retryable", status)
		}
		if !err.APIError.Retryable() {
			t.Errorf("APIError.Retryable() is false for %d", status)
		}
	}
}

// The library's own spend-limit errors are typed. A vendor's prose that happens
// to say "budget" is not one: Anthropic's text for a bad thinking budget did, and
// was read as the caller's spend limit.
func TestOnlyTheLibrarysBudgetErrorIsABudgetError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want types.ErrorKind
	}{
		{"typed budget error", &types.OperationError{Kind: types.KindBudgetExceeded, Message: "x"}, types.KindBudgetExceeded},
		{"the pricing package's budget error", fmt.Errorf("cost budget exceeded: daily spend 5.00 of limit 4.00"), types.KindBudgetExceeded},
		{"the pricing package's error wrapped", fmt.Errorf("extract: %w", errors.New("cost budget exceeded: monthly")), types.KindBudgetExceeded},
		{"vendor prose", errors.New("max_tokens must be greater than thinking.budget_tokens"), types.KindUnknown},
		{"account message", errors.New("monthly budget exceeded for this account"), types.KindUnknown},
		{"upper case", errors.New("BUDGET limit hit"), types.KindUnknown},
		{"the word alone", errors.New("budget"), types.KindUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.err); got != tc.want {
				t.Errorf("Classify = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestACompleteAnswerIsNotClassifiedAsTruncated(t *testing.T) {
	for _, reason := range []string{"stop", "end_turn", "completed", ""} {
		if got := ClassifyCompletion(CompletionResponse{FinishReason: reason, Content: "done"}); got == types.KindOutputTruncated {
			t.Errorf("ClassifyCompletion(%q) reported truncation for a complete answer", reason)
		}
	}
}

// A reply that is only tool calls is an answer: the model asked for tools
// instead of writing text. It used to classify as a malformed output because its
// content was empty, which the call path refuses and sends again. A reply with no
// content and no calls is still empty, and one cut off or withheld is still that.
func TestClassifyCompletionReadsAReplyOfOnlyToolCallsAsAnAnswer(t *testing.T) {
	calls := []ToolCall{{ID: "call_1", Name: "get_weather", Arguments: []byte(`{}`)}}
	cases := []struct {
		name string
		resp CompletionResponse
		want types.ErrorKind
	}{
		{"only calls", CompletionResponse{ToolCalls: calls, FinishReason: "tool_calls"}, types.KindUnknown},
		{"only calls that finished as stop", CompletionResponse{ToolCalls: calls, FinishReason: "stop"}, types.KindUnknown},
		{"only calls with no finish reason", CompletionResponse{ToolCalls: calls}, types.KindUnknown},
		{"calls and blank text", CompletionResponse{ToolCalls: calls, Content: "  ", FinishReason: "tool_calls"}, types.KindUnknown},
		{"calls and text", CompletionResponse{ToolCalls: calls, Content: "one moment", FinishReason: "tool_calls"}, types.KindUnknown},
		{"several calls", CompletionResponse{ToolCalls: append(calls, ToolCall{ID: "call_2", Name: "get_time"}), FinishReason: "tool_calls"}, types.KindUnknown},
		{"calls cut off at the ceiling", CompletionResponse{ToolCalls: calls, FinishReason: "max_output_tokens"}, types.KindOutputTruncated},
		{"calls cut off, in the chat dialect's word", CompletionResponse{ToolCalls: calls, FinishReason: "length"}, types.KindOutputTruncated},
		{"calls withheld by a content filter", CompletionResponse{ToolCalls: calls, FinishReason: "content_filter"}, types.KindPolicyViolation},
		{"no calls and no text", CompletionResponse{FinishReason: "tool_calls"}, types.KindMalformedOutput},
		{"an empty list of calls and no text", CompletionResponse{ToolCalls: []ToolCall{}, FinishReason: "stop"}, types.KindMalformedOutput},
		{"no calls and text", CompletionResponse{Content: "hello", FinishReason: "stop"}, types.KindUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyCompletion(tc.resp); got != tc.want {
				t.Errorf("ClassifyCompletion = %v, want %v", got, tc.want)
			}
		})
	}
}
