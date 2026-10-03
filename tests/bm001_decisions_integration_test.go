package tests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

type bmTriage struct {
	Spam    bool   `ask:"Is this message spam?"`
	Team    string `ask:"Which team should handle it?" oneof:"billing,technical,shipping"`
	Urgency int    `ask:"How urgent is it?" scale:"1-5"`
}

const bmMessage = "I was charged twice for order 7731 and nobody has answered in a week."

// BM-001 through the exported API and the real OpenAI provider, against a
// scripted server. A decision returns the answer the model gave, or an error:
// an answer that leaves a question out is never read as "no", as the first
// label, or as the bottom of a scale.
func TestIntegrationDecisionsReturnTheAnswerOrAnError(t *testing.T) {
	ctx := context.Background()
	labels := []string{"billing", "technical", "shipping"}

	cases := []struct {
		name    string
		body    string
		run     func() (any, error)
		want    any
		wantErr bool
	}{
		{"Is: yes", `{"q1":true}`, func() (any, error) { return schemaflux.Is(ctx, bmMessage, "Is this about billing?") }, true, false},
		{"Is: no", `{"q1":false}`, func() (any, error) { return schemaflux.Is(ctx, bmMessage, "Is this about billing?") }, false, false},
		{"Is: no answer", `{}`, func() (any, error) { return schemaflux.Is(ctx, bmMessage, "Is this about billing?") }, false, true},
		{"Is: the word yes", `{"q1":"yes"}`, func() (any, error) { return schemaflux.Is(ctx, bmMessage, "Is this about billing?") }, false, true},
		{"Which: the second", `{"q1":2}`, func() (any, error) { return schemaflux.Which(ctx, bmMessage, "Which team?", labels) }, "technical", false},
		{"Which: no answer", `{}`, func() (any, error) { return schemaflux.Which(ctx, bmMessage, "Which team?", labels) }, "", true},
		{"Which: past the end", `{"q1":9}`, func() (any, error) { return schemaflux.Which(ctx, bmMessage, "Which team?", labels) }, "", true},
		{"Rate: four", `{"q1":4}`, func() (any, error) { return schemaflux.Rate(ctx, bmMessage, "How urgent?", 1, 5) }, 4, false},
		{"Rate: off the scale", `{"q1":9}`, func() (any, error) { return schemaflux.Rate(ctx, bmMessage, "How urgent?", 1, 5) }, 0, true},
		{"Rate: no answer", `{}`, func() (any, error) { return schemaflux.Rate(ctx, bmMessage, "How urgent?", 1, 5) }, 0, true},
		{"Assess: all three", `{"q1":false,"q2":1,"q3":5}`, func() (any, error) { return schemaflux.Assess[bmTriage](ctx, bmMessage) }, bmTriage{Team: "billing", Urgency: 5}, false},
		{"Assess: one missing", `{"q1":false,"q2":1}`, func() (any, error) { return schemaflux.Assess[bmTriage](ctx, bmMessage) }, bmTriage{}, true},
		{"Assess: an empty object", `{}`, func() (any, error) { return schemaflux.Assess[bmTriage](ctx, bmMessage) }, bmTriage{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testfixtures.WithScriptedProvider(t, tc.body, nil)
			got, err := tc.run()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("the answer was accepted: %v", got)
				}
				if !errors.Is(err, schemaflux.ErrSchemaViolation) && !errors.Is(err, schemaflux.ErrMalformedOutput) {
					t.Errorf("the error is not classified as the answer's fault: %v", err)
				}
			} else if err != nil {
				t.Fatalf("the call failed: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}

	// WhichOrNone with nothing to choose from asks nothing.
	testfixtures.WithScriptedProvider(t, `{"q1":1}`, nil)
	if label, found, err := schemaflux.WhichOrNone(ctx, bmMessage, "Which open ticket is this about?", nil); err != nil || found || label != "" {
		t.Errorf("WhichOrNone with no labels = %q, %v, %v", label, found, err)
	}

	// The Result form carries the confidence and the envelope.
	testfixtures.WithScriptedProvider(t, `{"q1":true,"confidence":0.75}`, nil)
	result, err := schemaflux.IsResult(ctx, bmMessage, "Is this about billing?")
	if err != nil || !result.Value.Answer || result.Value.ModelConfidence != 0.75 || result.Meta.Operation != "is" {
		t.Errorf("IsResult = %+v, %v", result, err)
	}
}

// BM-002 through the exported API: a decision is asked once and one branch is
// run, and a decision that fails runs none.
func TestIntegrationCombinatorsRunTheBranchTheAnswerNames(t *testing.T) {
	ctx := context.Background()
	say := func(text string, ran *[]string) schemaflux.Step[string] {
		return func(context.Context) (string, error) {
			*ran = append(*ran, text)
			return text, nil
		}
	}
	routes := func(ran *[]string) []schemaflux.Route[string] {
		return []schemaflux.Route[string]{
			schemaflux.To("billing", say("to billing", ran)),
			schemaflux.To("technical", say("to technical", ran)),
		}
	}

	cases := []struct {
		name    string
		body    string
		run     func(ran *[]string) (string, error)
		want    string
		wantErr bool
	}{
		{"Whether: yes", `{"q1":true}`, func(ran *[]string) (string, error) {
			return schemaflux.Whether(ctx, bmMessage, "Is this spam?", say("discard", ran), say("handle", ran))
		}, "discard", false},
		{"Whether: no", `{"q1":false}`, func(ran *[]string) (string, error) {
			return schemaflux.Whether(ctx, bmMessage, "Is this spam?", say("discard", ran), say("handle", ran))
		}, "handle", false},
		{"Whether: no answer", `{}`, func(ran *[]string) (string, error) {
			return schemaflux.Whether(ctx, bmMessage, "Is this spam?", say("discard", ran), say("handle", ran))
		}, "", true},
		{"RouteBy: the first", `{"q1":1}`, func(ran *[]string) (string, error) {
			return schemaflux.RouteBy(ctx, bmMessage, "Which team?", routes(ran))
		}, "to billing", false},
		{"RouteBy: the second", `{"q1":2}`, func(ran *[]string) (string, error) {
			return schemaflux.RouteBy(ctx, bmMessage, "Which team?", routes(ran))
		}, "to technical", false},
		{"RouteBy: no answer", `{}`, func(ran *[]string) (string, error) {
			return schemaflux.RouteBy(ctx, bmMessage, "Which team?", routes(ran))
		}, "", true},
		{"RouteBy: an answer that is no route", `{"q1":3}`, func(ran *[]string) (string, error) {
			return schemaflux.RouteBy(ctx, bmMessage, "Which team?", routes(ran))
		}, "", true},
		{"Branch: yes", `{}`, func(ran *[]string) (string, error) {
			return schemaflux.Branch(ctx, true, say("then", ran), say("otherwise", ran))
		}, "then", false},
		{"Branch: no", `{}`, func(ran *[]string) (string, error) {
			return schemaflux.Branch(ctx, false, say("then", ran), say("otherwise", ran))
		}, "otherwise", false},
		{"Select: a label that is a route", `{}`, func(ran *[]string) (string, error) {
			return schemaflux.Select(ctx, "technical", routes(ran)...)
		}, "to technical", false},
		{"Select: a label that is no route", `{}`, func(ran *[]string) (string, error) {
			return schemaflux.Select(ctx, "returns", routes(ran)...)
		}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testfixtures.WithScriptedProvider(t, tc.body, nil)
			var ran []string
			got, err := tc.run(&ran)
			if tc.wantErr != (err != nil) || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
			if tc.wantErr && len(ran) != 0 {
				t.Errorf("a branch ran after the decision failed: %v", ran)
			}
			if !tc.wantErr && (len(ran) != 1 || ran[0] != tc.want) {
				t.Errorf("the branches that ran: %v", ran)
			}
		})
	}

	// Gather and Each: every step runs, the results are in order, and a
	// failure returns nothing.
	testfixtures.WithScriptedProvider(t, `{"q1":true}`, nil)
	ask := func(question string) schemaflux.Step[bool] {
		return func(ctx context.Context) (bool, error) { return schemaflux.Is(ctx, bmMessage, question) }
	}
	answers, err := schemaflux.Gather(ctx, ask("Is it about billing?"), ask("Is it urgent?"), ask("Is it angry?"))
	if err != nil || len(answers) != 3 || !answers[0] || !answers[1] || !answers[2] {
		t.Errorf("Gather = %v, %v", answers, err)
	}
	spam, urgency, err := schemaflux.Gather2(ctx, ask("Is it spam?"), func(context.Context) (int, error) { return 3, nil })
	if err != nil || !spam || urgency != 3 {
		t.Errorf("Gather2 = %v, %d, %v", spam, urgency, err)
	}
	lengths, err := schemaflux.Each(ctx, []string{"a", "bb", "ccc"}, func(_ context.Context, text string) (int, error) { return len(text), nil })
	if err != nil || len(lengths) != 3 || lengths[0] != 1 || lengths[2] != 3 {
		t.Errorf("Each = %v, %v", lengths, err)
	}
	failing := errors.New("the second step failed")
	if got, err := schemaflux.Gather(ctx, ask("Is it spam?"), func(context.Context) (bool, error) { return true, failing }); !errors.Is(err, failing) || got != nil {
		t.Errorf("Gather after a failure = %v, %v", got, err)
	}
}

// The questions reach the provider as data in the message, and nothing of the
// caller's is in the instructions or the declared answer.
func TestIntegrationADecisionKeepsTheCallersTextOutOfItsInstructions(t *testing.T) {
	provider := testfixtures.WithScriptedProvider(t, `{"q1":false,"q2":1,"q3":5}`, nil)
	if _, err := schemaflux.Assess[bmTriage](context.Background(), bmMessage); err != nil {
		t.Fatalf("Assess: %v", err)
	}
	requests := provider.Requests()
	if len(requests) != 1 {
		t.Fatalf("%d requests were made for three questions about one subject", len(requests))
	}
	request := requests[0]
	for _, text := range []string{"spam", "billing", "urgent", "7731"} {
		if strings.Contains(request.SystemPrompt, text) {
			t.Errorf("the instructions carry %q", text)
		}
	}
	for _, text := range []string{`"Is this message spam?"`, `1: "billing"`, "a rating from 1 to 5", "order 7731"} {
		if !strings.Contains(request.UserPrompt, text) {
			t.Errorf("the message has no %s:\n%s", text, request.UserPrompt)
		}
	}
	if request.JSONSchema == nil || request.SchemaName != "assessment" {
		t.Errorf("no declared answer: %q", request.SchemaName)
	}
}

// ExampleIs is a yes-or-no question a program branches on. There is no prompt
// in it: the question is the whole of what the caller writes.
func ExampleIs() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(`{"q1":true}`))

	refund, err := schemaflux.Is(context.Background(),
		"I was charged twice for order 7731. Please send the second charge back.",
		"Is the customer asking for money back?")
	if err != nil {
		fmt.Println("could not decide:", err)
		return
	}
	fmt.Println("refund requested:", refund)

	// Output:
	// refund requested: true
}

// ExampleWhich chooses one of the labels it is given, and returns that label.
func ExampleWhich() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(`{"q1":1}`))

	team, err := schemaflux.Which(context.Background(),
		"I was charged twice for order 7731.",
		"Which team should handle this?",
		[]string{"billing", "technical", "shipping"})
	if err != nil {
		fmt.Println("could not decide:", err)
		return
	}
	fmt.Println("team:", team)

	// Output:
	// team: billing
}

// ExampleRate answers on a scale.
func ExampleRate() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(`{"q1":4}`))

	urgency, err := schemaflux.Rate(context.Background(),
		"Our checkout has been down for an hour.",
		"How urgent is this?", 1, 5)
	if err != nil {
		fmt.Println("could not decide:", err)
		return
	}
	fmt.Println("urgency:", urgency)

	// Output:
	// urgency: 4
}

// ExampleAssess asks three questions about one message in one call.
func ExampleAssess() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(`{"q1":false,"q2":1,"q3":4}`))

	type Triage struct {
		Spam    bool   `ask:"Is this message spam?"`
		Team    string `ask:"Which team should handle it?" oneof:"billing,technical,shipping"`
		Urgency int    `ask:"How urgent is it?" scale:"1-5"`
	}
	triage, err := schemaflux.Assess[Triage](context.Background(),
		"I was charged twice for order 7731 and nobody has answered in a week.")
	if err != nil {
		fmt.Println("could not decide:", err)
		return
	}
	fmt.Printf("spam=%v team=%s urgency=%d\n", triage.Spam, triage.Team, triage.Urgency)

	// Output:
	// spam=false team=billing urgency=4
}

// ExampleRouteBy asks which way to go and goes that way.
func ExampleRouteBy() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(`{"q1":2}`))

	handle := func(team string) schemaflux.Step[string] {
		return func(context.Context) (string, error) { return "sent to " + team, nil }
	}
	outcome, err := schemaflux.RouteBy(context.Background(),
		"The app crashes when I open my invoices.",
		"Which team should handle this?",
		[]schemaflux.Route[string]{
			schemaflux.To("billing", handle("billing")),
			schemaflux.To("technical", handle("technical")),
		})
	if err != nil {
		fmt.Println("could not decide:", err)
		return
	}
	fmt.Println(outcome)

	// Output:
	// sent to technical
}

// ExampleGather asks three independent questions at once and waits as long as
// the slowest of them takes.
func ExampleGather() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(`{"q1":true}`))

	message := "I was charged twice and I am cancelling if this is not fixed today."
	ask := func(question string) schemaflux.Step[bool] {
		return func(ctx context.Context) (bool, error) { return schemaflux.Is(ctx, message, question) }
	}
	answers, err := schemaflux.Gather(context.Background(),
		ask("Is the customer asking for money back?"),
		ask("Is the customer angry?"),
		ask("Is the customer threatening to leave?"))
	if err != nil {
		fmt.Println("could not decide:", err)
		return
	}
	fmt.Println(answers)

	// Output:
	// [true true true]
}
