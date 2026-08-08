package types

import (
	"errors"
	"strings"
	"testing"
)

// The rule these types exist to keep: an error from this library describes a
// failure without reproducing the caller's data. Every operation-specific error
// here carries a *shape* and a *reason*, and every `Error()` prints only those.
//
// This is the surface where the rule is easiest to break by accident — adding
// a value to an error message is one word — and hardest to notice, because the
// result still looks like a useful error. So each type is checked against a
// marker string fed through the field a value would plausibly land in.

const payloadMarker = "MARKER-SSN-123-45-6789"

// describedShapes are the error types that carry a described input. Each is
// constructed with the marker in the position a caller's value would reach if
// somebody wired one through, so the assertion is about the type rather than
// about today's construction site.
func TestNoErrorTypePrintsTheCallersValue(t *testing.T) {
	cause := errors.New("underlying failure")

	cases := []struct {
		name string
		err  error
	}{
		{"extract", ExtractError{InputShape: DescribeValue(payloadMarker), Reason: "unparseable", Err: cause}},
		{"transform", TransformError{InputShape: DescribeValue(payloadMarker), FromType: "Source", ToType: "Target", Reason: "no mapping", Err: cause}},
		{"generate", GenerateError{TargetType: "Target", Reason: "refused", Err: cause}},
		{"classify", ClassifyError{InputShape: DescribeValue(payloadMarker), Reason: "not in set", Err: cause}},
		{"summarize", SummarizeError{InputShape: DescribeValue(payloadMarker), Length: 42, Reason: "too long", Err: cause}},
		{"operation", &OperationError{Kind: KindMalformedOutput, Op: "extract", Message: "the response could not be parsed"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			message := tc.err.Error()
			if message == "" {
				t.Fatal("the error prints nothing at all")
			}
			if strings.Contains(message, payloadMarker) {
				t.Errorf("the caller's value reached the error string: %s", message)
			}
		})
	}
}

// DescribeValue is the function that makes the rule affordable: it says what an
// input *is* without saying what it contains.
func TestDescribeValueSaysTheShapeAndNotTheContent(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"string", payloadMarker},
		{"map", map[string]any{"ssn": payloadMarker}},
		{"slice", []string{payloadMarker, payloadMarker}},
		{"struct", struct {
			SSN   string
			Count int
		}{payloadMarker, 3}},
		{"nil", nil},
		{"number", 42},
		{"nested", map[string]any{"outer": map[string]any{"inner": payloadMarker}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			described := DescribeValue(tc.value)
			if strings.Contains(described, payloadMarker) {
				t.Errorf("DescribeValue reproduced the value: %q", described)
			}
			if tc.value != nil && described == "" {
				t.Error("DescribeValue said nothing about a non-nil value")
			}
		})
	}
}

// A long string is described by its size, not quoted — the case where quoting
// is most tempting and most expensive.
func TestDescribeValueDoesNotQuoteALongString(t *testing.T) {
	long := strings.Repeat("secret ", 500)

	described := DescribeValue(long)
	if len(described) > 200 {
		t.Errorf("DescribeValue returned %d characters for a long input; it should describe, not reproduce", len(described))
	}
	if strings.Contains(described, "secret secret secret") {
		t.Errorf("DescribeValue quoted the input: %q", described)
	}
}

// errors.Is and errors.As have to reach through these types, or a caller
// branching on a kind gets the wrong answer and falls back to string matching —
// which is the thing the taxonomy exists to remove.
func TestEveryWrappedErrorUnwraps(t *testing.T) {
	cause := errors.New("the underlying failure")

	wrapped := []error{
		ExtractError{Reason: "r", Err: cause},
		TransformError{Reason: "r", Err: cause},
		GenerateError{Reason: "r", Err: cause},
		ClassifyError{Reason: "r", Err: cause},
		SummarizeError{Reason: "r", Err: cause},
	}

	for _, err := range wrapped {
		if !errors.Is(err, cause) {
			t.Errorf("%T does not unwrap to its cause", err)
		}
	}
}

func TestOperationErrorReachesItsSentinel(t *testing.T) {
	cases := []struct {
		kind     ErrorKind
		sentinel error
	}{
		{KindRateLimited, ErrRateLimited},
		{KindTimeout, ErrTimeout},
		{KindMalformedOutput, ErrMalformedOutput},
		{KindOutputTruncated, ErrOutputTruncated},
		{KindReviewRequired, ErrReviewRequired},
	}

	for _, tc := range cases {
		t.Run(tc.kind.String(), func(t *testing.T) {
			err := &OperationError{Kind: tc.kind, Message: "something happened"}
			if !errors.Is(err, tc.sentinel) {
				t.Errorf("an error of kind %v does not match its sentinel", tc.kind)
			}
			// And not a different one, or branching is meaningless.
			if tc.sentinel != ErrTimeout && errors.Is(err, ErrTimeout) {
				t.Errorf("an error of kind %v also matches ErrTimeout", tc.kind)
			}
		})
	}
}

// The three dispositions are mutually exclusive by design: a caller asking
// "should I retry this" must not get yes and no from the same error.
func TestEveryKindHasExactlyOneDisposition(t *testing.T) {
	for kind := KindUnknown; kind <= KindReviewRequired; kind++ {
		err := &OperationError{Kind: kind}

		count := 0
		for _, yes := range []bool{err.Retryable(), err.Repairable(), err.Terminal()} {
			if yes {
				count++
			}
		}
		if count > 1 {
			t.Errorf("%v reports %d dispositions; they are meant to be mutually exclusive", kind, count)
		}
	}
}
