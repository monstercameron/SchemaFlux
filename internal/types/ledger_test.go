package types

import (
	"strings"
	"testing"
)

// The zero ledger is correct and honest for an engine that made no adaptive
// decisions -- it must say so plainly rather than rendering an empty list.
func TestDecisionLedgerExplainEmptyLedger(t *testing.T) {
	var l DecisionLedger
	if got, want := l.Explain(), "no adaptive decisions were made during this run"; got != want {
		t.Errorf("Explain() = %q, want %q", got, want)
	}
}

func TestDecisionLedgerRecordAndExplainOrderedWithReasons(t *testing.T) {
	var l DecisionLedger
	l.Record("chunk 1, items 0-19", "chunk size held at 20", "first pass fit within bounds")
	l.Record("chunk 1, items 0-19", "chunk size halved to 10", "MDSP response dropped 3 items")
	l.Record("isolate pass 1", "atomic fallback for 2 items", "isolate pass still missing items")

	explain := l.Explain()
	lines := strings.Split(explain, "\n")
	if len(lines) != 3 {
		t.Fatalf("Explain() produced %d lines, want 3", len(lines))
	}
	if !strings.HasPrefix(lines[0], "1. chunk 1, items 0-19: chunk size held at 20 (first pass fit within bounds)") {
		t.Errorf("line 1 = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "2. chunk 1, items 0-19: chunk size halved to 10 (MDSP response dropped 3 items)") {
		t.Errorf("line 2 = %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "3. isolate pass 1: atomic fallback for 2 items (isolate pass still missing items)") {
		t.Errorf("line 3 = %q", lines[2])
	}

	if len(l.Entries) != 3 {
		t.Fatalf("Entries has %d entries, want 3", len(l.Entries))
	}
}
