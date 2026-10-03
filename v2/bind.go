package schemaflux

import (
	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/internal/types"
)

// The root registers with internal/bind the functions that name its types, so
// that the helper every operation function calls can serve the
// subpackages as well as the root. This is a package-level initialiser, and
// Go runs the initialisers of a package after those of every package it
// imports and before any package that imports it: exp, redact, flow, op,
// provider, mw and schemafluxtest each import this package, so each of them
// starts running after the registration is done.
var _ = bind.Register(rootHooks)

// rootHooks are the functions the root registers.
var rootHooks = bind.Hooks{
	Convert:      convertError,
	ToInternal:   toInternalProvider,
	FromInternal: fromInternalProvider,
	Common:       commonOf,
	Unwrap:       unwrapProvider,

	Variables: variablesOf,

	KindOf:       kindNumberOf,
	InternalKind: internalKindOf,
	KindOfError:  kindNumberOfError,
	FromProvider: fromProviderError,
	ToProvider:   providerFailure,

	UsageFromInternal:    usageFromInternal,
	UsageToInternal:      usageToInternal,
	CostFromInternal:     costFromInternal,
	CostToInternal:       costToInternal,
	TimingFromInternal:   timingFromInternal,
	TimingToInternal:     timingToInternal,
	RequestFromInternal:  requestFromInternal,
	RequestToInternal:    requestToInternal,
	ResponseFromInternal: responseFromInternal,
	ResponseToInternal:   responseToInternal,
	ChunkFromInternal:    chunkFromInternal,
	ChunkToInternal:      chunkToInternal,
	LimitsFromInternal:   decisionLimitsFromInternal,
	LimitsToInternal:     DecisionLimits.internal,
}

// kindNumberOf is kindFromInternal with the Kind as its number, zero for none.
func kindNumberOf(kind types.ErrorKind, narrow types.Narrowing) uint8 {
	if k, ok := kindFromInternal(kind, narrow); ok {
		return uint8(k)
	}
	return 0
}

// internalKindOf is kindToInternal for a Kind given as its number.
func internalKindOf(kind uint8) (types.ErrorKind, types.Narrowing) {
	return kindToInternal(Kind(kind))
}

// kindNumberOfError is the Kind of an error as its number, and whether anything
// decided it.
func kindNumberOfError(err error) (uint8, bool) {
	kind, _, known := decidedKind(err)
	return uint8(kind), known
}

// commonOf is the internal common settings of options, which is an Options, and
// the configuration error of a setting no call can use. A subpackage's options
// type embeds Options and cannot call the unexported methods of the root, so it
// asks through bind.CommonOf. The error leaves its operation empty: the
// operation function passes it through bind.ConvertError with its own name.
// Anything that is not an Options is a mistake of this module's own code and is
// an internal error.
func commonOf(options any) (bind.Common, error) {
	var o Options
	switch typed := options.(type) {
	case Options:
		o = typed
	case *Options:
		if typed != nil {
			o = *typed
		}
	default:
		return bind.Common{}, &Error{Kind: ErrInternal, Message: "the settings passed to the library are not an Options"}
	}
	common := o.common()
	if err := common.Check(""); err != nil {
		return bind.Common{}, err
	}
	return common, nil
}

// init settles the piece of process state that a program linking this package
// must not inherit from the environment.
func init() {
	// Request tracking reads five SCHEMAFLUX_REQUEST_* variables whenever
	// nothing has configured it, and a variable set to something unexpected in
	// the environment of a caller's process would change the ids of every call.
	// Pinning the defaults here means the environment is never read; they are
	// the ones v2/requestid pins, and requestid.Configure replaces them. A
	// configuration already in force is left as it is.
	//
	// The process logger is not settled here: the default client installed by
	// client.go's init is the one that sets it, to a logger that discards, so a
	// program that links this package and sets no Logger gets no output from the
	// library.
	requesttracking.PinDefaults()
}
