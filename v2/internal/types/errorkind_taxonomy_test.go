package types

import (
	"errors"
	"fmt"
	"testing"
)

// There is one taxonomy: for an error this package defines, the kind KindOf
// reports is the kind whose sentinel errors.Is matches, and no other sentinel.
// The table is every constructor and every sentinel the package has. A case is
// the error, the kind it stands for, and whether errors.Is is also expected to
// reach the kinds of errors it wraps.

type taxonomyCase struct {
	name string
	err  error
	kind ErrorKind
}

// everyKind is each kind with a name, KindUnknown excluded.
func everyKind() []ErrorKind {
	var kinds []ErrorKind
	for kind := KindConfiguration; kind <= lastKind; kind++ {
		kinds = append(kinds, kind)
	}
	return kinds
}

func taxonomyCases() []taxonomyCase {
	var cases []taxonomyCase
	legacy := func(name string, build func(cause error) error, kind ErrorKind) {
		cases = append(cases, taxonomyCase{name + " around a classified error", build(Newf(kind, "op", "m")), kind})
	}

	for _, kind := range everyKind() {
		label := kind.String()
		cases = append(cases,
			taxonomyCase{"the sentinel for " + label, sentinelFor(kind), kind},
			taxonomyCase{"Newf " + label, Newf(kind, "extract", "something about %d items", 3), kind},
			taxonomyCase{"Wrap " + label, Wrap(kind, "extract", errors.New("underneath")), kind},
			taxonomyCase{"a literal OperationError " + label, &OperationError{Kind: kind, Op: "x", Message: "m", Ambiguous: true}, kind},
			taxonomyCase{"a classified error wrapped with %w, " + label, fmt.Errorf("context: %w", Newf(kind, "op", "m")), kind},
			taxonomyCase{"the sentinel wrapped with %w, " + label, fmt.Errorf("context: %w", sentinelFor(kind)), kind},
			taxonomyCase{"a join of an unclassified error and " + label, errors.Join(errors.New("first"), Newf(kind, "op", "m")), kind},
		)
		legacy("ClassifyError", func(c error) error { return ClassifyError{Reason: "r", Categories: []string{"a"}, Err: c} }, kind)
	}

	// The error types of one operation each, around a classified error and
	// around nothing.
	around := Newf(KindSchemaViolation, "op", "m")
	for name, err := range map[string]error{
		"ScoreError":     ScoreError{Reason: "r", Err: around},
		"CompareError":   CompareError{Reason: "r", Err: around},
		"ChooseError":    ChooseError{Reason: "r", Err: around},
		"FilterError":    FilterError{Reason: "r", Err: around},
		"SortError":      SortError{Reason: "r", Err: around},
		"ExtractError":   ExtractError{Reason: "r", Err: around},
		"TransformError": TransformError{Reason: "r", Err: around},
		"GenerateError":  GenerateError{Reason: "r", Err: around},
		"SummarizeError": SummarizeError{Reason: "r", Err: around},
		"RewriteError":   RewriteError{Reason: "r", Err: around},
		"TranslateError": TranslateError{Reason: "r", Err: around},
		"ExpandError":    ExpandError{Reason: "r", Err: around},
	} {
		cases = append(cases, taxonomyCase{name + " around a schema violation", err, KindSchemaViolation})
	}
	for name, err := range map[string]error{
		"ClassifyError":  ClassifyError{Reason: "r"},
		"ScoreError":     ScoreError{Reason: "r"},
		"CompareError":   CompareError{Reason: "r"},
		"ChooseError":    ChooseError{Reason: "r"},
		"FilterError":    FilterError{Reason: "r"},
		"SortError":      SortError{Reason: "r"},
		"ExtractError":   ExtractError{Reason: "r"},
		"TransformError": TransformError{Reason: "r"},
		"GenerateError":  GenerateError{Reason: "r"},
		"SummarizeError": SummarizeError{Reason: "r"},
		"RewriteError":   RewriteError{Reason: "r"},
		"TranslateError": TranslateError{Reason: "r"},
		"ExpandError":    ExpandError{Reason: "r"},
	} {
		cases = append(cases, taxonomyCase{name + " around nothing", err, KindUnknown})
	}

	// The generic review error, with and without a packet, and wrapped.
	review := NewReviewRequired(ReviewPacket[string]{Candidate: "c", FailedChecks: []string{"total"}})
	cases = append(cases,
		taxonomyCase{"NewReviewRequired", review, KindReviewRequired},
		taxonomyCase{"NewReviewRequired with an empty packet", NewReviewRequired(ReviewPacket[int]{}), KindReviewRequired},
		taxonomyCase{"a review error wrapped with %w", fmt.Errorf("approve: %w", review), KindReviewRequired},
		taxonomyCase{"a review error and another error joined", errors.Join(errors.New("other"), review), KindReviewRequired},
		taxonomyCase{"a ReviewRequiredError literal", &ReviewRequiredError[struct{ N int }]{}, KindReviewRequired},
	)

	// Nothing classified.
	cases = append(cases,
		taxonomyCase{"a plain error", errors.New("plain"), KindUnknown},
		taxonomyCase{"a wrapped plain error", fmt.Errorf("w: %w", errors.New("plain")), KindUnknown},
		taxonomyCase{"a sentinel of this package that is not a kind", ErrEndpointNotAllowed, KindUnknown},
		taxonomyCase{"the data policy conflict", ErrDataPolicyConflict, KindUnknown},
		taxonomyCase{"an OperationError of no kind", &OperationError{Op: "x"}, KindUnknown},
		taxonomyCase{"a nil error", nil, KindUnknown},
	)
	return cases
}

func TestKindOfAndErrorsIsAgreeForEveryErrorTheTypesPackageDefines(t *testing.T) {
	cases := taxonomyCases()
	if len(cases) < 150 {
		t.Fatalf("the table has %d cases", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := KindOf(tc.err); got != tc.kind {
				t.Errorf("KindOf = %v, want %v", got, tc.kind)
			}
			for _, kind := range everyKind() {
				want := kind == tc.kind
				if got := errors.Is(tc.err, sentinelFor(kind)); got != want {
					t.Errorf("errors.Is(err, %v) = %v, want %v (KindOf says %v)", sentinelFor(kind), got, want, KindOf(tc.err))
				}
			}
		})
	}
}

// Every kind has a sentinel and a name, there is no kind above lastKind, and the
// sentinels are distinct.
func TestEveryKindHasASentinelAndAName(t *testing.T) {
	seen := map[error]ErrorKind{}
	for _, kind := range everyKind() {
		sentinel := sentinelFor(kind)
		if sentinel == nil {
			t.Errorf("%v has no sentinel", kind)
			continue
		}
		if other, dup := seen[sentinel]; dup {
			t.Errorf("%v and %v share a sentinel", kind, other)
		}
		seen[sentinel] = kind
		if kind.String() == "unknown" {
			t.Errorf("kind %d has no name", kind)
		}
	}
	if sentinelFor(lastKind+1) != nil || (lastKind+1).String() != "unknown" {
		t.Errorf("kind %d exists beyond lastKind: it has a sentinel or a name, so lastKind is out of date", lastKind+1)
	}
	if sentinelFor(KindUnknown) != nil {
		t.Error("KindUnknown has a sentinel")
	}
}

// errors.Is of an OperationError is true of its own kind's sentinel and of
// nothing else: not of another error of the same kind, and not of something that
// only wraps the sentinel.
func TestAnOperationErrorMatchesItsSentinelAndNothingElse(t *testing.T) {
	first := Newf(KindTimeout, "extract", "the first")
	second := Newf(KindTimeout, "classify", "the second")
	cases := []struct {
		name   string
		target error
		want   bool
	}{
		{"its own sentinel", ErrTimeout, true},
		{"another error of the same kind", second, false},
		{"an error that wraps its sentinel", fmt.Errorf("wrapped: %w", ErrTimeout), false},
		{"an OperationError wrapping its sentinel", Wrap(KindTimeout, "x", ErrTimeout), false},
		{"itself", first, true},
		{"another kind's sentinel", ErrRateLimited, false},
		{"another kind's error", Newf(KindRateLimited, "extract", "the first"), false},
		{"a plain error", errors.New("timeout"), false},
		{"a plain error with the sentinel's text", errors.New(ErrTimeout.Error()), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := errors.Is(first, tc.target); got != tc.want {
				t.Errorf("errors.Is(first, target) = %v, want %v", got, tc.want)
			}
		})
	}
}

// When one classified error wraps another, KindOf is the outermost kind, and
// errors.Is also reaches the inner one's sentinel because errors.Is walks the
// chain. This is the one place the two are not the same question, and it is
// stated here so that it is not found by accident.
func TestAClassifiedErrorAroundAnotherReportsTheOuterKindAndMatchesBoth(t *testing.T) {
	inner := Newf(KindAuthentication, "llm.call", "the key was refused")
	outer := Wrap(KindProviderUnavailable, "extract", inner)

	if got := KindOf(outer); got != KindProviderUnavailable {
		t.Errorf("KindOf = %v, want the outer kind", got)
	}
	if !errors.Is(outer, ErrProviderUnavailable) || !errors.Is(outer, ErrAuthentication) {
		t.Error("errors.Is does not reach both the outer and the inner kind")
	}
	if errors.Is(outer, ErrRateLimited) {
		t.Error("errors.Is matched a kind nothing in the chain has")
	}

	// A review requirement around a classified error is a review requirement.
	review := fmt.Errorf("vote: %w", NewReviewRequired(ReviewPacket[string]{}))
	if KindOf(review) != KindReviewRequired {
		t.Errorf("KindOf = %v for a wrapped review error", KindOf(review))
	}
	if KindOf(Wrap(KindRepairExhausted, "vote", NewReviewRequired(ReviewPacket[string]{}))) != KindRepairExhausted {
		t.Error("the outer kind did not win over an inner review error")
	}
}

func TestKindOfSurvivesATypedNilError(t *testing.T) {
	var operationErr *OperationError
	var reviewErr *ReviewRequiredError[string]
	if got := KindOf(operationErr); got != KindUnknown {
		t.Errorf("KindOf((*OperationError)(nil)) = %v", got)
	}
	if got := KindOf(reviewErr); got != KindReviewRequired {
		t.Errorf("KindOf((*ReviewRequiredError)(nil)) = %v", got)
	}
}
