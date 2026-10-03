package schemaflux

import (
	"context"
	"errors"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// DeduplicateResult is the items kept from a list, the groups of items that
// were collapsed into them, and where each of them was in the list.
//
// The items in it are the caller's own values, not copies made from JSON. The
// slices are new: changing them never changes the list that was passed in.
type DeduplicateResult[T any] struct {
	// Unique are the items kept, one for the first item of each group of
	// duplicates and one for each item that has none, in the order of the input.
	// Empty, never nil, for an empty list.
	Unique []T

	// Duplicates are the groups of items that mean the same, the item kept first
	// and the rest in the order of the input. A group has at least two items;
	// there is no group for an item that has no duplicate. Empty, never nil, when
	// nothing was a duplicate.
	Duplicates [][]T

	// TotalRemoved is how many items collapsed into an earlier one: the length of
	// the input less the length of Unique.
	TotalRemoved int

	// UniqueIndices are, for each item of Unique, its position in the input.
	UniqueIndices []int

	// DuplicateIndices are, for each group of Duplicates, the positions of its
	// items in the input, ascending. It has no entries when Duplicates has none.
	DuplicateIndices [][]int
}

// deduplicateInternal builds the internal options of a Deduplicate call on ctx.
func deduplicateInternal(ctx context.Context, o Options) types.OpOptions {
	var opts types.OpOptions
	o.common().Apply(&opts)
	opts.Context = ctx
	return opts
}

// withoutItemCause returns err as it is, except that an error for an item that
// could not be written as JSON, which the internal call makes before it sends
// anything and builds on the error of the item's own MarshalJSON, loses that
// cause: the message of a caller's MarshalJSON is the caller's data and does not
// belong in an error that is logged and sent on. An error that has a provider or a
// request was made after a request went out and is left alone. A call that
// succeeds passes through it with no allocation: the pointer errors.As fills is
// declared only after the error is known to be one.
func withoutItemCause(err error) error {
	if err == nil {
		return nil
	}
	var failure *types.OperationError
	if !errors.As(err, &failure) || failure.Kind != types.KindInvalidRequest || failure.Cause == nil || failure.Provider != "" || failure.RequestID != "" {
		return err
	}
	clean := *failure
	clean.Cause = nil
	return &clean
}

// Deduplicate removes items that mean the same as an earlier one and keeps the
// caller's own values. threshold is from 0 to 1: 1 is the same item, 0.9 the same
// item written differently, 0.7 the same with one detail in disagreement. The
// model reports how close each match is and the threshold is applied here: a
// match at or above it is a duplicate. Matches are followed, so if C matches B and
// B matches A the three are one group. Default tier Smart, mode Transform. With
// nothing numeric in the items or the steering it prefers no reasoning effort,
// which the caller's own Effort replaces.
//
//	res, err := schemaflux.Deduplicate(ctx, leads, 0.9)
//	leads = res.Unique
//
// Items that are written as the same JSON are duplicates without asking, and
// only the distinct items are sent, in one request for the whole list, so the
// list has to fit in the model's context. With at most one distinct item no
// request is made. Two values that differ only in what JSON leaves out (an
// unexported field, or one tagged json:"-") are the same item.
//
// A threshold outside 0 to 1, or one that is not a number, is an error of kind
// ErrInvalidRequest, returned before anything is sent. An answer that cannot be
// read, that names an item the list does not have, that matches an item with a
// later one, that has a similarity outside 0 to 1, or that leaves a member out is
// an error of kind ErrInvalidAnswer: never "no duplicates". On any error the
// result is empty. The Record of the call is "deduplicate", delivered at
// ContractSchemaAndInvariantChecked: the answer is declared to the provider as a
// schema, and every index and match in it is checked against the list.
func Deduplicate[T any](ctx context.Context, items []T, threshold float64, opts ...Options) (DeduplicateResult[T], error) {
	var zero DeduplicateResult[T]
	call, err := bind.Begin(ctx, "deduplicate")
	if err != nil {
		return zero, err
	}
	if !(threshold >= 0 && threshold <= 1) { // written so that NaN is refused too
		return zero, call.Refuse(types.KindInvalidRequest, "threshold must be a number from 0 to 1")
	}
	o := mergeOptions(opts)
	if err := o.common().Check("deduplicate"); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Deduplicate(items, threshold, deduplicateInternal(call.Context(), o))
		return DeduplicateResult[T](res), call.End(nil, withoutItemCause(err))
	}
	res, err := bind.Recorded(&call, types.ContractSchemaAndInvariantChecked, func(ctx context.Context) (ops.DeduplicateResult[T], error) {
		res, err := ops.Deduplicate(items, threshold, deduplicateInternal(ctx, o))
		return res, withoutItemCause(err)
	})
	return DeduplicateResult[T](res), err
}
