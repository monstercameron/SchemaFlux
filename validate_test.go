package schemaflux_test

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// validateSignup is a value to validate, written as JSON with lower case names.
type validateSignup struct {
	Email string `json:"email"`
	Age   int    `json:"age"`
}

const (
	// validateErrorAndWarning lists one error and one warning and proposes nothing.
	validateErrorAndWarning = `{"errors":[{"field":"email","message":"not an email","suggestion":"fix it","explanation":"no at sign"}],` +
		`"warnings":[{"field":"age","message":"young","suggestion":"","explanation":""}],"info":[],"confidence":0.9,"summary":"one problem"}`

	// validateWarningOnly lists a warning and nothing else.
	validateWarningOnly = `{"errors":[],"warnings":[{"field":"age","message":"young"}],"info":[],"confidence":0.8,"summary":"a warning"}`

	validateClean = `{"errors":[],"warnings":[],"info":[],"confidence":0.95,"summary":"fine"}`
)

var (
	validateBadSignup = validateSignup{Email: "not-an-email", Age: 17}
	validateRules     = schemaflux.ValidateOptions{Rules: "the email has to be a real address"}
)

func TestValidateReturnsTheIssuesAndTheVerdictWorkedOutFromThem(t *testing.T) {
	t.Parallel()
	model := answeringWith(validateErrorAndWarning)
	res, err := schemaflux.Validate(operationClient(model), validateBadSignup, validateRules)
	if err != nil {
		t.Fatal(err)
	}
	wantIssues := []schemaflux.JudgmentIssue{
		{Subject: "email", Severity: "error", Message: "not an email", Suggestion: "fix it", Evidence: []string{"no at sign"}},
		{Subject: "age", Severity: "warning", Message: "young"},
	}
	if res.Verdict != schemaflux.VerdictFail || res.Subject != validateBadSignup || res.Summary != "one problem" || res.ModelConfidence != 0.9 {
		t.Errorf("got %+v", res)
	}
	if len(res.Issues) != 2 || !reflect.DeepEqual(res.Issues[0], wantIssues[0]) || res.Issues[1].Subject != "age" || res.Issues[1].Severity != "warning" || res.Issues[1].Message != "young" {
		t.Errorf("issues = %+v", res.Issues)
	}
	if res.Corrected != nil || res.DeterministicOnly {
		t.Errorf("corrected %v deterministic only %v", res.Corrected, res.DeterministicOnly)
	}
	req := model.Last()
	wantText(t, req, validateBadSignup.Email)
	if req.Schema == nil || req.SchemaName != "validation" {
		t.Errorf("schema %v named %q: the request has to declare one", req.Schema, req.SchemaName)
	}
}

func TestValidateDecidesWhatGoCanDecideAndAsksNoModelWhenNothingIsLeft(t *testing.T) {
	t.Parallel()
	rules := map[string]string{"Email": "email", "Age": "min:18"}
	model := answeringWith(validateClean)
	res, err := schemaflux.Validate(operationClient(model), validateBadSignup, schemaflux.ValidateOptions{FieldRules: rules})
	if err != nil {
		t.Fatal(err)
	}
	if model.Calls() != 0 || !res.DeterministicOnly || res.Verdict != schemaflux.VerdictFail || len(res.Issues) != 2 || res.ModelConfidence != 0 {
		t.Errorf("got %+v after %d requests", res, model.Calls())
	}
	if res.Issues[0].Subject != "Age" || res.Issues[1].Subject != "Email" {
		t.Errorf("the issues are not in field order: %+v", res.Issues)
	}
	pass, err := schemaflux.Validate(operationClient(model), validateSignup{Email: "a@b.co", Age: 30}, schemaflux.ValidateOptions{FieldRules: rules})
	if err != nil || pass.Verdict != schemaflux.VerdictPass || pass.Issues != nil || !pass.DeterministicOnly || model.Calls() != 0 {
		t.Errorf("a value that meets every rule: %+v, %v", pass, err)
	}
}

func TestValidateAsksTheModelOnlyAboutTheRulesGoCannotDecide(t *testing.T) {
	t.Parallel()
	model := answeringWith(validateClean)
	_, err := schemaflux.Validate(operationClient(model), validateBadSignup, schemaflux.ValidateOptions{
		FieldRules: map[string]string{"Email": "email", "Age": "plausible for a customer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	wantText(t, req, "plausible for a customer")
	wantNoText(t, req, `"Email":"email"`)
}

func TestValidateKeepsAnIssueGoFoundWhenTheModelFindsNone(t *testing.T) {
	t.Parallel()
	res, err := schemaflux.Validate(operationClient(answeringWith(validateClean)), validateBadSignup, schemaflux.ValidateOptions{
		FieldRules: map[string]string{"Email": "email", "Age": "plausible"},
	})
	if err != nil || res.Verdict != schemaflux.VerdictFail || len(res.Issues) != 1 || res.Issues[0].Subject != "Email" || res.DeterministicOnly {
		t.Errorf("got %+v, %v", res, err)
	}
}

func TestValidateWorksTheVerdictOutFromFailOn(t *testing.T) {
	t.Parallel()
	const infoOnly = `{"errors":[],"warnings":[],"info":[{"field":"age","message":"note"}],"confidence":0.8,"summary":"a note"}`
	cases := []struct {
		name   string
		body   string
		failOn string
		want   schemaflux.Verdict
	}{
		{"an error fails by default", validateErrorAndWarning, "", schemaflux.VerdictFail},
		{"a warning passes by default", validateWarningOnly, "", schemaflux.VerdictPass},
		{"a warning passes at error", validateWarningOnly, "error", schemaflux.VerdictPass},
		{"a warning fails at warning", validateWarningOnly, "warning", schemaflux.VerdictFail},
		{"a note passes at warning", infoOnly, "warning", schemaflux.VerdictPass},
		{"a note fails at info", infoOnly, "info", schemaflux.VerdictFail},
		{"nothing passes at info", validateClean, "info", schemaflux.VerdictPass},
		{"an error fails at info", validateErrorAndWarning, "info", schemaflux.VerdictFail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Validate(operationClient(answeringWith(tc.body)), validateBadSignup, schemaflux.ValidateOptions{Rules: "x", FailOn: tc.failOn})
			if err != nil || res.Verdict != tc.want {
				t.Errorf("verdict = %q, %v, want %q", res.Verdict, err, tc.want)
			}
		})
	}
}

func TestValidateDoesNotOverruleAVerdictOfInvalidTheModelVolunteers(t *testing.T) {
	t.Parallel()
	t.Run("with something listed the data fails", func(t *testing.T) {
		t.Parallel()
		body := `{"valid":false,"errors":[],"warnings":[{"field":"age","message":"young"}],"info":[],"confidence":0.8,"summary":"s"}`
		res, err := schemaflux.Validate(operationClient(answeringWith(body)), validateBadSignup, validateRules)
		if err != nil || res.Verdict != schemaflux.VerdictFail || len(res.Issues) != 1 {
			t.Errorf("got %+v, %v", res, err)
		}
	})
	t.Run("with nothing listed the answer is refused", func(t *testing.T) {
		t.Parallel()
		body := `{"valid":false,"errors":[],"warnings":[],"info":[],"confidence":0.8,"summary":"s"}`
		res, err := schemaflux.Validate(operationClient(answeringWith(body)), validateBadSignup, validateRules)
		wantError(t, err, schemaflux.ErrInvalidAnswer, "validate")
		if res.Verdict != "" || res.Issues != nil {
			t.Errorf("a refused answer left %+v beside the error", res)
		}
	})
	t.Run("a volunteered valid does not make the data pass", func(t *testing.T) {
		t.Parallel()
		body := `{"valid":true,"errors":[{"field":"email","message":"bad"}],"warnings":[],"info":[],"confidence":0.8,"summary":"s"}`
		res, err := schemaflux.Validate(operationClient(answeringWith(body)), validateBadSignup, validateRules)
		if err != nil || res.Verdict != schemaflux.VerdictFail {
			t.Errorf("got %+v, %v", res, err)
		}
	})
}

func TestValidateReturnsTheCorrectionTheModelProposedAndNilWhenItProposedNone(t *testing.T) {
	t.Parallel()
	t.Run("a correction laid over the data", func(t *testing.T) {
		t.Parallel()
		body := `{"errors":[{"field":"email","message":"bad"}],"warnings":[],"info":[],"confidence":0.8,"summary":"s","corrected":{"email":"a@b.co"}}`
		res, err := schemaflux.Validate(operationClient(answeringWith(body)), validateBadSignup, validateRules)
		if err != nil || res.Corrected == nil || *res.Corrected != (validateSignup{Email: "a@b.co", Age: 17}) {
			t.Fatalf("got %+v, %v", res, err)
		}
		if validateBadSignup.Email != "not-an-email" {
			t.Error("the correction was written into the caller's value")
		}
	})
	t.Run("no correction in the answer", func(t *testing.T) {
		t.Parallel()
		res, err := schemaflux.Validate(operationClient(answeringWith(validateErrorAndWarning)), validateBadSignup, validateRules)
		if err != nil || res.Corrected != nil {
			t.Errorf("got %+v, %v", res.Corrected, err)
		}
	})
	t.Run("a null correction", func(t *testing.T) {
		t.Parallel()
		body := `{"errors":[],"warnings":[],"info":[],"confidence":0.8,"summary":"s","corrected":null}`
		res, err := schemaflux.Validate(operationClient(answeringWith(body)), validateBadSignup, validateRules)
		if err != nil || res.Corrected != nil {
			t.Errorf("got %+v, %v", res.Corrected, err)
		}
	})
	t.Run("a correction of another shape is refused", func(t *testing.T) {
		t.Parallel()
		body := `{"errors":[],"warnings":[],"info":[],"confidence":0.8,"summary":"s","corrected":{"nope":1}}`
		res, err := schemaflux.Validate(operationClient(answeringWith(body)), validateBadSignup, validateRules)
		wantError(t, err, schemaflux.ErrInvalidAnswer, "validate")
		if res.Corrected != nil || res.Verdict != "" {
			t.Errorf("a refused answer left %+v beside the error", res)
		}
	})
	t.Run("OmitCorrection takes none even when the model sent one", func(t *testing.T) {
		t.Parallel()
		body := `{"errors":[{"field":"email","message":"bad"}],"warnings":[],"info":[],"confidence":0.8,"summary":"s","corrected":{"email":"a@b.co"}}`
		model := answeringWith(body)
		res, err := schemaflux.Validate(operationClient(model), validateBadSignup, schemaflux.ValidateOptions{Rules: "x", OmitCorrection: true})
		if err != nil || res.Corrected != nil {
			t.Errorf("got %+v, %v", res.Corrected, err)
		}
		wantNoText(t, model.Last(), "A correction is asked for.")
	})
}

func TestValidateWithNoOptionsAsksForExplanationsAndACorrectionAtTheFastTierInStrictMode(t *testing.T) {
	t.Parallel()
	model := answeringWith(validateClean)
	if _, err := schemaflux.Validate(operationClient(model), validateBadSignup, validateRules); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	wantText(t, req, "Give an explanation with every item.")
	wantText(t, req, "A correction is asked for.")
	wantText(t, req, `the email has to be a real address`)
	if math.Abs(req.Temperature-0.1) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" {
		t.Errorf("temperature %v ceiling %d model %q", req.Temperature, req.MaxTokens, req.Model)
	}
}

func TestValidateEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.ValidateOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Rules", schemaflux.ValidateOptions{Rules: "total equals the lines"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Rules:`)
			wantText(t, req, `total equals the lines`)
		}},
		{"FieldRules", schemaflux.ValidateOptions{FieldRules: map[string]string{"age": "plausible"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Rules for single fields, by field name:`)
			wantText(t, req, `"age":"plausible"`)
		}},
		{"SchemaHints", schemaflux.ValidateOptions{SchemaHints: map[string]string{"age": "in years"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `What each field holds, by field name:`)
			wantText(t, req, `"age":"in years"`)
		}},
		{"OmitExplanations", schemaflux.ValidateOptions{Rules: "x", OmitExplanations: true}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Give an explanation with every item.")
		}},
		{"OmitCorrection", schemaflux.ValidateOptions{Rules: "x", OmitCorrection: true}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "A correction is asked for.")
		}},
		{"Steering", schemaflux.ValidateOptions{Rules: "x", Options: schemaflux.Options{Steering: "be lenient"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "be lenient")
		}},
		{"Model", schemaflux.ValidateOptions{Rules: "x", Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeCreative", schemaflux.ValidateOptions{Rules: "x", Options: schemaflux.Options{Mode: schemaflux.ModeCreative}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.7) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
		{"TierSmart", schemaflux.ValidateOptions{Rules: "x", Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.ValidateOptions{Rules: "x", Options: schemaflux.Options{MaxOutputTokens: 55}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 55 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.ValidateOptions{Rules: "x", Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"Effort", schemaflux.ValidateOptions{Rules: "x", Options: schemaflux.Options{Effort: schemaflux.EffortLow}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" {
				t.Errorf("effort = %q", req.Effort)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(validateClean)
			if _, err := schemaflux.Validate(operationClient(model), validateBadSignup, tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestValidateOfNothingToValidateAgainstIsAConfigurationErrorBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := map[string]schemaflux.ValidateOptions{
		"no options":           {},
		"only spaces as rules": {Rules: "   "},
		"only a steering":      {Options: schemaflux.Options{Steering: "x"}},
		"empty maps":           {FieldRules: map[string]string{}, SchemaHints: map[string]string{}},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(validateClean)
			res, err := schemaflux.Validate(operationClient(model), validateBadSignup, opts)
			wantError(t, err, schemaflux.ErrConfiguration, "validate")
			if model.Calls() != 0 || res.Verdict != "" {
				t.Errorf("%d requests were sent, result %+v", model.Calls(), res)
			}
		})
	}
}

func TestValidateDeterministicNeverCallsAModelAndRefusesWhatOneWouldHaveToJudge(t *testing.T) {
	t.Parallel()
	decidable := map[string]string{"email": "email"}
	t.Run("a call that decides every rule", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(validateClean)
		res, err := schemaflux.Validate(operationClient(model), validateBadSignup, schemaflux.ValidateOptions{Deterministic: true, FieldRules: decidable})
		if err != nil || !res.DeterministicOnly || res.Verdict != schemaflux.VerdictFail || model.Calls() != 0 {
			t.Errorf("got %+v, %v after %d requests", res, err, model.Calls())
		}
	})
	refused := map[string]schemaflux.ValidateOptions{
		"free text rules":         {Deterministic: true, FieldRules: decidable, Rules: "be good"},
		"schema hints":            {Deterministic: true, FieldRules: decidable, SchemaHints: map[string]string{"age": "years"}},
		"no field rules":          {Deterministic: true},
		"a rule Go cannot decide": {Deterministic: true, FieldRules: map[string]string{"CANARY-field": "pleasant to read"}},
		"one of several":          {Deterministic: true, FieldRules: map[string]string{"email": "email", "age": "plausible"}},
	}
	for name, opts := range refused {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(validateClean)
			res, err := schemaflux.Validate(operationClient(model), validateBadSignup, opts)
			e := wantError(t, err, schemaflux.ErrConfiguration, "validate")
			if model.Calls() != 0 || res.Verdict != "" || res.DeterministicOnly {
				t.Errorf("%d requests were sent, result %+v", model.Calls(), res)
			}
			for _, text := range chainText(err) {
				if strings.Contains(text, "CANARY") || strings.Contains(text, "ValidateHybrid") {
					t.Errorf("the message names a field or an internal function: %q", e.Message)
				}
			}
		})
	}
}

func TestValidateRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := map[string]schemaflux.ValidateOptions{
		"a FailOn that is not one":                {Rules: "x", FailOn: "fatal"},
		"a negative output ceiling":               {Rules: "x", Options: schemaflux.Options{MaxOutputTokens: -1}},
		"a tier that is not one":                  {Rules: "x", Options: schemaflux.Options{Tier: 9}},
		"a mode that is not one":                  {Rules: "x", Options: schemaflux.Options{Mode: 9}},
		"a bad common setting with Deterministic": {Deterministic: true, FieldRules: map[string]string{"email": "email"}, Options: schemaflux.Options{Mode: 9}},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(validateClean)
			_, err := schemaflux.Validate(operationClient(model), validateBadSignup, opts)
			wantError(t, err, schemaflux.ErrConfiguration, "validate")
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent", model.Calls())
			}
		})
	}
}

func TestValidateNamesTheFieldOfABadFailOnAndNotItsValue(t *testing.T) {
	t.Parallel()
	_, err := schemaflux.Validate(operationClient(answeringWith(validateClean)), validateBadSignup, schemaflux.ValidateOptions{Rules: "x", FailOn: "CANARY-failon"})
	e := wantError(t, err, schemaflux.ErrConfiguration, "validate")
	if want := "FailOn is not one of error, warning and info"; e.Message != want {
		t.Errorf("message = %q, want %q", e.Message, want)
	}
	for _, text := range chainText(err) {
		if strings.Contains(text, "CANARY") {
			t.Errorf("the value reached %q", text)
		}
	}
}

func TestValidateRefusesAnAnswerThatDoesNotReportEveryListAndTheConfidence(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"no errors list":             `{"warnings":[],"info":[],"confidence":0.8,"summary":"s"}`,
		"a null warnings list":       `{"errors":[],"warnings":null,"info":[],"confidence":0.8,"summary":"s"}`,
		"no confidence":              `{"errors":[],"warnings":[],"info":[],"summary":"s"}`,
		"a confidence off the scale": `{"errors":[],"warnings":[],"info":[],"confidence":1.5,"summary":"s"}`,
		"an issue that says nothing": `{"errors":[{"field":"age","message":"  "}],"warnings":[],"info":[],"confidence":0.8,"summary":"s"}`,
		"a list that is not issues":  `{"errors":["bad"],"warnings":[],"info":[],"confidence":0.8,"summary":"s"}`,
		"text":                       `The data is fine.`,
		"a bare list":                `[]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Validate(operationClient(answeringWith(body)), validateBadSignup, validateRules)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "validate")
			if res.Verdict != "" || res.Issues != nil || res.Corrected != nil || res.Summary != "" {
				t.Errorf("a refused answer left %+v beside the error", res)
			}
		})
	}
}

func TestValidateWithANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	var nilContext context.Context
	_, err := schemaflux.Validate(nilContext, validateBadSignup, validateRules)
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "validate"); e.Message != "the context is nil" {
		t.Errorf("message = %q", e.Message)
	}
}

func TestValidateMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later value wins and a later zero leaves the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(validateWarningOnly)
		res, err := schemaflux.Validate(operationClient(model), validateBadSignup,
			schemaflux.ValidateOptions{Rules: "first", FailOn: "warning", Options: schemaflux.Options{Model: "first-model"}},
			schemaflux.ValidateOptions{Options: schemaflux.Options{Steering: "later"}},
			schemaflux.ValidateOptions{FailOn: "error"})
		if err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "first")
		wantText(t, req, "later")
		if req.Model != "first-model" || res.Verdict != schemaflux.VerdictPass {
			t.Errorf("model %q verdict %q", req.Model, res.Verdict)
		}
	})
	t.Run("Deterministic turned on stays on", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(validateClean)
		res, err := schemaflux.Validate(operationClient(model), validateBadSignup,
			schemaflux.ValidateOptions{Deterministic: true, FieldRules: map[string]string{"email": "email"}}, schemaflux.ValidateOptions{})
		if err != nil || !res.DeterministicOnly || model.Calls() != 0 {
			t.Errorf("got %+v, %v after %d requests", res, err, model.Calls())
		}
	})
	t.Run("a refused setting in a later value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Validate(operationClient(answeringWith(validateClean)), validateBadSignup, validateRules, schemaflux.ValidateOptions{FailOn: "fatal"})
		wantError(t, err, schemaflux.ErrConfiguration, "validate")
	})
}

func TestValidateReturnsAnErrorTheCallerCanClassifyWhenTheProviderFails(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  *schemaflux.Error
	}{
		{"unavailable", &schemaflux.Error{Kind: schemaflux.ErrUnavailable}},
		{"rate limited", &schemaflux.Error{Kind: schemaflux.ErrRateLimited, Status: 429}},
		{"unauthorized", &schemaflux.Error{Kind: schemaflux.ErrUnauthorized, Status: 401}},
		{"timed out", &schemaflux.Error{Kind: schemaflux.ErrTimeout}},
		{"refused", &schemaflux.Error{Kind: schemaflux.ErrRefused}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			model.Fail(tc.err)
			res, err := schemaflux.Validate(operationClient(&model), validateBadSignup, validateRules)
			wantError(t, err, tc.err.Kind, "validate")
			if res.Verdict != "" || res.Subject != (validateSignup{}) {
				t.Errorf("a failed call left %+v beside the error", res)
			}
		})
	}
}

func TestValidateOfAValueJSONCannotWriteIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	model := answeringWith(validateClean)
	_, err := schemaflux.Validate(operationClient(model), map[string]any{"c": make(chan int)}, validateRules)
	wantError(t, err, schemaflux.ErrInvalidRequest, "validate")
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}
