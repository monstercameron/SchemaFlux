package llm

import (
	"errors"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// The two errors that say why an answer carries no text file it under a kind and,
// for a refusal, under the mark that tells it from the other policy violations.
// Version 2 reads the mark to report the refused kind, so an answer the model
// declined and one the provider's filter withheld must both carry it, and a
// reason that is not a refusal must not.
func TestAnAnswerWithNoTextIsFiledUnderTheKindAndMarkOfItsFinishReason(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		kind   types.ErrorKind
		mark   types.Narrowing
	}{
		{"max_output_tokens", "max_output_tokens", types.KindOutputTruncated, types.NarrowNone},
		{"max_tokens", "max_tokens", types.KindOutputTruncated, types.NarrowNone},
		{"length", "length", types.KindOutputTruncated, types.NarrowNone},
		{"content_filter", "content_filter", types.KindPolicyViolation, types.NarrowRefusal},
		{"an empty reason", "", types.KindMalformedOutput, types.NarrowNone},
		{"stop", "stop", types.KindMalformedOutput, types.NarrowNone},
		{"end_turn", "end_turn", types.KindMalformedOutput, types.NarrowNone},
		{"tool_calls", "tool_calls", types.KindMalformedOutput, types.NarrowNone},
		{"refusal is not a reason of this function", "refusal", types.KindMalformedOutput, types.NarrowNone},
		{"a reason nobody knows", "something_new", types.KindMalformedOutput, types.NarrowNone},
		{"the case of a known reason matters", "Content_Filter", types.KindMalformedOutput, types.NarrowNone},
		{"a reason that quotes the input", "PATIENT-9921", types.KindMalformedOutput, types.NarrowNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := incompleteAnswerError("openai", "model-a", tc.reason)
			if got := types.KindOf(err); got != tc.kind {
				t.Errorf("kind = %v, want %v", got, tc.kind)
			}
			if got := types.NarrowingOf(err); got != tc.mark {
				t.Errorf("mark = %d, want %d", got, tc.mark)
			}
			if got := Classify(err); got != tc.kind {
				t.Errorf("Classify = %v, want %v", got, tc.kind)
			}
			var op *types.OperationError
			if !errors.As(err, &op) || op.Provider != "openai" || op.Model != "model-a" || op.Op != "llm.complete" {
				t.Errorf("the error does not name its route: %#v", err)
			}
			if strings.Contains(err.Error(), tc.reason) && tc.reason != "" {
				t.Errorf("the vendor's reason is in the text: %v", err)
			}
			if op != nil && op.Retryable() {
				t.Error("an answer that is stopped the same way each time is retryable")
			}
		})
	}
}

// An answer the model declined to give is a refusal too, whatever provider
// declined it.
func TestAnAnswerTheModelDeclinedIsARefusal(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "a provider with spaces", "", "gemini", "local", "azure", "bedrock", "x", "mistral"} {
		t.Run("provider "+provider, func(t *testing.T) {
			err := refusedAnswerError(provider, "model-a")
			if got := types.KindOf(err); got != types.KindPolicyViolation {
				t.Errorf("kind = %v, want a policy violation", got)
			}
			if got := types.NarrowingOf(err); got != types.NarrowRefusal {
				t.Errorf("mark = %d, want a refusal", got)
			}
			if !errors.Is(err, types.ErrPolicyViolation) {
				t.Error("the sentinel of the kind does not match")
			}
			var op *types.OperationError
			if !errors.As(err, &op) || op.Provider != provider || op.Model != "model-a" || op.Op != "llm.complete" {
				t.Errorf("the error does not name its route: %#v", err)
			}
			if op != nil && op.Retryable() {
				t.Error("a refusal is retryable")
			}
		})
	}
}
