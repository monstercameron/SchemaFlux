package redact_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/redact"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

const (
	noteText   = "Ada Lovelace wrote ada@example.com"
	nameAnswer = `{"spans":[{"text":"Ada Lovelace","category":"name"}]}`
	noSpans    = `{"spans":[]}`
	maskedNote = "************ wrote ***************"
)

// noteResult is what Text returns for noteText when the model names the name.
func noteResult() redact.TextResult {
	return redact.TextResult{
		Text: maskedNote, Original: noteText,
		Spans: []redact.Span{
			{Start: 0, End: 12, Category: "name", Original: "Ada Lovelace"},
			{Start: 19, End: 34, Category: "email", Original: "ada@example.com"},
		},
		Categories: map[string]int{"name": 1, "email": 1}, DetectorSpans: 1,
	}
}

// emptyResult is what Text returns beside an error.
var emptyResult = redact.TextResult{}

func TestTextMasksWhatTheModelNamesAndWhatTheDetectorsFind(t *testing.T) {
	t.Parallel()
	model := answeringWith(nameAnswer)
	res, err := redact.Text(clientOn(model), noteText)
	if err != nil {
		t.Fatal(err)
	}
	if want := noteResult(); !reflect.DeepEqual(res, want) {
		t.Errorf("got %+v, want %+v", res, want)
	}
	if model.Calls() != 1 {
		t.Errorf("%d requests were sent, want one", model.Calls())
	}
}

func TestTextMasksWhatTheDetectorsFindWhateverTheModelSays(t *testing.T) {
	t.Parallel()
	for name, answer := range map[string]string{"an answer of nothing": noSpans, "an answer that names the wrong thing": `{"spans":[{"text":"wrote","category":"name"}]}`} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res, err := redact.Text(clientOn(answeringWith(answer)), "wrote ada@example.com and 555-123-4567")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(res.Text, "ada@example.com") || strings.Contains(res.Text, "555-123-4567") || res.DetectorSpans != 2 {
				t.Errorf("got %+v", res)
			}
		})
	}
}

func TestTextLeavesWhatOnlyTheModelCouldFindWhenItFindsNothing(t *testing.T) {
	t.Parallel()
	res, err := redact.Text(clientOn(answeringWith(noSpans)), noteText)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Text, "Ada Lovelace wrote ") || res.DetectorSpans != 1 || len(res.Spans) != 1 {
		t.Errorf("got %+v: a name is the model's alone to find", res)
	}
}

func TestTextCountsOffsetsInCharactersNotBytes(t *testing.T) {
	t.Parallel()
	const text = "Zoë Müller wrote ada@example.com"
	res, err := redact.Text(clientOn(answeringWith(`{"spans":[{"text":"Müller","category":"name"}]}`)), text)
	if err != nil {
		t.Fatal(err)
	}
	want := []redact.Span{
		{Start: 4, End: 10, Category: "name", Original: "Müller"},
		{Start: 17, End: 32, Category: "email", Original: "ada@example.com"},
	}
	if !reflect.DeepEqual(res.Spans, want) || res.Text != "Zoë ****** wrote ***************" {
		t.Errorf("got %+v and %q", res.Spans, res.Text)
	}
	for _, span := range res.Spans {
		if got := string([]rune(text)[span.Start:span.End]); got != span.Original {
			t.Errorf("the characters from %d to %d are %q, not %q", span.Start, span.End, got, span.Original)
		}
	}
}

func TestTextMasksEveryCopyOfAValueInAnyCase(t *testing.T) {
	t.Parallel()
	res, err := redact.Text(clientOn(answeringWith(`{"spans":[{"text":"Dana Kim","category":"name"}]}`)), "Dana Kim called. DANA KIM wrote. dana\n kim left.")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(res.Text), "dana") || strings.Contains(strings.ToLower(res.Text), "kim") || len(res.Spans) != 3 || res.Categories["name"] != 3 {
		t.Errorf("got %+v", res)
	}
}

func TestTextLeavesAVeryShortValueInsideALongerWordAndSaysSo(t *testing.T) {
	t.Parallel()
	res, err := redact.Text(clientOn(answeringWith(`{"spans":[{"text":"Al","category":"name"}]}`)), "Al called Al Smith")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "*** called *** Smith" || res.LeftInsideWords != 1 || len(res.Spans) != 2 {
		t.Errorf("got %+v", res)
	}
}

func TestTextWithNoOptionsAsksAtTheFastTierInStrictModeForADeclaredSchema(t *testing.T) {
	t.Parallel()
	model := answeringWith(nameAnswer)
	if _, err := redact.Text(clientOn(model), noteText); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if math.Abs(req.Temperature-0.1) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" || req.WebSearch {
		t.Errorf("temperature %v ceiling %d model %q web %v", req.Temperature, req.MaxTokens, req.Model, req.WebSearch)
	}
	if req.Schema == nil || req.SchemaName != "redaction_spans" {
		t.Errorf("schema declared %v under the name %q", req.Schema != nil, req.SchemaName)
	}
	if req.Effort != "" {
		t.Errorf("effort %q: the operation has no preference", req.Effort)
	}
	wantText(t, req, "- email: Email addresses")
	wantText(t, req, "Find all sensitive data in this text and return each value as JSON:\n\n\"Ada Lovelace wrote ada@example.com\"")
}

func TestTextEachOptionFieldChangesTheRequestAndTheResultAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	const mailText = "mail ada@example.com or 555-123-4567"
	cases := []struct {
		name     string
		opts     redact.TextOptions
		reply    string
		text     string
		wantText string
		check    func(t *testing.T, req schemaflux.Request)
	}{
		{"Categories narrow the prompt and the detectors", redact.TextOptions{Categories: []string{"email"}}, noSpans, mailText, "mail *************** or 555-123-4567", func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `- "email"`)
			wantNoText(t, req, "- phone: Phone numbers")
		}},
		{"Categories of the caller's own", redact.TextOptions{Categories: []string{"project"}}, `{"spans":[{"text":"Falcon","category":"PROJECT"}]}`, "the Falcon launch", "the ****** launch", func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `- "project"`)
		}},
		{"MaskChar", redact.TextOptions{MaskChar: '#'}, noSpans, mailText, "mail ############### or ############", nil},
		{"ShowFirst", redact.TextOptions{ShowFirst: 2}, noSpans, "mail ada@example.com", "mail ad*************", nil},
		{"ShowLast", redact.TextOptions{ShowLast: 4}, noSpans, "mail ada@example.com", "mail ***********.com", nil},
		{"ShowFirst and ShowLast", redact.TextOptions{ShowFirst: 1, ShowLast: 4}, noSpans, "mail ada@example.com", "mail a**********.com", nil},
		{"ShowFirst and ShowLast that cover the whole span show none", redact.TextOptions{ShowFirst: 8, ShowLast: 8}, noSpans, "mail ada@example.com", "mail ***************", nil},
		{"MinMask", redact.TextOptions{MinMask: 6}, `{"spans":[{"text":"Al","category":"name"}]}`, "hello Al", "hello ******", nil},
		{"the default minimum mask is three", redact.TextOptions{}, `{"spans":[{"text":"Al","category":"name"}]}`, "hello Al", "hello ***", nil},
		{"Steering", redact.TextOptions{Options: schemaflux.Options{Steering: "treat order numbers as sensitive"}}, noSpans, "x", "x", func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "treat order numbers as sensitive")
		}},
		{"Model", redact.TextOptions{Options: schemaflux.Options{Model: "pinned-model"}}, noSpans, "x", "x", func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeTransform", redact.TextOptions{Options: schemaflux.Options{Mode: schemaflux.ModeTransform}}, noSpans, "x", "x", func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.3) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
		{"TierSmart", redact.TextOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, noSpans, "x", "x", func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", redact.TextOptions{Options: schemaflux.Options{MaxOutputTokens: 55}}, noSpans, "x", "x", func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 55 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", redact.TextOptions{Options: schemaflux.Options{WebSearch: true}}, noSpans, "x", "x", func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"Effort", redact.TextOptions{Options: schemaflux.Options{Effort: schemaflux.EffortHigh}}, noSpans, "x", "x", func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "high" || req.EffortPreferred {
				t.Errorf("effort = %q preferred %v, want high as the caller's requirement", req.Effort, req.EffortPreferred)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(tc.reply)
			res, err := redact.Text(clientOn(model), tc.text, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if res.Text != tc.wantText {
				t.Errorf("text = %q, want %q", res.Text, tc.wantText)
			}
			if tc.check != nil {
				tc.check(t, model.Last())
			}
		})
	}
}

func TestTextReportsACategoryAsTheCallerSpelledIt(t *testing.T) {
	t.Parallel()
	res, err := redact.Text(clientOn(answeringWith(`{"spans":[{"text":"Falcon","category":"PROJECT"}]}`)), "the Falcon launch",
		redact.TextOptions{Categories: []string{" Project "}})
	if err != nil || res.Spans[0].Category != "Project" || res.Categories["Project"] != 1 {
		t.Fatalf("got %+v, %v", res, err)
	}
}

func TestTextMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts []redact.TextOptions
		want string
	}{
		{"no value is the default", nil, "mail ***************"},
		{"one value is used as it is", []redact.TextOptions{{MaskChar: '#'}}, "mail ###############"},
		{"each value adds its fields", []redact.TextOptions{{ShowFirst: 1}, {MaskChar: '#'}, {ShowLast: 4}}, "mail a##########.com"},
		{"the later value wins", []redact.TextOptions{{MaskChar: '#'}, {MaskChar: '$'}}, "mail $$$$$$$$$$$$$$$"},
		{"a later zero value does not undo an earlier one", []redact.TextOptions{{MaskChar: '#', ShowFirst: 1}, {}}, "mail a##############"},
		{"later categories replace the earlier", []redact.TextOptions{{Categories: []string{"phone"}}, {Categories: []string{"email"}}}, "mail ***************"},
		{"later nil categories leave the earlier", []redact.TextOptions{{Categories: []string{"phone"}}, {MaskChar: '#'}}, "mail ada@example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := redact.Text(clientOn(answeringWith(noSpans)), "mail ada@example.com", tc.opts...)
			if err != nil || res.Text != tc.want {
				t.Errorf("got %q, %v, want %q", res.Text, err, tc.want)
			}
		})
	}
	t.Run("the common settings merge too", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(noSpans)
		_, err := redact.Text(clientOn(model), "x",
			redact.TextOptions{Options: schemaflux.Options{Steering: "be thorough", WebSearch: true}},
			redact.TextOptions{Options: schemaflux.Options{Model: "later-model"}},
			redact.TextOptions{Options: schemaflux.Options{MaxOutputTokens: 77}})
		if err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "be thorough")
		if req.Model != "later-model" || req.MaxTokens != 77 || !req.WebSearch {
			t.Errorf("model %q ceiling %d web %v", req.Model, req.MaxTokens, req.WebSearch)
		}
	})
}

func TestTextReturnsABlankTextAsItIsWithoutARequest(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", " ", "  \t\n"} {
		model := answeringWith(nameAnswer)
		res, err := redact.Text(clientOn(model), text)
		want := redact.TextResult{Text: text, Original: text, Spans: []redact.Span{}, Categories: map[string]int{}}
		if err != nil || !reflect.DeepEqual(res, want) || model.Calls() != 0 {
			t.Errorf("%q: got %+v, %v after %d requests, want %+v", text, res, err, model.Calls(), want)
		}
	}
}

func TestTextRefusesASettingNoCallCanUseBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-0c93be"
	cases := []struct {
		name string
		opts redact.TextOptions
		want string
	}{
		{"an empty list of categories", redact.TextOptions{Categories: []string{}}, "Categories"},
		{"a blank category", redact.TextOptions{Categories: []string{"email", " "}}, "Categories[1]"},
		{"a category with a comma", redact.TextOptions{Categories: []string{canary + ",email"}}, "Categories[0]"},
		{"a negative ShowFirst", redact.TextOptions{ShowFirst: -1}, "ShowFirst"},
		{"a negative ShowLast", redact.TextOptions{ShowLast: -1}, "ShowLast"},
		{"the lowest ShowFirst", redact.TextOptions{ShowFirst: math.MinInt}, "ShowFirst"},
		{"a negative MinMask", redact.TextOptions{MinMask: -1}, "MinMask"},
		{"a MinMask above 65536", redact.TextOptions{MinMask: 65537}, "MinMask"},
		{"the greatest MinMask", redact.TextOptions{MinMask: math.MaxInt}, "MinMask"},
		{"a negative mask character", redact.TextOptions{MaskChar: -1}, "MaskChar"},
		{"a surrogate half as a mask character", redact.TextOptions{MaskChar: 0xDFFF}, "MaskChar"},
		{"a mask character above the last character", redact.TextOptions{MaskChar: 0x110000}, "MaskChar"},
		{"a negative output ceiling", redact.TextOptions{Options: schemaflux.Options{MaxOutputTokens: -1}}, "MaxOutputTokens"},
		{"a tier that does not exist", redact.TextOptions{Options: schemaflux.Options{Tier: 9}}, "Tier"},
		{"a mode that does not exist", redact.TextOptions{Options: schemaflux.Options{Mode: 9}}, "Mode"},
	}
	for _, tc := range cases {
		for _, text := range []string{"mail " + canary + "@example.com", "  "} {
			name := tc.name
			if strings.TrimSpace(text) == "" {
				name += ", with a blank text"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				var recorder schemaflux.Recorder
				model := answeringWith(noSpans)
				res, err := redact.Text(clientOn(model, recordingTo(&recorder)), text, tc.opts)
				e := wantError(t, err, schemaflux.ErrConfiguration, "redact.text")
				if model.Calls() != 0 || !reflect.DeepEqual(res, emptyResult) {
					t.Errorf("%d requests were sent, result %+v", model.Calls(), res)
				}
				if !strings.Contains(e.Error(), tc.want) {
					t.Errorf("the error does not name %s: %v", tc.want, e)
				}
				if strings.Contains(e.Error(), "invalid options") {
					t.Errorf("the refusal is the internal call's and not the function's own: %v", e)
				}
				if r := onlyRecord(t, &recorder); r.Op != "redact.text" || r.Requests != 0 || r.Err != err {
					t.Errorf("record = %q requests %d err %v", r.Op, r.Requests, r.Err)
				}
				requireNoLeak(t, canary, err, true, &recorder, nil)
			})
		}
	}
}

func TestTextAcceptsTheEdgesOfWhatItRefuses(t *testing.T) {
	t.Parallel()
	masked := map[string]redact.TextOptions{
		"a MinMask of 65536":     {MinMask: 65536},
		"the greatest ShowFirst": {ShowFirst: math.MaxInt},
		"the greatest ShowLast":  {ShowLast: math.MaxInt},
		"both at their greatest": {ShowFirst: math.MaxInt, ShowLast: math.MaxInt},
		"the last character":     {MaskChar: 0x10FFFF},
	}
	for name, o := range masked {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res, err := redact.Text(clientOn(answeringWith(noSpans)), "mail ada@example.com", o)
			if err != nil || strings.Contains(res.Text, "ada@example.com") {
				t.Fatalf("got %q, %v", excerpt(res.Text), err)
			}
		})
	}
	t.Run("categories of any case and any symbol", func(t *testing.T) {
		t.Parallel()
		o := redact.TextOptions{Categories: []string{" EMAIL ", "Phone", "a b", `x"y`, "新"}}
		if _, err := redact.Text(clientOn(answeringWith(noSpans)), "mail ada@example.com", o); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTextRefusesAnAnswerThatIsNotWhatWasAskedFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  redact.TextOptions
		reply string
	}{
		{"text", redact.TextOptions{}, "the name is Ada Lovelace"},
		{"no answer at all", redact.TextOptions{}, ""},
		{"null", redact.TextOptions{}, "null"},
		{"an empty object", redact.TextOptions{}, "{}"},
		{"null spans", redact.TextOptions{}, `{"spans":null}`},
		{"spans that are text", redact.TextOptions{}, `{"spans":"Ada"}`},
		{"a bare list", redact.TextOptions{}, `[{"text":"Ada","category":"name"}]`},
		{"a span with no text", redact.TextOptions{}, `{"spans":[{"category":"name"}]}`},
		{"a span with blank text", redact.TextOptions{}, `{"spans":[{"text":"  ","category":"name"}]}`},
		{"a span with no category", redact.TextOptions{}, `{"spans":[{"text":"Ada"}]}`},
		{"a span whose text the text does not hold", redact.TextOptions{}, `{"spans":[{"text":"Grace Hopper","category":"name"}]}`},
		{"a good span and one the text does not hold", redact.TextOptions{}, `{"spans":[{"text":"Ada","category":"name"},{"text":"Grace Hopper","category":"name"}]}`},
		{"a category that was not asked for", redact.TextOptions{Categories: []string{"email"}}, `{"spans":[{"text":"Ada","category":"name"}]}`},
		{"a category that is a built-in one that was not asked for", redact.TextOptions{Categories: []string{"project"}}, `{"spans":[{"text":"Ada","category":"email"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(tc.reply)
			res, err := redact.Text(clientOn(model), noteText, tc.opts)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "redact.text")
			if model.Calls() == 0 {
				t.Error("nothing was sent")
			}
			if !reflect.DeepEqual(res, emptyResult) {
				t.Errorf("a refused answer left %+v beside the error: the text must not come back with the secret in it", res)
			}
		})
	}
}

func TestTextReturnsTheErrorOfAProviderThatFailsAndNothingElse(t *testing.T) {
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
			res, err := redact.Text(clientOn(&model), noteText)
			wantError(t, err, tc.err.Kind, "redact.text")
			if !reflect.DeepEqual(res, emptyResult) {
				t.Errorf("a failed call left %+v beside the error", res)
			}
		})
	}
}

func TestTextWithANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	var none context.Context
	res, err := redact.Text(none, noteText)
	e := wantError(t, err, schemaflux.ErrInvalidRequest, "redact.text")
	if !strings.Contains(e.Error(), "context") || !reflect.DeepEqual(res, emptyResult) {
		t.Errorf("got %+v and %v", res, e)
	}
	_, err = redact.Text(none, noteText, redact.TextOptions{ShowFirst: -1})
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "redact.text"); !strings.Contains(e.Error(), "context") {
		t.Errorf("a nil context and a bad setting: %v, want the context named first", e)
	}
}

func TestTextResultMarshalsWithTheTagsOfTheInternalResult(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(noteResult())
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"text":"************ wrote ***************","original":"Ada Lovelace wrote ada@example.com","spans":[{"start":0,"end":12,"category":"name","original":"Ada Lovelace"},{"start":19,"end":34,"category":"email","original":"ada@example.com"}],"categories":{"email":1,"name":1},"detector_spans":1,"left_inside_words":0}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestTextAddsExactlyOneRecordForEachCallWithTheOperationsName(t *testing.T) {
	t.Parallel()
	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		if _, err := redact.Text(clientOn(answeringWith(nameAnswer), recordingTo(&recorder)), noteText); err != nil {
			t.Fatal(err)
		}
		r := onlyRecord(t, &recorder)
		if r.Op != "redact.text" || r.Err != nil || r.Requests != 1 || r.Provider != "test" {
			t.Errorf("record = %q err %v requests %d provider %q", r.Op, r.Err, r.Requests, r.Provider)
		}
		if r.Requested != schemaflux.ContractSchemaAndInvariantChecked || r.Delivered != schemaflux.ContractSchemaAndInvariantChecked {
			t.Errorf("levels = %v and %v, want schema and invariants checked", r.Requested, r.Delivered)
		}
	})
	t.Run("a call that fails on its answer", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		_, err := redact.Text(clientOn(answeringWith("not json"), recordingTo(&recorder)), noteText)
		wantError(t, err, schemaflux.ErrInvalidAnswer, "redact.text")
		r := onlyRecord(t, &recorder)
		if r.Op != "redact.text" || r.Err != err || r.Requests < 1 || r.Requested != schemaflux.ContractSchemaAndInvariantChecked || r.Delivered >= schemaflux.ContractSchemaAndInvariantChecked {
			t.Errorf("record = %q err %v requests %d requested %v delivered %v", r.Op, r.Err, r.Requests, r.Requested, r.Delivered)
		}
	})
	t.Run("a call whose answer names a value the text does not hold", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		_, err := redact.Text(clientOn(answeringWith(`{"spans":[{"text":"Grace","category":"name"}]}`), recordingTo(&recorder)), noteText)
		wantError(t, err, schemaflux.ErrInvalidAnswer, "redact.text")
		if r := onlyRecord(t, &recorder); r.Op != "redact.text" || r.Err != err {
			t.Errorf("record = %q err %v", r.Op, r.Err)
		}
	})
	t.Run("a call the provider fails", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		var model schemafluxtest.Provider
		model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable})
		_, err := redact.Text(clientOn(&model, recordingTo(&recorder)), noteText)
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrUnavailable, "redact.text")
		if r.Op != "redact.text" || r.Err != err {
			t.Errorf("record = %q err %v", r.Op, r.Err)
		}
	})
	t.Run("a blank text sends nothing and still adds its Record", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(nameAnswer)
		if _, err := redact.Text(clientOn(model, recordingTo(&recorder)), "  "); err != nil {
			t.Fatal(err)
		}
		if r := onlyRecord(t, &recorder); r.Op != "redact.text" || r.Requests != 0 || r.Err != nil || model.Calls() != 0 {
			t.Errorf("record = %q requests %d err %v", r.Op, r.Requests, r.Err)
		}
	})
	t.Run("a recorder on the context and one on the client each get the Record", func(t *testing.T) {
		t.Parallel()
		var onClient, onContext schemaflux.Recorder
		ctx := schemaflux.WithRecorder(clientOn(answeringWith(nameAnswer), recordingTo(&onClient)), &onContext)
		if _, err := redact.Text(ctx, noteText); err != nil {
			t.Fatal(err)
		}
		if onlyRecord(t, &onClient).Op != "redact.text" || onlyRecord(t, &onContext).Op != "redact.text" {
			t.Error("a recorder was not given the Record")
		}
	})
	t.Run("several calls add one Record each", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		ctx := clientOn(answeringWith(nameAnswer), recordingTo(&recorder))
		for range 3 {
			if _, err := redact.Text(ctx, noteText); err != nil {
				t.Fatal(err)
			}
		}
		if got := len(recorder.Records()); got != 3 {
			t.Errorf("%d Records for three calls", got)
		}
	})
}

func TestTextUnderAFloorIsHeldToTheLevelItEstablishes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		floor   schemaflux.ContractLevel
		text    string
		refused bool
	}{
		{"a floor of schema constrained is met", schemaflux.ContractSchemaConstrained, noteText, false},
		{"a floor of schema and invariants is met", schemaflux.ContractSchemaAndInvariantChecked, noteText, false},
		{"a floor of evidence is not", schemaflux.ContractEvidenceChecked, noteText, true},
		{"a floor of full governance is not", schemaflux.ContractFullyGoverned, noteText, true},
		{"a floor above the level refuses a blank text too", schemaflux.ContractEvidenceChecked, " ", true},
		{"a floor at the level is met by a blank text", schemaflux.ContractSchemaAndInvariantChecked, " ", false},
	}
	for _, tc := range cases {
		for _, withRecorder := range []bool{false, true} {
			name := tc.name
			if withRecorder {
				name += ", with a recorder"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				var recorder schemaflux.Recorder
				model := namedModel(nameAnswer)
				settings := []func(*schemaflux.Client){underFloor(tc.floor)}
				if withRecorder {
					settings = append(settings, recordingTo(&recorder))
				}
				res, err := redact.Text(clientOn(model, settings...), tc.text)
				if tc.refused {
					wantError(t, err, schemaflux.ErrPolicy, "redact.text")
					if model.Calls() != 0 || !reflect.DeepEqual(res, emptyResult) {
						t.Errorf("%d requests were sent, result %+v", model.Calls(), res)
					}
				} else if err != nil || res.Original != tc.text {
					t.Fatalf("got %+v, %v", res, err)
				}
				if withRecorder {
					r := onlyRecord(t, &recorder)
					if r.Op != "redact.text" || (r.Err != nil) != tc.refused || r.Requested != schemaflux.ContractSchemaAndInvariantChecked {
						t.Errorf("record = %q err %v requested %v", r.Op, r.Err, r.Requested)
					}
					if tc.refused && r.Requests != 0 {
						t.Errorf("a refused call recorded %d requests", r.Requests)
					}
				}
			})
		}
	}
}

func TestTextGivesTheSameResultAndSendsTheSameRequestInEveryStateOfTheCall(t *testing.T) {
	// Not parallel: it compares what each state sent with what the plain call sent.
	opts := redact.TextOptions{Categories: []string{"name", "email"}, ShowLast: 2, MaskChar: '#', Options: schemaflux.Options{Steering: "be thorough"}}
	var recorder schemaflux.Recorder
	states := []struct {
		name  string
		model *schemafluxtest.Provider
		set   []func(*schemaflux.Client)
	}{
		{"plain", answeringWith(nameAnswer), nil},
		{"a recorder", answeringWith(nameAnswer), []func(*schemaflux.Client){recordingTo(&recorder)}},
		{"a floor it meets", namedModel(nameAnswer), []func(*schemaflux.Client){underFloor(schemaflux.ContractSchemaAndInvariantChecked)}},
	}
	var plainRequest schemaflux.Request
	var plainResult redact.TextResult
	for i, state := range states {
		res, err := redact.Text(clientOn(state.model, state.set...), noteText, opts)
		if err != nil || res.Text == noteText || res.DetectorSpans != 1 {
			t.Errorf("%s: got %+v, %v", state.name, res, err)
		}
		if i == 0 {
			plainRequest, plainResult = state.model.Last(), res
		} else {
			if !reflect.DeepEqual(state.model.Last(), plainRequest) {
				t.Errorf("%s: the request differs from the plain call's", state.name)
			}
			if !reflect.DeepEqual(res, plainResult) {
				t.Errorf("%s: the result differs from the plain call's: %+v and %+v", state.name, res, plainResult)
			}
		}
	}
}

func TestTextUnderAnObserverSendsTheSameRequestAndRecordsOnce(t *testing.T) {
	// Not parallel: an observer is process state.
	var seen []string
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			seen = append(seen, r.Operation)
		}
	}})
	defer restore()

	opts := redact.TextOptions{Categories: []string{"name", "email"}}
	plain := answeringWith(nameAnswer)
	if _, err := redact.Text(clientOn(plain), noteText, opts); err != nil {
		t.Fatal(err)
	}
	var recorder schemaflux.Recorder
	watched := answeringWith(nameAnswer)
	res, err := redact.Text(clientOn(watched, recordingTo(&recorder)), noteText, opts)
	if err != nil || res.Text == noteText {
		t.Fatalf("got %+v, %v", res, err)
	}
	if !reflect.DeepEqual(plain.Last(), watched.Last()) {
		t.Error("an observer changed what the model is asked")
	}
	if onlyRecord(t, &recorder).Op != "redact.text" {
		t.Error("the Record under an observer has the wrong operation")
	}
	if !reflect.DeepEqual(seen, []string{"redact.text", "redact.text"}) {
		t.Errorf("the observer saw %v, want the recorded operation name on both scopes", seen)
	}
}

func TestTextKeepsWhatItRedactsOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-e41b77"
	text := "write to " + canary + "@example.com about " + canary
	opts := redact.TextOptions{Categories: []string{canary + "-kind", "email"}, Options: schemaflux.Options{Steering: "steer " + canary}}
	cases := []struct {
		name       string
		model      func() *schemafluxtest.Provider
		opts       redact.TextOptions
		text       string
		wholeChain bool
	}{
		{"a call that succeeds", func() *schemafluxtest.Provider {
			return answeringWith(`{"spans":[{"text":"` + canary + `","category":"` + canary + `-kind"}]}`)
		}, opts, text, true},
		{"an answer that is text", func() *schemafluxtest.Provider { return answeringWith("they are " + canary) }, opts, text, true},
		{"an answer with a span the text does not hold", func() *schemafluxtest.Provider {
			return answeringWith(`{"spans":[{"text":"` + canary + `-nowhere","category":"email"}]}`)
		}, opts, text, true},
		{"an answer with a span with no text", func() *schemafluxtest.Provider {
			return answeringWith(`{"spans":[{"text":" ","category":"` + canary + `"}]}`)
		}, opts, text, true},
		{"an answer with a category that was not asked for", func() *schemafluxtest.Provider {
			return answeringWith(`{"spans":[{"text":"` + canary + `","category":"` + canary + `-other"}]}`)
		}, opts, text, true},
		{"an answer whose spans are the canary", func() *schemafluxtest.Provider {
			return answeringWith(`{"spans":"` + canary + `"}`)
		}, opts, text, true},
		{"a provider that fails with a vendor message beneath its error", func() *schemafluxtest.Provider {
			var model schemafluxtest.Provider
			model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)})
			return &model
		}, opts, text, false},
		{"a provider that fails with an error of its own", func() *schemafluxtest.Provider {
			var model schemafluxtest.Provider
			model.Fail(errors.New("vendor said " + canary))
			return &model
		}, opts, text, false},
		{"a refused setting", func() *schemafluxtest.Provider { return answeringWith(noSpans) }, redact.TextOptions{Categories: []string{canary + ",x"}}, text, true},
		{"a refused number", func() *schemafluxtest.Provider { return answeringWith(noSpans) }, redact.TextOptions{ShowFirst: -1, Categories: opts.Categories}, text, true},
		{"a blank text with the canary in the options", func() *schemafluxtest.Provider { return answeringWith(noSpans) }, opts, "  ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			var logs schemafluxtest.Logs
			_, err := redact.Text(clientOn(tc.model(), recordingTo(&recorder), loggingTo(&logs)), tc.text, tc.opts)
			requireNoLeak(t, canary, err, tc.wholeChain, &recorder, &logs)
			if len(recorder.Records()) != 1 {
				t.Errorf("%d Records, want one", len(recorder.Records()))
			}
		})
	}
}

func TestTextIsSafeForConcurrentCallsThatShareOptions(t *testing.T) {
	t.Parallel()
	opts := redact.TextOptions{Categories: []string{"name", "email"}, MaskChar: '#'}
	ctx := clientOn(answeringWith(nameAnswer))
	var wg sync.WaitGroup
	failures := make(chan string, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := redact.Text(ctx, noteText, opts)
			if err != nil {
				failures <- err.Error()
			} else if res.Text != "############ wrote ###############" {
				failures <- res.Text
			}
		}()
	}
	wg.Wait()
	close(failures)
	for f := range failures {
		t.Errorf("got %q", f)
	}
}
