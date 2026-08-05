package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/internal/types"
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
			name:    "default options",
			options: NewBatchOptions(),
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

func TestIsLegacyOption(t *testing.T) {
	legacyOpt := types.OpOptions{}
	newOpt := NewExtractOptions()

	if !IsLegacyOption(legacyOpt) {
		t.Error("Expected OpOptions to be identified as legacy")
	}

	if IsLegacyOption(newOpt) {
		t.Error("Expected ExtractOptions not to be identified as legacy")
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
