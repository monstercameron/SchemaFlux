package schemaflux

import (
	"context"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

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
