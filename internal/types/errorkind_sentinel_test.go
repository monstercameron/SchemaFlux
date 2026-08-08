package types

import (
	"errors"
	"strings"
	"testing"
)

// Every kind reaches its own sentinel through Is, and none of the kinds not
// already exercised by errorkind_test.go and payloadprivacy_test.go were
// covered -- sentinelFor's switch was under half tested. This closes the
// rest, kind by kind, so `errors.Is(err, types.ErrX)` is provably correct
// for the whole taxonomy rather than the handful of kinds that happened to
// be reached from other tests.
func TestEveryKindReachesItsOwnSentinelExhaustive(t *testing.T) {
	cases := []struct {
		kind     ErrorKind
		sentinel error
	}{
		{KindConfiguration, ErrConfiguration},
		{KindAuthentication, ErrAuthentication},
		{KindPermission, ErrPermission},
		{KindInvalidRequest, ErrInvalidRequest},
		{KindPolicyViolation, ErrPolicyViolation},
		{KindBudgetExceeded, ErrBudgetExceeded},
		{KindAdmissionRejected, ErrAdmissionRejected},
		{KindRateLimited, ErrRateLimited},
		{KindProviderUnavailable, ErrProviderUnavailable},
		{KindTimeout, ErrTimeout},
		{KindCanceled, ErrCanceled},
		{KindCircuitOpen, ErrCircuitOpen},
		{KindShutdown, ErrShutdown},
		{KindContextTooLarge, ErrContextTooLarge},
		{KindOutputTruncated, ErrOutputTruncated},
		{KindMalformedOutput, ErrMalformedOutput},
		{KindSchemaViolation, ErrSchemaViolation},
		{KindBatchProtocolViolation, ErrBatchProtocolViolation},
		{KindInvariantViolation, ErrInvariantViolation},
		{KindEvidenceViolation, ErrEvidenceViolation},
		{KindUnsupportedCapability, ErrUnsupportedCapability},
		{KindRepairExhausted, ErrRepairExhausted},
		{KindReviewRequired, ErrReviewRequired},
	}

	for _, tc := range cases {
		t.Run(tc.kind.String(), func(t *testing.T) {
			err := &OperationError{Kind: tc.kind, Op: "op"}
			if !errors.Is(err, tc.sentinel) {
				t.Errorf("errors.Is does not reach the sentinel for %v", tc.kind)
			}
		})
	}
}

// OperationError.Error()'s remaining branches: a Cause with no Message
// falls back to the cause's own text, and a set Diagnostic ref is appended
// safely (an ID and a digest, never captured content).
func TestOperationErrorFallsBackToCauseWhenMessageIsEmpty(t *testing.T) {
	cause := errors.New("dial tcp: connection refused")
	err := &OperationError{Kind: KindProviderUnavailable, Op: "extract", Cause: cause}
	got := err.Error()
	if !strings.Contains(got, "connection refused") {
		t.Errorf("Error() = %q, want it to fall back to the cause's text when Message is empty", got)
	}
}

func TestOperationErrorAppendsDiagnosticRefSafely(t *testing.T) {
	err := &OperationError{
		Kind:       KindMalformedOutput,
		Op:         "extract",
		Message:    "the response could not be parsed",
		Diagnostic: DiagnosticRef{ID: "diag-1-1", Digest: "abcd1234"},
	}
	got := err.Error()
	if !strings.Contains(got, "diag-1-1") || !strings.Contains(got, "abcd1234") {
		t.Errorf("Error() = %q, missing the diagnostic reference", got)
	}
}

// The refusal: KindUnknown has no sentinel at all, so it must not match any
// of them -- an unclassified failure is a gap, not a silent alias for one
// of the named kinds.
func TestKindUnknownMatchesNoSentinel(t *testing.T) {
	err := &OperationError{Kind: KindUnknown, Op: "op"}
	all := []error{
		ErrConfiguration, ErrAuthentication, ErrPermission, ErrInvalidRequest,
		ErrPolicyViolation, ErrBudgetExceeded, ErrAdmissionRejected, ErrRateLimited,
		ErrProviderUnavailable, ErrTimeout, ErrCanceled, ErrCircuitOpen, ErrShutdown,
		ErrContextTooLarge, ErrOutputTruncated, ErrMalformedOutput, ErrSchemaViolation,
		ErrBatchProtocolViolation, ErrInvariantViolation, ErrEvidenceViolation,
		ErrUnsupportedCapability, ErrRepairExhausted, ErrReviewRequired,
	}
	for _, sentinel := range all {
		if errors.Is(err, sentinel) {
			t.Errorf("an unclassified error matched sentinel %v", sentinel)
		}
	}
}
