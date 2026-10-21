package schemaflux

import (
	"context"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// CompareOptions are Compare's settings. The zero value is the default: the
// model chooses what to compare on, looks at both likenesses and differences, is
// asked to go to depth 5 and is asked for a similarity score.
//
// The common settings of Options come first and are set without naming the
// embedded type: CompareOptions{Model: "gpt-6-luna", Depth: 8}.
type CompareOptions struct {
	Options

	// Aspects are what to compare on, such as "price" and "risk". Nil means the
	// model chooses. Every aspect given has to be scored in the answer, and an
	// answer that leaves one out is an error of kind ErrInvalidAnswer. An empty
	// list that is not nil is sent as it is and also leaves the choice to the
	// model.
	Aspects []string

	// FocusOn is "differences", "similarities" or "both". Empty means "both". Any
	// other value is a configuration error.
	FocusOn string

	// Depth is how thorough the comparison is asked to be, from 1 to 10. Zero
	// means 5. A value outside 1 to 10, a negative one included, is a
	// configuration error. It is a request in the prompt: Go does not check how
	// deep the answer went.
	Depth int

	// OmitSimilarity removes one sentence from the request, the one that asks the
	// model to report a similarity score beside the differences. False keeps it.
	// The score is always asked for in the answer's shape and always returned and
	// checked, so this changes how the model is told to think about it and not
	// whether there is one.
	OmitSimilarity bool
}

// CompareResult is a model's comparison of two values.
type CompareResult[T any] struct {
	// ItemA is the first value given to Compare, as it was given.
	ItemA T `json:"item_a"`

	// ItemB is the second value given to Compare, as it was given.
	ItemB T `json:"item_b"`

	// SimilarityScore is the model's own claim of how alike the two are, from 0
	// to 1. An answer with a score off that scale is refused, so a score here is
	// always in range. It is the model's judgement and not a measurement.
	SimilarityScore float64 `json:"similarity_score"`

	// Similarities are the ways the two are alike. Nil when the model named none.
	Similarities []ComparisonPoint `json:"similarities,omitempty"`

	// Differences are the ways the two differ. Nil when the model named none.
	// Each has a Severity.
	Differences []ComparisonPoint `json:"differences,omitempty"`

	// Verdict is the model's one-line account of the comparison. It is never
	// empty: an answer without one is refused.
	Verdict string `json:"verdict"`

	// AspectScores is the model's score from 0 to 1 for each aspect, how alike the
	// two are on it. When Aspects were given it holds exactly those, spelled as the
	// caller spelled them. When none were given it holds the aspects the model
	// chose, as it wrote them. It is empty, never nil, when there are none.
	AspectScores map[string]float64 `json:"aspect_scores,omitempty"`
}

// ComparisonPoint is one way two values are alike or differ.
type ComparisonPoint struct {
	// Aspect is what the point is about.
	Aspect string `json:"aspect"`

	// Description says how the two compare on the aspect.
	Description string `json:"description"`

	// Severity is "minor", "moderate" or "major" for a difference, in lower case,
	// and empty for a similarity.
	Severity string `json:"severity,omitempty"`
}

// mergeCompareOptions merges the values of a call (M0).
func mergeCompareOptions(opts []CompareOptions) CompareOptions {
	return mergeValues(opts, func(o, later CompareOptions) CompareOptions {
		o.Options = o.Options.merge(later.Options)
		laySlice(&o.Aspects, later.Aspects)
		lay(&o.FocusOn, later.FocusOn)
		lay(&o.Depth, later.Depth)
		lay(&o.OmitSimilarity, later.OmitSimilarity)
		return o
	})
}

// checkCompareOptions returns the internal error of a setting no call can use,
// or nil. The depth is checked on the caller's own value, before its zero is
// read as the default.
func checkCompareOptions(o CompareOptions) error {
	if err := o.common().Check("compare"); err != nil {
		return err
	}
	if o.Depth < 0 || o.Depth > 10 {
		return types.Newf(types.KindConfiguration, "compare", "Depth must be from 1 to 10")
	}
	switch o.FocusOn {
	case "", "differences", "similarities", "both":
		return nil
	}
	return types.Newf(types.KindConfiguration, "compare", "FocusOn must be differences, similarities or both")
}

// compareInternal builds the internal options of a Compare call on ctx. It
// starts from the constructor's value, so a field left at its zero keeps the
// default: depth 5, focus on both, a similarity score asked for.
func compareInternal(ctx context.Context, o CompareOptions) ops.CompareOptions {
	opts := ops.NewCompareOptions()
	laySlice(&opts.ComparisonAspects, o.Aspects)
	lay(&opts.FocusOn, o.FocusOn)
	lay(&opts.Depth, o.Depth)
	turnOff(&opts.IncludeSimilarity, o.OmitSimilarity)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// compareResult is the result of the internal call as the root declares it. The
// zero result, which the internal call returns beside an error, converts to the
// zero result.
func compareResult[T any](res ops.CompareResult[T]) CompareResult[T] {
	return CompareResult[T]{
		ItemA: res.ItemA, ItemB: res.ItemB, SimilarityScore: res.SimilarityScore,
		Similarities: comparisonPoints(res.Similarities), Differences: comparisonPoints(res.Differences),
		Verdict: res.Verdict, AspectScores: res.AspectScores,
	}
}

// comparisonPoints converts the points of the internal result in one
// allocation. None converts to nil.
func comparisonPoints(points []ops.ComparisonPoint) []ComparisonPoint {
	if len(points) == 0 {
		return nil
	}
	out := make([]ComparisonPoint, len(points))
	for i, p := range points {
		out[i] = ComparisonPoint{Aspect: p.Aspect, Description: p.Description, Severity: p.Severity}
	}
	return out
}

// Compare says how two values of one type are alike and how they differ. The
// answer is read as JSON and held to what it was asked for: a similarity score
// and any aspect score from 0 to 1, a verdict, a severity of minor, moderate or
// major on every difference, and a score for every aspect named in
// CompareOptions.Aspects. An answer that breaks one of those is an error of kind
// ErrInvalidAnswer. Default tier Fast, mode Transform.
//
//	res, err := schemaflux.Compare(ctx, planA, planB)
//	res, err := schemaflux.Compare(ctx, planA, planB, schemaflux.CompareOptions{Aspects: []string{"price", "risk"}})
//	for _, d := range res.Differences { fmt.Println(d.Aspect, d.Description) }
//
// The values only have to be ones that can be written as JSON. The Record of the
// call is "compare", delivered at ContractJSONWellFormed: the answer is parsed
// and checked, but no schema is declared to the provider.
func Compare[T any](ctx context.Context, a, b T, opts ...CompareOptions) (CompareResult[T], error) {
	var zero CompareResult[T]
	call, err := bind.Begin(ctx, "compare")
	if err != nil {
		return zero, err
	}
	o := mergeCompareOptions(opts)
	if err := checkCompareOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Compare(a, b, compareInternal(call.Context(), o))
		return compareResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.CompareResult[T], error) {
		return ops.Compare(a, b, compareInternal(ctx, o))
	})
	return compareResult(res), err
}
