package schemaflux

import (
	"context"
	"math"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// SimilarOptions are Similar's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: SimilarOptions{Threshold: 0.9, Model: "gpt-6-luna"}.
type SimilarOptions struct {
	Options

	// Threshold is the score at or above which the two values are similar. It is
	// a number from 0 to 1, and it is applied here: the model is not told it.
	// Zero means 0.7, and a negative value means 0, so that everything is
	// similar. A value above 1, or one that is not a number, is a configuration
	// error.
	Threshold float64

	// Aspects are named things to compare the two values on; the answer has to
	// score each of them, and the model is asked to. Nil means none. A blank one,
	// one that repeats another (the comparison ignores case and the space around
	// a name), and one of the form the answer's own keys take (the letter a and
	// digits: a1, A12) are configuration errors.
	Aspects []string
}

// SimilarResult is the model's score for a pair of values and what this package
// decided from it.
type SimilarResult struct {
	// IsSimilar is true when Score is at least Threshold. The model's own
	// opinion of whether the values are similar is never read.
	IsSimilar bool `json:"is_similar"`

	// Score is the model's own claim of how alike the two are, from 0 to 1. It is
	// not a measurement and not comparable between models or prompts.
	Score float64 `json:"score"`

	// Threshold is the threshold that was applied: the option's value, 0.7 when
	// the option was zero, and 0 when it was negative.
	Threshold float64 `json:"threshold"`

	// MatchedAspects are the aspects asked for whose score is at or above
	// Threshold, in the order they were given. Nil when none was asked for or
	// none reached it.
	MatchedAspects []AspectMatch `json:"matched_aspects,omitempty"`

	// DifferingAspects are the aspects asked for whose score is below Threshold,
	// in the order they were given. Nil when none was asked for or none fell
	// below it.
	DifferingAspects []AspectMatch `json:"differing_aspects,omitempty"`

	// Explanation is why, in the model's words. It is never empty in a result: an
	// answer without one is refused.
	Explanation string `json:"explanation"`
}

// AspectMatch is the model's score and reason for one aspect Similar was asked
// to compare on.
type AspectMatch struct {
	// Aspect is the aspect as the caller wrote it in SimilarOptions.Aspects.
	Aspect string `json:"aspect"`

	// Score is the model's own claim for this aspect, from 0 to 1.
	Score float64 `json:"score"`

	// Reason is the model's explanation of the score. Empty when it gave none.
	Reason string `json:"reason,omitempty"`
}

// mergeSimilarOptions merges the values of a call (M0).
func mergeSimilarOptions(opts []SimilarOptions) SimilarOptions {
	return mergeValues(opts, func(o, later SimilarOptions) SimilarOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.Threshold, later.Threshold)
		laySlice(&o.Aspects, later.Aspects)
		return o
	})
}

// checkSimilarOptions returns the internal error of a setting no call can use,
// or nil. The threshold is checked on the caller's own value, before the meaning
// of its zero and of a negative is applied. The aspects are held to the rules the
// internal call holds them to (trimmed, compared without regard to case) so that
// it never has to refuse one: its own message names the aspect, and an option's
// text is not put in an error.
func checkSimilarOptions(o SimilarOptions) error {
	if err := o.common().Check("similar"); err != nil {
		return err
	}
	if err := checkFloat("similar", "Threshold", o.Threshold, math.Inf(-1), 1); err != nil {
		return err
	}
	for i, aspect := range o.Aspects {
		name := strings.TrimSpace(aspect)
		switch {
		case name == "":
			return types.Newf(types.KindConfiguration, "similar", "Aspects[%d] is blank", i)
		case isAnswerKey(name):
			return types.Newf(types.KindConfiguration, "similar", "Aspects[%d] has the form of an answer key (the letter a and digits); give it a descriptive name", i)
		}
		for j, earlier := range o.Aspects[:i] {
			if strings.EqualFold(strings.TrimSpace(earlier), name) {
				return types.Newf(types.KindConfiguration, "similar", "Aspects[%d] repeats Aspects[%d]", i, j)
			}
		}
	}
	return nil
}

// isAnswerKey reports whether name has the form of the keys the answer gives
// its aspects: the letter a, in either case, and one or more digits. A provider
// that keys its answer by name would otherwise be read as keying it by position.
func isAnswerKey(name string) bool {
	if len(name) < 2 || (name[0] != 'a' && name[0] != 'A') {
		return false
	}
	for i := 1; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return true
}

// similarInternal builds the internal options of a Similar call on ctx.
func similarInternal(ctx context.Context, o SimilarOptions) ops.SimilarOptions {
	opts := ops.NewSimilarOptions()
	switch {
	case o.Threshold > 0:
		opts.SimilarityThreshold = o.Threshold
	case o.Threshold < 0:
		opts.SimilarityThreshold = 0
	}
	laySlice(&opts.Aspects, o.Aspects)
	o.common().Apply(&opts.OpOptions)
	opts.OpOptions.Context = ctx
	return opts
}

// similarResult is the result of the internal call as the root declares it. The
// zero result, which the internal call returns beside an error, converts to the
// zero result.
func similarResult(res ops.SimilarResult) SimilarResult {
	return SimilarResult{
		IsSimilar:        res.IsSimilar,
		Score:            res.Score,
		Threshold:        res.Threshold,
		MatchedAspects:   aspectMatches(res.MatchedAspects),
		DifferingAspects: aspectMatches(res.DifferingAspects),
		Explanation:      res.Explanation,
	}
}

// aspectMatches is the aspects of the internal result as the root declares them:
// nil for none, and otherwise a copy, since the two types are not the same type.
func aspectMatches(in []ops.AspectMatch) []AspectMatch {
	if len(in) == 0 {
		return nil
	}
	out := make([]AspectMatch, len(in))
	for i, a := range in {
		out[i] = AspectMatch{Aspect: a.Aspect, Score: a.Score, Reason: a.Reason}
	}
	return out
}

// Similar says whether two values mean the same, with a score. The score is the
// model's; the threshold is applied here. Default tier Fast, mode Transform.
// With nothing numeric in the values, the aspects or the steering it prefers no
// reasoning effort, which the caller's own Effort replaces.
//
//	res, err := schemaflux.Similar(ctx, questionA, questionB)
//	if res.IsSimilar { dedupe(questionB) }
//	res, err := schemaflux.Similar(ctx, a, b, schemaflux.SimilarOptions{Threshold: 0.9, Aspects: []string{"meaning", "tone"}})
//
// An answer that has no score or no explanation, has a score outside 0 to 1, or
// leaves out an aspect that was asked for is an error of kind ErrInvalidAnswer,
// never a default. On any error the result is empty. The Record of the call is
// "similar", delivered at ContractSchemaConstrained: the answer is declared to
// the provider as a schema and read into it, and what Go checks beyond that (the
// aspects) is checked only when aspects are named.
func Similar[T any](ctx context.Context, a, b T, opts ...SimilarOptions) (SimilarResult, error) {
	var zero SimilarResult
	call, err := bind.Begin(ctx, "similar")
	if err != nil {
		return zero, err
	}
	o := mergeSimilarOptions(opts)
	if err := checkSimilarOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Similar(a, b, similarInternal(call.Context(), o))
		return similarResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractSchemaConstrained, func(ctx context.Context) (ops.SimilarResult, error) {
		return ops.Similar(a, b, similarInternal(ctx, o))
	})
	return similarResult(res), err
}
