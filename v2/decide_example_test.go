package schemaflux_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/mw"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// Is answers a yes-or-no question about any value. The model is scripted here,
// so the example runs offline.
func ExampleIs() {
	var model schemafluxtest.Provider
	model.Reply(`{"q1":true}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	refund, err := schemaflux.Is(ctx, "Please refund my order.", "Is the customer asking for a refund?")
	fmt.Println(refund, err)
	// Output: true <nil>
}

// IsDetail is Is with the model's own confidence in its answer, which is a
// claim and not a measurement.
func ExampleIsDetail() {
	var model schemafluxtest.Provider
	model.Reply(`{"q1":false,"confidence":0.8}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	j, err := schemaflux.IsDetail(ctx, "How do I reset my password?", "Is the customer asking for a refund?")
	fmt.Println(j.Answer, j.ModelConfidence, err)
	// Output: false 0.8 <nil>
}

// With a decision provider in front of the model, Is asks it first, and the
// Record says who answered.
func ExampleIs_decisionProvider() {
	var model schemafluxtest.Provider
	decider := schemafluxtest.NewDecider(schemafluxtest.Yes(0.97))
	recorder := &schemaflux.Recorder{}
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{
		Provider: mw.Decisions(&model, decider, mw.DecisionOptions{}),
		Recorder: recorder,
	})

	refund, err := schemaflux.Is(ctx, "Please refund my order.", "Is the customer asking for a refund?")
	record := recorder.Records()[0]
	fmt.Println(refund, err, record.AnsweredBy, record.Typed, model.Calls())
	// Output: true <nil> test-decider true 0
}

// Which chooses one of the labels and returns it as it was given. The model is
// scripted here, so the example runs offline.
func ExampleWhich() {
	var model schemafluxtest.Provider
	model.Reply(`{"q1":2}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	team, err := schemaflux.Which(ctx, "I was charged twice for my order.", "Which team should handle this?",
		[]string{"billing", "technical", "shipping"})
	fmt.Println(team, err)
	// Output: technical <nil>
}

// WhichDetail is Which with the model's own confidence in its answer.
func ExampleWhichDetail() {
	var model schemafluxtest.Provider
	model.Reply(`{"q1":1,"confidence":0.9}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	j, err := schemaflux.WhichDetail(ctx, "I was charged twice for my order.", "Which team should handle this?",
		[]string{"billing", "technical", "shipping"})
	fmt.Println(j.Answer, j.ModelConfidence, err)
	// Output: billing 0.9 <nil>
}

// WhichOrNone is Which with "none of these" as an answer. With three labels the
// fourth option is none.
func ExampleWhichOrNone() {
	var model schemafluxtest.Provider
	model.Reply(`{"q1":4}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	ticket, found, err := schemaflux.WhichOrNone(ctx, "My parcel has not come.", "Which open ticket is this a follow-up to?",
		[]string{"#101 refund", "#102 login", "#103 invoice"})
	fmt.Printf("%q %v %v\n", ticket, found, err)
	// Output: "" false <nil>
}

// WhichOrNoneDetail returns the label as a Judged, whether one was found, and
// the model's own confidence.
func ExampleWhichOrNoneDetail() {
	var model schemafluxtest.Provider
	model.Reply(`{"q1":3,"confidence":0.7}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	j, found, err := schemaflux.WhichOrNoneDetail(ctx, "My invoice is wrong.", "Which open ticket is this a follow-up to?",
		[]string{"#101 refund", "#102 login", "#103 invoice"})
	fmt.Println(j.Answer, found, j.ModelConfidence, err)
	// Output: #103 invoice true 0.7 <nil>
}

// Rate answers with a whole number on the scale, both ends included.
func ExampleRate() {
	var model schemafluxtest.Provider
	model.Reply(`{"q1":5}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	urgency, err := schemaflux.Rate(ctx, "My site is down and customers are leaving.", "How urgent is this?", 1, 5)
	fmt.Println(urgency, err)
	// Output: 5 <nil>
}

// RateDetail is Rate with the model's own confidence in its answer.
func ExampleRateDetail() {
	var model schemafluxtest.Provider
	model.Reply(`{"q1":2,"confidence":0.6}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	j, err := schemaflux.RateDetail(ctx, "The logo looks a little off.", "How urgent is this?", 1, 5)
	fmt.Println(j.Answer, j.ModelConfidence, err)
	// Output: 2 0.6 <nil>
}

// Triage is read by Assess: each field asks a question in its `ask` tag.
type Triage struct {
	Spam    bool   `ask:"Is this message spam?"`
	Team    string `ask:"Which team should handle it?" oneof:"billing,technical,shipping"`
	Urgency int    `ask:"How urgent is it?" scale:"1-5"`
}

// Assess asks several questions about one subject in a single request and fills
// the fields of a struct with the answers.
func ExampleAssess() {
	var model schemafluxtest.Provider
	model.Reply(`{"q1":false,"q2":1,"q3":4}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	triage, err := schemaflux.Assess[Triage](ctx, "I was charged twice for my order.")
	fmt.Println(triage.Spam, triage.Team, triage.Urgency, err, model.Calls())
	// Output: false billing 4 <nil> 1
}

// AssessDetail is Assess with the model's confidence in its answers taken
// together.
func ExampleAssessDetail() {
	var model schemafluxtest.Provider
	model.Reply(`{"q1":true,"q2":3,"q3":1,"confidence":0.85}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	j, err := schemaflux.AssessDetail[Triage](ctx, "Win a free cruise! Click now.")
	fmt.Println(j.Answer.Spam, j.Answer.Team, j.ModelConfidence, err)
	// Output: true shipping 0.85 <nil>
}

// Whether asks a yes-or-no question and runs one of two steps. A step that was
// not chosen is not called, and the model is asked once.
func ExampleWhether() {
	var model schemafluxtest.Provider
	model.Reply(`{"q1":true}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	discard := func(context.Context) (string, error) { return "discarded", nil }
	handle := func(context.Context) (string, error) { return "handled", nil }
	reply, err := schemaflux.Whether(ctx, "Win a free cruise! Click now.", "Is this message spam?", discard, handle)
	fmt.Println(reply, err, model.Calls())
	// Output: discarded <nil> 1
}

// RouteBy asks which of the routes' labels answers the question and runs that
// route. With a Recorder on the client the Record of the decision is there when
// the route runs.
func ExampleRouteBy() {
	var model schemafluxtest.Provider
	model.Reply(`{"q1":2}`)
	recorder := &schemaflux.Recorder{}
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model, Recorder: recorder})

	billing := func(context.Context) (string, error) { return "to billing", nil }
	technical := func(ctx context.Context) (string, error) {
		return fmt.Sprintf("to technical, after %d Record of %s", len(recorder.Records()), recorder.Records()[0].Op), nil
	}
	reply, err := schemaflux.RouteBy(ctx, "The app crashes on start.", "Which team should handle this?", []schemaflux.Route[string]{
		schemaflux.To("billing", billing),
		schemaflux.To("technical", technical),
	})
	fmt.Println(reply, err)
	// Output: to technical, after 1 Record of which <nil>
}
