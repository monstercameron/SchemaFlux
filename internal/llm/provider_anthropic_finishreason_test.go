package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// ST-003 / I-09. Anthropic's Messages API reports truncation as
// `"stop_reason":"max_tokens"`, exactly the way OpenAI reports "length" -- but
// AnthropicProvider.Complete used to hardcode FinishReason to "stop"
// unconditionally, discarding the field entirely. That made a truncated
// Anthropic answer indistinguishable from a complete one all the way to
// ParseJSON, which is the same bug I-09 describes for OpenAI, just on the
// other provider.
// newTestAnthropicProvider is declared in provider_cache_test.go and reused
// here.

func anthropicFinishReasonServer(t *testing.T, body string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestAnthropicProviderReportsMaxTokensStopReason(t *testing.T) {
	body := `{
	  "id": "msg_1",
	  "content": [{"type": "text", "text": "{\"name\":\"Jo"}],
	  "stop_reason": "max_tokens",
	  "model": "claude-3-5-sonnet-20240620",
	  "usage": {"input_tokens": 10, "output_tokens": 4}
	}`

	provider := newTestAnthropicProvider(t, anthropicFinishReasonServer(t, body))
	resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "claude-3-5-sonnet-20240620"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.FinishReason != "max_tokens" {
		t.Fatalf("FinishReason = %q, want %q", resp.FinishReason, "max_tokens")
	}

	// The decisive assertion for I-09: the classifier this library actually
	// uses must recognize the reason Complete reported.
	if kind := ClassifyCompletion(resp); kind.String() != "output truncated" {
		t.Errorf("ClassifyCompletion(%+v) = %v, want output truncated", resp, kind)
	}
}

// A normal completion's stop_reason ("end_turn") must pass through rather than
// being discarded in favor of the old hardcoded "stop" -- and must not be
// mistaken for truncation.
func TestAnthropicProviderReportsEndTurnStopReason(t *testing.T) {
	body := `{
	  "id": "msg_2",
	  "content": [{"type": "text", "text": "a complete answer"}],
	  "stop_reason": "end_turn",
	  "model": "claude-3-5-sonnet-20240620",
	  "usage": {"input_tokens": 10, "output_tokens": 4}
	}`

	provider := newTestAnthropicProvider(t, anthropicFinishReasonServer(t, body))
	resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "claude-3-5-sonnet-20240620"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.FinishReason != "end_turn" {
		t.Fatalf("FinishReason = %q, want %q", resp.FinishReason, "end_turn")
	}
	if kind := ClassifyCompletion(resp); kind.String() != "unknown" {
		t.Errorf("ClassifyCompletion(%+v) = %v, want unknown (not a failure)", resp, kind)
	}
}

// A response carrying no stop_reason at all, or one this library does not know,
// is not known to be a finished answer. It was reported as "stop", so a body
// that said nothing about how it ended was returned as a complete answer.
func TestAnthropicProviderRefusesAMissingOrUnknownStopReason(t *testing.T) {
	cases := []struct {
		name   string
		reason string // the JSON for the field, or "" to leave it out
	}{
		{"absent", ``},
		{"empty", `"stop_reason": "",`},
		{"null", `"stop_reason": null,`},
		{"unknown word", `"stop_reason": "banana",`},
		{"pause turn", `"stop_reason": "pause_turn",`},
		{"tool use", `"stop_reason": "tool_use",`},
		{"wrong case", `"stop_reason": "END_TURN",`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"id": "msg_3", ` + tc.reason + ` "content": [{"type": "text", "text": "ok"}],
			  "model": "claude-3-5-sonnet-20240620", "usage": {"input_tokens": 1, "output_tokens": 1}}`
			provider := newTestAnthropicProvider(t, anthropicFinishReasonServer(t, body))
			resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "claude-3-5-sonnet-20240620"})
			if err == nil {
				t.Fatalf("an answer with stop reason %q was returned: %+v", tc.reason, resp)
			}
			if !isEmptyResponse(resp) {
				t.Errorf("a result came back beside the error: %+v", resp)
			}
			if got := Classify(err); got != types.KindMalformedOutput {
				t.Errorf("Classify = %v, want %v", got, types.KindMalformedOutput)
			}
			if (&types.OperationError{Kind: Classify(err)}).Retryable() {
				t.Error("a body that says nothing of how it ended is the same on retry")
			}
		})
	}
}

// A refusal is the model declining to answer. Whatever text came with it is not
// the answer, and it is classified as a refusal, not returned as a success.
func TestAnthropicProviderReportsARefusalAsAnError(t *testing.T) {
	body := `{"id": "msg_4", "stop_reason": "refusal",
	  "content": [{"type": "text", "text": "I cannot help with that, because of PATIENT-7731"}],
	  "model": "claude-3-5-sonnet-20240620", "usage": {"input_tokens": 1, "output_tokens": 1}}`
	provider := newTestAnthropicProvider(t, anthropicFinishReasonServer(t, body))
	resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "claude-3-5-sonnet-20240620"})
	if err == nil {
		t.Fatalf("a refusal was returned as an answer: %+v", resp)
	}
	if !isEmptyResponse(resp) {
		t.Errorf("a result came back beside the error: %+v", resp)
	}
	if got := Classify(err); got != types.KindPolicyViolation {
		t.Errorf("Classify = %v, want %v", got, types.KindPolicyViolation)
	}
	if strings.Contains(err.Error(), "PATIENT-7731") {
		t.Errorf("the model's text is in the error: %v", err)
	}
}

func TestAnthropicProviderAcceptsTheStopReasonsThatSayHowAnAnswerEnded(t *testing.T) {
	for _, reason := range []string{"end_turn", "stop_sequence", "max_tokens"} {
		t.Run(reason, func(t *testing.T) {
			body := `{"id": "msg_5", "stop_reason": "` + reason + `",
			  "content": [{"type": "text", "text": "some text"}],
			  "model": "claude-3-5-sonnet-20240620", "usage": {"input_tokens": 1, "output_tokens": 1}}`
			provider := newTestAnthropicProvider(t, anthropicFinishReasonServer(t, body))
			resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "claude-3-5-sonnet-20240620"})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if resp.FinishReason != reason {
				t.Errorf("FinishReason = %q, want %q", resp.FinishReason, reason)
			}
		})
	}
}

// Cut off at the ceiling before any text: the answer says so, and is not an
// empty answer to be retried.
func TestAnthropicProviderClassifiesATruncationWithNoText(t *testing.T) {
	body := `{"id": "msg_6", "stop_reason": "max_tokens", "content": [{"type": "thinking"}],
	  "model": "claude-3-5-sonnet-20240620", "usage": {"input_tokens": 1, "output_tokens": 1}}`
	provider := newTestAnthropicProvider(t, anthropicFinishReasonServer(t, body))
	_, err := provider.Complete(context.Background(), CompletionRequest{Model: "claude-3-5-sonnet-20240620"})
	if got := Classify(err); got != types.KindOutputTruncated {
		t.Errorf("Classify = %v, want %v (%v)", got, types.KindOutputTruncated, err)
	}
}
