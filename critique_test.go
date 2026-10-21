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

var critiqueCriteria = []string{"clarity", "argument"}

// critiqueReport writes an answer to a critique of the two criteria above, with the
// issues given as JSON and one positive.
func critiqueReport(issues string) string {
	return `{"overall_score":0.7,"criteria_scores":{"clarity":0.8,"argument":0.6},"issues":` + issues +
		`,"positives":[{"criterion":"clarity","description":"well structured","strength":"strong"}],"summary":"readable","top_priorities":["fix the intro"]}`
}

func critiqueIssue(severity string) string {
	return `{"criterion":"clarity","severity":"` + severity + `","description":"confusing","location":"intro","suggestion":"rewrite it","fix":"use this sentence","impact":"readers stop"}`
}

func TestCritiqueReturnsTheJudgmentAndWhatTheModelClaimed(t *testing.T) {
	t.Parallel()
	model := answeringWith(critiqueReport("[" + critiqueIssue("major") + "]"))
	res, err := schemaflux.Critique(operationClient(model), "the essay", critiqueCriteria)
	if err != nil {
		t.Fatal(err)
	}
	want := []schemaflux.JudgmentIssue{{Subject: "clarity", Category: "clarity", Severity: "error", Message: "confusing", Suggestion: "use this sentence", Evidence: []string{"intro"}}}
	if !reflect.DeepEqual(res.Issues, want) {
		t.Errorf("issues = %+v", res.Issues)
	}
	if res.Verdict != schemaflux.VerdictMixed || res.Subject != "the essay" || res.Summary != "readable" || res.ModelConfidence != 0.7 {
		t.Errorf("got %+v", res)
	}
	if !reflect.DeepEqual(res.CriteriaScores, map[string]float64{"clarity": 0.8, "argument": 0.6}) || !reflect.DeepEqual(res.TopPriorities, []string{"fix the intro"}) {
		t.Errorf("scores %v priorities %v", res.CriteriaScores, res.TopPriorities)
	}
	if !reflect.DeepEqual(res.Positives, []schemaflux.CritiquePositive{{Criterion: "clarity", Description: "well structured", Strength: "strong"}}) {
		t.Errorf("positives = %+v", res.Positives)
	}
	req := model.Last()
	wantText(t, req, `- "clarity"`)
	wantText(t, req, `- "argument"`)
	wantText(t, req, "Critique this:")
	wantText(t, req, "the essay")
	if req.Schema != nil {
		t.Errorf("a schema was declared: %v", req.Schema)
	}
}

func TestCritiqueWorksTheVerdictOutFromTheIssues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		issues string
		want   schemaflux.Verdict
	}{
		{"no issues", "[]", schemaflux.VerdictPass},
		{"a critical issue", "[" + critiqueIssue("critical") + "]", schemaflux.VerdictFail},
		{"a major issue", "[" + critiqueIssue("major") + "]", schemaflux.VerdictMixed},
		{"a minor issue", "[" + critiqueIssue("minor") + "]", schemaflux.VerdictMixed},
		{"a minor and a critical issue", "[" + critiqueIssue("minor") + "," + critiqueIssue("critical") + "]", schemaflux.VerdictFail},
		{"two major issues", "[" + critiqueIssue("major") + "," + critiqueIssue("major") + "]", schemaflux.VerdictMixed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Critique(operationClient(answeringWith(critiqueReport(tc.issues))), "e", critiqueCriteria)
			if err != nil || res.Verdict != tc.want {
				t.Errorf("verdict = %q, %v, want %q", res.Verdict, err, tc.want)
			}
		})
	}
}

func TestCritiqueTakesTheFixBeforeTheSuggestionWhenBothAreThere(t *testing.T) {
	t.Parallel()
	noFix := `{"criterion":"clarity","severity":"minor","description":"d","suggestion":"only a suggestion"}`
	res, err := schemaflux.Critique(operationClient(answeringWith(critiqueReport("["+noFix+"]"))), "e", critiqueCriteria)
	if err != nil || res.Issues[0].Suggestion != "only a suggestion" || res.Issues[0].Evidence != nil {
		t.Errorf("got %+v, %v", res.Issues, err)
	}
}

func TestCritiqueAcceptsARubricInPlaceOfCriteria(t *testing.T) {
	t.Parallel()
	model := answeringWith(`{"overall_score":0.7,"criteria_scores":{"readability":0.8},"issues":[],"summary":"s","top_priorities":[]}`)
	res, err := schemaflux.Critique(operationClient(model), "the code", nil, schemaflux.CritiqueOptions{Rubric: map[string]string{"readability": "is it easy to follow?"}})
	if err != nil || res.Verdict != schemaflux.VerdictPass || res.CriteriaScores["readability"] != 0.8 {
		t.Fatalf("got %+v, %v", res, err)
	}
	wantText(t, model.Last(), `- "readability": "is it easy to follow?"`)
}

func TestCritiqueRefusesNoCriteriaAndNoRubricAsAnInvalidRequestBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		criteria []string
		opts     []schemaflux.CritiqueOptions
	}{
		"nil criteria":                  {nil, nil},
		"an empty list":                 {[]string{}, nil},
		"an empty rubric":               {nil, []schemaflux.CritiqueOptions{{Rubric: map[string]string{}}}},
		"options that set no criterion": {nil, []schemaflux.CritiqueOptions{{MaxIssues: 3}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(critiqueReport("[]"))
			res, err := schemaflux.Critique(operationClient(model), "e", tc.criteria, tc.opts...)
			e := wantError(t, err, schemaflux.ErrInvalidRequest, "critique")
			if e.Message != "criteria is empty and there is no Rubric" || model.Calls() != 0 || res.Verdict != "" {
				t.Errorf("message %q, %d requests, result %+v", e.Message, model.Calls(), res)
			}
		})
	}
}

func TestCritiqueWithNoOptionsIsConstructiveAndAsksForEveryPartAtTheFastTier(t *testing.T) {
	t.Parallel()
	model := answeringWith(critiqueReport("[]"))
	if _, err := schemaflux.Critique(operationClient(model), "e", critiqueCriteria); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	wantText(t, req, "Style: Be constructive and encouraging")
	wantText(t, req, "Include actionable suggestions for each issue.")
	wantText(t, req, "Include specific fixes where possible.")
	wantText(t, req, "Include positive feedback on what's done well.")
	wantNoText(t, req, "Only report")
	wantNoText(t, req, "Limit to top")
	wantNoText(t, req, "Domain:")
	wantNoText(t, req, "Target audience:")
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" {
		t.Errorf("temperature %v ceiling %d model %q", req.Temperature, req.MaxTokens, req.Model)
	}
}

func TestCritiqueEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.CritiqueOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Rubric", schemaflux.CritiqueOptions{Rubric: map[string]string{"tone": "is it polite?"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `- "tone": "is it polite?"`)
		}},
		{"Style harsh", schemaflux.CritiqueOptions{Style: "harsh"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Style: Be direct and critical")
		}},
		{"Style balanced", schemaflux.CritiqueOptions{Style: "balanced"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Style: Balance positive and negative feedback equally.")
		}},
		{"SeverityFilter", schemaflux.CritiqueOptions{SeverityFilter: "critical"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Only report critical issues.")
		}},
		{"SeverityFilter all", schemaflux.CritiqueOptions{SeverityFilter: "all"}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Only report")
		}},
		{"MaxIssues", schemaflux.CritiqueOptions{MaxIssues: 5}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Limit to top 5 issues.")
		}},
		{"Domain", schemaflux.CritiqueOptions{Domain: "law"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Domain: "law"`)
		}},
		{"Audience", schemaflux.CritiqueOptions{Audience: "juniors"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Target audience: "juniors"`)
		}},
		{"OmitSuggestions", schemaflux.CritiqueOptions{OmitSuggestions: true}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Include actionable suggestions")
		}},
		{"OmitFixes", schemaflux.CritiqueOptions{OmitFixes: true}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Include specific fixes")
		}},
		{"OmitPositives", schemaflux.CritiqueOptions{OmitPositives: true}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Include positive feedback")
		}},
		{"Steering", schemaflux.CritiqueOptions{Options: schemaflux.Options{Steering: "be brief"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "be brief")
		}},
		{"Model", schemaflux.CritiqueOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.CritiqueOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
		{"TierSmart", schemaflux.CritiqueOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.CritiqueOptions{Options: schemaflux.Options{MaxOutputTokens: 55}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 55 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.CritiqueOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"Effort", schemaflux.CritiqueOptions{Options: schemaflux.Options{Effort: schemaflux.EffortLow}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" {
				t.Errorf("effort = %q", req.Effort)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := tc.opts
			body := critiqueReport("[]")
			if opts.Rubric != nil {
				body = `{"overall_score":0.7,"criteria_scores":{"clarity":0.8,"argument":0.6,"tone":0.5},"issues":[],"summary":"s","top_priorities":[]}`
			}
			model := answeringWith(body)
			if _, err := schemaflux.Critique(operationClient(model), "e", critiqueCriteria, opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestCritiqueRemovesWhatTheModelWroteThatWasOmitted(t *testing.T) {
	t.Parallel()
	body := critiqueReport("[" + critiqueIssue("minor") + "]")
	cases := []struct {
		name  string
		opts  schemaflux.CritiqueOptions
		check func(t *testing.T, res schemaflux.CritiqueResult[string])
	}{
		{"nothing omitted", schemaflux.CritiqueOptions{}, func(t *testing.T, res schemaflux.CritiqueResult[string]) {
			if res.Issues[0].Suggestion != "use this sentence" || len(res.Positives) != 1 {
				t.Errorf("got %+v positives %v", res.Issues, res.Positives)
			}
		}},
		{"OmitFixes leaves the suggestion", schemaflux.CritiqueOptions{OmitFixes: true}, func(t *testing.T, res schemaflux.CritiqueResult[string]) {
			if res.Issues[0].Suggestion != "rewrite it" {
				t.Errorf("suggestion = %q", res.Issues[0].Suggestion)
			}
		}},
		{"OmitSuggestions leaves the fix", schemaflux.CritiqueOptions{OmitSuggestions: true}, func(t *testing.T, res schemaflux.CritiqueResult[string]) {
			if res.Issues[0].Suggestion != "use this sentence" {
				t.Errorf("suggestion = %q", res.Issues[0].Suggestion)
			}
		}},
		{"both omitted", schemaflux.CritiqueOptions{OmitSuggestions: true, OmitFixes: true}, func(t *testing.T, res schemaflux.CritiqueResult[string]) {
			if res.Issues[0].Suggestion != "" {
				t.Errorf("suggestion = %q", res.Issues[0].Suggestion)
			}
		}},
		{"OmitPositives", schemaflux.CritiqueOptions{OmitPositives: true}, func(t *testing.T, res schemaflux.CritiqueResult[string]) {
			if res.Positives != nil {
				t.Errorf("positives = %+v", res.Positives)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Critique(operationClient(answeringWith(body)), "e", critiqueCriteria, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, res)
		})
	}
}

func TestCritiqueEnforcesMaxIssuesAndSeverityFilterOnTheAnswer(t *testing.T) {
	t.Parallel()
	three := "[" + critiqueIssue("major") + "," + critiqueIssue("major") + "," + critiqueIssue("major") + "]"
	cases := []struct {
		name    string
		issues  string
		opts    schemaflux.CritiqueOptions
		refused bool
	}{
		{"three issues with no limit", three, schemaflux.CritiqueOptions{}, false},
		{"three issues within a limit of three", three, schemaflux.CritiqueOptions{MaxIssues: 3}, false},
		{"three issues over a limit of two", three, schemaflux.CritiqueOptions{MaxIssues: 2}, true},
		{"a major issue under the major filter", "[" + critiqueIssue("major") + "]", schemaflux.CritiqueOptions{SeverityFilter: "major"}, false},
		{"a minor issue under the major filter", "[" + critiqueIssue("minor") + "]", schemaflux.CritiqueOptions{SeverityFilter: "major"}, true},
		{"a minor issue under all", "[" + critiqueIssue("minor") + "]", schemaflux.CritiqueOptions{SeverityFilter: "all"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Critique(operationClient(answeringWith(critiqueReport(tc.issues))), "e", critiqueCriteria, tc.opts)
			if !tc.refused {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			wantError(t, err, schemaflux.ErrInvalidAnswer, "critique")
			if res.Verdict != "" || res.Issues != nil {
				t.Errorf("a refused answer left %+v beside the error", res)
			}
		})
	}
}

func TestCritiqueRefusesAnAnswerThatDoesNotScoreEveryCriterionOrIsNotComplete(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"a criterion left unscored":      `{"overall_score":0.7,"criteria_scores":{"clarity":0.8},"issues":[],"summary":"s","top_priorities":[]}`,
		"no criteria scores":             `{"overall_score":0.7,"issues":[],"summary":"s","top_priorities":[]}`,
		"a score off the scale":          `{"overall_score":0.7,"criteria_scores":{"clarity":1.8,"argument":0.6},"issues":[],"summary":"s","top_priorities":[]}`,
		"an overall score off the scale": `{"overall_score":7,"criteria_scores":{"clarity":0.8,"argument":0.6},"issues":[],"summary":"s","top_priorities":[]}`,
		"a null issues list":             `{"overall_score":0.7,"criteria_scores":{"clarity":0.8,"argument":0.6},"issues":null,"summary":"s","top_priorities":[]}`,
		"no issues list":                 `{"overall_score":0.7,"criteria_scores":{"clarity":0.8,"argument":0.6},"summary":"s","top_priorities":[]}`,
		"no summary":                     `{"overall_score":0.7,"criteria_scores":{"clarity":0.8,"argument":0.6},"issues":[],"top_priorities":[]}`,
		"text":                           `The essay is good.`,
		"a bare list":                    `[]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Critique(operationClient(answeringWith(body)), "e", critiqueCriteria)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "critique")
			if res.Verdict != "" || res.Subject != "" || res.CriteriaScores != nil || res.TopPriorities != nil {
				t.Errorf("a refused answer left %+v beside the error", res)
			}
		})
	}
}

func TestCritiqueRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := map[string]schemaflux.CritiqueOptions{
		"a style that is not one":           {Style: "savage"},
		"a severity filter that is not one": {SeverityFilter: "trivial"},
		"a negative MaxIssues":              {MaxIssues: -1},
		"a negative output ceiling":         {Options: schemaflux.Options{MaxOutputTokens: -1}},
		"a tier that is not one":            {Options: schemaflux.Options{Tier: 9}},
		"a mode that is not one":            {Options: schemaflux.Options{Mode: 9}},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(critiqueReport("[]"))
			_, err := schemaflux.Critique(operationClient(model), "e", critiqueCriteria, opts)
			wantError(t, err, schemaflux.ErrConfiguration, "critique")
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent", model.Calls())
			}
		})
	}
}

func TestCritiqueNamesTheFieldOfABadOptionAndNotItsValue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		opts schemaflux.CritiqueOptions
		want string
	}{
		{schemaflux.CritiqueOptions{Style: "CANARY-style"}, "Style is not one of constructive, harsh and balanced"},
		{schemaflux.CritiqueOptions{SeverityFilter: "CANARY-filter"}, "SeverityFilter is not one of all, critical, major and minor"},
		{schemaflux.CritiqueOptions{MaxIssues: -4}, "MaxIssues must not be negative"},
	}
	for _, tc := range cases {
		_, err := schemaflux.Critique(operationClient(answeringWith(critiqueReport("[]"))), "e", critiqueCriteria, tc.opts)
		e := wantError(t, err, schemaflux.ErrConfiguration, "critique")
		if e.Message != tc.want {
			t.Errorf("message = %q, want %q", e.Message, tc.want)
		}
		for _, text := range chainText(err) {
			if strings.Contains(text, "CANARY") {
				t.Errorf("the value reached %q", text)
			}
		}
	}
}

func TestCritiqueWithANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	var nilContext context.Context
	_, err := schemaflux.Critique(nilContext, "e", critiqueCriteria)
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "critique"); e.Message != "the context is nil" {
		t.Errorf("message = %q", e.Message)
	}
}

func TestCritiqueMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later value wins and a later zero leaves the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(critiqueReport("[]"))
		_, err := schemaflux.Critique(operationClient(model), "e", critiqueCriteria,
			schemaflux.CritiqueOptions{Style: "harsh", Domain: "first", Options: schemaflux.Options{Model: "first-model"}},
			schemaflux.CritiqueOptions{Options: schemaflux.Options{Steering: "later"}},
			schemaflux.CritiqueOptions{Style: "balanced"})
		if err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "Style: Balance positive and negative")
		wantText(t, req, `Domain: "first"`)
		wantText(t, req, "later")
		if req.Model != "first-model" {
			t.Errorf("model = %q", req.Model)
		}
	})
	t.Run("a bool turned on stays on", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(critiqueReport("[]"))
		_, err := schemaflux.Critique(operationClient(model), "e", critiqueCriteria, schemaflux.CritiqueOptions{OmitFixes: true}, schemaflux.CritiqueOptions{})
		if err != nil {
			t.Fatal(err)
		}
		wantNoText(t, model.Last(), "Include specific fixes")
	})
	t.Run("a refused setting in a later value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Critique(operationClient(answeringWith(critiqueReport("[]"))), "e", critiqueCriteria, schemaflux.CritiqueOptions{}, schemaflux.CritiqueOptions{Style: "savage"})
		wantError(t, err, schemaflux.ErrConfiguration, "critique")
	})
}

func TestCritiqueReturnsAnErrorTheCallerCanClassifyWhenTheProviderFails(t *testing.T) {
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
			res, err := schemaflux.Critique(operationClient(&model), "e", critiqueCriteria)
			wantError(t, err, tc.err.Kind, "critique")
			if res.Verdict != "" || res.Subject != "" {
				t.Errorf("a failed call left %+v beside the error", res)
			}
		})
	}
}

func TestCritiqueOfAValueJSONCannotWriteIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	model := answeringWith(critiqueReport("[]"))
	_, err := schemaflux.Critique(operationClient(model), map[string]any{"c": make(chan int)}, critiqueCriteria)
	wantError(t, err, schemaflux.ErrInvalidRequest, "critique")
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

// The refusal of an issue outside the severity filter is made by the internal
// call, which writes the issue's severity, the model's own word, into its message.
// Until that message names the position only, this test stays skipped.
func TestCritiqueSeverityFilterRefusalDoesNotQuoteTheModelsSeverity(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-sev"
	body := critiqueReport(`[{"criterion":"clarity","severity":"` + canary + `","description":"d"}]`)
	_, err := schemaflux.Critique(operationClient(answeringWith(body)), "e", critiqueCriteria, schemaflux.CritiqueOptions{SeverityFilter: "critical"})
	wantError(t, err, schemaflux.ErrInvalidAnswer, "critique")
	for _, text := range chainText(err) {
		if strings.Contains(text, canary) {
			t.Errorf("the model's severity reached %q", text)
		}
	}
}
