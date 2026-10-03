package tests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

// dec12Log collects the records a client's observer is told.
type dec12Log struct {
	mu      sync.Mutex
	records []schemaflux.DecisionRecord
}

func (l *dec12Log) observe(_ context.Context, record schemaflux.DecisionRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, record)
}

func (l *dec12Log) taken() []schemaflux.DecisionRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]schemaflux.DecisionRecord(nil), l.records...)
}

// BM-012 through the exported API and a provider instance: a client's observer
// is told what each decision on its context asked and read, and the decision is
// otherwise what it was.
func TestIntegrationADecisionLogRecordsWhatWasAskedAndRead(t *testing.T) {
	type triage struct {
		Spam    bool   `ask:"Is this message spam?"`
		Team    string `ask:"Which team should handle it?" oneof:"billing,technical,shipping"`
		Urgency int    `ask:"How urgent is it?" scale:"1-5"`
	}
	provider := testfixtures.NewScriptedReplies(
		`{"q1":true}`,
		`{"q1":2}`,
		`{"q1":4}`,
		`{"q1":false,"q2":1,"q3":5}`,
		`{"q1":false,"q2":3,"q3":2,"confidence":0.8}`,
		`{}`,
	)
	log := &dec12Log{}
	client := schemaflux.NewClient("test-key").WithProviderInstance(provider).WithDecisionObserver(log.observe)
	ctx := client.Context(context.Background())

	refund, err := schemaflux.Is(ctx, bmMessage, "Is the customer asking for money back?")
	if err != nil || !refund {
		t.Fatalf("Is = %v, %v", refund, err)
	}
	team, err := schemaflux.Which(ctx, bmMessage, "Which team should handle this?", []string{"billing", "technical"})
	if err != nil || team != "technical" {
		t.Fatalf("Which = %q, %v", team, err)
	}
	urgency, err := schemaflux.Rate(ctx, bmMessage, "How urgent is this?", 1, 5)
	if err != nil || urgency != 4 {
		t.Fatalf("Rate = %d, %v", urgency, err)
	}
	if _, err := schemaflux.Assess[triage](ctx, bmMessage); err != nil {
		t.Fatalf("Assess: %v", err)
	}
	result, err := schemaflux.AssessResult[triage](ctx, bmMessage)
	if err != nil || result.Value.Answer.Team != "shipping" {
		t.Fatalf("AssessResult = %+v, %v", result.Value, err)
	}
	_, failure := schemaflux.Is(ctx, bmMessage, "Is the customer asking for money back?")
	if failure == nil {
		t.Fatal("an empty answer was accepted")
	}

	records := log.taken()
	if len(records) != 6 {
		t.Fatalf("the observer was told %d times, want 6", len(records))
	}
	operations := []string{"is", "which", "rate", "assess", "assess", "is"}
	for i, record := range records {
		if record.Operation != operations[i] {
			t.Errorf("record %d is a %q, want %q", i+1, record.Operation, operations[i])
		}
	}

	if got := records[0].Asked; len(got) != 1 || got[0].Question != "Is the customer asking for money back?" || got[0].Answer != true {
		t.Errorf("the Is record: %+v", got)
	}
	if got := records[1].Asked[0]; got.Answer != "technical" || strings.Join(got.Options, ",") != "billing,technical" {
		t.Errorf("the Which record: %+v", got)
	}
	if got := records[2].Asked[0]; got.Answer != 4 || got.Low != 1 || got.High != 5 {
		t.Errorf("the Rate record: %+v", got)
	}
	assessed := records[3].Asked
	if len(assessed) != 3 || assessed[0].Answer != false || assessed[1].Answer != "billing" || assessed[2].Answer != 5 || assessed[2].Question != "How urgent is it?" {
		t.Errorf("the Assess record: %+v", assessed)
	}
	if records[3].ModelConfidence != nil {
		t.Error("the plain form asked for a confidence")
	}
	if c := records[4].ModelConfidence; c == nil || *c != 0.8 {
		t.Errorf("the Result form's confidence is %v", c)
	}
	failed := records[5]
	if failed.Err == nil || !errors.Is(failed.Err, schemaflux.ErrSchemaViolation) || failed.Err != failure {
		t.Errorf("the failed record's error is %v, the caller's was %v", failed.Err, failure)
	}
	if len(failed.Asked) != 1 || failed.Asked[0].Answer != nil || failed.Asked[0].Question == "" {
		t.Errorf("the failed record: %+v", failed.Asked)
	}
	for _, record := range records {
		if record.Elapsed < 0 {
			t.Errorf("a record has an elapsed time of %v", record.Elapsed)
		}
	}
}

// A client with no observer registered makes the same decisions, and a call on a
// context from another client is told to the observer of that one only.
func TestIntegrationADecisionLogBelongsToItsClient(t *testing.T) {
	provider := testfixtures.NewScripted(`{"q1":true}`)
	logged, other := &dec12Log{}, &dec12Log{}
	withObserver := schemaflux.NewClient("test-key").WithProviderInstance(provider).WithDecisionObserver(logged.observe)
	withContext := withObserver.Context(context.Background())
	plain := schemaflux.NewClient("test-key").WithProviderInstance(provider)
	plainContext := plain.Context(context.Background())
	second := schemaflux.NewClient("test-key").WithProviderInstance(provider).WithDecisionObserver(other.observe)
	secondContext := second.Context(context.Background())

	for _, ctx := range []context.Context{plainContext, withContext, secondContext, withContext} {
		if got, err := schemaflux.Is(ctx, bmMessage, "Is it a complaint?"); err != nil || !got {
			t.Fatalf("Is = %v, %v", got, err)
		}
	}
	if len(logged.taken()) != 2 || len(other.taken()) != 1 {
		t.Errorf("the observers were told %d and %d times, want 2 and 1", len(logged.taken()), len(other.taken()))
	}

	// Taken off, it is told nothing more to a context taken after.
	withObserver.WithDecisionObserver(nil)
	if _, err := schemaflux.Is(withObserver.Context(context.Background()), bmMessage, "Is it a complaint?"); err != nil {
		t.Fatal(err)
	}
	if len(logged.taken()) != 2 {
		t.Error("an observer that was removed was told")
	}
}

// Decisions that run at once under Gather each report themselves, and the
// record of one carries no answer of another.
func TestIntegrationDecisionsRunningTogetherAreEachLogged(t *testing.T) {
	log := &dec12Log{}
	client := schemaflux.NewClient("test-key").WithProviderInstance(testfixtures.NewScripted(`{"q1":true}`)).WithDecisionObserver(log.observe)
	ctx := client.Context(context.Background())

	ask := func(question string) schemaflux.Step[bool] {
		return func(ctx context.Context) (bool, error) { return schemaflux.Is(ctx, bmMessage, question) }
	}
	if _, err := schemaflux.Gather(ctx, ask("Is it about billing?"), ask("Is it urgent?"), ask("Is it angry?")); err != nil {
		t.Fatal(err)
	}
	questions := map[string]bool{}
	for _, record := range log.taken() {
		questions[record.Asked[0].Question] = true
	}
	if len(log.taken()) != 3 || len(questions) != 3 {
		t.Errorf("%d records over %d different questions, want 3 and 3", len(log.taken()), len(questions))
	}
}

// ExampleDecisionRecord prints a decision log: what a program that branched on
// a model's answers asked, and what it read.
func ExampleDecisionRecord() {
	provider := testfixtures.NewScriptedReplies(`{"q1":true}`, `{"q1":1}`, `{"q1":4}`)
	client := schemaflux.NewClient("example-key").
		WithProviderInstance(provider).
		WithDecisionObserver(func(_ context.Context, record schemaflux.DecisionRecord) {
			for _, asked := range record.Asked {
				fmt.Printf("%-5s %q -> %v\n", record.Operation, asked.Question, asked.Answer)
			}
		})
	ctx := client.Context(context.Background())
	message := "I was charged twice for order 7731. Please send the second charge back."

	refund, _ := schemaflux.Is(ctx, message, "Is the customer asking for money back?")
	team, _ := schemaflux.Which(ctx, message, "Which team should handle this?", []string{"billing", "technical", "shipping"})
	urgency, _ := schemaflux.Rate(ctx, message, "How urgent is this?", 1, 5)
	fmt.Println("took the path:", refund, team, urgency)

	// Output:
	// is    "Is the customer asking for money back?" -> true
	// which "Which team should handle this?" -> billing
	// rate  "How urgent is this?" -> 4
	// took the path: true billing 4
}

// WhichOrNoneResult through the exported API: the label, whether one was found,
// the model's confidence and the envelope.
func TestIntegrationWhichOrNoneResultReturnsTheAnswerAndTheEnvelope(t *testing.T) {
	ctx := context.Background()
	tickets := []string{"Late parcel", "Double charge", "Password reset"}
	cases := []struct {
		name      string
		body      string
		labels    []string
		wantLabel string
		wantFound bool
		wantConf  float64
		wantErr   bool
	}{
		{"the first", `{"q1":1,"confidence":0.9}`, tickets, "Late parcel", true, 0.9, false},
		{"the second", `{"q1":2,"confidence":0.8}`, tickets, "Double charge", true, 0.8, false},
		{"the last", `{"q1":3,"confidence":0.7}`, tickets, "Password reset", true, 0.7, false},
		{"none", `{"q1":4,"confidence":0.6}`, tickets, "", false, 0.6, false},
		{"none, with confidence zero", `{"q1":4,"confidence":0}`, tickets, "", false, 0, false},
		{"one label", `{"q1":1,"confidence":1}`, []string{"Only"}, "Only", true, 1, false},
		{"no labels", `{"q1":1,"confidence":1}`, nil, "", false, 0, false},
		{"past none", `{"q1":5,"confidence":0.5}`, tickets, "", false, 0, true},
		{"no confidence", `{"q1":1}`, tickets, "", false, 0, true},
		{"no answer", `{"confidence":0.5}`, tickets, "", false, 0, true},
		{"the label itself", `{"q1":"Late parcel","confidence":0.5}`, tickets, "", false, 0, true},
		{"the added option as a label", `{"q1":1,"confidence":0.5}`, []string{"none of these"}, "", false, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testfixtures.WithScriptedProvider(t, tc.body, nil)
			result, err := schemaflux.WhichOrNoneResult(ctx, bmMessage, "Which open ticket is this about?", tc.labels)
			if result.Meta.Operation != "which" {
				t.Errorf("the envelope names %q", result.Meta.Operation)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("the answer was accepted: %+v", result.Value)
				}
				if result.Value != (schemaflux.Judged[schemaflux.Picked]{}) {
					t.Errorf("a value came back beside the error: %+v", result.Value)
				}
				return
			}
			if err != nil {
				t.Fatalf("the call failed: %v", err)
			}
			got := result.Value
			if got.Answer.Label != tc.wantLabel || got.Answer.Found != tc.wantFound || got.ModelConfidence != tc.wantConf {
				t.Errorf("got %+v, want %q, %v, %v", got, tc.wantLabel, tc.wantFound, tc.wantConf)
			}
		})
	}
}

// ExampleWhichOrNoneResult asks which open ticket a message follows up, and
// reads the model's confidence beside the answer.
func ExampleWhichOrNoneResult() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(`{"q1":2,"confidence":0.9}`))

	result, err := schemaflux.WhichOrNoneResult(context.Background(),
		"Any news on the second charge? It is still on my statement.",
		"Which of these open tickets is this a follow-up to?",
		[]string{"Late parcel for order 7731", "Double charge on order 7731"})
	if err != nil {
		fmt.Println("could not decide:", err)
		return
	}
	if result.Value.Answer.Found {
		fmt.Printf("follow-up to %q, the model is %.0f%% sure\n", result.Value.Answer.Label, result.Value.ModelConfidence*100)
	} else {
		fmt.Println("a new matter")
	}

	// Output:
	// follow-up to "Double charge on order 7731", the model is 90% sure
}
