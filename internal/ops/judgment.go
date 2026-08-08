// package ops - shared helpers for the JudgmentResult collapse (OP-206).
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

// normalizeSeverity folds whatever a source operation called its severity
// levels onto the four Validate already used: "critical", "error",
// "warning", "info". An empty or unrecognized input becomes "info" rather
// than being dropped, because a finding with no severity is still a
// finding -- silently discarding it is the fail-open behavior AGENTS.md
// forbids.
func normalizeSeverity(s string) string {
	switch s {
	case "critical", "error", "warning", "info":
		return s
	case "major":
		return "error"
	case "minor":
		return "warning"
	case "high":
		return "error"
	case "medium":
		return "warning"
	case "low":
		return "info"
	default:
		return "info"
	}
}

// severityFromFraction maps Audit's 0.0-1.0 severity float onto the same
// four levels, using the thresholds buildAuditSummary already established
// (critical >= 0.9, high >= 0.7, medium >= 0.5, low >= 0.3, else info) so
// the JudgmentResult view of an audit finding agrees with AuditSummary
// about which findings are critical.
func severityFromFraction(f float64) string {
	switch {
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
