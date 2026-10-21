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

// textMemo is the structured value the Compress tests compress.
type textMemo struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

var compressInput = textMemo{Title: "Q3 report", Body: "Revenue grew twelve percent while costs stayed flat across the quarter."}

const compressAnswer = `{"compressed":{"title":"Q3 report","body":"Revenue up 12%, costs flat."},"preserved_info":["revenue growth"],"removed_info":["the wording"]}`

func init() {
	payload := func(canary string) schemaflux.CompressOptions {
		return schemaflux.CompressOptions{
			Options:      schemaflux.Options{Steering: "steer " + canary},
			RetainFields: []string{"retain " + canary}, RemoveFields: []string{"remove " + canary}, PreserveInfo: []string{"keep " + canary},
		}
	}
	textForms = append(textForms, textForm{
		op: "compress", level: schemaflux.ContractSchemaConstrained, body: compressAnswer, badBody: "not json",
		call: func(ctx context.Context, canary string) error {
			_, err := schemaflux.Compress(ctx, textMemo{Title: "t " + canary, Body: "b " + canary}, payload(canary))
			return err
		},
		refused: func(ctx context.Context, canary string) error {
			o := payload(canary)
			o.Strategy = "x" + canary
			_, err := schemaflux.Compress(ctx, textMemo{Title: "t " + canary, Body: "b " + canary}, o)
			return err
		},
	})
}

func TestCompressReturnsTheSmallerValueWithItsSizesMeasuredHere(t *testing.T) {
	t.Parallel()
	model := answeringWith(compressAnswer)
	got, err := schemaflux.Compress(operationClient(model), compressInput)
	if err != nil {
		t.Fatal(err)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	inputJSON, _ := json.Marshal(compressInput)
	compressedJSON, _ := json.Marshal(got.Compressed)
	want := schemaflux.CompressResult[textMemo]{
		Compressed:     textMemo{Title: "Q3 report", Body: "Revenue up 12%, costs flat."},
		OriginalSize:   len([]rune(string(inputJSON))),
		CompressedSize: len([]rune(string(compressedJSON))),
		PreservedInfo:  []string{"revenue growth"}, RemovedInfo: []string{"the wording"},
	}
	want.ActualRatio = float64(want.CompressedSize) / float64(want.OriginalSize)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	req := model.Last()
	wantText(t, req, "semantic data compression")
	wantText(t, req, "Compress this:")
	if req.Schema == nil || req.SchemaName != "compression" {
		t.Errorf("schema %v name %q: a compression of a struct is asked for as a declared schema", req.Schema != nil, req.SchemaName)
	}
}

func TestCompressOfATextReturnsTheCompressedText(t *testing.T) {
	t.Parallel()
	input := strings.Repeat("the report is long ", 10)
	got, err := schemaflux.Compress(operationClient(answeringWith(`{"compressed":"a long report","preserved_info":[],"removed_info":[]}`)), input)
	if err != nil || got.Compressed != "a long report" || got.OriginalSize != len([]rune(input)) || got.CompressedSize != 13 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if want := 13.0 / float64(len([]rune(input))); math.Abs(got.ActualRatio-want) > 1e-12 {
		t.Errorf("ratio = %v, want %v", got.ActualRatio, want)
	}
}

func TestCompressWithNoOptionsAsksForAThirdOfTheSizeWithTheSemanticStrategyAtTheFastTier(t *testing.T) {
	t.Parallel()
	model := answeringWith(compressAnswer)
	if _, err := schemaflux.Compress(operationClient(model), compressInput); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	wantText(t, req, "(30% of original)")
	wantText(t, req, "Preserve meaning and essential information")
	wantText(t, req, "Balance all types of information.")
	wantText(t, req, "Maintain the same format as input")
	for _, absent := range []string{"Must retain", "Remove these fields", "Preserve this specific"} {
		wantNoText(t, req, absent)
	}
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" || req.WebSearch || req.Effort != "" {
		t.Errorf("temperature %v ceiling %d model %q web %v effort %q", req.Temperature, req.MaxTokens, req.Model, req.WebSearch, req.Effort)
	}
}

func TestCompressEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.CompressOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Ratio", schemaflux.CompressOptions{Ratio: 0.5}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "(50% of original)") }},
		{"Ratio 1", schemaflux.CompressOptions{Ratio: 1}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "(100% of original)") }},
		{"Ratio 0.05", schemaflux.CompressOptions{Ratio: 0.05}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "(5% of original)") }},
		{"RetainFields", schemaflux.CompressOptions{RetainFields: []string{"title", "body"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Must retain these fields/information: ["title","body"]`)
		}},
		{"RemoveFields", schemaflux.CompressOptions{RemoveFields: []string{"missing"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Remove these fields/information: ["missing"]`)
		}},
		{"PreserveInfo", schemaflux.CompressOptions{PreserveInfo: []string{"dates"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Preserve this specific information: ["dates"]`)
		}},
		{"Strategy lossy", schemaflux.CompressOptions{Strategy: "lossy"}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "Aggressive compression") }},
		{"Strategy lossless", schemaflux.CompressOptions{Strategy: "lossless"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Preserve all information, only remove redundancy")
		}},
		{"Priority facts", schemaflux.CompressOptions{Priority: "facts"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Prioritize keeping factual information and data.")
		}},
		{"Priority actions", schemaflux.CompressOptions{Priority: "actions"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Prioritize keeping action items and to-dos.")
		}},
		{"Priority decisions", schemaflux.CompressOptions{Priority: "decisions"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Prioritize keeping decisions and conclusions.")
		}},
		{"OutputFormat summary", schemaflux.CompressOptions{OutputFormat: "summary"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Output as a flowing summary paragraph.")
		}},
		{"OutputFormat key_points", schemaflux.CompressOptions{OutputFormat: "key_points"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Output as bullet points of key information.")
		}},
		{"OutputFormat structured", schemaflux.CompressOptions{OutputFormat: "structured"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Output as structured JSON with labeled sections.")
		}},
		{"MaxSize caps the target", schemaflux.CompressOptions{MaxSize: 26}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Target size: approximately 26 characters")
		}},
		{"Steering", schemaflux.CompressOptions{Options: schemaflux.Options{Steering: "keep numbers"}}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "keep numbers") }},
		{"Model", schemaflux.CompressOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"TierSmart", schemaflux.CompressOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.CompressOptions{Options: schemaflux.Options{MaxOutputTokens: 66}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 66 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.CompressOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"ModeStrict", schemaflux.CompressOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// An answer small enough for any MaxSize the cases set.
			model := answeringWith(`{"compressed":{"title":"Q3","body":"up"},"preserved_info":[],"removed_info":[]}`)
			if _, err := schemaflux.Compress(operationClient(model), compressInput, tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestCompressEnforcesMaxSizeAndTheFieldListsThatNameAFieldOfTheInput(t *testing.T) {
	t.Parallel()
	small := `{"compressed":{"title":"Q3","body":"up"},"preserved_info":[],"removed_info":[]}`
	noTitle := `{"compressed":{"body":"Revenue up."},"preserved_info":[],"removed_info":[]}`
	cases := []struct {
		name    string
		opts    schemaflux.CompressOptions
		answer  string
		refused bool
	}{
		{"an answer within MaxSize", schemaflux.CompressOptions{MaxSize: 60}, small, false},
		{"an answer over MaxSize", schemaflux.CompressOptions{MaxSize: 10}, small, true},
		{"an answer exactly at MaxSize", schemaflux.CompressOptions{MaxSize: 26}, small, false},
		{"an answer one character over MaxSize", schemaflux.CompressOptions{MaxSize: 25}, small, true},
		{"no MaxSize allows any size", schemaflux.CompressOptions{}, compressAnswer, false},
		{"a retained field that is present", schemaflux.CompressOptions{RetainFields: []string{"title"}}, small, false},
		{"a retained field that is dropped", schemaflux.CompressOptions{RetainFields: []string{"title"}}, noTitle, true},
		{"a retained entry that is not a field of the input is a request", schemaflux.CompressOptions{RetainFields: []string{"the amounts"}}, noTitle, false},
		{"a removed field that is gone", schemaflux.CompressOptions{RemoveFields: []string{"title"}}, noTitle, false},
		{"a removed field that is still there", schemaflux.CompressOptions{RemoveFields: []string{"title"}}, small, true},
		{"a removed entry that is not a field of the input", schemaflux.CompressOptions{RemoveFields: []string{"the amounts"}}, small, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Compress(operationClient(answeringWith(tc.answer)), compressInput, tc.opts)
			if !tc.refused {
				if err != nil {
					t.Fatalf("Compress failed: %v", err)
				}
				return
			}
			wantError(t, err, schemaflux.ErrInvalidAnswer, "compress")
			if !reflect.DeepEqual(got, schemaflux.CompressResult[textMemo]{}) {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestCompressRefusesAnAnswerThatIsNotACompression(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"text":                      `Revenue up.`,
		"a list":                    `["a"]`,
		"no compressed member":      `{"preserved_info":[],"removed_info":[]}`,
		"a null compressed member":  `{"compressed":null,"preserved_info":[],"removed_info":[]}`,
		"no preserved_info":         `{"compressed":{"title":"a","body":"b"},"removed_info":[]}`,
		"no removed_info":           `{"compressed":{"title":"a","body":"b"},"preserved_info":[]}`,
		"a compressed of the wrong": `{"compressed":[1],"preserved_info":[],"removed_info":[]}`,
		"empty":                     ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Compress(operationClient(answeringWith(body)), compressInput)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "compress")
			if !reflect.DeepEqual(got, schemaflux.CompressResult[textMemo]{}) {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestCompressRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	const ratio = "Ratio must be above 0 and at most 1, or zero for the default"
	cases := map[string]struct {
		opts    schemaflux.CompressOptions
		message string
	}{
		"a ratio above 1":               {schemaflux.CompressOptions{Ratio: 1.5}, ratio},
		"a negative ratio":              {schemaflux.CompressOptions{Ratio: -0.1}, ratio},
		"a ratio that is not a number":  {schemaflux.CompressOptions{Ratio: math.NaN()}, ratio},
		"an infinite ratio":             {schemaflux.CompressOptions{Ratio: math.Inf(1)}, ratio},
		"a strategy that is not one":    {schemaflux.CompressOptions{Strategy: "tight"}, "Strategy is not one of lossy, lossless and semantic"},
		"a priority that is not one":    {schemaflux.CompressOptions{Priority: "names"}, "Priority is not one of facts, actions, decisions and all"},
		"a format that is not one":      {schemaflux.CompressOptions{OutputFormat: "xml"}, "OutputFormat is not one of same, summary, key_points and structured"},
		"a strategy in another case":    {schemaflux.CompressOptions{Strategy: "Lossy"}, "Strategy is not one of lossy, lossless and semantic"},
		"a negative max size":           {schemaflux.CompressOptions{MaxSize: -1}, "MaxSize must not be negative"},
		"a negative output ceiling":     {schemaflux.CompressOptions{Options: schemaflux.Options{MaxOutputTokens: -1}}, "MaxOutputTokens must not be negative"},
		"a tier that is not one":        {schemaflux.CompressOptions{Options: schemaflux.Options{Tier: 5}}, "Tier is not one of TierSmart, TierFast and TierQuick"},
		"a bad setting among good ones": {schemaflux.CompressOptions{Ratio: 0.5, Strategy: "lossy", Priority: "x"}, "Priority is not one of facts, actions, decisions and all"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(compressAnswer)
			got, err := schemaflux.Compress(operationClient(model), compressInput, tc.opts)
			e := wantError(t, err, schemaflux.ErrConfiguration, "compress")
			if e.Message != tc.message || !reflect.DeepEqual(got, schemaflux.CompressResult[textMemo]{}) || model.Calls() != 0 {
				t.Errorf("message %q, value %+v, %d requests", e.Message, got, model.Calls())
			}
		})
	}
}

func TestCompressMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later value wins and a later zero leaves the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(compressAnswer)
		_, err := schemaflux.Compress(operationClient(model), compressInput,
			schemaflux.CompressOptions{Ratio: 0.5, Strategy: "lossy", Priority: "facts", Options: schemaflux.Options{Model: "first"}},
			schemaflux.CompressOptions{Ratio: 0.2},
			schemaflux.CompressOptions{})
		if err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "(20% of original)")
		wantText(t, req, "Aggressive compression")
		wantText(t, req, "Prioritize keeping factual information")
		if req.Model != "first" {
			t.Errorf("model = %q", req.Model)
		}
	})
	t.Run("a later list replaces the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(compressAnswer)
		_, err := schemaflux.Compress(operationClient(model), compressInput,
			schemaflux.CompressOptions{PreserveInfo: []string{"first"}}, schemaflux.CompressOptions{PreserveInfo: []string{"second"}})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), `["second"]`)
		wantNoText(t, model.Last(), "first")
	})
	t.Run("a bad setting in a later value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Compress(operationClient(answeringWith(compressAnswer)), compressInput,
			schemaflux.CompressOptions{}, schemaflux.CompressOptions{Ratio: 3})
		wantError(t, err, schemaflux.ErrConfiguration, "compress")
	})
}

func TestCompressSendsTheSameRequestWhateverRecordsTheCall(t *testing.T) {
	t.Parallel()
	plain := answeringWith(compressAnswer)
	if _, err := schemaflux.Compress(operationClient(plain), compressInput); err != nil {
		t.Fatal(err)
	}
	var recorder schemaflux.Recorder
	recorded := answeringWith(compressAnswer)
	if _, err := schemaflux.Compress(operationClient(recorded, recordingTo(&recorder)), compressInput); err != nil {
		t.Fatal(err)
	}
	floored := namedModel(compressAnswer)
	if _, err := schemaflux.Compress(operationClient(floored, underFloor(schemaflux.ContractSchemaConstrained)), compressInput); err != nil {
		t.Fatal(err)
	}
	for name, other := range map[string]schemaflux.Request{"a recorder": recorded.Last(), "a floor": floored.Last()} {
		if requestText(plain.Last()) != requestText(other) || plain.Last().SchemaName != other.SchemaName || plain.Last().Format != other.Format {
			t.Errorf("%s changed what the model is asked", name)
		}
	}
}

func TestCompressResultMarshalsWithTheTagsOfTheInternalResult(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(schemaflux.CompressResult[string]{Compressed: "c", OriginalSize: 10, CompressedSize: 3, ActualRatio: 0.3, PreservedInfo: []string{"p"}, RemovedInfo: []string{"r"}})
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	_ = json.Unmarshal(got, &a)
	_ = json.Unmarshal([]byte(`{"compressed":"c","original_size":10,"compressed_size":3,"actual_ratio":0.3,"preserved_info":["p"],"removed_info":["r"]}`), &b)
	if !reflect.DeepEqual(a, b) || strings.Contains(string(got), "metadata") {
		t.Errorf("got %s", got)
	}
	empty, _ := json.Marshal(schemaflux.CompressResult[string]{})
	if strings.Contains(string(empty), "preserved_info") || strings.Contains(string(empty), "removed_info") {
		t.Errorf("an empty list is written: %s", empty)
	}
}

func TestCompressOfAMapIsDeliveredAsWellFormedAndHeldToThatLevelByAFloor(t *testing.T) {
	t.Parallel()
	wantLevelFollowsTheType(t, levelCall{
		op:       "compress",
		mapReply: `{"compressed":{"body":"Revenue up."},"preserved_info":["growth"],"removed_info":["wording"]}`, structReply: compressAnswer,
		intoMap: func(ctx context.Context) error {
			_, err := schemaflux.Compress(ctx, map[string]string{"body": "Revenue grew twelve percent while costs stayed flat."})
			return err
		},
		intoStruct: func(ctx context.Context) error {
			_, err := schemaflux.Compress(ctx, compressInput)
			return err
		},
	})
}
