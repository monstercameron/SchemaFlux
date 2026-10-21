package ops

import (
	"context"

	"github.com/monstercameron/schemaflux/v2/internal/logger"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// callerContext is the context a call's options carry: the one in CommonOptions,
// or failing that the one in the embedded OpOptions, which is the order
// mergeEmbeddedOpOptions reads them in. It is read before the options are
// validated or merged, derives nothing, and is nil when the options carry none.
//
// It is resolvedContext without the timeout: that one derives a child context
// with a timer for the caller's Timeout, which a line of log does not need and
// would leave running until it fires if the call then refuses its options.
func callerContext(common CommonOptions, embedded types.OpOptions) context.Context {
	if common.Context != nil {
		return common.Context
	}
	return embedded.Context
}

// loggerForOptions is the logger of the client the call's options carry, found
// through callerContext. It is for the lines an operation writes before it has
// built the context its call runs under: the one that says the operation started
// and the one that reports options it refused. A deadline derived later keeps the
// client the context carries, so the logger found here is the one the rest of the
// call finds.
func loggerForOptions(common CommonOptions, embedded types.OpOptions) *logger.Logger {
	return loggerFor(callerContext(common, embedded))
}
