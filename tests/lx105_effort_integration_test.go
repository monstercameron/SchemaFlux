package tests

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

func lx105Effort(body map[string]any) (string, bool) {
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok {
		return "", false
	}
	effort, _ := reasoning["effort"].(string)
	return effort, true
}

// LX-105 through the exported API and the real OpenAI provider on gpt-6-luna:
// what reasoning effort a request carries, per operation and per option.
func TestIntegrationReasoningEffortOnTheWire(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gpt-6-luna")

	similar := func(effort schemaflux.Effort) error {
		opts := schemaflux.NewSimilarOptions()
		opts.OpOptions.Effort = effort
		_, err := schemaflux.Similar("a", "b", opts)
		return err
	}
	question := func(effort schemaflux.Effort) error {
		opts := schemaflux.NewQuestionOptions("What is the total?")
		opts.OpOptions.Effort = effort
		_, err := schemaflux.Question[string, string]("12 units at $3.50", opts)
		return err
	}
	deduplicate := func(effort schemaflux.Effort) error {
		_, err := schemaflux.Deduplicate([]string{"a", "b"}, 0.8, schemaflux.OpOptions{Effort: effort})
		return err
	}
	summarize := func(effort schemaflux.Effort) error {
		opts := schemaflux.NewSummarizeOptions()
		opts.CommonOptions = opts.CommonOptions.WithEffort(effort)
		_, err := schemaflux.Summarize("a long report", opts)
		return err
	}
	classify := func(effort schemaflux.Effort) error {
		opts := schemaflux.NewClassifyOptions().WithCategories([]string{"billing", "technical"})
		opts.OpOptions.Effort = effort
		_, err := schemaflux.Classify[string, string]("My invoice is wrong", opts)
		return err
	}
	classifyAmounts := func(effort schemaflux.Effort) error {
		opts := schemaflux.NewClassifyOptions().WithCategories([]string{"large", "small"})
		opts.CategoryDescriptions = map[string]string{"large": "line items total more than $500"}
		opts.OpOptions.Effort = effort
		_, err := schemaflux.Classify[string, string]("3 x $129.99 and 2 x $55.02", opts)
		return err
	}
	similarNumbers := func(effort schemaflux.Effort) error {
		opts := schemaflux.NewSimilarOptions()
		opts.OpOptions.Effort = effort
		_, err := schemaflux.Similar(map[string]any{"qty": 12, "unit_price": 18.5}, map[string]any{"total": 222}, opts)
		return err
	}
	const (
		classifyAnswer = `{"category":"billing","confidence":0.9,"reasoning":"r"}`
		largeAnswer    = `{"category":"large","confidence":0.9,"reasoning":"r"}`
		similarAnswer  = `{"score":0.9,"explanation":"x"}`
		questionAnswer = `{"answer":"42","confidence":0.9,"reasoning":"r","evidence":[]}`
		dedupAnswer    = `{"duplicates":[]}`
		summaryAnswer  = `a short summary`
	)

	cases := []struct {
		name    string
		run     func(schemaflux.Effort) error
		answer  string
		effort  schemaflux.Effort
		want    string
		sent    bool
		wantErr error
	}{
		{"Classify prefers none", classify, classifyAnswer, schemaflux.EffortDefault, "none", true, nil},
		{"Classify with the caller's low", classify, classifyAnswer, schemaflux.EffortLow, "low", true, nil},
		// A number in play is a sum waiting to happen, and with reasoning off
		// the model got sums right 13 times in 24. The preference is dropped.
		{"Classify with amounts to add up prefers nothing", classifyAmounts, largeAnswer, schemaflux.EffortDefault, "", false, nil},
		{"Classify with amounts and the caller's none", classifyAmounts, largeAnswer, schemaflux.EffortNone, "none", true, nil},
		{"Similar with numbers to compare prefers nothing", similarNumbers, similarAnswer, schemaflux.EffortDefault, "", false, nil},
		{"Similar prefers none", similar, similarAnswer, schemaflux.EffortDefault, "none", true, nil},
		{"Similar with the caller's low", similar, similarAnswer, schemaflux.EffortLow, "low", true, nil},
		{"Similar with the caller's high", similar, similarAnswer, schemaflux.EffortHigh, "high", true, nil},
		{"Deduplicate prefers none", deduplicate, dedupAnswer, schemaflux.EffortDefault, "none", true, nil},
		{"Deduplicate with the caller's medium", deduplicate, dedupAnswer, schemaflux.EffortMedium, "medium", true, nil},
		// Question may be asked to compute, so it prefers nothing and the
		// request is what it was.
		{"Question sets nothing", question, questionAnswer, schemaflux.EffortDefault, "", false, nil},
		{"Question with the caller's none", question, questionAnswer, schemaflux.EffortNone, "none", true, nil},
		{"Summarize sets nothing", summarize, summaryAnswer, schemaflux.EffortDefault, "", false, nil},
		{"Summarize with WithEffort", summarize, summaryAnswer, schemaflux.EffortLow, "low", true, nil},
		// "minimal" is a word gpt-5 takes and gpt-6 does not. The API would
		// fail the request; it is refused here first, and nothing is sent.
		{"a value the model does not take", similar, similarAnswer, schemaflux.EffortMinimal, "", false, schemaflux.ErrInvalidRequest},
		{"a misspelt value", question, questionAnswer, schemaflux.Effort("hgih"), "", false, schemaflux.ErrInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(tc.answer))

			err := tc.run(tc.effort)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if len(*captured) != 0 {
					t.Fatalf("a refused request was sent")
				}
				return
			}
			if err != nil {
				t.Fatalf("the call failed: %v", err)
			}
			if len(*captured) != 1 {
				t.Fatalf("%d requests were sent, want 1", len(*captured))
			}
			got, sent := lx105Effort((*captured)[0])
			if sent != tc.sent || got != tc.want {
				t.Fatalf("reasoning on the wire = %q (present %v), want %q (present %v)", got, sent, tc.want, tc.sent)
			}
		})
	}
}

// A preference is dropped where it cannot apply: the same call on a model that
// does not reason carries no reasoning block and succeeds.
func TestIntegrationAPreferredEffortIsDroppedWhereItCannotApply(t *testing.T) {
	// gpt-6-sol and gpt-5.6-luna accept the word and were not measured with it,
	// and the second is on record failing arithmetic with it.
	for _, model := range []string{"gpt-4o", "a-model-nobody-has-heard-of", "gpt-6-sol", "gpt-6.1-sol", "gpt-5.6-luna"} {
		t.Run(model, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", model)
			_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(`{"score":0.9,"explanation":"x"}`))

			if _, err := schemaflux.Similar("a", "b", schemaflux.NewSimilarOptions()); err != nil {
				t.Fatalf("Similar: %v", err)
			}
			if _, sent := lx105Effort((*captured)[0]); sent {
				t.Errorf("a reasoning block was sent to %s", model)
			}
		})
	}
}

// ExampleEffort sets how much the model reasons for one call. Reasoning off is
// the fast setting for a judgment; a question that needs arithmetic asks for
// more. It runs under go test with a scripted provider: no credential, no
// spend.
func ExampleEffort() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(
		`{"answer": 880.6, "confidence": 0.95, "reasoning": "10% off, then 8% tax", "evidence": []}`))

	opts := schemaflux.NewQuestionOptions("What is the total after a 10% discount and 8% tax?")
	opts.OpOptions.Effort = schemaflux.EffortLow

	result, err := schemaflux.Question[string, float64](
		"12 units at $18.50, 7 units at $42.00, 3 units at $129.99", opts)
	if err != nil {
		fmt.Println("the question could not be answered:", err)
		return
	}
	fmt.Printf("total: %.2f\n", result.Answer)

	// Output:
	// total: 880.60
}

// LX-021 through the exported API: items that are written the same are grouped
// without asking, and only the distinct ones are sent.
func TestIntegrationDeduplicateCollapsesIdenticalItems(t *testing.T) {
	t.Run("identical items the model says nothing about", func(t *testing.T) {
		_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(`{"duplicates":[]}`))

		result, err := schemaflux.Deduplicate([]string{"refund request", "login failure", "refund request"}, 0.8)
		if err != nil {
			t.Fatalf("Deduplicate: %v", err)
		}
		if !reflect.DeepEqual(result.UniqueIndices, []int{0, 1}) || !reflect.DeepEqual(result.DuplicateIndices, [][]int{{0, 2}}) {
			t.Errorf("UniqueIndices = %v, DuplicateIndices = %v", result.UniqueIndices, result.DuplicateIndices)
		}
		if len(*captured) != 1 {
			t.Fatalf("%d requests were sent, want 1", len(*captured))
		}
	})

	t.Run("nothing is sent when every item is the same", func(t *testing.T) {
		_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(`not json`))

		result, err := schemaflux.Deduplicate([]string{"same", "same", "same"}, 0.8)
		if err != nil {
			t.Fatalf("Deduplicate: %v", err)
		}
		if len(*captured) != 0 {
			t.Errorf("%d requests were sent for a list with one distinct item", len(*captured))
		}
		if result.TotalRemoved != 2 || len(result.Unique) != 1 {
			t.Errorf("Unique = %v, TotalRemoved = %d", result.Unique, result.TotalRemoved)
		}
	})
}

// LX-061 through the exported API, with the client set where the fluent
// builders and WithContext put it: on CommonOptions. The first exported-API
// test of this set it on the embedded half, which the old code did read, so it
// passed with the defect in place.
func TestRootStreamsUseTheClientSetOnCommonOptions(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")

	mine := schemaflux.NewClient("").WithProviderInstance(&fakeStreamingProvider{deltas: []string{"from ", "my client"}})
	// Built second, so it is the process-wide default.
	_ = schemaflux.NewClient("").WithProviderInstance(&fakeStreamingProvider{deltas: []string{"from the other client"}})

	opts := schemaflux.NewSummarizeOptions()
	opts.CommonOptions.Context = mine.Context(context.Background())
	stream, err := schemaflux.StreamSummarize("a long report", opts)
	if err != nil {
		t.Fatalf("StreamSummarize: %v", err)
	}
	if got := drainStream(t, stream); got != "from my client" {
		t.Errorf("the stream came %q, want it from the client on the context", got)
	}

	cancelled, cancel := context.WithCancel(mine.Context(context.Background()))
	cancel()
	opts.CommonOptions.Context = cancelled
	stream, err = schemaflux.StreamSummarize("a long report", opts)
	if stream != nil || !errors.Is(err, schemaflux.ErrCanceled) {
		t.Errorf("stream opened = %v, err = %v; want no stream and a cancellation", stream != nil, err)
	}
}
