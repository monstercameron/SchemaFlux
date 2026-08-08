package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

func TestInfer(t *testing.T) {
	setupMockClient()

	// Update mock to return inferred data
	setLLMCaller(func(ctx context.Context, system, user string, opts types.OpOptions) (string, error) {
		if strings.Contains(system, "data inference expert") {
			if strings.Contains(user, `"name":"John"`) && strings.Contains(user, `"age":30`) {
				return `{
					"name": "John",
					"age": 30,
					"email": "john.doe@example.com",
					"city": "San Francisco"
				}`, nil
			}
			if strings.Contains(user, `"name":"iPhone 15"`) {
				return `{
					"name": "iPhone 15",
					"price": 999,
					"category": "smartphone",
					"brand": "Apple"
				}`, nil
			}
		}
		return mockLLMResponse(ctx, system, user, opts)
	})

	t.Run("InferPersonFields", func(t *testing.T) {
		type Person struct {
			Name  string `json:"name"`
			Age   int    `json:"age"`
			Email string `json:"email"`
			City  string `json:"city"`
		}

		partial := Person{Name: "John", Age: 30}
		result, err := Infer(partial, NewInferOptions().WithBackground("Tech professional"))

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if result.Name != "John" {
			t.Errorf("Expected name=John, got %s", result.Name)
		}

		if result.Age != 30 {
			t.Errorf("Expected age=30, got %d", result.Age)
		}

		if result.Email == "" {
			t.Errorf("Expected email to be inferred, got empty")
		}

		if result.City == "" {
			t.Errorf("Expected city to be inferred, got empty")
		}
	})

	t.Run("InferProductFields", func(t *testing.T) {
		type Product struct {
			Name     string  `json:"name"`
			Price    float64 `json:"price"`
			Category string  `json:"category"`
			Brand    string  `json:"brand"`
		}

		partial := Product{Name: "iPhone 15"}
		result, err := Infer(partial, NewInferOptions().WithBackground("Latest Apple smartphone released in 2023"))

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if result.Name != "iPhone 15" {
			t.Errorf("Expected name=iPhone 15, got %s", result.Name)
		}

		if result.Price <= 0 {
			t.Errorf("Expected price to be inferred, got %f", result.Price)
		}

		if result.Category != "smartphone" {
			t.Errorf("Expected category=smartphone, got %s", result.Category)
		}

		if result.Brand != "Apple" {
			t.Errorf("Expected brand=Apple, got %s", result.Brand)
		}
	})

	t.Run("InferOptionsValidation", func(t *testing.T) {
		opts := NewInferOptions()
		if err := opts.Validate(); err != nil {
			t.Errorf("Expected no validation error, got %v", err)
		}
	})

	t.Run("InferWithContext", func(t *testing.T) {
		type Person struct {
			Name  string `json:"name"`
			Age   int    `json:"age"`
			Email string `json:"email"`
			City  string `json:"city"`
		}

		partial := Person{Name: "John", Age: 30}
		opts := NewInferOptions().WithBackground("Lives in San Francisco")
		result, err := Infer(partial, opts)

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if result.Name != "John" || result.Age != 30 {
			t.Errorf("Expected original fields to be preserved")
		}
	})
}
