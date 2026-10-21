package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "diff example: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "Intelligent Difference Detection")
	fmt.Fprintln(out, "================================")
	fmt.Fprintln(out, "The comparison is made in Go; the model explains the changes.")
	fmt.Fprintln(out)

	type Customer struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Email  string `json:"email"`
		Status string `json:"status"`
		Phone  string `json:"phone"`
	}
	oldCustomer := Customer{ID: 12345, Name: "John Smith", Email: "john@example.com", Status: "active"}
	newCustomer := Customer{ID: 12345, Name: "John A. Smith", Email: "john.smith@example.com", Status: "inactive", Phone: "+1-555-0123"}
	fmt.Fprintln(out, "Customer Record Changes")
	if err := showDiff(ctx, out, oldCustomer, newCustomer, schemaflux.DiffOptions{Background: "Customer management system"}); err != nil {
		return fmt.Errorf("customer comparison failed; check provider configuration and retry: %w", err)
	}

	type Product struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Price       float64  `json:"price"`
		Category    string   `json:"category"`
		Description string   `json:"description"`
		InStock     bool     `json:"in_stock"`
		Tags        []string `json:"tags"`
	}
	oldProduct := Product{ID: "PROD-001", Name: "Wireless Bluetooth Headphones", Price: 79.99, Category: "Electronics", Description: "High-quality wireless headphones", InStock: true}
	newProduct := Product{ID: "PROD-001", Name: "Premium Wireless Bluetooth Headphones (Black)", Price: 89.99, Category: "Electronics > Audio", Description: "Premium high-quality wireless headphones with noise cancellation", InStock: false, Tags: []string{"bestseller", "premium", "noise-cancelling"}}
	fmt.Fprintln(out, "\nProduct Catalog Changes")
	if err := showDiff(ctx, out, oldProduct, newProduct, schemaflux.DiffOptions{Background: "E-commerce product catalog"}); err != nil {
		return fmt.Errorf("product comparison failed; check provider configuration and retry: %w", err)
	}

	type Config struct {
		ServiceName string `json:"service_name"`
		Version     string `json:"version"`
		Port        int    `json:"port"`
		Debug       bool   `json:"debug"`
		LastUpdated string `json:"last_updated"`
	}
	oldConfig := Config{ServiceName: "api-gateway", Version: "1.2.3", Port: 8080, Debug: false, LastUpdated: "2023-01-01T10:00:00Z"}
	newConfig := Config{ServiceName: "api-gateway", Version: "1.3.0", Port: 9090, Debug: true, LastUpdated: "2023-01-02T15:30:00Z"}
	fmt.Fprintln(out, "\nConfiguration Changes (ignoring timestamps)")
	// IgnoreFields uses Go field names. LastUpdated is excluded from both the comparison and summary.
	if err := showDiff(ctx, out, oldConfig, newConfig, schemaflux.DiffOptions{
		Background:   "Service configuration management",
		IgnoreFields: []string{"LastUpdated"},
	}); err != nil {
		return fmt.Errorf("configuration comparison failed; check provider configuration and retry: %w", err)
	}
	fmt.Fprintln(out, "\nDifference detection completed")
	return nil
}

func showDiff[T any](ctx context.Context, out io.Writer, before, after T, options schemaflux.DiffOptions) error {
	result, err := schemaflux.Diff(ctx, before, after, options)
	if err != nil {
		return err
	}
	if result.SummaryError != nil {
		return result.SummaryError
	}
	fmt.Fprintf(out, "  Added: %v\n  Removed: %v\n  Modified: %d fields\n", result.Added, result.Removed, len(result.Modified))
	for _, change := range result.Modified {
		fmt.Fprintf(out, "    - %s: %v -> %v\n", change.Field, change.OldValue, change.NewValue)
	}
	fmt.Fprintf(out, "  Summary: %s\n", result.Summary)
	return nil
}
