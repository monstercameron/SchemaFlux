package tests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
	"github.com/monstercameron/schemaflux/mw"
)

// LX-025 through the exported API. Validate works its verdict out from the
// issues the model lists. An answer that listed none read the same as an
// answer that found none, so a model that said the record was invalid and did
// not say why came back valid, and so did an empty object.
func TestIntegrationValidateRefusesAnAnswerThatItemisesNothing(t *testing.T) {
	issue := `{"field":"age","message":"must be at least 18","suggestion":null,"explanation":"the rule says so"}`
	cases := []struct {
		name    string
		body    string
		failOn  string
		wantErr bool
		valid   bool
		errors  int
	}{
		{"invalid, with nothing itemised", `{"valid":false,"summary":"the record is wrong"}`, "", true, false, 0},
		{"an empty object", `{}`, "", true, false, 0},
		{"a verdict and nothing else", `{"valid":true}`, "", true, false, 0},
		{"no list of warnings", `{"errors":[],"info":[],"confidence":0.9,"summary":"s"}`, "", true, false, 0},
		{"no confidence", `{"errors":[],"warnings":[],"info":[],"summary":"s"}`, "", true, false, 0},
		{"invalid, with every list empty", `{"valid":false,"errors":[],"warnings":[],"info":[],"confidence":0.9,"summary":"s"}`, "", true, false, 0},
		{"an issue with no message", `{"errors":[{"field":"age"}],"warnings":[],"info":[],"confidence":0.9,"summary":"s"}`, "", true, false, 0},
		{"a whole answer that found nothing", `{"errors":[],"warnings":[],"info":[],"confidence":0.9,"summary":"fine"}`, "", false, true, 0},
		{"a whole answer that found an error", `{"errors":[` + issue + `],"warnings":[],"info":[],"confidence":0.9,"summary":"one error"}`, "", false, false, 1},
		{"valid claimed over an error", `{"valid":true,"errors":[` + issue + `],"warnings":[],"info":[],"confidence":0.9,"summary":"s"}`, "", false, false, 1},
		{"a warning, which fails when asked", `{"errors":[],"warnings":[` + issue + `],"info":[],"confidence":0.9,"summary":"s"}`, "warning", false, false, 0},
		{"a warning, which does not fail by default", `{"errors":[],"warnings":[` + issue + `],"info":[],"confidence":0.9,"summary":"s"}`, "", false, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testfixtures.WithScriptedProvider(t, tc.body, nil)
			opts := schemaflux.NewValidateOptions().WithRules("age must be at least 18")
			if tc.failOn != "" {
				opts = opts.WithFailOn(tc.failOn)
			}
			result, err := schemaflux.Validate(customer{Email: "a@b.com", Country: "GB", Age: 12}, opts)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("the answer was accepted: valid = %v", result.Valid)
				}
				if result.Valid || len(result.Errors) != 0 || result.Corrected != nil {
					t.Errorf("a refused validation returned a result: %+v", result)
				}
				if !errors.Is(err, schemaflux.ErrSchemaViolation) && !errors.Is(err, schemaflux.ErrMalformedOutput) {
					t.Errorf("the error is not classified as the answer's fault: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if result.Valid != tc.valid || len(result.Errors) != tc.errors {
				t.Errorf("valid %v with %d errors, want %v with %d", result.Valid, len(result.Errors), tc.valid, tc.errors)
			}

			// The shared judgment says the same thing.
			judged, err := schemaflux.ValidateHybrid(customer{Email: "a@b.com", Country: "GB", Age: 12}, opts)
			if err != nil {
				t.Fatalf("ValidateHybrid: %v", err)
			}
			if passes := judged.Verdict == schemaflux.VerdictPass; passes != tc.valid {
				t.Errorf("ValidateHybrid's verdict is %v, and Validate said valid = %v", judged.Verdict, tc.valid)
			}
		})
	}
}

// A correction is returned only to a caller who asked for one.
func TestIntegrationValidateTakesACorrectionOnlyWhenAsked(t *testing.T) {
	body := `{"errors":[{"field":"country","message":"must be ISO alpha-2"}],"warnings":[],"info":[],"confidence":0.9,"summary":"s","corrected":{"email":"ada@example.com","country":"GB","age":36}}`
	subject := customer{Email: "ada@example.com", Country: "United Kingdom", Age: 36}

	testfixtures.WithScriptedProvider(t, body, nil)
	asked, err := schemaflux.Validate(subject, schemaflux.NewValidateOptions().WithRules("country must be ISO alpha-2").WithAutoCorrect(true))
	if err != nil || asked.Corrected == nil || asked.Corrected.Country != "GB" {
		t.Fatalf("corrected %+v, err %v", asked.Corrected, err)
	}

	notAsked, err := schemaflux.Validate(subject, schemaflux.NewValidateOptions().WithRules("country must be ISO alpha-2").WithAutoCorrect(false))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if notAsked.Corrected != nil {
		t.Errorf("a correction nobody asked for was returned: %+v", notAsked.Corrected)
	}
}

// Example_validateRefusesAVerdictWithNoReason shows what a caller sees when the
// model says a record is invalid and lists nothing: an error, and no verdict.
func Example_validateRefusesAVerdictWithNoReason() {
	schemaflux.NewClient("example-key").WithProviderInstance(
		testfixtures.NewScripted(`{"valid": false, "summary": "the record is wrong"}`))

	result, err := schemaflux.Validate(
		customer{Email: "ada@example.com", Country: "GB", Age: 12},
		schemaflux.NewValidateOptions().WithRules("age must be at least 18"))

	fmt.Println("valid:", result.Valid)
	fmt.Println("refused:", errors.Is(err, schemaflux.ErrSchemaViolation))

	// Output:
	// valid: false
	// refused: true
}

// routeProvider answers under a provider and model of its choosing, or names
// neither, or fails.
type routeProvider struct {
	name          string
	fail          error
	answersAs     string
	answersModel  string
	namesNothing  bool
	calls         int
	askedForModel string
}

func (p *routeProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls++
	p.askedForModel = req.Model
	if p.fail != nil {
		return llm.CompletionResponse{}, p.fail
	}
	resp := llm.CompletionResponse{
		Content: `{"category":"billing","confidence":0.9,"reasoning":"r"}`, Model: req.Model, Provider: p.name, FinishReason: "stop",
		Usage: schemaflux.TokenUsage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30},
	}
	if p.answersAs != "" {
		resp.Provider = p.answersAs
	}
	if p.answersModel != "" {
		resp.Model = p.answersModel
	}
	if p.namesNothing {
		resp.Provider, resp.Model = "", ""
	}
	return resp, nil
}
func (p *routeProvider) Name() string                               { return p.name }
func (p *routeProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *routeProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

// Review round 11, through the exported API. An answer is held to the client's
// data policy as the route it names. A fallback that had been checked and
// allowed was refused afterwards, because its answer named a dated snapshot of
// the model nobody had declared under that name; and a gateway that kept its
// provider's name while answering from a model the policy forbids was accepted.
func TestIntegrationAnAnswerIsHeldToTheRouteItNames(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "test-model")
	eu := []string{"eu"}
	down := errors.New("the primary is down")
	policy := schemaflux.DataPolicy{AllowedProviders: []string{"primary", "backup"}, AllowedRegions: eu}

	cases := []struct {
		name     string
		primary  *routeProvider
		backup   *routeProvider
		altModel string
		refused  bool
		backups  int
	}{
		{"the primary answers as itself",
			&routeProvider{name: "primary"}, &routeProvider{name: "backup"}, "backup-model", false, 0},
		{"the primary answers as a dated snapshot of the model asked",
			&routeProvider{name: "primary", answersModel: "test-model-2026-09-15"}, &routeProvider{name: "backup"}, "backup-model", false, 0},
		{"the primary answers and names nothing",
			&routeProvider{name: "primary", namesNothing: true}, &routeProvider{name: "backup"}, "backup-model", false, 0},
		{"the primary keeps its name and answers from a forbidden model",
			&routeProvider{name: "primary", answersModel: "us-model"}, &routeProvider{name: "backup"}, "backup-model", true, 0},
		{"the primary keeps its name and answers from a model nobody declared",
			&routeProvider{name: "primary", answersModel: "mystery-model"}, &routeProvider{name: "backup"}, "backup-model", true, 0},
		{"the primary answers as a provider the policy does not allow",
			&routeProvider{name: "primary", answersAs: "elsewhere"}, &routeProvider{name: "backup"}, "backup-model", true, 0},
		{"the backup answers as itself",
			&routeProvider{name: "primary", fail: down}, &routeProvider{name: "backup"}, "backup-model", false, 1},
		{"the backup answers as a snapshot of its model",
			&routeProvider{name: "primary", fail: down}, &routeProvider{name: "backup", answersModel: "backup-model-20250929"}, "backup-model", false, 1},
		{"the backup answers and names nothing",
			&routeProvider{name: "primary", fail: down}, &routeProvider{name: "backup", namesNothing: true}, "backup-model", false, 1},
		{"the backup answers from a forbidden model of its own",
			&routeProvider{name: "primary", fail: down}, &routeProvider{name: "backup", answersModel: "backup-us-model"}, "backup-model", true, 1},
		{"the backup sends it on to a provider the policy does not allow",
			&routeProvider{name: "primary", fail: down}, &routeProvider{name: "backup", answersAs: "elsewhere"}, "backup-model", true, 1},
		{"the backup has no model of its own, and the request's is not declared for it",
			&routeProvider{name: "primary", fail: down}, &routeProvider{name: "backup"}, "", true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			schemaflux.DeclareRoute("primary", "test-model", eu, 0)
			schemaflux.DeclareRoute("primary", "us-model", []string{"us"}, 0)
			schemaflux.DeclareRoute("backup", "backup-model", eu, 0)
			schemaflux.DeclareRoute("backup", "backup-us-model", []string{"us"}, 0)

			chain := mw.Chain(tc.primary, mw.Fallback([]mw.FallbackRoute{{Handler: tc.backup, Model: tc.altModel}}))
			client := schemaflux.NewClient("").WithProviderInstance(chain).WithDataPolicy(policy)
			err := lx126Classify(client.Context(context.Background()))

			if tc.backup.calls != tc.backups {
				t.Fatalf("the backup was called %d times, want %d", tc.backup.calls, tc.backups)
			}
			if tc.refused != (err != nil) {
				t.Fatalf("err = %v, want a refusal: %v", err, tc.refused)
			}
			if tc.backups == 1 && tc.backup.askedForModel != tc.altModel {
				t.Errorf("the backup was asked for %q, want its own model %q", tc.backup.askedForModel, tc.altModel)
			}
			switch {
			case !tc.refused:
			case tc.primary.fail != nil && tc.backups == 0:
				// The backup was never an option, so what is reported is the
				// failure that sent the chain looking.
				if !errors.Is(err, down) {
					t.Errorf("err = %v, want the primary's own failure", err)
				}
			default:
				if !errors.Is(err, schemaflux.ErrPolicyViolation) {
					t.Errorf("err = %v, want a policy refusal", err)
				}
			}
		})
	}
}

// When every rule is required and no option meets them all, the error says
// which option failed which rule, and nothing of the options or the rules.
func TestIntegrationArbitrateSaysWhichOptionFailedWhichRule(t *testing.T) {
	testfixtures.WithScriptedProvider(t, lx024Evaluations(
		[2]string{lx024Rule(true, "0.9"), lx024Rule(false, "0.1")},
		[2]string{lx024Rule(false, "0.2"), lx024Rule(false, "0.3")},
	), nil)

	result, err := schemaflux.Arbitrate(lx024Candidates, schemaflux.ArbitrateOptions{
		Rules:           []string{"has three or more years of experience", "is based in Faro"},
		RequireAllRules: true,
	})
	if !errors.Is(err, schemaflux.ErrReviewRequired) {
		t.Fatalf("err = %v, want a review-required error", err)
	}
	for _, want := range []string{"option 1 failed rule 2", "option 2 failed rules 1, 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q: %v", want, err)
		}
	}
	for _, leaked := range []string{"Ada", "Lisbon", "Faro", "experience", "because"} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("the error carries %q: %v", leaked, err)
		}
	}
	if result.Evaluations != nil {
		t.Errorf("evaluations came back beside the error: %+v", result.Evaluations)
	}
}

// Decide refuses a situation that cannot reach the model, whether or not a
// fallback is set. An error is not one: it is shown as its message (round 12;
// written as JSON it is an empty object, and for a round it was refused).
func TestIntegrationDecideRefusesASituationThatSaysNothing(t *testing.T) {
	testfixtures.WithScriptedProvider(t, `{"selected":1,"explanation":"x","confidence":0.9,"alternatives":[]}`, nil)
	branches := []schemaflux.Decision[string]{
		{Value: "retry", Description: "try the payment again"},
		{Value: "refund", Description: "refund the customer"},
	}

	for _, opts := range []schemaflux.DecideOptions{schemaflux.NewDecideOptions(), schemaflux.NewDecideOptions().WithFallback(0)} {
		value, result, err := schemaflux.Decide(context.Background(), struct{ secret string }{secret: "the card was declined"}, branches, opts)
		if !errors.Is(err, schemaflux.ErrInvalidRequest) {
			t.Fatalf("value %q, result %+v, err %v; want an invalid-request error", value, result, err)
		}
		if result.Fallback || value != "" {
			t.Errorf("the fallback was taken for a request that could not be made: %q, %+v", value, result)
		}
	}

	// An error is a situation: its message is what the model is shown.
	provider := testfixtures.WithScriptedProvider(t, `{"selected":1,"explanation":"x","confidence":0.9,"alternatives":[]}`, nil)
	value, result, err := schemaflux.Decide(context.Background(), errors.New("the card was declined"), branches)
	if err != nil || value != "refund" || result.SelectedIndex != 1 {
		t.Errorf("value %q, result %+v, err %v", value, result, err)
	}
	if requests := provider.Requests(); len(requests) != 1 || !strings.Contains(requests[0].UserPrompt, "the card was declined") {
		t.Errorf("the error's message did not reach the model: %d requests", len(requests))
	}
}

// ExampleArbitrate_reasons shows asking for a reason with every evaluation,
// which Arbitrate leaves out unless asked.
func ExampleArbitrate_reasons() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(
		`{"options":{"o1":{"r1":{"passed":true,"score":0.9,"reasoning":"six years"}},"o2":{"r1":{"passed":false,"score":0.2,"reasoning":"two years"}}},"reasoning":"o1 has the experience","confidence":0.9}`))

	result, err := schemaflux.Arbitrate(lx024Candidates, schemaflux.ArbitrateOptions{
		Rules:            []string{"has three or more years of experience"},
		IncludeReasoning: true,
	})
	if err != nil {
		fmt.Println("no winner:", err)
		return
	}
	fmt.Println("winner:", result.Winner.Name)
	for _, evaluation := range result.Evaluations {
		for _, rule := range evaluation.RuleResults {
			fmt.Printf("option %d: passed=%v, %s\n", evaluation.Index, rule.Passed, rule.Reasoning)
		}
	}

	// Output:
	// winner: Ada
	// option 0: passed=true, six years
	// option 1: passed=false, two years
}
