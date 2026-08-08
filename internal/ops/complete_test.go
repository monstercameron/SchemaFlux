package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

func TestComplete_Basic(t *testing.T) {
	// This test would require LLM, so we'll test the option setup and validation
	opts := NewCompleteOptions()
	if err := opts.Validate(); err != nil {
		t.Errorf("Default options validation failed: %v", err)
	}

	if opts.MaxLength != 100 {
		t.Errorf("Expected default MaxLength 100, got %d", opts.MaxLength)
	}
	if opts.Temperature != 0.7 {
		t.Errorf("Expected default Temperature 0.7, got %f", opts.Temperature)
	}
}

func TestComplete_Options(t *testing.T) {
	opts := NewCompleteOptions().
		WithBackground([]string{"Hello", "How are you?"}).
		WithMaxLength(200).
		WithStopSequences([]string{".", "!"}).
		WithTemperature(0.5).
		WithTopP(0.8).
		WithTopK(40)

	if len(opts.Background) != 2 {
		t.Errorf("Expected 2 context messages, got %d", len(opts.Background))
	}
	if opts.MaxLength != 200 {
		t.Errorf("Expected MaxLength 200, got %d", opts.MaxLength)
	}
	if len(opts.StopSequences) != 2 {
		t.Errorf("Expected 2 stop sequences, got %d", len(opts.StopSequences))
	}
	if opts.Temperature != 0.5 {
		t.Errorf("Expected Temperature 0.5, got %f", opts.Temperature)
	}
	if opts.TopP != 0.8 {
		t.Errorf("Expected TopP 0.8, got %f", opts.TopP)
	}
	if opts.TopK != 40 {
		t.Errorf("Expected TopK 40, got %d", opts.TopK)
	}
}

func TestComplete_OptionsValidation(t *testing.T) {
	tests := []struct {
		name    string
		opts    CompleteOptions
		wantErr bool
	}{
		{
			name:    "valid defaults",
			opts:    NewCompleteOptions(),
			wantErr: false,
		},
		{
			name:    "negative max length",
			opts:    NewCompleteOptions().WithMaxLength(-1),
			wantErr: true,
		},
		{
			name:    "zero max length",
			opts:    NewCompleteOptions().WithMaxLength(0),
			wantErr: true,
		},
		{
			name:    "negative temperature",
			opts:    NewCompleteOptions().WithTemperature(-0.1),
			wantErr: true,
		},
		{
			name:    "temperature too high",
			opts:    NewCompleteOptions().WithTemperature(2.1),
			wantErr: true,
		},
		{
			name:    "zero topP",
			opts:    NewCompleteOptions().WithTopP(0),
			wantErr: true,
		},
		{
			name:    "topP too high",
			opts:    NewCompleteOptions().WithTopP(1.1),
			wantErr: true,
		},
		{
			name:    "negative topK",
			opts:    NewCompleteOptions().WithTopK(-1),
			wantErr: true,
		},
		{
			name:    "zero topK",
			opts:    NewCompleteOptions().WithTopK(0),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestComplete_EmptyInput(t *testing.T) {
	opts := NewCompleteOptions()
	ctx := context.Background()
	_, err := Complete(ctx, "", opts)
	if err == nil {
		t.Error("Expected error for empty input")
	}
}

func TestComplete_WhitespaceOnlyInput(t *testing.T) {
	opts := NewCompleteOptions()
	ctx := context.Background()
	_, err := Complete(ctx, "   ", opts)
	if err == nil {
		t.Error("Expected error for whitespace-only input")
	}
}

func TestProcessCompletionResponse(t *testing.T) {
	tests := []struct {
		name     string
		response string
		original string
		opts     CompleteOptions
		expected string
	}{
		{
			name:     "basic completion",
			response: "is beautiful today.",
			original: "The weather",
			opts:     NewCompleteOptions().WithMaxLength(100),
			expected: "The weather is beautiful today.",
		},
		{
			name:     "response repeats original",
			response: "The weather is sunny.",
			original: "The weather",
			opts:     NewCompleteOptions().WithMaxLength(100),
			expected: "The weather is sunny.",
		},
		{
			name:     "stop sequence",
			response: "is great. Have a nice day!",
			original: "The weather",
			opts:     NewCompleteOptions().WithStopSequences([]string{"."}).WithMaxLength(100),
			expected: "The weather is great",
		},
		{
			name:     "max length limit",
			response: "is absolutely beautiful and wonderful with lots of sunshine and blue skies everywhere you look",
			original: "The weather",
			opts:     NewCompleteOptions().WithMaxLength(30),
			expected: "The weather is absolutely beautiful and",
		},
		{
			name:     "empty response",
			response: "",
			original: "Hello",
			opts:     NewCompleteOptions().WithMaxLength(100),
			expected: "Hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := processCompletionResponse(tt.response, tt.original, tt.opts)
			if result != tt.expected {
				t.Errorf("processCompletionResponse() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

func TestBuildCompleteSystemPrompt(t *testing.T) {
	tests := []struct {
		name     string
		opts     CompleteOptions
		contains []string
	}{
		{
			name: "strict mode",
			opts: NewCompleteOptions().WithMode(types.Strict),
			contains: []string{
				"strict grammatical",
				"formal tone",
			},
		},
		{
			name: "creative mode",
			opts: NewCompleteOptions().WithIntelligence(types.Fast),
			contains: []string{
				"creatively",
				"imaginative",
			},
		},
		{
			name: "with context",
			opts: NewCompleteOptions().WithBackground([]string{"Hello"}),
			contains: []string{
				"provided context",
				"conversation flow",
			},
		},
		{
			name: "with stop sequences",
			opts: NewCompleteOptions().WithStopSequences([]string{".", "!"}),
			contains: []string{
				"Stop generation",
				"sequences",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt := buildCompleteSystemPrompt(tt.opts)
			for _, substr := range tt.contains {
				if !strings.Contains(prompt, substr) {
					t.Errorf("buildCompleteSystemPrompt() missing expected substring: %s", substr)
				}
			}
		})
	}
}

func TestBuildCompleteUserPrompt(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		opts     CompleteOptions
		contains []string
	}{
		{
			name:     "basic prompt",
			text:     "Hello world",
			opts:     NewCompleteOptions(),
			contains: []string{"Complete this text", "Hello world"},
		},
		{
			name:     "with context",
			text:     "How can I help",
			opts:     NewCompleteOptions().WithBackground([]string{"User: I need help", "Assistant: Sure!"}),
			contains: []string{"Context:", "1. User: I need help", "2. Assistant: Sure!", "How can I help"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt := buildCompleteUserPrompt(tt.text, tt.opts)
			for _, substr := range tt.contains {
				if !strings.Contains(prompt, substr) {
					t.Errorf("buildCompleteUserPrompt() missing expected substring: %s", substr)
				}
			}
		})
	}
}

func TestCompleteResult_Structure(t *testing.T) {
	result := CompleteResult{
		Text:            "Hello world complete",
		Original:        "Hello world",
		Length:          9,
		ModelConfidence: 0.8,
		Metadata:        map[string]any{"test": "value"},
	}

	if result.Text != "Hello world complete" {
		t.Errorf("Expected Text 'Hello world complete', got %q", result.Text)
	}
	if result.Original != "Hello world" {
		t.Errorf("Expected Original 'Hello world', got %q", result.Original)
	}
	if result.Length != 9 {
		t.Errorf("Expected Length 9, got %d", result.Length)
	}
	if result.ModelConfidence != 0.8 {
		t.Errorf("Expected ModelConfidence 0.8, got %f", result.ModelConfidence)
	}
	if result.Metadata["test"] != "value" {
		t.Errorf("Expected metadata test=value, got %v", result.Metadata["test"])
	}
}

// OP-404. Complete and CompleteField took a provider argument that no other
// operation took, which leaked internal/llm into a public signature and made
// them the one call a caller had to configure differently from every other.
func TestCompleteUsesTheConfiguredProviderLikeEveryOtherOperation(t *testing.T) {
	var sawCall bool
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		sawCall = true
		return "the completed text", nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	result, err := Complete(context.Background(), "the beginning of", NewCompleteOptions())
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !sawCall {
		t.Error("Complete did not reach the configured caller")
	}
	if result.Text == "" {
		t.Error("Complete returned no completed text")
	}
}

// The same for CompleteField, which delegated to it.
func TestCompleteFieldUsesTheConfiguredProvider(t *testing.T) {
	type record struct {
		Bio string `json:"bio"`
	}
	// The field is named by its Go name; the json tag is the wire form.

	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "a filled-in biography", nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := CompleteField(context.Background(), record{Bio: "born in"},
		NewCompleteFieldOptions("Bio"))
	if err != nil {
		t.Fatalf("CompleteField: %v", err)
	}
}
