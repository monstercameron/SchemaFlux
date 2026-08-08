// 17-diff: Find semantic differences between two versions of data
// Intelligence: Fast (Cerebras gpt-oss-120b)
// Expectations:
// - Customer: Detects name, email, status changes + phone added
// - Product: Detects name, price, category, description, stock changes + tags added
// - Config: Detects version, port, debug changes (ignoring LastUpdated)

package main

import (
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/internal/exampleutil"
)

func main() {

	fmt.Println("?? Intelligent Difference Detection Example")
	fmt.Println("=")

	// Initialize SchemaFlux with Fast intelligence (Cerebras)
	fmt.Println("?? Initializing SchemaFlux...")
	if err := exampleutil.Bootstrap(); err != nil {
		schemaflux.GetLogger().Error("Failed to initialize SchemaFlux", "error", err)
		return
	}
	fmt.Println("? SchemaFlux initialized successfully")
	fmt.Println()

	// Example 1: Customer Record Changes
	fmt.Println("?? Customer Record Changes")
	fmt.Println("-")

	type Customer struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Email  string `json:"email"`
		Status string `json:"status"`
		Phone  string `json:"phone"`
	}

	oldCustomer := Customer{
		ID:     12345,
		Name:   "John Smith",
		Email:  "john@example.com",
		Status: "active",
	}

	newCustomer := Customer{
		ID:     12345,
		Name:   "John A. Smith",
		Email:  "john.smith@example.com",
		Status: "inactive",
		Phone:  "+1-555-0123",
	}

	fmt.Printf("?? Old Customer:\n  %+v\n\n", oldCustomer)
	fmt.Printf("?? New Customer:\n  %+v\n\n", newCustomer)

	fmt.Println("?? Analyzing differences...")
	result, err := schemaflux.Diff(oldCustomer, newCustomer,
		schemaflux.NewDiffOptions().WithBackground("Customer management system"))
	if err != nil {
		schemaflux.GetLogger().Error("Diff failed", "error", err)
		return
	}

	fmt.Printf("?? Changes Detected:\n")
	fmt.Printf("  Added: %v\n", result.Added)
	fmt.Printf("  Removed: %v\n", result.Removed)
	fmt.Printf("  Modified: %d fields\n", len(result.Modified))
	for _, change := range result.Modified {
		fmt.Printf("    - %s: %v ? %v\n", change.Field, change.OldValue, change.NewValue)
	}
	fmt.Printf("\n?? Summary: %s\n", result.Summary)
	fmt.Println()

	// Example 2: Product Catalog Changes
	fmt.Println("?? Product Catalog Changes")
	fmt.Println("-")

	type Product struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Price       float64  `json:"price"`
		Category    string   `json:"category"`
		Description string   `json:"description"`
		InStock     bool     `json:"in_stock"`
		Tags        []string `json:"tags"`
	}

	oldProduct := Product{
		ID:          "PROD-001",
		Name:        "Wireless Bluetooth Headphones",
		Price:       79.99,
		Category:    "Electronics",
		Description: "High-quality wireless headphones",
		InStock:     true,
	}

	newProduct := Product{
		ID:          "PROD-001",
		Name:        "Premium Wireless Bluetooth Headphones (Black)",
		Price:       89.99,
		Category:    "Electronics > Audio",
		Description: "Premium high-quality wireless headphones with noise cancellation",
		InStock:     false,
		Tags:        []string{"bestseller", "premium", "noise-cancelling"},
	}

	fmt.Printf("?? Old Product:\n  %+v\n\n", oldProduct)
	fmt.Printf("?? New Product:\n  %+v\n\n", newProduct)

	fmt.Println("?? Analyzing differences...")
	productResult, err := schemaflux.Diff(oldProduct, newProduct,
		schemaflux.NewDiffOptions().WithBackground("E-commerce product catalog"))
	if err != nil {
		schemaflux.GetLogger().Error("Diff failed", "error", err)
		return
	}

	fmt.Printf("?? Changes Detected:\n")
	fmt.Printf("  Added: %v\n", productResult.Added)
	fmt.Printf("  Removed: %v\n", productResult.Removed)
	fmt.Printf("  Modified: %d fields\n", len(productResult.Modified))
	for _, change := range productResult.Modified {
		fmt.Printf("    - %s: %v ? %v\n", change.Field, change.OldValue, change.NewValue)
	}
	fmt.Printf("\n?? Summary: %s\n", productResult.Summary)
	fmt.Println()

	// Example 3: Configuration Changes with Ignored Fields
	fmt.Println("??  Configuration Changes (with ignored fields)")
	fmt.Println("-")

	type Config struct {
		ServiceName string `json:"service_name"`
		Version     string `json:"version"`
		Port        int    `json:"port"`
		Debug       bool   `json:"debug"`
		LastUpdated string `json:"last_updated"`
	}

	oldConfig := Config{
		ServiceName: "api-gateway",
		Version:     "1.2.3",
		Port:        8080,
		Debug:       false,
		LastUpdated: "2023-01-01T10:00:00Z",
	}

	newConfig := Config{
		ServiceName: "api-gateway",
		Version:     "1.3.0",
		Port:        9090,
		Debug:       true,
		LastUpdated: "2023-01-02T15:30:00Z",
	}

	fmt.Printf("??  Old Config:\n  %+v\n\n", oldConfig)
	fmt.Printf("??  New Config:\n  %+v\n\n", newConfig)

	fmt.Println("?? Analyzing differences (ignoring timestamps)...")
	configResult, err := schemaflux.Diff(oldConfig, newConfig,
		schemaflux.NewDiffOptions().
			WithBackground("Service configuration management").
			WithIgnoreFields([]string{"LastUpdated"}))
	if err != nil {
		schemaflux.GetLogger().Error("Diff failed", "error", err)
		return
	}

	fmt.Printf("?? Changes Detected:\n")
	fmt.Printf("  Added: %v\n", configResult.Added)
	fmt.Printf("  Removed: %v\n", configResult.Removed)
	fmt.Printf("  Modified: %d fields\n", len(configResult.Modified))
	for _, change := range configResult.Modified {
		fmt.Printf("    - %s: %v ? %v\n", change.Field, change.OldValue, change.NewValue)
	}
	fmt.Printf("\n?? Summary: %s\n", configResult.Summary)
	fmt.Println()

	fmt.Println("? Success! Intelligent difference detection completed")
}
