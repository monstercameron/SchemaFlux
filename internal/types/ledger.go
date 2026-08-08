package types

import (
	"fmt"
	"strings"
)

// PL-014: the post-execution half of planner explainability.
//
// Plan.Explain() (plan.go) is the PRE-execution half: what would run, before
// it runs. It cannot say what actually happened during a run that adapts as
// it goes -- AdaptiveChunkState (internal/ops/adaptive.go) grows and halves
// a chunk size in response to what a real call came back with, and until
// this file existed nothing collected those decisions anywhere a caller
// could read them back. "Adaptive routing that cannot explain itself is
// unauditable" is TODOS.md's own line for why this exists: a caller asking
// "why did this take 40 calls instead of the 12 the plan estimated" needs
// the sequence of halvings and the reason each one happened, not just the
// final chunk count.

// DecisionEntry is one adaptive decision made during a run, in the order it
// happened.
type DecisionEntry struct {
	// Node identifies what the decision was about -- a chunk's position in
	// the run ("chunk 3, items 40-59") or a stage name, never the items
	// themselves.
	Node string

	// Decision is what changed, or what stayed the same and why it was
	// considered: "chunk size held at 20", "chunk size halved to 10",
	// "isolate pass 1 over 3 unresolved items", "atomic fallback for 2
	// items".
	Decision string

	// Reason is why -- read from the same source that made the decision
	// (AdaptiveChunkState.Reason(), for a sizing decision) rather than
	// reconstructed after the fact from the outcome alone, so the reason
	// text can never drift from the logic that actually produced the
	// decision.
	Reason string
}

// DecisionLedger is the ordered record of every adaptive decision a run
// made. The zero value is an empty ledger -- correct and honest for an
// engine that makes no adaptive decisions at all (RunOpManyPartial's
// one-call-per-item dispatch has nothing to record), not a sentinel a caller
// has to special-case.
type DecisionLedger struct {
	Entries []DecisionEntry
}

// Record appends one decision to the ledger.
func (l *DecisionLedger) Record(node, decision, reason string) {
	l.Entries = append(l.Entries, DecisionEntry{Node: node, Decision: decision, Reason: reason})
}

// Explain renders the ledger as a human-readable, ordered list -- PL-014's
// "every adaptive decision in a run appears in the ledger with a reason",
// rendered directly rather than requiring a caller to walk Entries by hand.
func (l DecisionLedger) Explain() string {
	if len(l.Entries) == 0 {
		return "no adaptive decisions were made during this run"
	}
	lines := make([]string, 0, len(l.Entries))
	for i, e := range l.Entries {
		lines = append(lines, fmt.Sprintf("%d. %s: %s (%s)", i+1, e.Node, e.Decision, e.Reason))
	}
	return strings.Join(lines, "\n")
}
