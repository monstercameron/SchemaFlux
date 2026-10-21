package schemaflux

import (
	"context"
	"math"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// FilterOptions are Filter's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: FilterOptions{Model: "gpt-6-luna", Exclude: true}.
type FilterOptions struct {
	Options

	// Exclude removes the items that meet the criteria and returns the rest.
	// False keeps the items that meet them.
	Exclude bool

	// MinConfidence asks the model to act only on items it is at least this sure
	// about. It is an instruction and is not checked: the answer is a list of ids
	// and carries no confidence to check it against. It is a number up to 1. Zero
	// means 0.7, and a negative value leaves the instruction out. A value above 1,
	// or one that is not a number, is a configuration error.
	MinConfidence float64

	// IncludeReasons asks the model to reason about each item. The reasons are not
	// returned. False does not ask.
	IncludeReasons bool
}

// mergeFilterOptions merges the values of a call (M0).
func mergeFilterOptions(opts []FilterOptions) FilterOptions {
	return mergeValues(opts, func(o, later FilterOptions) FilterOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.Exclude, later.Exclude)
		lay(&o.MinConfidence, later.MinConfidence)
		lay(&o.IncludeReasons, later.IncludeReasons)
		return o
	})
}

// checkFilterOptions returns the internal error of a setting no filter can use,
// or nil. The number is checked on the caller's own value, before the meaning of
// its zero and of a negative is applied.
func checkFilterOptions(o FilterOptions) error {
	if err := o.common().Check("filter"); err != nil {
		return err
	}
	return checkFloat("filter", "MinConfidence", o.MinConfidence, math.Inf(-1), 1)
}

// filterInternal builds the internal options of a Filter call on ctx. The
// constructor's BatchSize stays: nothing reads it.
func filterInternal(ctx context.Context, criteria string, o FilterOptions) ops.FilterOptions {
	opts := ops.NewFilterOptions()
	opts.Criteria = criteria
	turnOff(&opts.KeepMatching, o.Exclude)
	layFloor(&opts.MinConfidence, o.MinConfidence)
	lay(&opts.IncludeReasons, o.IncludeReasons)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// Filter returns the items that meet the criteria, in their original order. The
// result is always a subset of the items given, made of the caller's own values:
// the model answers with ids the call assigned, and an id that was not offered or
// that is repeated is an error of kind ErrInvalidAnswer. A list too long for one
// answer is split into several calls, each held to its own ids. For no items the
// result is the input slice itself, and nothing is sent. Default tier Fast, mode
// Transform.
//
//	open, err := schemaflux.Filter(ctx, tasks, "not yet completed")
//	rest, err := schemaflux.Filter(ctx, emails, "is a newsletter", schemaflux.FilterOptions{Exclude: true})
//
// Blank criteria are an error of kind ErrInvalidRequest, returned before anything
// is sent, and so is an item that cannot be written as JSON. The Record of the
// call is "filter", delivered at ContractJSONWellFormed: the answer is parsed and
// each id in it is checked against the items offered, but no schema is declared to
// the provider, so a client whose DataPolicy sets a higher floor refuses the call
// with ErrPolicy before anything is sent.
func Filter[T any](ctx context.Context, items []T, criteria string, opts ...FilterOptions) ([]T, error) {
	call, err := bind.Begin(ctx, "filter")
	if err != nil {
		return nil, err
	}
	o := mergeFilterOptions(opts)
	if err := refuseBlank(&call, "criteria", criteria); err != nil {
		return nil, err
	}
	if err := checkFilterOptions(o); err != nil {
		return nil, call.End(nil, err)
	}
	if call.Plain() {
		kept, err := ops.Filter(items, filterInternal(call.Context(), criteria, o))
		return kept, call.End(nil, err)
	}
	return bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) ([]T, error) {
		return ops.Filter(items, filterInternal(ctx, criteria, o))
	})
}
