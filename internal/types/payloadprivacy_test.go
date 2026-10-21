package types

import (
	"errors"
	"reflect"
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

// An error's text is its reason and, for classification, the shape of its
// input. Every other field of every error type is filled here with the marker,
// the way a value would arrive if somebody wired one through, and the message
// must still not contain it: a field that reaches Error() fails this test, where
// a marker passed through DescribeValue could never have.
func TestNoErrorTypePrintsAFieldThatIsNotItsReasonOrShape(t *testing.T) {
	types := map[string]reflect.Type{
		"classify":  reflect.TypeOf(ClassifyError{}),
		"score":     reflect.TypeOf(ScoreError{}),
		"compare":   reflect.TypeOf(CompareError{}),
		"choose":    reflect.TypeOf(ChooseError{}),
		"filter":    reflect.TypeOf(FilterError{}),
		"sort":      reflect.TypeOf(SortError{}),
		"extract":   reflect.TypeOf(ExtractError{}),
		"transform": reflect.TypeOf(TransformError{}),
		"generate":  reflect.TypeOf(GenerateError{}),
		"summarize": reflect.TypeOf(SummarizeError{}),
		"rewrite":   reflect.TypeOf(RewriteError{}),
		"translate": reflect.TypeOf(TranslateError{}),
		"expand":    reflect.TypeOf(ExpandError{}),
	}
	for name, typ := range types {
		t.Run(name, func(t *testing.T) {
			value := reflect.New(typ).Elem()
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				target := value.Field(i)
				switch {
				case field.Name == "Reason":
					target.SetString("the reason")
				case field.Name == "InputShape":
					target.SetString("text, 5 bytes")
				case field.Name == "Err":
					target.Set(reflect.ValueOf(errors.New(payloadMarker)))
				case field.Type.Kind() == reflect.String:
					target.SetString(payloadMarker)
				case field.Type.Kind() == reflect.Slice && field.Type.Elem().Kind() == reflect.String:
					target.Set(reflect.ValueOf([]string{payloadMarker}))
				case field.Type.Kind() == reflect.Interface:
					target.Set(reflect.ValueOf(payloadMarker))
				case field.Type.Kind() == reflect.Int:
					target.SetInt(424242)
				case field.Type.Kind() == reflect.Float64:
					target.SetFloat(0.424242)
				default:
					t.Fatalf("%s.%s is a %s, which this test does not know how to fill", name, field.Name, field.Type.Kind())
				}
			}

			message := value.Interface().(error).Error()
			if strings.Contains(message, payloadMarker) || strings.Contains(message, "424242") {
				t.Errorf("a field other than the reason or the shape reached the error string: %s", message)
			}
			if !strings.Contains(message, "the reason") {
				t.Errorf("the error does not print its reason: %s", message)
			}
		})
	}

	// The same for the classified error, which prints its message and not the
	// provider, model, request IDs or item IDs it carries.
	err := &OperationError{
		Kind: KindMalformedOutput, Op: "extract", Message: "the response could not be parsed",
		Provider: payloadMarker, Model: payloadMarker, RequestID: payloadMarker,
		CorrelationID: payloadMarker, AttemptID: payloadMarker, ItemIDs: []string{payloadMarker},
		Cause: errors.New(payloadMarker),
	}
	if message := err.Error(); strings.Contains(message, payloadMarker) {
		t.Errorf("a field other than the message reached the error string: %s", message)
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
// "should I retry this" must not get yes and no from the same error. A kind has
// at most one; the kinds with none are listed, so that adding a kind without
// deciding is a failure here.
func TestEveryKindHasAtMostOneDisposition(t *testing.T) {
	noDisposition := map[ErrorKind]bool{KindUnknown: true, KindContextTooLarge: true, KindAdmissionRejected: true}
	for kind := KindUnknown; kind <= lastKind; kind++ {
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
		if (count == 0) != noDisposition[kind] {
			t.Errorf("%v has %d dispositions; the kinds with none are KindUnknown, KindContextTooLarge and KindAdmissionRejected", kind, count)
		}
	}
}
