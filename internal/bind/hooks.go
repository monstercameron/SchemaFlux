// Package bind is the junction between the root package of version 2 and the
// packages beside it (exp, redact, flow, op, provider, mw, schemafluxtest).
//
// Every operation function of version 2 does the same work around the internal
// call it makes: it settles the common options, runs the call, and turns what
// came back into the root's types. That work has to be callable from the
// subpackages as well as from the root, and a subpackage imports the root for
// its types, so the work cannot live unexported in the root and cannot import
// it. It lives here, in a package the root is not allowed to be imported by,
// and what it needs of the root reaches it as values the root registers once,
// at init, with Register.
//
// This file holds the registered values and the accessors every caller uses.
// common.go holds Common, the common options in the internal types, call.go the
// helper that wraps one call of an operation function (Begin, End, Recorded,
// Stream), conversions.go the accessors of the root's conversions between its
// types and the internal ones, and bound.go the limits on what is written to a
// log. Nothing in this package imports a public package or anything under v2.
package bind

import (
	"context"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Hooks are the functions the root package gives this package at init, because
// they have to name types of the root and this package cannot import it. The
// root registers them together, once, with Register.
//
// A function left nil is a hook that was not registered: the accessor for it
// answers as it does before anything is registered. The root registers all of
// them; a nil is for the tests of this package, which register stand-ins.
//
// The conversions between the root's types and the internal ones are fields of
// type any, each holding a function of the type its comment gives. They are not
// declared with those types because this package cannot name the root's, and
// they are read through the generic accessors of conversions.go, which name the
// root's type at the call and so allocate nothing: a function returning an any
// would box every value it converted.
type Hooks struct {
	// Convert returns the root's *Error for an error of the call path of the
	// operation named op, and nil for a nil error. An error that is already
	// the root's own comes back with the fields it left empty filled in.
	Convert func(op string, err error) error

	// ToInternal returns the llm.Provider for a root Provider, which is passed
	// as an any because this package does not name the type. A Provider that
	// is a built-in provider seen from the root comes back as the built-in one,
	// not wrapped a second time. It returns nil for a value that is not a
	// Provider.
	ToInternal func(provider any) llm.Provider

	// FromInternal returns a root Provider, as an any, for an llm.Provider. An
	// llm.Provider that is a root Provider seen from the internal side comes
	// back as that Provider, not wrapped a second time. It returns nil for nil.
	FromInternal func(provider llm.Provider) any

	// Common returns the common settings of an options value in the internal
	// types, and the root's own configuration error for a setting no call can
	// use. options is the root's Options, which a subpackage's options type
	// embeds and which this package cannot name; any other value is a mistake of
	// the caller of this package and is an error. The error is the one the root's
	// check gives with the operation left empty, which ConvertError fills in.
	Common func(options any) (Common, error)

	// Unwrap returns the provider a root Provider wraps, as an any, or nil when
	// it wraps none. It is the one step of the walk through a chain of wrappers
	// that Optional takes: the root's Provider is the type a wrapper's Unwrap
	// method returns, and this package cannot name it.
	Unwrap func(provider any) any

	// KindOf returns the root's Kind, as its number, for an internal kind and the
	// narrowing mark the internal error carries, and zero for a kind the root has
	// none for (types.KindUnknown). The mark is read first: a refusal and a
	// recovered panic are told apart from the other errors of the kind they were
	// filed under, and a policy violation with no mark is a policy violation.
	KindOf func(kind types.ErrorKind, narrow types.Narrowing) uint8

	// InternalKind returns the internal kind a root Kind, given as its number, is
	// carried as through the call path, and the mark that lets KindOf bring it
	// back as the same Kind: it is the inverse of KindOf for every Kind. A number
	// that is not a Kind is a provider being unavailable.
	InternalKind func(kind uint8) (types.ErrorKind, types.Narrowing)

	// KindOfError returns the root's Kind of any error, and whether something
	// decided it: an error that carries a kind, a sentinel of the internal
	// packages, or what the internal classifier reads. An error nothing knows
	// reports false, and the Kind is then zero.
	KindOfError func(err error) (kind uint8, known bool)

	// FromProvider returns the root's *Error for an error that a built-in
	// provider returned: the kind of the first classified error of the chain,
	// then what the internal classifier reads, and a provider that is
	// unavailable for what nothing knows. Its Op is left empty.
	FromProvider func(err error) error

	// ToProvider returns the error the call path is given for an error a root
	// Provider returned, with the provider and model it was asked as the route:
	// the kind of the chain, the route and the wait the retry loop reads, and the
	// provider's error in its chain.
	ToProvider func(err error, provider, model string) error

	// Variables returns the environment variables the key and the base URL of the
	// built-in provider called name are read from, in the order they are tried,
	// and whether the provider reads any. It is the root's table, which a test
	// holds to what the environment parser reads; the provider package reads the
	// same one.
	Variables func(name string) (keys, baseURLs []string, ok bool)

	// The conversions. Each is a function of the type in its comment, with U, C,
	// T, R, S, K and L standing for the root's Usage, Cost, Timing, Request,
	// Response, Chunk and DecisionLimits.

	// UsageFromInternal is func(types.TokenUsage) U.
	UsageFromInternal any
	// UsageToInternal is func(U) types.TokenUsage.
	UsageToInternal any
	// CostFromInternal is func(types.CostInfo) C.
	CostFromInternal any
	// CostToInternal is func(C) types.CostInfo.
	CostToInternal any
	// TimingFromInternal is func(types.Timing) T.
	TimingFromInternal any
	// TimingToInternal is func(T) types.Timing.
	TimingToInternal any
	// RequestFromInternal is func(llm.CompletionRequest) (R, error). It fails for
	// a request that carries what a root Request has no field for.
	RequestFromInternal any
	// RequestToInternal is func(R) llm.CompletionRequest.
	RequestToInternal any
	// ResponseFromInternal is func(llm.CompletionResponse) (S, error). It fails
	// for a response that carries what a root Response has no field for.
	ResponseFromInternal any
	// ResponseToInternal is func(S) llm.CompletionResponse.
	ResponseToInternal any
	// ChunkFromInternal is func(llm.StreamChunk) (K, error).
	ChunkFromInternal any
	// ChunkToInternal is func(K) llm.StreamChunk.
	ChunkToInternal any
	// LimitsFromInternal is func(llm.DecisionLimits) L, with L the root's
	// DecisionLimits.
	LimitsFromInternal any
	// LimitsToInternal is func(L) llm.DecisionLimits.
	LimitsToInternal any
}

// registered is the Hooks the root registered, or nil before it did. It is
// written once, by Register, and read by every accessor without a lock.
var registered atomic.Pointer[Hooks]

// Register records the functions the root provides and returns an empty value,
// so that the root can write
//
//	var _ = bind.Register(bind.Hooks{...})
//
// and have the registration run as a package-level initialiser. Go initialises
// a package after every package it imports, so a subpackage that imports the
// root, and calls this package from an init of its own, runs after the root has
// registered.
//
// Register is called once for the life of the program. Calling it again with
// the same functions does nothing. Calling it again with other functions
// panics, and leaves the first registration in force: it means two roots are
// linked into one program, which is a mistake of the build and cannot be
// repaired at run time. Functions are told apart by their code, so two closures
// of one function literal count as the same function; the root registers
// package-level functions.
//
// Register is safe for concurrent use. It is for the root of version 2 and for
// the tests of this package; it is not a setting a caller can change.
func Register(h Hooks) struct{} {
	next := &h
	for {
		current := registered.Load()
		if current == nil {
			if registered.CompareAndSwap(nil, next) {
				return struct{}{}
			}
			continue
		}
		if sameHooks(*current, h) {
			return struct{}{}
		}
		panic("bind: Register was called twice with different functions: more than one root package of the library is linked into this program")
	}
}

// Registered reports whether the root has registered its hooks. It is for code
// that wants to name the missing registration in its own error; the accessors
// of this package already answer sensibly when it has not.
func Registered() bool {
	return registered.Load() != nil
}

// ConvertError returns the root's *Error for err, an error of the call path of
// the operation named op. It returns nil for a nil error, without calling
// anything and without allocating.
//
// Before the root has registered, and when it registered no Convert, the error
// is returned unchanged: this package is then running in a program without the
// root, which is only its own tests, and there is no root error to convert to.
func ConvertError(op string, err error) error {
	if err == nil {
		return nil
	}
	if h := registered.Load(); h != nil && h.Convert != nil {
		return h.Convert(op, err)
	}
	return err
}

// ToInternal returns the llm.Provider the call path takes for provider, a root
// Provider passed as an any. It returns nil when provider is nil, and what the
// registered ToInternal returns otherwise: nil for a value that is not a
// Provider, the built-in provider itself for one that was made from a built-in
// provider, and an adapter for any other. Adapting a provider that is already
// adapted allocates nothing.
//
// Before the root has registered, and when it registered no ToInternal, a
// non-nil provider gives a provider whose every Complete fails with an error
// of kind configuration saying that no root package is linked. A call fails
// with that error instead of reaching a nil provider.
func ToInternal(provider any) llm.Provider {
	if provider == nil {
		return nil
	}
	if h := registered.Load(); h != nil && h.ToInternal != nil {
		return h.ToInternal(provider)
	}
	return unregisteredProvider{}
}

// FromInternal returns the root Provider, as an any, for provider, an
// llm.Provider of the call path. The caller asserts the result to the root's
// Provider type. It returns nil when provider is nil, and what the registered
// FromInternal returns otherwise: the Provider an internal adapter was made
// from, or an adapter over a built-in provider. Taking back a provider that
// ToInternal adapted allocates nothing.
//
// Before the root has registered, and when it registered no FromInternal, it
// returns nil: without the root there is no Provider type to adapt to, and a
// nil is what a caller reads as "no provider".
func FromInternal(provider llm.Provider) any {
	if provider == nil {
		return nil
	}
	if h := registered.Load(); h != nil && h.FromInternal != nil {
		return h.FromInternal(provider)
	}
	return nil
}

// CommonOf returns the common settings of options, the root's Options as an
// any, in the internal types, and the root's configuration error when a setting
// is one no call can use (a negative ceiling, a tier or a mode that is not one
// of the constants). A subpackage whose options type embeds the root's Options
// passes the embedded value: bind.CommonOf(o.Options). The operation function
// passes the error through ConvertError with its own name, which fills in the
// operation the error left empty.
//
// Before the root has registered, and when it registered no Common, it returns
// the zero Common and an error of kind configuration saying no root package is
// linked: there is no Options to read without one.
func CommonOf(options any) (Common, error) {
	if h := registered.Load(); h != nil && h.Common != nil {
		return h.Common(options)
	}
	return Common{}, errNoRootForOptions
}

// maxUnwrapDepth is how many wrappers Optional looks through. A chain is a
// handful of layers; the bound is for a wrapper that returns itself.
const maxUnwrapDepth = 64

// Optional returns the first of p and what it wraps, through the root's Unwrap
// method (the registered Unwrap hook), that is a T, and reports whether there
// is one. A wrapper that is a T itself is found before the provider under it.
// It is the one lookup of an optional interface of a provider (streaming,
// warming, retry settings, decision limits, the model of a tier) that the root
// and the middleware package make, so that a wrapper which does not hide its
// inner provider hides none of what the inner provider can do.
//
// It looks through at most 64 wrappers, so a chain that loops ends the walk
// with false and not with a hang. It reports false for a nil p, and for a p
// that is not a T when the root has registered no Unwrap, which is p alone.
// It allocates nothing.
//
// The unwrapping step is a registered function and not an interface this
// package declares because the method a wrapper writes returns the root's
// Provider, a type this package cannot name; a wrapper written by a caller has
// that method and no other. T is an interface of the root, of the provider
// package or of this module's own internals; p is a value of the root's
// Provider type.
func Optional[T any](p any) (T, bool) {
	var unwrap func(any) any
	if h := registered.Load(); h != nil {
		unwrap = h.Unwrap
	}
	for depth := 0; p != nil && depth < maxUnwrapDepth; depth++ {
		if found, ok := p.(T); ok {
			return found, true
		}
		if unwrap == nil {
			break
		}
		p = unwrap(p)
	}
	var none T
	return none, false
}

// sameHooks reports whether a and b hold the same functions. A func cannot be
// compared with ==, so each is compared by its code pointer; two nil functions
// are the same.
func sameHooks(a, b Hooks) bool {
	return codePointer(a.Convert) == codePointer(b.Convert) &&
		codePointer(a.ToInternal) == codePointer(b.ToInternal) &&
		codePointer(a.FromInternal) == codePointer(b.FromInternal) &&
		codePointer(a.Common) == codePointer(b.Common) &&
		codePointer(a.Unwrap) == codePointer(b.Unwrap) &&
		codePointer(a.KindOf) == codePointer(b.KindOf) &&
		codePointer(a.InternalKind) == codePointer(b.InternalKind) &&
		codePointer(a.KindOfError) == codePointer(b.KindOfError) &&
		codePointer(a.FromProvider) == codePointer(b.FromProvider) &&
		codePointer(a.ToProvider) == codePointer(b.ToProvider) &&
		codePointer(a.Variables) == codePointer(b.Variables) &&
		codePointer(a.UsageFromInternal) == codePointer(b.UsageFromInternal) &&
		codePointer(a.UsageToInternal) == codePointer(b.UsageToInternal) &&
		codePointer(a.CostFromInternal) == codePointer(b.CostFromInternal) &&
		codePointer(a.CostToInternal) == codePointer(b.CostToInternal) &&
		codePointer(a.TimingFromInternal) == codePointer(b.TimingFromInternal) &&
		codePointer(a.TimingToInternal) == codePointer(b.TimingToInternal) &&
		codePointer(a.RequestFromInternal) == codePointer(b.RequestFromInternal) &&
		codePointer(a.RequestToInternal) == codePointer(b.RequestToInternal) &&
		codePointer(a.ResponseFromInternal) == codePointer(b.ResponseFromInternal) &&
		codePointer(a.ResponseToInternal) == codePointer(b.ResponseToInternal) &&
		codePointer(a.ChunkFromInternal) == codePointer(b.ChunkFromInternal) &&
		codePointer(a.ChunkToInternal) == codePointer(b.ChunkToInternal) &&
		codePointer(a.LimitsFromInternal) == codePointer(b.LimitsFromInternal) &&
		codePointer(a.LimitsToInternal) == codePointer(b.LimitsToInternal)
}

// codePointer is the address of the code of the function f, which is zero for a
// nil function or no value at all. It is how two functions are told apart
// without calling them.
func codePointer(f any) uintptr {
	if f == nil {
		return 0
	}
	return reflect.ValueOf(f).Pointer()
}

// errNotRegistered is what an unregistered provider fails every call with. It
// is built once: a call that fails must not allocate to say why.
var errNotRegistered = types.Newf(types.KindConfiguration, "provider",
	"no root package of the library is linked into this program, so no Provider can be used")

// errNoRootForOptions is what CommonOf returns when no root is registered.
var errNoRootForOptions = types.Newf(types.KindConfiguration, "options",
	"no root package of the library is linked into this program, so no Options can be read")

// unregisteredProvider is what ToInternal returns before the root has
// registered. It has no fields, so converting it to an interface allocates
// nothing.
type unregisteredProvider struct{}

// Complete fails with the configuration error of errNotRegistered.
func (unregisteredProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, errNotRegistered
}

// Name is "unregistered".
func (unregisteredProvider) Name() string { return "unregistered" }

// EstimateCost is zero.
func (unregisteredProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }

// RetryPolicy is zero retries: the failure does not depend on the attempt.
func (unregisteredProvider) RetryPolicy() (int, time.Duration) { return 0, 0 }
