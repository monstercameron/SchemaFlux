package ops

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/requesttracking"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

func TestCommonOptions(t *testing.T) {
	tests := []struct {
		name    string
		options CommonOptions
		wantErr bool
	}{
		{
			name: "valid options",
			options: CommonOptions{
				Steering:     "test steering",
				Threshold:    0.5,
				Mode:         types.TransformMode,
				Intelligence: types.Fast,
			},
			wantErr: false,
		},
		{
			name: "invalid threshold too high",
			options: CommonOptions{
				Threshold: 1.5,
			},
			wantErr: true,
		},
		{
			name: "invalid threshold negative",
			options: CommonOptions{
				Threshold: -0.1,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.options.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("CommonOptions.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExtractOptionsStrictAndPartialTransitions(t *testing.T) {
	tests := []struct {
		name        string
		options     ExtractOptions
		wantStrict  bool
		wantPartial bool
		wantErr     bool
	}{
		{"constructor defaults", NewExtractOptions(), false, true, false},
		{"strict alone disables default partial", NewExtractOptions().WithStrictSchema(true), true, false, false},
		{"strict false restores default partial", NewExtractOptions().WithStrictSchema(true).WithStrictSchema(false), false, true, false},
		{"explicit partial false", NewExtractOptions().WithAllowPartial(false), false, false, false},
		{"explicit partial true", NewExtractOptions().WithAllowPartial(true), false, true, false},
		{"explicit partial true then strict conflicts", NewExtractOptions().WithAllowPartial(true).WithStrictSchema(true), true, true, true},
		{"strict then explicit partial true conflicts", NewExtractOptions().WithStrictSchema(true).WithAllowPartial(true), true, true, true},
		{"explicit partial false then strict", NewExtractOptions().WithAllowPartial(false).WithStrictSchema(true), true, false, false},
		{"strict then explicit partial false", NewExtractOptions().WithStrictSchema(true).WithAllowPartial(false), true, false, false},
		{"explicit partial true survives strict false", NewExtractOptions().WithAllowPartial(true).WithStrictSchema(false), false, true, false},
		{"literal conflict remains invalid", ExtractOptions{StrictSchema: true, AllowPartial: true}, true, true, true},
		{"zero value strict", ExtractOptions{}.WithStrictSchema(true), true, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.options.StrictSchema != tc.wantStrict || tc.options.AllowPartial != tc.wantPartial {
				t.Errorf("StrictSchema/AllowPartial = %v/%v, want %v/%v", tc.options.StrictSchema, tc.options.AllowPartial, tc.wantStrict, tc.wantPartial)
			}
			err := tc.options.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}

func TestExtractOptions(t *testing.T) {
	tests := []struct {
		name    string
		options ExtractOptions
		wantErr bool
	}{
		{
			name:    "default options",
			options: NewExtractOptions(),
			wantErr: false,
		},
		{
			name: "with schema hints",
			options: NewExtractOptions().
				WithSchemaHints(map[string]string{
					"date":   "ISO 8601",
					"amount": "USD",
				}),
			wantErr: false,
		},
		{
			name: "strict schema enabled",
			options: NewExtractOptions().
				WithStrictSchema(true).
				WithAllowPartial(false),
			wantErr: false,
		},
		{
			name: "conflicting options",
			options: NewExtractOptions().
				WithStrictSchema(true).
				WithAllowPartial(true),
			wantErr: true,
		},
		{
			name: "with examples",
			options: NewExtractOptions().
				WithExamples(
					struct{ Name string }{Name: "Example1"},
					struct{ Name string }{Name: "Example2"},
				),
			wantErr: false,
		},
		{
			name: "with field rules",
			options: NewExtractOptions().
				WithFieldRules(map[string]string{
					"email": "must be valid email",
					"age":   "must be between 0 and 150",
				}),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.options.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractOptions.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestTransformOptions(t *testing.T) {
	tests := []struct {
		name    string
		options TransformOptions
		wantErr bool
	}{
		{
			name:    "default options",
			options: NewTransformOptions(),
			wantErr: false,
		},
		{
			name: "valid merge strategies",
			options: NewTransformOptions().
				WithMergeStrategy("merge"),
			wantErr: false,
		},
		{
			name: "invalid merge strategy",
			options: TransformOptions{
				CommonOptions: CommonOptions{},
				MergeStrategy: "invalid",
			},
			wantErr: true,
		},
		{
			name: "with mapping rules",
			options: TransformOptions{
				CommonOptions: CommonOptions{},
				MappingRules: map[string]string{
					"fullName": "firstName + lastName",
					"age":      "calculateAge(birthDate)",
				},
				MergeStrategy: "replace",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.options.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("TransformOptions.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGenerateOptions(t *testing.T) {
	tests := []struct {
		name    string
		options GenerateOptions
		wantErr bool
	}{
		{
			name:    "default options",
			options: NewGenerateOptions(),
			wantErr: false,
		},
		{
			name: "with count",
			options: GenerateOptions{
				CommonOptions: CommonOptions{},
				Count:         10,
			},
			wantErr: false,
		},
		{
			name: "invalid count",
			options: GenerateOptions{
				CommonOptions: CommonOptions{},
				Count:         0,
			},
			wantErr: true,
		},
		{
			name: "with constraints",
			options: GenerateOptions{
				CommonOptions: CommonOptions{},
				Count:         1,
				Constraints: map[string]interface{}{
					"age":     "18-65",
					"country": []string{"US", "CA"},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.options.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("GenerateOptions.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSummarizeOptions(t *testing.T) {
	tests := []struct {
		name    string
		options SummarizeOptions
		wantErr bool
	}{
		{
			name:    "default options",
			options: NewSummarizeOptions(),
			wantErr: false,
		},
		{
			name: "valid length units",
			options: SummarizeOptions{
				CommonOptions:  CommonOptions{},
				LengthUnit:     "words",
				MaxCompression: 0.5,
			},
			wantErr: false,
		},
		{
			name: "invalid length unit",
			options: SummarizeOptions{
				CommonOptions:  CommonOptions{},
				LengthUnit:     "invalid",
				MaxCompression: 0.5,
			},
			wantErr: true,
		},
		{
			name: "invalid compression ratio",
			options: SummarizeOptions{
				CommonOptions:  CommonOptions{},
				MaxCompression: 1.5,
			},
			wantErr: true,
		},
		{
			name: "with bullet points and focus areas",
			options: SummarizeOptions{
				CommonOptions:  CommonOptions{},
				BulletPoints:   true,
				FocusAreas:     []string{"key findings", "recommendations"},
				MaxCompression: 0.2,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.options.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("SummarizeOptions.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRewriteOptions(t *testing.T) {
	tests := []struct {
		name    string
		options RewriteOptions
		wantErr bool
	}{
		{
			name:    "default options",
			options: NewRewriteOptions(),
			wantErr: false,
		},
		{
			name: "valid formality level",
			options: RewriteOptions{
				CommonOptions:  CommonOptions{},
				FormalityLevel: 8,
				PreserveFacts:  true,
			},
			wantErr: false,
		},
		{
			name: "invalid formality level too high",
			options: RewriteOptions{
				CommonOptions:  CommonOptions{},
				FormalityLevel: 11,
				PreserveFacts:  true,
			},
			wantErr: true,
		},
		{
			name: "invalid formality level too low",
			options: RewriteOptions{
				CommonOptions:  CommonOptions{},
				FormalityLevel: 0,
				PreserveFacts:  true,
			},
			wantErr: true,
		},
		{
			name: "with tone and audience",
			options: RewriteOptions{
				CommonOptions:  CommonOptions{},
				TargetTone:     "professional",
				Audience:       "executives",
				FormalityLevel: 8,
				PreserveFacts:  true,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.options.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("RewriteOptions.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestTranslateOptions(t *testing.T) {
	tests := []struct {
		name    string
		options TranslateOptions
		wantErr bool
	}{
		{
			name:    "default without target language",
			options: NewTranslateOptions(),
			wantErr: true,
		},
		{
			name: "with target language",
			options: TranslateOptions{
				CommonOptions:      CommonOptions{},
				TargetLanguage:     "Spanish",
				PreserveFormatting: true,
				CulturalAdaptation: 5,
				Formality:          "neutral",
			},
			wantErr: false,
		},
		{
			name: "invalid cultural adaptation",
			options: TranslateOptions{
				CommonOptions:      CommonOptions{},
				TargetLanguage:     "French",
				CulturalAdaptation: 11,
			},
			wantErr: true,
		},
		{
			name: "with glossary",
			options: TranslateOptions{
				CommonOptions:      CommonOptions{},
				TargetLanguage:     "Japanese",
				CulturalAdaptation: 8,
				Glossary: map[string]string{
					"CEO": "代表取締役",
					"AI":  "人工知能",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.options.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("TranslateOptions.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestClassifyOptions(t *testing.T) {
	tests := []struct {
		name    string
		options ClassifyOptions
		wantErr bool
	}{
		{
			name:    "default without categories",
			options: NewClassifyOptions(),
			wantErr: true,
		},
		{
			name: "with categories",
			options: ClassifyOptions{
				CommonOptions:     CommonOptions{},
				Categories:        []string{"positive", "negative", "neutral"},
				MinConfidence:     0.7,
				IncludeConfidence: true,
			},
			wantErr: false,
		},
		{
			name: "invalid min confidence",
			options: ClassifyOptions{
				CommonOptions: CommonOptions{},
				Categories:    []string{"cat1"},
				MinConfidence: 1.5,
			},
			wantErr: true,
		},
		{
			name: "multi-label with max categories",
			options: ClassifyOptions{
				CommonOptions:     CommonOptions{},
				Categories:        []string{"tech", "business", "sports", "entertainment"},
				MultiLabel:        true,
				MaxCategories:     2,
				MinConfidence:     0.6,
				IncludeConfidence: true,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.options.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("ClassifyOptions.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBatchOptions(t *testing.T) {
	tests := []struct {
		name    string
		options BatchOptions
		wantErr bool
	}{
		{
			name: "valid options",
			options: BatchOptions{
				CommonOptions: CommonOptions{},
				Mode:          "parallel",
				Concurrency:   10,
				BatchSize:     50,
				ErrorStrategy: "continue",
			},
			wantErr: false,
		},
		{
			name: "invalid mode",
			options: BatchOptions{
				CommonOptions: CommonOptions{},
				Mode:          "invalid",
				Concurrency:   1,
				BatchSize:     1,
			},
			wantErr: true,
		},
		{
			name: "invalid concurrency",
			options: BatchOptions{
				CommonOptions: CommonOptions{},
				Mode:          "parallel",
				Concurrency:   0,
				BatchSize:     1,
			},
			wantErr: true,
		},
		{
			name: "invalid batch size",
			options: BatchOptions{
				CommonOptions: CommonOptions{},
				Mode:          "merged",
				Concurrency:   1,
				BatchSize:     0,
			},
			wantErr: true,
		},
		{
			name: "invalid error strategy",
			options: BatchOptions{
				CommonOptions: CommonOptions{},
				Mode:          "parallel",
				Concurrency:   10,
				BatchSize:     50,
				ErrorStrategy: "invalid",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.options.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("BatchOptions.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBuilderPattern(t *testing.T) {
	// Test fluent builder pattern
	opts := NewExtractOptions().
		WithSteering("focus on names").
		WithThreshold(0.8).
		WithMode(types.Strict).
		WithIntelligence(types.Smart).
		WithStrictSchema(true).
		WithSchemaHints(map[string]string{
			"date": "ISO format",
		})

	if opts.CommonOptions.Steering != "focus on names" {
		t.Errorf("Expected steering 'focus on names', got '%s'", opts.CommonOptions.Steering)
	}
	if opts.CommonOptions.Threshold != 0.8 {
		t.Errorf("Expected threshold 0.8, got %f", opts.CommonOptions.Threshold)
	}
	if opts.CommonOptions.Mode != types.Strict {
		t.Errorf("Expected mode Strict, got %v", opts.CommonOptions.Mode)
	}
	if opts.CommonOptions.Intelligence != types.Smart {
		t.Errorf("Expected intelligence Smart, got %v", opts.CommonOptions.Intelligence)
	}
	if !opts.StrictSchema {
		t.Error("Expected StrictSchema to be true")
	}
	if len(opts.SchemaHints) != 1 {
		t.Errorf("Expected 1 schema hint, got %d", len(opts.SchemaHints))
	}
}

func TestBackwardCompatibility(t *testing.T) {
	// Test conversion from OpOptions
	legacyOpts := types.OpOptions{
		Steering:     "test steering",
		Threshold:    0.75,
		Mode:         types.Creative,
		Intelligence: types.Quick,
		Context:      context.Background(),
		RequestID:    "test-123",
	}

	// Test conversion for different operation types
	operationTypes := []string{
		"extract", "transform", "generate", "summarize", "rewrite",
		"translate", "expand", "classify", "score", "compare",
		"choose", "filter", "sort", "batch",
	}

	for _, opType := range operationTypes {
		t.Run(opType, func(t *testing.T) {
			converted := ConvertOpOptions(legacyOpts, opType)

			if converted.GetSteering() != legacyOpts.Steering {
				t.Errorf("Steering mismatch: got %s, want %s", converted.GetSteering(), legacyOpts.Steering)
			}
			if converted.GetThreshold() != legacyOpts.Threshold {
				t.Errorf("Threshold mismatch: got %f, want %f", converted.GetThreshold(), legacyOpts.Threshold)
			}
			if converted.GetMode() != legacyOpts.Mode {
				t.Errorf("Mode mismatch: got %v, want %v", converted.GetMode(), legacyOpts.Mode)
			}
			if converted.GetIntelligence() != legacyOpts.Intelligence {
				t.Errorf("Intelligence mismatch: got %v, want %v", converted.GetIntelligence(), legacyOpts.Intelligence)
			}
		})
	}
}

func TestOptionsToOpOptions(t *testing.T) {
	requesttracking.Configure(requesttracking.DefaultConfig())

	// Test that specialized options can convert back to OpOptions
	extractOpts := NewExtractOptions().
		WithSteering("test").
		WithThreshold(0.9).
		WithMode(types.Strict).
		WithIntelligence(types.Smart)

	opOpts := extractOpts.toOpOptions()

	if opOpts.Steering != "test" {
		t.Errorf("Expected steering 'test', got '%s'", opOpts.Steering)
	}
	if opOpts.Threshold != 0.9 {
		t.Errorf("Expected threshold 0.9, got %f", opOpts.Threshold)
	}
	if opOpts.Mode != types.Strict {
		t.Errorf("Expected mode Strict, got %v", opOpts.Mode)
	}
	if opOpts.Intelligence != types.Smart {
		t.Errorf("Expected intelligence Smart, got %v", opOpts.Intelligence)
	}
	if opOpts.RequestID == "" {
		t.Error("expected generated request ID to be set")
	}
}

// A threshold that is not a number passes every range comparison, because
// every comparison with NaN is false, so it was read as a valid one.
func TestCommonOptionsValidateRefusesAThresholdThatIsNotANumber(t *testing.T) {
	cases := []struct {
		name      string
		threshold float64
		refused   bool
	}{
		{"zero", 0, false},
		{"negative zero", math.Copysign(0, -1), false},
		{"a half", 0.5, false},
		{"one", 1, false},
		{"the smallest positive number", math.SmallestNonzeroFloat64, false},
		{"just under one", math.Nextafter(1, 0), false},
		{"just over one", math.Nextafter(1, 2), true},
		{"just under zero", -math.SmallestNonzeroFloat64, true},
		{"two", 2, true},
		{"NaN", math.NaN(), true},
		{"positive infinity", math.Inf(1), true},
		{"negative infinity", math.Inf(-1), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CommonOptions{Threshold: tc.threshold}.Validate()
			if (err != nil) != tc.refused {
				t.Fatalf("Validate() = %v for a threshold of %v, want an error: %v", err, tc.threshold, tc.refused)
			}
		})
	}

	t.Run("an options type that embeds CommonOptions refuses it too", func(t *testing.T) {
		if err := NewExtractOptions().WithThreshold(math.NaN()).Validate(); err == nil {
			t.Error("ExtractOptions accepted a NaN threshold")
		}
	})
}

// A length unit the operation cannot measure in is refused with the rest of the
// options, before the model is called and paid for. Every entry point that takes
// a SummarizeOptions is held to it.
func TestSummarizeRefusesAnUnknownLengthUnitBeforeAnyCall(t *testing.T) {
	entryPoints := map[string]func(SummarizeOptions) error{
		"Summarize": func(opts SummarizeOptions) error {
			_, err := Summarize("some text to summarize", opts)
			return err
		},
		"SummarizeWithMetadata": func(opts SummarizeOptions) error {
			_, err := SummarizeWithMetadata("some text to summarize", opts)
			return err
		},
		"SummarizeResult": func(opts SummarizeOptions) error {
			_, err := SummarizeResult("some text to summarize", opts)
			return err
		},
	}
	units := []struct {
		unit    string
		refused bool
	}{
		{"words", false},
		{"sentences", false},
		{"paragraphs", false},
		{"", false},
		{"wrods", true},
		{"word", true},
		{"sentence", true},
		{"Words", true},
		{" words", true},
		{"lines", true},
		{"bytes", true},
		{"not-a-real-unit", true},
	}
	for name, run := range entryPoints {
		for _, tc := range units {
			t.Run(name+" "+tc.unit, func(t *testing.T) {
				calls := countingCaller(t, `{"text":"Short.","key_points":["a"],"confidence":0.9}`, nil)
				opts := NewSummarizeOptions()
				opts.TargetLength, opts.LengthUnit = 3, tc.unit

				err := run(opts)
				if tc.refused {
					if err == nil {
						t.Fatalf("a length unit of %q was accepted", tc.unit)
					}
					if *calls != 0 {
						t.Errorf("the model was called %d times for a unit that is refused", *calls)
					}
					return
				}
				if err != nil && *calls == 0 {
					t.Errorf("a length unit of %q was refused before any call: %v", tc.unit, err)
				}
			})
		}
	}
}

// Reading an options value, as Validate does to find a locked limit on its
// context, makes no request ID, applies no Timeout and wraps nothing. It used
// to merge in full and throw the result away: an ID from the system's random
// source, two context values, a timer when a Timeout was set.
func TestReadingOptionsMakesNoRequestID(t *testing.T) {
	t.Cleanup(func() { requesttracking.Configure(requesttracking.DefaultConfig()) })
	requesttracking.Configure(requesttracking.Config{
		Enabled:               true,
		RequestIDStrategy:     requesttracking.IDStrategyTrace,
		CorrelationIDStrategy: requesttracking.CorrelationStrategyGenerate,
	})
	var asked int
	t.Cleanup(requesttracking.SetTraceIDSource(func(context.Context) string { asked++; return "trace-id" }))

	extract := NewExtractOptions()
	extract.CommonOptions.Timeout = 30e9
	cases := []struct {
		name    string
		options BaseOptions
	}{
		{"an options type with both halves", extract},
		{"a CommonOptions", extract.CommonOptions},
		{"a ChooseOptions", NewChooseOptions()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			asked = 0
			if err := tc.options.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			merged, callers := optionsForReading(tc.options)
			if asked != 0 {
				t.Errorf("reading the options asked for %d request IDs, want none", asked)
			}
			if merged.RequestID != "" || merged.CorrelationID != "" {
				t.Errorf("reading made the IDs %q and %q for a caller who set none", merged.RequestID, merged.CorrelationID)
			}
			if _, bounded := merged.Context.Deadline(); bounded {
				t.Error("reading the options applied the Timeout to the context")
			}
			if _, bounded := callers.Deadline(); bounded {
				t.Error("the context the caller supplied carries a deadline the caller did not give it")
			}
		})
	}
}

func TestReadingOptionsKeepsTheCallersIDsAndContext(t *testing.T) {
	type contextKey struct{}
	base := context.WithValue(context.Background(), contextKey{}, "kept")
	extract := NewExtractOptions()
	extract.CommonOptions.Context = base
	extract.CommonOptions.RequestID = "req-mine"
	extract.CommonOptions.CorrelationID = "corr-mine"
	embedded := NewExtractOptions()
	embedded.OpOptions.Context = base
	embedded.OpOptions.RequestID = "req-embedded"

	for name, options := range map[string]ExtractOptions{"common half": extract, "embedded half": embedded} {
		t.Run(name, func(t *testing.T) {
			merged, callers := optionsForReading(options)
			if callers != base {
				t.Error("the caller's context was not returned as it was")
			}
			if merged.Context != base {
				t.Error("the merged context is not the caller's")
			}
			if merged.Context.Value(contextKey{}) != "kept" {
				t.Error("the caller's context value is gone")
			}
		})
	}
	merged, _ := optionsForReading(extract)
	if merged.RequestID != "req-mine" || merged.CorrelationID != "corr-mine" {
		t.Errorf("IDs = %q and %q, want the ones the caller set", merged.RequestID, merged.CorrelationID)
	}
	merged, _ = optionsForReading(embedded)
	if merged.RequestID != "req-embedded" {
		t.Errorf("RequestID = %q, want the embedded half's", merged.RequestID)
	}
}

// A call still gets its IDs from the conversion it runs under.
func TestConvertingOptionsStillMakesTheIDs(t *testing.T) {
	t.Cleanup(func() { requesttracking.Configure(requesttracking.DefaultConfig()) })
	requesttracking.Configure(requesttracking.Config{
		Enabled:               true,
		RequestIDStrategy:     requesttracking.IDStrategyAuto,
		CorrelationIDStrategy: requesttracking.CorrelationStrategyInherit,
	})
	opts := NewExtractOptions()
	converted := opts.toOpOptions()
	if converted.RequestID == "" || converted.CorrelationID == "" {
		t.Fatalf("toOpOptions made no IDs: %q, %q", converted.RequestID, converted.CorrelationID)
	}
	if got := requesttracking.FromContext(converted.Context); got.RequestID != converted.RequestID {
		t.Errorf("the converted context carries %q, the options %q", got.RequestID, converted.RequestID)
	}
	common := opts.CommonOptions.toOpOptions()
	if common.RequestID == "" {
		t.Error("CommonOptions.toOpOptions made no request ID")
	}
	if common.Mode != opts.CommonOptions.Mode || common.Intelligence != opts.CommonOptions.Intelligence {
		t.Errorf("the CommonOptions conversion lost a field: %+v", common)
	}
}

// What reading and converting an options value cost, so that a change that makes
// either dearer fails here. Measured with no Timeout: reading is a copy of the
// struct and one boxing of the options.
func TestOptionsResolutionAllocationsStayWithinBudget(t *testing.T) {
	opts := NewExtractOptions()
	cases := []struct {
		name   string
		run    func()
		budget float64
	}{
		{"Validate", func() { _ = opts.Validate() }, 2},
		{"optionsForReading", func() { _, _ = optionsForReading(opts) }, 2},
		{"mergeOptionHalves without identity", func() { _ = mergeOptionHalves(opts.CommonOptions, opts.OpOptions, false) }, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := testing.AllocsPerRun(200, tc.run); got > tc.budget {
				t.Errorf("%v allocations, budget %v", got, tc.budget)
			}
		})
	}
}

// A call merges its options once. Extract used to check them by merging them
// (Validate) and then merge them again to work with them, and ExtractResult
// resolved them once to decide what it would promise and had Extract resolve
// them again. The checks have to give the verdicts they gave when they merged
// for themselves, in the same order, and a refused call has to ask nothing.
func extractOptionCases() []struct {
	name string
	opts func() ExtractOptions
} {
	locked := func(limits LockedLimits) context.Context {
		return WithLockedLimits(context.Background(), limits)
	}
	return []struct {
		name string
		opts func() ExtractOptions
	}{
		{"defaults", NewExtractOptions},
		{"a threshold of NaN", func() ExtractOptions { o := NewExtractOptions(); o.CommonOptions.Threshold = math.NaN(); return o }},
		{"a negative threshold", func() ExtractOptions { o := NewExtractOptions(); o.CommonOptions.Threshold = -0.1; return o }},
		{"a threshold above one", func() ExtractOptions { o := NewExtractOptions(); o.CommonOptions.Threshold = 1.5; return o }},
		{"a threshold of one", func() ExtractOptions { o := NewExtractOptions(); o.CommonOptions.Threshold = 1; return o }},
		{"strict schema with partial answers", func() ExtractOptions {
			return NewExtractOptions().WithStrictSchema(true).WithAllowPartial(true)
		}},
		{"strict schema alone", func() ExtractOptions {
			return NewExtractOptions().WithStrictSchema(true).WithAllowPartial(false)
		}},
		{"more output than the lock allows, set on the embedded half", func() ExtractOptions {
			o := NewExtractOptions()
			o.CommonOptions.Context = locked(LockedLimits{MaxOutputTokens: 100})
			o.OpOptions.MaxOutputTokens = 5000
			return o
		}},
		{"output within the lock", func() ExtractOptions {
			o := NewExtractOptions()
			o.CommonOptions.Context = locked(LockedLimits{MaxOutputTokens: 100})
			o.OpOptions.MaxOutputTokens = 50
			return o
		}},
		{"the lock carried by the embedded half's context", func() ExtractOptions {
			o := NewExtractOptions()
			o.OpOptions.Context = locked(LockedLimits{MaxOutputTokens: 100})
			o.OpOptions.MaxOutputTokens = 5000
			return o
		}},
		{"a mode weaker than the floor", func() ExtractOptions {
			o := NewExtractOptions().WithMode(types.Creative)
			o.CommonOptions.Context = locked(LockedLimits{MinMode: types.Strict})
			return o
		}},
		{"a mode that meets the floor", func() ExtractOptions {
			o := NewExtractOptions().WithMode(types.Strict)
			o.CommonOptions.Context = locked(LockedLimits{MinMode: types.Strict})
			return o
		}},
		{"a lock and a timeout", func() ExtractOptions {
			o := NewExtractOptions()
			o.Timeout = time.Minute
			o.CommonOptions.Context = locked(LockedLimits{MaxOutputTokens: 100})
			o.OpOptions.MaxOutputTokens = 5000
			return o
		}},
	}
}

func TestValidateResolvedGivesTheVerdictsOfValidate(t *testing.T) {
	for _, tc := range extractOptionCases() {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts()
			want := opts.Validate()
			got := opts.validateResolved(opts.toOpOptions())
			if (want == nil) != (got == nil) || (want != nil && want.Error() != got.Error()) {
				t.Errorf("validateResolved = %v, Validate = %v", got, want)
			}
		})
	}
}

func TestExtractAndExtractResultRefuseWhatValidateRefusesBeforeAskingTheModel(t *testing.T) {
	for _, tc := range extractOptionCases() {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts()
			refusal := opts.Validate()
			calls := answerAndRecord(t, `{"order_id":"A-2207","product":"Team plan","amount":"$49"}`)

			_, extractErr := Extract[benchFlat]("order A-2207, Team plan, $49", opts)
			_, resultErr := ExtractResult[benchFlat]("order A-2207, Team plan, $49", opts)

			if refusal == nil {
				if extractErr != nil || resultErr != nil {
					t.Fatalf("valid options refused: Extract %v, ExtractResult %v", extractErr, resultErr)
				}
				if len(*calls) != 2 {
					t.Errorf("%d model calls for two valid calls", len(*calls))
				}
				return
			}
			want := "invalid options: " + refusal.Error()
			if extractErr == nil || extractErr.Error() != want {
				t.Errorf("Extract = %v, want %q", extractErr, want)
			}
			if resultErr == nil || !strings.Contains(resultErr.Error(), want) {
				t.Errorf("ExtractResult = %v, want it to carry %q", resultErr, want)
			}
			if len(*calls) != 0 {
				t.Errorf("the model was asked %d times about options that were refused", len(*calls))
			}
		})
	}
}
