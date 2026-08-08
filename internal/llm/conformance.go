package llm

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// CP-003. CP-001's registry ships empty on purpose -- nothing had measured a
// real vendor, and a guessed declaration is exactly the confident-and-wrong
// data the negotiation in capabilities.go exists to refuse. This file is the
// suite that does the measuring, and the thing that turns a measurement into
// a ProviderCapabilities entry instead of a line in a test log that the next
// person has to transcribe by hand.
//
// # Two kinds of check, on purpose
//
// Some of what CP-003 asks for is a fact about THIS LIBRARY'S transport code
// -- does a 429 with an unparsable Retry-After get treated as "unknown", not
// "zero"; does a stream that stops without a terminal event surface as an
// error instead of a short success; is the API key ever visible in an error
// string. Those are provable against a local, hand-controlled HTTP server
// speaking the OpenAI Responses API wire shape, and proving them that way
// costs nothing and needs no credential -- see ProtocolConformanceChecks.
//
// The rest is a fact about a real vendor -- does gpt-5.6 actually honor the
// "additionalProperties" schema keyword; does this route's usage block
// report real numbers. Nothing local can answer that; it requires a real
// call to a real endpoint, which is the one part of this suite that spends
// money and is gated behind SCHEMAFLUX_LIVE_TESTS -- see ModelConformanceChecks
// and conformance_live_test.go, the only file in this package that runs one.

// ConformanceCategory distinguishes a check provable against a local double
// from one that requires a real model to answer.
type ConformanceCategory int

const (
	// CategoryProtocol checks are self-contained: they build their own
	// local HTTP server and never take a credential anywhere. Safe to run
	// in every `go test ./...`.
	CategoryProtocol ConformanceCategory = iota

	// CategoryModel checks measure what a real vendor route actually does
	// and require a *LiveTarget carrying real credentials. Skipped, not
	// failed, when none is supplied -- "we didn't check" and "we checked
	// and it failed" are different facts and this suite does not conflate
	// them, the same distinction HasDeclared draws in capabilities.go.
	CategoryModel
)

func (c ConformanceCategory) String() string {
	switch c {
	case CategoryProtocol:
		return "protocol"
	case CategoryModel:
		return "model"
	default:
		return "unknown"
	}
}

// LiveTarget names the real provider/model route a CategoryModel check may
// call, and carries the already-credentialed Provider to call it with. A
// CategoryProtocol check never receives one -- see ConformanceCheck.Run.
type LiveTarget struct {
	// ProviderName is the registry key CapabilitiesFor and RegisterCapabilities
	// use -- e.g. "openai", not a display name.
	ProviderName string

	// Model is the exact model string the request will name.
	Model string

	// Provider is the real, already-credentialed provider to call. Never
	// logged, never described beyond its Name() -- see AGENTS.md "never log
	// or embed the caller's payload," which applies here too: this suite's
	// own synthetic fixtures are not the caller's data, but the credential
	// riding along on Provider is nobody's business outside the HTTP
	// transport that already has it.
	Provider Provider
}

// ConformanceOutcome is one check's result. Detail describes the check's own
// synthetic fixture and the shape of what came back -- never the content of
// a real request or response, so a conformance run is safe to paste into an
// issue.
type ConformanceOutcome struct {
	// Passed is false for an infrastructure failure (the check's own local
	// server misbehaved, a context error escaped) and also false for a
	// live check that was skipped for lack of a LiveTarget -- Skipped tells
	// those two apart.
	Passed bool

	// Skipped means the check never ran: a CategoryModel check with no
	// LiveTarget. A skipped check is not a failed one and must not be
	// counted against a label the same way a failure is -- see
	// ConformanceReport.Label.
	Skipped bool

	// Supported is only meaningful when the check's Capability is non-empty:
	// whether the measured route actually has that capability. A check that
	// Passed can still observe Supported == false -- "the route correctly
	// reported it does not support X" is a passing measurement of an unmet
	// capability, not a failure of the check.
	Supported bool

	// Detail is a short, human-readable account of what was observed. Shapes
	// and counts, never payload content.
	Detail string
}

func skippedOutcome(reason string) ConformanceOutcome {
	return ConformanceOutcome{Skipped: true, Detail: reason}
}

func failedOutcome(detail string) ConformanceOutcome {
	return ConformanceOutcome{Passed: false, Detail: detail}
}

func passedOutcome(detail string) ConformanceOutcome {
	return ConformanceOutcome{Passed: true, Detail: detail}
}

// ConformanceCheck is one measurable fact from CP-003's list: auth
// classification, timeouts, cancellation, ambiguous completion, 429
// handling, 5xx behaviour, malformed/truncated/empty/refused output, schema
// keywords, usage extraction, streaming termination, credential redaction,
// and capability-declaration consistency.
type ConformanceCheck struct {
	// ID is stable and machine-readable -- it is the key a support matrix
	// and the capability registry both index by, so renaming one is a
	// breaking change to any generated matrix, the same way a Capability
	// constant renaming is.
	ID string

	// Description is one sentence, for a human reading a report.
	Description string

	Category ConformanceCategory

	// Capability is the Capability this check's result should be recorded
	// against, or "" if this check does not correspond to one (e.g. a pure
	// error-classification check has nothing to declare in
	// ProviderCapabilities.Supports).
	Capability Capability

	// Run performs the check. live is nil for a CategoryProtocol check
	// (which must not read it) and non-nil for a CategoryModel check that
	// was not skipped.
	Run func(ctx context.Context, live *LiveTarget) ConformanceOutcome
}

// ConformanceReport is the outcome of running a set of checks against one
// route, keyed by check ID so a caller (or a generated matrix) can look up
// any single result without re-running the suite.
type ConformanceReport struct {
	ProviderName string
	Model        string
	RanAt        time.Time

	// Results is keyed by ConformanceCheck.ID.
	Results map[string]ConformanceOutcome

	// checks is retained so Capabilities() can recover which Capability
	// each result maps to without threading a second parallel slice
	// through every caller of RunConformanceSuite.
	checks []ConformanceCheck
}

// RunConformanceSuite runs every check in checks and returns the aggregate
// report. live may be nil -- every CategoryModel check is then recorded
// Skipped rather than run, which is what makes it safe to call this with a
// live target of nil from a normal, non-live test: it proves the harness and
// the protocol checks without ever touching a network.
func RunConformanceSuite(ctx context.Context, providerName, model string, checks []ConformanceCheck, live *LiveTarget) *ConformanceReport {
	report := &ConformanceReport{
		ProviderName: providerName,
		Model:        model,
		RanAt:        time.Now().UTC(),
		Results:      make(map[string]ConformanceOutcome, len(checks)),
		checks:       append([]ConformanceCheck(nil), checks...),
	}

	for _, check := range checks {
		if check.Category == CategoryModel && live == nil {
			report.Results[check.ID] = skippedOutcome("requires SCHEMAFLUX_LIVE_TESTS=1 and a live target")
			continue
		}
		report.Results[check.ID] = check.Run(ctx, live)
	}

	return report
}

// ConformanceLabel is CP-003's four-way rating, ordered from least to most
// established. The zero value, ConformanceLabelUnrated, is distinct from
// ConformanceLabelIntegrated on purpose -- a route nobody has run the suite
// against yet must not read the same as one that ran it and passed nothing.
type ConformanceLabel int

const (
	// ConformanceLabelUnrated means no report exists for this route -- the
	// suite was never run, which is different from every check failing.
	ConformanceLabelUnrated ConformanceLabel = iota

	// ConformanceLabelIntegrated means a report exists: the suite ran
	// end-to-end against this route without an infrastructure failure. It
	// says nothing about whether any individual check passed -- an adapter
	// that compiles and answers is "integrated" even if every protocol
	// check then fails, because CP-003 asks documentation to keep this
	// label distinct from "conformant," not to skip straight past it.
	ConformanceLabelIntegrated

	// ConformanceLabelConformant means every CategoryProtocol check in the
	// report passed: this library's own error classification, output
	// handling, and streaming termination behave correctly against this
	// route's wire shape.
	ConformanceLabelConformant

	// ConformanceLabelLiveVerified means, in addition, every CategoryModel
	// check that ran (none were skipped) passed: the real route was called
	// and delivered what it was measured against.
	ConformanceLabelLiveVerified

	// ConformanceLabelProductionSupported means LiveVerified, and the
	// observed capabilities do not contradict whatever is already declared
	// in the capability registry for this exact route -- see
	// ConflictsWithRegistry. A provider that regresses on a capability it
	// used to have loses this label automatically the next time the suite
	// runs, which is the "generated, not hand-written" property CP-003 asks
	// the matrix to have.
	ConformanceLabelProductionSupported
)

func (l ConformanceLabel) String() string {
	switch l {
	case ConformanceLabelUnrated:
		return "unrated"
	case ConformanceLabelIntegrated:
		return "integrated"
	case ConformanceLabelConformant:
		return "conformant"
	case ConformanceLabelLiveVerified:
		return "live-verified"
	case ConformanceLabelProductionSupported:
		return "production-supported"
	default:
		return fmt.Sprintf("ConformanceLabel(%d)", int(l))
	}
}

// Label computes CP-003's rating from the report's results. It never reads
// anything but r itself -- a fresh report always recomputes rather than
// trusting a cached label, so a label can never survive being invalidated by
// a check that stopped passing.
func (r *ConformanceReport) Label() ConformanceLabel {
	if r == nil || len(r.Results) == 0 {
		return ConformanceLabelUnrated
	}

	protocolAllPassed := true
	modelRan := false
	modelAllPassed := true

	for _, check := range r.checks {
		outcome, ok := r.Results[check.ID]
		if !ok {
			continue
		}
		switch check.Category {
		case CategoryProtocol:
			if !outcome.Passed {
				protocolAllPassed = false
			}
		case CategoryModel:
			if outcome.Skipped {
				continue
			}
			modelRan = true
			if !outcome.Passed {
				modelAllPassed = false
			}
		}
	}

	if !protocolAllPassed {
		return ConformanceLabelIntegrated
	}
	if !modelRan || !modelAllPassed {
		return ConformanceLabelConformant
	}
	if len(r.ConflictsWithRegistry()) > 0 {
		return ConformanceLabelLiveVerified
	}
	return ConformanceLabelProductionSupported
}

// Capabilities builds a ProviderCapabilities from every CategoryModel check
// in the report that declared a Capability and did not skip. A
// CategoryProtocol check never contributes here: it measures this library's
// own transport code, not a fact about the route's model, and mixing the two
// would let a purely local test result populate a registry entry that
// production routing then trusts as a real vendor measurement -- exactly the
// "confident and wrong" data CP-001's empty-by-default registry exists to
// prevent.
func (r *ConformanceReport) Capabilities() ProviderCapabilities {
	caps := ProviderCapabilities{
		Provider: r.ProviderName,
		Model:    r.Model,
		Supports: make(map[Capability]bool),
	}
	for _, check := range r.checks {
		if check.Category != CategoryModel || check.Capability == "" {
			continue
		}
		outcome, ok := r.Results[check.ID]
		if !ok || outcome.Skipped {
			continue
		}
		caps.Supports[check.Capability] = outcome.Supported
	}
	return caps
}

// ConflictsWithRegistry compares r's observed CategoryModel capabilities
// against whatever CapabilitiesFor already returns for r's route, and
// reports every capability where the two disagree, sorted for a stable
// report. An unregistered route (CapabilitiesFor's second return false)
// conflicts with nothing -- there is nothing to disagree with yet, and that
// is exactly the case Publish exists to fill in.
func (r *ConformanceReport) ConflictsWithRegistry() []string {
	observed := r.Capabilities()
	if len(observed.Supports) == 0 {
		return nil
	}

	declared, known := CapabilitiesFor(r.ProviderName, r.Model)
	if !known {
		return nil
	}

	var conflicts []string
	for cap, observedValue := range observed.Supports {
		if !declared.HasDeclared(cap) {
			continue
		}
		if declared.Has(cap) != observedValue {
			conflicts = append(conflicts, fmt.Sprintf("%s: registry says %v, suite observed %v", cap, declared.Has(cap), observedValue))
		}
	}
	sort.Strings(conflicts)
	return conflicts
}

// Publish writes r's observed CategoryModel capabilities into the shared
// capability registry via RegisterCapabilities, which is the whole point of
// CP-003: a conformance run's findings become the data CP-001's negotiation
// consults, not a line in a test log the next person has to transcribe by
// hand. It does nothing (and returns false) for a report with no
// CategoryModel results at all -- a purely protocol run has nothing to
// declare about a real route, and registering an empty Supports map would
// read as "measured and supports nothing," which is a different and false
// claim from "never asked."
func (r *ConformanceReport) Publish() bool {
	caps := r.Capabilities()
	if len(caps.Supports) == 0 {
		return false
	}
	RegisterCapabilities(caps)
	return true
}

// Summary renders one line per check, sorted by ID for a stable report. It
// is the closest thing this package has to a "support matrix" in text form
// -- generated from r.Results, never hand-maintained, which is CP-003's
// verification requirement stated as a String method rather than a file
// nobody regenerates.
func (r *ConformanceReport) Summary() string {
	ids := make([]string, 0, len(r.Results))
	for id := range r.Results {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var b strings.Builder
	fmt.Fprintf(&b, "conformance: provider=%q model=%q label=%s\n", r.ProviderName, r.Model, r.Label())
	for _, id := range ids {
		outcome := r.Results[id]
		status := "FAIL"
		switch {
		case outcome.Skipped:
			status = "SKIP"
		case outcome.Passed:
			status = "PASS"
		}
		fmt.Fprintf(&b, "  [%s] %-40s %s\n", status, id, outcome.Detail)
	}
	return b.String()
}
