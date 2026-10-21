package schemaflux_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

const (
	summaryText   = "The quarterly report shows revenue grew twelve percent while costs stayed flat."
	summaryShort  = "Revenue grew."
	summaryDetail = `{"text":"Revenue grew twelve percent.","key_points":["revenue up 12%","costs flat"],"confidence":0.85}`
)

func TestSummarizeReturnsTheModelsTextTrimmed(t *testing.T) {
	t.Parallel()
	model := answeringWith("  \n" + summaryShort + "  \n")
	got, err := schemaflux.Summarize(operationClient(model), summaryText)
	if err != nil || got != summaryShort {
		t.Fatalf("Summarize = %q, %v", got, err)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	req := model.Last()
	wantText(t, req, "text summarization expert")
	wantText(t, req, "Summarize this text:")
	wantText(t, req, summaryText)
	if req.Format != "text" || req.Schema != nil {
		t.Errorf("format %q schema %v: a summary is the model's text, with no schema", req.Format, req.Schema != nil)
	}
}

func TestSummarizeWithNoOptionsAsksForAParagraphAtTheFastTier(t *testing.T) {
	t.Parallel()
	model := answeringWith(summaryShort)
	if _, err := schemaflux.Summarize(operationClient(model), summaryText); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	wantNoText(t, req, "Target length")
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" || req.WebSearch || req.Effort != "" {
		t.Errorf("temperature %v ceiling %d model %q web %v effort %q", req.Temperature, req.MaxTokens, req.Model, req.WebSearch, req.Effort)
	}
	wantText(t, req, "Style: paragraph")
}

func TestSummarizeEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.SummarizeOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"TargetLength with no unit is in sentences", schemaflux.SummarizeOptions{TargetLength: 2}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Target length: 2 sentences")
		}},
		{"TargetLength in words", schemaflux.SummarizeOptions{TargetLength: 30, LengthUnit: "words"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Target length: 30 words")
		}},
		{"TargetLength in paragraphs", schemaflux.SummarizeOptions{TargetLength: 1, LengthUnit: "paragraphs"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Target length: 1 paragraphs")
		}},
		{"LengthUnit with no target says nothing", schemaflux.SummarizeOptions{LengthUnit: "words"}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Target length")
		}},
		{"Style", schemaflux.SummarizeOptions{Style: "executive"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Style: executive")
		}},
		{"BulletPoints replace the style", schemaflux.SummarizeOptions{BulletPoints: true, Style: "executive"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Format as bullet points")
			wantNoText(t, req, "Style:")
		}},
		{"FocusAreas", schemaflux.SummarizeOptions{FocusAreas: []string{"revenue", "costs"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Focus on: revenue, costs")
		}},
		{"PreserveInfo", schemaflux.SummarizeOptions{PreserveInfo: []string{"dates", "names"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Must preserve: dates, names")
		}},
		{"Steering", schemaflux.SummarizeOptions{Steering: "write for a child"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "write for a child")
		}},
		{"Model", schemaflux.SummarizeOptions{Model: "pinned-model"}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"TierSmart", schemaflux.SummarizeOptions{Tier: schemaflux.TierSmart}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.SummarizeOptions{MaxOutputTokens: 66}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 66 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.SummarizeOptions{WebSearch: true}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"Effort", schemaflux.SummarizeOptions{Effort: schemaflux.EffortLow}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" || req.EffortPreferred {
				t.Errorf("effort %q preferred %v", req.Effort, req.EffortPreferred)
			}
		}},
		{"ModeCreative", schemaflux.SummarizeOptions{Mode: schemaflux.ModeCreative}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.7) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(summaryShort)
			if _, err := schemaflux.Summarize(operationClient(model), summaryText, tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestSummarizeTargetLengthIsARequestAndNothingIsMeasured(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("word ", 200)
	got, err := schemaflux.Summarize(operationClient(answeringWith(long)), summaryText, schemaflux.SummarizeOptions{TargetLength: 3, LengthUnit: "words"})
	if err != nil || strings.TrimSpace(long) != got {
		t.Errorf("a long answer to a short target was refused or cut: %v", err)
	}
}

func TestSummarizeRefusesAnEmptyAnswer(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{"empty": "", "spaces": "   ", "newlines and tabs": "\n\t\n"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Summarize(operationClient(answeringWith(body)), summaryText)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "summarize")
			if got != "" {
				t.Errorf("an empty answer left %q beside the error", got)
			}
		})
	}
}

func TestSummarizeRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		opts    schemaflux.SummarizeOptions
		message string
	}{
		"a negative target":             {schemaflux.SummarizeOptions{TargetLength: -1}, "TargetLength must not be negative"},
		"a very negative target":        {schemaflux.SummarizeOptions{TargetLength: -1 << 30}, "TargetLength must not be negative"},
		"a unit that is not one":        {schemaflux.SummarizeOptions{LengthUnit: "pages"}, "LengthUnit is not one of words, sentences and paragraphs"},
		"a unit in another case":        {schemaflux.SummarizeOptions{LengthUnit: "Words"}, "LengthUnit is not one of words, sentences and paragraphs"},
		"a negative output ceiling":     {schemaflux.SummarizeOptions{MaxOutputTokens: -1}, "MaxOutputTokens must not be negative"},
		"a tier that is not one":        {schemaflux.SummarizeOptions{Tier: 5}, "Tier is not one of TierSmart, TierFast and TierQuick"},
		"a mode that is not one":        {schemaflux.SummarizeOptions{Mode: 5}, "Mode is not one of ModeStrict, ModeTransform and ModeCreative"},
		"a bad setting among good ones": {schemaflux.SummarizeOptions{TargetLength: 3, Style: "bullet", MaxOutputTokens: -9}, "MaxOutputTokens must not be negative"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(summaryShort)
			got, err := schemaflux.Summarize(operationClient(model), summaryText, tc.opts)
			e := wantError(t, err, schemaflux.ErrConfiguration, "summarize")
			if e.Message != tc.message || got != "" || model.Calls() != 0 {
				t.Errorf("message %q, value %q, %d requests", e.Message, got, model.Calls())
			}
		})
	}
}

func TestSummarizeWithANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	var nilContext context.Context
	got, err := schemaflux.Summarize(nilContext, summaryText)
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "summarize"); e.Message != "the context is nil" || got != "" {
		t.Errorf("message %q, value %q", e.Message, got)
	}
}

func TestSummarizeMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later value wins and a later zero leaves the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(summaryShort)
		_, err := schemaflux.Summarize(operationClient(model), summaryText,
			schemaflux.SummarizeOptions{TargetLength: 5, LengthUnit: "words", Style: "bullet", Model: "first"},
			schemaflux.SummarizeOptions{TargetLength: 9},
			schemaflux.SummarizeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "Target length: 9 words")
		wantText(t, req, "Style: bullet")
		if req.Model != "first" {
			t.Errorf("model = %q", req.Model)
		}
	})
	t.Run("a bool turned on stays on", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(summaryShort)
		_, err := schemaflux.Summarize(operationClient(model), summaryText, schemaflux.SummarizeOptions{BulletPoints: true}, schemaflux.SummarizeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "Format as bullet points")
	})
	t.Run("a later list replaces the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(summaryShort)
		_, err := schemaflux.Summarize(operationClient(model), summaryText,
			schemaflux.SummarizeOptions{FocusAreas: []string{"first"}}, schemaflux.SummarizeOptions{FocusAreas: []string{"second"}})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "Focus on: second")
		wantNoText(t, model.Last(), "first")
	})
	t.Run("a bad setting in a later value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Summarize(operationClient(answeringWith(summaryShort)), summaryText,
			schemaflux.SummarizeOptions{}, schemaflux.SummarizeOptions{LengthUnit: "pages"})
		wantError(t, err, schemaflux.ErrConfiguration, "summarize")
	})
}

func TestSummarizeReturnsAnErrorTheCallerCanClassifyWhenTheProviderFails(t *testing.T) {
	t.Parallel()
	for name, kind := range map[string]schemaflux.Kind{
		"unavailable":  schemaflux.ErrUnavailable,
		"rate limited": schemaflux.ErrRateLimited,
		"unauthorized": schemaflux.ErrUnauthorized,
		"timed out":    schemaflux.ErrTimeout,
		"refused":      schemaflux.ErrRefused,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			model.Fail(&schemaflux.Error{Kind: kind})
			got, err := schemaflux.Summarize(operationClient(&model), summaryText)
			wantError(t, err, kind, "summarize")
			if got != "" {
				t.Errorf("a failed call left %q beside the error", got)
			}
		})
	}
}

func TestSummarizeWithACancelledContextReturnsCanceledAndSendsNothing(t *testing.T) {
	t.Parallel()
	model := answeringWith(summaryShort)
	ctx, cancel := context.WithCancel(operationClient(model))
	cancel()
	_, err := schemaflux.Summarize(ctx, summaryText)
	wantError(t, err, schemaflux.ErrCanceled, "summarize")
	if !errors.Is(err, context.Canceled) || model.Calls() != 0 {
		t.Errorf("err %v after %d requests", err, model.Calls())
	}
}

func TestSummarizeSendsTheSameRequestWhateverRecordsTheCallAndNeverTheDetailOne(t *testing.T) {
	t.Parallel()
	plain := answeringWith(summaryShort)
	if _, err := schemaflux.Summarize(operationClient(plain), summaryText); err != nil {
		t.Fatal(err)
	}
	var recorder schemaflux.Recorder
	recorded := answeringWith(summaryShort)
	if _, err := schemaflux.Summarize(operationClient(recorded, recordingTo(&recorder)), summaryText); err != nil {
		t.Fatal(err)
	}
	if requestText(plain.Last()) != requestText(recorded.Last()) || plain.Last().Format != recorded.Last().Format {
		t.Error("a recorder changed what the model is asked")
	}
	wantNoText(t, recorded.Last(), "key_points")
	if onlyRecord(t, &recorder).Op != "summarize" {
		t.Error("the Record has the wrong operation")
	}
}

func TestSummarizeAddsOneRecordForEachCallWithTheOperationsName(t *testing.T) {
	t.Parallel()
	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		if _, err := schemaflux.Summarize(operationClient(answeringWith(summaryShort), recordingTo(&recorder)), summaryText); err != nil {
			t.Fatal(err)
		}
		r := onlyRecord(t, &recorder)
		if r.Op != "summarize" || r.Err != nil || r.Requests != 1 || r.Provider != "test" {
			t.Errorf("record = %q err %v requests %d provider %q", r.Op, r.Err, r.Requests, r.Provider)
		}
		if r.Requested != schemaflux.ContractPromptOnly || r.Delivered != schemaflux.ContractPromptOnly {
			t.Errorf("levels = %v and %v, want prompt only", r.Requested, r.Delivered)
		}
	})
	t.Run("a call that fails on its answer", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		_, err := schemaflux.Summarize(operationClient(answeringWith(""), recordingTo(&recorder)), summaryText)
		wantError(t, err, schemaflux.ErrInvalidAnswer, "summarize")
		r := onlyRecord(t, &recorder)
		if r.Op != "summarize" || r.Err != err || r.Requests != 1 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a call the provider fails", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		var model schemafluxtest.Provider
		model.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited})
		_, err := schemaflux.Summarize(operationClient(&model, recordingTo(&recorder)), summaryText)
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrRateLimited, "summarize")
		if r.Op != "summarize" || r.Err != err {
			t.Errorf("record = %q err %v", r.Op, r.Err)
		}
	})
	t.Run("a call refused before anything is sent", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(summaryShort)
		_, err := schemaflux.Summarize(operationClient(model, recordingTo(&recorder)), summaryText, schemaflux.SummarizeOptions{TargetLength: -1})
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrConfiguration, "summarize")
		if r.Op != "summarize" || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a recorder on the context and one on the client each get the Record", func(t *testing.T) {
		t.Parallel()
		var onClient, onContext schemaflux.Recorder
		ctx := schemaflux.WithRecorder(operationClient(answeringWith(summaryShort), recordingTo(&onClient)), &onContext)
		if _, err := schemaflux.Summarize(ctx, summaryText); err != nil {
			t.Fatal(err)
		}
		if onlyRecord(t, &onClient).Op != "summarize" || onlyRecord(t, &onContext).Op != "summarize" {
			t.Error("a recorder was not given the Record")
		}
	})
}

func TestSummarizeUnderAnyFloorIsRefusedBeforeAnythingIsSentBecauseItDeliversTheModelsText(t *testing.T) {
	t.Parallel()
	for _, floor := range []schemaflux.ContractLevel{
		schemaflux.ContractJSONWellFormed, schemaflux.ContractSchemaConstrained,
		schemaflux.ContractSchemaAndInvariantChecked, schemaflux.ContractEvidenceChecked,
	} {
		for _, withRecorder := range []bool{false, true} {
			name := floor.String()
			if withRecorder {
				name += ", with a recorder"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				var recorder schemaflux.Recorder
				model := namedModel(summaryShort)
				settings := []func(*schemaflux.Client){underFloor(floor)}
				if withRecorder {
					settings = append(settings, recordingTo(&recorder))
				}
				got, err := schemaflux.Summarize(operationClient(model, settings...), summaryText)
				wantError(t, err, schemaflux.ErrPolicy, "summarize")
				if got != "" || model.Calls() != 0 {
					t.Errorf("answer %q after %d requests", got, model.Calls())
				}
				if withRecorder {
					r := onlyRecord(t, &recorder)
					if r.Op != "summarize" || r.Err != err || r.Requests != 0 {
						t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
					}
				}
			})
		}
	}
}

func TestSummarizeUnderAnObserverSendsTheSameRequestAndRecordsOnce(t *testing.T) {
	// Not parallel: an observer is process state.
	var seen []string
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			seen = append(seen, r.Operation)
		}
	}})
	defer restore()

	plain := answeringWith(summaryShort)
	if _, err := schemaflux.Summarize(operationClient(plain), summaryText); err != nil {
		t.Fatal(err)
	}
	var recorder schemaflux.Recorder
	watched := answeringWith(summaryShort)
	got, err := schemaflux.Summarize(operationClient(watched, recordingTo(&recorder)), summaryText)
	if err != nil || got != summaryShort {
		t.Fatalf("Summarize = %q, %v", got, err)
	}
	if requestText(plain.Last()) != requestText(watched.Last()) {
		t.Error("an observer changed what the model is asked")
	}
	if onlyRecord(t, &recorder).Op != "summarize" {
		t.Error("the Record under an observer has the wrong operation")
	}
	if len(seen) != 2 || seen[0] != "summarize" || seen[1] != "summarize" {
		t.Errorf("the observer saw %v, want one summarize for each call", seen)
	}
}

func TestSummarizeKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-c81d5e"
	opts := schemaflux.SummarizeOptions{
		Options:      schemaflux.Options{Steering: "steer " + canary},
		FocusAreas:   []string{"focus " + canary},
		PreserveInfo: []string{"keep " + canary},
		Style:        "style " + canary,
	}
	run := func(ctx context.Context) error {
		_, err := schemaflux.Summarize(ctx, "text "+canary, opts)
		return err
	}
	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("a summary") }, true, run)
	})
	t.Run("an empty answer", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("") }, true, run)
	})
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, run)
	})
	t.Run("a refused option", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("a summary") }, true,
			func(ctx context.Context) error {
				bad := opts
				bad.LengthUnit = canary
				_, err := schemaflux.Summarize(ctx, "text "+canary, bad)
				return err
			})
	})
}

func TestSummarizeDetailReturnsTheSummaryWithWhatTheModelReportedAndWhatWasMeasured(t *testing.T) {
	t.Parallel()
	model := answeringWith(summaryDetail)
	got, err := schemaflux.SummarizeDetail(operationClient(model), summaryText, schemaflux.SummarizeOptions{TargetLength: 4, LengthUnit: "words"})
	if err != nil {
		t.Fatal(err)
	}
	want := schemaflux.Summary{
		Text: "Revenue grew twelve percent.", KeyPoints: []string{"revenue up 12%", "costs flat"},
		ModelConfidence: 0.85, Length: 4,
	}
	if got.Text != want.Text || got.ModelConfidence != want.ModelConfidence || got.Length != want.Length ||
		len(got.KeyPoints) != 2 || got.KeyPoints[0] != want.KeyPoints[0] || got.KeyPoints[1] != want.KeyPoints[1] {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if wantRatio := float64(len([]rune(want.Text))) / float64(len([]rune(summaryText))); math.Abs(got.CompressionRatio-wantRatio) > 1e-9 {
		t.Errorf("compression ratio %v, want the length of the summary over the input in runes: %v", got.CompressionRatio, wantRatio)
	}
}

func TestSummarizeDetailLengthIsMeasuredInTheUnitAskedForAndIsZeroWithNoTarget(t *testing.T) {
	t.Parallel()
	const body = `{"text":"One sentence here. And a second one.","key_points":[],"confidence":0.5}`
	cases := []struct {
		name string
		opts schemaflux.SummarizeOptions
		want int
	}{
		{"no target", schemaflux.SummarizeOptions{}, 0},
		{"sentences", schemaflux.SummarizeOptions{TargetLength: 2}, 2},
		{"words", schemaflux.SummarizeOptions{TargetLength: 8, LengthUnit: "words"}, 7},
		{"paragraphs", schemaflux.SummarizeOptions{TargetLength: 1, LengthUnit: "paragraphs"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.SummarizeDetail(operationClient(answeringWith(body)), summaryText, tc.opts)
			if err != nil || got.Length != tc.want {
				t.Errorf("length = %d, err %v, want %d", got.Length, err, tc.want)
			}
		})
	}
}

func TestSummarizeDetailRefusesASummaryMoreThanTwentyPercentOverTheTargetButNotOneWithinTheTolerance(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		body    string
		target  int
		refused bool
	}{
		{"exactly the target", `{"text":"one two three","key_points":[],"confidence":0.5}`, 3, false},
		{"under the target", `{"text":"one two","key_points":[],"confidence":0.5}`, 3, false},
		{"within the tolerance", `{"text":"one two three four five six","key_points":[],"confidence":0.5}`, 5, false},
		{"far over the target", `{"text":"one two three four five six seven eight nine ten","key_points":[],"confidence":0.5}`, 3, true},
		{"over by more than a fifth", `{"text":"one two three four five six seven","key_points":[],"confidence":0.5}`, 5, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.SummarizeDetail(operationClient(answeringWith(tc.body)), summaryText,
				schemaflux.SummarizeOptions{TargetLength: tc.target, LengthUnit: "words"})
			if !tc.refused {
				if err != nil || got.Text == "" {
					t.Fatalf("got %+v, %v", got, err)
				}
				return
			}
			wantError(t, err, schemaflux.ErrInvalidAnswer, "summarizedetail")
			if got.Text != "" || got.Length != 0 {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestSummarizeDetailRefusesAnAnswerThatIsNotASummary(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"text":                       `A short summary.`,
		"a list":                     `["a"]`,
		"no text":                    `{"key_points":["a"],"confidence":0.5}`,
		"an empty text":              `{"text":"","key_points":["a"],"confidence":0.5}`,
		"a text that is a number":    `{"text":5,"key_points":[],"confidence":0.5}`,
		"no confidence":              `{"text":"x","key_points":[]}`,
		"a confidence off the scale": `{"text":"x","key_points":[],"confidence":4}`,
		"empty":                      ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.SummarizeDetail(operationClient(answeringWith(body)), summaryText)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "summarizedetail")
			if got.Text != "" || got.KeyPoints != nil || got.ModelConfidence != 0 {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestSummarizeDetailAsksAnotherQuestionThanSummarizeDoes(t *testing.T) {
	t.Parallel()
	plain, detail := answeringWith(summaryShort), answeringWith(summaryDetail)
	if _, err := schemaflux.Summarize(operationClient(plain), summaryText); err != nil {
		t.Fatal(err)
	}
	if _, err := schemaflux.SummarizeDetail(operationClient(detail), summaryText); err != nil {
		t.Fatal(err)
	}
	wantNoText(t, plain.Last(), "key_points")
	wantText(t, detail.Last(), "key_points")
	if plain.Last().Format != "text" || detail.Last().Format != "json" {
		t.Errorf("formats = %q and %q", plain.Last().Format, detail.Last().Format)
	}
}

func TestSummarizeDetailEachOptionChangesTheRequest(t *testing.T) {
	t.Parallel()
	model := answeringWith(summaryDetail)
	_, err := schemaflux.SummarizeDetail(operationClient(model), summaryText, schemaflux.SummarizeOptions{
		TargetLength: 30, LengthUnit: "words", Style: "executive", FocusAreas: []string{"revenue"}, PreserveInfo: []string{"dates"},
		Model: "pinned", Steering: "plainly", MaxOutputTokens: 77, WebSearch: true, Effort: schemaflux.EffortLow, Mode: schemaflux.ModeStrict,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	for _, text := range []string{"Target length: 30 words", "Style: executive", "Focus on: revenue", "Must preserve: dates", "plainly"} {
		wantText(t, req, text)
	}
	if req.Model != "pinned" || req.MaxTokens != 77 || !req.WebSearch || req.Effort != "low" || math.Abs(req.Temperature-0.1) > 1e-6 {
		t.Errorf("request = model %q ceiling %d web %v effort %q temperature %v", req.Model, req.MaxTokens, req.WebSearch, req.Effort, req.Temperature)
	}
}

func TestSummarizeDetailRefusesABadOptionAndANilContextBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	model := answeringWith(summaryDetail)
	for name, opts := range map[string]schemaflux.SummarizeOptions{
		"a negative target":      {TargetLength: -2},
		"a unit that is not one": {LengthUnit: "lines"},
		"a negative ceiling":     {MaxOutputTokens: -1},
		"a tier that is not one": {Tier: 6},
	} {
		_, err := schemaflux.SummarizeDetail(operationClient(model), summaryText, opts)
		if e := wantError(t, err, schemaflux.ErrConfiguration, "summarizedetail"); e.Message == "" {
			t.Errorf("%s: no message", name)
		}
	}
	var nilContext context.Context
	_, err := schemaflux.SummarizeDetail(nilContext, summaryText)
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "summarizedetail"); e.Message != "the context is nil" {
		t.Errorf("message = %q", e.Message)
	}
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestSummarizeDetailSendsTheSameRequestInEveryState(t *testing.T) {
	t.Parallel()
	plain := answeringWith(summaryDetail)
	if _, err := schemaflux.SummarizeDetail(operationClient(plain), summaryText); err != nil {
		t.Fatal(err)
	}
	var recorder schemaflux.Recorder
	recorded := answeringWith(summaryDetail)
	if _, err := schemaflux.SummarizeDetail(operationClient(recorded, recordingTo(&recorder)), summaryText); err != nil {
		t.Fatal(err)
	}
	floored := namedModel(summaryDetail)
	if _, err := schemaflux.SummarizeDetail(operationClient(floored, underFloor(schemaflux.ContractJSONWellFormed)), summaryText); err != nil {
		t.Fatal(err)
	}
	for name, other := range map[string]*schemafluxtest.Provider{"a recorder": recorded, "a floor": floored} {
		if requestText(plain.Last()) != requestText(other.Last()) || plain.Last().Format != other.Last().Format {
			t.Errorf("%s changed what the model is asked", name)
		}
	}
}

func TestSummarizeDetailAddsOneRecordWithTheTwinsMeta(t *testing.T) {
	t.Parallel()
	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		if _, err := schemaflux.SummarizeDetail(operationClient(answeringWith(summaryDetail), recordingTo(&recorder)), summaryText); err != nil {
			t.Fatal(err)
		}
		r := onlyRecord(t, &recorder)
		if r.Op != "summarizedetail" || r.Err != nil || r.Requests != 1 || r.Provider != "test" {
			t.Errorf("record = %q err %v requests %d provider %q", r.Op, r.Err, r.Requests, r.Provider)
		}
		if r.Requested != schemaflux.ContractJSONWellFormed || r.Delivered != schemaflux.ContractJSONWellFormed {
			t.Errorf("levels = %v and %v, want json well-formed", r.Requested, r.Delivered)
		}
	})
	t.Run("a call that fails on its answer", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		_, err := schemaflux.SummarizeDetail(operationClient(answeringWith("not json"), recordingTo(&recorder)), summaryText)
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrInvalidAnswer, "summarizedetail")
		if r.Op != "summarizedetail" || r.Err != err || r.Requests != 1 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a call the provider fails", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		var model schemafluxtest.Provider
		model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable})
		_, err := schemaflux.SummarizeDetail(operationClient(&model, recordingTo(&recorder)), summaryText)
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrUnavailable, "summarizedetail")
		if r.Err != err {
			t.Errorf("record err %v, call err %v", r.Err, err)
		}
	})
	t.Run("a call refused before anything is sent", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(summaryDetail)
		_, err := schemaflux.SummarizeDetail(operationClient(model, recordingTo(&recorder)), summaryText, schemaflux.SummarizeOptions{LengthUnit: "x"})
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrConfiguration, "summarizedetail")
		if r.Op != "summarizedetail" || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
}

func TestSummarizeDetailUnderAFloorIsHeldToTheLevelItEstablishes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		floor   schemaflux.ContractLevel
		refused bool
	}{
		{"a floor of well-formed JSON is met", schemaflux.ContractJSONWellFormed, false},
		{"a floor of schema constrained is not", schemaflux.ContractSchemaConstrained, true},
		{"a floor of evidence is not", schemaflux.ContractEvidenceChecked, true},
	} {
		for _, withRecorder := range []bool{false, true} {
			name := tc.name
			if withRecorder {
				name += ", with a recorder"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				var recorder schemaflux.Recorder
				model := namedModel(summaryDetail)
				settings := []func(*schemaflux.Client){underFloor(tc.floor)}
				if withRecorder {
					settings = append(settings, recordingTo(&recorder))
				}
				got, err := schemaflux.SummarizeDetail(operationClient(model, settings...), summaryText)
				if tc.refused {
					wantError(t, err, schemaflux.ErrPolicy, "summarizedetail")
					if got.Text != "" || model.Calls() != 0 {
						t.Errorf("answer %+v after %d requests", got, model.Calls())
					}
				} else if err != nil || got.Text == "" {
					t.Fatalf("SummarizeDetail = %+v, %v", got, err)
				}
				if withRecorder {
					r := onlyRecord(t, &recorder)
					if r.Op != "summarizedetail" || (r.Err != nil) != tc.refused || r.Requested != schemaflux.ContractJSONWellFormed {
						t.Errorf("record = %q err %v requested %v", r.Op, r.Err, r.Requested)
					}
				}
			})
		}
	}
}

func TestSummarizeDetailUnderAnObserverRecordsOnce(t *testing.T) {
	// Not parallel: an observer is process state.
	var seen []string
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			seen = append(seen, r.Operation)
		}
	}})
	defer restore()

	var recorder schemaflux.Recorder
	got, err := schemaflux.SummarizeDetail(operationClient(answeringWith(summaryDetail), recordingTo(&recorder)), summaryText)
	if err != nil || got.Text == "" {
		t.Fatalf("SummarizeDetail = %+v, %v", got, err)
	}
	if onlyRecord(t, &recorder).Op != "summarizedetail" {
		t.Error("the Record under an observer has the wrong operation")
	}
	// One scope for the call, named as the Record of the call is.
	if len(seen) != 1 || seen[0] != "summarizedetail" {
		t.Errorf("the observer saw %v, want one operation, summarizedetail", seen)
	}
}

func TestSummarizeDetailKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-e07a93"
	opts := schemaflux.SummarizeOptions{
		TargetLength: 3, LengthUnit: "words",
		Options:      schemaflux.Options{Steering: "steer " + canary},
		FocusAreas:   []string{"focus " + canary},
		PreserveInfo: []string{"keep " + canary},
	}
	run := func(ctx context.Context) error {
		_, err := schemaflux.SummarizeDetail(ctx, "text "+canary, opts)
		return err
	}
	for name, setup := range map[string]func(*schemafluxtest.Provider){
		"a call that succeeds": func(p *schemafluxtest.Provider) { p.Reply(`{"text":"short","key_points":[],"confidence":0.5}`) },
		"a summary over the target": func(p *schemafluxtest.Provider) {
			p.Reply(`{"text":"` + strings.Repeat(canary+" ", 10) + `","key_points":[],"confidence":0.5}`)
		},
		"an answer that is text": func(p *schemafluxtest.Provider) { p.Reply("the summary is " + canary) },
		"an answer of the wrong shape": func(p *schemafluxtest.Provider) {
			p.Reply(`{"text":["` + canary + `"],"key_points":"` + canary + `","confidence":"` + canary + `"}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			requireNoPayload(t, canary, setup, true, run)
		})
	}
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, run)
	})
}

func TestSummaryMarshalsWithTheTagsOfTheInternalSummary(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(schemaflux.Summary{Text: "t", KeyPoints: []string{"k"}, ModelConfidence: 0.5, CompressionRatio: 0.25, Length: 3})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"text":"t","key_points":["k"],"confidence":0.5,"compression_ratio":0.25,"length":3}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
