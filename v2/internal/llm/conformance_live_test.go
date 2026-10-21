package llm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// The only test in this package that spends money. CP-003's conformance
// suite measures real vendor routes, which AGENTS.md's "never spend money by
// accident" rule requires be gated: a plain `go test ./...` must never reach
// here, so requireLiveConformance is the single gate every test in this
// file calls first, and it is the only place SCHEMAFLUX_LIVE_TESTS is read
// in this package.
//
//	SCHEMAFLUX_LIVE_TESTS=1 SCHEMAFLUX_MODEL=gpt-5.6-mini go test ./internal/llm/... -run TestLiveConformance -v
//
// This file cannot import the root schemaflux package to reuse its
// InitWithEnv/.env loading (root already imports internal/llm, so the
// reverse import would cycle), so credential resolution here is
// deliberately minimal: OPENAI_API_KEY or SCHEMAFLUX_OPENAI_API_KEY or
// SCHEMAFLUX_API_KEY, read directly. A caller who wants .env loading runs
// the root package's own live tests, or exports the variable before
// invoking this one.
func requireLiveConformance(t *testing.T) (apiKey, model string) {
	t.Helper()

	if os.Getenv("SCHEMAFLUX_LIVE_TESTS") != "1" {
		t.Skip("set SCHEMAFLUX_LIVE_TESTS=1 to run the conformance suite against a real provider; it spends money")
	}

	for _, envVar := range []string{"SCHEMAFLUX_OPENAI_API_KEY", "OPENAI_API_KEY", "SCHEMAFLUX_API_KEY"} {
		if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
			apiKey = v
			break
		}
	}
	if apiKey == "" {
		t.Fatal("SCHEMAFLUX_LIVE_TESTS=1 but no API key was found in SCHEMAFLUX_OPENAI_API_KEY, OPENAI_API_KEY, or SCHEMAFLUX_API_KEY")
	}

	model = strings.TrimSpace(os.Getenv("SCHEMAFLUX_MODEL"))
	if model == "" {
		model = "gpt-5.6-mini"
	}
	return apiKey, model
}

// TestLiveConformanceOpenAI runs both halves of the CP-003 suite -- the
// local protocol checks (which spend nothing but confirm the harness works
// against this same OpenAIProvider before trusting it against real traffic)
// and the model checks (which do spend money) -- against the real OpenAI
// API, and publishes the measured capabilities into the registry via
// Publish(). This is the one call in the whole repository that turns a
// conformance run into registry data instead of a line in a log: run it,
// and CapabilitiesFor("openai", model) answers from what this run actually
// observed rather than from nothing (CP-001 ships the registry empty) or
// from a guess.
func TestLiveConformanceOpenAI(t *testing.T) {
	apiKey, model := requireLiveConformance(t)

	provider, err := NewOpenAIProvider(ProviderConfig{
		APIKey:  apiKey,
		Timeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}

	live := &LiveTarget{ProviderName: "openai", Model: model, Provider: provider}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	checks := append(ProtocolConformanceChecks(), ModelConformanceChecks()...)
	report := RunConformanceSuite(ctx, "openai", model, checks, live)

	t.Log(report.Summary())

	for _, check := range checks {
		outcome := report.Results[check.ID]
		if outcome.Skipped {
			t.Errorf("check %q was skipped despite a live target being supplied", check.ID)
			continue
		}
		if !outcome.Passed {
			t.Errorf("check %q: %s", check.ID, outcome.Detail)
		}
	}

	if conflicts := report.ConflictsWithRegistry(); len(conflicts) > 0 {
		t.Logf("capability conflicts with the existing registry entry: %v", conflicts)
	}

	if published := report.Publish(); !published {
		t.Error("Publish() wrote nothing -- no CategoryModel check produced a result to record")
	}

	t.Logf("label: %s", report.Label())
}
