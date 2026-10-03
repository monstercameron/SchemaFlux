package ops

import (
	"context"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/telemetry"
)

// Recorded runs one call of an operation under a call log and returns its value
// beside the record of how it ran. operation is the name the record and the
// telemetry carry. establishes is the contract level a success delivers: a
// floor it cannot meet refuses the call before run is called, and it is checked
// again after. run builds the operation's options from the ctx it is given,
// which carries the log, and makes the call.
//
// It is the exported form of what the Result forms do for themselves
// (dx12ResultFrom): the call log goes on a context, the floor is checked before
// and after, the identity of the call is settled, and an observer that is
// installed sees one operation, with the requests the call makes inside it as
// its children. It is for an operation that has no Result form of its own, or
// whose Result form sends another request than the plain one.
//
// The options an operation is called with have to be built from the context
// run is given. Options built before Recorded was called carry the caller's
// context, which has no log, and what the call cost is then lost from the
// record.
//
// Meta.Elapsed is the whole call, from the entry to the return, and not the
// last request's duration. Meta.Repairs and the repair entries of Meta.Checks
// are what the call fed back to the model.
func Recorded[T any](ctx context.Context, operation string, establishes types.ContractLevel,
	run func(ctx context.Context) (T, error)) (types.Result[T], error) {

	started := time.Now()
	// The client the call runs on is fixed before the call log is made, so the
	// floor, the requests and the record are all of one client.
	ctx, _ = PinExec(ctx)
	result, err := dx12ResultFrom(operation, ctx, "", "", establishes,
		func(ctx context.Context, _, _ string) (T, error) { return run(ctx) })
	result.Meta.Elapsed = time.Since(started)
	return result, err
}

// Observing reports whether a telemetry observer is installed. It is one
// atomic load and allocates nothing.
func Observing() bool {
	return telemetry.HasObserver()
}
