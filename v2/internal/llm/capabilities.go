package llm

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/monstercameron/schemaflux/v2/internal/types"
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
// UsageEstimated or better" -- see the "never fail open" note above. A
// requirement may name only the three qualities below; any other string is
// refused by Negotiate and met by no route.
type UsageQuality string

const (
	// UsageUnknown is a route whose usage reporting has not been measured. As a
	// requirement it is no requirement.
	UsageUnknown UsageQuality = ""

	// UsageEstimated is a route that reports token counts it estimated.
	UsageEstimated UsageQuality = "estimated"

	// UsageExact is a route that reports the token counts it measured.
	UsageExact UsageQuality = "exact"
)

// known reports whether q is one of the three qualities there are. UsageQuality
// is an open string type, so a value written as a literal can be any string, and
// one that is not among these was mistyped.
func (q UsageQuality) known() bool {
	switch q {
	case UsageUnknown, UsageEstimated, UsageExact:
		return true
	}
	return false
}

// meetsMinimum reports whether q is at least as good as minimum, using the
// ordering unknown < estimated < exact. A minimum of UsageUnknown is no requirement,
// since nothing is worse than unknown, so it is met by every route. A minimum that
// is not one of the three qualities is not met by any: it used to rank as zero
// and so was met by every route, which turned a mistyped requirement into no
// requirement. Negotiate says that is what happened.
func (q UsageQuality) meetsMinimum(minimum UsageQuality) bool {
	if !minimum.known() {
		return false
	}
	if minimum == UsageUnknown {
		return true
	}
	rank := map[UsageQuality]int{UsageUnknown: 0, UsageEstimated: 1, UsageExact: 2}
	return rank[q] >= rank[minimum]
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

// HasDeclared reports whether capability has an explicit entry in c.Supports,
// distinguishing "declared false" from "never declared." Meets and Negotiate
// do not need this distinction (both unmet cases refuse), but a caller
// building a support matrix (CP-003) does: an underlying provider that was
// tested and found lacking is a different fact from one nobody ran the
// conformance suite against yet.
func (c ProviderCapabilities) HasDeclared(capability Capability) bool {
	_, ok := c.Supports[capability]
	return ok
}

// Has reports whether capability is declared and true. An undeclared capability
// reports false here, same as a capability explicitly declared false --
// Meets and Negotiate both use this, not HasDeclared, because neither cares
// why a requirement is unmet, only that it is.
func (c ProviderCapabilities) Has(capability Capability) bool {
	return c.Supports[capability]
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
	// A requirement that names a quality there is not cannot be checked, and is a
	// mistake in the requirement and not a route that falls short of it.
	if !req.MinUsageQuality.known() {
		return types.Newf(types.KindConfiguration, "llm.negotiate",
			"the requirement's minimum usage quality %q is not one of %q, %q or %q",
			string(req.MinUsageQuality), string(UsageUnknown), string(UsageEstimated), string(UsageExact))
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
// matched by prefix when no exact model entry exists.
//
// It is guarded by a lock. It was left without one on the reasoning that
// routes are declared at start-up and looked up afterwards. A client's data
// policy now looks a route up on every call, and the error it returns for an
// undeclared route says to declare it, which a caller will do while calls are
// running: an unguarded map read during a write is a fatal error in Go, not a
// stale answer.
var capabilityRegistry = struct {
	sync.RWMutex
	exact    map[string]routeEntry // "provider\x1fmodel"
	families []capabilityFamily
}{exact: make(map[string]routeEntry)}

type capabilityFamily struct {
	provider string
	prefix   string
	caps     ProviderCapabilities
}

// routeEntry is what the registry holds for one exact route.
//
// An entry is one of two kinds. A whole entry (RegisterCapabilities) is the
// route's complete declaration: no family is consulted for it. Any other entry
// (DeclareRoute, a published conformance run) is an overlay: the fields it sets
// win, and every other field is resolved from the route's family when the route
// is looked up. It used to be a copy of the family's entry made when the route
// was declared, which froze the family at that moment: a longer family
// registered afterwards never reached the route, and a family re-registered to
// tighten its regions did not either.
//
// regionsDeclared and retentionDeclared say whether caps.Regions and
// caps.RetentionDays were set, because their zero values are meaningful ("no
// region", "keeps nothing") and are not "unset".
type routeEntry struct {
	caps              ProviderCapabilities
	whole             bool
	regionsDeclared   bool
	retentionDeclared bool
}

func registryKey(provider, model string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + "\x1f" + strings.ToLower(strings.TrimSpace(model))
}

// RegisterCapabilities declares what is known about one exact provider/model
// route. It replaces whatever was registered for the route before.
//
// The registry is guarded by a lock, so this is safe to call while requests
// are being served. What is stored is a copy: the caller's slices and map are
// not kept, so changing them afterwards does not change the declaration. They
// used to be kept as given.
func RegisterCapabilities(caps ProviderCapabilities) {
	capabilityRegistry.Lock()
	defer capabilityRegistry.Unlock()
	capabilityRegistry.exact[registryKey(caps.Provider, caps.Model)] = routeEntry{caps: copyCapabilities(caps), whole: true}
}

// copyCapabilities returns caps with its slices and map copied.
func copyCapabilities(caps ProviderCapabilities) ProviderCapabilities {
	if caps.Supports != nil {
		supports := make(map[Capability]bool, len(caps.Supports))
		for capability, supported := range caps.Supports {
			supports[capability] = supported
		}
		caps.Supports = supports
	}
	// make and copy, so that a list that is empty stays one: append onto nil
	// gives nil back for it, and "declared, and nowhere" became "not declared".
	if caps.SchemaKeywords != nil {
		keywords := make([]string, len(caps.SchemaKeywords))
		copy(keywords, caps.SchemaKeywords)
		caps.SchemaKeywords = keywords
	}
	if caps.Regions != nil {
		regions := make([]string, len(caps.Regions))
		copy(regions, caps.Regions)
		caps.Regions = regions
	}
	return caps
}

// publishObserved stores what a conformance run observed about a route, and
// keeps what the route has declared about where it runs and what it keeps,
// which a run knows nothing of. The read and the write are under one lock, so
// a declaration made in between is not overwritten with the older one.
//
// What is stored is an overlay (see routeEntry): the fields the run observed,
// and the placement the route had declared. A field the run did not observe, a
// context window say, is resolved from the route's family when the route is
// looked up. It was copied from the family into the route's own entry at this
// point, regions and retention only, so Negotiate on a route its family covered
// failed on every other field after a Publish, and a family changed later did
// not reach the route.
func publishObserved(caps ProviderCapabilities) {
	capabilityRegistry.Lock()
	defer capabilityRegistry.Unlock()
	key := registryKey(caps.Provider, caps.Model)
	// A route that was declared whole stays whole: what the run observed is merged
	// into its declaration, capability by capability, and everything else it
	// declared, the context window, the usage reporting, the keywords, the regions
	// and the retention, is kept. Storing the observation as an overlay would have
	// resolved all of those from a family, and for a route with none they became
	// zero, so a route declared as meeting a requirement was refused for it after a
	// run.
	if previous, had := capabilityRegistry.exact[key]; had && previous.whole {
		merged := copyCapabilities(previous.caps)
		if merged.Supports == nil {
			merged.Supports = make(map[Capability]bool, len(caps.Supports))
		}
		for capability, supported := range caps.Supports {
			merged.Supports[capability] = supported
		}
		if betweenReadAndWriteForTest != nil {
			betweenReadAndWriteForTest()
		}
		capabilityRegistry.exact[key] = routeEntry{caps: merged, whole: true}
		return
	}
	entry := routeEntry{caps: copyCapabilities(caps)}
	// Where the run observed regions or a retention, they are the route's. Where
	// it did not, the route's own earlier declaration stands, and failing that
	// the family's applies at lookup.
	entry.regionsDeclared = len(caps.Regions) > 0
	entry.retentionDeclared = caps.RetentionDays != 0
	if previous, had := capabilityRegistry.exact[key]; had {
		if !entry.regionsDeclared && previous.regionsDeclared {
			entry.caps.Regions = copyCapabilities(previous.caps).Regions
			entry.regionsDeclared = true
		}
		if !entry.retentionDeclared && previous.retentionDeclared {
			entry.caps.RetentionDays = previous.caps.RetentionDays
			entry.retentionDeclared = true
		}
	}
	if betweenReadAndWriteForTest != nil {
		betweenReadAndWriteForTest()
	}
	capabilityRegistry.exact[key] = entry
}

// betweenReadAndWriteForTest is nil outside this package's tests. A test sets
// it to stand between publishObserved's read of a route and its write, and
// show that nothing else can get to the registry there.
var betweenReadAndWriteForTest func()

// DeclareRoute declares where one provider/model route runs and how long it
// keeps what it is sent, which is what a data policy asks of a route.
//
// The declaration is an overlay on whatever else is known about the route. The
// regions and the retention are the route's own and win over its family's.
// Every other field is resolved when the route is looked up: from an earlier
// registration of the route, or else from its family, so a family registered
// or changed after this call still reaches the route. The family's entry was
// copied into the route here, and kept.
//
// regions is every region the route may run in. A route that may be served
// from two regions declares both, and is allowed only by a policy that allows
// both. An empty list stays empty, and a nil one stays nil: "declared, and
// nowhere" is not "not declared". retentionDays is the longest the route keeps
// content, and zero says it keeps none; a route whose retention is not known
// should not be declared.
//
// Like RegisterCapabilities, it is for start-up, before requests are served.
func DeclareRoute(provider, model string, regions []string, retentionDays int) {
	// Read and written under one lock, so two declarations of the same route
	// cannot each start from what the other is about to replace.
	capabilityRegistry.Lock()
	defer capabilityRegistry.Unlock()
	key := registryKey(provider, model)
	entry, had := capabilityRegistry.exact[key]
	if !had {
		entry.caps.Provider = provider
		entry.caps.Model = model
	}
	// make and copy rather than append onto nil, which gives nil back for an
	// empty list.
	entry.caps.Regions = nil
	if regions != nil {
		entry.caps.Regions = make([]string, len(regions))
		copy(entry.caps.Regions, regions)
	}
	entry.caps.RetentionDays = retentionDays
	entry.regionsDeclared = true
	entry.retentionDeclared = true
	capabilityRegistry.exact[key] = entry
}

// RegisterCapabilityFamily declares what is known about every model whose
// name starts with prefix, for routes with no exact entry. CP-001 lists
// "keyword stripping ... becomes declared behaviour rather than a
// transport-layer workaround" for the gpt-5.6 family specifically (CB-02);
// families are how one declaration covers luna/sol/terra/etc. without a
// registration call per model name, matching the longest-prefix convention
// the request-shape table in models.go uses.
//
// Registering a family that is already registered, the same provider and the
// same prefix, replaces it, as RegisterCapabilities does for a route. The
// first declaration stood and the later one was ignored without a word, so a
// family re-registered to tighten its regions or its retention left the
// looser one in force.
func RegisterCapabilityFamily(provider, prefix string, caps ProviderCapabilities) {
	capabilityRegistry.Lock()
	defer capabilityRegistry.Unlock()
	// A copy, as RegisterCapabilities keeps: the caller's map and slices were
	// stored as given, so changing them afterwards changed the declaration.
	family := capabilityFamily{
		provider: strings.ToLower(strings.TrimSpace(provider)),
		prefix:   strings.ToLower(strings.TrimSpace(prefix)),
		caps:     copyCapabilities(caps),
	}
	for i := range capabilityRegistry.families {
		held := &capabilityRegistry.families[i]
		if held.provider == family.provider && held.prefix == family.prefix {
			*held = family
			return
		}
	}
	capabilityRegistry.families = append(capabilityRegistry.families, family)
}

// CapabilitiesFor looks up what is known about provider/model: an exact
// match first, then the longest matching family prefix, in that order. An exact
// entry made by RegisterCapabilities stands alone. One made by DeclareRoute or
// by publishing a conformance run holds only the fields it set, and the rest
// come from the family at the time of the lookup. The
// second return value is false when nothing matches either -- the caller
// must treat that as "unknown," not as the zero ProviderCapabilities, which
// is why Negotiate takes both return values rather than re-deriving `known`
// from whether the struct happens to be its zero value (a genuinely
// all-false declaration and "never registered" are both the zero value of
// every field except Provider/Model, and conflating them is exactly the
// silent-default failure CP-001 exists to close).
func CapabilitiesFor(provider, model string) (ProviderCapabilities, bool) {
	capabilityRegistry.RLock()
	defer capabilityRegistry.RUnlock()
	return capabilitiesForLocked(provider, model)
}

// capabilitiesForLocked is CapabilitiesFor for a caller that holds the lock.
//
// What it returns is a copy. It returned the registry's own map and slices, so
// a caller who changed what it had looked up changed the declaration, and a
// route declared from its family's entry shared the family's lists.
func capabilitiesForLocked(provider, model string) (ProviderCapabilities, bool) {
	entry, hasEntry, family := routeSourcesLocked(provider, model)
	if hasEntry && entry.whole {
		return copyCapabilities(entry.caps), true
	}
	if !hasEntry {
		if family == nil {
			return ProviderCapabilities{}, false
		}
		caps := copyCapabilities(family.caps)
		caps.Provider = provider
		caps.Model = model
		return caps, true
	}

	// An overlay: the route's own fields over its family's.
	var caps ProviderCapabilities
	if family != nil {
		caps = copyCapabilities(family.caps)
	}
	caps.Provider = entry.caps.Provider
	caps.Model = entry.caps.Model
	own := copyCapabilities(entry.caps)
	if own.Supports != nil {
		// Per capability, so a run that observed some of them does not take the
		// family's others away, and one it observed false is false.
		if caps.Supports == nil {
			caps.Supports = make(map[Capability]bool, len(own.Supports))
		}
		for capability, supported := range own.Supports {
			caps.Supports[capability] = supported
		}
	}
	if own.SchemaKeywords != nil {
		caps.SchemaKeywords = own.SchemaKeywords
	}
	if own.UsageReportingQuality != UsageUnknown {
		caps.UsageReportingQuality = own.UsageReportingQuality
	}
	if own.ContextWindow > 0 {
		caps.ContextWindow = own.ContextWindow
	}
	if own.MaxOutputTokens > 0 {
		caps.MaxOutputTokens = own.MaxOutputTokens
	}
	regions, retention := placementOf(entry, hasEntry, family)
	caps.Regions = nil
	if regions != nil {
		caps.Regions = make([]string, len(regions))
		copy(caps.Regions, regions)
	}
	caps.RetentionDays = retention
	return caps, true
}

// RoutePlacementFor returns where a route runs and how long it keeps content:
// the two fields a data policy reads. It resolves the route as CapabilitiesFor
// does, and it is for a caller that wants those two and nothing else:
// CapabilitiesFor copies the route's capability map and both of its lists on
// every call, and the policy check made it up to three times per request.
//
// regions is the registry's own list, not a copy, and must not be changed.
func RoutePlacementFor(provider, model string) (regions []string, retentionDays int, known bool) {
	capabilityRegistry.RLock()
	defer capabilityRegistry.RUnlock()
	entry, hasEntry, family := routeSourcesLocked(provider, model)
	if !hasEntry && family == nil {
		return nil, 0, false
	}
	regions, retentionDays = placementOf(entry, hasEntry, family)
	return regions, retentionDays, true
}

// routeSourcesLocked finds what the registry holds for a route: its own entry,
// if it has one, and the family that covers it, if one does. The caller holds
// the lock.
func routeSourcesLocked(provider, model string) (entry routeEntry, hasEntry bool, family *capabilityFamily) {
	entry, hasEntry = capabilityRegistry.exact[registryKey(provider, model)]

	normalizedProvider := strings.ToLower(strings.TrimSpace(provider))
	normalizedModel := strings.ToLower(strings.TrimSpace(model))
	for i := range capabilityRegistry.families {
		fam := &capabilityRegistry.families[i]
		if fam.provider != "" && fam.provider != normalizedProvider {
			continue
		}
		if !strings.HasPrefix(normalizedModel, fam.prefix) {
			continue
		}
		// The longest prefix wins. Two families of one length that both match
		// differ in their provider, and the one that names the provider is the
		// more specific. Two of the same provider and prefix cannot both be held:
		// registering one replaces the other.
		if family == nil || len(fam.prefix) > len(family.prefix) ||
			len(fam.prefix) == len(family.prefix) && family.provider == "" && fam.provider != "" {
			family = fam
		}
	}
	return entry, hasEntry, family
}

// placementOf resolves the regions and the retention of a route from its entry
// and its family, without copying. A whole entry stands alone; any other entry's
// own placement wins where it declared one, field by field.
func placementOf(entry routeEntry, hasEntry bool, family *capabilityFamily) (regions []string, retentionDays int) {
	if hasEntry && entry.whole {
		return entry.caps.Regions, entry.caps.RetentionDays
	}
	if family != nil {
		regions, retentionDays = family.caps.Regions, family.caps.RetentionDays
	}
	if hasEntry {
		if entry.regionsDeclared {
			regions = entry.caps.Regions
		}
		if entry.retentionDeclared {
			retentionDays = entry.caps.RetentionDays
		}
	}
	return regions, retentionDays
}

// ResetCapabilityRegistryForTest clears every registration. Test-only (the
// name says so on purpose, matching the convention this package already uses
// -- see mockshape.go): production code registers capabilities once at
// startup and never needs to un-declare one.
func ResetCapabilityRegistryForTest() {
	capabilityRegistry.Lock()
	defer capabilityRegistry.Unlock()
	capabilityRegistry.exact = make(map[string]routeEntry)
	capabilityRegistry.families = nil
}
