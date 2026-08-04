// 16-infer: Infer missing information from partial data using LLM
// Intelligence: Fast (Cerebras gpt-oss-120b)
// Expectations:
// - Person: Name="John", Age=30 ? infers Email and City based on context
// - Product: Name="iPhone 15" ? infers Price, Category, Brand

package main

import (
	"context"
	"fmt"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/internal/exampleutil"
)

// Person represents a person with some fields that might be missing
type Person struct {
	Name  string `json:"name"`
	Age   int    `json:"age"`
	Email string `json:"email"`
	City  string `json:"city"`
}

// Product represents a product with incomplete information
type Product struct {
	Name     string  `json:"name"`
	Price    float64 `json:"price"`
	Category string  `json:"category"`
	Brand    string  `json:"brand"`
}

func main() {

	// Initialize SchemaFlux with Fast intelligence (Cerebras)
	fmt.Println("?? Initializing SchemaFlux...")
	if err := exampleutil.Bootstrap(); err != nil {
		schemaflux.GetLogger().Error("Failed to initialize SchemaFlux", "error", err)
		return
	}
	fmt.Println("? SchemaFlux initialized successfully")

	fmt.Println("?? Smart Data Inference Example")
	fmt.Println("=" + string(make([]byte, 50)))

	// Create a context with 10-second timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Example 1: Infer missing person fields
	fmt.Println("\n?? Partial Person Data:")
	partialPerson := Person{
		Name: "John",
		Age:  30,
	}
	fmt.Printf("  Name: %s\n", partialPerson.Name)
	fmt.Printf("  Age:  %d\n", partialPerson.Age)
	fmt.Println("  Email: (missing)")
	fmt.Println("  City:  (missing)")

	// Infer complete person data
	fmt.Println("\n?? Starting person inference...")

	// Set timeout context on the options
	opts := schemaflux.NewInferOptions().
		WithContext("Tech professional working in San Francisco").
		WithIntelligence(schemaflux.Fast)
	opts.OpOptions.Context = ctx

	completePerson, err := schemaflux.Infer[Person](partialPerson, opts)

	if err != nil {
		schemaflux.GetLogger().Error("Person inference failed", "error", err)
		return
	}
	fmt.Println("? Person inference completed")

	fmt.Println("\n? Inferred Complete Person:")
	fmt.Printf("  Name:  %s\n", completePerson.Name)
	fmt.Printf("  Age:   %d\n", completePerson.Age)
	fmt.Printf("  Email: %s\n", completePerson.Email)
	fmt.Printf("  City:  %s\n", completePerson.City)

	// Example 2: Infer missing product fields
	fmt.Println("\n?? Partial Product Data:")
	partialProduct := Product{
		Name: "iPhone 15",
	}
	fmt.Printf("  Name: %s\n", partialProduct.Name)
	fmt.Println("  Price:    (missing)")
	fmt.Println("  Category: (missing)")
	fmt.Println("  Brand:    (missing)")

	// Infer complete product data
	fmt.Println("\n?? Starting product inference...")

	// Set timeout context on the options
	productOpts := schemaflux.NewInferOptions().
		WithContext("Latest Apple smartphone released in 2023 with premium pricing").
		WithIntelligence(schemaflux.Fast)
	productOpts.OpOptions.Context = ctx

	completeProduct, err := schemaflux.Infer[Product](partialProduct, productOpts)

	if err != nil {
		schemaflux.GetLogger().Error("Product inference failed", "error", err)
		return
	}
	fmt.Println("? Product inference completed")

	fmt.Println("\n? Inferred Complete Product:")
	fmt.Printf("  Name:     %s\n", completeProduct.Name)
	fmt.Printf("  Price:    $%.2f\n", completeProduct.Price)
	fmt.Printf("  Category: %s\n", completeProduct.Category)
	fmt.Printf("  Brand:    %s\n", completeProduct.Brand)

	fmt.Println("\n? Success! Partial data ? Complete records")
}
