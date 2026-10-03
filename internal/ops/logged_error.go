package ops

import (
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// loggedError is err reduced to what a log line may say of a failure: the kind
// the call path classified it as, and the refusal mark when it has one. Its text
// is the kind's name and nothing else.
//
// A log line used to carry err itself, and an error's text is a provider's: a
// vendor's message, its code and the body it answered with, which quote the
// request back. Every caller sends the library's lines to a log, so none of them
// may hold it. Nothing in the chain is kept: the value has no cause, and an
// observer that unwraps it finds nothing.
func loggedError(err error) error {
	if err == nil {
		return nil
	}
	kind := types.KindOf(err)
	if kind == types.KindUnknown {
		kind = llm.Classify(err)
	}
	return &types.OperationError{Kind: kind, Narrow: types.NarrowingOf(err)}
}
