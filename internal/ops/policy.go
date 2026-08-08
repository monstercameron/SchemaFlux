package ops

import (
	"errors"
	"fmt"
	"strings"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// CP-001/CP-002 route selection. internal/llm/capabilities.go answers
// "what can this route do" and internal/types/policy.go answers "what is
// this route allowed to receive"; neither alone decides whether a route may
// actually be used. A route can meet every capability and still be
// disallowed by policy (a perfectly capable provider in the wrong region),
// or be policy-eligible and still incapable (an allowed provider that
// cannot deliver a required capability). This file is the one place that
// checks both together, so a planner (M14's negotiation step, out of scope
// here -- see internal/ops/plan.go) has a single function to call instead of
// two half-checks that could disagree about which routes are actually safe.

// RouteCandidate is one provider/model route being evaluated for a call.
type RouteCandidate struct {
	Provider string
	Model    string

	// Region is where this candidate is declared to execute. Empty means
	// undeclared; a policy that restricts regions refuses an undeclared
	// region, the same never-fail-open rule types.DataPolicy.AllowsRegion
	// already documents.
	Region string

	// RetentionDays is how long this candidate is declared to retain
	// content. types.RetentionUnspecified is not a valid value here -- a
	// candidate with unmeasured retention behavior should not be offered as
	// a route at all, the same reasoning llm.ProviderCapabilities'
	// RetentionDays doc comment gives for why a route should be left
	// unregistered rather than registered with a guess.
	RetentionDays types.RetentionDays
}

// ErrRouteRefused reports that SelectRoute found no eligible candidate.
var ErrRouteRefused = errors.New("no candidate route was eligible")

// errDataPolicyRefusal wraps every EligibleRoute refusal that originates
// from the DataPolicy check rather than the capability check, so a caller
// using errors.Is can tell CP-001 and CP-002 refusals apart without parsing
// the message.
var errDataPolicyRefusal = errors.New("route refused by data policy")

// ErrDataPolicyRefusal is errDataPolicyRefusal, exported for callers outside
// this package that need to distinguish a policy refusal from a capability
// refusal (llm.ErrCapabilityUnknown / llm.ErrCapabilityUnmet) with
// errors.Is.
var ErrDataPolicyRefusal = errDataPolicyRefusal

// EligibleRoute reports whether one candidate meets both req (CP-001) and
// policy (CP-002). A nil return means the route may be used. A non-nil
// return names why not and wraps llm.ErrCapabilityUnknown,
// llm.ErrCapabilityUnmet, or ErrDataPolicyRefusal so callers can distinguish
// the two families of refusal with errors.Is without parsing text.
func EligibleRoute(c RouteCandidate, req llm.Requirement, policy types.DataPolicy) error {
	caps, known := llm.CapabilitiesFor(c.Provider, c.Model)
	if err := llm.Negotiate(caps, known, req); err != nil {
		return err
	}

	if !policy.AllowsProvider(c.Provider) {
		return fmt.Errorf("%w: provider %q is not in the data policy's allowed list", errDataPolicyRefusal, c.Provider)
	}
	if !policy.AllowsRegion(c.Region) {
		return fmt.Errorf("%w: region %q is not in the data policy's allowed list", errDataPolicyRefusal, c.Region)
	}
	if !policy.AllowsRetention(c.RetentionDays) {
		return fmt.Errorf("%w: retention of %d days exceeds the data policy's ceiling", errDataPolicyRefusal, c.RetentionDays)
	}

	return nil
}

// SelectRoute evaluates candidates in the given order and returns the first
// that EligibleRoute accepts. It never returns a candidate that was not
// fully checked against both req and policy, and never falls back to an
// ineligible candidate merely because it was the last one tried -- an empty
// candidates list, or a list where nothing qualifies, is ErrRouteRefused,
// not a zero-value RouteCandidate silently returned as if it had been
// selected.
func SelectRoute(candidates []RouteCandidate, req llm.Requirement, policy types.DataPolicy) (RouteCandidate, error) {
	var reasons []string
	for _, c := range candidates {
		if err := EligibleRoute(c, req, policy); err != nil {
			reasons = append(reasons, fmt.Sprintf("%s/%s: %v", c.Provider, c.Model, err))
			continue
		}
		return c, nil
	}
	if len(reasons) == 0 {
		return RouteCandidate{}, fmt.Errorf("%w: no candidates were offered", ErrRouteRefused)
	}
	return RouteCandidate{}, fmt.Errorf("%w: %s", ErrRouteRefused, strings.Join(reasons, " | "))
}
