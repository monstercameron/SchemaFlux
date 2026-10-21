package mw

import (
	"context"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/types"
	"github.com/monstercameron/schemaflux/v2/provider"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// pair is a generating provider and a decision provider, named, scripted to
// answer a yes-or-no with the answers given.
func pair(answers ...schemaflux.DecisionAnswer) (*schemafluxtest.Provider, *schemafluxtest.Decider) {
	generator := new(schemafluxtest.Provider)
	generator.SetName("generator")
	generator.Reply(`{"answer":"generated"}`)
	decider := schemafluxtest.NewDecider(answers...)
	decider.SetName("decider")
	return generator, decider
}

func TestDecisions(t *testing.T) {
	t.Parallel()

	t.Run("a decision goes to the decision provider, which answers it typed, and the generator is not asked", func(t *testing.T) {
		t.Parallel()
		generator, decider := pair(schemafluxtest.Yes(0.93))
		resp, err := Decisions(generator, decider, DecisionOptions{}).Complete(t.Context(), deciding(yesNo(`"x"`, "is it?")))
		if err != nil {
			t.Fatal(err)
		}
		want := []schemaflux.DecisionAnswer{{Kind: schemaflux.DecisionYesNo, Yes: true, ModelConfidence: 0.93, ConfidenceReported: true}}
		if !slices.Equal(resp.Answers, want) || resp.Escalation != nil || resp.Provider != "decider" {
			t.Errorf("answers %+v, escalation %+v, provider %q", resp.Answers, resp.Escalation, resp.Provider)
		}
		if generator.Calls() != 0 || decider.Calls() != 1 {
			t.Errorf("the generator was asked %d times and the decider %d, want 0 and 1", generator.Calls(), decider.Calls())
		}
	})

	t.Run("a request with no decision goes to the generator and the decider is not asked", func(t *testing.T) {
		t.Parallel()
		generator, decider := pair()
		resp, err := Decisions(generator, decider, DecisionOptions{}).Complete(t.Context(), ask("summarize"))
		if err != nil || resp.Content != `{"answer":"generated"}` {
			t.Fatalf("%q, %v", resp.Content, err)
		}
		if decider.Calls() != 0 {
			t.Errorf("the decider was asked %d times, want 0", decider.Calls())
		}
	})

	t.Run("a decision with no questions goes to the generator", func(t *testing.T) {
		t.Parallel()
		generator, decider := pair()
		if _, err := Decisions(generator, decider, DecisionOptions{}).Complete(t.Context(), deciding(&schemaflux.Decision{State: `"x"`})); err != nil {
			t.Fatal(err)
		}
		if generator.Calls() != 1 || decider.Calls() != 0 {
			t.Errorf("the generator was asked %d times and the decider %d, want 1 and 0", generator.Calls(), decider.Calls())
		}
	})

	t.Run("an answer below the floor is passed over, both calls are made, and the response says why", func(t *testing.T) {
		t.Parallel()
		generator, decider := pair(schemafluxtest.Yes(0.4))
		resp, err := Decisions(generator, decider, DecisionOptions{Floor: 0.7, Model: "fast-model"}).Complete(t.Context(), deciding(yesNo(`"x"`, "is it?")))
		if err != nil {
			t.Fatal(err)
		}
		if resp.Content != `{"answer":"generated"}` || len(resp.Answers) != 0 {
			t.Errorf("content %q, answers %+v: the generator's answer was expected alone", resp.Content, resp.Answers)
		}
		want := schemaflux.DecisionEscalation{Provider: "decider", Model: "fast-model", Reason: "below the floor", ModelConfidence: 0.4, ConfidenceReported: true}
		if resp.Escalation == nil || *resp.Escalation != want {
			t.Errorf("escalation = %+v, want %+v", resp.Escalation, want)
		}
		if generator.Calls() != 1 || decider.Calls() != 1 {
			t.Errorf("the generator was asked %d times and the decider %d, want 1 each", generator.Calls(), decider.Calls())
		}
	})

	t.Run("with no floor the decision provider's answer is the answer whatever it says of itself", func(t *testing.T) {
		t.Parallel()
		generator, decider := pair(schemafluxtest.Yes(0.01))
		resp, err := Decisions(generator, decider, DecisionOptions{}).Complete(t.Context(), deciding(yesNo(`"x"`, "is it?")))
		if err != nil || !resp.Answers[0].Yes || resp.Escalation != nil || generator.Calls() != 0 {
			t.Errorf("answers %+v, escalation %+v, %v, the generator asked %d times", resp.Answers, resp.Escalation, err, generator.Calls())
		}
	})

	t.Run("an answer with no probability reported cannot clear a floor", func(t *testing.T) {
		t.Parallel()
		generator, decider := pair(schemafluxtest.Yes())
		resp, err := Decisions(generator, decider, DecisionOptions{Floor: 0.1}).Complete(t.Context(), deciding(yesNo(`"x"`, "is it?")))
		if err != nil || resp.Escalation == nil || resp.Escalation.ConfidenceReported || generator.Calls() != 1 {
			t.Errorf("escalation %+v, %v, the generator asked %d times", resp.Escalation, err, generator.Calls())
		}
	})

	t.Run("a probability at the floor clears it and one just under does not", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			probability float64
			passedOver  bool
		}{{0.7, false}, {0.69, true}, {1, false}, {0, true}} {
			generator, decider := pair(schemafluxtest.Yes(tc.probability))
			resp, err := Decisions(generator, decider, DecisionOptions{Floor: 0.7}).Complete(t.Context(), deciding(yesNo(`"x"`, "is it?")))
			if err != nil || (resp.Escalation != nil) != tc.passedOver {
				t.Errorf("probability %v: escalation %+v, %v, want passed over = %v", tc.probability, resp.Escalation, err, tc.passedOver)
			}
		}
	})

	t.Run("a request of several questions is passed over whole when any one answer is under the floor", func(t *testing.T) {
		t.Parallel()
		generator, decider := pair(schemafluxtest.Yes(0.95), schemafluxtest.No(0.5), schemafluxtest.Yes(0.99))
		decision := &schemaflux.Decision{State: `"x"`, Questions: []schemaflux.DecisionQuestion{
			{Kind: schemaflux.DecisionYesNo, Ask: "one?"}, {Kind: schemaflux.DecisionYesNo, Ask: "two?"}, {Kind: schemaflux.DecisionYesNo, Ask: "three?"},
		}}
		resp, err := Decisions(generator, decider, DecisionOptions{Floor: 0.9}).Complete(t.Context(), deciding(decision))
		if err != nil || resp.Escalation == nil || resp.Escalation.ModelConfidence != 0.5 || len(resp.Answers) != 0 {
			t.Errorf("escalation %+v, answers %+v, %v: the lowest probability, 0.5, passes the whole request over", resp.Escalation, resp.Answers, err)
		}
	})

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"unavailable", unavailable()},
		{"rate limited", rateLimited(0)},
		{"a timeout", &schemaflux.Error{Kind: schemaflux.ErrTimeout}},
	} {
		t.Run("a decision provider that is "+tc.name+" in transit sends the decision to the generator", func(t *testing.T) {
			t.Parallel()
			generator, decider := pair()
			decider.Fail(tc.err)
			resp, err := Decisions(generator, decider, DecisionOptions{}).Complete(t.Context(), deciding(yesNo(`"x"`, "is it?")))
			if err != nil || resp.Escalation == nil || resp.Escalation.Reason != "unavailable" || resp.Escalation.Provider != "decider" {
				t.Fatalf("escalation %+v, %v", resp.Escalation, err)
			}
			if resp.Escalation.ConfidenceReported || generator.Calls() != 1 {
				t.Errorf("a failure reports no probability, and the generator was asked %d times", generator.Calls())
			}
		})
		t.Run("with NoFailover a decision provider that is "+tc.name+" in transit fails the request", func(t *testing.T) {
			t.Parallel()
			generator, decider := pair()
			decider.Fail(tc.err)
			_, err := Decisions(generator, decider, DecisionOptions{NoFailover: true}).Complete(t.Context(), deciding(yesNo(`"x"`, "is it?")))
			if _, ok := kindOf(err); !ok || generator.Calls() != 0 {
				t.Errorf("err = %v, the generator was asked %d times, want an error and 0", err, generator.Calls())
			}
		})
	}

	t.Run("a decision the decision provider refuses as one it cannot take goes to the generator, with NoFailover too", func(t *testing.T) {
		t.Parallel()
		for _, noFailover := range []bool{false, true} {
			generator, decider := pair()
			decider.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnsupported})
			resp, err := Decisions(generator, decider, DecisionOptions{NoFailover: noFailover}).Complete(t.Context(), deciding(yesNo(`"x"`, "is it?")))
			if err != nil || resp.Escalation == nil || resp.Escalation.Reason != "unsupported" {
				t.Errorf("NoFailover %v: escalation %+v, %v", noFailover, resp.Escalation, err)
			}
		}
	})

	t.Run("a decision past the decision provider's limits goes to the generator without the decision provider being asked", func(t *testing.T) {
		t.Parallel()
		generator, decider := pair(schemafluxtest.Chose("a"))
		decider.SetLimits(schemaflux.DecisionLimits{MaxOptions: 2})
		resp, err := Decisions(generator, decider, DecisionOptions{}).Complete(t.Context(), deciding(choice("which?", "a", "b", "c")))
		if err != nil || resp.Content == "" || resp.Escalation != nil {
			t.Fatalf("%q, escalation %+v, %v", resp.Content, resp.Escalation, err)
		}
		if decider.Calls() != 0 || generator.Calls() != 1 {
			t.Errorf("the decider was asked %d times and the generator %d, want 0 and 1", decider.Calls(), generator.Calls())
		}
	})

	t.Run("a decision within the limits is sent to the decision provider", func(t *testing.T) {
		t.Parallel()
		generator, decider := pair(schemafluxtest.Chose("b"))
		decider.SetLimits(schemaflux.DecisionLimits{MaxOptions: 3, MaxQuestions: 1})
		resp, err := Decisions(generator, decider, DecisionOptions{}).Complete(t.Context(), deciding(choice("which?", "a", "b", "c")))
		if err != nil || resp.Answers[0].Label != "b" || generator.Calls() != 0 {
			t.Errorf("answers %+v, %v, the generator asked %d times", resp.Answers, err, generator.Calls())
		}
	})

	t.Run("limits are found through a wrapper around the decision provider", func(t *testing.T) {
		t.Parallel()
		generator, decider := pair(schemafluxtest.Chose("a"))
		decider.SetLimits(schemaflux.DecisionLimits{MaxOptions: 2})
		wrapped := Chain(decider, Retry(), Cache())
		if _, err := Decisions(generator, wrapped, DecisionOptions{}).Complete(t.Context(), deciding(choice("which?", "a", "b", "c"))); err != nil {
			t.Fatal(err)
		}
		if decider.Calls() != 0 || generator.Calls() != 1 {
			t.Errorf("the decider was asked %d times and the generator %d, want 0 and 1: its limits were not seen through the wrappers", decider.Calls(), generator.Calls())
		}
	})

	t.Run("a failure of the decision provider that is neither a refusal nor in transit is the request's error", func(t *testing.T) {
		t.Parallel()
		generator, decider := pair()
		decider.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnauthorized})
		_, err := Decisions(generator, decider, DecisionOptions{}).Complete(t.Context(), deciding(yesNo(`"x"`, "is it?")))
		wantKind(t, err, schemaflux.ErrUnauthorized)
		if generator.Calls() != 0 {
			t.Errorf("the generator was asked %d times: a misconfigured pair must not be hidden behind one that works", generator.Calls())
		}
	})

	t.Run("a generator that fails after a low-confidence answer fails the request and that answer is not returned", func(t *testing.T) {
		t.Parallel()
		generator, decider := pair(schemafluxtest.Yes(0.2))
		generator.Fail(&schemaflux.Error{Kind: schemaflux.ErrTimeout})
		resp, err := Decisions(generator, decider, DecisionOptions{Floor: 0.9}).Complete(t.Context(), deciding(yesNo(`"x"`, "is it?")))
		wantKind(t, err, schemaflux.ErrTimeout)
		if len(resp.Answers) != 0 || resp.Escalation != nil {
			t.Errorf("the failed request returned %+v", resp)
		}
	})

	t.Run("a floor that is not a number from 0 to 1 fails every decision before anything is sent", func(t *testing.T) {
		t.Parallel()
		for _, floor := range []float64{-0.1, 1.01, math.NaN(), math.Inf(1)} {
			generator, decider := pair(schemafluxtest.Yes(1))
			_, err := Decisions(generator, decider, DecisionOptions{Floor: floor}).Complete(t.Context(), deciding(yesNo(`"x"`, "is it?")))
			wantKind(t, err, schemaflux.ErrConfiguration)
			if generator.Calls() != 0 || decider.Calls() != 0 {
				t.Errorf("floor %v: %d and %d requests were sent", floor, generator.Calls(), decider.Calls())
			}
		}
	})

	t.Run("the data policy holds for the decision provider's route as for the generator's", func(t *testing.T) {
		t.Parallel()
		generator, decider := pair(schemafluxtest.Yes(1))
		ctx := withPolicy(t.Context(), types.DataPolicy{AllowedProviders: []string{"generator"}, AllowedRegions: []string{"us"}, MaxRetentionDays: 30})
		resp, err := Decisions(generator, decider, DecisionOptions{}).Complete(ctx, deciding(yesNo(`"x"`, "is it?")))
		if err != nil || resp.Escalation != nil || resp.Content == "" {
			t.Fatalf("%q, escalation %+v, %v", resp.Content, resp.Escalation, err)
		}
		if decider.Calls() != 0 {
			t.Errorf("the decision provider was sent a decision the policy keeps from it, %d times", decider.Calls())
		}
	})

	t.Run("a route the policy allows is used", func(t *testing.T) {
		t.Parallel()
		provider.DeclareRoute("mw-decisions-allowed", "test-model", []string{"us"}, 0)
		generator, decider := pair(schemafluxtest.Yes(1))
		decider.SetName("mw-decisions-allowed")
		ctx := withPolicy(t.Context(), types.DataPolicy{AllowedProviders: []string{"mw-decisions-allowed", "generator"}, AllowedRegions: []string{"us"}, MaxRetentionDays: 30})
		resp, err := Decisions(generator, decider, DecisionOptions{}).Complete(ctx, schemaflux.Request{Model: "test-model", User: "u", Decision: yesNo(`"x"`, "is it?")})
		if err != nil || len(resp.Answers) != 1 || decider.Calls() != 1 {
			t.Errorf("answers %+v, %v, the decider asked %d times", resp.Answers, err, decider.Calls())
		}
	})

	t.Run("Model is the model the decision provider is asked under", func(t *testing.T) {
		t.Parallel()
		generator, decider := pair(schemafluxtest.Yes(1))
		req := schemaflux.Request{Model: "the-request-model", User: "u", Decision: yesNo(`"x"`, "is it?")}
		if _, err := Decisions(generator, decider, DecisionOptions{Model: "  decision-model  "}).Complete(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		if got := decider.Last().Model; got != "decision-model" {
			t.Errorf("the decision provider was asked under %q", got)
		}
		generator2, decider2 := pair(schemafluxtest.Yes(1))
		Decisions(generator2, decider2, DecisionOptions{}).Complete(t.Context(), req)
		if got := decider2.Last().Model; got != "the-request-model" {
			t.Errorf("with no Model, the decision provider was asked under %q", got)
		}
	})

	t.Run("the generator is asked the request as it was, with the decision", func(t *testing.T) {
		t.Parallel()
		generator, decider := pair(schemafluxtest.Yes(0.1))
		req := deciding(yesNo(`"x"`, "is it?"))
		if _, err := Decisions(generator, decider, DecisionOptions{Floor: 0.5}).Complete(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		got := generator.Last()
		if got.System != req.System || got.User != req.User || got.Decision == nil || got.Decision.Questions[0].Ask != "is it?" {
			t.Errorf("the generator was sent %+v", got)
		}
	})

	t.Run("a pair with no decision provider is the generator, and with no generator the decision provider", func(t *testing.T) {
		t.Parallel()
		generator, decider := pair()
		if got := Decisions(generator, nil, DecisionOptions{}); got != schemaflux.Provider(generator) {
			t.Errorf("no decider: %v", got)
		}
		if got := Decisions(nil, decider, DecisionOptions{}); got != schemaflux.Provider(decider) {
			t.Errorf("no generator: %v", got)
		}
		if got := Decisions(nil, nil, DecisionOptions{}); got != nil {
			t.Errorf("neither: %v", got)
		}
	})

	t.Run("to the library the pair is its generator: name, unwrap and retry settings", func(t *testing.T) {
		t.Parallel()
		inner := new(schemafluxtest.Provider)
		inner.SetName("generator")
		generator := retrier{Provider: inner, retries: 4, backoff: time.Second}
		_, decider := pair()
		both := Decisions(generator, decider, DecisionOptions{})
		if both.Name() != "generator" {
			t.Errorf("Name = %q", both.Name())
		}
		if both.(provider.Unwrapper).Unwrap() != schemaflux.Provider(generator) {
			t.Errorf("Unwrap = %v", both.(provider.Unwrapper).Unwrap())
		}
		if retries, backoff := both.(provider.Retrier).RetryPolicy(); retries != 4 || backoff != time.Second {
			t.Errorf("RetryPolicy = %d, %v", retries, backoff)
		}
	})

	t.Run("a caller that gave up while the decision provider answered gets the context's error and the generator is not asked", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		generator, _ := pair()
		decider := new(schemafluxtest.Decider)
		decider.SetName("decider")
		decider.Respond(func(int, schemaflux.Decision) ([]schemaflux.DecisionAnswer, error) {
			cancel()
			return []schemaflux.DecisionAnswer{schemafluxtest.Yes(0.1)}, nil
		})
		_, err := Decisions(generator, decider, DecisionOptions{Floor: 0.9}).Complete(ctx, deciding(yesNo(`"x"`, "is it?")))
		wantKind(t, err, schemaflux.ErrCanceled)
		if generator.Calls() != 0 {
			t.Errorf("the generator was asked %d times", generator.Calls())
		}
	})

	t.Run("a refusal for the floor names no text of the decision", func(t *testing.T) {
		t.Parallel()
		generator, decider := pair(schemafluxtest.Yes(1))
		_, err := Decisions(generator, decider, DecisionOptions{Floor: 2}).Complete(t.Context(), deciding(yesNo(`"the secret text `+testEmail+`"`, "is it?")))
		mustNotContain(t, "the error", err.Error(), "secret", testEmail)
	})

	t.Run("the pair is held to a Floor of zero as a floor of none, not a floor of zero percent", func(t *testing.T) {
		t.Parallel()
		generator, decider := pair(schemafluxtest.Yes())
		resp, err := Decisions(generator, decider, DecisionOptions{Floor: 0}).Complete(t.Context(), deciding(yesNo(`"x"`, "is it?")))
		if err != nil || resp.Escalation != nil || generator.Calls() != 0 {
			t.Errorf("escalation %+v, %v, the generator asked %d times: an answer with no probability is still the answer", resp.Escalation, err, generator.Calls())
		}
	})
}

// rawDecider is a decision provider that gives the answers it was given whatever
// the request asks, which the scripted decider of schemafluxtest refuses to do:
// it is how a provider that answers wrongly is made.
type rawDecider struct{ answers []schemaflux.DecisionAnswer }

func (d rawDecider) Complete(context.Context, schemaflux.Request) (schemaflux.Response, error) {
	return schemaflux.Response{Answers: d.answers, Model: "raw", Provider: "decider"}, nil
}
func (rawDecider) Name() string { return "decider" }
func (rawDecider) DecisionLimits() schemaflux.DecisionLimits {
	return schemaflux.DecisionLimits{MaxQuestions: 10, MaxOptions: 10, MaxLevels: 10, MaxStateTokens: 1000}
}

// What the decision provider answers is checked against the questions by the pair
// before a probability is read from it, so a program that uses the Provider
// directly, with no client above it to check, is never handed an answer that is
// not an answer to its decision.
func TestDecisionsDoesNotPassOnAnAnswerThatIsNotAnAnswerToTheDecision(t *testing.T) {
	t.Parallel()

	questions := []schemaflux.DecisionQuestion{
		{Kind: schemaflux.DecisionYesNo, Ask: "Is it?"},
		{Kind: schemaflux.DecisionChoice, Ask: "Which?", Options: []string{"red", "green"}},
		{Kind: schemaflux.DecisionScale, Ask: "How?", Low: 1, High: 5},
	}
	good := []schemaflux.DecisionAnswer{
		{Kind: schemaflux.DecisionYesNo, Yes: true, ModelConfidence: 0.9, ConfidenceReported: true},
		{Kind: schemaflux.DecisionChoice, Label: "green", ModelConfidence: 0.9, ConfidenceReported: true},
		{Kind: schemaflux.DecisionScale, Level: 4, ModelConfidence: 0.9, ConfidenceReported: true},
	}
	with := func(change func(a []schemaflux.DecisionAnswer) []schemaflux.DecisionAnswer) []schemaflux.DecisionAnswer {
		return change(slices.Clone(good))
	}
	cases := []struct {
		name    string
		answers []schemaflux.DecisionAnswer
		valid   bool
	}{
		{"every answer valid", good, true},
		{"too few", good[:1], false},
		{"too many", append(slices.Clone(good), good[0]), false},
		{"a label that was not offered", with(func(a []schemaflux.DecisionAnswer) []schemaflux.DecisionAnswer { a[1].Label = "purple"; return a }), false},
		{"the wrong kind", with(func(a []schemaflux.DecisionAnswer) []schemaflux.DecisionAnswer {
			a[1].Kind = schemaflux.DecisionYesNo
			return a
		}), false},
		{"a level above the scale", with(func(a []schemaflux.DecisionAnswer) []schemaflux.DecisionAnswer { a[2].Level = 99; return a }), false},
		{"a level below the scale", with(func(a []schemaflux.DecisionAnswer) []schemaflux.DecisionAnswer { a[2].Level = 0; return a }), false},
		{"a probability above one", with(func(a []schemaflux.DecisionAnswer) []schemaflux.DecisionAnswer { a[0].ModelConfidence = 7; return a }), false},
		{"a negative probability", with(func(a []schemaflux.DecisionAnswer) []schemaflux.DecisionAnswer { a[0].ModelConfidence = -1; return a }), false},
		{"a probability that is not a number", with(func(a []schemaflux.DecisionAnswer) []schemaflux.DecisionAnswer {
			a[0].ModelConfidence = math.NaN()
			return a
		}), false},
	}
	for _, tc := range cases {
		for _, floor := range []float64{0, 0.5} {
			t.Run(fmt.Sprintf("%s escalates at floor %v", tc.name, floor), func(t *testing.T) {
				t.Parallel()
				generator, _ := pair()
				decider := rawDecider{tc.answers}
				resp, err := Decisions(generator, decider, DecisionOptions{Floor: floor}).Complete(t.Context(), deciding(&schemaflux.Decision{State: `"x"`, Questions: questions}))
				if err != nil {
					t.Fatalf("Complete: %v", err)
				}
				if tc.valid {
					if generator.Calls() != 0 || !slices.Equal(resp.Answers, tc.answers) || resp.Escalation != nil {
						t.Errorf("a valid answer was not returned: %d generator calls, %+v, escalation %+v", generator.Calls(), resp.Answers, resp.Escalation)
					}
					return
				}
				if generator.Calls() != 1 {
					t.Errorf("%d calls to the generator, want 1", generator.Calls())
				}
				if len(resp.Answers) != 0 || resp.Content != `{"answer":"generated"}` {
					t.Errorf("the generator's answer was not the one returned: %+v", resp)
				}
				if resp.Escalation == nil || resp.Escalation.Reason != "unavailable" || resp.Escalation.Provider != "decider" {
					t.Errorf("the escalation is %+v, want the decision provider passed over as unavailable", resp.Escalation)
				}
			})
			t.Run(fmt.Sprintf("%s fails as an invalid answer with no failover at floor %v", tc.name, floor), func(t *testing.T) {
				t.Parallel()
				generator, _ := pair()
				decider := rawDecider{tc.answers}
				resp, err := Decisions(generator, decider, DecisionOptions{Floor: floor, NoFailover: true}).Complete(t.Context(), deciding(&schemaflux.Decision{State: `"x"`, Questions: questions}))
				if tc.valid {
					if err != nil || !slices.Equal(resp.Answers, tc.answers) {
						t.Errorf("a valid answer: err %v, %+v", err, resp.Answers)
					}
					return
				}
				wantKind(t, err, schemaflux.ErrInvalidAnswer)
				if generator.Calls() != 0 {
					t.Errorf("the generator was asked %d times, want 0", generator.Calls())
				}
				if len(resp.Answers) != 0 {
					t.Errorf("an answer came back beside the error: %+v", resp.Answers)
				}
			})
		}
	}
}

// A floor that is not a probability is an unusable setting, and fails every request
// through the pair, one with no decision in it too.
func TestDecisionsWithAFloorThatIsNotAProbabilityFailsEveryRequest(t *testing.T) {
	t.Parallel()
	for _, floor := range []float64{-0.0001, 1.0001, math.NaN(), math.Inf(1), math.Inf(-1), -1, 2} {
		generator, decider := pair(schemafluxtest.Yes(0.9))
		pair := Decisions(generator, decider, DecisionOptions{Floor: floor})
		for name, req := range map[string]schemaflux.Request{
			"a decision":  deciding(yesNo(`"x"`, "is it?")),
			"no decision": ask("summarize"),
		} {
			_, err := pair.Complete(t.Context(), req)
			e := wantKind(t, err, schemaflux.ErrConfiguration)
			if e.Op != "mw.decisions" {
				t.Errorf("floor %v, %s: Op %q", floor, name, e.Op)
			}
		}
		if generator.Calls() != 0 || decider.Calls() != 0 {
			t.Errorf("floor %v: %d requests were sent, want none", floor, generator.Calls()+decider.Calls())
		}
	}
	for _, floor := range []float64{0, 0.5, 1} {
		generator, decider := pair(schemafluxtest.Yes(1))
		if _, err := Decisions(generator, decider, DecisionOptions{Floor: floor}).Complete(t.Context(), ask("summarize")); err != nil {
			t.Errorf("floor %v: %v", floor, err)
		}
	}
}
