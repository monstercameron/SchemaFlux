package schemaflux_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// extractedInvoice is the type the Extract tests fill.
type extractedInvoice struct {
	Number string  `json:"number"`
	Total  float64 `json:"total"`
}

const invoiceAnswer = `{"number":"A-17","total":12.5}`

func TestExtractReadsTheAnswerIntoTheType(t *testing.T) {
	t.Parallel()
	model := answeringWith(invoiceAnswer)
	got, err := schemaflux.Extract[extractedInvoice](operationClient(model), "Invoice A-17 for 12.50")
	if err != nil {
		t.Fatal(err)
	}
	if got != (extractedInvoice{Number: "A-17", Total: 12.5}) {
		t.Errorf("got %+v", got)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	req := model.Last()
	if req.Format != "json" || req.SchemaName != "extractedInvoice" || req.Schema == nil {
		t.Errorf("request = format %q schema name %q schema %v, want a declared JSON schema", req.Format, req.SchemaName, req.Schema != nil)
	}
	if !strings.Contains(req.User, "Invoice A-17 for 12.50") {
		t.Errorf("the input did not reach the user message: %q", excerpt(req.User))
	}
}

func TestExtractStrictSchemaWorksAloneThroughThePublicCall(t *testing.T) {
	t.Parallel()
	model := answeringWith(invoiceAnswer)
	got, err := schemaflux.Extract[extractedInvoice](operationClient(model), "Invoice A-17 for 12.50", schemaflux.ExtractOptions{StrictSchema: true})
	if err != nil {
		t.Fatalf("Extract with StrictSchema alone: %v", err)
	}
	if got != (extractedInvoice{Number: "A-17", Total: 12.5}) {
		t.Fatalf("Extract = %+v, want complete invoice", got)
	}
	if model.Calls() != 1 {
		t.Fatalf("provider calls = %d, want one", model.Calls())
	}
	if req := model.Last(); req.Schema == nil || !strings.Contains(req.User, "Enforce strict schema validation") || strings.Contains(req.User, "Allow partial extraction") {
		t.Fatalf("strict schema request has schema=%v and prompt %q", req.Schema != nil, excerpt(req.User))
	}
}

func TestExtractStrictChecksNumericFidelityThroughThePublicCall(t *testing.T) {
	t.Parallel()
	type account struct {
		ID float64 `json:"id"`
	}
	for _, tc := range []struct {
		name    string
		answer  string
		wantErr bool
	}{
		{"representable", `{"id":9007199254740992}`, false},
		{"precision loss", `{"id":9007199254740993}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(tc.answer)
			got, err := schemaflux.Extract[account](operationClient(model), "account ID", schemaflux.ExtractOptions{Mode: schemaflux.ModeStrict})
			if tc.wantErr {
				if !errors.Is(err, schemaflux.ErrInvalidAnswer) {
					t.Fatalf("Extract error = %v, want invalid answer", err)
				}
				if strings.Contains(err.Error(), "9007199254740993") || !strings.Contains(err.Error(), "/id") {
					t.Fatalf("Extract error reveals a number or omits its field path: %v", err)
				}
				if got != (account{}) {
					t.Fatalf("Extract returned a partial result: %+v", got)
				}
				return
			}
			if err != nil || got.ID != 9007199254740992 {
				t.Fatalf("Extract = %+v, %v", got, err)
			}
		})
	}
}

func TestExtractWithNoOptionsAsksForTheFastTierInTransformMode(t *testing.T) {
	t.Parallel()
	model := answeringWith(invoiceAnswer)
	if _, err := schemaflux.Extract[extractedInvoice](operationClient(model), "x"); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" {
		t.Errorf("temperature %v, ceiling %d, model %q: want 0.3, 2000, test-model", req.Temperature, req.MaxTokens, req.Model)
	}
	if req.Effort != "" || req.WebSearch || strings.Contains(req.User, "Additional instructions") {
		t.Errorf("a default call carried an effort, web search or extra steering: %+v", req)
	}
}

func TestExtractEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	hints := map[string]string{"total": "in euros"}
	cases := []struct {
		name  string
		opts  schemaflux.ExtractOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"SchemaHints", schemaflux.ExtractOptions{SchemaHints: hints}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Schema hints: total (in euros)")
			wantText(t, req, "Allow partial extraction if some fields are missing.")
		}},
		{"FieldRules", schemaflux.ExtractOptions{FieldRules: map[string]string{"total": "two decimals"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Field rules: total: two decimals")
		}},
		{"Examples", schemaflux.ExtractOptions{Examples: []any{extractedInvoice{Number: "Z-9", Total: 1}}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Follow these examples")
			wantText(t, req, "Z-9")
		}},
		{"StrictSchema", schemaflux.ExtractOptions{StrictSchema: true}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Enforce strict schema validation")
			wantNoText(t, req, "Allow partial extraction")
		}},
		{"StrictSchema with hints still allows no partial extraction", schemaflux.ExtractOptions{StrictSchema: true, SchemaHints: hints}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Allow partial extraction")
		}},
		{"NoPartial withholds the sentence", schemaflux.ExtractOptions{NoPartial: true, SchemaHints: hints}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Schema hints")
			wantNoText(t, req, "Allow partial extraction")
		}},
		{"NoPartial alone adds nothing", schemaflux.ExtractOptions{NoPartial: true}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Additional instructions")
		}},
		{"Steering", schemaflux.ExtractOptions{Steering: "read the euro column"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "read the euro column")
		}},
		{"Model", schemaflux.ExtractOptions{Model: "pinned-model"}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.ExtractOptions{Mode: schemaflux.ModeStrict}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v, want 0.1", req.Temperature)
			}
		}},
		{"ModeCreative", schemaflux.ExtractOptions{Mode: schemaflux.ModeCreative}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.7) > 1e-6 {
				t.Errorf("temperature = %v, want 0.7", req.Temperature)
			}
		}},
		{"TierSmart", schemaflux.ExtractOptions{Tier: schemaflux.TierSmart}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d, want the smart tier's 4000", req.MaxTokens)
			}
		}},
		{"TierQuick", schemaflux.ExtractOptions{Tier: schemaflux.TierQuick}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 1000 {
				t.Errorf("ceiling = %d, want the quick tier's 1000", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.ExtractOptions{MaxOutputTokens: 77}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 77 {
				t.Errorf("ceiling = %d, want 77", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.ExtractOptions{WebSearch: true}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("the request does not declare web search")
			}
		}},
		{"Effort", schemaflux.ExtractOptions{Effort: schemaflux.EffortLow}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" || req.EffortPreferred {
				t.Errorf("effort = %q preferred %v, want low, asked for by the caller", req.Effort, req.EffortPreferred)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(invoiceAnswer)
			if _, err := schemaflux.Extract[extractedInvoice](operationClient(model), "Invoice A-17", tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestExtractExactFieldsRefusesAPropertyTheTypeDoesNotName(t *testing.T) {
	t.Parallel()
	const extra = `{"number":"A-17","total":12.5,"notes":"x"}`
	for _, tc := range []struct {
		name    string
		opts    schemaflux.ExtractOptions
		refused bool
	}{
		{"by default an extra property is tolerated", schemaflux.ExtractOptions{}, false},
		{"ExactFields refuses it", schemaflux.ExtractOptions{ExactFields: true}, true},
		{"ModeStrict implies ExactFields", schemaflux.ExtractOptions{Mode: schemaflux.ModeStrict}, true},
		{"CompleteFields alone tolerates it", schemaflux.ExtractOptions{CompleteFields: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Extract[extractedInvoice](operationClient(answeringWith(extra)), "x", tc.opts)
			if !tc.refused {
				if err != nil || got.Number != "A-17" {
					t.Fatalf("got %+v, %v", got, err)
				}
				return
			}
			wantError(t, err, schemaflux.ErrInvalidAnswer, "extract")
			if got != (extractedInvoice{}) {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestExtractCompleteFieldsRefusesARequiredFieldLeftEmpty(t *testing.T) {
	t.Parallel()
	const empty = `{"number":"","total":12.5}`
	for _, tc := range []struct {
		name    string
		opts    schemaflux.ExtractOptions
		refused bool
	}{
		{"by default an empty field is tolerated", schemaflux.ExtractOptions{}, false},
		{"CompleteFields refuses it", schemaflux.ExtractOptions{CompleteFields: true}, true},
		{"ModeStrict implies CompleteFields", schemaflux.ExtractOptions{Mode: schemaflux.ModeStrict}, true},
		{"ExactFields alone tolerates it", schemaflux.ExtractOptions{ExactFields: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Extract[extractedInvoice](operationClient(answeringWith(empty)), "x", tc.opts)
			if !tc.refused {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			wantError(t, err, schemaflux.ErrInvalidAnswer, "extract")
			if got != (extractedInvoice{}) {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestExtractRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := map[string]schemaflux.ExtractOptions{
		"a negative output ceiling":      {MaxOutputTokens: -1},
		"a very negative ceiling":        {MaxOutputTokens: -1 << 30},
		"a tier that is not one":         {Tier: schemaflux.TierQuick + 1},
		"a negative tier":                {Tier: -1},
		"a mode that is not one":         {Mode: schemaflux.ModeCreative + 1},
		"a negative mode":                {Mode: -3},
		"a bad setting beside good ones": {StrictSchema: true, Model: "m", MaxOutputTokens: -2},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(invoiceAnswer)
			got, err := schemaflux.Extract[extractedInvoice](operationClient(model), "x", opts)
			wantError(t, err, schemaflux.ErrConfiguration, "extract")
			if got != (extractedInvoice{}) || model.Calls() != 0 {
				t.Errorf("got %+v after %d requests", got, model.Calls())
			}
		})
	}
}

func TestExtractWithANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	var nilContext context.Context
	got, err := schemaflux.Extract[extractedInvoice](nilContext, "x")
	e := wantError(t, err, schemaflux.ErrInvalidRequest, "extract")
	if e.Message != "the context is nil" || got != (extractedInvoice{}) {
		t.Errorf("message %q, value %+v", e.Message, got)
	}
}

func TestExtractMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("no value is the default", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(invoiceAnswer)
		if _, err := schemaflux.Extract[extractedInvoice](operationClient(model), "x"); err != nil {
			t.Fatal(err)
		}
		wantNoText(t, model.Last(), "Additional instructions")
	})
	t.Run("one value is used as it is", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(invoiceAnswer)
		_, err := schemaflux.Extract[extractedInvoice](operationClient(model), "x", schemaflux.ExtractOptions{Model: "only"})
		if err != nil || model.Last().Model != "only" {
			t.Errorf("model %q, err %v", model.Last().Model, err)
		}
	})
	t.Run("a later value that is not zero wins and a later zero leaves the earlier alone", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(invoiceAnswer)
		first := schemaflux.ExtractOptions{Model: "first", Steering: "keep this", MaxOutputTokens: 11}
		second := schemaflux.ExtractOptions{Model: "second"}
		if _, err := schemaflux.Extract[extractedInvoice](operationClient(model), "x", first, second); err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		if req.Model != "second" || req.MaxTokens != 11 {
			t.Errorf("model %q ceiling %d, want second and 11", req.Model, req.MaxTokens)
		}
		wantText(t, req, "keep this")
	})
	t.Run("a bool set by an earlier value is not cleared by a later one", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(invoiceAnswer)
		_, err := schemaflux.Extract[extractedInvoice](operationClient(model), "x",
			schemaflux.ExtractOptions{StrictSchema: true, WebSearch: true},
			schemaflux.ExtractOptions{StrictSchema: false, WebSearch: false})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "Enforce strict schema validation")
		if !model.Last().WebSearch {
			t.Error("web search was turned off by a later value")
		}
	})
	t.Run("a later map replaces the earlier one", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(invoiceAnswer)
		_, err := schemaflux.Extract[extractedInvoice](operationClient(model), "x",
			schemaflux.ExtractOptions{SchemaHints: map[string]string{"number": "first"}},
			schemaflux.ExtractOptions{SchemaHints: map[string]string{"total": "second"}})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "total (second)")
		wantNoText(t, model.Last(), "number (first)")
	})
	t.Run("three values", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(invoiceAnswer)
		_, err := schemaflux.Extract[extractedInvoice](operationClient(model), "x",
			schemaflux.ExtractOptions{Model: "a"},
			schemaflux.ExtractOptions{Model: "b"},
			schemaflux.ExtractOptions{Model: "c"})
		if err != nil || model.Last().Model != "c" {
			t.Errorf("model %q, err %v", model.Last().Model, err)
		}
	})
	t.Run("a bad setting in any value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Extract[extractedInvoice](operationClient(answeringWith(invoiceAnswer)), "x",
			schemaflux.ExtractOptions{}, schemaflux.ExtractOptions{MaxOutputTokens: -1})
		wantError(t, err, schemaflux.ErrConfiguration, "extract")
	})
}

func TestExtractReadsAnyKindOfInput(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		input any
		seen  string
	}{
		"text":            {"Invoice A-17", "Invoice A-17"},
		"a struct":        {extractedInvoice{Number: "B-2", Total: 3}, "B-2"},
		"a map":           {map[string]any{"ref": "C-3"}, "C-3"},
		"a slice":         {[]string{"D-4", "E-5"}, "E-5"},
		"a number":        {42, "42"},
		"text with quote": {`say "hi"`, `hi`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(invoiceAnswer)
			if _, err := schemaflux.Extract[extractedInvoice](operationClient(model), tc.input); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(model.Last().User, tc.seen) {
				t.Errorf("%q is not in the user message: %s", tc.seen, excerpt(model.Last().User))
			}
		})
	}
}

func TestExtractReturnsAnErrorTheCallerCanClassifyForEachWayTheCallFails(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(*schemafluxtest.Provider)
		kind  schemaflux.Kind
	}{
		{"the provider is unavailable", func(p *schemafluxtest.Provider) { p.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable}) }, schemaflux.ErrUnavailable},
		{"the provider is rate limited", func(p *schemafluxtest.Provider) {
			p.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited, Status: 429})
		}, schemaflux.ErrRateLimited},
		{"the key is refused", func(p *schemafluxtest.Provider) {
			p.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnauthorized, Status: 401})
		}, schemaflux.ErrUnauthorized},
		{"the answer is not JSON", func(p *schemafluxtest.Provider) { p.Reply("I cannot help with that.") }, schemaflux.ErrInvalidAnswer},
		{"the answer is the wrong shape", func(p *schemafluxtest.Provider) { p.Reply(`["a","b"]`) }, schemaflux.ErrInvalidAnswer},
		{"the answer has a field of the wrong type", func(p *schemafluxtest.Provider) { p.Reply(`{"number":7,"total":"x"}`) }, schemaflux.ErrInvalidAnswer},
		{"the answer is empty", func(p *schemafluxtest.Provider) { p.Reply("") }, schemaflux.ErrInvalidAnswer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			tc.setup(&model)
			got, err := schemaflux.Extract[extractedInvoice](operationClient(&model), "x")
			wantError(t, err, tc.kind, "extract")
			if got != (extractedInvoice{}) {
				t.Errorf("a failed call left %+v beside the error", got)
			}
		})
	}
}

func TestExtractWithACancelledContextReturnsCanceledAndSendsNothing(t *testing.T) {
	t.Parallel()
	model := answeringWith(invoiceAnswer)
	ctx, cancel := context.WithCancel(operationClient(model))
	cancel()
	_, err := schemaflux.Extract[extractedInvoice](ctx, "x")
	wantError(t, err, schemaflux.ErrCanceled, "extract")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the context's own error is not reachable: %v", err)
	}
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestExtractAddsOneRecordForEachCallWithTheOperationsName(t *testing.T) {
	t.Parallel()
	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(invoiceAnswer)
		if _, err := schemaflux.Extract[extractedInvoice](operationClient(model, recordingTo(&recorder)), "x"); err != nil {
			t.Fatal(err)
		}
		r := onlyRecord(t, &recorder)
		if r.Op != "extract" || r.Err != nil || r.Requests != 1 || r.Provider != "test" || r.Model != "test-model" {
			t.Errorf("record = %q err %v requests %d provider %q model %q", r.Op, r.Err, r.Requests, r.Provider, r.Model)
		}
		if r.Requested != schemaflux.ContractSchemaConstrained || r.Delivered != schemaflux.ContractSchemaConstrained {
			t.Errorf("levels = %v and %v, want schema constrained", r.Requested, r.Delivered)
		}
		if r.Provenance == nil || r.Provenance.ResultID == "" {
			t.Errorf("Extract fills the provenance: %+v", r.Provenance)
		}
		if len(r.Checks) == 0 {
			t.Error("Extract reports its checks")
		}
	})
	t.Run("a call that fails on its answer", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith("not json")
		_, err := schemaflux.Extract[extractedInvoice](operationClient(model, recordingTo(&recorder)), "x")
		wantError(t, err, schemaflux.ErrInvalidAnswer, "extract")
		r := onlyRecord(t, &recorder)
		if r.Op != "extract" || r.Err != err || r.Requests != 2 || r.Repairs != 1 {
			t.Errorf("record = %q err %v requests %d repairs %d, want the refused answer sent back once", r.Op, r.Err, r.Requests, r.Repairs)
		}
		if r.Delivered != schemaflux.ContractPromptOnly {
			t.Errorf("a failed call delivered %v", r.Delivered)
		}
	})
	t.Run("a call the provider fails", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		var model schemafluxtest.Provider
		model.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited})
		_, err := schemaflux.Extract[extractedInvoice](operationClient(&model, recordingTo(&recorder)), "x")
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrRateLimited, "extract")
		if r.Op != "extract" || r.Err != err {
			t.Errorf("record = %q err %v", r.Op, r.Err)
		}
	})
	t.Run("a call refused before anything is sent", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(invoiceAnswer)
		_, err := schemaflux.Extract[extractedInvoice](operationClient(model, recordingTo(&recorder)), "x",
			schemaflux.ExtractOptions{MaxOutputTokens: -1})
		r := onlyRecord(t, &recorder)
		if r.Op != "extract" || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a recorder on the context and one on the client each get the Record", func(t *testing.T) {
		t.Parallel()
		var onClient, onContext schemaflux.Recorder
		ctx := schemaflux.WithRecorder(operationClient(answeringWith(invoiceAnswer), recordingTo(&onClient)), &onContext)
		if _, err := schemaflux.Extract[extractedInvoice](ctx, "x"); err != nil {
			t.Fatal(err)
		}
		if onlyRecord(t, &onClient).Op != "extract" || onlyRecord(t, &onContext).Op != "extract" {
			t.Error("a recorder was not given the Record")
		}
	})
	t.Run("a Strict call asks for and delivers the checked level", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		_, err := schemaflux.Extract[extractedInvoice](operationClient(answeringWith(invoiceAnswer), recordingTo(&recorder)), "x",
			schemaflux.ExtractOptions{Mode: schemaflux.ModeStrict})
		if err != nil {
			t.Fatal(err)
		}
		r := onlyRecord(t, &recorder)
		if r.Requested != schemaflux.ContractSchemaAndInvariantChecked || r.Delivered != schemaflux.ContractSchemaAndInvariantChecked {
			t.Errorf("levels = %v and %v", r.Requested, r.Delivered)
		}
	})
}

func TestExtractUnderAFloorIsRefusedBeforeAnythingIsSentWhenItsLevelIsBelowIt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		floor   schemaflux.ContractLevel
		opts    schemaflux.ExtractOptions
		refused bool
	}{
		{"a floor the call meets", schemaflux.ContractSchemaConstrained, schemaflux.ExtractOptions{}, false},
		{"a floor below the call", schemaflux.ContractJSONWellFormed, schemaflux.ExtractOptions{}, false},
		{"a floor above the call", schemaflux.ContractSchemaAndInvariantChecked, schemaflux.ExtractOptions{}, true},
		{"the strict level, met by ModeStrict", schemaflux.ContractSchemaAndInvariantChecked, schemaflux.ExtractOptions{Mode: schemaflux.ModeStrict}, false},
		{"the strict level, met by ExactFields", schemaflux.ContractSchemaAndInvariantChecked, schemaflux.ExtractOptions{ExactFields: true}, false},
		{"a floor nothing meets", schemaflux.ContractFullyGoverned, schemaflux.ExtractOptions{Mode: schemaflux.ModeStrict}, true},
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
				model := namedModel(invoiceAnswer)
				settings := []func(*schemaflux.Client){underFloor(tc.floor)}
				if withRecorder {
					settings = append(settings, recordingTo(&recorder))
				}
				got, err := schemaflux.Extract[extractedInvoice](operationClient(model, settings...), "x", tc.opts)
				if tc.refused {
					wantError(t, err, schemaflux.ErrPolicy, "extract")
					if model.Calls() != 0 || got != (extractedInvoice{}) {
						t.Errorf("%d requests were sent and the value is %+v", model.Calls(), got)
					}
				} else if err != nil || got.Number != "A-17" {
					t.Fatalf("got %+v, %v", got, err)
				}
				if withRecorder {
					r := onlyRecord(t, &recorder)
					if r.Op != "extract" || (r.Err != nil) != tc.refused {
						t.Errorf("record = %q err %v", r.Op, r.Err)
					}
					if tc.refused && r.Requests != 0 {
						t.Errorf("a refused call recorded %d requests", r.Requests)
					}
				}
			})
		}
	}
}

func TestExtractUnderAnObserverSendsTheSameRequestAndRecordsOnce(t *testing.T) {
	// Not parallel: an observer is process state, and it changes the path of
	// every call that runs while it is installed.
	var seen []string
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			seen = append(seen, r.Operation)
		}
	}})
	defer restore()

	plain := answeringWith(invoiceAnswer)
	if _, err := schemaflux.Extract[extractedInvoice](operationClient(plain), "Invoice A-17"); err != nil {
		t.Fatal(err)
	}
	var recorder schemaflux.Recorder
	watched := answeringWith(invoiceAnswer)
	got, err := schemaflux.Extract[extractedInvoice](operationClient(watched, recordingTo(&recorder)), "Invoice A-17")
	if err != nil || got.Number != "A-17" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if requestText(plain.Last()) != requestText(watched.Last()) {
		t.Error("an observer changed what the model is asked")
	}
	if onlyRecord(t, &recorder).Op != "extract" {
		t.Error("the Record under an observer has the wrong operation")
	}
	if len(seen) != 2 || seen[0] != "extract" || seen[1] != "extract" {
		t.Errorf("the observer saw %v, want one extract for each call", seen)
	}
}

func TestExtractKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-5e1c7a"
	type canaryType struct {
		Value string `json:"value" desc:"CANARY-5e1c7a-tag"`
	}
	opts := schemaflux.ExtractOptions{
		Options:     schemaflux.Options{Steering: "steer " + canary},
		SchemaHints: map[string]string{canary + "-key": "hint " + canary},
		FieldRules:  map[string]string{"value": "rule " + canary},
	}
	input := map[string]string{canary + "-input-key": "text " + canary}

	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(`{"value":"fine"}`) }, true,
			func(ctx context.Context) error {
				_, err := schemaflux.Extract[canaryType](ctx, input, opts)
				return err
			})
	})
	t.Run("an answer that is refused", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("the value is " + canary + " not json") }, true,
			func(ctx context.Context) error {
				_, err := schemaflux.Extract[canaryType](ctx, input, opts)
				return err
			})
	})
	t.Run("an answer of the wrong shape that quotes the input", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(`{"value":["` + canary + `"],"` + canary + `":1}`) }, true,
			func(ctx context.Context) error {
				_, err := schemaflux.Extract[canaryType](ctx, input, schemaflux.ExtractOptions{ExactFields: true, Options: opts.Options})
				return err
			})
	})
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false,
			func(ctx context.Context) error {
				_, err := schemaflux.Extract[canaryType](ctx, input, opts)
				return err
			})
	})
	t.Run("a refused option", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("{}") }, true,
			func(ctx context.Context) error {
				bad := opts
				bad.MaxOutputTokens = -1
				_, err := schemaflux.Extract[canaryType](ctx, input, bad)
				return err
			})
	})
}

// levelCall is one call of an operation into a map and into a struct, for the
// tests that hold the Record's level to the type: a map has no schema a strict
// provider can be given, and the level is the one of a call that sent none.
type levelCall struct {
	op string
	// mapReply and structReply are what the model answers to each call.
	mapReply, structReply string
	intoMap, intoStruct   func(ctx context.Context) error
}

// wantLevelFollowsTheType holds a call into a map to the level of a call that
// sent no schema, and a call into a struct to schema constrained: what the
// Record says, what a floor of schema constrained does with each, and that the
// provider is never reached by the refused one. A map call used to be recorded
// and admitted as schema constrained though nothing declared a schema.
func wantLevelFollowsTheType(t *testing.T, c levelCall) {
	t.Helper()
	t.Run("a map is delivered as well formed, and no schema is sent", func(t *testing.T) {
		var recorder schemaflux.Recorder
		model := answeringWith(c.mapReply)
		if err := c.intoMap(operationClient(model, recordingTo(&recorder))); err != nil {
			t.Fatal(err)
		}
		if model.Calls() != 1 || model.Last().Schema != nil {
			t.Errorf("%d requests were sent and the last carried a schema: %v", model.Calls(), model.Last().Schema != nil)
		}
		r := onlyRecord(t, &recorder)
		if r.Op != c.op || r.Requested != schemaflux.ContractJSONWellFormed || r.Delivered != schemaflux.ContractJSONWellFormed {
			t.Errorf("record = %q requested %v delivered %v, want well formed", r.Op, r.Requested, r.Delivered)
		}
	})
	for _, withRecorder := range []bool{false, true} {
		name := "a map is refused under a schema-constrained floor before anything is sent"
		if withRecorder {
			name += ", with a recorder"
		}
		t.Run(name, func(t *testing.T) {
			var recorder schemaflux.Recorder
			model := namedModel(c.mapReply)
			settings := []func(*schemaflux.Client){underFloor(schemaflux.ContractSchemaConstrained)}
			if withRecorder {
				settings = append(settings, recordingTo(&recorder))
			}
			err := c.intoMap(operationClient(model, settings...))
			wantError(t, err, schemaflux.ErrPolicy, c.op)
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent for a call that cannot meet the floor", model.Calls())
			}
			if withRecorder {
				if r := onlyRecord(t, &recorder); r.Op != c.op || r.Requests != 0 || r.Err == nil || r.Requested != schemaflux.ContractJSONWellFormed {
					t.Errorf("record = %q requests %d err %v requested %v", r.Op, r.Requests, r.Err, r.Requested)
				}
			}
		})
	}
	t.Run("a map is admitted under a well-formed floor", func(t *testing.T) {
		model := namedModel(c.mapReply)
		if err := c.intoMap(operationClient(model, underFloor(schemaflux.ContractJSONWellFormed))); err != nil || model.Calls() != 1 {
			t.Fatalf("requests=%d err=%v", model.Calls(), err)
		}
	})
	t.Run("a struct is admitted under a schema-constrained floor and delivered as schema constrained", func(t *testing.T) {
		var recorder schemaflux.Recorder
		model := namedModel(c.structReply)
		if err := c.intoStruct(operationClient(model, recordingTo(&recorder), underFloor(schemaflux.ContractSchemaConstrained))); err != nil {
			t.Fatal(err)
		}
		if model.Calls() != 1 || model.Last().Schema == nil {
			t.Errorf("%d requests were sent and the last carried a schema: %v", model.Calls(), model.Last().Schema != nil)
		}
		r := onlyRecord(t, &recorder)
		if r.Op != c.op || r.Requested != schemaflux.ContractSchemaConstrained || r.Delivered != schemaflux.ContractSchemaConstrained {
			t.Errorf("record = %q requested %v delivered %v, want schema constrained", r.Op, r.Requested, r.Delivered)
		}
	})
}

func TestExtractIntoAMapIsDeliveredAsWellFormedAndHeldToThatLevelByAFloor(t *testing.T) {
	t.Parallel()
	wantLevelFollowsTheType(t, levelCall{
		op: "extract", mapReply: `{"number":"A-17"}`, structReply: invoiceAnswer,
		intoMap: func(ctx context.Context) error {
			_, err := schemaflux.Extract[map[string]string](ctx, "x")
			return err
		},
		intoStruct: func(ctx context.Context) error {
			_, err := schemaflux.Extract[extractedInvoice](ctx, "x")
			return err
		},
	})
	// The options that lift a declared schema to schema and invariants checked
	// have nothing to lift when none was declared.
	t.Run("strict mode into a map is still well formed", func(t *testing.T) {
		var recorder schemaflux.Recorder
		got, err := schemaflux.Extract[map[string]string](operationClient(namedModel(`{"number":"A-17"}`), recordingTo(&recorder)), "x",
			schemaflux.ExtractOptions{Mode: schemaflux.ModeStrict})
		if err != nil || got["number"] != "A-17" {
			t.Fatalf("got %v, %v", got, err)
		}
		if r := onlyRecord(t, &recorder); r.Requested != schemaflux.ContractJSONWellFormed || r.Delivered != schemaflux.ContractJSONWellFormed {
			t.Errorf("requested %v delivered %v, want well formed", r.Requested, r.Delivered)
		}
	})
	t.Run("strict mode into a map is refused under the floor strict mode meets for a struct", func(t *testing.T) {
		model := namedModel(`{"number":"A-17"}`)
		_, err := schemaflux.Extract[map[string]string](operationClient(model, underFloor(schemaflux.ContractSchemaAndInvariantChecked)), "x",
			schemaflux.ExtractOptions{Mode: schemaflux.ModeStrict})
		wantError(t, err, schemaflux.ErrPolicy, "extract")
		if model.Calls() != 0 {
			t.Errorf("%d requests were sent", model.Calls())
		}
		if _, err := schemaflux.Extract[extractedInvoice](operationClient(namedModel(invoiceAnswer), underFloor(schemaflux.ContractSchemaAndInvariantChecked)), "x",
			schemaflux.ExtractOptions{Mode: schemaflux.ModeStrict}); err != nil {
			t.Errorf("a struct in strict mode is refused under that floor: %v", err)
		}
	})
}
