package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
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

func TestACompleteAnswerIsNotClassifiedAsTruncated(t *testing.T) {
	for _, reason := range []string{"stop", "end_turn", "completed", ""} {
		if got := ClassifyCompletion(CompletionResponse{FinishReason: reason, Content: "done"}); got == types.KindOutputTruncated {
			t.Errorf("ClassifyCompletion(%q) reported truncation for a complete answer", reason)
		}
	}
}
