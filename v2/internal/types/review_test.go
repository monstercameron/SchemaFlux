package types

import (
	"errors"
	"strings"
	"testing"
)

func TestNewReviewRequiredReachesTheSentinel(t *testing.T) {
	err := NewReviewRequired(ReviewPacket[int]{Candidate: 7})
	if !errors.Is(err, ErrReviewRequired) {
		t.Fatalf("errors.Is(err, ErrReviewRequired) = false for %v", err)
	}
}

func TestReviewRequiredErrorIsReachableViaAs(t *testing.T) {
	packet := ReviewPacket[string]{
		Candidate:       "draft answer",
		InputRefs:       []string{"note.txt, 12 bytes"},
		Evidence:        []string{"rule R1 passed"},
		FailedChecks:    []string{"rule R2 failed"},
		Attempts:        3,
		SuggestedAction: "confirm the total",
	}
	err := NewReviewRequired(packet)

	var reviewErr *ReviewRequiredError[string]
	if !errors.As(err, &reviewErr) {
		t.Fatalf("errors.As did not reach *ReviewRequiredError[string]: %v", err)
	}
	if reviewErr.Packet.Candidate != "draft answer" {
		t.Errorf("Candidate = %q", reviewErr.Packet.Candidate)
	}
	if reviewErr.Packet.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", reviewErr.Packet.Attempts)
	}
}

func TestReviewRequiredErrorMessageNamesTheFailedChecks(t *testing.T) {
	err := NewReviewRequired(ReviewPacket[int]{
		FailedChecks: []string{"invariant survived regeneration", "evidence contradicted"},
	})
	msg := err.Error()
	if !strings.Contains(msg, "invariant survived regeneration") {
		t.Errorf("Error() = %q, missing the failed check", msg)
	}
}

func TestReviewRequiredErrorMessageOmitsTheCandidate(t *testing.T) {
	err := NewReviewRequired(ReviewPacket[string]{
		Candidate:    "the customer's private note contents",
		FailedChecks: []string{"policy requires review"},
	})
	if strings.Contains(err.Error(), "private note contents") {
		t.Errorf("Error() leaked the candidate: %q", err.Error())
	}
}

func TestReviewRequiredErrorMessageOmitsInputRefsAsData(t *testing.T) {
	// InputRefs are descriptions, not values, but Error() still should not
	// echo caller-controlled strings verbatim into every log line by default --
	// only FailedChecks (the library's own vocabulary) is printed.
	err := NewReviewRequired(ReviewPacket[int]{
		InputRefs:    []string{"customer_id=88213"},
		FailedChecks: []string{"policy requires review"},
	})
	if strings.Contains(err.Error(), "88213") {
		t.Errorf("Error() = %q, printed an input ref", err.Error())
	}
}

func TestReviewRequiredErrorWithNoFailedChecksStillHasAMessage(t *testing.T) {
	err := NewReviewRequired(ReviewPacket[int]{})
	if err.Error() == "" {
		t.Error("Error() is empty")
	}
}

func TestReviewPacketIsGenericOverArbitraryCandidateTypes(t *testing.T) {
	type record struct {
		Total float64
		Items int
	}
	packet := ReviewPacket[record]{Candidate: record{Total: 12.5, Items: 3}}
	err := NewReviewRequired(packet)

	var reviewErr *ReviewRequiredError[record]
	if !errors.As(err, &reviewErr) {
		t.Fatal("errors.As did not reach a struct-typed ReviewRequiredError")
	}
	if reviewErr.Packet.Candidate.Total != 12.5 || reviewErr.Packet.Candidate.Items != 3 {
		t.Errorf("Candidate = %+v", reviewErr.Packet.Candidate)
	}
}

func TestReviewRequiredIsDistinguishableFromOtherKinds(t *testing.T) {
	other := &OperationError{Kind: KindTimeout, Message: "slow"}
	if errors.Is(other, ErrReviewRequired) {
		t.Error("an unrelated OperationError matched the review sentinel")
	}

	reviewErr := NewReviewRequired(ReviewPacket[int]{})
	if errors.Is(reviewErr, ErrTimeout) {
		t.Error("a review-required error matched an unrelated sentinel")
	}
}

func TestOperationErrorKindReviewRequiredAlsoReachesTheSentinel(t *testing.T) {
	// KindReviewRequired already existed before this task; this asserts the
	// two producers of ErrReviewRequired (OperationError and
	// ReviewRequiredError) are both reachable the same way, so a caller
	// checking errors.Is(err, ErrReviewRequired) does not need to know which
	// one produced a given error.
	opErr := &OperationError{Kind: KindReviewRequired}
	if !errors.Is(opErr, ErrReviewRequired) {
		t.Error("OperationError{Kind: KindReviewRequired} does not reach ErrReviewRequired")
	}
	if !opErr.Terminal() {
		t.Error("KindReviewRequired is not terminal")
	}
}
