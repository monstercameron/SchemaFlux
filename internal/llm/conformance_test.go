package llm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// CP-003 offline coverage.
//
// Every test in this file runs with SCHEMAFLUX_LIVE_TESTS unset -- it must,
// because `go test ./...` runs it in ordinary CI, and AGENTS.md's "never
// spend money by accident" rule is unconditional. TestConformanceOfflineRunMakesNoNetworkCalls
// is not a belief about that, it is a proof: it installs a dial guard that
// fails any connection attempt outside 127.0.0.1/::1 for the duration of the
// whole protocol suite, so a check that somehow reached across the network
// would fail the test loudly rather than silently succeeding by accident.

// newGuardedTransport builds an *http.Transport that refuses to dial
// anywhere but loopback, so a test using it proves "no outbound network call
// happened" by construction rather than by inspecting logs after the fact.
func newGuardedTransport() (*http.Transport, *int64, *int64) {
	var dials, nonLocal int64
	base := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			atomic.AddInt64(&dials, 1)
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				host = addr
			}
			if host != "127.0.0.1" && host != "localhost" && host != "::1" {
				atomic.AddInt64(&nonLocal, 1)
				return nil, errors.New("conformance offline test: refused a dial to a non-loopback address: " + addr)
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}
	return base, &dials, &nonLocal
}

// TestConformanceOfflineRunMakesNoNetworkCalls installs the dial guard as
// http.DefaultTransport (what a zero-value http.Client -- and every
// *http.Client this package builds with only a Timeout set -- actually
// uses), runs the full protocol suite plus RunConformanceSuite with a nil
// live target, and asserts every dial the suite made resolved to loopback.
// It also asserts at least one dial happened, so this cannot pass by the
// suite quietly doing nothing.
func TestConformanceOfflineRunMakesNoNetworkCalls(t *testing.T) {
	guarded, dials, nonLocal := newGuardedTransport()
	previous := http.DefaultTransport
	http.DefaultTransport = guarded
	t.Cleanup(func() { http.DefaultTransport = previous })

	report := RunConformanceSuite(context.Background(), "openai", "gpt-5.6-mini", ProtocolConformanceChecks(), nil)

	if atomic.LoadInt64(dials) == 0 {
		t.Fatal("no dial was observed at all -- this proves nothing about network isolation")
	}
	if got := atomic.LoadInt64(nonLocal); got != 0 {
		t.Fatalf("the conformance suite attempted %d dial(s) to a non-loopback address", got)
	}
	if len(report.Results) == 0 {
		t.Fatal("the suite produced no results")
	}
}

// TestConformanceModelChecksSkipWithoutALiveTarget proves the other half of
// the zero-network guarantee: every CategoryModel check is recorded Skipped,
// never Run, when live is nil -- so ModelConformanceChecks compiling into
// the binary can never itself cause a network call outside
// conformance_live_test.go.
func TestConformanceModelChecksSkipWithoutALiveTarget(t *testing.T) {
	checks := ModelConformanceChecks()
	if len(checks) == 0 {
		t.Fatal("ModelConformanceChecks returned nothing to test")
	}

	report := RunConformanceSuite(context.Background(), "openai", "gpt-5.6-mini", checks, nil)
	for _, check := range checks {
		outcome, ok := report.Results[check.ID]
		if !ok {
			t.Errorf("check %q produced no result", check.ID)
			continue
		}
		if !outcome.Skipped {
			t.Errorf("check %q ran without a live target (Passed=%v Detail=%q)", check.ID, outcome.Passed, outcome.Detail)
		}
	}
}

// The protocol suite itself, run for real against local httptest servers.
// This is the part of CP-003 that needs no credential and no live gate: it
// measures this library's own OpenAI Responses API client code, not a
// vendor. Table-driven so each of CP-003's named dimensions is traceable to
// one row.
func TestProtocolConformanceChecksPassAgainstTheirOwnFixtures(t *testing.T) {
	checks := ProtocolConformanceChecks()
	if len(checks) < 10 {
		t.Fatalf("only %d protocol checks; CP-003 wants each dimension covered", len(checks))
	}

	for _, check := range checks {
		t.Run(check.ID, func(t *testing.T) {
			outcome := check.Run(context.Background(), nil)
			if outcome.Skipped {
				t.Fatalf("a protocol check must never skip (it takes no live target): %q", outcome.Detail)
			}
			if !outcome.Passed {
				t.Fatalf("check %q failed against its own hand-built fixture: %s", check.ID, outcome.Detail)
			}
			if outcome.Detail == "" {
				t.Errorf("check %q passed with no detail", check.ID)
			}
		})
	}
}

// A regression on any one of these must be visible without reading the
// production check bodies: each row states, by hand, what a specific wire
// response should classify as, using the same Classify/RetryAfterFrom
// surface a caller depends on. These are not copied from a run -- they are
// the documented contract in classify.go and ratelimit.go, restated as
// fixtures.
func TestCheckStatusClassificationHandCheckedCases(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   types.ErrorKind
	}{
		{"401 is authentication", http.StatusUnauthorized, types.KindAuthentication},
		{"403 is permission", http.StatusForbidden, types.KindPermission},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome := checkStatusClassification(context.Background(), tc.status, tc.want)
			if !outcome.Passed {
				t.Fatalf("outcome = %+v", outcome)
			}
		})
	}
}

// TestConformanceReportLabelHandCheckedMatrix pins Label()'s state machine
// against hand-constructed result sets -- not a run's output, a table
// derived directly from the doc comments on the ConformanceLabel constants.
func TestConformanceReportLabelHandCheckedMatrix(t *testing.T) {
	protoCheck := ConformanceCheck{ID: "p1", Category: CategoryProtocol}
	modelCheck := ConformanceCheck{ID: "m1", Category: CategoryModel, Capability: CapNativeJSONSchema}

	cases := []struct {
		name    string
		checks  []ConformanceCheck
		results map[string]ConformanceOutcome
		want    ConformanceLabel
	}{
		{
			name:    "no results at all is unrated",
			checks:  nil,
			results: map[string]ConformanceOutcome{},
			want:    ConformanceLabelUnrated,
		},
		{
			name:    "a failing protocol check caps the label at integrated",
			checks:  []ConformanceCheck{protoCheck},
			results: map[string]ConformanceOutcome{"p1": failedOutcome("boom")},
			want:    ConformanceLabelIntegrated,
		},
		{
			name:    "protocol passes, no model checks ran, is conformant",
			checks:  []ConformanceCheck{protoCheck},
			results: map[string]ConformanceOutcome{"p1": passedOutcome("ok")},
			want:    ConformanceLabelConformant,
		},
		{
			name:    "protocol passes, model check skipped, stays conformant",
			checks:  []ConformanceCheck{protoCheck, modelCheck},
			results: map[string]ConformanceOutcome{"p1": passedOutcome("ok"), "m1": skippedOutcome("no live target")},
			want:    ConformanceLabelConformant,
		},
		{
			name:    "protocol passes, model check failed, stays conformant not live-verified",
			checks:  []ConformanceCheck{protoCheck, modelCheck},
			results: map[string]ConformanceOutcome{"p1": passedOutcome("ok"), "m1": failedOutcome("boom")},
			want:    ConformanceLabelConformant,
		},
		{
			name:    "protocol and model both pass, no registry entry, is production-supported",
			checks:  []ConformanceCheck{protoCheck, modelCheck},
			results: map[string]ConformanceOutcome{"p1": passedOutcome("ok"), "m1": {Passed: true, Supported: true, Detail: "ok"}},
			want:    ConformanceLabelProductionSupported,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := &ConformanceReport{
				ProviderName: "conformance-test-vendor",
				Model:        "conformance-test-model",
				Results:      tc.results,
				checks:       tc.checks,
			}
			if got := report.Label(); got != tc.want {
				t.Errorf("Label() = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestConformanceReportLabelDropsToLiveVerifiedOnRegistryConflict is the
// case CP-003 names explicitly: "a provider failing a declared capability
// loses the label automatically." A route already declared to support
// native JSON Schema that the suite then measures as NOT supporting it must
// not read as production-supported.
func TestConformanceReportLabelDropsToLiveVerifiedOnRegistryConflict(t *testing.T) {
	defer ResetCapabilityRegistryForTest()
	ResetCapabilityRegistryForTest()

	RegisterCapabilities(ProviderCapabilities{
		Provider: "conformance-conflict-vendor",
		Model:    "conformance-conflict-model",
		Supports: map[Capability]bool{CapNativeJSONSchema: true},
	})

	protoCheck := ConformanceCheck{ID: "p1", Category: CategoryProtocol}
	modelCheck := ConformanceCheck{ID: "m1", Category: CategoryModel, Capability: CapNativeJSONSchema}

	report := &ConformanceReport{
		ProviderName: "conformance-conflict-vendor",
		Model:        "conformance-conflict-model",
		checks:       []ConformanceCheck{protoCheck, modelCheck},
		Results: map[string]ConformanceOutcome{
			"p1": passedOutcome("ok"),
			// The suite measured this route as NOT supporting native JSON
			// Schema, contradicting the registration above.
			"m1": {Passed: true, Supported: false, Detail: "route rejected the schema"},
		},
	}

	if got := report.Label(); got != ConformanceLabelLiveVerified {
		t.Fatalf("Label() = %s, want %s (a contradicted declaration must not read as production-supported)", got, ConformanceLabelLiveVerified)
	}
	conflicts := report.ConflictsWithRegistry()
	if len(conflicts) != 1 {
		t.Fatalf("ConflictsWithRegistry() = %v, want exactly one conflict", conflicts)
	}
	if !strings.Contains(conflicts[0], "native_json_schema") {
		t.Errorf("conflict message = %q, want it to name the capability", conflicts[0])
	}
}

// TestConformanceReportPublishWritesToTheRegistry is CP-003's central claim
// made concrete: a report's findings become registry data, not a line a
// human has to copy out of a test log.
func TestConformanceReportPublishWritesToTheRegistry(t *testing.T) {
	defer ResetCapabilityRegistryForTest()
	ResetCapabilityRegistryForTest()

	if _, known := CapabilitiesFor("conformance-publish-vendor", "conformance-publish-model"); known {
		t.Fatal("registry already knew this route before Publish; test fixture is not isolated")
	}

	modelCheck := ConformanceCheck{ID: "m1", Category: CategoryModel, Capability: CapNativeJSONSchema}
	report := &ConformanceReport{
		ProviderName: "conformance-publish-vendor",
		Model:        "conformance-publish-model",
		checks:       []ConformanceCheck{modelCheck},
		Results: map[string]ConformanceOutcome{
			"m1": {Passed: true, Supported: true, Detail: "measured"},
		},
	}

	if published := report.Publish(); !published {
		t.Fatal("Publish() reported nothing was written")
	}

	caps, known := CapabilitiesFor("conformance-publish-vendor", "conformance-publish-model")
	if !known {
		t.Fatal("CapabilitiesFor does not know the route after Publish")
	}
	if !caps.Has(CapNativeJSONSchema) {
		t.Error("the published registration does not carry the measured capability")
	}
}

// TestConformanceReportPublishOfPurelyProtocolReportWritesNothing guards the
// distinction Capabilities' doc comment draws: a report with no
// CategoryModel results has nothing true to say about a real route, and
// must not register an empty-but-present declaration that would read as
// "measured and supports nothing."
func TestConformanceReportPublishOfPurelyProtocolReportWritesNothing(t *testing.T) {
	defer ResetCapabilityRegistryForTest()
	ResetCapabilityRegistryForTest()

	protoCheck := ConformanceCheck{ID: "p1", Category: CategoryProtocol}
	report := &ConformanceReport{
		ProviderName: "conformance-protocol-only-vendor",
		Model:        "conformance-protocol-only-model",
		checks:       []ConformanceCheck{protoCheck},
		Results:      map[string]ConformanceOutcome{"p1": passedOutcome("ok")},
	}

	if published := report.Publish(); published {
		t.Fatal("Publish() wrote a registration from a purely protocol report")
	}
	if _, known := CapabilitiesFor("conformance-protocol-only-vendor", "conformance-protocol-only-model"); known {
		t.Fatal("the registry knows about a route no model check ever measured")
	}
}

// TestConformanceReportSummaryIsGeneratedNotEmpty proves Summary reflects
// r.Results rather than being a static string -- two reports with different
// results must render differently.
func TestConformanceReportSummaryIsGeneratedNotEmpty(t *testing.T) {
	check := ConformanceCheck{ID: "z1", Category: CategoryProtocol, Description: "d"}
	passing := &ConformanceReport{ProviderName: "p", Model: "m", checks: []ConformanceCheck{check},
		Results: map[string]ConformanceOutcome{"z1": passedOutcome("all good")}}
	failing := &ConformanceReport{ProviderName: "p", Model: "m", checks: []ConformanceCheck{check},
		Results: map[string]ConformanceOutcome{"z1": failedOutcome("broke")}}

	passingSummary := passing.Summary()
	failingSummary := failing.Summary()

	if passingSummary == failingSummary {
		t.Fatal("Summary() did not change between a passing and a failing report")
	}
	if !strings.Contains(passingSummary, "PASS") || !strings.Contains(passingSummary, "z1") {
		t.Errorf("passing summary = %q", passingSummary)
	}
	if !strings.Contains(failingSummary, "FAIL") || !strings.Contains(failingSummary, "broke") {
		t.Errorf("failing summary = %q", failingSummary)
	}
}

// TestRunConformanceSuiteEndToEndAgainstFakes runs the full public entry
// point exactly the way conformance_live_test.go will (protocol checks plus
// model checks), but with live == nil, proving the integration end to end
// without a credential.
func TestRunConformanceSuiteEndToEndAgainstFakes(t *testing.T) {
	all := append(ProtocolConformanceChecks(), ModelConformanceChecks()...)
	report := RunConformanceSuite(context.Background(), "openai", "gpt-5.6-mini", all, nil)

	if len(report.Results) != len(all) {
		t.Fatalf("got %d results for %d checks", len(report.Results), len(all))
	}

	label := report.Label()
	if label != ConformanceLabelConformant {
		t.Errorf("Label() = %s, want %s (protocol passed, every model check skipped)", label, ConformanceLabelConformant)
	}

	// A purely-offline run (every model check skipped) must publish nothing.
	if published := report.Publish(); published {
		t.Error("Publish() wrote a registration although every model check was skipped")
	}
}
