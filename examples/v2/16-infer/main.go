package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// Person has fields that may be missing from the source record.
type Person struct {
	Name  string `json:"name"`
	Age   int    `json:"age"`
	Email string `json:"email"`
	City  string `json:"city"`
}

// Product has incomplete catalog information.
type Product struct {
	Name     string  `json:"name"`
	Price    float64 `json:"price"`
	Category string  `json:"category"`
	Brand    string  `json:"brand"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := run(ctx, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "Smart Data Inference Example")
	fmt.Fprintln(out, "==================================================")

	fmt.Fprintln(out, "\nPartial Person Data:")
	partialPerson := Person{Name: "John", Age: 30}
	fmt.Fprintf(out, "  Name: %s\n", partialPerson.Name)
	fmt.Fprintf(out, "  Age:  %d\n", partialPerson.Age)
	fmt.Fprintln(out, "  Email: (missing)")
	fmt.Fprintln(out, "  City:  (missing)")
	fmt.Fprintln(out, "\nStarting person inference...")
	// Background supplies context clues; the zero value uses Infer's default fast tier.
	completePerson, err := schemaflux.Infer(ctx, partialPerson, schemaflux.InferOptions{
		Background: "Tech professional working in San Francisco",
	})
	if err != nil {
		return fmt.Errorf("person inference failed; check the provider configuration and retry: %w", err)
	}
	fmt.Fprintln(out, "Person inference completed")
	fmt.Fprintln(out, "\nInferred Complete Person:")
	fmt.Fprintf(out, "  Name:  %s\n", completePerson.Name)
	fmt.Fprintf(out, "  Age:   %d\n", completePerson.Age)
	fmt.Fprintf(out, "  Email: %s\n", completePerson.Email)
	fmt.Fprintf(out, "  City:  %s\n", completePerson.City)

	fmt.Fprintln(out, "\nPartial Product Data:")
	partialProduct := Product{Name: "iPhone 15"}
	fmt.Fprintf(out, "  Name: %s\n", partialProduct.Name)
	fmt.Fprintln(out, "  Price:    (missing)")
	fmt.Fprintln(out, "  Category: (missing)")
	fmt.Fprintln(out, "  Brand:    (missing)")
	fmt.Fprintln(out, "\nStarting product inference...")
	completeProduct, err := schemaflux.Infer(ctx, partialProduct, schemaflux.InferOptions{
		Background: "Latest Apple smartphone released in 2023 with premium pricing",
	})
	if err != nil {
		return fmt.Errorf("product inference failed; check the provider configuration and retry: %w", err)
	}
	fmt.Fprintln(out, "Product inference completed")
	fmt.Fprintln(out, "\nInferred Complete Product:")
	fmt.Fprintf(out, "  Name:     %s\n", completeProduct.Name)
	fmt.Fprintf(out, "  Price:    $%.2f\n", completeProduct.Price)
	fmt.Fprintf(out, "  Category: %s\n", completeProduct.Category)
	fmt.Fprintf(out, "  Brand:    %s\n", completeProduct.Brand)
	fmt.Fprintln(out, "\nSuccess! Partial data → Complete records")
	return nil
}
