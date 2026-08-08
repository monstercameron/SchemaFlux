package mw

import (
	"context"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
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
}

// FallbackOption configures the primary route's requirements and the
// substitution policy.
type FallbackOption func(*fallbackConfig)

type fallbackConfig struct {
	primaryCaps            FallbackCapabilities
	primaryPolicy          FallbackDataPolicy
	kinds                  map[types.ErrorKind]bool // nil means "any failure triggers fallover"
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
// module already agree on. Without this option, any non-nil error from a
// route is eligible to trigger a fallover to the next qualifying route --
// the default a caller wiring "provider failover" in the ordinary sense
// expects. A caller who only wants to fail over on the route itself being
// unavailable, not on the answer being unusable, narrows it here.
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
//     FallbackCapabilities and FallbackDataPolicy. CP-001 lists streaming,
//     tool calling, multi-turn, prompt caching, idempotency keys, seed,
//     regions, retention modes, and context/output ceilings; none of those
//     have a corresponding signal anywhere in a CompletionRequest or on
//     llm.Provider today, so there is nothing truthful to check them
//     against.
func Fallback(alternates []FallbackRoute, opts ...FallbackOption) Middleware {
	cfg := fallbackConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	routes := append([]FallbackRoute(nil), alternates...)

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

	lastResp, lastErr := resp, err
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

		resp, err = alt.Handler.Complete(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastResp, lastErr = resp, err
	}

	return lastResp, lastErr
}

// eligible reports whether alt meets the primary route's declared minimum
// requirements for the given request. An alternate that fails this check is
// never called -- that is the property the tests in fallback_test.go pin.
func (p *fallbackProvider) eligible(req llm.CompletionRequest, alt FallbackRoute) bool {
	wantsNativeSchema := len(req.JSONSchema) > 0 && req.ResponseFormat == "json"
	if wantsNativeSchema && p.primary.Capabilities.NativeJSONSchema && !alt.Capabilities.NativeJSONSchema {
		if !p.cfg.allowSchemaDegradation {
			return false
		}
		// Explicitly allowed by policy. Not recorded as delivered anywhere
		// -- see the package doc comment's honesty note on Fallback.
	}

	if p.primary.DataPolicy.Classification != "" &&
		alt.DataPolicy.Classification != p.primary.DataPolicy.Classification {
		return false
	}

	return true
}

// triggers reports whether err should cause Complete to try the next route.
func (p *fallbackProvider) triggers(err error) bool {
	if err == nil {
		return false
	}
	if p.cfg.kinds == nil {
		return true
	}
	return p.cfg.kinds[llm.Classify(err)]
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
