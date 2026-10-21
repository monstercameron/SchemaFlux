package schemaflux

import (
	"context"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Merge combines several values of one type into one, by the strategy named in
// plain language: "first-wins", "prefer the newest". One source is returned as it
// is, without a call; none is an error of kind ErrInvalidRequest. The answer is
// read into a T, and one that does not fit is an error of kind ErrInvalidAnswer.
// Default tier Smart, mode Transform.
//
//	profile, err := schemaflux.Merge(ctx, []Profile{crm, billing}, "prefer the newest")
//
// The Record of the call is "merge", with no request for one source. The answer
// is parsed JSON with no declared schema, so it is delivered at
// ContractJSONWellFormed: a client whose DataPolicy sets a floor above that
// refuses the call with ErrPolicy before anything is sent, a single source
// included.
func Merge[T any](ctx context.Context, sources []T, strategy string, opts ...Options) (T, error) {
	var zero T
	call, err := bind.Begin(ctx, "merge")
	if err != nil {
		return zero, err
	}
	o := mergeOptions(opts)
	if err := refuseEmpty(&call, "sources", len(sources)); err != nil {
		return zero, err
	}
	if err := o.common().Check("merge"); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		merged, err := ops.Merge(sources, strategy, settingsOnlyOptions(call.Context(), o))
		return merged, call.End(nil, err)
	}
	return bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (T, error) {
		return ops.Merge(sources, strategy, settingsOnlyOptions(ctx, o))
	})
}
