package mw

import (
	"context"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// FallbackCapabilities is the one capability dimension this package can
// check against something concrete: whether a request asked for a strict,
// natively-enforced JSON Schema (CompletionRequest.JSONSchema is set and
// ResponseFormat is "json"). Everything else CP-001 will eventually cover
// has no corresponding signal in a CompletionRequest to check it against
// (see the doc comment on Fallback).
type FallbackCapabilities struct {
	// NativeJSONSchema reports whether this route enforces a JSON Schema
	// server-side (like OpenAI's Responses API strict mode) rather than
	// only accepting a "produce JSON" hint.
	NativeJSONSchema bool
}

// FallbackDataPolicy is a caller-declared classification label. Two routes
// are policy-compatible only when their Classification strings match; an
// empty Classification on the primary route means no policy was declared,
// and Fallback enforces nothing in that case -- it does not invent a
// "public" or "unrestricted" default, because a silent default is exactly
// the failure this exists to prevent. Declare it if it matters.
type FallbackDataPolicy struct {
	Classification string
}

// FallbackRoute is one candidate provider together with what it is declared
// to deliver.
type FallbackRoute struct {
	Handler      Handler
	Capabilities FallbackCapabilities
	DataPolicy   FallbackDataPolicy

	// Model is the model this route runs the request on, when that is not the
	// model the request names. An alternate at another provider almost always
	// has its own: a request for a gpt model cannot be served by a provider
	// that has none. When it is set the request is sent to this route under
	// this model, and the client's data policy is checked for this provider
	// and this model. Left empty, the request's own model is used for both.
	// The policy has to be checked against the route that will be used, and an
	// alternate that rewrites the model is not the route the request names.
	Model string
}

// FallbackOption configures the primary route's requirements and the
// substitution policy.
type FallbackOption func(*fallbackConfig)

type fallbackConfig struct {
	primaryCaps            FallbackCapabilities
	primaryPolicy          FallbackDataPolicy
	kinds                  map[types.ErrorKind]bool // nil means "any failure of the provider triggers fallover"
	allowSchemaDegradation bool
}

// WithPrimaryCapabilities declares what the wrapped (primary) route is
// relied upon to deliver. An alternate lacking a capability declared here is
// never used as a substitute, unless that specific gap has its own explicit
// opt-in (see AllowSchemaDegradation).
func WithPrimaryCapabilities(c FallbackCapabilities) FallbackOption {
	return func(cfg *fallbackConfig) { cfg.primaryCaps = c }
}

// WithPrimaryDataPolicy declares what the wrapped (primary) route is
// permitted to receive. An alternate whose declared classification does not
// match is never used as a substitute -- a private-region primary route
// does not fail over to a route declared "public".
func WithPrimaryDataPolicy(p FallbackDataPolicy) FallbackOption {
	return func(cfg *fallbackConfig) { cfg.primaryPolicy = p }
}

// WithFallbackKinds restricts which failure kinds trigger a fallover, using
// the same taxonomy llm.Classify and every other consumer of it in this
// module already agree on. Without this option, a failure of the route is
// eligible to trigger a fallover to the next qualifying route -- the default a
// caller wiring "provider failover" in the ordinary sense expects -- except a
// failure that stops everything: one whose kind types.OperationError.Terminal
// reports as terminal (a refused budget or data policy, a cancellation, a
// shutdown, a configuration or authentication or permission error, a request
// the provider rejected as invalid). Those are not about the route, and sending
// the same request to another route would spend where a budget said not to,
// send content where a policy said not to, or carry on after the caller gave up.
// A refusal as an unsupported capability is about the route and is not among
// them: a provider that cannot take a request's tools, messages or search
// refuses it before sending anything, and the next route is tried.
// A full bulkhead (types.KindAdmissionRejected) and a panic (an invariant
// violation marked types.NarrowPanic) are not about the route either, and stop a
// default Fallback: the first is a limit the program set, and the second a
// defect, and neither is cured by asking another provider.
// A caller who only wants to fail over on the route itself being unavailable,
// not on the answer being unusable, narrows it here, and a kind named here
// triggers a fallover even when it is terminal.
func WithFallbackKinds(kinds ...types.ErrorKind) FallbackOption {
	return func(cfg *fallbackConfig) {
		cfg.kinds = make(map[types.ErrorKind]bool, len(kinds))
		for _, k := range kinds {
			cfg.kinds[k] = true
		}
	}
}

// AllowSchemaDegradation permits an alternate lacking NativeJSONSchema to
// serve a request that asked for one, when the primary route declared it.
// Off by default, per the Revised line: "a named degradation ... is allowed
// only by explicit policy." See the package doc comment on Fallback for what
// this option does NOT do (it does not record the degradation anywhere the
// caller can observe after the fact).
func AllowSchemaDegradation() FallbackOption {
	return func(cfg *fallbackConfig) { cfg.allowSchemaDegradation = true }
}

// Fallback builds a Middleware that tries alternates, in order, when the
// wrapped provider (or a prior alternate) fails with a fallover-eligible
// error and the alternate qualifies under the declared requirements -- but
// only to an alternate that was DECLARED, by the caller wiring this
// middleware, to meet the primary route's minimum capability and
// data-policy requirements. TODOS.md's Revised line on MW-008 is explicit
// that failover is not free substitution: a private-region failure may not
// fall back to a public provider, and a route that cannot deliver the
// requested contract may not be substituted in silently. This is the
// mechanism that refuses to do either.
//
// The wrapped provider (whatever Chain passes as `next`) is always the
// primary route and is always tried first, regardless of any capability or
// policy declared for it -- those declarations describe what a SUBSTITUTE
// must match, not a gate on the route being replaced.
//
// # What is and is not enforced here, and why (read before wiring this up)
//
// CP-001 (machine-readable provider capabilities, negotiated automatically)
// and CP-002 (a DataPolicy type with classification, region, and retention,
// enforced at the client/tenant boundary) are both still open in TODOS.md.
// Nothing in this codebase can currently introspect a provider and answer
// "does this one support native JSON Schema" or "is this one licensed to
// receive EU personal data" -- there is no capability struct, no policy
// struct, and no field on llm.Provider that would let this middleware derive
// either fact on its own. Building that here, one middleware deep, would be
// exactly the fabrication AGENTS.md forbids: a number (or a boolean) that
// looks measured and is actually guessed.
//
// So this package does the honest half instead: FallbackRoute carries
// CALLER-DECLARED capability and data-policy labels, and Fallback refuses to
// route to an alternate whose declared labels do not meet the primary
// route's declared requirement. This is real enforcement -- an alternate
// that does not qualify is never called, provably (see the fallback_test.go
// cases that assert a disqualified alternate's Complete is never invoked) --
// but it is only as good as what the caller declares. An undeclared
// (zero-value) requirement enforces nothing, and this file does not pretend
// otherwise anywhere in its docs.
//
// What is explicitly NOT done, because the plumbing to do it honestly does
// not exist yet:
//
//   - Recording a schema degradation "as delivered" (the Revised line's
//     second clause). A-006's Result/Meta envelope is where requested-versus-
//     delivered contract level lives, and it is populated in internal/ops
//     from a call record carried on context (internal/ops/callrecord.go) --
//     a layer above the llm.Provider seam this middleware operates at.
//     mw.Fallback has no handle on that envelope and cannot reach back into
//     it without either internal/ops depending on mw (backwards from
//     today's dependency direction, since mw is the generic, ops-agnostic
//     layer) or mw depending on internal/ops (which would make a low-level
//     transport middleware import the operation layer that sits on top of
//     it). AllowSchemaDegradation still gates the substitution -- it cannot
//     happen without the caller opting in -- but the resulting response
//     carries no marker that a degradation occurred. Closing this needs a
//     seam A-001/A-006 do not expose yet: a way for a Provider-level
//     middleware to annotate the envelope the operation layer eventually
//     builds.
//   - Any capability or policy dimension beyond the two declared on
//     FallbackCapabilities and FallbackDataPolicy, with one exception.
//     CP-001 lists streaming, tool calling, multi-turn, prompt caching,
//     idempotency keys, seed, and context/output ceilings; none of those
//     have a corresponding signal anywhere in a CompletionRequest or on
//     llm.Provider today, so there is nothing truthful to check them
//     against. The exception is the provider, the regions and the retention
//     of an alternate, which a client's data policy (Client.WithDataPolicy)
//     is checked against before the alternate is used: see FallbackRoute.Model
//     and ops.CheckRoutePolicy.
//
// A FallbackRoute with a nil Handler is left out when Fallback is built, the
// way Chain leaves out a nil Middleware: there is nothing to call, and keeping
// it would panic on the first failure of the primary, far from where the route
// was written.
func Fallback(alternates []FallbackRoute, opts ...FallbackOption) Middleware {
	cfg := fallbackConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	routes := make([]FallbackRoute, 0, len(alternates))
	for _, route := range alternates {
		if route.Handler != nil {
			routes = append(routes, route)
		}
	}

	return func(next Handler) Handler {
		return &fallbackProvider{
			primary: FallbackRoute{
				Handler:      next,
				Capabilities: cfg.primaryCaps,
				DataPolicy:   cfg.primaryPolicy,
			},
			alternates: routes,
			cfg:        cfg,
		}
	}
}

type fallbackProvider struct {
	primary    FallbackRoute
	alternates []FallbackRoute
	cfg        fallbackConfig
}

func (p *fallbackProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	resp, err := p.primary.Handler.Complete(ctx, req)
	if err == nil {
		return resp, nil
	}

	lastErr := err
	for _, alt := range p.alternates {
		if !p.triggers(lastErr) {
			// This failure -- the primary's, or a prior alternate's -- is
			// classified on its own terms and is not one this Fallback was
			// configured to route around. Stop rather than keep trying:
			// working through every remaining route on a kind of failure
			// none of them can fix just delays returning the real error.
			break
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return llm.CompletionResponse{}, ctxErr
		}
		if !p.eligible(req, alt) {
			continue
		}
		// The client's data policy governs where its content may go, and an
		// alternate is somewhere it may go. It is held to the policy by the same
		// check the primary was, before it is sent anything. An alternate the
		// policy does not allow is passed over like one that lacks a capability:
		// it was never an option, so the failure that is reported is the one
		// that sent this looking.
		routed := req
		if alt.Model != "" {
			routed.Model = alt.Model
		}

		// The model checked is the one the alternate will send, which is not
		// always the one the request names (llm.ModelSentBy).
		sent := llm.ModelSentBy(alt.Handler, routed.Model)
		if ops.CheckRoutePolicy(ctx, alt.Handler.Name(), sent) != nil {
			continue
		}

		resp, err = alt.Handler.Complete(ctx, routed)
		if err == nil {
			// The answer says which route it came by. An alternate that does
			// not say is spoken for here, since this is where it is known: the
			// client's data policy checks the route an answer names, and an
			// answer with no name on it is taken for the primary's.
			if resp.Provider == "" {
				resp.Provider = alt.Handler.Name()
			}
			if resp.Model == "" {
				resp.Model = sent
			}
			return resp, nil
		}
		lastErr = err
	}

	return llm.CompletionResponse{}, lastErr
}

// eligible reports whether alt meets the primary route's declared minimum
// requirements for the given request. An alternate that fails this check is
// never called -- that is the property the tests in fallback_test.go pin.
func (p *fallbackProvider) eligible(req llm.CompletionRequest, alt FallbackRoute) bool {
	// An alternate without native JSON Schema support serves a request that
	// needs it only when AllowSchemaDegradation says so. The degradation is not
	// recorded as delivered anywhere -- see the doc comment on Fallback.
	wantsNativeSchema := len(req.JSONSchema) > 0 && req.ResponseFormat == "json"
	if wantsNativeSchema && p.primary.Capabilities.NativeJSONSchema &&
		!alt.Capabilities.NativeJSONSchema && !p.cfg.allowSchemaDegradation {
		return false
	}

	if p.primary.DataPolicy.Classification != "" &&
		alt.DataPolicy.Classification != p.primary.DataPolicy.Classification {
		return false
	}

	return true
}

// triggers reports whether err should cause Complete to try the next route.
// With kinds configured only those kinds do. Without, any failure does except
// one that is terminal: a failure that stops everything is not the route's.
//
// An unsupported capability is terminal for everything else that reads the kind,
// and is not terminal here. A provider refuses with it before it sends anything,
// because of what the provider can do (tools, a message list, a search, an
// effort) and not because of anything the request or the caller did wrong, so
// another route may well take the same request. Treating it as the end sent a
// request with tools to a primary that cannot take them and never asked the
// alternate that can.
func (p *fallbackProvider) triggers(err error) bool {
	if err == nil {
		return false
	}
	kind := llm.Classify(err)
	if p.cfg.kinds != nil {
		return p.cfg.kinds[kind]
	}
	if kind == types.KindUnsupportedCapability {
		return true
	}
	// Two more are not about the route, though nothing calls them terminal. A
	// full bulkhead (an admission refused) is the limit the program set on its own
	// concurrency, and failing over from it sends the request to a route the limit
	// was meant to keep it from. A panic is a defect in this process, and an
	// alternate that quietly answers in its place hides it.
	if kind == types.KindAdmissionRejected || (kind == types.KindInvariantViolation && types.NarrowingOf(err) == types.NarrowPanic) {
		return false
	}
	return !kindIsTerminal(kind)
}

// Name, EstimateCost, and RetryPolicy describe the primary route, matching
// every other middleware in this package: they answer "what is this,
// nominally", not "whichever route happened to serve the last call".
func (p *fallbackProvider) Name() string { return p.primary.Handler.Name() }

func (p *fallbackProvider) EstimateCost(req llm.CompletionRequest) float64 {
	return p.primary.Handler.EstimateCost(req)
}

func (p *fallbackProvider) RetryPolicy() (int, time.Duration) {
	return p.primary.Handler.RetryPolicy()
}
