package ops

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// LAT-006. What this library spends on a decision besides the wait for the
// model: building the request, and reading the answer back into the caller's
// type. A program that makes every control-flow decision with these calls pays
// it on every message, so it is measured by itself.

// dec12Screen, dec12Triage and dec12After are the shape of the questions a
// support desk asks of a message (examples/flowdesk/desk), copied here so that
// this test does not import the example.
type dec12Screen struct {
	Spam         bool   `ask:"Is this message spam, a marketing blast or an automated notification, and not a customer writing to a support desk?"`
	Foreign      bool   `ask:"Is the body of the message written in a language other than English?"`
	Language     string `ask:"What language is the body of the message written in?" oneof:"English,Spanish,French,German,Portuguese,another language"`
	PersonalData bool   `ask:"Does the body of the message contain a payment card number, a password, a phone number, a street address or a government ID number? An email address, a name or an order number does not count."`
}

type dec12Triage struct {
	Team       string `ask:"Which team should handle this message?" oneof:"billing,technical,shipping,account"`
	Intent     string `ask:"What is the customer doing?" oneof:"asking a question,making a complaint,requesting an action,giving feedback"`
	Urgency    int    `ask:"How urgent is it, where 1 can wait a week and 5 means the customer is blocked or losing money right now?" scale:"1-5"`
	Angry      bool   `ask:"Is the customer angry or threatening to leave?"`
	Refund     bool   `ask:"Is the customer asking for money back?"`
	Answerable bool   `ask:"Does the message say what the problem or the request is, specifically enough that the desk knows what to look into? Answer no only when it is too vague to tell what it is about."`
	NeedsHuman bool   `ask:"Is the customer demanding money back, or threatening a chargeback or legal action, so that a person has to approve the reply before it is sent?"`
}

type dec12After struct {
	Next  string `ask:"What should the desk do after replying?" oneof:"close the ticket,wait for the customer,follow up tomorrow,escalate to a manager"`
	Again bool   `ask:"Is this customer likely to write to the desk again about the same matter within a week?"`
}

// dec12Look is thirteen questions over three embedded structs.
type dec12Look struct {
	dec12Screen
	dec12Triage
	dec12After
}

// dec12Message is a subject of the size a support message is.
type dec12Message struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

var dec12Subject = dec12Message{
	ID:      "M-1042",
	From:    "dana@example.com",
	Subject: "Charged twice for order 7731",
	Body:    "Hello, I was charged twice for order 7731 on the 3rd and nobody has answered my two earlier emails. I would like the second charge back on my card by Friday, or I will dispute it with my bank. Thank you, Dana.",
}

const dec12LookAnswer = `{"q1":false,"q2":false,"q3":1,"q4":false,"q5":1,"q6":2,"q7":4,"q8":true,"q9":true,"q10":true,"q11":true,"q12":4,"q13":false}`

// dec12Shape is one decision as the benchmarks pose it.
type dec12Shape struct {
	name      string
	questions []assessQuestion
	// ask is what the call hands to be asked: for a type, its plan's, which is
	// worked out once; for one question, the question alone.
	ask      dec12Ask
	response string
	// whole makes the call through its exported function, with the provider
	// stubbed to answer at once.
	whole func(ctx context.Context) error
	// read reads the answer into what the caller gets back.
	read func(answers []answer) error
}

func dec12Shapes() []dec12Shape {
	sixLabels := []string{"billing", "technical", "shipping", "account", "returns", "sales"}
	lookPlan := planFor(reflect.TypeOf(dec12Look{}))

	isQuestions := []assessQuestion{{kind: questionYesNo, ask: "Is the customer asking for money back?"}}
	whichQuestions := []assessQuestion{{kind: questionChoice, ask: "Which team should handle this message?", labels: sixLabels}}

	return []dec12Shape{
		{
			name:      "Is",
			questions: isQuestions,
			ask:       dec12Ask{questions: isQuestions},
			response:  `{"q1":true}`,
			whole: func(ctx context.Context) error {
				_, err := Is(ctx, dec12Subject, "Is the customer asking for money back?")
				return err
			},
			read: func(answers []answer) error {
				_ = answers[0].yes
				return nil
			},
		},
		{
			name:      "Which6",
			questions: whichQuestions,
			ask:       dec12Ask{questions: whichQuestions},
			response:  `{"q1":3}`,
			whole: func(ctx context.Context) error {
				_, err := Which(ctx, dec12Subject, "Which team should handle this message?", sixLabels)
				return err
			},
			read: func(answers []answer) error {
				_ = sixLabels[answers[0].choice]
				return nil
			},
		},
		{
			name:      "Assess13",
			questions: lookPlan.questions,
			ask:       lookPlan.ask,
			response:  dec12LookAnswer,
			whole: func(ctx context.Context) error {
				_, err := Assess[dec12Look](ctx, dec12Subject)
				return err
			},
			read: func(answers []answer) error {
				var look dec12Look
				lookPlan.fill(reflect.ValueOf(&look).Elem(), answers)
				return nil
			},
		},
	}
}

// dec12StubProvider answers every call with response, at once.
func dec12StubProvider(tb testing.TB, response string) {
	tb.Helper()
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return response, nil
	})
	tb.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
}

// dec12Build is the first of the three things measured: the request, up to the
// provider call.
func dec12Build(shape dec12Shape) func() error {
	return func() error {
		_, err := dec12BuildRequest(context.Background(), dec12Subject, shape.ask, false, nil)
		return err
	}
}

// dec12Read is the second: reading the answer back into the caller's type.
func dec12Read(shape dec12Shape) func() error {
	return func() error {
		answers, _, err := readAssessAnswer(shape.response, shape.questions, false)
		if err != nil {
			return err
		}
		return shape.read(answers)
	}
}

func BenchmarkDecisionBuildRequest(b *testing.B) {
	for _, shape := range dec12Shapes() {
		b.Run(shape.name, func(b *testing.B) {
			run := dec12Build(shape)
			b.ReportAllocs()
			for b.Loop() {
				if err := run(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDecisionReadAnswer(b *testing.B) {
	for _, shape := range dec12Shapes() {
		b.Run(shape.name, func(b *testing.B) {
			run := dec12Read(shape)
			b.ReportAllocs()
			for b.Loop() {
				if err := run(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkDecisionWhole is the decision from the exported call to the answer
// with the provider stubbed, so it is everything the library does besides the
// wait.
func BenchmarkDecisionWhole(b *testing.B) {
	for _, shape := range dec12Shapes() {
		b.Run(shape.name, func(b *testing.B) {
			dec12StubProvider(b, shape.response)
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				if err := shape.whole(ctx); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// dec12Budgets is the most allocations one decision may make in each stage, by
// shape. Allocations do not vary from run to run, which is why they can be
// held; the time is only reported. A figure here goes down when the library
// gets cheaper, and goes up only on purpose, with the reason beside it.
//
// Each is what was measured plus two. Before LAT-006 the stages were 25, 47 and
// 198 allocations to build the request (Is, Which6, Assess13), 11, 13 and 60 to
// read the answer, and 44, 68 and 266 for the whole decision.
var dec12Budgets = map[string]struct{ build, read, whole float64 }{
	"Is":       {build: 7, read: 13, whole: 28},
	"Which6":   {build: 7, read: 13, whole: 28},
	"Assess13": {build: 6, read: 52, whole: 65},
}

func TestDecisionAllocationsStayWithinBudget(t *testing.T) {
	for _, shape := range dec12Shapes() {
		t.Run(shape.name, func(t *testing.T) {
			budget, ok := dec12Budgets[shape.name]
			if !ok {
				t.Fatalf("no budget is recorded for %s", shape.name)
			}
			dec12StubProvider(t, shape.response)
			ctx := context.Background()

			stages := []struct {
				name   string
				budget float64
				run    func() error
			}{
				{"build the request", budget.build, dec12Build(shape)},
				{"read the answer", budget.read, dec12Read(shape)},
				{"the whole decision", budget.whole, func() error { return shape.whole(ctx) }},
			}
			for _, stage := range stages {
				if err := stage.run(); err != nil {
					t.Fatalf("%s: %v", stage.name, err)
				}
				allocs := testing.AllocsPerRun(50, func() { _ = stage.run() })

				// The time is reported and not held: it depends on the machine
				// and on what else is running on it.
				const runs = 200
				started := time.Now()
				for i := 0; i < runs; i++ {
					_ = stage.run()
				}
				t.Logf("%-22s %6.0f allocations (budget %.0f), %v each", stage.name, allocs, stage.budget, time.Since(started)/runs)

				if allocs > stage.budget {
					t.Errorf("%s makes %.0f allocations, over its budget of %.0f", stage.name, allocs, stage.budget)
				}
			}
		})
	}
}
