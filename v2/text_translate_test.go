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
	translateInput  = "Good morning, team. The invoice is ready."
	translateAnswer = "Buenos dias, equipo. La factura esta lista."
	translateDetail = `{"text":"Buenos dias, equipo. La factura esta lista.","source_language_detected":"English","confidence":0.95,"alternatives":[{"phrase":"team","alternative":"grupo","context":"informal"}]}`
)

func init() {
	payload := func(canary string) schemaflux.TranslateOptions {
		return schemaflux.TranslateOptions{
			Options:        schemaflux.Options{Steering: "steer " + canary},
			SourceLanguage: "from " + canary, Formality: "formal " + canary, Dialect: "dialect " + canary,
			Glossary: map[string]string{"key " + canary: "value " + canary},
		}
	}
	textForms = append(textForms,
		textForm{
			op: "translate", plain: true, body: translateAnswer, badBody: "  ",
			call: func(ctx context.Context, canary string) error {
				_, err := schemaflux.Translate(ctx, translateInput+" "+canary, "Spanish", payload(canary))
				return err
			},
			refused: func(ctx context.Context, canary string) error {
				o := payload(canary)
				o.CulturalAdaptation = 11
				_, err := schemaflux.Translate(ctx, translateInput+" "+canary, "Spanish", o)
				return err
			},
		},
		textForm{
			op: "translatedetail", level: schemaflux.ContractJSONWellFormed, body: translateDetail, badBody: "not json",
			call: func(ctx context.Context, canary string) error {
				_, err := schemaflux.TranslateDetail(ctx, translateInput+" "+canary, "Spanish", payload(canary))
				return err
			},
			refused: func(ctx context.Context, canary string) error {
				_, err := schemaflux.TranslateDetail(ctx, translateInput+" "+canary, " \t", payload(canary))
				return err
			},
			refusedKind: schemaflux.ErrInvalidRequest,
		})
}

func TestTranslateReturnsTheModelsTextTrimmedAsAPlainRequest(t *testing.T) {
	t.Parallel()
	model := answeringWith("\n  " + translateAnswer + " \n")
	got, err := schemaflux.Translate(operationClient(model), translateInput, "Spanish")
	if err != nil || got != translateAnswer {
		t.Fatalf("Translate = %q, %v", got, err)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	req := model.Last()
	wantText(t, req, "translation expert")
	wantText(t, req, "Translate this text:")
	wantText(t, req, translateInput)
	wantText(t, req, "Translate to Spanish")
	if req.Format != "text" || req.Schema != nil {
		t.Errorf("format %q schema %v: a translation is the model's text, with no schema", req.Format, req.Schema != nil)
	}
}

func TestTranslateWithNoOptionsPreservesFormattingAtNeutralFormalityAtTheFastTier(t *testing.T) {
	t.Parallel()
	model := answeringWith(translateAnswer)
	if _, err := schemaflux.Translate(operationClient(model), translateInput, "French"); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	wantText(t, req, "Preserve formatting")
	for _, absent := range []string{"From ", "dialect", "Formality:", "Cultural adaptation", "glossary"} {
		wantNoText(t, req, absent)
	}
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" || req.WebSearch || req.Effort != "" {
		t.Errorf("temperature %v ceiling %d model %q web %v effort %q", req.Temperature, req.MaxTokens, req.Model, req.WebSearch, req.Effort)
	}
}

func TestTranslateEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.TranslateOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"SourceLanguage", schemaflux.TranslateOptions{SourceLanguage: "English"}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "From English") }},
		{"NoPreserveFormatting", schemaflux.TranslateOptions{NoPreserveFormatting: true}, func(t *testing.T, req schemaflux.Request) { wantNoText(t, req, "Preserve formatting") }},
		{"CulturalAdaptation 8", schemaflux.TranslateOptions{CulturalAdaptation: 8}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Cultural adaptation level: 8/10")
		}},
		{"CulturalAdaptation 10", schemaflux.TranslateOptions{CulturalAdaptation: 10}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Cultural adaptation level: 10/10")
		}},
		{"CulturalAdaptation 1", schemaflux.TranslateOptions{CulturalAdaptation: 1}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Cultural adaptation level: 1/10")
		}},
		{"CulturalAdaptation 5 is the default and says nothing", schemaflux.TranslateOptions{CulturalAdaptation: 5}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Cultural adaptation")
		}},
		{"CulturalAdaptation negative means a literal translation", schemaflux.TranslateOptions{CulturalAdaptation: -1}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Cultural adaptation level: 0/10")
		}},
		{"CulturalAdaptation very negative means a literal translation", schemaflux.TranslateOptions{CulturalAdaptation: -1 << 30}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Cultural adaptation level: 0/10")
		}},
		{"Formality", schemaflux.TranslateOptions{Formality: "formal"}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Formality: formal") }},
		{"Formality neutral says nothing", schemaflux.TranslateOptions{Formality: "neutral"}, func(t *testing.T, req schemaflux.Request) { wantNoText(t, req, "Formality:") }},
		{"Dialect", schemaflux.TranslateOptions{Dialect: "Mexican"}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Use Mexican dialect") }},
		{"Glossary is written in the order of its keys", schemaflux.TranslateOptions{Glossary: map[string]string{"team": "equipo", "invoice": "factura"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Use glossary: invoice=factura, team=equipo")
		}},
		{"Steering", schemaflux.TranslateOptions{Options: schemaflux.Options{Steering: "keep it short"}}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "keep it short") }},
		{"Model", schemaflux.TranslateOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"MaxOutputTokens", schemaflux.TranslateOptions{Options: schemaflux.Options{MaxOutputTokens: 66}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 66 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.TranslateOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"ModeCreative", schemaflux.TranslateOptions{Options: schemaflux.Options{Mode: schemaflux.ModeCreative}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.7) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(translateAnswer)
			if _, err := schemaflux.Translate(operationClient(model), translateInput, "Spanish", tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestTranslateRefusesAnAnswerThatIgnoresTheGlossaryForATermThatIsInTheText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		glossary map[string]string
		answer   string
		refused  bool
	}{
		{"a term in the text translated as the glossary says", map[string]string{"invoice": "factura"}, translateAnswer, false},
		{"a term in the text translated another way", map[string]string{"invoice": "cuenta"}, translateAnswer, true},
		{"a term that is not in the text is not required", map[string]string{"receipt": "recibo"}, translateAnswer, false},
		{"a term is a whole word of the text", map[string]string{"inv": "xyz"}, translateAnswer, false},
		{"the glossary translation in another case", map[string]string{"invoice": "FACTURA"}, translateAnswer, false},
		{"the term in another case", map[string]string{"INVOICE": "cuenta"}, translateAnswer, true},
		{"one of two terms missing", map[string]string{"invoice": "factura", "team": "grupo"}, translateAnswer, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Translate(operationClient(answeringWith(tc.answer)), translateInput, "Spanish", schemaflux.TranslateOptions{Glossary: tc.glossary})
			if !tc.refused {
				if err != nil || got != tc.answer {
					t.Fatalf("Translate = %q, %v", got, err)
				}
				return
			}
			wantError(t, err, schemaflux.ErrInvalidAnswer, "translate")
			if got != "" {
				t.Errorf("a refused translation left %q beside the error", got)
			}
		})
	}
}

func TestTranslateRefusesAnEmptyAnswer(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{"empty": "", "spaces": "   ", "newlines and tabs": "\n\t\n"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Translate(operationClient(answeringWith(body)), translateInput, "Spanish")
			wantError(t, err, schemaflux.ErrInvalidAnswer, "translate")
			if got != "" {
				t.Errorf("an empty answer left %q beside the error", got)
			}
		})
	}
}

func TestTranslateRefusesABlankLanguageAndABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	const adaptation = "CulturalAdaptation must not be above 10"
	cases := map[string]struct {
		language string
		opts     schemaflux.TranslateOptions
		kind     schemaflux.Kind
		message  string
	}{
		"an empty language":                     {"", schemaflux.TranslateOptions{}, schemaflux.ErrInvalidRequest, "language is blank"},
		"a language of spaces":                  {" \t\n", schemaflux.TranslateOptions{}, schemaflux.ErrInvalidRequest, "language is blank"},
		"an adaptation of 11":                   {"Spanish", schemaflux.TranslateOptions{CulturalAdaptation: 11}, schemaflux.ErrConfiguration, adaptation},
		"a very large adaptation":               {"Spanish", schemaflux.TranslateOptions{CulturalAdaptation: 1 << 30}, schemaflux.ErrConfiguration, adaptation},
		"a negative output ceiling":             {"Spanish", schemaflux.TranslateOptions{Options: schemaflux.Options{MaxOutputTokens: -1}}, schemaflux.ErrConfiguration, "MaxOutputTokens must not be negative"},
		"a tier that is not one":                {"Spanish", schemaflux.TranslateOptions{Options: schemaflux.Options{Tier: 5}}, schemaflux.ErrConfiguration, "Tier is not one of TierSmart, TierFast and TierQuick"},
		"a bad setting beside a blank language": {"", schemaflux.TranslateOptions{CulturalAdaptation: 11}, schemaflux.ErrInvalidRequest, "language is blank"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(translateAnswer)
			got, err := schemaflux.Translate(operationClient(model), translateInput, tc.language, tc.opts)
			e := wantError(t, err, tc.kind, "translate")
			if e.Message != tc.message || got != "" || model.Calls() != 0 {
				t.Errorf("message %q, value %q, %d requests", e.Message, got, model.Calls())
			}
		})
	}
}

func TestTranslateMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later value wins and a later zero leaves the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(translateAnswer)
		_, err := schemaflux.Translate(operationClient(model), translateInput, "Spanish",
			schemaflux.TranslateOptions{SourceLanguage: "English", CulturalAdaptation: 3, Dialect: "Mexican", Options: schemaflux.Options{Model: "first"}},
			schemaflux.TranslateOptions{CulturalAdaptation: 9},
			schemaflux.TranslateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "Cultural adaptation level: 9/10")
		wantText(t, req, "From English")
		wantText(t, req, "Use Mexican dialect")
		if req.Model != "first" {
			t.Errorf("model = %q", req.Model)
		}
	})
	t.Run("a bool turned on stays on", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(translateAnswer)
		_, err := schemaflux.Translate(operationClient(model), translateInput, "Spanish", schemaflux.TranslateOptions{NoPreserveFormatting: true}, schemaflux.TranslateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		wantNoText(t, model.Last(), "Preserve formatting")
	})
	t.Run("a later glossary replaces the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(translateAnswer)
		_, err := schemaflux.Translate(operationClient(model), translateInput, "Spanish",
			schemaflux.TranslateOptions{Glossary: map[string]string{"first": "uno"}}, schemaflux.TranslateOptions{Glossary: map[string]string{"second": "dos"}})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "second=dos")
		wantNoText(t, model.Last(), "first")
	})
	t.Run("a bad setting in a later value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Translate(operationClient(answeringWith(translateAnswer)), translateInput, "Spanish",
			schemaflux.TranslateOptions{}, schemaflux.TranslateOptions{CulturalAdaptation: 40})
		wantError(t, err, schemaflux.ErrConfiguration, "translate")
	})
}

func TestTranslateDetailReturnsTheTranslationWithWhatTheModelReported(t *testing.T) {
	t.Parallel()
	got, err := schemaflux.TranslateDetail(operationClient(answeringWith(translateDetail)), translateInput, "Spanish")
	if err != nil {
		t.Fatal(err)
	}
	want := schemaflux.Translation{
		Text: translateAnswer, SourceLanguageDetected: "English", ModelConfidence: 0.95,
		Alternatives: []schemaflux.TranslationAlternative{{Phrase: "team", Alternative: "grupo", Context: "informal"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestTranslateDetailAcceptsAnAnswerWithNoAlternatives(t *testing.T) {
	t.Parallel()
	got, err := schemaflux.TranslateDetail(operationClient(answeringWith(`{"text":"Hola","source_language_detected":"English","confidence":0.5}`)), "Hello", "Spanish")
	if err != nil || got.Text != "Hola" || got.Alternatives != nil {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestTranslateDetailRefusesAnAnswerThatIsNotATranslation(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"text":                       `Hola.`,
		"a list":                     `["a"]`,
		"no text":                    `{"source_language_detected":"English","confidence":0.5}`,
		"an empty text":              `{"text":"","source_language_detected":"English","confidence":0.5}`,
		"no detected language":       `{"text":"x","confidence":0.5}`,
		"no confidence":              `{"text":"x","source_language_detected":"English"}`,
		"a confidence off the scale": `{"text":"x","source_language_detected":"English","confidence":4}`,
		"empty":                      ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.TranslateDetail(operationClient(answeringWith(body)), translateInput, "Spanish")
			wantError(t, err, schemaflux.ErrInvalidAnswer, "translatedetail")
			if !reflect.DeepEqual(got, schemaflux.Translation{}) {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestTranslateDetailChecksTheGlossaryToo(t *testing.T) {
	t.Parallel()
	got, err := schemaflux.TranslateDetail(operationClient(answeringWith(translateDetail)), translateInput, "Spanish",
		schemaflux.TranslateOptions{Glossary: map[string]string{"invoice": "cuenta"}})
	wantError(t, err, schemaflux.ErrInvalidAnswer, "translatedetail")
	if !reflect.DeepEqual(got, schemaflux.Translation{}) {
		t.Errorf("a refused answer left %+v beside the error", got)
	}
}

func TestTranslateDetailAsksAnotherQuestionThanTranslateDoes(t *testing.T) {
	t.Parallel()
	plain, detail := answeringWith(translateAnswer), answeringWith(translateDetail)
	if _, err := schemaflux.Translate(operationClient(plain), translateInput, "Spanish"); err != nil {
		t.Fatal(err)
	}
	if _, err := schemaflux.TranslateDetail(operationClient(detail), translateInput, "Spanish"); err != nil {
		t.Fatal(err)
	}
	wantNoText(t, plain.Last(), "source_language_detected")
	wantText(t, detail.Last(), "source_language_detected")
	wantText(t, detail.Last(), "Translate to Spanish")
	if plain.Last().Format != "text" || detail.Last().Format != "json" {
		t.Errorf("formats = %q and %q", plain.Last().Format, detail.Last().Format)
	}
}

func TestTranslateDetailRefusesABlankLanguageAndABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	model := answeringWith(translateDetail)
	_, err := schemaflux.TranslateDetail(operationClient(model), translateInput, "")
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "translatedetail"); e.Message != "language is blank" {
		t.Errorf("message = %q", e.Message)
	}
	_, err = schemaflux.TranslateDetail(operationClient(model), translateInput, "French", schemaflux.TranslateOptions{CulturalAdaptation: 12})
	wantError(t, err, schemaflux.ErrConfiguration, "translatedetail")
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestTranslateDetailSendsTheSameRequestInEveryState(t *testing.T) {
	t.Parallel()
	plain := answeringWith(translateDetail)
	if _, err := schemaflux.TranslateDetail(operationClient(plain), translateInput, "Spanish"); err != nil {
		t.Fatal(err)
	}
	var recorder schemaflux.Recorder
	recorded := answeringWith(translateDetail)
	if _, err := schemaflux.TranslateDetail(operationClient(recorded, recordingTo(&recorder)), translateInput, "Spanish"); err != nil {
		t.Fatal(err)
	}
	floored := namedModel(translateDetail)
	if _, err := schemaflux.TranslateDetail(operationClient(floored, underFloor(schemaflux.ContractJSONWellFormed)), translateInput, "Spanish"); err != nil {
		t.Fatal(err)
	}
	for name, other := range map[string]schemaflux.Request{"a recorder": recorded.Last(), "a floor": floored.Last()} {
		if requestText(plain.Last()) != requestText(other) || plain.Last().Format != other.Format {
			t.Errorf("%s changed what the model is asked", name)
		}
	}
}

func TestTranslationMarshalsWithTheTagsOfTheInternalTranslation(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(schemaflux.Translation{
		Text: "t", SourceLanguageDetected: "English", ModelConfidence: 0.5,
		Alternatives: []schemaflux.TranslationAlternative{{Phrase: "p", Alternative: "a", Context: "c"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	_ = json.Unmarshal(got, &a)
	_ = json.Unmarshal([]byte(`{"text":"t","source_language_detected":"English","confidence":0.5,"alternatives":[{"phrase":"p","alternative":"a","context":"c"}]}`), &b)
	if !reflect.DeepEqual(a, b) || strings.Contains(string(got), "metadata") {
		t.Errorf("got %s", got)
	}
	empty, _ := json.Marshal(schemaflux.Translation{Text: "t"})
	if strings.Contains(string(empty), "alternatives") || strings.Contains(string(empty), "source_language_detected") {
		t.Errorf("an empty list and an empty language are written: %s", empty)
	}
	bare, _ := json.Marshal(schemaflux.TranslationAlternative{Phrase: "p", Alternative: "a"})
	if strings.Contains(string(bare), "context") {
		t.Errorf("an empty context is written: %s", bare)
	}
}
