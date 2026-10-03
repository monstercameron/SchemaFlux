package ops

import (
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/telemetry"
)

// observedPlain opens the operation scope of a call of the plain form of the
// operation name, for a form that takes its context in its options, and puts
// the scope's context on them, so that the requests the call makes are the
// scope's children. The caller defers the end with the address of its error
// result:
//
//	defer observedPlain(&opts.CommonOptions, &opts.OpOptions, "summarize").end(&err)
//
// It is observeOperation for a plain form, and shares what that one does: with
// no observer registered it is one atomic load and returns nil, whose end does
// nothing, so a call that nobody observes costs what it always did; and inside
// a scope of the same operation (the Result form that wraps this one, the call of
// the version-2 API) it opens no second scope and leaves the options as they
// are, because their context is that scope's already.
//
// The options are the plain form's own copy, which is why they can be written
// to. The requests the call makes are given the operation's name as well, so
// that every span of one call names one operation, as the Result forms do.
func observedPlain(common *CommonOptions, embedded *types.OpOptions, name string) *observedOperation {
	if !telemetry.HasObserver() {
		return nil
	}
	ctx, observed := observeOperation(callerContext(*common, *embedded), name)
	if observed != nil {
		useContext(common, embedded, WithOperationName(ctx, name))
	}
	return observed
}
