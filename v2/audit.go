package schemaflux

import (
	"context"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

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
