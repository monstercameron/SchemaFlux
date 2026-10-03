package mw

import (
	schemaflux "github.com/monstercameron/schemaflux/v2"

	impl "github.com/monstercameron/schemaflux/mw"
)

// BulkheadOptions are the settings of Bulkhead. The zero value is the default.
type BulkheadOptions struct {
	// Reject makes a request that finds the bulkhead full fail at once with
	// an error of kind schemaflux.ErrBusy, instead of waiting for a slot. The
	// default is to wait, bounded by the request's context.
	Reject bool
}

// Bulkhead lets at most limit requests be in flight to the provider at once, a
// hard wall in front of it: a slow or stuck provider can hold limit requests
// and no more, so the rest of the program does not queue up behind it. A limit
// below one is an unusable setting, and fails every request with an error of
// kind schemaflux.ErrConfiguration.
//
// A request that finds the wall full waits for a slot, and the wait ends with
// its context; with Reject it fails at once with ErrBusy, to shed load instead
// of queueing behind it. A slot is taken for the whole request, the reading of
// the answer included, and given back when the request ends, however it ends.
//
// The wall is kept per provider name and belongs to the Middleware this
// returns: the same Middleware applied to two providers with one name gives
// them one limit, and to two with different names gives each its own. Waiting
// requests are not served in the order they arrived: the limit is never
// exceeded and a request that gives up leaves no slot taken, but a request can
// be passed over under steady load.
//
// Where it goes in a chain decides what it counts; the package comment says.
// Bulkhead does not forward streaming: a wrapped provider cannot stream.
func Bulkhead(limit int, opts ...BulkheadOptions) Middleware {
	o := mergeBulkheadOptions(opts)

	var problem string
	if limit < 1 {
		problem = "the limit must be at least one request in flight"
	}

	var settings []impl.BulkheadOption
	if o.Reject {
		settings = append(settings, impl.RejectWhenFull())
	}
	build := impl.Bulkhead(limit, settings...)

	return func(next schemaflux.Provider) schemaflux.Provider {
		return wrap("mw.bulkhead", next, problem, build)
	}
}

// mergeBulkheadOptions merges option values left to right, a later field that
// is not zero winning.
func mergeBulkheadOptions(opts []BulkheadOptions) BulkheadOptions {
	var merged BulkheadOptions
	for _, o := range opts {
		if o.Reject {
			merged.Reject = true
		}
	}
	return merged
}
