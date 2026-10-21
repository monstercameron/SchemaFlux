package schemaflux_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// parsedPerson is the type the Parse tests read into.
type parsedPerson struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

const (
	// parseUnreadable is text no Go parser recognises, so only a model can read it.
	parseUnreadable = "Ada is thirty six years old"
	parseAnswer     = `{"name":"Ada","age":36}`
)

func TestParseReadsEachLocalFormatWithoutAskingAModel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		input  any
		opts   schemaflux.ParseOptions
		format string
	}{
		{"JSON", `{"name":"Ada","age":36}`, schemaflux.ParseOptions{}, "json"},
		{"JSON bytes", []byte(`{"name":"Ada","age":36}`), schemaflux.ParseOptions{}, "json"},
		{"JSON with spaces around it", "  \n{\"name\":\"Ada\",\"age\":36}\n ", schemaflux.ParseOptions{}, "json"},
		{"XML", `<person><Name>Ada</Name><Age>36</Age></person>`, schemaflux.ParseOptions{}, "xml"},
		{"YAML", "name: Ada\nage: 36", schemaflux.ParseOptions{}, "yaml"},
		{"pipe delimited with the column names in a hint", "Ada|36", schemaflux.ParseOptions{FormatHints: []string{"name|age"}}, "pipe-delimited"},
		{"a format named by a hint", `{"name":"Ada","age":36}`, schemaflux.ParseOptions{FormatHints: []string{"json"}}, "json"},
		{"a custom delimiter", "Ada;36", schemaflux.ParseOptions{CustomDelimiters: []string{";"}, FormatHints: []string{"name|age"}}, "custom-delimited"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(parseAnswer)
			got, err := schemaflux.Parse[parsedPerson](operationClient(model), tc.input, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got.Data != (parsedPerson{Name: "Ada", Age: 36}) || got.Format != tc.format {
				t.Errorf("got %+v in format %q, want Ada 36 in %q", got.Data, got.Format, tc.format)
			}
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent for input the Go parsers read", model.Calls())
			}
		})
	}
}

func TestParseReadsACSVTableIntoASlice(t *testing.T) {
	t.Parallel()
	model := answeringWith("{}")
	got, err := schemaflux.Parse[[]parsedPerson](operationClient(model), "name,age\nAda,36\nGrace,45")
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != "csv" || len(got.Data) != 2 || got.Data[1] != (parsedPerson{Name: "Grace", Age: 45}) {
		t.Errorf("got %+v", got)
	}
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestParseAsksTheModelOnlyWhenTheFallbackIsAllowedAndNoParserReadsTheInput(t *testing.T) {
	t.Parallel()
	t.Run("the fallback reads what the parsers cannot, and says so in the format", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(parseAnswer)
		got, err := schemaflux.Parse[parsedPerson](operationClient(model), parseUnreadable, schemaflux.ParseOptions{AllowLLMFallback: true})
		if err != nil {
			t.Fatal(err)
		}
		if got.Data != (parsedPerson{Name: "Ada", Age: 36}) || got.Format != "unknown (LLM-assisted)" {
			t.Errorf("got %+v in format %q", got.Data, got.Format)
		}
		if model.Calls() != 1 {
			t.Fatalf("%d requests were sent, want 1", model.Calls())
		}
		wantText(t, model.Last(), "Ada is thirty six years old")
	})
	t.Run("without the fallback nothing is sent and the call fails", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(parseAnswer)
		got, err := schemaflux.Parse[parsedPerson](operationClient(model), parseUnreadable)
		if err == nil {
			t.Fatal("input no parser reads was accepted")
		}
		wantError(t, err, schemaflux.ErrInvalidRequest, "parse")
		if got != (schemaflux.ParseResult[parsedPerson]{}) || model.Calls() != 0 {
			t.Errorf("got %+v after %d requests", got, model.Calls())
		}
	})
	t.Run("input the parsers read is not sent even when the fallback is allowed", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(parseAnswer)
		_, err := schemaflux.Parse[parsedPerson](operationClient(model), `{"name":"Ada","age":36}`, schemaflux.ParseOptions{AllowLLMFallback: true})
		if err != nil || model.Calls() != 0 {
			t.Errorf("err %v after %d requests", err, model.Calls())
		}
	})
}

func TestParseEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.ParseOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"AutoFix asks the model to repair", schemaflux.ParseOptions{AutoFix: true}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Attempt to fix malformed or incomplete data")
		}},
		{"FormatHints reach the model", schemaflux.ParseOptions{FormatHints: []string{"a quirky ledger"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Format hints: ["a quirky ledger"]`)
		}},
		{"CustomDelimiters reach the model when none is in the input", schemaflux.ParseOptions{CustomDelimiters: []string{"@@"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Custom delimiters: ["@@"]`)
		}},
		{"Steering", schemaflux.ParseOptions{Options: schemaflux.Options{Steering: "dates are day first"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "dates are day first")
		}},
		{"Model", schemaflux.ParseOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.ParseOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v, want 0.1", req.Temperature)
			}
		}},
		{"TierSmart", schemaflux.ParseOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d, want the smart tier's 4000", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.ParseOptions{Options: schemaflux.Options{MaxOutputTokens: 77}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 77 {
				t.Errorf("ceiling = %d, want 77", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.ParseOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("the request does not declare web search")
			}
		}},
		{"Effort", schemaflux.ParseOptions{Options: schemaflux.Options{Effort: schemaflux.EffortLow}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" || req.EffortPreferred {
				t.Errorf("effort = %q preferred %v, want low, asked for by the caller", req.Effort, req.EffortPreferred)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(parseAnswer)
			opts := tc.opts
			opts.AllowLLMFallback = true
			if _, err := schemaflux.Parse[parsedPerson](operationClient(model), parseUnreadable, opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
	t.Run("AutoFix does nothing unless the fallback is allowed", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(parseAnswer)
		_, err := schemaflux.Parse[parsedPerson](operationClient(model), parseUnreadable, schemaflux.ParseOptions{AutoFix: true})
		wantError(t, err, schemaflux.ErrInvalidRequest, "parse")
		if model.Calls() != 0 {
			t.Errorf("%d requests were sent", model.Calls())
		}
	})
	t.Run("without AutoFix the repair sentence is absent", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(parseAnswer)
		_, err := schemaflux.Parse[parsedPerson](operationClient(model), parseUnreadable, schemaflux.ParseOptions{AllowLLMFallback: true})
		if err != nil {
			t.Fatal(err)
		}
		wantNoText(t, model.Last(), "Attempt to fix")
	})
}

func TestParseWithAnOptionThatNothingCanUseIsRefusedBeforeAnythingIsRead(t *testing.T) {
	t.Parallel()
	cases := map[string]schemaflux.ParseOptions{
		"a negative output ceiling":      {Options: schemaflux.Options{MaxOutputTokens: -1}},
		"a tier that is not one":         {Options: schemaflux.Options{Tier: 7}},
		"a mode that is not one":         {Options: schemaflux.Options{Mode: -1}},
		"an empty custom delimiter":      {CustomDelimiters: []string{""}},
		"an empty one after a good one":  {CustomDelimiters: []string{";", ""}},
		"a bad setting beside good ones": {AllowLLMFallback: true, FormatHints: []string{"json"}, Options: schemaflux.Options{MaxOutputTokens: -5}},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(parseAnswer)
			got, err := schemaflux.Parse[parsedPerson](operationClient(model), `{"name":"Ada","age":36}`, opts)
			wantError(t, err, schemaflux.ErrConfiguration, "parse")
			if got != (schemaflux.ParseResult[parsedPerson]{}) || model.Calls() != 0 {
				t.Errorf("got %+v after %d requests", got, model.Calls())
			}
		})
	}
}

func TestParseRefusesInputThatHoldsNothingToParse(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]any{"an empty string": "", "only space": " \n\t ", "nil": nil} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(parseAnswer)
			got, err := schemaflux.Parse[parsedPerson](operationClient(model), input, schemaflux.ParseOptions{AllowLLMFallback: true})
			wantError(t, err, schemaflux.ErrInvalidRequest, "parse")
			if got != (schemaflux.ParseResult[parsedPerson]{}) || model.Calls() != 0 {
				t.Errorf("got %+v after %d requests", got, model.Calls())
			}
		})
	}
}

func TestParseRefusesAHintThatNamesNoFieldWithoutAskingTheModel(t *testing.T) {
	t.Parallel()
	model := answeringWith(parseAnswer)
	_, err := schemaflux.Parse[parsedPerson](operationClient(model), "Ada|36", schemaflux.ParseOptions{FormatHints: []string{"nickname|years"}, AllowLLMFallback: true})
	wantError(t, err, schemaflux.ErrConfiguration, "parse")
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent for a configuration the model cannot fix", model.Calls())
	}
}

func TestParseMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("one value is used as it is", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(parseAnswer)
		if _, err := schemaflux.Parse[parsedPerson](operationClient(model), parseUnreadable, schemaflux.ParseOptions{AllowLLMFallback: true}); err != nil {
			t.Fatal(err)
		}
		if model.Calls() != 1 {
			t.Errorf("%d requests", model.Calls())
		}
	})
	t.Run("a bool set by an earlier value is not cleared by a later one", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(parseAnswer)
		_, err := schemaflux.Parse[parsedPerson](operationClient(model), parseUnreadable,
			schemaflux.ParseOptions{AllowLLMFallback: true, AutoFix: true}, schemaflux.ParseOptions{AllowLLMFallback: false, AutoFix: false})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "Attempt to fix")
	})
	t.Run("a later list replaces the earlier one", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(parseAnswer)
		_, err := schemaflux.Parse[parsedPerson](operationClient(model), parseUnreadable,
			schemaflux.ParseOptions{AllowLLMFallback: true, FormatHints: []string{"first hint"}},
			schemaflux.ParseOptions{FormatHints: []string{"second hint"}})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "second hint")
		wantNoText(t, model.Last(), "first hint")
	})
	t.Run("common settings merge as in every operation", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(parseAnswer)
		_, err := schemaflux.Parse[parsedPerson](operationClient(model), parseUnreadable,
			schemaflux.ParseOptions{AllowLLMFallback: true, Options: schemaflux.Options{Model: "first", MaxOutputTokens: 11}},
			schemaflux.ParseOptions{Options: schemaflux.Options{Model: "second"}})
		if err != nil || model.Last().Model != "second" || model.Last().MaxTokens != 11 {
			t.Errorf("err %v model %q ceiling %d", err, model.Last().Model, model.Last().MaxTokens)
		}
	})
	t.Run("a bad setting in any value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Parse[parsedPerson](operationClient(answeringWith(parseAnswer)), `{"name":"Ada","age":36}`,
			schemaflux.ParseOptions{}, schemaflux.ParseOptions{CustomDelimiters: []string{""}})
		wantError(t, err, schemaflux.ErrConfiguration, "parse")
	})
}

func TestParseReturnsAnErrorTheCallerCanClassifyForEachWayTheFallbackFails(t *testing.T) {
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
		{"the answer is not JSON", func(p *schemafluxtest.Provider) { p.Reply("I cannot read that.") }, schemaflux.ErrInvalidAnswer},
		{"the answer is the wrong shape", func(p *schemafluxtest.Provider) { p.Reply(`["a","b"]`) }, schemaflux.ErrInvalidAnswer},
		{"the answer is empty", func(p *schemafluxtest.Provider) { p.Reply("") }, schemaflux.ErrInvalidAnswer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			tc.setup(&model)
			got, err := schemaflux.Parse[parsedPerson](operationClient(&model), parseUnreadable, schemaflux.ParseOptions{AllowLLMFallback: true})
			wantError(t, err, tc.kind, "parse")
			if got != (schemaflux.ParseResult[parsedPerson]{}) {
				t.Errorf("a failed call left %+v beside the error", got)
			}
		})
	}
}

func TestParseWithACancelledContextAndTheFallbackReturnsCanceledAndSendsNothing(t *testing.T) {
	t.Parallel()
	model := answeringWith(parseAnswer)
	ctx, cancel := context.WithCancel(operationClient(model))
	cancel()
	_, err := schemaflux.Parse[parsedPerson](ctx, parseUnreadable, schemaflux.ParseOptions{AllowLLMFallback: true})
	wantError(t, err, schemaflux.ErrCanceled, "parse")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the context's own error is not reachable: %v", err)
	}
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestParseOfInputTheGoParsersReadStillAddsARecordUnderAFloor(t *testing.T) {
	t.Parallel()
	// The row of the table holds a call that sends nothing: a floor above well
	// formed refuses it all the same.
	var recorder schemaflux.Recorder
	model := namedModel(parseAnswer)
	_, err := schemaflux.Parse[parsedPerson](operationClient(model, underFloor(schemaflux.ContractSchemaConstrained), recordingTo(&recorder)), `{"name":"Ada","age":36}`)
	wantError(t, err, schemaflux.ErrPolicy, "parse")
	if r := onlyRecord(t, &recorder); r.Op != "parse" || r.Requests != 0 {
		t.Errorf("record = %q requests %d", r.Op, r.Requests)
	}
}

func TestParseKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-d20b6e"
	type canaryType struct {
		Name string `json:"name" desc:"CANARY-d20b6e-tag"`
	}
	opts := schemaflux.ParseOptions{
		Options:          schemaflux.Options{Steering: "steer " + canary},
		AllowLLMFallback: true,
		AutoFix:          true,
		FormatHints:      []string{"hint " + canary},
		CustomDelimiters: []string{"delimiter " + canary},
	}
	input := "text " + canary
	call := func(o schemaflux.ParseOptions) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			_, err := schemaflux.Parse[canaryType](ctx, input, o)
			return err
		}
	}

	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(`{"name":"fine"}`) }, true, call(opts))
	})
	t.Run("an answer that is refused", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("the name is " + canary + " not json") }, true, call(opts))
	})
	t.Run("an answer of the wrong shape that quotes the input", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(`{"name":["` + canary + `"],"` + canary + `":1}`) }, true, call(opts))
	})
	t.Run("input no parser reads and no fallback", func(t *testing.T) {
		t.Parallel()
		local := opts
		local.AllowLLMFallback = false
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("{}") }, true, call(local))
	})
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, call(opts))
	})
	t.Run("a hint that names no field, over delimited input that holds the canary", func(t *testing.T) {
		t.Parallel()
		// The hint is the developer's own column names and the error names it;
		// the input is the data, and it is not in the error.
		bad := opts
		bad.FormatHints = []string{"nickname|years"}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("{}") }, true,
			func(ctx context.Context) error {
				_, err := schemaflux.Parse[canaryType](ctx, canary+"|"+canary, bad)
				return err
			})
	})
	t.Run("a refused option", func(t *testing.T) {
		t.Parallel()
		bad := opts
		bad.MaxOutputTokens = -1
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("{}") }, true, call(bad))
	})
	t.Run("Go parsers reading JSON that holds the canary", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("{}") }, true,
			func(ctx context.Context) error {
				_, err := schemaflux.Parse[canaryType](ctx, `{"name":["`+canary+`"]}`)
				return err
			})
	})
}

func TestParseResultCarriesTheJSONTagsOfTheInternalResult(t *testing.T) {
	t.Parallel()
	got, err := schemaflux.Parse[parsedPerson](operationClient(answeringWith("{}")), `{"name":"Ada","age":36}`)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := marshaledText(got)
	if err != nil || !strings.Contains(encoded, `"data":{`) || !strings.Contains(encoded, `"format":"json"`) {
		t.Errorf("encoded = %s, %v", encoded, err)
	}
}
