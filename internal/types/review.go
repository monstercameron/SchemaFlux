package types

import (
	"errors"
	"strings"
)

// ReviewPacket is a terminal outcome, and a successful one -- not a failure
// dressed up as a struct.
//
// Automated recovery has several dead ends that all mean the same thing: the
// library has spent its budget and cannot certify an answer by itself. An
// invariant that survives its regenerations, providers that disagree
// materially, evidence that contradicts itself, a policy that names a human --
// each of those used to become either a bare error the caller could not act on,
// or (worse) a plausible-looking answer nobody checked. This is the one shape
// behind all of them, so a caller that knows how to route a review knows how to
// route every one of them, not just Approve's.
type ReviewPacket[T any] struct {
	// Candidate is the answer the operation was about to return -- the value
	// itself, not a description. Whatever receives this packet needs to be
	// able to look at what is being reviewed; that is different from the
	// caller's other data, which InputRefs below deliberately does not carry.
	//
	// It is the zero value of T when there was no candidate to show -- an
	// invariant that never produced a usable answer at all has nothing here.
	Candidate T

	// InputRefs describes the inputs that produced (or failed to produce)
	// Candidate, by reference and shape rather than value. types.DescribeValue
	// exists for building these without reproducing the caller's data: a
	// review packet that embeds the caller's records is a review packet that
	// leaks them into whatever handles it.
	InputRefs []string

	// Evidence is whatever the operation can say in the candidate's favor:
	// rule names, sample-agreement counts, which providers concurred. These
	// are the library's own findings, not the caller's data.
	Evidence []string

	// FailedChecks names the deterministic checks the candidate did not pass,
	// or the reason automated recovery gave up.
	FailedChecks []string

	// Attempts is how many regenerations or samples were spent before this was
	// raised.
	Attempts int

	// SuggestedAction is a short, human-facing hint about what would resolve
	// it: "confirm the total", "pick between two providers' answers". It is
	// a hint, not a workflow -- the library stops here.
	SuggestedAction string
}

// ReviewRequiredError carries a ReviewPacket behind the shared
// ErrReviewRequired sentinel.
//
// It exists as a distinct, generic type -- rather than folding the packet into
// *OperationError -- because OperationError is not generic and Candidate has
// to keep its real type for whatever handles the review to use it. Is() still
// makes it reachable the same way every other kind is: a caller writes
// errors.Is(err, types.ErrReviewRequired) without knowing this type exists,
// and errors.As(err, &reviewErr) only when it wants the packet.
type ReviewRequiredError[T any] struct {
	Packet ReviewPacket[T]
}

// Error deliberately omits Candidate and InputRefs. Every caller logs the
// error it returns, and both are the caller's data, not the library's to
// print into a log line.
func (e *ReviewRequiredError[T]) Error() string {
	message := "review required"
	if len(e.Packet.FailedChecks) > 0 {
		message += ": " + strings.Join(e.Packet.FailedChecks, ", ")
	}
	return message
}

// Is reports true for ErrReviewRequired, so errors.Is reaches the sentinel
// without a caller needing to import or type-assert ReviewRequiredError.
func (e *ReviewRequiredError[T]) Is(target error) bool {
	return errors.Is(target, ErrReviewRequired)
}

// NewReviewRequired builds the sentinel-bearing error from a packet. It is the
// one place a ReviewPacket is turned into an error, so every producer of a
// review outcome -- Approve, Vote, and whatever comes after them -- constructs
// the same shape of error the same way.
func NewReviewRequired[T any](packet ReviewPacket[T]) error {
	return &ReviewRequiredError[T]{Packet: packet}
}
