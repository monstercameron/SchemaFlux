package tests

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/mw"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

// LX-100, LX-103, LX-104, through the exported API: the bounded operations
// answered by a decision provider, with a generating provider behind it.

// decisionPair is a client whose provider is a generating double paired with a
// deciding one. The generator answers under OpenAI's name and default model and
// the decider under Jev's, so each is priced at its own rate.
type decisionPair struct {
	generator *schemafluxtest.Provider
	decider   *schemafluxtest.Decider
	client    *schemaflux.Client
	ctx       context.Context
}

func newDecisionPair(t *testing.T, options mw.DecisionOptions) *decisionPair {
	t.Helper()
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "")
	pair := &decisionPair{
		generator: schemafluxtest.New().As("openai"),
		decider:   schemafluxtest.NewDecider().As("jev", "jev-1.13.0"),
	}
	pair.client = schemaflux.NewClient("test-key").
		WithModel("gpt-6-luna").
		WithProviderInstance(mw.Decisions(pair.generator, pair.decider, options))
	pair.ctx = pair.client.Context(context.Background())
	return pair
}

// only fails the test unless the decider was asked exactly once and the
// generator not at all, and returns the decision the decider was sent.
func (pair *decisionPair) only(t *testing.T) schemaflux.TypedDecision {
	t.Helper()
	if pair.decider.CallCount() != 1 || pair.generator.CallCount() != 0 {
		t.Fatalf("%d requests to the decider and %d to the generator, want one and none", pair.decider.CallCount(), pair.generator.CallCount())
	}
	decision := pair.decider.Requests()[0].Decision
	if decision == nil {
		t.Fatal("the request the decider was sent carries no decision")
	}
	return *decision
}

// extractOn and summarizeOn are the default options of the two, on a client's
// context.
func extractOn(ctx context.Context) schemaflux.ExtractOptions {
	opts := schemaflux.NewExtractOptions()
	opts.CommonOptions.Context = ctx
	return opts
}

func summarizeOn(ctx context.Context) schemaflux.SummarizeOptions {
	opts := schemaflux.NewSummarizeOptions()
	opts.CommonOptions.Context = ctx
	return opts
}

const refundMessage = "Please refund my order 7731, it arrived broken."

type deskTriage struct {
	Refund  bool   `ask:"Is the customer asking for a refund?"`
	Team    string `ask:"Which team should handle this?" oneof:"billing,technical,shipping"`
	Urgency int    `ask:"How urgent is this?" scale:"1-5"`
	Angry   bool   `ask:"Is the customer angry?"`
}

func TestIntegrationADecisionProviderAnswersIs(t *testing.T) {
	pair := newDecisionPair(t, mw.DecisionOptions{})
	pair.decider.Answer(schemafluxtest.Yes(0.96))

	got, err := schemaflux.Is(pair.ctx, refundMessage, "Is the customer asking for a refund?")
	if err != nil || !got {
		t.Fatalf("Is = %v, %v", got, err)
	}
	decision := pair.only(t)
	want := []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionYesNo, Ask: "Is the customer asking for a refund?"}}
	if !reflect.DeepEqual(decision.Questions, want) {
		t.Errorf("the decider was asked %+v", decision.Questions)
	}
	if decision.State != `"`+refundMessage+`"` || decision.Guidance != "" {
		t.Errorf("state %q and guidance %q", decision.State, decision.Guidance)
	}

	// A no is a no, and steering reaches the decider as guidance and not as state.
	pair.decider.Answer(schemafluxtest.No(0.8))
	got, err = schemaflux.Is(pair.ctx, refundMessage, "Is the customer asking for a refund?", schemaflux.OpOptions{Steering: "An exchange is not a refund."})
	if err != nil || got {
		t.Fatalf("Is = %v, %v", got, err)
	}
	second := pair.decider.Requests()[1].Decision
	if second.Guidance != "An exchange is not a refund." || second.State != decision.State {
		t.Errorf("guidance %q and state %q", second.Guidance, second.State)
	}
}

func TestIntegrationADecisionProviderAnswersWhichAndWhichOrNone(t *testing.T) {
	labels := []string{"billing", "technical", "shipping"}

	t.Run("Which", func(t *testing.T) {
		pair := newDecisionPair(t, mw.DecisionOptions{})
		pair.decider.Answer(schemafluxtest.Chose("shipping", 0.7))
		got, err := schemaflux.Which(pair.ctx, refundMessage, "Which team should handle this?", labels)
		if err != nil || got != "shipping" {
			t.Fatalf("Which = %q, %v", got, err)
		}
		question := pair.only(t).Questions[0]
		if question.Kind != schemaflux.DecisionChoice || !reflect.DeepEqual(question.Options, labels) || question.Criteria != nil {
			t.Errorf("the decider was asked %+v", question)
		}
	})
	t.Run("WhichOrNone finds one", func(t *testing.T) {
		pair := newDecisionPair(t, mw.DecisionOptions{})
		pair.decider.Answer(schemafluxtest.Chose("technical", 0.9))
		got, found, err := schemaflux.WhichOrNone(pair.ctx, refundMessage, "Which team?", labels)
		if err != nil || !found || got != "technical" {
			t.Fatalf("WhichOrNone = %q, %v, %v", got, found, err)
		}
		if options := pair.only(t).Questions[0].Options; len(options) != 4 || options[3] != "none of these" {
			t.Errorf("the decider was offered %q, want the labels and the answer for none", options)
		}
	})
	t.Run("WhichOrNone finds none", func(t *testing.T) {
		pair := newDecisionPair(t, mw.DecisionOptions{})
		pair.decider.Answer(schemafluxtest.Chose("none of these", 0.9))
		got, found, err := schemaflux.WhichOrNone(pair.ctx, refundMessage, "Which team?", labels)
		if err != nil || found || got != "" {
			t.Fatalf("WhichOrNone = %q, %v, %v", got, found, err)
		}
		pair.only(t)
	})
	t.Run("a label that was not offered is refused and not the first label", func(t *testing.T) {
		pair := newDecisionPair(t, mw.DecisionOptions{})
		pair.decider.Answer(schemafluxtest.Chose("returns", 0.99))
		got, err := schemaflux.Which(pair.ctx, refundMessage, "Which team?", labels)
		if err == nil || got != "" || schemaflux.KindOf(err) != types.KindSchemaViolation {
			t.Fatalf("Which = %q, %v", got, err)
		}
		if strings.Contains(err.Error(), "returns") {
			t.Errorf("the error repeats the answer: %v", err)
		}
	})
}

func TestIntegrationADecisionProviderAnswersRate(t *testing.T) {
	pair := newDecisionPair(t, mw.DecisionOptions{})
	pair.decider.Answer(schemafluxtest.Level(4, 0.88))
	got, err := schemaflux.Rate(pair.ctx, refundMessage, "How urgent is this?", 1, 5)
	if err != nil || got != 4 {
		t.Fatalf("Rate = %d, %v", got, err)
	}
	question := pair.only(t).Questions[0]
	if question.Kind != schemaflux.DecisionScale || question.Low != 1 || question.High != 5 {
		t.Errorf("the decider was asked %+v", question)
	}

	// A level off the scale is refused and not taken for its nearest end.
	pair.decider.Answer(schemafluxtest.Level(6, 0.99))
	if got, err := schemaflux.Rate(pair.ctx, refundMessage, "How urgent is this?", 1, 5); err == nil || got != 0 {
		t.Errorf("Rate = %d, %v for a level off the scale", got, err)
	}
}

func TestIntegrationADecisionProviderAnswersAnAssessOfFourFieldsInOneRequest(t *testing.T) {
	pair := newDecisionPair(t, mw.DecisionOptions{})
	pair.decider.Answer(schemafluxtest.Yes(0.96), schemafluxtest.Chose("shipping", 0.67), schemafluxtest.Level(4, 0.99), schemafluxtest.No(0.9))

	got, err := schemaflux.Assess[deskTriage](pair.ctx, refundMessage)
	if err != nil {
		t.Fatalf("Assess: %v", err)
	}
	if want := (deskTriage{Refund: true, Team: "shipping", Urgency: 4, Angry: false}); got != want {
		t.Errorf("Assess = %+v, want %+v", got, want)
	}
	decision := pair.only(t)
	kinds := make([]schemaflux.DecisionKind, len(decision.Questions))
	for i, question := range decision.Questions {
		kinds[i] = question.Kind
	}
	want := []schemaflux.DecisionKind{schemaflux.DecisionYesNo, schemaflux.DecisionChoice, schemaflux.DecisionScale, schemaflux.DecisionYesNo}
	if !reflect.DeepEqual(kinds, want) {
		t.Errorf("the one request asked %v, want the four questions %v", kinds, want)
	}

	// One answer short: nothing is filled, and nothing is defaulted.
	pair.decider.Answer(schemafluxtest.Yes(0.96), schemafluxtest.Chose("shipping", 0.67), schemafluxtest.Level(4, 0.99))
	got, err = schemaflux.Assess[deskTriage](pair.ctx, refundMessage)
	if err == nil || got != (deskTriage{}) {
		t.Errorf("Assess = %+v, %v with one question unanswered", got, err)
	}
}

func TestIntegrationTheResultFormsReportTheDecidersProbability(t *testing.T) {
	labels := []string{"billing", "technical", "shipping"}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

	t.Run("IsResult", func(t *testing.T) {
		pair := newDecisionPair(t, mw.DecisionOptions{})
		pair.decider.Answer(schemafluxtest.Yes(0.96))
		result, err := schemaflux.IsResult(pair.ctx, refundMessage, "Is the customer asking for a refund?")
		if err != nil || !result.Value.Answer || !near(result.Value.ModelConfidence, 0.96) {
			t.Fatalf("IsResult = %+v, %v", result.Value, err)
		}
		if result.Meta.Provider != "jev" || result.Meta.Model != "jev-1.13.0" || result.Meta.Attempts != 1 {
			t.Errorf("the envelope names %s/%s after %d attempts", result.Meta.Provider, result.Meta.Model, result.Meta.Attempts)
		}
		pair.only(t)
	})
	t.Run("WhichResult", func(t *testing.T) {
		pair := newDecisionPair(t, mw.DecisionOptions{})
		pair.decider.Answer(schemafluxtest.Chose("billing", 0.67))
		result, err := schemaflux.WhichResult(pair.ctx, refundMessage, "Which team?", labels)
		if err != nil || result.Value.Answer != "billing" || !near(result.Value.ModelConfidence, 0.67) {
			t.Fatalf("WhichResult = %+v, %v", result.Value, err)
		}
		pair.only(t)
	})
	t.Run("WhichOrNoneResult", func(t *testing.T) {
		pair := newDecisionPair(t, mw.DecisionOptions{})
		pair.decider.Answer(schemafluxtest.Chose("none of these", 0.81))
		result, err := schemaflux.WhichOrNoneResult(pair.ctx, refundMessage, "Which team?", labels)
		if err != nil || result.Value.Answer.Found || !near(result.Value.ModelConfidence, 0.81) {
			t.Fatalf("WhichOrNoneResult = %+v, %v", result.Value, err)
		}
		pair.only(t)
	})
	t.Run("RateResult", func(t *testing.T) {
		pair := newDecisionPair(t, mw.DecisionOptions{})
		pair.decider.Answer(schemafluxtest.Level(2, 0.5))
		result, err := schemaflux.RateResult(pair.ctx, refundMessage, "How urgent?", 1, 5)
		if err != nil || result.Value.Answer != 2 || !near(result.Value.ModelConfidence, 0.5) {
			t.Fatalf("RateResult = %+v, %v", result.Value, err)
		}
		pair.only(t)
	})
	t.Run("AssessResult reports the least sure of its answers", func(t *testing.T) {
		pair := newDecisionPair(t, mw.DecisionOptions{})
		pair.decider.Answer(schemafluxtest.Yes(0.96), schemafluxtest.Chose("shipping", 0.67), schemafluxtest.Level(4, 0.99), schemafluxtest.No(0.9))
		result, err := schemaflux.AssessResult[deskTriage](pair.ctx, refundMessage)
		if err != nil || result.Value.Answer.Team != "shipping" || !near(result.Value.ModelConfidence, 0.67) {
			t.Fatalf("AssessResult = %+v, %v", result.Value, err)
		}
		pair.only(t)
	})
	t.Run("a decider that reports no probability gives a Result form no confidence to report", func(t *testing.T) {
		pair := newDecisionPair(t, mw.DecisionOptions{})
		pair.decider.Answer(schemaflux.DecisionAnswer{Kind: schemaflux.DecisionYesNo, Yes: true})
		result, err := schemaflux.IsResult(pair.ctx, refundMessage, "Is it?")
		if err == nil || result.Value.Answer || result.Value.ModelConfidence != 0 {
			t.Fatalf("IsResult = %+v, %v; a confidence was made up", result.Value, err)
		}
		// The plain form does not ask for one, and is answered.
		if got, err := schemaflux.Is(pair.ctx, refundMessage, "Is it?"); err != nil || !got {
			t.Errorf("Is = %v, %v", got, err)
		}
	})
}

func TestIntegrationADecisionProviderDecidesWhetherAndRouteBy(t *testing.T) {
	step := func(reply string) schemaflux.Step[string] {
		return func(context.Context) (string, error) { return reply, nil }
	}
	t.Run("Whether", func(t *testing.T) {
		pair := newDecisionPair(t, mw.DecisionOptions{})
		pair.decider.Answer(schemafluxtest.No(0.9))
		got, err := schemaflux.Whether(pair.ctx, refundMessage, "Is this message spam?", step("discarded"), step("handled"))
		if err != nil || got != "handled" {
			t.Fatalf("Whether = %q, %v", got, err)
		}
		pair.only(t)
	})
	t.Run("RouteBy", func(t *testing.T) {
		pair := newDecisionPair(t, mw.DecisionOptions{})
		pair.decider.Answer(schemafluxtest.Chose("technical", 0.9))
		routes := []schemaflux.Route[string]{schemaflux.To("billing", step("to billing")), schemaflux.To("technical", step("to technical"))}
		got, err := schemaflux.RouteBy(pair.ctx, refundMessage, "Which team should handle this?", routes)
		if err != nil || got != "to technical" {
			t.Fatalf("RouteBy = %q, %v", got, err)
		}
		if options := pair.only(t).Questions[0].Options; !reflect.DeepEqual(options, []string{"billing", "technical"}) {
			t.Errorf("the decider was offered %q", options)
		}
	})
	t.Run("a decider that fails runs no step", func(t *testing.T) {
		pair := newDecisionPair(t, mw.DecisionOptions{NoFailover: true})
		pair.decider.Fail(llm.NewAPIError("jev", "jev-latest", 503, ""))
		ran := false
		run := func(context.Context) (string, error) { ran = true; return "ran", nil }
		if got, err := schemaflux.Whether(pair.ctx, refundMessage, "Is this message spam?", run, run); err == nil || got != "" || ran {
			t.Errorf("Whether = %q, %v, a step ran: %v", got, err, ran)
		}
	})
}

func TestIntegrationADecisionProviderAnswersClassify(t *testing.T) {
	categories := []string{"billing", "technical", "shipping"}
	options := func(pair *decisionPair) schemaflux.ClassifyOptions {
		opts := schemaflux.NewClassifyOptions().WithCategories(categories)
		opts.CommonOptions.Context = pair.ctx
		return opts
	}

	t.Run("the label and the probability", func(t *testing.T) {
		pair := newDecisionPair(t, mw.DecisionOptions{})
		pair.decider.Answer(schemafluxtest.Chose("billing", 0.91))
		opts := options(pair).WithSteering("A refund is billing.")
		opts.CategoryDescriptions = map[string]string{"billing": "Payments and refunds", "shipping": "Parcels"}
		result, err := schemaflux.Classify[string, string](refundMessage, opts)
		if err != nil {
			t.Fatalf("Classify: %v", err)
		}
		if result.Category != "billing" || result.ModelConfidence != 0.91 || !reflect.DeepEqual(result.Categories, []string{"billing"}) {
			t.Errorf("Classify = %+v", result)
		}
		if result.Reasoning != "" || len(result.Alternatives) != 0 {
			t.Errorf("reasoning %q and alternatives %v were made up for a model that writes none", result.Reasoning, result.Alternatives)
		}
		decision := pair.only(t)
		question := decision.Questions[0]
		if question.Kind != schemaflux.DecisionChoice || !reflect.DeepEqual(question.Options, categories) ||
			!reflect.DeepEqual(question.Criteria, []string{"Payments and refunds", "", "Parcels"}) {
			t.Errorf("the decider was asked %+v", question)
		}
		if decision.Guidance != "A refund is billing." || decision.State != `"`+refundMessage+`"` {
			t.Errorf("guidance %q and state %q", decision.Guidance, decision.State)
		}
	})
	t.Run("the confidence floor of the options holds for a decider's probability", func(t *testing.T) {
		pair := newDecisionPair(t, mw.DecisionOptions{})
		pair.decider.Answer(schemafluxtest.Chose("billing", 0.4))
		result, err := schemaflux.Classify[string, string](refundMessage, options(pair).WithMinConfidence(0.8))
		if err == nil || result.Category != "" {
			t.Fatalf("Classify = %+v, %v below its own floor", result, err)
		}
		pair.only(t)
	})
	t.Run("a category that was not offered is refused", func(t *testing.T) {
		pair := newDecisionPair(t, mw.DecisionOptions{})
		pair.decider.Answer(schemafluxtest.Chose("returns", 0.99))
		if result, err := schemaflux.Classify[string, string](refundMessage, options(pair)); err == nil || result.Category != "" {
			t.Fatalf("Classify = %+v, %v", result, err)
		}
	})
	t.Run("a decider that reports no probability cannot classify", func(t *testing.T) {
		pair := newDecisionPair(t, mw.DecisionOptions{})
		pair.decider.Answer(schemaflux.DecisionAnswer{Kind: schemaflux.DecisionChoice, Label: "billing"})
		if result, err := schemaflux.Classify[string, string](refundMessage, options(pair)); err == nil || result.Category != "" || result.ModelConfidence != 0 {
			t.Fatalf("Classify = %+v, %v; a confidence was made up", result, err)
		}
	})
}

func TestIntegrationADecisionProviderAnswersChoose(t *testing.T) {
	tickets := goldenTickets()
	pair := newDecisionPair(t, mw.DecisionOptions{})
	pair.decider.Answer(schemafluxtest.Chose("i-000002", 0.8))

	got, err := schemaflux.Choose(tickets, schemaflux.NewChooseOptions().WithContext(pair.ctx).WithSteering("the most urgent").WithCriteria([]string{"blocked customers first"}))
	if err != nil || got != tickets[1] {
		t.Fatalf("Choose = %+v, %v", got, err)
	}
	decision := pair.only(t)
	question := decision.Questions[0]
	wantCriteria := []string{`{"id":"T-1","subject":"charged twice","amount":42.5}`, `{"id":"T-2","subject":"cannot log in","amount":0}`}
	if question.Kind != schemaflux.DecisionChoice || !reflect.DeepEqual(question.Options, []string{"i-000001", "i-000002"}) || !reflect.DeepEqual(question.Criteria, wantCriteria) {
		t.Errorf("the decider was asked %+v", question)
	}
	if decision.Guidance != "the most urgent. Selection criteria: blocked customers first" {
		t.Errorf("guidance %q", decision.Guidance)
	}
	if want := `[{"id":"i-000001","item":` + wantCriteria[0] + `},{"id":"i-000002","item":` + wantCriteria[1] + `}]`; decision.State != want {
		t.Errorf("state %s, want %s", decision.State, want)
	}

	// An id that was not offered is refused, as it is from a generating model.
	pair.decider.Answer(schemafluxtest.Chose("T-2", 0.99))
	if got, err := schemaflux.Choose(tickets, schemaflux.NewChooseOptions().WithContext(pair.ctx)); err == nil || got != (goldenTicket{}) {
		t.Errorf("Choose = %+v, %v for an id that was not offered", got, err)
	}
}

// What a choice among labels, a yes or no, or a level cannot express carries no
// decision; what the decider declares it cannot take is not sent to it. Either
// way the generator gets the request it gets with no decider there at all.
func TestIntegrationWhatADeciderCannotTakeGoesToTheGeneratorAsItAlwaysDid(t *testing.T) {
	type invoice struct {
		Number string  `json:"number"`
		Total  float64 `json:"total"`
	}
	many := make([]string, 12)
	for i := range many {
		many[i] = "label-" + string(rune('a'+i))
	}
	classify := func(ctx context.Context, change func(*schemaflux.ClassifyOptions)) error {
		opts := schemaflux.NewClassifyOptions().WithCategories([]string{"billing", "technical"})
		opts.CommonOptions.Context = ctx
		change(&opts)
		_, err := schemaflux.Classify[string, string](refundMessage, opts)
		return err
	}

	cases := []struct {
		name     string
		decision bool // whether the request carries a decision at all
		run      func(ctx context.Context) error
	}{
		{"Extract", false, func(ctx context.Context) error {
			_, err := schemaflux.Extract[invoice](goldenText, extractOn(ctx))
			return err
		}},
		{"Summarize", false, func(ctx context.Context) error {
			_, err := schemaflux.Summarize(goldenText, summarizeOn(ctx))
			return err
		}},
		{"Classify into several labels", false, func(ctx context.Context) error {
			return classify(ctx, func(opts *schemaflux.ClassifyOptions) { *opts = opts.WithMultiLabel(true).WithMaxCategories(2) })
		}},
		{"Classify with one category", false, func(ctx context.Context) error {
			return classify(ctx, func(opts *schemaflux.ClassifyOptions) { opts.Categories = []string{"billing"} })
		}},
		{"Classify with a category given twice", false, func(ctx context.Context) error {
			return classify(ctx, func(opts *schemaflux.ClassifyOptions) { opts.Categories = []string{"billing", "technical", "billing "} })
		}},
		{"Rate over more levels than the decider takes", true, func(ctx context.Context) error {
			_, err := schemaflux.Rate(ctx, refundMessage, "How urgent is this?", 1, 100)
			return err
		}},
		{"Rate over eleven levels", true, func(ctx context.Context) error {
			_, err := schemaflux.Rate(ctx, refundMessage, "How urgent is this?", 0, 10)
			return err
		}},
		{"Which among more options than the decider takes", true, func(ctx context.Context) error {
			_, err := schemaflux.Which(ctx, refundMessage, "Which label?", many)
			return err
		}},
		{"Is about a subject longer than the decider's context", true, func(ctx context.Context) error {
			_, err := schemaflux.Is(ctx, strings.Repeat("a long message. ", 400), "Is it long?")
			return err
		}},
		{"Choose among more options than the decider takes", true, func(ctx context.Context) error {
			_, err := schemaflux.Choose(many, schemaflux.NewChooseOptions().WithContext(ctx))
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "")

			// The request with no decider anywhere.
			alone := schemafluxtest.New().As("openai").Shaped()
			aloneCtx := schemaflux.NewClient("test-key").WithModel("gpt-6-luna").WithProviderInstance(alone).Context(context.Background())
			_ = tc.run(aloneCtx)
			if alone.CallCount() == 0 {
				t.Fatal("the operation sent nothing")
			}

			// The same call with a decider that takes ten options, ten levels
			// and a state of a thousand tokens.
			generator := schemafluxtest.New().As("openai").Shaped()
			decider := schemafluxtest.NewDecider().As("jev", "jev-1.13.0").
				Limits(schemaflux.DecisionLimits{MaxOptions: 10, MaxLevels: 10, MaxStateTokens: 1000}).
				Answer(schemafluxtest.Yes(0.99))
			pairCtx := schemaflux.NewClient("test-key").WithModel("gpt-6-luna").
				WithProviderInstance(mw.Decisions(generator, decider, mw.DecisionOptions{})).Context(context.Background())
			_ = tc.run(pairCtx)

			if decider.CallCount() != 0 {
				t.Errorf("the decider was sent %d requests it cannot answer", decider.CallCount())
			}
			if generator.CallCount() != alone.CallCount() {
				t.Fatalf("the generator got %d requests, and %d with no decider", generator.CallCount(), alone.CallCount())
			}
			for i, want := range alone.Requests() {
				got := generator.Requests()[i]
				if (got.Decision != nil) != tc.decision {
					t.Errorf("request %d carries a decision: %v, want %v", i+1, got.Decision != nil, tc.decision)
				}
				if got.SystemPrompt != want.SystemPrompt || got.UserPrompt != want.UserPrompt {
					t.Errorf("request %d is not the one the generator gets today\n got %q\n     %q\nwant %q\n     %q", i+1, got.SystemPrompt, got.UserPrompt, want.SystemPrompt, want.UserPrompt)
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("request %d differs from the one sent with no decider:\n got %+v\nwant %+v", i+1, got, want)
				}
			}
		})
	}
}

// A decision provider used alone, over its real protocol on a loopback
// stand-in: decisions work, and what it cannot write is refused before
// anything is sent.
func TestIntegrationADecisionProviderAloneRefusesWhatItCannotWrite(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "")
	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"answers":{"q1":{"type":"noul","noul":0.93}},"model":"jev-1.13.0","usage":{"input_tokens":310,"output_tokens":20}}`)
	}))
	defer server.Close()
	sent := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(bodies)
	}

	decider, err := schemaflux.CreateProvider("jev", schemaflux.ProviderConfig{APIKey: "test-decider-key", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	client := schemaflux.NewClient("test-key").WithProviderInstance(decider).WithModel("jev-latest")
	ctx := client.Context(context.Background())

	result, err := schemaflux.IsResult(ctx, refundMessage, "Is the customer asking for a refund?")
	if err != nil || !result.Value.Answer || math.Abs(result.Value.ModelConfidence-0.93) > 1e-9 {
		t.Fatalf("IsResult = %+v, %v", result.Value, err)
	}
	if sent() != 1 || !strings.Contains(bodies[0], `"type":"noul"`) || strings.Contains(bodies[0], "You answer questions") {
		t.Fatalf("%d requests; the body is %s", sent(), bodies)
	}
	if result.Meta.Provider != "jev" || result.Meta.Model != "jev-1.13.0" || !result.Meta.Cost.Priced {
		t.Errorf("the envelope names %s/%s, priced %v", result.Meta.Provider, result.Meta.Model, result.Meta.Cost.Priced)
	}
	if want := 310 * 0.042 / 1_000_000; math.Abs(result.Meta.Cost.TotalCost-want) > 1e-12 {
		t.Errorf("cost %v, want %v: 310 input tokens at the decision model's rate", result.Meta.Cost.TotalCost, want)
	}

	type invoice struct {
		Number string `json:"number"`
	}
	refused := map[string]func() error{
		"Extract": func() error {
			_, err := schemaflux.Extract[invoice](goldenText, extractOn(ctx))
			return err
		},
		"Summarize": func() error {
			_, err := schemaflux.Summarize(goldenText, summarizeOn(ctx))
			return err
		},
		"Classify into several labels": func() error {
			opts := schemaflux.NewClassifyOptions().WithCategories([]string{"billing", "technical"}).WithMultiLabel(true)
			opts.CommonOptions.Context = ctx
			_, err := schemaflux.Classify[string, string](refundMessage, opts)
			return err
		},
		"Rate over a hundred levels": func() error {
			_, err := schemaflux.Rate(ctx, refundMessage, "How urgent?", 1, 100)
			return err
		},
	}
	for name, run := range refused {
		t.Run(name, func(t *testing.T) {
			before := sent()
			err := run()
			if schemaflux.KindOf(err) != types.KindUnsupportedCapability {
				t.Errorf("error %v (kind %v), want an unsupported capability", err, schemaflux.KindOf(err))
			}
			if sent() != before {
				t.Errorf("%d requests were sent for something the provider cannot answer", sent()-before)
			}
			if err != nil && (strings.Contains(err.Error(), "test-decider-key") || strings.Contains(err.Error(), "Northwind")) {
				t.Errorf("the error contains the key or the caller's text: %v", err)
			}
		})
	}
}

// The route and the fallback, with what each call cost: above the floor one
// call, below it two, each priced at its own model's rate, and both in the
// envelope, on the client's budget and in the decision record.
func TestIntegrationBelowTheFloorBothCallsArePaidForAndTheRecordSaysWhoAnswered(t *testing.T) {
	million := schemaflux.TokenUsage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}
	const deciderCost, generatorCost = 0.042, 0.10 // per million input tokens: jev-1.13.0, gpt-6-luna

	cases := []struct {
		name       string
		confidence float64
		floor      float64
		answer     bool
		answeredBy string
		typed      bool
		attempts   int
		cost       float64
		tokens     int
		askedFirst string
		escalated  string
	}{
		{"above the floor one call is made", 0.96, 0.7, true, "jev", true, 1, deciderCost, 1_000_000, "", ""},
		{"with no floor one call is made whatever it says", 0.2, 0, true, "jev", true, 1, deciderCost, 1_000_000, "", ""},
		{"below the floor two are made", 0.55, 0.7, false, "openai", false, 2, deciderCost + generatorCost, 2_000_000, "jev", "below the floor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "")
			generator := schemafluxtest.New().As("openai").Reply(`{"q1":false,"confidence":0.9}`).WithUsage(million)
			decider := schemafluxtest.NewDecider().As("jev", "jev-1.13.0").Answer(schemafluxtest.Yes(tc.confidence)).WithUsage(million)
			var records []schemaflux.DecisionRecord
			client := schemaflux.NewClient("test-key").WithModel("gpt-6-luna").WithBudget(10).
				WithDecisionObserver(func(_ context.Context, record schemaflux.DecisionRecord) { records = append(records, record) }).
				WithProviderInstance(mw.Decisions(generator, decider, mw.DecisionOptions{Floor: tc.floor}))

			result, err := schemaflux.IsResult(client.Context(context.Background()), refundMessage, "Is the customer asking for a refund?")
			if err != nil {
				t.Fatalf("IsResult: %v", err)
			}
			if result.Value.Answer != tc.answer {
				t.Errorf("the answer is %v, want %v, the answer of %s", result.Value.Answer, tc.answer, tc.answeredBy)
			}
			if result.Meta.Provider != tc.answeredBy || result.Meta.Attempts != tc.attempts {
				t.Errorf("the envelope says %s answered after %d attempts, want %s after %d", result.Meta.Provider, result.Meta.Attempts, tc.answeredBy, tc.attempts)
			}
			if result.Meta.Usage.PromptTokens != tc.tokens {
				t.Errorf("usage of %d input tokens, want %d", result.Meta.Usage.PromptTokens, tc.tokens)
			}
			if !result.Meta.Cost.Priced || math.Abs(result.Meta.Cost.TotalCost-tc.cost) > 1e-9 {
				t.Errorf("cost %v (priced %v), want %v: each call at its own model's rate", result.Meta.Cost.TotalCost, result.Meta.Cost.Priced, tc.cost)
			}
			if spent, complete := client.Spent(); !complete || math.Abs(spent-tc.cost) > 1e-9 {
				t.Errorf("the client's budget was charged %v (complete %v), want %v", spent, complete, tc.cost)
			}
			if len(records) != 1 {
				t.Fatalf("%d decision records", len(records))
			}
			record := records[0]
			if record.AnsweredBy != tc.answeredBy || record.Typed != tc.typed || record.AskedFirst != tc.askedFirst || record.Escalated != tc.escalated {
				t.Errorf("the decision record says %+v", record)
			}
			if tc.askedFirst != "" && (record.AskedFirstModelConfidence == nil || math.Abs(*record.AskedFirstModelConfidence-tc.confidence) > 1e-9) {
				t.Errorf("the record does not carry the probability of the answer that was not used: %v", record.AskedFirstModelConfidence)
			}
			if tc.askedFirst == "" && record.AskedFirstModelConfidence != nil {
				t.Errorf("the record carries a probability for a provider that was not passed over")
			}
		})
	}
}

func TestIntegrationADeciderThatIsDownOrRefusesAndAGeneratorThatThenFails(t *testing.T) {
	down := llm.NewAPIError("jev", "jev-latest", 503, "")
	million := schemaflux.TokenUsage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}
	build := func(t *testing.T, options mw.DecisionOptions) (*schemafluxtest.Provider, *schemafluxtest.Decider, *schemaflux.Client, *[]schemaflux.DecisionRecord) {
		configtest.Setenv(t, "SCHEMAFLUX_MODEL", "")
		generator := schemafluxtest.New().As("openai").Reply(`{"q1":true}`).WithUsage(million)
		decider := schemafluxtest.NewDecider().As("jev", "jev-1.13.0").WithUsage(million)
		records := &[]schemaflux.DecisionRecord{}
		client := schemaflux.NewClient("test-key").WithModel("gpt-6-luna").WithBudget(10).WithRetries(0).
			WithDecisionObserver(func(_ context.Context, record schemaflux.DecisionRecord) { *records = append(*records, record) }).
			WithProviderInstance(mw.Decisions(generator, decider, options))
		return generator, decider, client, records
	}

	t.Run("a decider that is down", func(t *testing.T) {
		generator, decider, client, records := build(t, mw.DecisionOptions{})
		decider.Fail(down)
		got, err := schemaflux.Is(client.Context(context.Background()), refundMessage, "Is it?")
		if err != nil || !got {
			t.Fatalf("Is = %v, %v", got, err)
		}
		if decider.CallCount() != 1 || generator.CallCount() != 1 {
			t.Errorf("%d calls to the decider and %d to the generator", decider.CallCount(), generator.CallCount())
		}
		if record := (*records)[0]; record.AnsweredBy != "openai" || record.AskedFirst != "jev" || record.Escalated != "unavailable" || record.AskedFirstModelConfidence != nil {
			t.Errorf("the decision record says %+v", record)
		}
	})
	t.Run("a decider that is down, with failover off", func(t *testing.T) {
		generator, decider, client, _ := build(t, mw.DecisionOptions{NoFailover: true})
		decider.Fail(down)
		if got, err := schemaflux.Is(client.Context(context.Background()), refundMessage, "Is it?"); err == nil || got || llm.Classify(err) != types.KindProviderUnavailable {
			t.Fatalf("Is = %v, %v", got, err)
		}
		if generator.CallCount() != 0 {
			t.Errorf("the generator was asked %d times", generator.CallCount())
		}
	})
	t.Run("a decider that refuses", func(t *testing.T) {
		generator, decider, client, records := build(t, mw.DecisionOptions{NoFailover: true})
		// It declares no limits to the pair, and refuses when it is asked.
		refusing := refusingDecider{decider}
		client = client.WithProviderInstance(mw.Decisions(generator, refusing, mw.DecisionOptions{NoFailover: true}))
		got, err := schemaflux.Is(client.Context(context.Background()), refundMessage, "Is it?")
		if err != nil || !got {
			t.Fatalf("Is = %v, %v", got, err)
		}
		if record := (*records)[0]; record.AnsweredBy != "openai" || record.AskedFirst != "refusing" || record.Escalated != "unsupported" {
			t.Errorf("the decision record says %+v", record)
		}
	})
	t.Run("a generator that fails after a low-confidence answer", func(t *testing.T) {
		generator, decider, client, records := build(t, mw.DecisionOptions{Floor: 0.7})
		decider.Answer(schemafluxtest.Yes(0.4))
		broken := llm.NewAPIError("openai", "gpt-6-luna", 400, "")
		generator.Fail(broken)
		got, err := schemaflux.IsResult(client.Context(context.Background()), refundMessage, "Is it?")
		if !errors.Is(err, error(broken)) && schemaflux.KindOf(err) != types.KindInvalidRequest {
			t.Fatalf("error %v, want the generator's", err)
		}
		if got.Value.Answer || got.Value.ModelConfidence != 0 {
			t.Errorf("the low-confidence answer came back beside the error: %+v", got.Value)
		}
		if record := (*records)[0]; record.Err == nil || record.AnsweredBy != "" || record.Asked[0].Answer != nil {
			t.Errorf("the decision record of a failure says %+v", record)
		}
		// The decider's call was made and is on the budget.
		if spent, _ := client.Spent(); math.Abs(spent-0.042) > 1e-9 {
			t.Errorf("the client's budget was charged %v, want the decider's 0.042", spent)
		}
	})
}

// refusingDecider refuses every decision as one it cannot take, when asked.
type refusingDecider struct{ *schemafluxtest.Decider }

func (refusingDecider) Name() string { return "refusing" }
func (refusingDecider) Complete(context.Context, schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	return schemaflux.CompletionResponse{}, (schemaflux.DecisionLimits{MaxQuestions: -1}).Check("refusing", "m", nil)
}

func TestIntegrationADataPolicyHoldsForBothRoutesOfADecision(t *testing.T) {
	us := []string{"us"}
	cases := []struct {
		name       string
		allowed    []string
		toDecider  int
		toGenerate int
		wantErr    bool
	}{
		{"both routes allowed", []string{"openai", "jev"}, 1, 0, false},
		{"only the generator's route allowed", []string{"openai"}, 0, 1, false},
		{"only the decider's route allowed", []string{"jev"}, 0, 0, true},
		{"neither allowed", []string{"anthropic"}, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "")
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			schemaflux.DeclareRoute("openai", "gpt-6-luna", us, 0)
			schemaflux.DeclareRoute("jev", "jev-1.13.0", us, 0)

			generator := schemafluxtest.New().As("openai").Reply(`{"q1":false}`)
			decider := schemafluxtest.NewDecider().As("jev", "jev-1.13.0").Answer(schemafluxtest.Yes(0.99))
			client := schemaflux.NewClient("test-key").WithModel("gpt-6-luna").
				WithDataPolicy(schemaflux.DataPolicy{AllowedProviders: tc.allowed, AllowedRegions: us}).
				WithProviderInstance(mw.Decisions(generator, decider, mw.DecisionOptions{}))

			got, err := schemaflux.Is(client.Context(context.Background()), refundMessage, "Is it?")
			if tc.wantErr != (err != nil) {
				t.Fatalf("Is = %v, %v", got, err)
			}
			if tc.wantErr && (schemaflux.KindOf(err) != types.KindPolicyViolation || got) {
				t.Errorf("Is = %v, %v, want the policy's refusal", got, err)
			}
			if decider.CallCount() != tc.toDecider || generator.CallCount() != tc.toGenerate {
				t.Errorf("%d requests to the decider and %d to the generator, want %d and %d", decider.CallCount(), generator.CallCount(), tc.toDecider, tc.toGenerate)
			}
			if !tc.wantErr && got != (tc.toDecider == 1) {
				t.Errorf("the answer %v is not the one of the provider that was asked", got)
			}
		})
	}
}

// What a decision costs this library when a decider answers it, beside what it
// costs when a generating provider does. Reported, and held only loosely: the
// figures are for the report, and the budgets that are held are in internal/ops.
func TestIntegrationWhatADecisionThroughADeciderAllocates(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "")
	generator := schemafluxtest.New().As("openai").Reply(`{"q1":true}`)
	decider := schemafluxtest.NewDecider().As("jev", "jev-1.13.0").Answer(schemafluxtest.Yes(0.99))
	alone := schemaflux.NewClient("test-key").WithModel("gpt-6-luna").WithProviderInstance(generator).Context(context.Background())
	paired := schemaflux.NewClient("test-key").WithModel("gpt-6-luna").
		WithProviderInstance(mw.Decisions(schemafluxtest.New().As("openai").Reply(`{"q1":true}`), decider, mw.DecisionOptions{Floor: 0.5})).Context(context.Background())

	run := func(ctx context.Context) func() {
		return func() {
			if got, err := schemaflux.Is(ctx, refundMessage, "Is the customer asking for a refund?"); err != nil || !got {
				t.Fatalf("Is = %v, %v", got, err)
			}
		}
	}
	run(alone)()
	run(paired)()
	// The doubles keep every request they are sent, which is their allocation
	// and not the library's; it is the same on both sides.
	generating := testing.AllocsPerRun(100, run(alone))
	deciding := testing.AllocsPerRun(100, run(paired))
	t.Logf("Is through a generating provider: %.0f allocations; through a decider: %.0f", generating, deciding)
	if deciding > generating+12 {
		t.Errorf("a decision answered by a decider allocates %.0f times, against %.0f by a generating provider", deciding, generating)
	}
}
