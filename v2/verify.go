package schemaflux

import (
	"context"
	"math"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

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
