package types

// Five operations -- Validate, Verify, Audit, Critique, Score -- were one
// operation shape wearing five vocabularies: a verdict, a list of things
// wrong, and a summary, each spelled with its own field names (Valid vs.
// OverallVerdict vs. PassesAudit vs. ModelOverallScore; ValidationIssue vs.
// ClaimVerification vs. AuditFinding vs. CritiqueIssue). A caller who wanted
// to write one piece of code that handled "the judgment came back bad" for
// more than one of these operations could not, because there was no one
// shape to write it against. JudgmentResult is that shape (OP-206).
//
// It is one of four result families a stable operation draws from: judgment
// (this one), transformation, collection, and text. An operation belongs to
// judgment when its answer is fundamentally "here is a verdict on the
// subject, and here is what's wrong with it" -- not "here is a new value"
// (transformation) and not "here is a subset/ordering/partition of what I
// gave you" (collection).

// Verdict is the outcome a judgment operation reached about its subject.
//
// It is deliberately coarse. The operations feeding into JudgmentResult each
// have their own richer vocabulary today (Validate's "error/warning/info",
// Verify's "verified/false/partially_true/misleading/unverifiable", Audit's
// severity-weighted PassesAudit) and that vocabulary is not lost -- it lives
// on each JudgmentIssue's Severity and Category. Verdict is the one-word
// answer to "did the subject pass," which is the question every one of these
// operations was actually being asked underneath its own words for it.
type Verdict string

const (
	// VerdictPass: no issue reached the operation's own failure threshold.
	VerdictPass Verdict = "pass"

	// VerdictFail: at least one issue did.
	VerdictFail Verdict = "fail"

	// VerdictMixed: the subject is partly right and partly wrong in a way
	// that neither Pass nor Fail describes honestly -- Verify's
	// "partially_true" and "misleading" land here rather than being forced
	// into a binary that would overstate or understate the finding.
	VerdictMixed Verdict = "mixed"

	// VerdictUnknown: the operation could not reach a verdict at all, such
	// as Verify's "unverifiable". This is not VerdictFail: the subject was
	// not shown to be wrong, only unresolved, and collapsing the two would
	// let a caller's failure-handling code fire on "I don't know."
	VerdictUnknown Verdict = "unknown"
)

// JudgmentIssue is one thing a judgment operation found wrong (or worth
// flagging) about its subject.
//
// The field names are deliberately generic across what used to be four
// unrelated types (ValidationIssue.Field, ClaimVerification.Claim,
// AuditFinding.Field, CritiqueIssue.Criterion all became Subject; Message
// absorbed Message/Description/Issue; Suggestion absorbed
// Suggestion/Recommendation/Fix). A field that only one operation ever
// populates is a field that should live in that operation's own
// ModelClaims entry, not here -- the fourth-membership-test problem
// AGENTS.md names for invariants applies just as much to result shapes.
type JudgmentIssue struct {
	// Subject is what this issue is about: a field path, a claim's text, a
	// location in the input, a criterion name. Empty when the issue applies
	// to the whole subject rather than a part of it.
	Subject string

	// Category buckets the issue: a validation severity's cause, Audit's
	// "security"/"compliance"/"quality", Verify's "logic"/"consistency"/
	// "fact", Critique's criterion. Free text, because the five operations
	// this feeds do not share one taxonomy and inventing one here would be
	// a sixth vocabulary rather than a collapse of the existing five.
	Category string

	// Severity is "critical", "error", "warning", or "info" -- the same
	// four levels Validate already used, onto which Audit's 0.0-1.0
	// severity float and Critique's "critical"/"major"/"minor" are mapped
	// deterministically in Go, not left for a model to spell three ways.
	Severity string

	// Message describes the problem. Never the caller's payload verbatim --
	// see AGENTS.md's rule against embedding input values in anything a
	// caller logs; a field name or a category is fine here, a data value is
	// not.
	Message string

	// Suggestion is how to fix it, when the operation produced one.
	Suggestion string

	// Evidence is the specific support for this issue: a quoted fragment,
	// a pattern that matched, a source index rendered as text. It is kept
	// as its own field rather than folded into Message so a caller can
	// choose to omit it from a log line while keeping the finding itself.
	Evidence []string

	// ModelConfidence is this issue's own claimed confidence, for
	// operations (Verify's per-claim confidence) that report one below the
	// result level. It is a claim, not a measurement, for the same reason
	// JudgmentResult.ModelConfidence is: the same process that produced the
	// finding produced the score for it.
	ModelConfidence float64
}

// JudgmentResult is the shared shape for an operation whose answer is a
// verdict on a subject, plus what's wrong with it.
//
// It keeps the four-compartment discipline Result[T]/Meta established
// (runtime facts / contract / checks / model claims) even though it does
// not embed Meta directly: Verdict and Issues are what the operation
// determined -- for Validate's deterministic rules, exactly, in Go, and for
// a model-assisted review, as reported by the model and treated as the
// operation's finding, not as a bare unlabelled claim. ModelConfidence and
// ModelClaims are what the model said about its own answer, kept apart on
// purpose: reading a model's self-score as though it were the verdict is
// D-09, half of the review this collapse is closing.
type JudgmentResult[T any] struct {
	// Subject is the input the judgment was made about.
	Subject T

	// Verdict is the one-word outcome. See the Verdict type.
	Verdict Verdict

	// Issues is everything found wrong, most-certain first when an
	// operation has a mix of deterministic and model-reported findings
	// (Validate's deterministic field-rule violations precede the model's).
	Issues []JudgmentIssue

	// Evidence is result-level supporting material that does not belong to
	// one issue -- distinct from JudgmentIssue.Evidence, which is scoped to
	// a single finding.
	Evidence []string

	// Summary is the operation's own one-paragraph account of the verdict.
	Summary string

	// --- Model claims. Kept out of Verdict and Issues above on purpose. ---

	// ModelConfidence is the model's own score for the result as a whole.
	// Not calibrated, not comparable across models or prompts, not a
	// measurement -- the same warning as Meta.ModelConfidence, and for the
	// same reason: it comes from the process being scored, not from an
	// independent check.
	ModelConfidence float64

	// ModelClaims holds whatever else the model asserted that does not fit
	// Verdict, Issues, or ModelConfidence: Verify's trust score, Critique's
	// per-criterion scores and top priorities, Validate's proposed
	// correction (unverified -- nothing checked that applying it actually
	// fixes the issues found, so it is a claim, not a delivered fact).
	ModelClaims map[string]any
}
