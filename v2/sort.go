package schemaflux

import (
	"context"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

// SortOptions are Sort's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: SortOptions{Model: "gpt-6-luna", Direction: "descending"}.
//
// A long list is not sorted in one answer: with more than 50 items, or after a
// whole-list answer was refused, each item is scored on its own and the order is
// worked out in Go. On that path equal items always keep their order, whatever
// NoStableOrder says, and the model is asked the Direction for each item.
type SortOptions struct {
	Options

	// Direction is "ascending" or "descending". Empty means "ascending". Any
	// other value is a configuration error.
	Direction string

	// SecondaryCriteria break ties, in order. Nil means none.
	SecondaryCriteria []string

	// ComparisonLogic says how to compare two items, in plain language. Empty
	// means none.
	ComparisonLogic string

	// NoStableOrder withholds the instruction to keep equal items in their
	// original order. False keeps it.
	NoStableOrder bool

	// IncludeScores asks the model to weigh a score for each item. The scores are
	// not returned. False does not ask.
	IncludeScores bool
}

// mergeSortOptions merges the values of a call (M0).
func mergeSortOptions(opts []SortOptions) SortOptions {
	return mergeValues(opts, func(o, later SortOptions) SortOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.Direction, later.Direction)
		laySlice(&o.SecondaryCriteria, later.SecondaryCriteria)
		lay(&o.ComparisonLogic, later.ComparisonLogic)
		lay(&o.NoStableOrder, later.NoStableOrder)
		lay(&o.IncludeScores, later.IncludeScores)
		return o
	})
}

// checkSortOptions returns the internal error of a setting no sort can use, or
// nil.
func checkSortOptions(o SortOptions) error {
	if err := o.common().Check("sort"); err != nil {
		return err
	}
	return checkWordOf("sort", "Direction", o.Direction, "ascending", "descending")
}

// sortInternal builds the internal options of a Sort call on ctx.
func sortInternal(ctx context.Context, criteria string, o SortOptions) ops.SortOptions {
	opts := ops.NewSortOptions()
	opts.Criteria = criteria
	lay(&opts.Direction, o.Direction)
	laySlice(&opts.SecondaryCriteria, o.SecondaryCriteria)
	lay(&opts.ComparisonLogic, o.ComparisonLogic)
	turnOff(&opts.Stable, o.NoStableOrder)
	lay(&opts.IncludeScores, o.IncludeScores)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// Sort returns the items in the order the criteria describe. The result is always
// a permutation of the items given, made of the caller's own values: the model
// answers with ids the call assigned, and an answer that leaves one out or repeats
// one is refused. A long list is scored item by item and ordered in Go; the
// Record's Strategy says which path ran: "trivial" for fewer than two items,
// "whole-list", "scoring", or "scoring-fallback" when a whole-list answer was
// refused. Default tier Fast, mode Transform.
//
//	ranked, err := schemaflux.Sort(ctx, issues, "most urgent first")
//	ranked, err := schemaflux.Sort(ctx, issues, "severity", schemaflux.SortOptions{Direction: "descending"})
//
// Blank criteria are an error of kind ErrInvalidRequest, returned before anything
// is sent, and so is an item that cannot be written as JSON. The Record of the
// call is "sort", delivered at ContractJSONWellFormed: the answer is parsed and
// held to a permutation in Go, but no schema is declared to the provider, so a
// client whose DataPolicy sets a higher floor refuses the call with ErrPolicy
// before anything is sent.
func Sort[T any](ctx context.Context, items []T, criteria string, opts ...SortOptions) ([]T, error) {
	call, err := bind.Begin(ctx, "sort")
	if err != nil {
		return nil, err
	}
	o := mergeSortOptions(opts)
	if err := refuseBlank(&call, "criteria", criteria); err != nil {
		return nil, err
	}
	if err := checkSortOptions(o); err != nil {
		return nil, call.End(nil, err)
	}
	if call.Plain() {
		sorted, err := ops.Sort(items, sortInternal(call.Context(), criteria, o))
		return sorted, call.End(nil, err)
	}
	// The twin runs the same execution and reports which path it took and what it
	// checked, which the Record carries.
	res, err := ops.SortResult(items, sortInternal(call.Context(), criteria, o))
	return res.Value, call.End(&res.Meta, err)
}
