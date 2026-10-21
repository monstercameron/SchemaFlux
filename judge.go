package schemaflux

import (
	"context"
	"math"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
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

// ValidateOptions are Validate's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: ValidateOptions{Model: "gpt-6-luna", FieldRules: rules}.
type ValidateOptions struct {
	Options

	// Rules are requirements in plain language, for a model. Empty means none.
	Rules string

	// FieldRules give a rule for a field, keyed by field name. Nil means none.
	FieldRules map[string]string

	// SchemaHints describe fields for the model, keyed by field name. Nil means
	// none.
	SchemaHints map[string]string

	// Deterministic never calls a model: a rule Go cannot decide, any Rules and any
	// SchemaHints are a configuration error. False lets a model judge them. The
	// context and the seven common settings are not used for the call, since
	// nothing is sent; a bad one is still refused.
	Deterministic bool

	// FailOn is the least severity that fails the validation: "error", "warning" or
	// "info". Empty means "error". Any other value is a configuration error.
	FailOn string

	// OmitCorrection leaves the proposed correction out of the request. False asks
	// for one.
	OmitCorrection bool

	// OmitExplanations leaves the per-issue explanation out of the request. False
	// asks for it.
	OmitExplanations bool
}

// ValidateResult is a value's validation.
type ValidateResult[T any] struct {
	Judgment[T]

	// Corrected is the model's proposed fix: the data with the correction laid over
	// it. Nothing checked that it resolves the issues. Nil when none was proposed,
	// and when OmitCorrection is set.
	Corrected *T `json:"corrected,omitempty"`

	// DeterministicOnly reports that every rule was decided in Go and no model was
	// asked.
	DeterministicOnly bool `json:"deterministic_only,omitempty"`
}

// mergeValidateOptions merges the values of a call (M0).
func mergeValidateOptions(opts []ValidateOptions) ValidateOptions {
	return mergeValues(opts, func(o, later ValidateOptions) ValidateOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.Rules, later.Rules)
		layMap(&o.FieldRules, later.FieldRules)
		layMap(&o.SchemaHints, later.SchemaHints)
		lay(&o.Deterministic, later.Deterministic)
		lay(&o.FailOn, later.FailOn)
		lay(&o.OmitCorrection, later.OmitCorrection)
		lay(&o.OmitExplanations, later.OmitExplanations)
		return o
	})
}

// checkValidateOptions returns the internal error of a setting no validation can
// use, or nil. Of a Deterministic call it also refuses what a model would have to
// judge, in words of this package: the internal call words the same refusals for
// its own names and, for a rule it cannot decide, lists the field names.
func checkValidateOptions(o ValidateOptions) error {
	if err := o.common().Check("validate"); err != nil {
		return err
	}
	if err := checkWordOf("validate", "FailOn", o.FailOn, "error", "warning", "info"); err != nil {
		return err
	}
	if o.Deterministic {
		switch {
		case strings.TrimSpace(o.Rules) != "":
			return types.Newf(types.KindConfiguration, "validate", "Deterministic is set and Rules is free text, which needs a model")
		case len(o.SchemaHints) > 0:
			return types.Newf(types.KindConfiguration, "validate", "Deterministic is set and SchemaHints needs a model")
		case len(o.FieldRules) == 0:
			return types.Newf(types.KindConfiguration, "validate", "there is nothing to validate against: give FieldRules")
		}
	}
	return nil
}

// validateInternal builds the internal options of a Validate call on ctx.
func validateInternal(ctx context.Context, o ValidateOptions) ops.ValidateOptions {
	opts := ops.NewValidateOptions()
	lay(&opts.Rules, o.Rules)
	layMap(&opts.SchemaHints, o.SchemaHints)
	layMap(&opts.FieldRules, o.FieldRules)
	lay(&opts.FailOn, o.FailOn)
	turnOff(&opts.AutoCorrect, o.OmitCorrection)
	turnOff(&opts.IncludeExplanations, o.OmitExplanations)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// validateRun makes the internal call of a Validate: the one that decides every
// rule in Go when the caller asked for that, and the one that asks a model about
// what Go cannot decide otherwise.
func validateRun[T any](data T, in ops.ValidateOptions, deterministic bool) (ValidateResult[T], error) {
	var zero ValidateResult[T]
	if deterministic {
		j, err := ops.ValidateDeterministically(data, in)
		if err != nil {
			// Every other refusal of this call was made before it, so what is left
			// is a field rule Go cannot decide. The internal message lists the field
			// names, which are the caller's text, and names an internal function.
			return zero, types.Newf(types.KindConfiguration, "validate", "Deterministic is set and a rule in FieldRules is not one Go can decide")
		}
		res := validateResultOf(j)
		res.DeterministicOnly = true
		return res, nil
	}
	j, err := ops.ValidateHybrid(data, in)
	return validateResultOf(j), err
}

// validateResultOf is the result of the internal call as the root declares it.
// The two claims it carries are read with the comma-ok form: a call that proposed
// no correction has no entry for it, and a bare assertion would panic.
func validateResultOf[T any](j types.JudgmentResult[T]) ValidateResult[T] {
	res := ValidateResult[T]{Judgment: judgmentOf(j)}
	if corrected, ok := j.ModelClaims["corrected"].(*T); ok {
		res.Corrected = corrected
	}
	if only, ok := j.ModelClaims["deterministic_only"].(bool); ok {
		res.DeterministicOnly = only
	}
	return res
}

// Validate checks a value against rules. Field rules Go can decide exactly
// ("email", "min:18", "iso3166-alpha2") are decided in Go; a model is asked only
// about what is left, and not at all when nothing is. With nothing to validate
// against, which is no Rules, no FieldRules and no SchemaHints, the call is an
// error of kind ErrConfiguration. Default tier Fast, mode Strict.
//
//	res, err := schemaflux.Validate(ctx, signup, schemaflux.ValidateOptions{FieldRules: map[string]string{"Email": "email", "Age": "min:18"}})
//	if res.Verdict == schemaflux.VerdictFail { for _, i := range res.Issues { ... } }
//
// The verdict is worked out here from the issues and FailOn; the model is not
// asked for one. The Record of the call is "validate", delivered at
// ContractSchemaConstrained when the request declares a schema for the answer,
// which is read into a declared type, and nothing relational is checked beyond
// that. With a correction asked for (OmitCorrection false, the default) the
// schema holds the corrected value, so a T no strict schema can describe (a map,
// a recursive type, an interface, a type nested deeper than six levels) leaves
// the request without one: the call is then delivered at ContractJSONWellFormed,
// and a floor above that level refuses it before anything is sent. With
// OmitCorrection set the schema does not depend on T. A call with Deterministic
// set is held to the schema-constrained level though nothing is sent.
func Validate[T any](ctx context.Context, data T, opts ...ValidateOptions) (ValidateResult[T], error) {
	var zero ValidateResult[T]
	call, err := bind.Begin(ctx, "validate")
	if err != nil {
		return zero, err
	}
	o := mergeValidateOptions(opts)
	if err := checkValidateOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := validateRun(data, validateInternal(call.Context(), o), o.Deterministic)
		return res, call.End(nil, err)
	}
	// The level follows the type only where the schema does: with a correction
	// asked for, and a model to ask. A deterministic call sends nothing and is
	// held to the row's level.
	level := ops.ValidateContract[T](!o.OmitCorrection && !o.Deterministic)
	return bind.Recorded(&call, level, func(ctx context.Context) (ValidateResult[T], error) {
		return validateRun(data, validateInternal(ctx, o), o.Deterministic)
	})
}

// VerifyOptions are Verify's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: VerifyOptions{Model: "gpt-6-luna", SkipLogic: true}.
type VerifyOptions struct {
	Options

	// Sources are material to check the claims against. Nil means the model's own
	// knowledge. It has an effect only when the facts are checked.
	Sources []any

	// TrustedSources name kinds of source to prefer. Nil means none. It has an
	// effect only when the facts are checked.
	TrustedSources []string

	// Domain names the subject area. Empty means none.
	Domain string

	// Strictness is how much evidence the model is asked to want: "strict",
	// "moderate" or "lenient". It is guidance and nothing checks that it was
	// followed. It has an effect only when the facts are checked. Empty means
	// "moderate". Any other value is a configuration error.
	Strictness string

	// MinConfidence is the least confidence at which "verified" stands; below it a
	// claim is reported as unverifiable. It is applied here, and the model is not
	// told it. It also lowers an overall verdict of verified, and a verified claim
	// with no evidence, when evidence is asked for, or with no cited source, when
	// sources are given, is lowered too, and the claim's Note says why. It is a
	// number up to 1. Zero means 0.7, and a negative value means 0. A value above 1,
	// or one that is not a number, is a configuration error.
	MinConfidence float64

	// SkipFacts turns off the check of the facts. False runs it. Turning off all
	// three of SkipFacts, SkipLogic and SkipConsistency is a configuration error.
	SkipFacts bool

	// SkipLogic turns off the check of the reasoning. False runs it.
	SkipLogic bool

	// SkipConsistency turns off the check of the text against itself. False runs
	// it.
	SkipConsistency bool

	// OmitEvidence leaves the evidence out of the request. False asks for it.
	OmitEvidence bool

	// OmitReasoning leaves the reasoning out of the request. False asks for it.
	OmitReasoning bool
}

// VerifyResult is a text's verification.
type VerifyResult struct {
	Judgment[string]

	// Claims is every claim the model assessed, the verified ones included. Issues
	// holds only those that were not verified. Nil when none.
	Claims []ClaimVerification `json:"claims,omitempty"`

	// ModelTrustScore is the model's own overall score for the text, from 0 to 1.
	// It is a claim and not a measurement.
	ModelTrustScore float64 `json:"trust_score"`
}

// ClaimVerification is one claim of a text and what was found of it.
type ClaimVerification struct {
	// Claim is the claim.
	Claim string `json:"claim"`

	// Verdict is "verified", "false", "partially_true", "unverifiable" or
	// "misleading", after MinConfidence and the other checks made here.
	Verdict string `json:"verdict"`

	// ModelConfidence is the model's own claim for the verdict, from 0 to 1.
	ModelConfidence float64 `json:"confidence"`

	// Evidence is what supports or refutes the claim, as the model gave it.
	Evidence []string `json:"evidence,omitempty"`

	// Reasoning is why the model reached its verdict.
	Reasoning string `json:"reasoning,omitempty"`

	// Sources are the positions in VerifyOptions.Sources the claim rests on.
	Sources []int `json:"sources,omitempty"`

	// Corrections is the correct information, when the claim is false or
	// misleading.
	Corrections string `json:"corrections,omitempty"`

	// ModelVerdict is the verdict the model gave, where Verdict is not it. Empty
	// when the two agree.
	ModelVerdict string `json:"model_verdict,omitempty"`

	// Note is this library's reason that Verdict differs from ModelVerdict. Empty
	// when they agree.
	Note string `json:"note,omitempty"`
}

// mergeVerifyOptions merges the values of a call (M0).
func mergeVerifyOptions(opts []VerifyOptions) VerifyOptions {
	return mergeValues(opts, func(o, later VerifyOptions) VerifyOptions {
		o.Options = o.Options.merge(later.Options)
		laySlice(&o.Sources, later.Sources)
		laySlice(&o.TrustedSources, later.TrustedSources)
		lay(&o.Domain, later.Domain)
		lay(&o.Strictness, later.Strictness)
		lay(&o.MinConfidence, later.MinConfidence)
		lay(&o.SkipFacts, later.SkipFacts)
		lay(&o.SkipLogic, later.SkipLogic)
		lay(&o.SkipConsistency, later.SkipConsistency)
		lay(&o.OmitEvidence, later.OmitEvidence)
		lay(&o.OmitReasoning, later.OmitReasoning)
		return o
	})
}

// checkVerifyOptions returns the internal error of a setting no verification can
// use, or nil. The number is checked on the caller's own value, before the
// meaning of its zero and of a negative is applied.
func checkVerifyOptions(o VerifyOptions) error {
	if err := o.common().Check("verify"); err != nil {
		return err
	}
	if err := checkWordOf("verify", "Strictness", o.Strictness, "strict", "moderate", "lenient"); err != nil {
		return err
	}
	if err := checkFloat("verify", "MinConfidence", o.MinConfidence, math.Inf(-1), 1); err != nil {
		return err
	}
	if o.SkipFacts && o.SkipLogic && o.SkipConsistency {
		return types.Newf(types.KindConfiguration, "verify", "SkipFacts, SkipLogic and SkipConsistency are all set, so there is nothing to verify")
	}
	return nil
}

// verifyInternal builds the internal options of a Verify call on ctx.
func verifyInternal(ctx context.Context, o VerifyOptions) ops.VerifyOptions {
	opts := ops.NewVerifyOptions()
	laySlice(&opts.Sources, o.Sources)
	lay(&opts.Strictness, o.Strictness)
	turnOff(&opts.IncludeEvidence, o.OmitEvidence)
	turnOff(&opts.ExplainReasoning, o.OmitReasoning)
	turnOff(&opts.CheckLogic, o.SkipLogic)
	turnOff(&opts.CheckFacts, o.SkipFacts)
	turnOff(&opts.CheckConsistency, o.SkipConsistency)
	lay(&opts.Domain, o.Domain)
	laySlice(&opts.TrustedSources, o.TrustedSources)
	layFloor(&opts.MinConfidence, o.MinConfidence)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// verifyResultOf is the result of the internal call as the root declares it, for
// the text that was verified. The two claims it carries are read with the
// comma-ok form. The zero judgment, which the internal call returns beside an
// error, converts to the zero result, and the subject is the text only when the
// call gave a judgment.
func verifyResultOf(text string, j types.JudgmentResult[any]) VerifyResult {
	res := VerifyResult{Judgment: Judgment[string]{
		Verdict:         Verdict(j.Verdict),
		Issues:          issuesOf(j.Issues),
		Evidence:        j.Evidence,
		Summary:         j.Summary,
		ModelConfidence: j.ModelConfidence,
	}}
	if j.Verdict == "" {
		return res
	}
	res.Subject = text
	if claims, ok := j.ModelClaims["claims"].([]ops.ClaimVerification); ok && len(claims) > 0 {
		res.Claims = make([]ClaimVerification, len(claims))
		for i, claim := range claims {
			res.Claims[i] = ClaimVerification(claim)
		}
	}
	if score, ok := j.ModelClaims["trust_score"].(float64); ok {
		res.ModelTrustScore = score
	}
	return res
}

// Verify checks a text with a model: its claims, its reasoning and its
// consistency. The findings are the model's. The verdict is worked out here as
// the worst of the checks that ran, and a check that is on and that the model did
// not report on is an error and not a pass. Default tier Smart, mode Strict.
//
//	res, err := schemaflux.Verify(ctx, summary, schemaflux.VerifyOptions{Sources: []any{sourceDoc}, SkipLogic: true})
//	if res.Verdict != schemaflux.VerdictPass { ... }
//
// A claim that cites a source that was not given is an error of kind
// ErrInvalidAnswer. The Record of the call is "verify", delivered at
// ContractSchemaAndInvariantChecked: the request declares a schema, and each
// verdict, issue kind and severity in the answer is held to its vocabulary and
// each check that is on to a report.
func Verify(ctx context.Context, text string, opts ...VerifyOptions) (VerifyResult, error) {
	var zero VerifyResult
	call, err := bind.Begin(ctx, "verify")
	if err != nil {
		return zero, err
	}
	o := mergeVerifyOptions(opts)
	if err := checkVerifyOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		j, err := ops.VerifyWithModel(text, verifyInternal(call.Context(), o))
		return verifyResultOf(text, j), call.End(nil, err)
	}
	j, err := bind.Recorded(&call, types.ContractSchemaAndInvariantChecked, func(ctx context.Context) (types.JudgmentResult[any], error) {
		return ops.VerifyWithModel(text, verifyInternal(ctx, o))
	})
	return verifyResultOf(text, j), err
}

// AuditOptions are Audit's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: AuditOptions{Model: "gpt-6-luna", Policies: policies}.
type AuditOptions struct {
	Options

	// Policies are rules to check the value against, in the caller's words. Nil
	// means none.
	Policies []string

	// Categories are kinds of problem to look for besides the policies: "security",
	// "compliance", "quality". Nil means none; with neither policies nor
	// categories, everything found counts.
	Categories []string

	// Threshold is the lowest severity to list, from 0 to 1. It does not change the
	// verdict. Zero lists everything. A value outside 0 to 1, or one that is not a
	// number, is a configuration error.
	Threshold float64

	// Shallow inspects the top level only. False inspects nested values too.
	Shallow bool
}

// AuditResult is a value's audit.
type AuditResult[T any] struct {
	Judgment[T]

	// Other is what was found that the caller did not ask about. It does not count
	// toward the verdict. Nil when none.
	Other []JudgmentIssue `json:"other_findings,omitempty"`

	// BySeverity counts the listed findings by severity. Its keys are "critical",
	// "high", "medium", "low" and "info", where an issue's own Severity is
	// "critical", "error", "warning" or "info".
	BySeverity map[string]int `json:"by_severity"`

	// ByCategory counts the listed findings by category.
	ByCategory map[string]int `json:"by_category"`

	// ByPolicy counts every violation of each supplied policy, listed or not, and
	// has every policy, at zero when nothing was found against it. Nil when no
	// policies were supplied.
	ByPolicy map[string]int `json:"by_policy,omitempty"`

	// Critical reports a finding of severity 0.9 or above among those that count,
	// listed or not.
	Critical bool `json:"critical"`

	// BelowThreshold is how many findings that count are not in Issues because
	// their severity is under Threshold. They still decide the verdict.
	BelowThreshold int `json:"below_threshold,omitempty"`

	// OtherHighestSeverity is the highest severity among Other, from 0 to 1. The
	// severities are the model's own scale.
	OtherHighestSeverity float64 `json:"other_highest_severity,omitempty"`
}

// mergeAuditOptions merges the values of a call (M0).
func mergeAuditOptions(opts []AuditOptions) AuditOptions {
	return mergeValues(opts, func(o, later AuditOptions) AuditOptions {
		o.Options = o.Options.merge(later.Options)
		laySlice(&o.Policies, later.Policies)
		laySlice(&o.Categories, later.Categories)
		lay(&o.Threshold, later.Threshold)
		lay(&o.Shallow, later.Shallow)
		return o
	})
}

// checkAuditOptions returns the internal error of a setting no audit can use, or
// nil.
func checkAuditOptions(o AuditOptions) error {
	if err := o.common().Check("audit"); err != nil {
		return err
	}
	return checkFloat("audit", "Threshold", o.Threshold, 0, 1)
}

// auditInternal builds the internal options of an Audit call on ctx. The internal
// type spells its common settings out and takes the context as a field, so each
// is laid onto it by name. The constructor's Deprecated field Deep stays false.
func auditInternal(ctx context.Context, o AuditOptions) ops.AuditOptions {
	opts := ops.NewAuditOptions()
	laySlice(&opts.Policies, o.Policies)
	laySlice(&opts.Categories, o.Categories)
	lay(&opts.Threshold, o.Threshold)
	lay(&opts.Shallow, o.Shallow)
	c := o.common()
	lay(&opts.Model, c.Model)
	lay(&opts.Intelligence, c.Tier)
	lay(&opts.Effort, c.Effort)
	lay(&opts.Mode, c.Mode)
	lay(&opts.Steering, c.Steering)
	lay(&opts.MaxOutputTokens, c.MaxOutputTokens)
	lay(&opts.WebSearch, c.WebSearch)
	opts.Context = ctx
	return opts
}

// auditResultOf is the result of the internal call as the root declares it. The
// keys of the model's claims are read with the comma-ok form, so a claim that is
// not there leaves its field at zero. The zero judgment, which the internal call
// returns beside an error, converts to the zero result.
func auditResultOf[T any](j types.JudgmentResult[T]) AuditResult[T] {
	res := AuditResult[T]{Judgment: judgmentOf(j)}
	claims := j.ModelClaims
	res.BySeverity, _ = claims["by_severity"].(map[string]int)
	res.ByCategory, _ = claims["by_category"].(map[string]int)
	res.ByPolicy, _ = claims["by_policy"].(map[string]int)
	res.Critical, _ = claims["critical"].(bool)
	res.BelowThreshold, _ = claims["findings_below_threshold"].(int)
	res.OtherHighestSeverity, _ = claims["other_highest_severity"].(float64)
	if other, ok := claims["other_findings"].([]types.JudgmentIssue); ok {
		res.Other = issuesOf(other)
	}
	return res
}

// Audit inspects a value with a model against the policies and categories the
// caller names. The findings are the model's. The verdict is worked out here: it
// fails when a policy the caller supplied is violated, whatever its severity, or
// when a problem in a named category has a severity of 0.7 or above. With policies
// and no categories, a problem that is no policy's is in Other and does not count.
// An answer that does not report on every policy is an error of kind
// ErrInvalidAnswer. Default tier Smart, mode Transform.
//
//	res, err := schemaflux.Audit(ctx, ledger, schemaflux.AuditOptions{Policies: []string{"Amounts must balance"}})
//	if res.Verdict == schemaflux.VerdictFail { for _, i := range res.Issues { fmt.Println(i.Rule, i.Subject) } }
//
// BySeverity and ByCategory count the listed findings. ByPolicy counts every
// violation of each supplied policy, listed or not. The Record of the call is
// "audit", delivered at ContractSchemaAndInvariantChecked: the request declares a
// schema, and every finding is held to the categories asked about.
func Audit[T any](ctx context.Context, data T, opts ...AuditOptions) (AuditResult[T], error) {
	var zero AuditResult[T]
	call, err := bind.Begin(ctx, "audit")
	if err != nil {
		return zero, err
	}
	o := mergeAuditOptions(opts)
	if err := checkAuditOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		j, err := ops.AuditWithModel(data, auditInternal(call.Context(), o))
		return auditResultOf(j), call.End(nil, err)
	}
	// The twin sends the same request and reports what the Record carries.
	res, err := ops.AuditWithMeta(data, auditInternal(call.Context(), o))
	return auditResultOf(res.Value), call.End(&res.Meta, err)
}

// CritiqueOptions are Critique's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: CritiqueOptions{Model: "gpt-6-luna", MaxIssues: 5}.
type CritiqueOptions struct {
	Options

	// Rubric says what a criterion means, keyed by criterion; its keys are criteria
	// too. Nil means none.
	Rubric map[string]string

	// Style is "constructive", "harsh" or "balanced". Empty means "constructive".
	// Any other value is a configuration error.
	Style string

	// SeverityFilter is "all", "critical", "major" or "minor". Empty means "all".
	// Any other value is a configuration error.
	SeverityFilter string

	// MaxIssues is the most issues to report. Zero means no limit. A negative value
	// is a configuration error.
	MaxIssues int

	// Domain names the subject area. Empty means none.
	Domain string

	// Audience is who the critique is for. Empty means unstated.
	Audience string

	// OmitSuggestions leaves the suggestions out of the request, and removes the
	// ones the model writes anyway. False asks for them.
	OmitSuggestions bool

	// OmitFixes leaves the fixes out of the request, and removes the ones the model
	// writes anyway. False asks for them.
	OmitFixes bool

	// OmitPositives leaves the positives out of the request, and removes the ones
	// the model writes anyway. False asks for them.
	OmitPositives bool
}

// CritiqueResult is a value's critique.
type CritiqueResult[T any] struct {
	// Judgment's ModelConfidence is the model's overall score for the value.
	Judgment[T]

	// CriteriaScores is the model's own score for every criterion asked for; an
	// answer that leaves one out is refused.
	CriteriaScores map[string]float64 `json:"criteria_scores"`

	// Positives is what the model found good. Nil when none, and when
	// OmitPositives is set.
	Positives []CritiquePositive `json:"positives,omitempty"`

	// TopPriorities is what the model would fix first. Nil when it named none.
	TopPriorities []string `json:"top_priorities,omitempty"`
}

// CritiquePositive is one thing the model found good about a value.
type CritiquePositive struct {
	// Criterion is the criterion it is good on.
	Criterion string `json:"criterion"`

	// Description is what is good.
	Description string `json:"description"`

	// Strength is how strong it is, in the model's words. Empty when it gave none.
	Strength string `json:"strength,omitempty"`
}

// mergeCritiqueOptions merges the values of a call (M0).
func mergeCritiqueOptions(opts []CritiqueOptions) CritiqueOptions {
	return mergeValues(opts, func(o, later CritiqueOptions) CritiqueOptions {
		o.Options = o.Options.merge(later.Options)
		layMap(&o.Rubric, later.Rubric)
		lay(&o.Style, later.Style)
		lay(&o.SeverityFilter, later.SeverityFilter)
		lay(&o.MaxIssues, later.MaxIssues)
		lay(&o.Domain, later.Domain)
		lay(&o.Audience, later.Audience)
		lay(&o.OmitSuggestions, later.OmitSuggestions)
		lay(&o.OmitFixes, later.OmitFixes)
		lay(&o.OmitPositives, later.OmitPositives)
		return o
	})
}

// checkCritiqueOptions returns the internal error of a setting no critique can
// use, or nil.
func checkCritiqueOptions(o CritiqueOptions) error {
	if err := o.common().Check("critique"); err != nil {
		return err
	}
	if err := checkCount("critique", "MaxIssues", o.MaxIssues); err != nil {
		return err
	}
	if err := checkWordOf("critique", "Style", o.Style, "constructive", "harsh", "balanced"); err != nil {
		return err
	}
	return checkWordOf("critique", "SeverityFilter", o.SeverityFilter, "all", "critical", "major", "minor")
}

// critiqueInternal builds the internal options of a Critique call on ctx.
func critiqueInternal(ctx context.Context, criteria []string, o CritiqueOptions) ops.CritiqueOptions {
	opts := ops.NewCritiqueOptions()
	opts.Criteria = criteria
	layMap(&opts.Rubric, o.Rubric)
	turnOff(&opts.IncludeSuggestions, o.OmitSuggestions)
	turnOff(&opts.IncludeFixes, o.OmitFixes)
	lay(&opts.SeverityFilter, o.SeverityFilter)
	lay(&opts.MaxIssues, o.MaxIssues)
	lay(&opts.Domain, o.Domain)
	lay(&opts.Audience, o.Audience)
	lay(&opts.Style, o.Style)
	turnOff(&opts.IncludePositives, o.OmitPositives)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// critiqueResultOf is the result of the internal call as the root declares it.
// The three claims it carries are read with the comma-ok form. The zero judgment,
// which the internal call returns beside an error, converts to the zero result.
func critiqueResultOf[T any](j types.JudgmentResult[T]) CritiqueResult[T] {
	res := CritiqueResult[T]{Judgment: judgmentOf(j)}
	claims := j.ModelClaims
	res.CriteriaScores, _ = claims["criteria_scores"].(map[string]float64)
	res.TopPriorities, _ = claims["top_priorities"].([]string)
	if positives, ok := claims["positives"].([]ops.CritiquePositive); ok && len(positives) > 0 {
		res.Positives = make([]CritiquePositive, len(positives))
		for i, positive := range positives {
			res.Positives[i] = CritiquePositive(positive)
		}
	}
	return res
}

// Critique reviews a value with a model against criteria. The verdict is worked
// out here from the issues: fail when one is critical, mixed when there are issues
// and none is critical, pass when there are none. criteria may be nil when a
// Rubric is given. Default tier Fast, mode Transform.
//
//	res, err := schemaflux.Critique(ctx, essay, []string{"clarity", "argument"})
//	res, err := schemaflux.Critique(ctx, pr, nil, schemaflux.CritiqueOptions{Rubric: reviewRubric, MaxIssues: 5})
//	fmt.Println(res.Verdict, res.TopPriorities)
//
// No criteria and no Rubric is an error of kind ErrInvalidRequest, returned before
// anything is sent. The Record of the call is "critique", delivered at
// ContractJSONWellFormed: the answer is parsed, but no schema is declared to the
// provider, so a client whose DataPolicy sets a higher floor refuses the call with
// ErrPolicy before anything is sent.
func Critique[T any](ctx context.Context, input T, criteria []string, opts ...CritiqueOptions) (CritiqueResult[T], error) {
	var zero CritiqueResult[T]
	call, err := bind.Begin(ctx, "critique")
	if err != nil {
		return zero, err
	}
	o := mergeCritiqueOptions(opts)
	if len(criteria) == 0 && len(o.Rubric) == 0 {
		return zero, call.Refuse(types.KindInvalidRequest, "criteria is empty and there is no Rubric")
	}
	if err := checkCritiqueOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		j, err := ops.CritiqueWithModel(input, critiqueInternal(call.Context(), criteria, o))
		return critiqueResultOf(j), call.End(nil, err)
	}
	j, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (types.JudgmentResult[T], error) {
		return ops.CritiqueWithModel(input, critiqueInternal(ctx, criteria, o))
	})
	return critiqueResultOf(j), err
}
