package llm

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// CP-001. Today "supported provider" means an adapter compiles: mw.Fallback
// (MW-008) and mw.Cache (MW-004) both had to fall back to a CALLER-DECLARED
// label because nothing in this package could answer "does this route
// support native JSON Schema" or "what region does this route run in" on its
// own. This file is that introspection: a capability vocabulary, a small
// registry of what is actually known about specific provider/model routes,
// and a negotiation function that intersects what an operation needs against
// what a route is declared to deliver.
//
// # Never fail open
//
// A route this registry has never heard of is UNKNOWN, not permissive. Every
// function here that decides eligibility treats "no declaration" as "cannot
// meet any requirement" -- the same rule mw.Fallback's doc comment already
// states for its own caller-declared labels, now enforced against real data
// instead of a label the caller could get wrong. A capability guess that
// turns out to be false is worse than an outright refusal: the refusal is
// visible immediately, the wrong guess ships a request to a route that
// cannot actually deliver what was asked and only fails later, if at all.

// Capability names one machine-checkable feature of a provider/model route.
// This is deliberately a small, closed set rather than an open string: a
// typo'd capability name that nothing in the registry ever sets is
// indistinguishable from "not supported," which is safe, but a typo'd name
// used as a REQUIREMENT that nothing ever satisfies is a silent way to make
// every route ineligible, and a closed set turns that into a compile error
// instead of a runtime mystery.
type Capability string

const (
	// CapNativeJSONSchema is the capability mw.Fallback's FallbackCapabilities
	// already names: a route that enforces a JSON Schema server-side (like
	// the Responses API's strict json_schema format), not just a "produce
	// JSON" hint.
	CapNativeJSONSchema Capability = "native_json_schema"

	// CapJSONMode is the weaker guarantee: the provider will bias output
	// toward valid JSON syntax without enforcing a schema against it.
	CapJSONMode Capability = "json_mode"

	// CapStreaming is StreamingProvider support -- CompleteStream exists and
	// is expected to work, not merely that the type assertion would compile.
	CapStreaming Capability = "streaming"

	// CapToolCalling is CompletionRequest.Tools support.
	CapToolCalling Capability = "tool_calling"

	// CapMultiTurn is support for a conversation history longer than one
	// system/user exchange, as opposed to a single-shot completion.
	CapMultiTurn Capability = "multi_turn"

	// CapPromptCaching is support for CompletionRequest.PromptCacheKey --
	// the provider actually uses the hint to route to a warm prefix, rather
	// than silently ignoring an unrecognized field.
	CapPromptCaching Capability = "prompt_caching"

	// CapIdempotencyKeys is support for a caller-supplied idempotency key
	// that lets a retried request be recognized as a retry rather than
	// re-executed.
	CapIdempotencyKeys Capability = "idempotency_keys"

	// CapSeed is support for deterministic sampling via a caller-supplied
	// seed.
	CapSeed Capability = "seed"

	// CapExactUsageReporting distinguishes a route that reports measured
	// token counts from one that estimates them. See UsageQuality for the
	// three-way version of this fact; this capability is the boolean
	// "exact, specifically" shorthand used by requirement checks that only
	// care about that one level.
	CapExactUsageReporting Capability = "exact_usage_reporting"

	// CapModelRevisionReporting is support for the response naming the exact
	// model revision that served it, as opposed to only echoing back the
	// alias the caller requested.
	CapModelRevisionReporting Capability = "model_revision_reporting"
)

// UsageQuality describes how trustworthy a route's reported token usage is.
// A route this codebase has never measured is UsageUnknown, which every
// requirement check treats the same as "does not meet a minimum of
// UsageEstimated or better" -- see the "never fail open" note above.
type UsageQuality string

const (
	UsageUnknown   UsageQuality = ""
	UsageEstimated UsageQuality = "estimated"
	UsageExact     UsageQuality = "exact"
)

// meetsMinimum reports whether q is at least as good as min, using the
// ordering unknown < estimated < exact. UsageUnknown never meets any
// declared minimum, including UsageUnknown itself -- a requirement of
// UsageUnknown is not a real requirement (nothing declares that as a
// minimum), so this only matters for min in {estimated, exact}.
func (q UsageQuality) meetsMinimum(min UsageQuality) bool {
	if min == UsageUnknown {
		return true
	}
	rank := map[UsageQuality]int{UsageUnknown: 0, UsageEstimated: 1, UsageExact: 2}
	return rank[q] >= rank[min]
}

// ProviderCapabilities is what is actually known about one provider/model
// route. A zero value is not "supports nothing" in a way callers should
// construct by hand and pass off as a real declaration -- it is what
// CapabilitiesFor returns internally before an unknown route is reported as
// unknown via its second return value. Construct one only through
// RegisterCapabilities, or read one back through CapabilitiesFor.
type ProviderCapabilities struct {
	// Provider and Model identify the route these facts were measured or
	// declared against. Both participate in registry lookups (see
	// CapabilitiesFor); Model may be a family prefix (see
	// RegisterCapabilityFamily).
	Provider string
	Model    string

	// Supports records each capability this route was explicitly declared
	// to have or lack. A capability absent from this map is UNDECLARED, not
	// false -- Meets and Negotiate both treat an absent key as unmet, but
	// they are distinguishable in code (HasDeclared) for a caller that needs
	// to tell "declared false" from "never asked."
	Supports map[Capability]bool

	// SchemaKeywords lists the JSON Schema keywords this route's schema
	// enforcement mechanism (if CapNativeJSONSchema) actually honors. A
	// keyword this route silently drops is worse than one it rejects
	// outright, per CP-001's "keyword stripping is permitted only when the
	// remaining mechanism plus deterministic validation still delivers the
	// requested contract, and the plan discloses it" -- this field is the
	// disclosure. Nil means "not applicable or not measured," not "supports
	// every keyword."
	SchemaKeywords []string

	// UsageReportingQuality is how trustworthy this route's TokenUsage is.
	UsageReportingQuality UsageQuality

	// Regions lists where this route is permitted to execute, using
	// whatever region vocabulary the caller's DataPolicy also uses (this
	// package does not define one -- see internal/types.DataPolicy for
	// CP-002's). Empty means "no region declared," which CP-002's policy
	// check treats as ineligible for any policy that restricts regions, the
	// same "undeclared is not universal" rule every other field here
	// follows.
	Regions []string

	// RetentionDays is the longest retention this route is declared to
	// perform, in days. Zero means the route performs no retention at all
	// (the strictest, and the correct zero value for a route nobody has
	// measured otherwise would be wrong to assume). A route whose retention
	// behavior is genuinely unmeasured should not be registered rather than
	// registered with a guessed value here.
	RetentionDays int

	// ContextWindow and MaxOutputTokens are token ceilings. Zero means
	// unmeasured, and a Requirement with a minimum against a zero ceiling
	// never passes -- see Negotiate.
	ContextWindow   int
	MaxOutputTokens int
}

// HasDeclared reports whether cap has an explicit entry in c.Supports,
// distinguishing "declared false" from "never declared." Meets and Negotiate
// do not need this distinction (both unmet cases refuse), but a caller
// building a support matrix (CP-003) does: an underlying provider that was
// tested and found lacking is a different fact from one nobody ran the
// conformance suite against yet.
func (c ProviderCapabilities) HasDeclared(cap Capability) bool {
	_, ok := c.Supports[cap]
	return ok
}

// Has reports whether cap is declared and true. An undeclared capability
// reports false here, same as a capability explicitly declared false --
// Meets and Negotiate both use this, not HasDeclared, because neither cares
// why a requirement is unmet, only that it is.
func (c ProviderCapabilities) Has(cap Capability) bool {
	return c.Supports[cap]
}

// Requirement is what an operation or invocation needs from the route that
// serves it. A zero Requirement is satisfied by any known route -- it names
// nothing.
type Requirement struct {
	// Capabilities lists what must all be true on the candidate route.
	Capabilities []Capability

	// MinUsageQuality, if not UsageUnknown, is the minimum usage-reporting
	// trustworthiness required.
	MinUsageQuality UsageQuality

	// SchemaKeywords lists JSON Schema keywords the request's schema
	// actually uses. A route lacking CapNativeJSONSchema is checked only
	// against CapNativeJSONSchema itself (SchemaKeywords is meaningless
	// without it); a route that has it but whose SchemaKeywords list omits
	// one of these is unmet, per CP-001's disclosure requirement -- silent
	// keyword stripping is not an option this function offers.
	SchemaKeywords []string

	// MinContextWindow and MinMaxOutputTokens, if positive, are the token
	// ceilings the request needs. A route with an unmeasured (zero) ceiling
	// never meets a positive minimum -- see the "never fail open" note.
	MinContextWindow   int
	MinMaxOutputTokens int
}

// ErrCapabilityUnknown reports a route CapabilitiesFor has no declaration
// for. It is distinct from ErrCapabilityUnmet: an unknown route might
// support everything asked of it, but nothing here can say so, and "cannot
// say" refuses rather than assumes.
var ErrCapabilityUnknown = errors.New("route capabilities are not declared")

// ErrCapabilityUnmet reports a known route that was checked against a
// Requirement it does not satisfy.
var ErrCapabilityUnmet = errors.New("route does not meet the required capabilities")

// Meets reports whether caps satisfies req, without distinguishing why not.
// Negotiate is the version that explains the gap; this is the version a hot
// path checks when it only needs the boolean.
func Meets(caps ProviderCapabilities, req Requirement) bool {
	return len(missingCapabilities(caps, req)) == 0 &&
		caps.UsageReportingQuality.meetsMinimum(req.MinUsageQuality) &&
		meetsCeiling(caps.ContextWindow, req.MinContextWindow) &&
		meetsCeiling(caps.MaxOutputTokens, req.MinMaxOutputTokens) &&
		missingSchemaKeywords(caps, req) == nil
}

// Negotiate checks caps (as returned by CapabilitiesFor) against req and
// returns a nil error only when every declared requirement is met by a KNOWN
// route. known must be the second return value CapabilitiesFor produced for
// caps; Negotiate does not re-derive it, so a caller cannot accidentally
// negotiate against a zero-value ProviderCapabilities it built by hand and
// have it read as "known and supports nothing" versus "unknown" -- those are
// different refusals with different messages, and only the caller who did
// the lookup knows which one actually happened.
func Negotiate(caps ProviderCapabilities, known bool, req Requirement) error {
	if !known {
		return fmt.Errorf("%w: provider=%q model=%q", ErrCapabilityUnknown, caps.Provider, caps.Model)
	}

	var reasons []string

	if missing := missingCapabilities(caps, req); len(missing) > 0 {
		names := make([]string, len(missing))
		for i, c := range missing {
			names[i] = string(c)
		}
		sort.Strings(names)
		reasons = append(reasons, "missing capabilities: "+strings.Join(names, ", "))
	}

	if !caps.UsageReportingQuality.meetsMinimum(req.MinUsageQuality) {
		reasons = append(reasons, fmt.Sprintf("usage reporting quality %q below required %q",
			caps.UsageReportingQuality, req.MinUsageQuality))
	}

	if missing := missingSchemaKeywords(caps, req); len(missing) > 0 {
		sort.Strings(missing)
		reasons = append(reasons, "unsupported schema keywords: "+strings.Join(missing, ", "))
	}

	if !meetsCeiling(caps.ContextWindow, req.MinContextWindow) {
		reasons = append(reasons, fmt.Sprintf("context window %d below required %d", caps.ContextWindow, req.MinContextWindow))
	}
	if !meetsCeiling(caps.MaxOutputTokens, req.MinMaxOutputTokens) {
		reasons = append(reasons, fmt.Sprintf("max output tokens %d below required %d", caps.MaxOutputTokens, req.MinMaxOutputTokens))
	}

	if len(reasons) == 0 {
		return nil
	}
	return fmt.Errorf("%w: provider=%q model=%q: %s", ErrCapabilityUnmet, caps.Provider, caps.Model, strings.Join(reasons, "; "))
}

func missingCapabilities(caps ProviderCapabilities, req Requirement) []Capability {
	var missing []Capability
	for _, want := range req.Capabilities {
		if !caps.Has(want) {
			missing = append(missing, want)
		}
	}
	return missing
}

// missingSchemaKeywords reports which of req.SchemaKeywords caps does not
// declare support for. Meaningless (and skipped) when the request did not
// ask for CapNativeJSONSchema -- a route that only offers CapJSONMode is
// already excluded by missingCapabilities in that case, and its
// SchemaKeywords field describes a mechanism the request never invokes.
func missingSchemaKeywords(caps ProviderCapabilities, req Requirement) []string {
	if len(req.SchemaKeywords) == 0 {
		return nil
	}
	wantsNative := false
	for _, c := range req.Capabilities {
		if c == CapNativeJSONSchema {
			wantsNative = true
			break
		}
	}
	if !wantsNative {
		return nil
	}
	supported := make(map[string]bool, len(caps.SchemaKeywords))
	for _, k := range caps.SchemaKeywords {
		supported[k] = true
	}
	var missing []string
	for _, k := range req.SchemaKeywords {
		if !supported[k] {
			missing = append(missing, k)
		}
	}
	return missing
}

// meetsCeiling reports whether have satisfies a minimum of want. A want of
// zero or less names no requirement and is always satisfied. A have of zero
// with want positive is "unmeasured," which never satisfies a positive
// requirement -- the same never-fail-open rule as everywhere else in this
// file.
func meetsCeiling(have, want int) bool {
	if want <= 0 {
		return true
	}
	return have > 0 && have >= want
}

// capabilityRegistry holds what this package has been told about specific
// provider/model routes, plus family entries (see RegisterCapabilityFamily)
// matched by prefix when no exact model entry exists. It is not safe for
// concurrent registration and lookup by design -- see the doc comment on
// RegisterCapabilities.
var capabilityRegistry = struct {
	exact    map[string]ProviderCapabilities // "provider\x1fmodel"
	families []capabilityFamily
}{exact: make(map[string]ProviderCapabilities)}

type capabilityFamily struct {
	provider string
	prefix   string
	caps     ProviderCapabilities
}

func registryKey(provider, model string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + "\x1f" + strings.ToLower(strings.TrimSpace(model))
}

// RegisterCapabilities declares what is known about one exact provider/model
// route. It is meant to be called from package init (this package's own
// registrations, or a caller's, before any request-serving goroutine starts)
// -- there is no lock here on purpose, the same tradeoff internal/ops's
// operation registry makes, because a capability declaration that could race
// a lookup mid-request is a correctness bug no mutex placement fixes on its
// own; the fix is "declare capabilities before serving traffic," which this
// signature does not prevent a caller from violating but does not need to
// guard against either.
func RegisterCapabilities(caps ProviderCapabilities) {
	capabilityRegistry.exact[registryKey(caps.Provider, caps.Model)] = caps
}

// RegisterCapabilityFamily declares what is known about every model whose
// name starts with prefix, for routes with no exact entry. CP-001 lists
// "keyword stripping ... becomes declared behaviour rather than a
// transport-layer workaround" for the gpt-5.6 family specifically (CB-02);
// families are how one declaration covers luna/sol/terra/etc. without a
// registration call per model name, matching the same prefix convention
// provider.go's supportsTemperature already uses for that family.
func RegisterCapabilityFamily(provider, prefix string, caps ProviderCapabilities) {
	capabilityRegistry.families = append(capabilityRegistry.families, capabilityFamily{
		provider: strings.ToLower(strings.TrimSpace(provider)),
		prefix:   strings.ToLower(strings.TrimSpace(prefix)),
		caps:     caps,
	})
}

// CapabilitiesFor looks up what is known about provider/model: an exact
// match first, then the longest matching family prefix, in that order. The
// second return value is false when nothing matches either -- the caller
// must treat that as "unknown," not as the zero ProviderCapabilities, which
// is why Negotiate takes both return values rather than re-deriving `known`
// from whether the struct happens to be its zero value (a genuinely
// all-false declaration and "never registered" are both the zero value of
// every field except Provider/Model, and conflating them is exactly the
// silent-default failure CP-001 exists to close).
func CapabilitiesFor(provider, model string) (ProviderCapabilities, bool) {
	key := registryKey(provider, model)
	if caps, ok := capabilityRegistry.exact[key]; ok {
		return caps, true
	}

	normalizedProvider := strings.ToLower(strings.TrimSpace(provider))
	normalizedModel := strings.ToLower(strings.TrimSpace(model))

	var best *capabilityFamily
	for i := range capabilityRegistry.families {
		fam := &capabilityRegistry.families[i]
		if fam.provider != "" && fam.provider != normalizedProvider {
			continue
		}
		if !strings.HasPrefix(normalizedModel, fam.prefix) {
			continue
		}
		if best == nil || len(fam.prefix) > len(best.prefix) {
			best = fam
		}
	}
	if best == nil {
		return ProviderCapabilities{}, false
	}
	caps := best.caps
	caps.Provider = provider
	caps.Model = model
	return caps, true
}

// ResetCapabilityRegistryForTest clears every registration. Test-only (the
// name says so on purpose, matching the convention this package already uses
// -- see mockshape.go): production code registers capabilities once at
// startup and never needs to un-declare one.
func ResetCapabilityRegistryForTest() {
	capabilityRegistry.exact = make(map[string]ProviderCapabilities)
	capabilityRegistry.families = nil
}
