package fluent

import (
	"context"
	"testing"
)

// setters_test.go proved Model, Steer, RequestID, CorrelationID, Context, and
// the tier setters apply on every core builder. It did not touch each
// builder's own field-specific methods (ExtractRequest.Partial,
// GenerateRequest.Count, FilterRequest.KeepMatching, and so on), nor
// WithOptions/Configure, nor the Mode setters (Strict/Creative/ExactFields/
// CompleteFields) on their own -- those are what this file adds, following
// the same build/set/read-back discipline readField and requireSet enforce.

func TestExtractRequest_FieldSpecificSettersApply(t *testing.T) {
	opts := Extracting[map[string]any]("x").
		Partial(true).
		SchemaHints(map[string]string{"age": "an integer"}).
		opts
	requireSet(t, "Extracting", "AllowPartial", opts, true)
	requireSet(t, "Extracting", "SchemaHints", opts, map[string]string{"age": "an integer"})
}

func TestExtractRequest_WithOptionsReplacesOpts(t *testing.T) {
	custom := NewExtractOptions().WithThreshold(0.9)
	built := Extracting[map[string]any]("x").WithOptions(custom)
	if built.opts.CommonOptions.Threshold != 0.9 {
		t.Errorf("WithOptions did not replace opts: Threshold = %v, want 0.9", built.opts.CommonOptions.Threshold)
	}
}

func TestExtractRequest_ConfigureAppliesFn(t *testing.T) {
	built := Extracting[map[string]any]("x").Configure(func(o ExtractOptions) ExtractOptions {
		o.CommonOptions.ExactFields = true
		return o
	})
	if !built.opts.CommonOptions.ExactFields {
		t.Error("Configure's function was not applied")
	}
}

func TestExtractRequest_ModeSettersApply(t *testing.T) {
	if got := Extracting[map[string]any]("x").Strict().opts.CommonOptions.Mode; got != Strict {
		t.Errorf("Strict() = %v, want Strict", got)
	}
}

func TestTransformRequest_WithOptionsAndConfigureApply(t *testing.T) {
	custom := NewTransformOptions().WithMergeStrategy("preset")
	if got := Transforming[string, string]("x").WithOptions(custom).opts.MergeStrategy; got != "preset" {
		t.Errorf("WithOptions did not carry through: MergeStrategy = %q, want %q", got, "preset")
	}
	if got := Transforming[string, string]("x").Configure(func(o TransformOptions) TransformOptions {
		o.MergeStrategy = "configured"
		return o
	}).opts.MergeStrategy; got != "configured" {
		t.Errorf("Configure did not apply: MergeStrategy = %q, want %q", got, "configured")
	}
}

func TestFilterRequest_ConfigureApplies(t *testing.T) {
	if got := Filtering([]string{"a"}).Configure(func(o FilterOptions) FilterOptions {
		o.Criteria = "configured"
		return o
	}).opts.Criteria; got != "configured" {
		t.Errorf("Configure did not apply: Criteria = %q, want %q", got, "configured")
	}
}

func TestTransformRequest_FieldSpecificSettersApply(t *testing.T) {
	built := Transforming[string, string]("x").Merge("append")
	if built.opts.MergeStrategy != "append" {
		t.Errorf("Merge() = %q, want %q", built.opts.MergeStrategy, "append")
	}
}

func TestTransformRequest_ModeAndTierSettersApply(t *testing.T) {
	cases := []struct {
		name string
		mode Mode
		fn   func(TransformRequest[string, string]) TransformRequest[string, string]
	}{
		{"Strict", Strict, func(r TransformRequest[string, string]) TransformRequest[string, string] { return r.Strict() }},
		{"Creative", Creative, func(r TransformRequest[string, string]) TransformRequest[string, string] { return r.Creative() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.fn(Transforming[string, string]("x")).opts.CommonOptions.Mode; got != tc.mode {
				t.Errorf("Mode = %v, want %v", got, tc.mode)
			}
		})
	}

	tierCases := []struct {
		name string
		tier Speed
		fn   func(TransformRequest[string, string]) TransformRequest[string, string]
	}{
		{"Smart", Smart, func(r TransformRequest[string, string]) TransformRequest[string, string] { return r.Smart() }},
		{"Fast", Fast, func(r TransformRequest[string, string]) TransformRequest[string, string] { return r.Fast() }},
		{"Quick", Quick, func(r TransformRequest[string, string]) TransformRequest[string, string] { return r.Quick() }},
	}
	for _, tc := range tierCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.fn(Transforming[string, string]("x")).opts.CommonOptions.Intelligence; got != tc.tier {
				t.Errorf("Intelligence = %v, want %v", got, tc.tier)
			}
		})
	}
}

func TestTransformRequest_ExactAndCompleteFieldsApply(t *testing.T) {
	built := Transforming[string, string]("x").ExactFields().CompleteFields()
	if !built.opts.CommonOptions.ExactFields || !built.opts.CommonOptions.CompleteFields {
		t.Errorf("ExactFields/CompleteFields did not both apply: %+v", built.opts)
	}
}

func TestGenerateRequest_FieldSpecificSettersApply(t *testing.T) {
	built := Generating[string]("prompt").Count(3).Style("terse")
	if built.opts.Count != 3 {
		t.Errorf("Count() = %d, want 3", built.opts.Count)
	}
	if built.opts.Style != "terse" {
		t.Errorf("Style() = %q, want %q", built.opts.Style, "terse")
	}
}

func TestGenerateRequest_ModeAndTierSettersApply(t *testing.T) {
	built := Generating[string]("prompt").Strict().ExactFields().CompleteFields().Creative().Fast()
	if built.opts.CommonOptions.Mode != Creative {
		t.Errorf("final Mode = %v, want Creative (last call wins)", built.opts.CommonOptions.Mode)
	}
	if built.opts.CommonOptions.Intelligence != Fast {
		t.Errorf("Intelligence = %v, want Fast", built.opts.CommonOptions.Intelligence)
	}
	smart := Generating[string]("prompt").Smart()
	if smart.opts.CommonOptions.Intelligence != Smart {
		t.Errorf("Smart() = %v, want Smart", smart.opts.CommonOptions.Intelligence)
	}
	quick := Generating[string]("prompt").Quick()
	if quick.opts.CommonOptions.Intelligence != Quick {
		t.Errorf("Quick() = %v, want Quick", quick.opts.CommonOptions.Intelligence)
	}
}

func TestGenerateRequest_WithOptionsAndConfigureApply(t *testing.T) {
	custom := NewGenerateOptions().WithStyle("custom")
	if got := Generating[string]("p").WithOptions(custom).opts.Style; got != "custom" {
		t.Errorf("WithOptions did not carry through: Style = %q", got)
	}
	if got := Generating[string]("p").Configure(func(o GenerateOptions) GenerateOptions {
		o.Count = 7
		return o
	}).opts.Count; got != 7 {
		t.Errorf("Configure did not apply: Count = %d, want 7", got)
	}
}

func TestGenerateRequest_RunResultReturnsEnvelope(t *testing.T) {
	restore := installStubProvider(t, &slowStubProvider{body: `{"name":"x"}`})
	defer restore()

	result, err := Generating[stubExtractTarget]("prompt").RunResult(context.Background())
	if err != nil {
		t.Fatalf("RunResult failed: %v", err)
	}
	if result.Meta.Operation != "generate" {
		t.Errorf("Meta.Operation = %q, want %q", result.Meta.Operation, "generate")
	}
}

func TestChooseRequest_FieldSpecificSettersApply(t *testing.T) {
	built := Choosing([]string{"a", "b"}).By("best").Top(2).Reasoning(false)
	if built.opts.TopN != 2 {
		t.Errorf("Top() = %d, want 2", built.opts.TopN)
	}
	if built.opts.RequireReasoning {
		t.Error("Reasoning(false) did not apply")
	}
}

func TestChooseRequest_ModeAndTierAndWithOptionsApply(t *testing.T) {
	if got := Choosing([]string{"a"}).Fast().opts.CommonOptions.Intelligence; got != Fast {
		t.Errorf("Fast() = %v, want Fast", got)
	}
	custom := NewChooseOptions().WithTopN(5)
	if got := Choosing([]string{"a"}).WithOptions(custom).opts.TopN; got != 5 {
		t.Errorf("WithOptions did not carry through: TopN = %d, want 5", got)
	}
}

func TestFilterRequest_FieldSpecificSettersApply(t *testing.T) {
	built := Filtering([]string{"a", "b"}).By("good").KeepMatching(false).MinConfidence(0.8)
	if built.opts.KeepMatching {
		t.Error("KeepMatching(false) did not apply")
	}
	if built.opts.MinConfidence != 0.8 {
		t.Errorf("MinConfidence() = %v, want 0.8", built.opts.MinConfidence)
	}
}

func TestFilterRequest_ModeAndTierAndWithOptionsApply(t *testing.T) {
	if got := Filtering([]string{"a"}).By("x").Smart().opts.CommonOptions.Intelligence; got != Smart {
		t.Errorf("Smart() = %v, want Smart", got)
	}
	if got := Filtering([]string{"a"}).By("x").Fast().opts.CommonOptions.Intelligence; got != Fast {
		t.Errorf("Fast() = %v, want Fast", got)
	}
	if got := Filtering([]string{"a"}).By("x").Quick().opts.CommonOptions.Intelligence; got != Quick {
		t.Errorf("Quick() = %v, want Quick", got)
	}
	custom := NewFilterOptions().WithCriteria("preset")
	if got := Filtering([]string{"a"}).WithOptions(custom).opts.Criteria; got != "preset" {
		t.Errorf("WithOptions did not carry through: Criteria = %q, want %q", got, "preset")
	}
}

func TestSortRequest_FieldSpecificSettersApply(t *testing.T) {
	asc := Sorting([]string{"a", "b"}).By("size").Asc()
	if asc.opts.Direction != "ascending" {
		t.Errorf("Asc() = %q, want %q", asc.opts.Direction, "ascending")
	}
	desc := Sorting([]string{"a", "b"}).By("size").Desc()
	if desc.opts.Direction != "descending" {
		t.Errorf("Desc() = %q, want %q", desc.opts.Direction, "descending")
	}
}

func TestSortRequest_ModeAndTierAndWithOptionsApply(t *testing.T) {
	if got := Sorting([]string{"a"}).By("x").Smart().opts.CommonOptions.Intelligence; got != Smart {
		t.Errorf("Smart() = %v, want Smart", got)
	}
	if got := Sorting([]string{"a"}).By("x").Fast().opts.CommonOptions.Intelligence; got != Fast {
		t.Errorf("Fast() = %v, want Fast", got)
	}
	if got := Sorting([]string{"a"}).By("x").Quick().opts.CommonOptions.Intelligence; got != Quick {
		t.Errorf("Quick() = %v, want Quick", got)
	}
	custom := NewSortOptions().WithCriteria("preset")
	if got := Sorting([]string{"a"}).WithOptions(custom).opts.Criteria; got != "preset" {
		t.Errorf("WithOptions did not carry through: Criteria = %q, want %q", got, "preset")
	}
}
