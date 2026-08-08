package tests

import (
	"errors"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

// This file covers the root package's negotiation wrappers: Negotiate,
// Settle, NegotiateAdversarial, and SettleAdversarial. Negotiate/Settle are
// documented as the same operation under two names (Negotiate forwards to
// Settle), and NegotiateAdversarial/SettleAdversarial likewise, so each pair
// is tested for identical behaviour on the same scripted response.

func TestSettleProducesTheScriptedSolution(t *testing.T) {
	testfixtures.WithScriptedProvider(t, `{"solution":"Tuesday 3pm","satisfaction":{"time":0.8},"overall_satisfaction":0.8,"confidence":0.9}`, nil)

	result, err := schemaflux.Settle[string]("find a meeting time", schemaflux.NegotiateOptions{
		Constraints: []string{"before 5pm"},
	})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if result.Solution != "Tuesday 3pm" {
		t.Errorf("Solution = %q, want the scripted solution", result.Solution)
	}
	if result.OverallSatisfaction != 0.8 {
		t.Errorf("OverallSatisfaction = %v, want 0.8", result.OverallSatisfaction)
	}
}

// Negotiate is documented as forwarding to Settle -- same input, same output.
func TestNegotiateForwardsToSettle(t *testing.T) {
	testfixtures.WithScriptedProvider(t, `{"solution":"Tuesday 3pm","satisfaction":{"time":0.8},"overall_satisfaction":0.8,"confidence":0.9}`, nil)

	result, err := schemaflux.Negotiate[string]("find a meeting time", schemaflux.NegotiateOptions{
		Constraints: []string{"before 5pm"},
	})
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}
	if result.Solution != "Tuesday 3pm" {
		t.Errorf("Solution = %q, want Negotiate to reach the same path as Settle", result.Solution)
	}
}

func TestSettleProviderFailureIsAnError(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("provider unavailable"))

	_, err := schemaflux.Settle[string]("find a meeting time")
	if err == nil {
		t.Fatal("expected an error when the provider is unreachable")
	}
}

type salaryTerms struct {
	BaseSalary int `json:"base_salary"`
}

func adversarialCtx() schemaflux.AdversarialContext[salaryTerms] {
	return schemaflux.AdversarialContext[salaryTerms]{
		Ours:        schemaflux.AdversarialPosition[salaryTerms]{Position: salaryTerms{BaseSalary: 160000}},
		Theirs:      schemaflux.AdversarialPosition[salaryTerms]{Position: salaryTerms{BaseSalary: 130000}},
		OurLeverage: "strong",
	}
}

func TestSettleAdversarialProducesTheScriptedDeal(t *testing.T) {
	testfixtures.WithScriptedProvider(t,
		`{"deal":{"base_salary":155000},"deal_reached":true,"term_movements":[{"term":"base_salary","movement":"they_concede"}],"who_conceded_more":"they","our_satisfaction":0.9,"their_satisfaction":0.6,"confidence":0.85}`, nil)

	result, err := schemaflux.SettleAdversarial[salaryTerms](adversarialCtx())
	if err != nil {
		t.Fatalf("SettleAdversarial: %v", err)
	}
	if !result.DealReached {
		t.Error("DealReached = false, want the scripted true")
	}
	if result.Deal.BaseSalary != 155000 {
		t.Errorf("Deal.BaseSalary = %d, want 155000", result.Deal.BaseSalary)
	}
	if result.WhoConcededMore != "they" {
		t.Errorf("WhoConcededMore = %q, want %q", result.WhoConcededMore, "they")
	}
}

// NegotiateAdversarial is documented as forwarding to SettleAdversarial.
func TestNegotiateAdversarialForwardsToSettleAdversarial(t *testing.T) {
	testfixtures.WithScriptedProvider(t,
		`{"deal":{"base_salary":155000},"deal_reached":true,"term_movements":[],"who_conceded_more":"they","our_satisfaction":0.9,"their_satisfaction":0.6,"confidence":0.85}`, nil)

	result, err := schemaflux.NegotiateAdversarial[salaryTerms](adversarialCtx())
	if err != nil {
		t.Fatalf("NegotiateAdversarial: %v", err)
	}
	if result.Deal.BaseSalary != 155000 {
		t.Errorf("Deal.BaseSalary = %d, want NegotiateAdversarial to reach the same path as SettleAdversarial", result.Deal.BaseSalary)
	}
}

func TestSettleAdversarialProviderFailureIsAnError(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("provider unavailable"))

	_, err := schemaflux.SettleAdversarial[salaryTerms](adversarialCtx())
	if err == nil {
		t.Fatal("expected an error when the provider is unreachable")
	}
}
