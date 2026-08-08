package ops

import "fmt"

// PL-005: adaptive chunk sizing, bounded.
//
// packChunks (plan.go) answers "what is the largest chunk this input and
// this budget allow", computed once, with no memory of any earlier call.
// AdaptiveChunkState is the layer on top that PL-005 asks for: a size that
// grows on sustained compliance and halves on the failure signals a real
// run produces -- truncation, omissions, duplicate ids, a malformed
// protocol, or a repair rate above threshold -- while never exceeding the
// operation's own ceiling or the deterministic per-request limit
// packChunks already computed.
//
// It cannot be exercised by Preflight, which by construction makes no
// provider call and therefore observes no compliance or failure signal.
// It exists for a caller executing a plan's MDSP chunks in sequence (or a
// future scheduler, SC-001) to feed real outcomes into and read an adjusted
// size back from, one chunk boundary at a time. History is advisory and
// local to the state value -- nothing here is package-level or shared
// across goroutines or tenants, which is TRU-15's "no bypassing locked
// provider or data-policy limits by claiming urgency" made structurally
// true rather than merely documented: two AdaptiveChunkState values never
// see each other's history.

// AdaptiveOutcome classifies what happened after one MDSP chunk ran, for
// AdaptiveChunkState.Record to react to.
type AdaptiveOutcome uint8

const (
	// OutcomeCompliant: the response covered every offered id exactly once
	// and decoded cleanly. Sustained compliance is what is allowed to grow
	// the size.
	OutcomeCompliant AdaptiveOutcome = iota

	// OutcomeTruncated: the response was cut off before it could be parsed
	// at all -- the strongest signal that the chunk was too large.
	OutcomeTruncated

	// OutcomeOmission: the response covered fewer ids than were offered.
	OutcomeOmission

	// OutcomeDuplicateID: the response answered the same id more than once.
	OutcomeDuplicateID

	// OutcomeMalformedProtocol: the response was not the expected shape at
	// all (unparseable JSON, wrong top-level shape).
	OutcomeMalformedProtocol

	// OutcomeRepaired: the response needed a repair pass to become valid.
	// One repair alone does not halve the size; RepairRateThreshold
	// consecutive repairs does, because a chunk size that always needs one
	// repair is not "working", even though every individual attempt
	// eventually succeeds.
	OutcomeRepaired
)

// String names the outcome for a decision-log reason string.
func (o AdaptiveOutcome) String() string {
	switch o {
	case OutcomeCompliant:
		return "compliant"
	case OutcomeTruncated:
		return "truncated"
	case OutcomeOmission:
		return "omission"
	case OutcomeDuplicateID:
		return "duplicate id"
	case OutcomeMalformedProtocol:
		return "malformed protocol"
	case OutcomeRepaired:
		return "repaired"
	default:
		return "unknown outcome"
	}
}

// halves reports whether a single occurrence of this outcome halves the
// chunk size immediately, without waiting for a run of them the way
// OutcomeRepaired's RepairRateThreshold does.
func (o AdaptiveOutcome) halves() bool {
	switch o {
	case OutcomeTruncated, OutcomeOmission, OutcomeDuplicateID, OutcomeMalformedProtocol:
		return true
	default:
		return false
	}
}

// growthFactor is how much a size grows after sustained compliance. Chosen
// smaller than the halving factor (which is exactly 1/2) on purpose: a
// wrong grow costs one bad chunk before the next failure halves it back,
// while a wrong halving that undershoots costs only efficiency, not
// correctness, so the size is quicker to shrink than to regrow -- the same
// asymmetry a TCP congestion window uses for the same reason.
const growthFactor = 1.25

// sustainedComplianceRun is how many consecutive OutcomeCompliant results
// are required before the size grows.
const sustainedComplianceRun = 3

// RepairRateThreshold is how many consecutive OutcomeRepaired results are
// required before the size halves. Below this, a repair is treated as the
// existing repair budget working as designed, not as a signal the chunk
// itself is wrong.
const RepairRateThreshold = 3

// AdaptiveChunkState tracks one operation invocation's chunk-size history
// across successive MDSP calls.
type AdaptiveChunkState struct {
	// size is the current chunk size, always within [1, max].
	size int
	max  int
	min  int

	complianceRun int
	repairRun     int

	// reason is why the size last changed, or why it has not: PL-005's
	// "record the reason for each change" and PL-014's plan-explanation
	// requirement, read from the same field.
	reason string
}

// NewAdaptiveChunkState starts adaptive sizing at start, bounded to
// [1, max]. max is the deterministic per-request limit that packChunks
// already computed for this operation and budget -- PL-005's "a
// deterministic per-request limit always wins" -- so growth here can never
// exceed what a single-shot plan would have allowed anyway.
func NewAdaptiveChunkState(start, max int) *AdaptiveChunkState {
	if max < 1 {
		max = 1
	}
	if start > max {
		start = max
	}
	if start < 1 {
		start = 1
	}
	return &AdaptiveChunkState{size: start, max: max, min: 1, reason: "initial size"}
}

// Size returns the current chunk size.
func (s *AdaptiveChunkState) Size() int { return s.size }

// Reason returns why the size is what it is, for a decision ledger entry.
func (s *AdaptiveChunkState) Reason() string { return s.reason }

// Record feeds one chunk's outcome into the state, adjusting size and
// resetting the run counters that do not apply to it.
func (s *AdaptiveChunkState) Record(outcome AdaptiveOutcome) {
	switch {
	case outcome.halves():
		s.repairRun = 0
		s.complianceRun = 0
		s.halve(outcome)

	case outcome == OutcomeRepaired:
		s.complianceRun = 0
		s.repairRun++
		if s.repairRun >= RepairRateThreshold {
			s.repairRun = 0
			s.halve(outcome)
			return
		}
		s.reason = fmt.Sprintf("repaired (%d/%d before halving)", s.repairRun, RepairRateThreshold)

	case outcome == OutcomeCompliant:
		s.repairRun = 0
		s.complianceRun++
		if s.complianceRun >= sustainedComplianceRun && s.size < s.max {
			s.grow()
			s.complianceRun = 0
			return
		}
		s.reason = fmt.Sprintf("compliant (%d/%d before growing)", s.complianceRun, sustainedComplianceRun)
	}
}

func (s *AdaptiveChunkState) halve(cause AdaptiveOutcome) {
	previous := s.size
	next := s.size / 2
	if next < s.min {
		next = s.min
	}
	s.size = next
	s.reason = fmt.Sprintf("halved from %d to %d after %s", previous, s.size, cause)
}

func (s *AdaptiveChunkState) grow() {
	previous := s.size
	next := int(float64(s.size) * growthFactor)
	if next <= previous {
		next = previous + 1
	}
	if next > s.max {
		next = s.max
	}
	s.size = next
	s.reason = fmt.Sprintf("grew from %d to %d after %d consecutive compliant chunks", previous, s.size, sustainedComplianceRun)
}
