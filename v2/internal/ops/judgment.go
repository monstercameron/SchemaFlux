// Shared helpers for the JudgmentResult collapse (OP-206).
//
// Validate, Verify, Audit, and Critique each built their own verdict/issues/
// summary shape with its own field names. The conversion helpers here are
// the seam: each operation keeps its own LLM prompt and its own legacy
// result type (so a caller on the old name sees byte-for-byte the same
// behavior it always did), and only translates into types.JudgmentResult at
// the boundary. Putting the translation in one file means the severity and
// verdict mappings are decided once, not once per operation with a chance
// to disagree.

package ops

import (
	"math"
	"strings"
)

// severityUnknown is what a severity the mapping does not recognise becomes.
// It is not one of the four levels JudgmentIssue documents, and that is the
// point: a finding is never dropped, and its severity is never guessed. A
// caller sorting or filtering on severity sees a value that is not any of the
// four, and the verdict derived from the issues treats it as a finding that is
// not a pass.
const severityUnknown = "unknown"

// normalizeSeverity folds whatever a source operation called its severity
// levels onto the four Validate already used: "critical", "error",
// "warning", "info". Case and surrounding space are ignored. An empty or
// unrecognized input becomes severityUnknown and not "info": the severity of
// a review finding is what a caller sorts on, and reading "severe" or "fatal"
// as the least serious level hides the findings that matter most.
func normalizeSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical", "fatal", "blocker":
		return "critical"
	case "error", "err", "major", "high":
		return "error"
	case "warning", "warn", "minor", "medium":
		return "warning"
	case "info", "informational", "notice", "low":
		return "info"
	default:
		return severityUnknown
	}
}

// severityFromFraction maps Audit's 0.0-1.0 severity float onto the same
// four levels, using the thresholds buildAuditSummary already established
// (critical >= 0.9, high >= 0.7, medium >= 0.5, low >= 0.3, else info) so
// the JudgmentResult view of an audit finding agrees with AuditSummary
// about which findings are critical. A value that is not a number, or is
// negative, is severityUnknown.
func severityFromFraction(f float64) string {
	switch {
	case math.IsNaN(f) || f < 0:
		return severityUnknown
	case f >= 0.9:
		return "critical"
	case f >= 0.7:
		return "error"
	case f >= 0.5:
		return "warning"
	default:
		return "info"
	}
}

// evidenceSlice turns a single evidence string into the []string
// JudgmentIssue.Evidence expects, omitting it entirely when empty rather
// than carrying a slice with one blank entry.
func evidenceSlice(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}
