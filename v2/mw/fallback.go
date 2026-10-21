package mw

import (
	"context"
	"errors"
	"slices"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"

	impl "github.com/monstercameron/schemaflux/v2/internal/runtime/mw"
)

// Capabilities is what a route is declared to deliver. It is the one dimension a
// request carries a signal for, so it is the one dimension Fallback can check: a
// declaration beyond it would be a guess.
type Capabilities struct {
	// NativeJSONSchema says that the route enforces the request's JSON Schema
	// on its own side, as a provider with a strict structured-output mode does,
	// and not only accepts a hint to write JSON. False is the route accepting
	// the hint alone.
	NativeJSONSchema bool
}

// A Route is one provider that Fallback may send a request to, with what it is
// declared to deliver.
type Route struct {
	// Provider answers requests sent by this route. A nil Provider is a
	// mistake in the set-up, and every request fails with an error of kind
	// schemaflux.ErrConfiguration before anything is sent.
	Provider schemaflux.Provider

	// Capabilities is what the route is declared to deliver. The zero value
	// declares nothing, which is the lack of the capability.
	Capabilities Capabilities

	// Model is the model this route runs the request on, when that is not the
	// model the request names. An alternate at another provider almost always
	// has its own: a request for one vendor's model cannot be served by another
	// vendor's provider. The request is sent to the route under this model, and
	// the client's data policy is checked for this provider and this model.
	// Empty sends the request's own model, and the policy is checked for that.
	Model string
}

// FallbackOptions are the settings of Fallback. The zero value is the default:
// any failure that is not the caller's or the policy's fails over, and a route
// that cannot enforce a schema is not used for a request that needs it.
type FallbackOptions struct {
	// On lists the kinds of failure that send a request to the next route. An
	// error triggers a fallover when its Kind is one of them, and then even
	// when it is one that stops everything by default. Naming a kind sends
	// requests where the default does not: ErrUnauthorized fails over a
	// rejected key to a route with another, ErrPolicy sends content to another
	// provider after one was refused it. The client's data policy is checked for
	// every route whatever this says.
	//
	// Nil is every failure except those that are not about the route. These
	// kinds stop the request and are returned as they are, because trying another
	// route would spend where a budget said not to, send content where a policy
	// said not to, carry on after the caller gave up, hide a defect, or get round
	// a limit the program set:
	//
	//	ErrConfiguration    ErrInvalidRequest   ErrUnauthorized    ErrPolicy
	//	ErrRefused          ErrBudgetExceeded   ErrBusy            ErrCanceled
	//	ErrClosed           ErrReviewRequired   ErrInternal
	//
	// ErrBusy is the program's own limit (a full Bulkhead), and ErrInternal a
	// defect, a panic in the chain, that is reported and not replaced by another
	// route's answer. These kinds go on to the next route:
	//
	//	ErrUnsupported      ErrRateLimited      ErrUnavailable     ErrTimeout
	//	ErrTooLarge         ErrTruncated        ErrInvalidAnswer
	//
	// that is: a capability the route lacks, which another route may have, an
	// outage, a rate limit, a timeout, an input too large for the model, an answer
	// cut off or one that failed its checks. So does a failure of the provider's own
	// that no layer classified. A kind that is not one of the library's kinds is a
	// configuration error. Naming kinds in On replaces this default.
	//
	// The kinds of every options value are joined: a later value adds to the
	// list and never takes a kind from it.
	On []schemaflux.Kind

	// AllowSchemaDegradation lets a route that cannot enforce a schema serve a
	// request that needs one, when the primary route is declared to. By
	// default such a route is skipped. The answer then carries no mark that it
	// was served without enforcement: the answer is checked against the schema
	// by the library whichever route wrote it, and this only allows the
	// route to be asked. A request needs one when it carries a schema and asks for
	// Format "json"; a request with a schema and no Format is not held to it. It
	// loosens a guard, so it can be set in the first options value only.
	AllowSchemaDegradation bool

	// Primary is what the wrapped provider, the route tried first, is declared
	// to deliver. An alternate that lacks a capability declared here is not
	// used, unless the lack has its own opt-in (AllowSchemaDegradation). The
	// zero value declares nothing, and then every alternate is eligible.
	Primary Capabilities
}

// Fallback sends a request to the next of alternates when the wrapped provider,
// the primary route, fails with an error that is about the route. The routes
// are tried in order, each only if the failure so far is one On names (or one of
// the default set), the route meets the declarations, and the client's data
// policy allows it: a route the policy does not allow is passed over as one that
// lacks a capability is, and the failure that sent the request looking is the one
// reported. When every route fails, the error returned is the last one's, in the
// sense that errors.Is and errors.As reach it: the failures of the routes before
// it are not kept, and nothing in it says that more than one route was tried. A
// failure that stops the request, and so is not tried again, is the one returned
// by the same reading, and not the identical value.
//
// The primary route is always tried first and is never checked against the
// declarations: they describe what a substitute must match, not a gate on the
// route being replaced.
//
// A failover is a different provider answering, so it is not free substitution.
// What this package checks is only what it is told: Capabilities are declared by
// the caller and are enforced as declared and no further; a route's data policy
// is the client's (Client.Policy), where it is checked against the provider
// and model the route will send. The answer names the route that served it, in
// Response.Provider and Response.Model, where the route does not say it is
// filled in.
//
// The alternates' own chains are the program's to build: a Retry, a
// CircuitBreaker and a RateLimit for each route go around that route's provider
// before it is given here, and Fallback goes around the primary route's. The
// package comment's order has Fallback outside the primary's Retry and
// CircuitBreaker, so that a route that is down is left for the next at once.
//
// A Fallback's Unwrap is its primary route: the one that is warmed, and whose
// retry settings, model table and decision limits are the chain's. An
// alternate's connection is opened by its first use. Fallback does not forward
// streaming: a wrapped provider cannot stream.
func Fallback(alternates []Route, opts ...FallbackOptions) Middleware {
	o, problem := mergeFallbackOptions(opts)
	for _, kind := range o.On {
		if kind < schemaflux.ErrConfiguration || kind > schemaflux.ErrInternal {
			problem = "On names a kind that is not one of the library's kinds"
			break
		}
	}
	routes := make([]impl.FallbackRoute, 0, len(alternates))
	for _, route := range alternates {
		if route.Provider == nil {
			problem = "a route has no Provider"
			break
		}
		routes = append(routes, impl.FallbackRoute{
			Handler:      internalOf(route.Provider),
			Capabilities: impl.FallbackCapabilities{NativeJSONSchema: route.Capabilities.NativeJSONSchema},
			Model:        route.Model,
		})
	}

	// With On set, a route's errors are given the one of two kinds the wrapped
	// package tells apart for this purpose, as the caller's list says, and the
	// original is given back as the request leaves: the wrapped package's own
	// kind taxonomy cannot tell a model's refusal from the data policy's, or a
	// panic from a bad answer, and the library's kinds can.
	selecting := len(o.On) > 0
	var on map[schemaflux.Kind]bool
	var settings []impl.FallbackOption
	if selecting {
		on = make(map[schemaflux.Kind]bool, len(o.On))
		for _, kind := range o.On {
			on[kind] = true
		}
		settings = append(settings, impl.WithFallbackKinds(types.KindProviderUnavailable))
		for i := range routes {
			routes[i].Handler = &kindGate{stage: stage{next: routes[i].Handler}, on: on}
		}
	}
	settings = append(settings, impl.WithPrimaryCapabilities(impl.FallbackCapabilities{NativeJSONSchema: o.Primary.NativeJSONSchema}))
	if o.AllowSchemaDegradation {
		settings = append(settings, impl.AllowSchemaDegradation())
	}
	build := impl.Fallback(routes, settings...)

	return func(next schemaflux.Provider) schemaflux.Provider {
		if !selecting {
			return wrap("mw.fallback", next, problem, build)
		}
		return wrap("mw.fallback", next, problem, func(primary llm.Provider) llm.Provider {
			return &peeling{stage{next: build(&kindGate{stage: stage{next: primary}, on: on})}}
		})
	}
}

// mergeFallbackOptions merges option values left to right. The kinds in On are
// joined, an earlier kind kept, and the capabilities declared for the primary
// route are joined, since a later value that declares one does not take back
// what an earlier one declared. AllowSchemaDegradation loosens a guard, so only
// the first value may set it: in a later one it is a problem, and the second
// result says so, for the caller to make a configuration error of.
func mergeFallbackOptions(opts []FallbackOptions) (FallbackOptions, string) {
	var merged FallbackOptions
	var problem string
	for i, o := range opts {
		for _, kind := range o.On {
			if !slices.Contains(merged.On, kind) {
				merged.On = append(merged.On, kind)
			}
		}
		if o.AllowSchemaDegradation {
			if i == 0 {
				merged.AllowSchemaDegradation = true
			} else if problem == "" {
				problem = "AllowSchemaDegradation is set in a later options value, which would loosen a guard an earlier one set: set it in the first"
			}
		}
		merged.Primary.NativeJSONSchema = merged.Primary.NativeJSONSchema || o.Primary.NativeJSONSchema
	}
	return merged, problem
}

// routeOp marks an error that a kindGate gave a kind for the wrapped package's
// choice of whether to fail over.
const routeOp = "mw.fallback.route"

// kindGate is the handler of a route when the caller named the kinds that fail
// over. It returns the route's failure under one of two kinds that the wrapped
// package reads as "fail over" and "stop", chosen by the library's Kind of the
// failure, with the failure itself as the cause. A peeling stage gives it back as
// it leaves the Fallback.
type kindGate struct {
	stage
	on map[schemaflux.Kind]bool
}

// Complete calls the route and relabels its failure.
func (g *kindGate) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	resp, err := g.next.Complete(ctx, req)
	if err == nil {
		return resp, nil
	}
	label := types.KindShutdown
	if converted, ok := bind.ConvertError("", err).(*schemaflux.Error); ok && g.on[converted.Kind] {
		label = types.KindProviderUnavailable
	}
	return llm.CompletionResponse{}, &types.OperationError{Kind: label, Op: routeOp, Cause: err}
}

// peeling is the outermost handler of a Fallback with a list of kinds. It gives
// back the failure a kindGate wrapped, so that nothing outside the Fallback sees
// the label.
type peeling struct{ stage }

// Complete calls the Fallback and unwraps a relabeled failure.
func (p *peeling) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	resp, err := p.next.Complete(ctx, req)
	if err == nil {
		return resp, nil
	}
	var labeled *types.OperationError
	if errors.As(err, &labeled) && labeled.Op == routeOp && labeled.Cause != nil {
		return llm.CompletionResponse{}, labeled.Cause
	}
	return llm.CompletionResponse{}, err
}
