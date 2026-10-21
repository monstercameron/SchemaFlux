package ops

import (
	"context"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// OB-006. Carrying the operation's name down to the one place that emits
// telemetry.
//
// CallLLM is the single funnel every provider call goes through, which makes
// it the only site that can observe a caller's own registered transport as
// well as the ones this repository ships. What it does not have is the name of
// the operation it is serving: types.OpOptions carries mode, tier, schema, and
// identifiers, but not "extract" or "classify", and the forty-odd operations
// in this package are hand-written functions rather than instances of one
// runner that could supply it.
//
// So the name travels on the context, the same shape callrecord.go already
// uses to get an execution record back out of CallLLM without changing the
// return type of every operation at once. An operation that knows its name
// attaches it; CallLLM reads it if it is there.
//
// This is deliberately not a field on types.OpOptions. Sixty-odd call sites
// build an OpOptions and pass it down, and a field that most of them forget to
// set is worse than a context value most of them do not carry: the field looks
// authoritative and is empty, while the absence of a context value is exactly
// what it is. When every operation lowers to one executor (A-001), the
// executor supplies the name directly and this goes away.

type operationNameKey struct{}

// WithOperationName returns a context naming the operation whose provider
// calls run under it. An empty name leaves ctx unchanged rather than storing
// one, so a caller that has nothing to say does not overwrite an outer
// operation's name with a blank.
func WithOperationName(ctx context.Context, name string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(name) == "" {
		return ctx
	}
	return context.WithValue(ctx, operationNameKey{}, nameForCaller(ctx, name))
}

// callerNameKey is the context key of the name a caller's API gave a call.
type callerNameKey struct{}

// callerName says that the call under it is the caller's operation named caller,
// which runs the internal operation named internal.
type callerName struct{ caller, internal string }

// WithCallerName returns a context under which the internal operation named
// internal is named caller wherever its name is reported: the operation scope
// its call opens, and the requests it makes. It is for a form of an operation
// that the caller's API names differently from the internal twin it runs (the
// version-2 API's "isdetail" runs the internal "is"), so that a trace names the
// call as the record of the call does. An operation the internal twin runs
// under another name, a sub-operation, keeps that name.
//
// The old root never calls it, so its names are unchanged. An empty name, and a
// caller that is the internal name already, leave ctx as it is.
func WithCallerName(ctx context.Context, caller, internal string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if caller == "" || internal == "" || caller == internal {
		return ctx
	}
	return context.WithValue(ctx, callerNameKey{}, callerName{caller: caller, internal: internal})
}

// nameForCaller is the name a call of the internal operation name is reported
// under on ctx: the caller's own when WithCallerName gave one for it, and name
// otherwise. It reads one context value and allocates nothing.
func nameForCaller(ctx context.Context, name string) string {
	if ctx == nil {
		return name
	}
	if given, ok := ctx.Value(callerNameKey{}).(callerName); ok && given.internal == name {
		return given.caller
	}
	return name
}

// operationNameFromContext reads the name WithOperationName attached, or "".
func operationNameFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	name, _ := ctx.Value(operationNameKey{}).(string)
	return name
}

type repairRequestKey struct{}

// repairContext returns the context a repair loop makes its request on:
// ctx itself for the first attempt, and one marked as a repair for every
// attempt after it. The mark is how the request's start event says it is a
// repair, which nothing else can tell it: a repair is an ordinary request with
// a longer prompt, and an operation that makes several requests in one call
// for other reasons (Sort over many items) must not have them read as repairs.
func repairContext(ctx context.Context, attempt int) context.Context {
	if attempt <= 0 || ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, repairRequestKey{}, true)
}

// isRepairRequest reports whether ctx was marked by repairContext.
func isRepairRequest(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(repairRequestKey{}).(bool)
	return marked
}

// operationNameFor resolves the best name available where telemetry is
// emitted, preferring what an operation said outright over what can be
// recovered from an identity it built for another purpose.
//
// The fallback reads the first segment of opts.CacheIdentity, which
// SchemaCacheKey renders as "operation:version:name:version:hash:dialect" --
// so an operation that set a cache identity has already named itself, and
// Extract is the one that does today (P-009). SchemaCacheKey writes "-" for an
// empty segment, which is not a name and is refused here rather than exported
// as one.
//
// An unresolved name is left empty, never guessed. A span called after the
// wrong operation is worse than one that admits it does not know which
// operation it served, because a reader cannot tell the two apart.
func operationNameFor(ctx context.Context, opts types.OpOptions) string {
	if name := operationNameFromContext(ctx); name != "" {
		return name
	}

	identity := strings.TrimSpace(opts.CacheIdentity)
	if identity == "" {
		return ""
	}
	head, _, found := strings.Cut(identity, ":")
	if !found || head == "-" {
		return ""
	}
	return head
}
