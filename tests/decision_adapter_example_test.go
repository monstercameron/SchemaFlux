package tests

import (
	"context"
	"fmt"
	"strings"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/mw"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

// keywordDecider is an adapter for a decision API, written against the
// exported names only: it implements Provider, reads the typed decision off the
// request, and answers typed. This one decides in memory, by looking for words.
// One for a real API puts its HTTP call where the loop is.
type keywordDecider struct{}

func (keywordDecider) Name() string                                      { return "keywords" }
func (keywordDecider) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (keywordDecider) RetryPolicy() (int, time.Duration)                 { return 0, 0 }
func (keywordDecider) DecisionLimits() schemaflux.DecisionLimits {
	return schemaflux.DecisionLimits{MaxOptions: 16}
}

func (d keywordDecider) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	if err := d.DecisionLimits().Check(d.Name(), "keywords-1", req.Decision); err != nil {
		return schemaflux.CompletionResponse{}, err // no decision, or one past the limits
	}
	state := strings.ToLower(req.Decision.State)
	answers := make([]schemaflux.DecisionAnswer, len(req.Decision.Questions))
	for i, question := range req.Decision.Questions {
		answers[i] = schemaflux.DecisionAnswer{Kind: question.Kind, ModelConfidence: 0.9, ConfidenceReported: true}
		switch question.Kind {
		case schemaflux.DecisionYesNo:
			answers[i].Yes = strings.Contains(state, "refund")
		case schemaflux.DecisionChoice:
			answers[i].Label = question.Options[0]
			for _, label := range question.Options {
				if strings.Contains(state, strings.ToLower(label)) {
					answers[i].Label = label
				}
			}
		case schemaflux.DecisionScale:
			answers[i].Level = question.High
		}
	}
	return schemaflux.CompletionResponse{Provider: d.Name(), Model: "keywords-1", DecisionAnswers: answers}, nil
}

// Example_decisionAdapter pairs an adapter for a decision API with a generating
// provider. The decisions go to the adapter; the summary, which a decision API
// cannot write, goes to the generating provider.
func Example_decisionAdapter() {
	generator := schemafluxtest.New().Reply(`{"summary":"A customer wants a refund for a late shipping order.","key_points":[],"confidence":0.9}`)
	client := schemaflux.NewClient("example-key").
		WithProviderInstance(mw.Decisions(generator, keywordDecider{}, mw.DecisionOptions{}))
	ctx := client.Context(context.Background())

	type triage struct {
		Refund  bool   `ask:"Is the customer asking for a refund?"`
		Team    string `ask:"Which team should handle this?" oneof:"billing,technical,shipping"`
		Urgency int    `ask:"How urgent is this?" scale:"1-5"`
	}
	message := "My shipping was late. Please refund the order."

	result, err := schemaflux.AssessResult[triage](ctx, message)
	if err != nil {
		fmt.Println("assess failed:", err)
		return
	}
	fmt.Printf("%+v\n", result.Value.Answer)
	fmt.Printf("answered by %s, which reported %.1f\n", result.Meta.Provider, result.Value.ModelConfidence)
	fmt.Println("requests to the generating provider:", generator.CallCount())

	opts := schemaflux.NewSummarizeOptions()
	opts.CommonOptions.Context = ctx
	if _, err := schemaflux.Summarize(message, opts); err != nil {
		fmt.Println("summarize failed:", err)
		return
	}
	fmt.Println("requests to the generating provider:", generator.CallCount())

	// Output:
	// {Refund:true Team:shipping Urgency:5}
	// answered by keywords, which reported 0.9
	// requests to the generating provider: 0
	// requests to the generating provider: 1
}
