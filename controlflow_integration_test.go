package schemaflux_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
)

type department struct {
	Name string
	Desk string
}

func departments() []schemaflux.Decision[department] {
	return []schemaflux.Decision[department]{
		{Value: department{"Technical Support", "tech"}, Description: "Bugs, outages, and product defects"},
		{Value: department{"Billing Support", "billing"}, Description: "Invoices, refunds, and payment problems"},
		{Value: department{"Customer Success", "success"}, Description: "Onboarding, training, and account reviews"},
	}
}

// A router built on Decide must not send everything to the first department
// when the provider is unreachable or answers badly.
func TestIntegrationDecideDoesNotDefaultToTheFirstBranch(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		err   error
		fails bool
	}{
		{"provider_error", "", errors.New("provider unavailable"), true},
		{"prose", "Billing, I think.", nil, false},
		{"refusal", "I'm sorry, I can't help with that.", nil, false},
		{"empty", "", nil, false},
		{"whitespace", "   \n ", nil, false},
		{"error_page", "<html><body>502 Bad Gateway</body></html>", nil, false},
		{"truncated_json", `{"selected": 1`, nil, false},
		{"array_not_object", `[{"selected":1}]`, nil, false},
		{"index_too_high", `{"selected":9}`, nil, false},
		{"index_negative", `{"selected":-2}`, nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withScriptedProvider(t, tc.body, tc.err)

			chosen, result, err := schemaflux.Decide(context.Background(), "an invoice question", departments())
			if err == nil {
				t.Fatalf("expected an error, got %q at index %d", chosen.Name, result.SelectedIndex)
			}
			if chosen.Name != "" {
				t.Errorf("a failed decision must not return a department, got %q", chosen.Name)
			}
			if result.SelectedIndex != -1 {
				t.Errorf("SelectedIndex = %d, want -1", result.SelectedIndex)
			}
		})
	}
}

// With a fallback configured the router keeps working, and says so.
func TestIntegrationDecideFallbackIsLabelled(t *testing.T) {
	withScriptedProvider(t, "", errors.New("provider unavailable"))

	chosen, result, err := schemaflux.Decide(context.Background(), "an invoice question", departments(),
		schemaflux.NewDecideOptions().WithFallback(0))
	if err != nil {
		t.Fatalf("a configured fallback must not error: %v", err)
	}
	if chosen.Desk != "tech" || !result.Fallback {
		t.Errorf("chosen=%q Fallback=%v, want the configured branch marked as a fallback", chosen.Desk, result.Fallback)
	}
}

// A well-formed answer routes correctly.
func TestIntegrationDecideRoutes(t *testing.T) {
	withScriptedProvider(t, `{"selected":1,"explanation":"the customer is disputing an invoice","confidence":0.9}`, nil)

	chosen, result, err := schemaflux.Decide(context.Background(), "invoice dispute", departments())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if chosen.Desk != "billing" || result.SelectedIndex != 1 || result.Fallback {
		t.Errorf("chosen=%q index=%d fallback=%v", chosen.Desk, result.SelectedIndex, result.Fallback)
	}
}

// Match must report an outage instead of routing every input to the default.
func TestIntegrationMatchDoesNotDefaultOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		err  error
	}{
		{"provider_error", "", errors.New("provider unavailable")},
		{"prose", "It could go either way.", nil},
		{"empty", "", nil},
		{"error_page", "<html>502</html>", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withScriptedProvider(t, tc.body, tc.err)

			defaulted := false
			matched, err := schemaflux.Match(context.Background(), "a ticket",
				schemaflux.Like("a billing question", func() { t.Error("the case must not run") }),
				schemaflux.Otherwise(func() { defaulted = true }),
			)
			if err == nil {
				t.Fatalf("an unusable answer must be reported, got matched=%v", matched)
			}
			if defaulted {
				t.Error("an outage must not be read as 'nothing matched'")
			}
		})
	}
}

// A leading Otherwise must not shadow the cases written after it.
func TestIntegrationOtherwiseDoesNotShadowLaterCases(t *testing.T) {
	withScriptedProvider(t, "true", nil)

	ran := ""
	matched, err := schemaflux.Match(context.Background(), "a ticket",
		schemaflux.Otherwise(func() { ran = "otherwise" }),
		schemaflux.Like("a billing question", func() { ran = "billing" }),
	)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !matched || ran != "billing" {
		t.Errorf("ran = %q, want the matching case to win", ran)
	}
}

// Example_decideRouting shows a support-ticket router. Without a fallback, an
// unreachable provider is an error rather than a silent route to the first
// department. It runs under go test with a scripted provider: no credential,
// no spend.
func Example_decideRouting() {
	schemaflux.NewClient("example-key").WithProviderInstance(&scriptedProvider{
		body: `{"selected":1,"explanation":"the customer is disputing an invoice","confidence":0.91}`,
	})

	chosen, decision, err := schemaflux.Decide(
		context.Background(),
		"Customer says they were charged twice for the March invoice.",
		departments())
	if err != nil {
		fmt.Println("could not route:", err)
		return
	}

	fmt.Println("route to:", chosen.Name)
	fmt.Printf("confidence: %.2f\n", decision.Confidence)
	fmt.Println("fallback:", decision.Fallback)

	// Output:
	// route to: Billing Support
	// confidence: 0.91
	// fallback: false
}

// Example_decideFailsClosed is the counterpart: with the provider down and no
// fallback configured, the router reports the outage instead of routing every
// ticket to the first department.
func Example_decideFailsClosed() {
	schemaflux.NewClient("example-key").WithProviderInstance(&scriptedProvider{
		err: errors.New("provider unavailable"),
	})

	chosen, decision, err := schemaflux.Decide(context.Background(), "an invoice question", departments())

	fmt.Println("error is nil:", err == nil)
	fmt.Println("department:", chosen.Name == "")
	fmt.Println("index:", decision.SelectedIndex)

	// Output:
	// error is nil: false
	// department: true
	// index: -1
}

// Example_matchRouting shows the case form, and that the default branch is
// evaluated last no matter where it is written.
func Example_matchRouting() {
	schemaflux.NewClient("example-key").WithProviderInstance(&scriptedProvider{body: "true"})

	var routed strings.Builder
	matched, err := schemaflux.Match(context.Background(), "My card was charged twice.",
		schemaflux.Otherwise(func() { routed.WriteString("general queue") }),
		schemaflux.Like("a billing question", func() { routed.WriteString("billing queue") }),
	)
	if err != nil {
		fmt.Println("could not match:", err)
		return
	}

	fmt.Println("matched:", matched)
	fmt.Println("routed to:", routed.String())

	// Output:
	// matched: true
	// routed to: billing queue
}
