package schemaflux

import (
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Verdict is the outcome a judgment reached about its subject. It is coarse on
// purpose: the richer vocabulary of each operation is on the Severity and
// Category of the issues.
type Verdict string

const (
	// VerdictUnknown is no verdict: the operation could not settle the question,
	// as Verify does for a text whose claims it cannot establish. It is not a
	// failure, and the zero Verdict, the empty string, is not it either.
	VerdictUnknown Verdict = "unknown"

	// VerdictPass is a subject in which no issue reached the operation's own
	// failure line.
	VerdictPass Verdict = "pass"

	// VerdictFail is a subject in which at least one issue did.
	VerdictFail Verdict = "fail"

	// VerdictMixed is a subject that is partly right and partly wrong in a way
	// that neither pass nor fail describes: Verify's partly true and misleading
	// claims, and a critique with issues and none of them critical.
	VerdictMixed Verdict = "mixed"
)

// Judgment is a verdict on a subject and what was found wrong with it. It is the
// part of the answer that Validate, Verify, Audit and Critique share, and each of
// their results embeds it, so res.Verdict and res.Issues read the same for all
// four and a function can take res.Judgment.
type Judgment[T any] struct {
	// Subject is what was judged: the value given, or for Verify the text.
	Subject T

	// Verdict is the one-word outcome. It is worked out here from the issues and
	// never taken from the model; for Validate, a verdict of invalid that the model
	// volunteers is not overruled.
	Verdict Verdict

	// Issues is everything found, the most certain first. Nil when none.
	Issues []JudgmentIssue

	// Evidence is support that belongs to no single issue. Only Verify fills it,
	// with the evidence of every claim.
	Evidence []string

	// Summary is a short account. For Audit and for a Deterministic Validate it is
	// a count written here and not the model's.
	Summary string

	// ModelConfidence is the model's own score for the whole. It is a claim and not
	// a measurement. Zero when no model was asked, and always for Audit.
	ModelConfidence float64
}

// JudgmentIssue is one finding of a judgment.
type JudgmentIssue struct {
	// Subject is what the finding is about: a field path, a claim, a criterion.
	// Empty when it is about the whole.
	Subject string

	// Category is the operation's own bucket for the finding: "security",
	// "fact:false", "logic:circular", or a criterion.
	Category string

	// Severity is "critical", "error", "warning" or "info", mapped here from the
	// operation's own scale.
	Severity string

	// Message is the problem, in words that are never the caller's data.
	Message string

	// Suggestion is how to fix it, when one was given.
	Suggestion string

	// Rule is the caller's rule or policy that the finding breaks, in the caller's
	// words. Empty when it breaks none.
	Rule string

	// Evidence is support for this finding. For Critique, it is where in the value
	// the finding is.
	Evidence []string

	// ModelConfidence is the model's own claim for this finding, when it gave one.
	ModelConfidence float64
}

// judgmentOf is the judgment of the internal call as the root declares it. The
// two have the same fields, so the issues are the one part that is copied: one
// allocation, made only when there are some. The zero judgment, which the
// internal call returns beside an error, converts to the zero judgment.
func judgmentOf[T any](j types.JudgmentResult[T]) Judgment[T] {
	return Judgment[T]{
		Subject:         j.Subject,
		Verdict:         Verdict(j.Verdict),
		Issues:          issuesOf(j.Issues),
		Evidence:        j.Evidence,
		Summary:         j.Summary,
		ModelConfidence: j.ModelConfidence,
	}
}

// issuesOf is a list of internal issues as the root declares them, and nil for
// none.
func issuesOf(in []types.JudgmentIssue) []JudgmentIssue {
	if len(in) == 0 {
		return nil
	}
	out := make([]JudgmentIssue, len(in))
	for i, issue := range in {
		out[i] = JudgmentIssue(issue)
	}
	return out
}
