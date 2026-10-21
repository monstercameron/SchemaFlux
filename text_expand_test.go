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
	expandInput  = "Revenue grew."
	expandAnswer = "Revenue grew twelve percent over the previous quarter, driven by new subscriptions."
	expandDetail = `{"text":"Revenue grew twelve percent over the previous quarter, driven by new subscriptions.","added_content":["the size of the growth","its cause"],"confidence":0.8}`
)

func init() {
	payload := func(canary string) schemaflux.ExpandOptions {
		return schemaflux.ExpandOptions{
			Options:     schemaflux.Options{Steering: "steer " + canary},
			ElaborateOn: []string{"elaborate " + canary}, AddContext: []string{"context " + canary}, Style: "style " + canary,
		}
	}
	textForms = append(textForms,
		textForm{
			op: "expand", plain: true, body: expandAnswer, badBody: "  ",
			call: func(ctx context.Context, canary string) error {
				_, err := schemaflux.Expand(ctx, expandInput+" "+canary, payload(canary))
				return err
			},
			refused: func(ctx context.Context, canary string) error {
				o := payload(canary)
				o.Factor = 0.5
				_, err := schemaflux.Expand(ctx, expandInput+" "+canary, o)
				return err
			},
		},
		textForm{
			op: "expanddetail", level: schemaflux.ContractJSONWellFormed, body: expandDetail, badBody: "not json",
			call: func(ctx context.Context, canary string) error {
				_, err := schemaflux.ExpandDetail(ctx, expandInput+" "+canary, payload(canary))
				return err
			},
			refused: func(ctx context.Context, canary string) error {
				o := payload(canary)
				o.DetailLevel = 11
				_, err := schemaflux.ExpandDetail(ctx, expandInput+" "+canary, o)
				return err
			},
		})
}

func TestExpandReturnsTheModelsTextTrimmedAsAPlainRequest(t *testing.T) {
	t.Parallel()
	model := answeringWith("  \n" + expandAnswer + "\n ")
	got, err := schemaflux.Expand(operationClient(model), expandInput)
	if err != nil || got != expandAnswer {
		t.Fatalf("Expand = %q, %v", got, err)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	req := model.Last()
	wantText(t, req, "content expansion expert")
	wantText(t, req, "Expand on this text:")
	wantText(t, req, expandInput)
	if req.Format != "text" || req.Schema != nil {
		t.Errorf("format %q schema %v: an expansion is the model's text, with no schema", req.Format, req.Schema != nil)
	}
}

func TestExpandWithNoOptionsIsBalancedAtDetailFiveAndCreativeAtTheFastTier(t *testing.T) {
	t.Parallel()
	model := answeringWith(expandAnswer)
	if _, err := schemaflux.Expand(operationClient(model), expandInput); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	wantText(t, req, "Detail level: 5/10")
	wantText(t, req, "Style: balanced")
	for _, absent := range []string{"Expand by", "Include relevant examples", "Elaborate on:", "Add context about"} {
		wantNoText(t, req, absent)
	}
	if math.Abs(req.Temperature-0.7) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" || req.WebSearch || req.Effort != "" {
		t.Errorf("temperature %v ceiling %d model %q web %v effort %q", req.Temperature, req.MaxTokens, req.Model, req.WebSearch, req.Effort)
	}
}

func TestExpandEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.ExpandOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Factor 3", schemaflux.ExpandOptions{Factor: 3}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Expand by 3.0x") }},
		{"Factor 1", schemaflux.ExpandOptions{Factor: 1}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Expand by 1.0x") }},
		{"Factor 2.5", schemaflux.ExpandOptions{Factor: 2.5}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Expand by 2.5x") }},
		{"Factor 2 is the default and says nothing", schemaflux.ExpandOptions{Factor: 2}, func(t *testing.T, req schemaflux.Request) { wantNoText(t, req, "Expand by") }},
		{"DetailLevel 9", schemaflux.ExpandOptions{DetailLevel: 9}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Detail level: 9/10") }},
		{"DetailLevel 1", schemaflux.ExpandOptions{DetailLevel: 1}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Detail level: 1/10") }},
		{"DetailLevel 10", schemaflux.ExpandOptions{DetailLevel: 10}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Detail level: 10/10") }},
		{"IncludeExamples", schemaflux.ExpandOptions{IncludeExamples: true}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Include relevant examples") }},
		{"ElaborateOn", schemaflux.ExpandOptions{ElaborateOn: []string{"growth", "cost"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Elaborate on: growth, cost")
		}},
		{"AddContext", schemaflux.ExpandOptions{AddContext: []string{"the market"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Add context about: the market")
		}},
		{"Style", schemaflux.ExpandOptions{Style: "technical"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Style: technical")
			wantNoText(t, req, "Style: balanced")
		}},
		{"Steering", schemaflux.ExpandOptions{Options: schemaflux.Options{Steering: "stay formal"}}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "stay formal") }},
		{"Model", schemaflux.ExpandOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"MaxOutputTokens", schemaflux.ExpandOptions{Options: schemaflux.Options{MaxOutputTokens: 66}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 66 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.ExpandOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"ModeStrict", schemaflux.ExpandOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(expandAnswer)
			if _, err := schemaflux.Expand(operationClient(model), expandInput, tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestExpandFactorIsARequestAndNothingIsMeasured(t *testing.T) {
	t.Parallel()
	got, err := schemaflux.Expand(operationClient(answeringWith("Short.")), strings.Repeat("a long input ", 20), schemaflux.ExpandOptions{Factor: 5})
	if err != nil || got != "Short." {
		t.Errorf("an answer shorter than the factor was refused or changed: %q, %v", got, err)
	}
}

func TestExpandRefusesAnEmptyAnswer(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{"empty": "", "spaces": "   ", "newlines and tabs": "\n\t\n"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Expand(operationClient(answeringWith(body)), expandInput)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "expand")
			if got != "" {
				t.Errorf("an empty answer left %q beside the error", got)
			}
		})
	}
}

func TestExpandRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	const factor = "Factor must be a number from 1"
	const detail = "DetailLevel must be a whole number from 1 to 10, or zero for the default"
	cases := map[string]struct {
		opts    schemaflux.ExpandOptions
		message string
	}{
		"a factor below 1":              {schemaflux.ExpandOptions{Factor: 0.5}, factor},
		"a negative factor":             {schemaflux.ExpandOptions{Factor: -2}, factor},
		"a factor that is not a number": {schemaflux.ExpandOptions{Factor: math.NaN()}, factor},
		"an infinite factor":            {schemaflux.ExpandOptions{Factor: math.Inf(1)}, factor},
		"a detail level of 11":          {schemaflux.ExpandOptions{DetailLevel: 11}, detail},
		"a negative detail level":       {schemaflux.ExpandOptions{DetailLevel: -1}, detail},
		"a negative output ceiling":     {schemaflux.ExpandOptions{Options: schemaflux.Options{MaxOutputTokens: -1}}, "MaxOutputTokens must not be negative"},
		"a mode that is not one":        {schemaflux.ExpandOptions{Options: schemaflux.Options{Mode: 5}}, "Mode is not one of ModeStrict, ModeTransform and ModeCreative"},
		"a bad setting among good ones": {schemaflux.ExpandOptions{Factor: 3, DetailLevel: 30, Style: "technical"}, detail},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(expandAnswer)
			got, err := schemaflux.Expand(operationClient(model), expandInput, tc.opts)
			e := wantError(t, err, schemaflux.ErrConfiguration, "expand")
			if e.Message != tc.message || got != "" || model.Calls() != 0 {
				t.Errorf("message %q, value %q, %d requests", e.Message, got, model.Calls())
			}
		})
	}
}

func TestExpandMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later value wins and a later zero leaves the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(expandAnswer)
		_, err := schemaflux.Expand(operationClient(model), expandInput,
			schemaflux.ExpandOptions{Factor: 3, DetailLevel: 3, Style: "narrative", Options: schemaflux.Options{Model: "first"}},
			schemaflux.ExpandOptions{DetailLevel: 8},
			schemaflux.ExpandOptions{})
		if err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "Detail level: 8/10")
		wantText(t, req, "Expand by 3.0x")
		wantText(t, req, "Style: narrative")
		if req.Model != "first" {
			t.Errorf("model = %q", req.Model)
		}
	})
	t.Run("a bool turned on stays on", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(expandAnswer)
		_, err := schemaflux.Expand(operationClient(model), expandInput, schemaflux.ExpandOptions{IncludeExamples: true}, schemaflux.ExpandOptions{})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "Include relevant examples")
	})
	t.Run("a later list replaces the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(expandAnswer)
		_, err := schemaflux.Expand(operationClient(model), expandInput,
			schemaflux.ExpandOptions{ElaborateOn: []string{"first"}}, schemaflux.ExpandOptions{ElaborateOn: []string{"second"}})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "Elaborate on: second")
		wantNoText(t, model.Last(), "first")
	})
	t.Run("a bad setting in a later value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Expand(operationClient(answeringWith(expandAnswer)), expandInput,
			schemaflux.ExpandOptions{}, schemaflux.ExpandOptions{Factor: 0.1})
		wantError(t, err, schemaflux.ErrConfiguration, "expand")
	})
}

func TestExpandDetailReturnsTheExpansionWithWhatTheModelReportedAndTheRatioMeasured(t *testing.T) {
	t.Parallel()
	got, err := schemaflux.ExpandDetail(operationClient(answeringWith(expandDetail)), expandInput)
	if err != nil {
		t.Fatal(err)
	}
	want := schemaflux.Expansion{
		Text: expandAnswer, AddedContent: []string{"the size of the growth", "its cause"}, ModelConfidence: 0.8,
		ExpansionRatio: float64(len([]rune(expandAnswer))) / float64(len([]rune(expandInput))),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestExpandDetailRatioIsZeroForAnEmptyInput(t *testing.T) {
	t.Parallel()
	got, err := schemaflux.ExpandDetail(operationClient(answeringWith(expandDetail)), "", schemaflux.ExpandOptions{Factor: 3})
	if err != nil || got.ExpansionRatio != 0 || got.Text == "" {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestExpandDetailRefusesAnExpansionMoreThanTwentyPercentShortOfAFactorOtherThanTwo(t *testing.T) {
	t.Parallel()
	input := strings.Repeat("a", 10)
	body := func(n int) string {
		return `{"text":"` + strings.Repeat("b", n) + `","added_content":[],"confidence":0.5}`
	}
	cases := []struct {
		name    string
		factor  float64
		length  int
		refused bool
	}{
		{"exactly the factor", 3, 30, false},
		{"over the factor", 3, 45, false},
		{"within the tolerance", 3, 24, false},
		{"just short of the tolerance", 3, 23, true},
		{"far short of the factor", 5, 12, true},
		{"the default factor measures nothing", 0, 3, false},
		{"a factor of two measures nothing", 2, 3, false},
		{"a factor of one", 1, 8, false},
		{"a factor of one, too short", 1, 7, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.ExpandDetail(operationClient(answeringWith(body(tc.length))), input, schemaflux.ExpandOptions{Factor: tc.factor})
			if !tc.refused {
				if err != nil || got.Text == "" {
					t.Fatalf("got %+v, %v", got, err)
				}
				return
			}
			wantError(t, err, schemaflux.ErrInvalidAnswer, "expanddetail")
			if !reflect.DeepEqual(got, schemaflux.Expansion{}) {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestExpandDetailRefusesAnAnswerThatIsNotAnExpansion(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"text":                       `Revenue grew a lot.`,
		"a list":                     `["a"]`,
		"no text":                    `{"added_content":[],"confidence":0.5}`,
		"an empty text":              `{"text":"","added_content":[],"confidence":0.5}`,
		"no added content":           `{"text":"x","confidence":0.5}`,
		"no confidence":              `{"text":"x","added_content":[]}`,
		"a confidence off the scale": `{"text":"x","added_content":[],"confidence":4}`,
		"empty":                      ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.ExpandDetail(operationClient(answeringWith(body)), expandInput)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "expanddetail")
			if !reflect.DeepEqual(got, schemaflux.Expansion{}) {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestExpandDetailAsksAnotherQuestionThanExpandDoes(t *testing.T) {
	t.Parallel()
	plain, detail := answeringWith(expandAnswer), answeringWith(expandDetail)
	if _, err := schemaflux.Expand(operationClient(plain), expandInput); err != nil {
		t.Fatal(err)
	}
	if _, err := schemaflux.ExpandDetail(operationClient(detail), expandInput); err != nil {
		t.Fatal(err)
	}
	wantNoText(t, plain.Last(), "added_content")
	wantText(t, detail.Last(), "added_content")
	if plain.Last().Format != "text" || detail.Last().Format != "json" {
		t.Errorf("formats = %q and %q", plain.Last().Format, detail.Last().Format)
	}
}

func TestExpandDetailRefusesABadOptionAndANilContextBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	model := answeringWith(expandDetail)
	for name, opts := range map[string]schemaflux.ExpandOptions{
		"a factor below 1":     {Factor: 0.2},
		"a detail level of 12": {DetailLevel: 12},
		"a negative ceiling":   {Options: schemaflux.Options{MaxOutputTokens: -1}},
	} {
		_, err := schemaflux.ExpandDetail(operationClient(model), expandInput, opts)
		if e := wantError(t, err, schemaflux.ErrConfiguration, "expanddetail"); e.Message == "" {
			t.Errorf("%s: no message", name)
		}
	}
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestExpandDetailSendsTheSameRequestInEveryState(t *testing.T) {
	t.Parallel()
	plain := answeringWith(expandDetail)
	if _, err := schemaflux.ExpandDetail(operationClient(plain), expandInput); err != nil {
		t.Fatal(err)
	}
	var recorder schemaflux.Recorder
	recorded := answeringWith(expandDetail)
	if _, err := schemaflux.ExpandDetail(operationClient(recorded, recordingTo(&recorder)), expandInput); err != nil {
		t.Fatal(err)
	}
	floored := namedModel(expandDetail)
	if _, err := schemaflux.ExpandDetail(operationClient(floored, underFloor(schemaflux.ContractJSONWellFormed)), expandInput); err != nil {
		t.Fatal(err)
	}
	for name, other := range map[string]schemaflux.Request{"a recorder": recorded.Last(), "a floor": floored.Last()} {
		if requestText(plain.Last()) != requestText(other) || plain.Last().Format != other.Format {
			t.Errorf("%s changed what the model is asked", name)
		}
	}
}

func TestExpansionMarshalsWithTheTagsOfTheInternalExpansion(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(schemaflux.Expansion{Text: "t", ExpansionRatio: 2.5, AddedContent: []string{"a"}, ModelConfidence: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	_ = json.Unmarshal(got, &a)
	_ = json.Unmarshal([]byte(`{"text":"t","expansion_ratio":2.5,"added_content":["a"],"confidence":0.5}`), &b)
	if !reflect.DeepEqual(a, b) || strings.Contains(string(got), "metadata") {
		t.Errorf("got %s", got)
	}
	empty, _ := json.Marshal(schemaflux.Expansion{Text: "t"})
	if strings.Contains(string(empty), "added_content") {
		t.Errorf("an empty list is written: %s", empty)
	}
}
