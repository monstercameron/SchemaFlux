package ops

import (
	"fmt"
	"sync"
)

// Adaptive chunk sizing, bounded.
//
// packChunks (plan.go) answers "what is the largest chunk this input and
// this budget allow", computed once, with no memory of any earlier call.
// AdaptiveChunkState is the layer on top: a size that grows on sustained
// compliance and halves on the failure signals a real run produces --
// truncation, omissions, duplicate ids, a malformed protocol, or a repair rate
// above threshold -- while never exceeding the operation's own ceiling or the
// deterministic per-request limit packChunks already computed.
//
// It cannot be exercised by Preflight, which by construction makes no
// provider call and therefore observes no compliance or failure signal. The
// recovering run (recover.go) feeds it the first-pass outcome of every top-level
// chunk and reads an adjusted size back, one chunk boundary at a time. History is
// advisory and local to the state value -- nothing here is package-level or
// shared across goroutines or tenants, so two AdaptiveChunkState values never
// see each other's history and a provider's limits cannot be bypassed by
// claiming urgency.

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
// across successive MDSP calls. It is safe for concurrent use. The zero value is
// a state of size one, bounded to one, which is what NewAdaptiveChunkState(0, 0)
// returns; a nil *AdaptiveChunkState reports size one and records nothing.
type AdaptiveChunkState struct {
	mu sync.Mutex

	// size is the current chunk size, always within [1, ceiling].
	size    int
	ceiling int

	complianceRun int
	repairRun     int

	// reason is why the size last changed, or why it has not: the reason for
	// each change, which the decision ledger reads from this same field.
	reason string
}

// NewAdaptiveChunkState starts adaptive sizing at start, bounded to
// [1, ceiling]. ceiling is the deterministic per-request limit that packChunks
// already computed for this operation and budget -- a deterministic
// per-request limit always wins -- so growth here can never
// exceed what a single-shot plan would have allowed anyway.
func NewAdaptiveChunkState(start, ceiling int) *AdaptiveChunkState {
	if ceiling < 1 {
		ceiling = 1
	}
	if start > ceiling {
		start = ceiling
	}
	if start < 1 {
		start = 1
	}
	return &AdaptiveChunkState{size: start, ceiling: ceiling, reason: "initial size"}
}

// start gives a state that was never built by NewAdaptiveChunkState the values its
// constructor gives for no bounds: one, within one. It is called with mu held.
func (s *AdaptiveChunkState) start() {
	if s.ceiling < 1 {
		s.ceiling = 1
	}
	if s.size < 1 {
		s.size = 1
	}
	if s.reason == "" {
		s.reason = "initial size"
	}
}

// Size returns the current chunk size.
func (s *AdaptiveChunkState) Size() int {
	if s == nil {
		return 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.start()
	return s.size
}

// Reason returns why the size is what it is, for a decision ledger entry.
func (s *AdaptiveChunkState) Reason() string {
	if s == nil {
		return "no adaptive state"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.start()
	return s.reason
}

// Record feeds one chunk's outcome into the state, adjusting size and
// resetting the run counters that do not apply to it. A value that is not one
// of the outcomes is left out of the history and says so in Reason, rather
// than counting as neither good nor bad without a trace.
func (s *AdaptiveChunkState) Record(outcome AdaptiveOutcome) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.start()
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
		if s.size >= s.ceiling {
			// Nothing to grow into: the run is not counted, so Reason never
			// reads "47/3 before growing". The run is already zero here, since
			// the size reaches the ceiling by growing or by starting there, and
			// both leave it at zero.
			s.reason = fmt.Sprintf("compliant, and already at the ceiling of %d", s.ceiling)
			return
		}
		s.complianceRun++
		if s.complianceRun >= sustainedComplianceRun {
			s.grow()
			s.complianceRun = 0
			return
		}
		s.reason = fmt.Sprintf("compliant (%d/%d before growing)", s.complianceRun, sustainedComplianceRun)

	default:
		s.reason = fmt.Sprintf("ignored an outcome that is not one of the six (%d), so the size is unchanged", int(outcome))
	}
}

func (s *AdaptiveChunkState) halve(cause AdaptiveOutcome) {
	previous := s.size
	if previous <= 1 {
		s.reason = fmt.Sprintf("kept at the minimum size of 1 after %s", cause)
		return
	}
	s.size = previous / 2
	s.reason = fmt.Sprintf("halved from %d to %d after %s", previous, s.size, cause)
}

func (s *AdaptiveChunkState) grow() {
	previous := s.size
	next := int(float64(s.size) * growthFactor)
	if next <= previous {
		next = previous + 1
	}
	if next > s.ceiling {
		next = s.ceiling
	}
	s.size = next
	s.reason = fmt.Sprintf("grew from %d to %d after %d consecutive compliant chunks", previous, s.size, sustainedComplianceRun)
}
