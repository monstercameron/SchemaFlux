package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

const critiqueMockResponse = `{
	"overall_score": 0.55,
	"criteria_scores": {"clarity": 0.6, "evidence": 0.5},
	"issues": [
		{"criterion": "evidence", "severity": "critical", "description": "no citations", "location": "paragraph 2", "fix": "add a source"},
		{"criterion": "clarity", "severity": "minor", "description": "run-on sentence", "suggestion": "split into two sentences"}
	],
	"positives": [
		{"criterion": "structure", "description": "clear intro"}
	],
	"summary": "Needs citations, otherwise readable.",
	"top_priorities": ["add citations"]
}`

func installCritiqueResponse(t *testing.T) {
	t.Helper()
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return critiqueMockResponse, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
}

// TestCritiqueWithModelMatchesLegacyCritique proves the collapse is
// behavior-preserving: for the same response, CritiqueWithModel's Issues
// describe the same findings the deprecated Critique's Issues did, and its
// Verdict reflects the critical issue present.
func TestCritiqueWithModelMatchesLegacyCritique(t *testing.T) {
	installCritiqueResponse(t)

	opts := NewCritiqueOptions().WithCriteria([]string{"clarity", "evidence"})

	legacy, err := Critique("some essay", opts)
	if err != nil {
		t.Fatalf("Critique failed: %v", err)
	}
	judged, err := CritiqueWithModel("some essay", opts)
	if err != nil {
		t.Fatalf("CritiqueWithModel failed: %v", err)
	}

	if len(judged.Issues) != len(legacy.Issues) {
		t.Fatalf("expected %d issues, got %d", len(legacy.Issues), len(judged.Issues))
	}
	if judged.Verdict != types.VerdictFail {
		t.Errorf("expected VerdictFail (a critical issue is present), got %v", judged.Verdict)
	}

	var foundFix, foundSuggestion bool
	for _, issue := range judged.Issues {
		switch issue.Subject {
		case "evidence":
			foundFix = true
			if issue.Suggestion != "add a source" {
				t.Errorf("expected the fix to win over an absent suggestion, got %q", issue.Suggestion)
			}
			if issue.Severity != "critical" {
				t.Errorf("expected severity 'critical', got %q", issue.Severity)
			}
		case "clarity":
			foundSuggestion = true
			if issue.Suggestion != "split into two sentences" {
				t.Errorf("expected the suggestion when no fix is present, got %q", issue.Suggestion)
			}
		}
	}
	if !foundFix || !foundSuggestion {
		t.Fatalf("expected both issues represented, got %+v", judged.Issues)
	}

	// ModelOverallScore is a claim, reachable but kept apart from Verdict.
	if judged.ModelConfidence != legacy.ModelOverallScore {
		t.Errorf("expected ModelConfidence %v, got %v", legacy.ModelOverallScore, judged.ModelConfidence)
	}
	if judged.ModelClaims["top_priorities"] == nil {
		t.Error("expected top_priorities to travel in ModelClaims")
	}
}

// TestCritiqueWithModelVerdictWithoutCritical proves Verdict is Mixed, not
// Fail, when issues exist but none is critical -- Fail is reserved for a
// critical finding, matching how a caller would actually want to gate on
// this.
func TestCritiqueWithModelVerdictWithoutCritical(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{
			"overall_score": 0.8,
			"criteria_scores": {"clarity": 0.8},
			"issues": [{"criterion": "clarity", "severity": "minor", "description": "typo"}],
			"summary": "Mostly good.",
			"top_priorities": []
		}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	opts := NewCritiqueOptions().WithCriteria([]string{"clarity"})
	judged, err := CritiqueWithModel("text", opts)
	if err != nil {
		t.Fatalf("CritiqueWithModel failed: %v", err)
	}
	if judged.Verdict != types.VerdictMixed {
		t.Errorf("expected VerdictMixed, got %v", judged.Verdict)
	}
}

func TestCritiqueOptions(t *testing.T) {
	t.Run("NewCritiqueOptions creates valid defaults", func(t *testing.T) {
		opts := NewCritiqueOptions().WithCriteria([]string{"clarity"})
		if err := opts.Validate(); err != nil {
			t.Errorf("default options with criteria should be valid: %v", err)
		}
	})

	t.Run("Validate requires criteria or rubric", func(t *testing.T) {
		opts := CritiqueOptions{}
		if err := opts.Validate(); err == nil {
			t.Error("expected error for empty criteria and rubric")
		}
	})

	t.Run("Validate rejects invalid severity filter", func(t *testing.T) {
		for _, filter := range []string{"invalid", "Major", "high"} {
			opts := NewCritiqueOptions().WithCriteria([]string{"test"}).WithSeverityFilter(filter)
			if err := opts.Validate(); err == nil {
				t.Errorf("severity filter %q was accepted", filter)
			}
		}
	})

	t.Run("Validate rejects invalid style", func(t *testing.T) {
		opts := NewCritiqueOptions().
			WithCriteria([]string{"test"}).
			WithStyle("invalid")
		if err := opts.Validate(); err == nil {
			t.Error("expected error for invalid style")
		}
	})
}

func TestCritiqueGivesFeedbackAgainstTheCriteriaAsked(t *testing.T) {
	coreAnswer(t, `{"overall_score":0.4,"criteria_scores":{"argument_strength":0.3,"evidence":0.4,"clarity":0.5},
		"issues":[{"criterion":"evidence","severity":"major","description":"none"}],"summary":"weak","top_priorities":[]}`)

	opts := NewCritiqueOptions().WithCriteria([]string{"argument_strength", "evidence", "clarity"})
	result, err := Critique("Climate change is a big problem.", opts)
	if err != nil {
		t.Fatalf("Critique: %v", err)
	}
	if result.ModelOverallScore != 0.4 || len(result.Issues) != 1 || len(result.CriteriaScores) != 3 {
		t.Errorf("result = %+v", result)
	}
}

func TestCritiqueDoesNotReadAMissingIssuesListAsNoIssues(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"only a summary", `{"summary":"looks fine"}`},
		{"only an overall score", `{"overall_score":0.9}`},
		{"issues is null", `{"overall_score":0.9,"issues":null,"criteria_scores":{"clarity":0.9}}`},
		{"issues is an object", `{"issues":{},"criteria_scores":{"clarity":0.9}}`},
		{"issues is a string", `{"issues":"none","criteria_scores":{"clarity":0.9}}`},
		{"an unrelated object", `{"other":1}`},
		{"an empty object", `{}`},
		{"not JSON", `sorry`},
		{"only positives", `{"positives":[]}`},
		{"only top priorities", `{"top_priorities":["x"]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			coreAnswer(t, c.body)
			judged, err := CritiqueWithModel("text", NewCritiqueOptions().WithCriteria([]string{"clarity"}))
			if err == nil {
				t.Fatalf("a verdict of %v was given for an answer with no issues list", judged.Verdict)
			}
			if judged.Verdict != types.VerdictUnknown && judged.Verdict != "" {
				t.Errorf("verdict = %v beside an error", judged.Verdict)
			}
			result, err := Critique("text", NewCritiqueOptions().WithCriteria([]string{"clarity"}))
			if err == nil || result.Metadata != nil || result.Summary != "" {
				t.Errorf("Critique = %+v, %v", result, err)
			}
		})
	}

	// An empty list is an answer: nothing was found.
	coreAnswer(t, `{"overall_score":0.9,"criteria_scores":{"clarity":0.9},"issues":[],"summary":"fine","top_priorities":[]}`)
	judged, err := CritiqueWithModel("text", NewCritiqueOptions().WithCriteria([]string{"clarity"}))
	if err != nil || judged.Verdict != types.VerdictPass {
		t.Errorf("an empty issues list: verdict %v, err %v", judged.Verdict, err)
	}
}

func TestCritiqueChecksTheScores(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		criteria  []string
		rubric    map[string]string
		wantError bool
	}{
		{"every criterion scored", `{"overall_score":0.5,"criteria_scores":{"clarity":0.8,"evidence":0.5},"issues":[],"summary":"s","top_priorities":[]}`, []string{"clarity", "evidence"}, nil, false},
		{"a criterion skipped", `{"overall_score":0.5,"criteria_scores":{"clarity":0.8},"issues":[],"summary":"s","top_priorities":[]}`, []string{"clarity", "evidence"}, nil, true},
		{"no scores at all", `{"overall_score":0.5,"issues":[],"summary":"s","top_priorities":[]}`, []string{"clarity"}, nil, true},
		{"scores matched without regard to case and space", `{"overall_score":0.5,"criteria_scores":{" Clarity ":0.8},"issues":[],"summary":"s","top_priorities":[]}`, []string{"clarity"}, nil, false},
		{"a rubric entry skipped", `{"overall_score":0.5,"criteria_scores":{"clarity":0.8},"issues":[],"summary":"s","top_priorities":[]}`, []string{"clarity"}, map[string]string{"naming": "are names clear"}, true},
		{"a rubric entry scored", `{"overall_score":0.5,"criteria_scores":{"naming":0.8},"issues":[],"summary":"s","top_priorities":[]}`, nil, map[string]string{"naming": "are names clear"}, false},
		{"a criterion score above one", `{"overall_score":0.5,"criteria_scores":{"clarity":7},"issues":[],"summary":"s","top_priorities":[]}`, []string{"clarity"}, nil, true},
		{"a negative criterion score", `{"overall_score":0.5,"criteria_scores":{"clarity":-0.2},"issues":[],"summary":"s","top_priorities":[]}`, []string{"clarity"}, nil, true},
		{"an overall score above one", `{"overall_score":85,"criteria_scores":{"clarity":0.8},"issues":[],"summary":"s","top_priorities":[]}`, []string{"clarity"}, nil, true},
		{"an overall score of exactly one", `{"overall_score":1,"criteria_scores":{"clarity":1},"issues":[],"summary":"s","top_priorities":[]}`, []string{"clarity"}, nil, false},
		{"no overall score is refused, not read as zero", `{"criteria_scores":{"clarity":0.8},"issues":[],"summary":"s","top_priorities":[]}`, []string{"clarity"}, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			coreAnswer(t, c.body)
			opts := NewCritiqueOptions()
			opts.Criteria, opts.Rubric = c.criteria, c.rubric
			result, err := Critique("text", opts)
			if c.wantError {
				if err == nil {
					t.Fatalf("accepted: %+v", result)
				}
				return
			}
			if err != nil {
				t.Fatalf("Critique: %v", err)
			}
		})
	}
}

func TestCritiqueStripsWhatWasNotAskedFor(t *testing.T) {
	coreAnswer(t, `{"overall_score":0.5,"criteria_scores":{"clarity":0.8},
		"issues":[{"criterion":"clarity","severity":"minor","description":"typo","suggestion":"S","fix":"F"}],
		"positives":[{"criterion":"clarity","description":"clear"}],"summary":"s","top_priorities":[]}`)

	cases := []struct {
		name                    string
		opts                    CritiqueOptions
		wantSuggestion, wantFix string
		wantPositives           int
	}{
		{"all on", NewCritiqueOptions(), "S", "F", 1},
		{"suggestions off", NewCritiqueOptions().WithIncludeSuggestions(false), "", "F", 1},
		{"fixes off", NewCritiqueOptions().WithIncludeFixes(false), "S", "", 1},
		{"positives off", NewCritiqueOptions().WithIncludePositives(false), "S", "F", 0},
		{"all off", NewCritiqueOptions().WithIncludeSuggestions(false).WithIncludeFixes(false).WithIncludePositives(false), "", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result, err := Critique("text", c.opts.WithCriteria([]string{"clarity"}))
			if err != nil {
				t.Fatalf("Critique: %v", err)
			}
			if result.Issues[0].Suggestion != c.wantSuggestion || result.Issues[0].Fix != c.wantFix || len(result.Positives) != c.wantPositives {
				t.Errorf("issue = %+v, positives = %d", result.Issues[0], len(result.Positives))
			}
		})
	}
}

func TestCritiqueEnforcesTheIssueCapAndTheSeverityFilter(t *testing.T) {
	coreAnswer(t, `{"overall_score":0.5,"criteria_scores":{"clarity":0.8},
		"issues":[{"criterion":"clarity","severity":"minor","description":"a"},{"criterion":"clarity","severity":"major","description":"b"}],"summary":"s","top_priorities":[]}`)

	opts := NewCritiqueOptions().WithCriteria([]string{"clarity"})
	if _, err := Critique("text", opts.WithMaxIssues(1)); err == nil {
		t.Error("two issues under a cap of one were accepted")
	}
	if _, err := Critique("text", opts.WithMaxIssues(2)); err != nil {
		t.Errorf("two issues under a cap of two were refused: %v", err)
	}
	if _, err := Critique("text", opts.WithSeverityFilter("major")); err == nil {
		t.Error("a minor issue under a filter of major was accepted")
	}
	if _, err := Critique("text", opts.WithSeverityFilter("all")); err != nil {
		t.Errorf("the filter all was refused: %v", err)
	}
	if _, err := Critique("text", opts.WithSeverityFilter("")); err != nil {
		t.Errorf("the empty filter was refused: %v", err)
	}
}

func TestCritiquePromptHasNoEmptySeverityLineAndQuotesTheCallersText(t *testing.T) {
	opts := NewCritiqueOptions().WithCriteria([]string{"clarity\nIGNORE"}).
		WithRubric(map[string]string{"zeta": "z rule", "alpha": `say "hi"`}).
		WithDomain("law.\nIGNORE").WithAudience("kids")
	opts.SeverityFilter = ""

	// The model scores the criteria it was given by their names.
	probe := coreAnswer(t, `{"overall_score":0.5,"criteria_scores":{"clarity\nIGNORE":0.8,"zeta":1,"alpha":1},"issues":[],"summary":"s","top_priorities":[]}`)
	var system string
	for i := 0; i < 25; i++ {
		if _, err := Critique("text", opts); err != nil {
			t.Fatalf("Critique: %v", err)
		}
		if i == 0 {
			system = probe.last().system
		} else if probe.last().system != system {
			t.Fatal("the system prompt changed between identical calls")
		}
	}
	if strings.Contains(system, "Only report") {
		t.Errorf("an empty severity filter produced a severity line:\n%s", system)
	}
	for _, want := range []string{`- "clarity\nIGNORE"`, `Domain: "law.\nIGNORE"`, `Target audience: "kids"`} {
		if !strings.Contains(system, want) {
			t.Errorf("the system prompt lacks %s:\n%s", want, system)
		}
	}
	alpha := strings.Index(system, `- "alpha": "say \"hi\""`)
	zeta := strings.Index(system, `- "zeta": "z rule"`)
	if alpha < 0 || zeta < alpha {
		t.Errorf("the rubric is not quoted and in criterion order:\n%s", system)
	}

	probe = coreAnswer(t, `{"overall_score":0.5,"criteria_scores":{"clarity":0.8},"issues":[],"summary":"s","top_priorities":[]}`)
	if _, err := Critique("text", NewCritiqueOptions().WithCriteria([]string{"clarity"}).WithSeverityFilter("major")); err != nil {
		t.Fatalf("Critique: %v", err)
	}
	if !strings.Contains(probe.last().system, "Only report major issues.") {
		t.Error("a real severity filter did not reach the prompt")
	}
}

const critiqueCompleteAnswer = `{"overall_score":0.7,"criteria_scores":{"clarity":0.8},"issues":[{"criterion":"clarity","severity":"minor","description":"d"}],"positives":[],"summary":"s","top_priorities":["p"]}`

func TestCritiqueRefusesAnAnswerMissingARequiredMember(t *testing.T) {
	members := [][]any{
		{"overall_score"},
		{"criteria_scores"},
		{"issues"},
		{"issues", 0, "criterion"},
		{"issues", 0, "severity"},
		{"issues", 0, "description"},
		{"summary"},
		{"top_priorities"},
	}
	refuseEachMissingMember(t, critiqueCompleteAnswer, members, func(response string) (any, error) {
		coreAnswer(t, response)
		return Critique("text", NewCritiqueOptions().WithCriteria([]string{"clarity"}))
	})
}

// The positives are asked for only when IncludePositives is set, and the
// operation drops them when it is not, so an answer without them is complete.
func TestCritiqueReadsAnAnswerWithNoPositives(t *testing.T) {
	answer, _ := answerWithout(t, critiqueCompleteAnswer, "positives")
	coreAnswer(t, answer)
	result, err := Critique("text", NewCritiqueOptions().WithCriteria([]string{"clarity"}))
	if err != nil || result.Summary != "s" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestCritiqueRefusesANullInTheAnswerWhereNothingCanBeNull(t *testing.T) {
	for name, body := range map[string]string{
		"overall_score":         strings.Replace(critiqueCompleteAnswer, `"overall_score":0.7`, `"overall_score":null`, 1),
		"summary":               strings.Replace(critiqueCompleteAnswer, `"summary":"s"`, `"summary":null`, 1),
		"issues[0].severity":    strings.Replace(critiqueCompleteAnswer, `"severity":"minor"`, `"severity":null`, 1),
		"issues[0].description": strings.Replace(critiqueCompleteAnswer, `"description":"d"`, `"description":null`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			coreAnswer(t, body)
			result, err := Critique("text", NewCritiqueOptions().WithCriteria([]string{"clarity"}))
			wantKind(t, err, types.KindSchemaViolation)
			if !strings.Contains(err.Error(), name+" is null") || result.Summary != "" || result.Issues != nil {
				t.Errorf("result = %+v, err = %v", result, err)
			}
		})
	}
}

func TestCritiqueRefusalsBeforeACallHaveAKind(t *testing.T) {
	refuse := func(change func(*CritiqueOptions)) func() error {
		return func() error {
			options := NewCritiqueOptions().WithCriteria([]string{"clarity"})
			change(&options)
			_, err := Critique("an essay", options)
			return err
		}
	}
	checkRefusalsBeforeACall(t, []refusalBeforeACall{
		{"no criteria and no rubric", func() error {
			_, err := Critique("an essay", NewCritiqueOptions())
			return err
		}, "invalid options: at least one criterion or rubric entry is required", types.KindConfiguration},
		{"a severity filter nobody knows", refuse(func(o *CritiqueOptions) { o.SeverityFilter = "trivial" }),
			"invalid options: invalid severity filter: want all, critical, major or minor", types.KindConfiguration},
		{"a style nobody knows", refuse(func(o *CritiqueOptions) { o.Style = "gentle" }),
			"invalid options: invalid style: gentle", types.KindConfiguration},
		{"a threshold above one on the common half", refuse(func(o *CritiqueOptions) { o.CommonOptions.Threshold = 4 }),
			"invalid options: threshold must be between 0 and 1, got 4.000000", types.KindConfiguration},
		{"a nil input", func() error {
			_, err := Critique[any](nil, NewCritiqueOptions().WithCriteria([]string{"clarity"}))
			return err
		}, "failed to normalize input: input is nil", types.KindInvalidRequest},
		{"CritiqueWithModel with no criteria", func() error {
			_, err := CritiqueWithModel("an essay", NewCritiqueOptions())
			return err
		}, "invalid options: at least one criterion or rubric entry is required", types.KindConfiguration},
		{"CritiqueWithModel with a style nobody knows", func() error {
			_, err := CritiqueWithModel("an essay", NewCritiqueOptions().WithCriteria([]string{"c"}).WithStyle("gentle"))
			return err
		}, "invalid options: invalid style: gentle", types.KindConfiguration},
		{"CritiqueWithModel with a nil input", func() error {
			_, err := CritiqueWithModel[any](nil, NewCritiqueOptions().WithCriteria([]string{"c"}))
			return err
		}, "failed to normalize input: input is nil", types.KindInvalidRequest},
	})
}
