package schemaflux_test

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

const (
	rewriteInput  = "hey team, the thing broke again lol"
	rewriteAnswer = "Dear team, the system failed again."
	rewriteDetail = `{"text":"Dear team, the system failed again.","changes_made":["formal greeting","removed slang"],"tone_achieved":"formal","confidence":0.9}`
)

func init() {
	payload := func(canary string) schemaflux.RewriteOptions {
		return schemaflux.RewriteOptions{
			Options: schemaflux.Options{Steering: "steer " + canary}, Tone: "tone " + canary, Audience: "who " + canary,
			StyleGuide: "style " + canary, Changes: []string{"change " + canary}, AvoidWords: []string{"avoid" + canary},
		}
	}
	textForms = append(textForms,
		textForm{
			op: "rewrite", plain: true, body: rewriteAnswer, badBody: "  ",
			call: func(ctx context.Context, canary string) error {
				_, err := schemaflux.Rewrite(ctx, rewriteInput+" "+canary, payload(canary))
				return err
			},
			refused: func(ctx context.Context, canary string) error {
				o := payload(canary)
				o.Formality = 11
				_, err := schemaflux.Rewrite(ctx, rewriteInput+" "+canary, o)
				return err
			},
		},
		textForm{
			op: "rewritedetail", level: schemaflux.ContractJSONWellFormed, body: rewriteDetail, badBody: "not json",
			call: func(ctx context.Context, canary string) error {
				_, err := schemaflux.RewriteDetail(ctx, rewriteInput+" "+canary, payload(canary))
				return err
			},
			refused: func(ctx context.Context, canary string) error {
				o := payload(canary)
				o.Formality = -3
				_, err := schemaflux.RewriteDetail(ctx, rewriteInput+" "+canary, o)
				return err
			},
		})
}

func TestRewriteReturnsTheModelsTextTrimmedAsAPlainRequest(t *testing.T) {
	t.Parallel()
	model := answeringWith("  \n" + rewriteAnswer + "\n ")
	got, err := schemaflux.Rewrite(operationClient(model), rewriteInput)
	if err != nil || got != rewriteAnswer {
		t.Fatalf("Rewrite = %q, %v", got, err)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	req := model.Last()
	wantText(t, req, "text rewriting expert")
	wantText(t, req, "Rewrite this text:")
	wantText(t, req, rewriteInput)
	if req.Format != "text" || req.Schema != nil {
		t.Errorf("format %q schema %v: a rewrite is the model's text, with no schema", req.Format, req.Schema != nil)
	}
}

func TestRewriteWithNoOptionsKeepsTheFactsAtFormalityFiveAtTheFastTier(t *testing.T) {
	t.Parallel()
	model := answeringWith(rewriteAnswer)
	if _, err := schemaflux.Rewrite(operationClient(model), rewriteInput); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	wantText(t, req, "Preserve all factual information")
	for _, absent := range []string{"Target tone", "Formality level", "Target audience", "Follow style", "Make these changes", "Avoid:", "Include:"} {
		wantNoText(t, req, absent)
	}
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" || req.WebSearch || req.Effort != "" {
		t.Errorf("temperature %v ceiling %d model %q web %v effort %q", req.Temperature, req.MaxTokens, req.Model, req.WebSearch, req.Effort)
	}
}

func TestRewriteEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.RewriteOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Tone", schemaflux.RewriteOptions{Tone: "formal"}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Target tone: formal") }},
		{"Formality 8", schemaflux.RewriteOptions{Formality: 8}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Formality level: 8/10") }},
		{"Formality 1", schemaflux.RewriteOptions{Formality: 1}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Formality level: 1/10") }},
		{"Formality 10", schemaflux.RewriteOptions{Formality: 10}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Formality level: 10/10") }},
		{"Formality 5 is the default and says nothing", schemaflux.RewriteOptions{Formality: 5}, func(t *testing.T, req schemaflux.Request) { wantNoText(t, req, "Formality level") }},
		{"NoPreserveFacts", schemaflux.RewriteOptions{NoPreserveFacts: true}, func(t *testing.T, req schemaflux.Request) { wantNoText(t, req, "Preserve all factual information") }},
		{"Audience", schemaflux.RewriteOptions{Audience: "executives"}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Target audience: executives") }},
		{"StyleGuide", schemaflux.RewriteOptions{StyleGuide: "AP"}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Follow style: AP") }},
		{"Changes", schemaflux.RewriteOptions{Changes: []string{"shorten", "drop jargon"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Make these changes: shorten, drop jargon")
		}},
		{"AvoidWords", schemaflux.RewriteOptions{AvoidWords: []string{"synergy", "leverage"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Avoid: synergy, leverage")
		}},
		{"IncludeWords", schemaflux.RewriteOptions{IncludeWords: []string{"system"}}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Include: system") }},
		{"Steering", schemaflux.RewriteOptions{Options: schemaflux.Options{Steering: "be brief"}}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "be brief") }},
		{"Model", schemaflux.RewriteOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"TierSmart", schemaflux.RewriteOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.RewriteOptions{Options: schemaflux.Options{MaxOutputTokens: 66}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 66 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.RewriteOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"Effort", schemaflux.RewriteOptions{Options: schemaflux.Options{Effort: schemaflux.EffortLow}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" || req.EffortPreferred {
				t.Errorf("effort %q preferred %v", req.Effort, req.EffortPreferred)
			}
		}},
		{"ModeCreative", schemaflux.RewriteOptions{Options: schemaflux.Options{Mode: schemaflux.ModeCreative}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.7) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(rewriteAnswer)
			if _, err := schemaflux.Rewrite(operationClient(model), rewriteInput, tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestRewriteRefusesAnAnswerThatBreaksTheWordListsMatchingWholeWordsWithoutRegardToCase(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		opts    schemaflux.RewriteOptions
		answer  string
		refused bool
	}{
		{"an avoided word that appears", schemaflux.RewriteOptions{AvoidWords: []string{"system"}}, rewriteAnswer, true},
		{"an avoided word in another case", schemaflux.RewriteOptions{AvoidWords: []string{"SYSTEM"}}, rewriteAnswer, true},
		{"an avoided word that is only part of a word", schemaflux.RewriteOptions{AvoidWords: []string{"sys"}}, rewriteAnswer, false},
		{"an avoided phrase that appears", schemaflux.RewriteOptions{AvoidWords: []string{"failed again"}}, rewriteAnswer, true},
		{"an avoided word that is absent", schemaflux.RewriteOptions{AvoidWords: []string{"synergy"}}, rewriteAnswer, false},
		{"a required word that is missing", schemaflux.RewriteOptions{IncludeWords: []string{"apology"}}, rewriteAnswer, true},
		{"a required word that is present in another case", schemaflux.RewriteOptions{IncludeWords: []string{"DEAR"}}, rewriteAnswer, false},
		{"a required word that is only part of a word", schemaflux.RewriteOptions{IncludeWords: []string{"fail"}}, rewriteAnswer, true},
		{"both lists met", schemaflux.RewriteOptions{AvoidWords: []string{"lol"}, IncludeWords: []string{"team"}}, rewriteAnswer, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Rewrite(operationClient(answeringWith(tc.answer)), rewriteInput, tc.opts)
			if !tc.refused {
				if err != nil || got != tc.answer {
					t.Fatalf("Rewrite = %q, %v", got, err)
				}
				return
			}
			wantError(t, err, schemaflux.ErrInvalidAnswer, "rewrite")
			if got != "" {
				t.Errorf("a refused rewrite left %q beside the error", got)
			}
		})
	}
}

func TestRewriteRefusesAnEmptyAnswer(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{"empty": "", "spaces": "   ", "newlines and tabs": "\n\t\n"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Rewrite(operationClient(answeringWith(body)), rewriteInput)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "rewrite")
			if got != "" {
				t.Errorf("an empty answer left %q beside the error", got)
			}
		})
	}
}

func TestRewriteRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	const formality = "Formality must be a whole number from 1 to 10, or zero for the default"
	cases := map[string]struct {
		opts    schemaflux.RewriteOptions
		message string
	}{
		"a formality of 11":             {schemaflux.RewriteOptions{Formality: 11}, formality},
		"a negative formality":          {schemaflux.RewriteOptions{Formality: -1}, formality},
		"a very large formality":        {schemaflux.RewriteOptions{Formality: 1 << 30}, formality},
		"a negative output ceiling":     {schemaflux.RewriteOptions{Options: schemaflux.Options{MaxOutputTokens: -1}}, "MaxOutputTokens must not be negative"},
		"a tier that is not one":        {schemaflux.RewriteOptions{Options: schemaflux.Options{Tier: 5}}, "Tier is not one of TierSmart, TierFast and TierQuick"},
		"a mode that is not one":        {schemaflux.RewriteOptions{Options: schemaflux.Options{Mode: 5}}, "Mode is not one of ModeStrict, ModeTransform and ModeCreative"},
		"a bad setting among good ones": {schemaflux.RewriteOptions{Tone: "formal", Formality: 12, Audience: "all"}, formality},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(rewriteAnswer)
			got, err := schemaflux.Rewrite(operationClient(model), rewriteInput, tc.opts)
			e := wantError(t, err, schemaflux.ErrConfiguration, "rewrite")
			if e.Message != tc.message || got != "" || model.Calls() != 0 {
				t.Errorf("message %q, value %q, %d requests", e.Message, got, model.Calls())
			}
		})
	}
}

func TestRewriteMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later value wins and a later zero leaves the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(rewriteAnswer)
		_, err := schemaflux.Rewrite(operationClient(model), rewriteInput,
			schemaflux.RewriteOptions{Tone: "casual", Formality: 3, Audience: "friends", Options: schemaflux.Options{Model: "first"}},
			schemaflux.RewriteOptions{Formality: 9},
			schemaflux.RewriteOptions{})
		if err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "Formality level: 9/10")
		wantText(t, req, "Target tone: casual")
		wantText(t, req, "Target audience: friends")
		if req.Model != "first" {
			t.Errorf("model = %q", req.Model)
		}
	})
	t.Run("a bool turned on stays on", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(rewriteAnswer)
		_, err := schemaflux.Rewrite(operationClient(model), rewriteInput, schemaflux.RewriteOptions{NoPreserveFacts: true}, schemaflux.RewriteOptions{})
		if err != nil {
			t.Fatal(err)
		}
		wantNoText(t, model.Last(), "Preserve all factual information")
	})
	t.Run("a later list replaces the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(rewriteAnswer)
		_, err := schemaflux.Rewrite(operationClient(model), rewriteInput,
			schemaflux.RewriteOptions{Changes: []string{"first"}}, schemaflux.RewriteOptions{Changes: []string{"second"}})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "Make these changes: second")
		wantNoText(t, model.Last(), "first")
	})
	t.Run("a bad setting in a later value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Rewrite(operationClient(answeringWith(rewriteAnswer)), rewriteInput,
			schemaflux.RewriteOptions{}, schemaflux.RewriteOptions{Formality: 40})
		wantError(t, err, schemaflux.ErrConfiguration, "rewrite")
	})
}

func TestRewriteDetailReturnsTheRewriteWithWhatTheModelReported(t *testing.T) {
	t.Parallel()
	got, err := schemaflux.RewriteDetail(operationClient(answeringWith(rewriteDetail)), rewriteInput)
	if err != nil {
		t.Fatal(err)
	}
	want := schemaflux.Rewritten{
		Text: rewriteAnswer, ChangesMade: []string{"formal greeting", "removed slang"}, ToneAchieved: "formal", ModelConfidence: 0.9,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestRewriteDetailRefusesAnAnswerThatIsNotARewrite(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"text":                       `Dear team.`,
		"a list":                     `["a"]`,
		"no text":                    `{"changes_made":[],"tone_achieved":"x","confidence":0.5}`,
		"an empty text":              `{"text":"","changes_made":[],"tone_achieved":"x","confidence":0.5}`,
		"a text that is a number":    `{"text":5,"changes_made":[],"tone_achieved":"x","confidence":0.5}`,
		"no confidence":              `{"text":"x","changes_made":[],"tone_achieved":"x"}`,
		"a confidence off the scale": `{"text":"x","changes_made":[],"tone_achieved":"x","confidence":4}`,
		"empty":                      ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.RewriteDetail(operationClient(answeringWith(body)), rewriteInput)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "rewritedetail")
			if !reflect.DeepEqual(got, schemaflux.Rewritten{}) {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestRewriteDetailChecksTheWordListsToo(t *testing.T) {
	t.Parallel()
	_, err := schemaflux.RewriteDetail(operationClient(answeringWith(rewriteDetail)), rewriteInput, schemaflux.RewriteOptions{AvoidWords: []string{"team"}})
	wantError(t, err, schemaflux.ErrInvalidAnswer, "rewritedetail")
	_, err = schemaflux.RewriteDetail(operationClient(answeringWith(rewriteDetail)), rewriteInput, schemaflux.RewriteOptions{IncludeWords: []string{"apology"}})
	wantError(t, err, schemaflux.ErrInvalidAnswer, "rewritedetail")
}

func TestRewriteDetailAsksAnotherQuestionThanRewriteDoes(t *testing.T) {
	t.Parallel()
	plain, detail := answeringWith(rewriteAnswer), answeringWith(rewriteDetail)
	if _, err := schemaflux.Rewrite(operationClient(plain), rewriteInput); err != nil {
		t.Fatal(err)
	}
	if _, err := schemaflux.RewriteDetail(operationClient(detail), rewriteInput); err != nil {
		t.Fatal(err)
	}
	wantNoText(t, plain.Last(), "changes_made")
	wantText(t, detail.Last(), "changes_made")
	if plain.Last().Format != "text" || detail.Last().Format != "json" {
		t.Errorf("formats = %q and %q", plain.Last().Format, detail.Last().Format)
	}
}

func TestRewriteDetailSendsTheSameRequestInEveryState(t *testing.T) {
	t.Parallel()
	plain := answeringWith(rewriteDetail)
	if _, err := schemaflux.RewriteDetail(operationClient(plain), rewriteInput); err != nil {
		t.Fatal(err)
	}
	var recorder schemaflux.Recorder
	recorded := answeringWith(rewriteDetail)
	if _, err := schemaflux.RewriteDetail(operationClient(recorded, recordingTo(&recorder)), rewriteInput); err != nil {
		t.Fatal(err)
	}
	floored := namedModel(rewriteDetail)
	if _, err := schemaflux.RewriteDetail(operationClient(floored, underFloor(schemaflux.ContractJSONWellFormed)), rewriteInput); err != nil {
		t.Fatal(err)
	}
	for name, other := range map[string]schemaflux.Request{"a recorder": recorded.Last(), "a floor": floored.Last()} {
		if requestText(plain.Last()) != requestText(other) || plain.Last().Format != other.Format {
			t.Errorf("%s changed what the model is asked", name)
		}
	}
}

func TestRewrittenMarshalsWithTheTagsOfTheInternalRewrite(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(schemaflux.Rewritten{Text: "t", ChangesMade: []string{"c"}, ToneAchieved: "formal", ModelConfidence: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"text":"t","changes_made":["c"],"confidence":0.5,"tone_achieved":"formal"}`
	var a, b map[string]any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(want), &b)
	if !reflect.DeepEqual(a, b) || strings.Contains(string(got), "metadata") {
		t.Errorf("got %s, want %s", got, want)
	}
	empty, _ := json.Marshal(schemaflux.Rewritten{Text: "t"})
	if strings.Contains(string(empty), "changes_made") || strings.Contains(string(empty), "tone_achieved") {
		t.Errorf("an empty list and an empty tone are written: %s", empty)
	}
}
