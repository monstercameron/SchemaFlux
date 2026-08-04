package ops

import (
	"fmt"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

func TestExplainOptions(t *testing.T) {
	tests := []struct {
		name    string
		opts    ExplainOptions
		wantErr bool
	}{
		{
			name:    "valid defaults",
			opts:    NewExplainOptions(),
			wantErr: false,
		},
		{
			name: "valid custom options",
			opts: NewExplainOptions().
				WithAudience("technical").
				WithDepth(3).
				WithFormat("bullet-points").
				WithContext("test context").
				WithFocus("implementation"),
			wantErr: false,
		},
		{
			name:    "invalid audience",
			opts:    NewExplainOptions().WithAudience("invalid"),
			wantErr: true,
		},
		{
			name:    "invalid depth low",
			opts:    NewExplainOptions().WithDepth(0),
			wantErr: true,
		},
		{
			name:    "invalid depth high",
			opts:    NewExplainOptions().WithDepth(5),
			wantErr: true,
		},
		{
			name:    "invalid format",
			opts:    NewExplainOptions().WithFormat("invalid"),
			wantErr: true,
		},
		{
			name:    "invalid focus",
			opts:    NewExplainOptions().WithFocus("invalid"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("ExplainOptions.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAnalyzeDataForExplanation(t *testing.T) {
	tests := []struct {
		name      string
		data      any
		wantType  string
		wantCount int
		wantErr   bool
	}{
		{
			name:      "simple struct",
			data:      struct{ Name string }{Name: "test"},
			wantType:  "struct { Name string }",
			wantCount: 1,
			wantErr:   false,
		},
		{
			name:      "complex struct",
			data:      types.OpOptions{Mode: types.Strict, Intelligence: types.Smart},
			wantType:  "types.OpOptions",
			wantCount: 7,
			wantErr:   false,
		},
		{
			name:      "slice",
			data:      []string{"a", "b", "c"},
			wantType:  "[]string",
			wantCount: 3,
			wantErr:   false,
		},
		{
			name:      "map",
			data:      map[string]int{"a": 1, "b": 2},
			wantType:  "map[string]int",
			wantCount: 2,
			wantErr:   false,
		},
		{
			name:      "primitive",
			data:      "hello",
			wantType:  "string",
			wantCount: 1,
			wantErr:   false,
		},
		{
			name:      "nil pointer",
			data:      (*string)(nil),
			wantType:  "",
			wantCount: 0,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			analysis, err := analyzeDataForExplanation(tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("analyzeDataForExplanation() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if analysis.DataType != tt.wantType {
					t.Errorf("analyzeDataForExplanation() dataType = %v, want %v", analysis.DataType, tt.wantType)
				}
				if analysis.FieldCount != tt.wantCount {
					t.Errorf("analyzeDataForExplanation() fieldCount = %v, want %v", analysis.FieldCount, tt.wantCount)
				}
			}
		})
	}
}

func TestExplain(t *testing.T) {
	// Test data
	testData := struct {
		Name        string `json:"name"`
		Age         int    `json:"age"`
		IsActive    bool   `json:"is_active"`
		Description string `json:"description"`
	}{
		Name:        "John Doe",
		Age:         30,
		IsActive:    true,
		Description: "A software developer with 5 years of experience",
	}

	tests := []struct {
		name    string
		data    any
		opts    ExplainOptions
		wantErr bool
	}{
		{
			name:    "basic explanation",
			data:    testData,
			opts:    NewExplainOptions(),
			wantErr: false,
		},
		{
			name: "technical audience",
			data: testData,
			opts: NewExplainOptions().
				WithAudience("technical").
				WithDepth(3).
				WithFormat("structured"),
			wantErr: false,
		},
		{
			name: "children audience",
			data: testData,
			opts: NewExplainOptions().
				WithAudience("children").
				WithDepth(1).
				WithFormat("paragraph"),
			wantErr: false,
		},
		{
			name: "executive audience",
			data: testData,
			opts: NewExplainOptions().
				WithAudience("executive").
				WithFocus("benefits").
				WithFormat("bullet-points"),
			wantErr: false,
		},
		{
			name:    "nil data",
			data:    nil,
			opts:    NewExplainOptions(),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip actual LLM calls in unit tests - just test validation
			if tt.wantErr {
				_, err := explainImpl(tt.data, tt.opts)
				if err == nil {
					t.Errorf("explainImpl() expected error but got none")
				}
				return
			}

			// For successful cases, we would need to mock the LLM response
			// For now, just test that options validation passes
			if err := tt.opts.Validate(); err != nil {
				t.Errorf("options validation failed: %v", err)
			}
		})
	}
}

func TestBuildSystemPrompt(t *testing.T) {
	tests := []struct {
		name     string
		opts     ExplainOptions
		contains []string
	}{
		{
			name: "children audience",
			opts: NewExplainOptions().WithAudience("children").WithDepth(1),
			contains: []string{
				"curious child",
				"simple words",
				"very high-level",
			},
		},
		{
			name: "technical audience",
			opts: NewExplainOptions().WithAudience("technical").WithDepth(4),
			contains: []string{
				"technical explanation",
				"comprehensive",
				"full technical depth",
			},
		},
		{
			name: "executive audience bullet points",
			opts: NewExplainOptions().WithAudience("executive").WithFormat("bullet-points"),
			contains: []string{
				"business executives",
				"business impact",
				"bullet points",
			},
		},
		{
			name: "step-by-step format",
			opts: NewExplainOptions().WithFormat("step-by-step"),
			contains: []string{
				"numbered steps",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt := buildSystemPrompt(tt.opts)
			for _, substr := range tt.contains {
				if !containsString(prompt, substr) {
					t.Errorf("buildSystemPrompt() missing expected substring: %s", substr)
				}
			}
		})
	}
}

func TestGetComplexityLevel(t *testing.T) {
	tests := []struct {
		depth int
		want  string
	}{
		{1, "simple"},
		{2, "intermediate"},
		{3, "detailed"},
		{4, "comprehensive"},
		{0, "intermediate"},
		{5, "intermediate"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("depth_%d", tt.depth), func(t *testing.T) {
			got := getComplexityLevel(tt.depth)
			if got != tt.want {
				t.Errorf("getComplexityLevel(%d) = %v, want %v", tt.depth, got, tt.want)
			}
		})
	}
}

// Helper function to check if string contains substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || containsString(s[1:], substr) || (len(s) > 0 && s[:len(substr)] == substr))
}

// Benchmark tests
func BenchmarkExplainOptionsValidation(b *testing.B) {
	opts := NewExplainOptions().
		WithAudience("technical").
		WithDepth(3).
		WithFormat("structured")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = opts.Validate()
	}
}

func BenchmarkAnalyzeDataForExplanation(b *testing.B) {
	testData := struct {
		Name        string            `json:"name"`
		Age         int               `json:"age"`
		Metadata    map[string]string `json:"metadata"`
		Items       []string          `json:"items"`
		IsActive    bool              `json:"is_active"`
		Description string            `json:"description"`
	}{
		Name:        "Benchmark Test",
		Age:         25,
		Metadata:    map[string]string{"key": "value", "type": "test"},
		Items:       []string{"item1", "item2", "item3"},
		IsActive:    true,
		Description: "This is a benchmark test data structure with various field types",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = analyzeDataForExplanation(testData)
	}
}
