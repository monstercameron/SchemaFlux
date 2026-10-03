package schemaflux_test

import (
	"context"
	"math"
	"reflect"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

const mergeAnswer = `{"name":"Ada Lovelace","phone":"+1 555 0100"}`

var mergeSources = []textContact{{Name: "Ada L.", Phone: "5550100"}, {Name: "Ada Lovelace", Phone: ""}}

func init() {
	textForms = append(textForms, textForm{
		op: "merge", level: schemaflux.ContractJSONWellFormed, body: mergeAnswer, badBody: "not json",
		call: func(ctx context.Context, canary string) error {
			_, err := schemaflux.Merge(ctx, []textContact{{Name: "a " + canary, Phone: "1"}, {Name: "b " + canary, Phone: "2"}}, "prefer "+canary,
				schemaflux.Options{Steering: "steer " + canary})
			return err
		},
		refused: func(ctx context.Context, canary string) error {
			_, err := schemaflux.Merge[textContact](ctx, nil, "prefer "+canary, schemaflux.Options{Steering: "steer " + canary})
			return err
		},
		refusedKind: schemaflux.ErrInvalidRequest,
	})
}

func TestMergeReturnsTheMergedValueAsAnAnswerReadIntoTheSourcesType(t *testing.T) {
	t.Parallel()
	model := answeringWith(mergeAnswer)
	got, err := schemaflux.Merge(operationClient(model), mergeSources, "prefer the newest")
	if err != nil || got != (textContact{Name: "Ada Lovelace", Phone: "+1 555 0100"}) {
		t.Fatalf("Merge = %+v, %v", got, err)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	req := model.Last()
	wantText(t, req, "data merging expert")
	wantText(t, req, "Merge these sources:\n"+`{"name":"Ada L.","phone":"5550100"}`+"\n"+`{"name":"Ada Lovelace","phone":""}`)
	wantText(t, req, "Using strategy: \"prefer the newest\"")
	if req.Format != "json" || req.Schema != nil {
		t.Errorf("format %q schema %v: a merge asks for JSON and sends no schema", req.Format, req.Schema != nil)
	}
	// The default tier of Merge is TierSmart.
	if req.MaxTokens != 4000 || math.Abs(req.Temperature-0.3) > 1e-6 || req.Model != "test-model" {
		t.Errorf("ceiling %d temperature %v model %q", req.MaxTokens, req.Temperature, req.Model)
	}
}

func TestMergeOfOneSourceReturnsItAsItIsWithoutACall(t *testing.T) {
	t.Parallel()
	var recorder schemaflux.Recorder
	model := answeringWith(mergeAnswer)
	got, err := schemaflux.Merge(operationClient(model, recordingTo(&recorder)), mergeSources[:1], "first wins")
	if err != nil || got != mergeSources[0] {
		t.Fatalf("Merge = %+v, %v", got, err)
	}
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent for one source", model.Calls())
	}
	if r := onlyRecord(t, &recorder); r.Op != "merge" || r.Err != nil || r.Requests != 0 {
		t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
	}
}

func TestMergeOfOneSourceStillSendsItsOptionsThroughTheChecksAndTheFloor(t *testing.T) {
	t.Parallel()
	_, err := schemaflux.Merge(operationClient(answeringWith(mergeAnswer)), mergeSources[:1], "first wins", schemaflux.Options{MaxOutputTokens: -1})
	wantError(t, err, schemaflux.ErrConfiguration, "merge")

	model := namedModel(mergeAnswer)
	_, err = schemaflux.Merge(operationClient(model, underFloor(schemaflux.ContractSchemaConstrained)), mergeSources[:1], "first wins")
	wantError(t, err, schemaflux.ErrPolicy, "merge")
}

func TestMergeRefusesNoSourcesAsAnInvalidRequestBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	for name, sources := range map[string][]textContact{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(mergeAnswer)
			got, err := schemaflux.Merge(operationClient(model), sources, "first wins")
			e := wantError(t, err, schemaflux.ErrInvalidRequest, "merge")
			if e.Message != "sources is empty" || got != (textContact{}) || model.Calls() != 0 {
				t.Errorf("message %q, value %+v, %d requests", e.Message, got, model.Calls())
			}
		})
	}
}

func TestMergeRefusesASourceJSONCannotWriteAsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	type unwritable struct{ F func() }
	model := answeringWith(mergeAnswer)
	got, err := schemaflux.Merge(operationClient(model), []unwritable{{}, {}}, "first wins")
	if !errorHasKind(err, schemaflux.ErrInvalidRequest) || !reflect.DeepEqual(got, unwritable{}) || model.Calls() != 0 {
		t.Errorf("Merge = %+v, %v after %d requests", got, err, model.Calls())
	}
}

func TestMergeRefusesAnAnswerThatIsNotTheSourcesType(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"text":                       `Ada Lovelace`,
		"a list":                     `[1,2]`,
		"a member of the wrong type": `{"name":5,"phone":"x"}`,
		"a null":                     `null`,
		"empty":                      ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Merge(operationClient(answeringWith(body)), mergeSources, "first wins")
			wantError(t, err, schemaflux.ErrInvalidAnswer, "merge")
			if got != (textContact{}) {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestMergeReadsTheAnswerIntoWhateverTypeTheSourcesAre(t *testing.T) {
	t.Parallel()
	t.Run("a map", func(t *testing.T) {
		t.Parallel()
		got, err := schemaflux.Merge(operationClient(answeringWith(`{"a":1,"b":2}`)), []map[string]int{{"a": 1}, {"b": 2}}, "union")
		if err != nil || !reflect.DeepEqual(got, map[string]int{"a": 1, "b": 2}) {
			t.Errorf("got %v, %v", got, err)
		}
	})
	t.Run("a pointer to a struct", func(t *testing.T) {
		t.Parallel()
		got, err := schemaflux.Merge(operationClient(answeringWith(mergeAnswer)), []*textContact{{Name: "a"}, {Name: "b"}}, "union")
		if err != nil || got == nil || got.Name != "Ada Lovelace" {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("a slice", func(t *testing.T) {
		t.Parallel()
		got, err := schemaflux.Merge(operationClient(answeringWith(`[1,2,3]`)), [][]int{{1}, {2, 3}}, "concatenate")
		if err != nil || !reflect.DeepEqual(got, []int{1, 2, 3}) {
			t.Errorf("got %v, %v", got, err)
		}
	})
}

func TestMergeEachOptionFieldChangesTheRequest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.Options
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Steering", schemaflux.Options{Steering: "prefer billing"}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "prefer billing") }},
		{"Model", schemaflux.Options{Model: "pinned-model"}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"TierFast", schemaflux.Options{Tier: schemaflux.TierFast}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 2000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.Options{MaxOutputTokens: 66}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 66 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.Options{WebSearch: true}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"Effort", schemaflux.Options{Effort: schemaflux.EffortLow}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" {
				t.Errorf("effort %q", req.Effort)
			}
		}},
		{"ModeCreative", schemaflux.Options{Mode: schemaflux.ModeCreative}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.7) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(mergeAnswer)
			if _, err := schemaflux.Merge(operationClient(model), mergeSources, "union", tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestMergeRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		opts    schemaflux.Options
		message string
	}{
		"a negative output ceiling": {schemaflux.Options{MaxOutputTokens: -1}, "MaxOutputTokens must not be negative"},
		"a tier that is not one":    {schemaflux.Options{Tier: 5}, "Tier is not one of TierSmart, TierFast and TierQuick"},
		"a mode that is not one":    {schemaflux.Options{Mode: 5}, "Mode is not one of ModeStrict, ModeTransform and ModeCreative"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(mergeAnswer)
			got, err := schemaflux.Merge(operationClient(model), mergeSources, "union", tc.opts)
			e := wantError(t, err, schemaflux.ErrConfiguration, "merge")
			if e.Message != tc.message || got != (textContact{}) || model.Calls() != 0 {
				t.Errorf("message %q, value %+v, %d requests", e.Message, got, model.Calls())
			}
		})
	}
}

func TestMergeMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	model := answeringWith(mergeAnswer)
	_, err := schemaflux.Merge(operationClient(model), mergeSources, "union",
		schemaflux.Options{Model: "first", MaxOutputTokens: 10, WebSearch: true},
		schemaflux.Options{MaxOutputTokens: 20},
		schemaflux.Options{})
	if err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if req.Model != "first" || req.MaxTokens != 20 || !req.WebSearch {
		t.Errorf("model %q ceiling %d web %v", req.Model, req.MaxTokens, req.WebSearch)
	}
}

func TestMergeSendsTheSameRequestWhateverRecordsTheCall(t *testing.T) {
	t.Parallel()
	plain := answeringWith(mergeAnswer)
	if _, err := schemaflux.Merge(operationClient(plain), mergeSources, "union"); err != nil {
		t.Fatal(err)
	}
	var recorder schemaflux.Recorder
	recorded := answeringWith(mergeAnswer)
	if _, err := schemaflux.Merge(operationClient(recorded, recordingTo(&recorder)), mergeSources, "union"); err != nil {
		t.Fatal(err)
	}
	if requestText(plain.Last()) != requestText(recorded.Last()) || plain.Last().Format != recorded.Last().Format || plain.Last().MaxTokens != recorded.Last().MaxTokens {
		t.Error("a recorder changed what the model is asked")
	}
}
