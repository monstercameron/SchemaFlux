package types

import (
	"errors"
	"fmt"
	"testing"
)

// The taxonomy is a control protocol, so the properties that matter are the
// ones a caller branches on.
func TestErrorKindDispositions(t *testing.T) {
	cases := []struct {
		kind       ErrorKind
		retryable  bool
		repairable bool
		terminal   bool
	}{
		// Retrying the same request could work.
		{KindRateLimited, true, false, false},
		{KindProviderUnavailable, true, false, false},
		{KindTimeout, true, false, false},
		{KindCircuitOpen, true, false, false},

		// Asking again with the problem named could work. Retrying could not:
		// the same request asks the same question of the same model.
		{KindMalformedOutput, false, true, false},
		{KindSchemaViolation, false, true, false},
		{KindOutputTruncated, false, true, false},
		{KindBatchProtocolViolation, false, true, false},
		{KindInvariantViolation, false, true, false},
		{KindEvidenceViolation, false, true, false},

		// Nothing this library can do will help.
		{KindConfiguration, false, false, true},
		{KindAuthentication, false, false, true},
		{KindPermission, false, false, true},
		{KindInvalidRequest, false, false, true},
		{KindPolicyViolation, false, false, true},
		{KindBudgetExceeded, false, false, true},
		{KindUnsupportedCapability, false, false, true},
		{KindCanceled, false, false, true},
		{KindShutdown, false, false, true},
		{KindRepairExhausted, false, false, true},
		{KindReviewRequired, false, false, true},

		// Unclassified is none of the three, deliberately: treating it as
		// retryable or as terminal would both be guesses.
		{KindUnknown, false, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.kind.String(), func(t *testing.T) {
			err := &OperationError{Kind: tc.kind}
			if err.Retryable() != tc.retryable {
				t.Errorf("Retryable = %v, want %v", err.Retryable(), tc.retryable)
			}
			if err.Repairable() != tc.repairable {
				t.Errorf("Repairable = %v, want %v", err.Repairable(), tc.repairable)
			}
			if err.Terminal() != tc.terminal {
				t.Errorf("Terminal = %v, want %v", err.Terminal(), tc.terminal)
			}
			// The three are exclusive: a caller reading them as a decision tree
			// must not find two branches true.
			count := 0
			for _, flag := range []bool{err.Retryable(), err.Repairable(), err.Terminal()} {
				if flag {
					count++
				}
			}
			if count > 1 {
				t.Errorf("%v is %d of retryable/repairable/terminal at once", tc.kind, count)
			}
		})
	}
}

// errors.Is reaches the sentinel from a wrapped OperationError, which is the
// whole point: a caller writes one comparison and does not care which layer
// produced the failure.
func TestErrorsIsReachesTheSentinel(t *testing.T) {
	pairs := []struct {
		kind     ErrorKind
		sentinel error
	}{
		{KindRateLimited, ErrRateLimited},
		{KindSchemaViolation, ErrSchemaViolation},
		{KindBudgetExceeded, ErrBudgetExceeded},
		{KindReviewRequired, ErrReviewRequired},
		{KindInvariantViolation, ErrInvariantViolation},
	}

	for _, pair := range pairs {
		err := error(&OperationError{Kind: pair.kind, Op: "extract"})
		if !errors.Is(err, pair.sentinel) {
			t.Errorf("errors.Is(%v, %v) = false", pair.kind, pair.sentinel)
		}

		wrapped := fmt.Errorf("running the pipeline: %w", err)
		if !errors.Is(wrapped, pair.sentinel) {
			t.Errorf("the sentinel does not survive wrapping for %v", pair.kind)
		}

		// And it must not match a different kind's sentinel.
		if errors.Is(err, ErrAuthentication) && pair.kind != KindAuthentication {
			t.Errorf("%v matched the authentication sentinel", pair.kind)
		}
	}
}

// The cause survives, so a caller can still reach the provider's own error.
func TestTheCauseSurvives(t *testing.T) {
	cause := errors.New("the underlying failure")
	err := Wrap(KindProviderUnavailable, "classify", cause)

	if !errors.Is(err, cause) {
		t.Error("errors.Is does not reach the cause")
	}
	if KindOf(err) != KindProviderUnavailable {
		t.Errorf("KindOf = %v", KindOf(err))
	}
	if KindOf(cause) != KindUnknown {
		t.Error("an unclassified error reported a kind")
	}
}

// The message carries no payload, because every caller logs this error and the
// payload is their data.
func TestTheMessageIsSanitized(t *testing.T) {
	err := Newf(KindSchemaViolation, "extract", "the response was %d bytes and carried none of the expected fields", 412)

	if got := err.Error(); got == "" {
		t.Fatal("no message")
	}
	for _, forbidden := range []string{"SSN", "@example.com", "4111"} {
		if contains(err.Error(), forbidden) {
			t.Errorf("the message carries a payload fragment: %s", err.Error())
		}
	}
	if !contains(err.Error(), "extract") {
		t.Error("the message does not name the operation")
	}
	if !contains(err.Error(), "schema violation") {
		t.Error("the message does not name the kind")
	}
}

// An ambiguous failure says so: the library performs no side effects, but its
// callers do, and a timeout after the request was sent may have been served.
func TestAmbiguityIsStated(t *testing.T) {
	err := &OperationError{Kind: KindTimeout, Op: "extract", Ambiguous: true}
	if !contains(err.Error(), "may have been served") {
		t.Errorf("an ambiguous timeout does not say so: %s", err.Error())
	}

	clear := &OperationError{Kind: KindTimeout, Op: "extract"}
	if contains(clear.Error(), "may have been served") {
		t.Errorf("an unambiguous failure claims ambiguity: %s", clear.Error())
	}
}

// Every kind names itself, so a log line is readable and a metric label is
// stable.
func TestEveryKindHasAName(t *testing.T) {
	for kind := KindUnknown; kind <= KindReviewRequired; kind++ {
		name := kind.String()
		if name == "" {
			t.Errorf("kind %d has no name", kind)
		}
		if kind != KindUnknown && name == "unknown" {
			t.Errorf("kind %d falls through to \"unknown\"", kind)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	}()
}
