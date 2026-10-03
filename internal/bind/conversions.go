package bind

import (
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// This file is the accessors of the conversions the root registers. Every
// package that turns the root's types into the internal ones, or back, does it
// through them, so that one pair of functions decides each conversion: what a
// usage with no total adds up to, which kind an internal kind and its mark
// stand for, what a request that carries tools is refused as.
//
// The accessors of the typed conversions are generic in the root's type, which
// the caller names:
//
//	usage := bind.UsageFromInternal[schemaflux.Usage](tokens)
//
// The registered function is a func value held in an any. Asserting it to the
// func type the caller names costs a comparison of two type words and allocates
// nothing, which a function returning an any could not do for a struct.
//
// Before the root has registered, and when it registered none, a conversion
// returns the zero value of the type asked for, and the conversions that can
// fail return a configuration error saying no root package is linked. A function
// registered under a type other than the one asked for is a mistake in the code
// that asks, and panics with the types it found.

// conversion returns the function pick selects from the registered hooks as the
// func type F. ok is false when no root registered it.
func conversion[F any](pick func(*Hooks) any) (f F, ok bool) {
	h := registered.Load()
	if h == nil {
		return f, false
	}
	held := pick(h)
	if held == nil {
		return f, false
	}
	f, ok = held.(F)
	if !ok {
		panic("bind: a conversion is registered under a type other than the one it is asked for")
	}
	return f, true
}

// UsageFromInternal returns the root's Usage, which U names, for an internal
// token count. A provider reports its counts as prompt and completion tokens or
// as input and output tokens, and the root reads either.
func UsageFromInternal[U any](u types.TokenUsage) U {
	if f, ok := conversion[func(types.TokenUsage) U](func(h *Hooks) any { return h.UsageFromInternal }); ok {
		return f(u)
	}
	var zero U
	return zero
}

// UsageToInternal returns the internal token count for the root's Usage. A Total
// left at zero beside an Input or an Output is their sum, which is what Total is
// defined to be.
func UsageToInternal[U any](u U) types.TokenUsage {
	if f, ok := conversion[func(U) types.TokenUsage](func(h *Hooks) any { return h.UsageToInternal }); ok {
		return f(u)
	}
	return types.TokenUsage{}
}

// CostFromInternal returns the root's Cost, which C names, for an internal cost.
func CostFromInternal[C any](c types.CostInfo) C {
	if f, ok := conversion[func(types.CostInfo) C](func(h *Hooks) any { return h.CostFromInternal }); ok {
		return f(c)
	}
	var zero C
	return zero
}

// CostToInternal returns the internal cost for the root's Cost.
func CostToInternal[C any](c C) types.CostInfo {
	if f, ok := conversion[func(C) types.CostInfo](func(h *Hooks) any { return h.CostToInternal }); ok {
		return f(c)
	}
	return types.CostInfo{}
}

// TimingFromInternal returns the root's Timing, which T names, for an internal
// timing.
func TimingFromInternal[T any](t types.Timing) T {
	if f, ok := conversion[func(types.Timing) T](func(h *Hooks) any { return h.TimingFromInternal }); ok {
		return f(t)
	}
	var zero T
	return zero
}

// TimingToInternal returns the internal timing for the root's Timing.
func TimingToInternal[T any](t T) types.Timing {
	if f, ok := conversion[func(T) types.Timing](func(h *Hooks) any { return h.TimingToInternal }); ok {
		return f(t)
	}
	return types.Timing{}
}

// RequestFromInternal returns the root's Request, which R names, for an internal
// request. A request that carries tool declarations, a tool choice or a message
// list has nothing in a Request to carry them, and is refused with an error of
// the unsupported kind and not passed on with them dropped: the provider would
// answer a different question from the one asked.
func RequestFromInternal[R any](r llm.CompletionRequest) (R, error) {
	if f, ok := conversion[func(llm.CompletionRequest) (R, error)](func(h *Hooks) any { return h.RequestFromInternal }); ok {
		return f(r)
	}
	var zero R
	return zero, errNoRootForConversion
}

// RequestToInternal returns the internal request for the root's Request.
func RequestToInternal[R any](r R) llm.CompletionRequest {
	if f, ok := conversion[func(R) llm.CompletionRequest](func(h *Hooks) any { return h.RequestToInternal }); ok {
		return f(r)
	}
	return llm.CompletionRequest{}
}

// ResponseFromInternal returns the root's Response, which S names, for an
// internal response. A response that asks for tool calls is refused for the
// reason RequestFromInternal gives.
func ResponseFromInternal[S any](r llm.CompletionResponse) (S, error) {
	if f, ok := conversion[func(llm.CompletionResponse) (S, error)](func(h *Hooks) any { return h.ResponseFromInternal }); ok {
		return f(r)
	}
	var zero S
	return zero, errNoRootForConversion
}

// ResponseToInternal returns the internal response for the root's Response.
func ResponseToInternal[S any](r S) llm.CompletionResponse {
	if f, ok := conversion[func(S) llm.CompletionResponse](func(h *Hooks) any { return h.ResponseToInternal }); ok {
		return f(r)
	}
	return llm.CompletionResponse{}
}

// ChunkFromInternal returns the root's Chunk, which K names, for an internal
// stream event. The whole answer a last event carries is refused as
// ResponseFromInternal refuses one.
func ChunkFromInternal[K any](c llm.StreamChunk) (K, error) {
	if f, ok := conversion[func(llm.StreamChunk) (K, error)](func(h *Hooks) any { return h.ChunkFromInternal }); ok {
		return f(c)
	}
	var zero K
	return zero, errNoRootForConversion
}

// ChunkToInternal returns the internal stream event for the root's Chunk.
func ChunkToInternal[K any](c K) llm.StreamChunk {
	if f, ok := conversion[func(K) llm.StreamChunk](func(h *Hooks) any { return h.ChunkToInternal }); ok {
		return f(c)
	}
	return llm.StreamChunk{}
}

// LimitsFromInternal returns the root's DecisionLimits, which L names, for the
// limits an internal decision provider declares.
func LimitsFromInternal[L any](l llm.DecisionLimits) L {
	if f, ok := conversion[func(llm.DecisionLimits) L](func(h *Hooks) any { return h.LimitsFromInternal }); ok {
		return f(l)
	}
	var zero L
	return zero
}

// LimitsToInternal returns the limits an internal decision provider declares for
// the root's DecisionLimits.
func LimitsToInternal[L any](l L) llm.DecisionLimits {
	if f, ok := conversion[func(L) llm.DecisionLimits](func(h *Hooks) any { return h.LimitsToInternal }); ok {
		return f(l)
	}
	return llm.DecisionLimits{}
}

// KindOf returns the root's Kind, as its number, for an internal kind and the
// mark of the error that carries it (types.NarrowingOf), and zero for a kind the
// root has none for. A refusal and a recovered panic are told apart by the mark
// from the other errors of their kind; without it a policy violation is a policy
// violation.
//
// It returns zero before the root has registered.
func KindOf(kind types.ErrorKind, narrow types.Narrowing) uint8 {
	if h := registered.Load(); h != nil && h.KindOf != nil {
		return h.KindOf(kind, narrow)
	}
	return 0
}

// InternalKind returns the internal kind a root Kind, given as its number, is
// carried as through the call path and the mark that lets KindOf bring it back
// as the same Kind. A number that is not a Kind is carried as a provider being
// unavailable, and so is every number before the root has registered.
func InternalKind(kind uint8) (types.ErrorKind, types.Narrowing) {
	if h := registered.Load(); h != nil && h.InternalKind != nil {
		return h.InternalKind(kind)
	}
	return types.KindProviderUnavailable, types.NarrowNone
}

// KindOfError returns the root's Kind, as its number, of any error, and whether
// something decided it: an error that carries a kind, a sentinel of the internal
// packages, or what the internal classifier reads. It reports false, with a zero
// Kind, for an error nothing knows, and before the root has registered.
func KindOfError(err error) (kind uint8, known bool) {
	if h := registered.Load(); h != nil && h.KindOfError != nil {
		return h.KindOfError(err)
	}
	return 0, false
}

// FromProvider returns the root's *Error for an error that a built-in provider
// returned, with its Op left empty, and nil for nil: the kind of the first
// classified error of the chain, then what the internal classifier reads, and a
// provider that is unavailable for what nothing knows. Before the root has
// registered it returns err unchanged.
func FromProvider(err error) error {
	if err == nil {
		return nil
	}
	if h := registered.Load(); h != nil && h.FromProvider != nil {
		return h.FromProvider(err)
	}
	return err
}

// ToProvider returns the error the call path is given for an error a root
// Provider returned when it was asked for model: the kind of the chain, the
// provider, the model and the wait the retry loop reads, with the provider's
// error in its chain. An error that already carries an internal kind is the call
// path's own and is returned as it is. Before the root has registered it returns
// err unchanged.
func ToProvider(err error, provider, model string) error {
	if err == nil {
		return nil
	}
	if h := registered.Load(); h != nil && h.ToProvider != nil {
		return h.ToProvider(err, provider, model)
	}
	return err
}

// errNoRootForConversion is what a conversion that can fail returns when no root
// is registered.
var errNoRootForConversion = types.Newf(types.KindConfiguration, "conversion",
	"no root package of the library is linked into this program, so no request or response can be converted")
