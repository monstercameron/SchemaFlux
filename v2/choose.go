package schemaflux

import (
	"context"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// ChooseOptions are Choose's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: ChooseOptions{Model: "gpt-6-luna", Criteria: []string{"cheapest"}}.
type ChooseOptions struct {
	Options

	// Criteria are what to choose by. Nil means the model's judgement of "best".
	Criteria []string

	// Strategy is "sequential", "tournament" or "scoring". Empty means
	// "scoring". It is guidance to the model and nothing checks that it was
	// followed. Any other value is a configuration error.
	Strategy string

	// OmitReasoning withholds the instruction to reason about the choice. False
	// keeps it. The reasoning is not returned either way: the answer is the id of
	// the option and nothing else.
	OmitReasoning bool
}

// mergeChooseOptions merges the values of a call (M0).
func mergeChooseOptions(opts []ChooseOptions) ChooseOptions {
	return mergeValues(opts, func(o, later ChooseOptions) ChooseOptions {
		o.Options = o.Options.merge(later.Options)
		laySlice(&o.Criteria, later.Criteria)
		lay(&o.Strategy, later.Strategy)
		lay(&o.OmitReasoning, later.OmitReasoning)
		return o
	})
}

// checkChooseOptions returns the internal error of a setting no choice can use,
// or nil.
func checkChooseOptions(o ChooseOptions) error {
	if err := o.common().Check("choose"); err != nil {
		return err
	}
	return checkWordOf("choose", "Strategy", o.Strategy, "sequential", "tournament", "scoring")
}

// chooseInternal builds the internal options of a Choose call on ctx. The
// constructor's TopN of 1 stays: Choose returns one option.
func chooseInternal(ctx context.Context, o ChooseOptions) ops.ChooseOptions {
	opts := ops.NewChooseOptions()
	laySlice(&opts.Criteria, o.Criteria)
	lay(&opts.Strategy, o.Strategy)
	turnOff(&opts.RequireReasoning, o.OmitReasoning)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// Choose returns the one option that best meets the criteria. It is always one
// of the options given: the model answers with an id the call assigned, and an id
// that was not offered is an error of kind ErrInvalidAnswer. One option is
// returned without a call. Default tier Fast, mode Transform.
//
//	best, err := schemaflux.Choose(ctx, laptops, schemaflux.ChooseOptions{Criteria: []string{"under $1500", "lightest"}})
//
// No options is an error of kind ErrInvalidRequest, returned before anything is
// sent, and so is an option that cannot be written as JSON. The Record of the
// call is "choose", delivered at ContractJSONWellFormed: the answer is parsed and
// its id is checked against the options offered, but no schema is declared to the
// provider, so a client whose DataPolicy sets a higher floor refuses the call with
// ErrPolicy before anything is sent.
func Choose[T any](ctx context.Context, options []T, opts ...ChooseOptions) (T, error) {
	var zero T
	call, err := bind.Begin(ctx, "choose")
	if err != nil {
		return zero, err
	}
	o := mergeChooseOptions(opts)
	if err := refuseEmpty(&call, "options", len(options)); err != nil {
		return zero, err
	}
	if err := checkChooseOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		best, err := ops.Choose(options, chooseInternal(call.Context(), o))
		return best, call.End(nil, err)
	}
	return bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (T, error) {
		return ops.Choose(options, chooseInternal(ctx, o))
	})
}
