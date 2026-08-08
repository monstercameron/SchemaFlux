package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

func TestDiff(t *testing.T) {
	setupMockClient()

	// Update mock to return diff summaries
	setLLMCaller(func(ctx context.Context, system, user string, opts types.OpOptions) (string, error) {
		if strings.Contains(system, "data analyst specializing in change detection") {
			if strings.Contains(user, "John Doe") && strings.Contains(user, "inactive") {
				return "Customer name was completed with middle initial and account status changed to inactive, suggesting possible account suspension or data cleanup activity.", nil
			}
			if strings.Contains(user, "iPhone 15") && strings.Contains(user, "1099.99") {
				return "Product name refined with color specification, price increased by $100.99 (11% markup), category made more specific with hierarchy. New tags indicate recent popular product introduction.", nil
			}
		}
		return mockLLMResponse(ctx, system, user, opts)
	})

	t.Run("DiffCustomerRecords", func(t *testing.T) {
		type Customer struct {
			ID     int    `json:"id"`
			Name   string `json:"name"`
			Email  string `json:"email"`
			Status string `json:"status"`
		}

		oldCust := Customer{ID: 1, Name: "John", Email: "john@example.com", Status: "active"}
		newCust := Customer{ID: 1, Name: "John Doe", Email: "john.doe@example.com", Status: "inactive"}

		result, err := Diff(oldCust, newCust, NewDiffOptions().WithBackground("Customer management system"))

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		// Check modified fields
		if len(result.Modified) != 3 {
			t.Errorf("Expected 3 modified fields, got %d", len(result.Modified))
		}

		// Check specific changes
		foundNameChange := false
		foundStatusChange := false
		foundEmailChange := false
		for _, change := range result.Modified {
			if change.Field == "Name" {
				if change.OldValue != "John" || change.NewValue != "John Doe" {
					t.Errorf("Name change incorrect: %v -> %v", change.OldValue, change.NewValue)
				}
				foundNameChange = true
			}
			if change.Field == "Status" {
				if change.OldValue != "active" || change.NewValue != "inactive" {
					t.Errorf("Status change incorrect: %v -> %v", change.OldValue, change.NewValue)
				}
				foundStatusChange = true
			}
			if change.Field == "Email" {
				if change.OldValue != "john@example.com" || change.NewValue != "john.doe@example.com" {
					t.Errorf("Email change incorrect: %v -> %v", change.OldValue, change.NewValue)
				}
				foundEmailChange = true
			}
		}

		if !foundNameChange || !foundStatusChange || !foundEmailChange {
			t.Errorf("Expected name, email, and status changes not found")
		}

		// Check summary
		if result.Summary == "" {
			t.Errorf("Expected non-empty summary")
		}

		if !strings.Contains(result.Summary, "inactive") {
			t.Errorf("Summary should mention status change: %s", result.Summary)
		}
	})

	t.Run("DiffProductCatalog", func(t *testing.T) {
		type Product struct {
			ID       string   `json:"id"`
			Name     string   `json:"name"`
			Price    float64  `json:"price"`
			Category string   `json:"category"`
			Tags     []string `json:"tags"`
		}

		oldProd := Product{
			ID:       "IPHONE15-128",
			Name:     "iPhone 15 128GB",
			Price:    999.00,
			Category: "Smartphones",
		}
		newProd := Product{
			ID:       "IPHONE15-128",
			Name:     "iPhone 15 128GB (Black)",
			Price:    1099.99,
			Category: "Electronics > Smartphones",
			Tags:     []string{"bestseller", "new-arrival"},
		}

		result, err := Diff(oldProd, newProd, NewDiffOptions().WithBackground("E-commerce product catalog"))

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		// Check added fields (none expected since Tags field exists in both)
		if len(result.Added) != 0 {
			t.Errorf("Expected no added fields, got %v", result.Added)
		}

		// Check modified fields (Name, Price, Category, Tags)
		if len(result.Modified) != 4 {
			t.Errorf("Expected 4 modified fields, got %d", len(result.Modified))
		}

		// Check summary mentions key changes
		if !strings.Contains(result.Summary, "price increased") {
			t.Errorf("Summary should mention price increase: %s", result.Summary)
		}
	})

	t.Run("DiffWithIgnoredFields", func(t *testing.T) {
		type Document struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Content     string `json:"content"`
			LastUpdated string `json:"last_updated"`
		}

		oldDoc := Document{ID: "1", Title: "Old Title", Content: "Old content", LastUpdated: "2023-01-01"}
		newDoc := Document{ID: "1", Title: "New Title", Content: "New content", LastUpdated: "2023-01-02"}

		result, err := Diff(oldDoc, newDoc, NewDiffOptions().WithIgnoreFields([]string{"LastUpdated"}))

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		// Should not detect LastUpdated change
		for _, change := range result.Modified {
			if change.Field == "LastUpdated" {
				t.Errorf("LastUpdated should be ignored")
			}
		}

		// Should detect Title and Content changes
		if len(result.Modified) != 2 {
			t.Errorf("Expected 2 modified fields (ignoring LastUpdated), got %d", len(result.Modified))
		}
	})

	t.Run("DiffOptionsValidation", func(t *testing.T) {
		opts := NewDiffOptions()
		if err := opts.Validate(); err != nil {
			t.Errorf("Expected no validation error, got %v", err)
		}
	})

	t.Run("DiffNoChanges", func(t *testing.T) {
		type Item struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}

		item1 := Item{ID: 1, Name: "Test"}
		item2 := Item{ID: 1, Name: "Test"}

		result, err := Diff(item1, item2, NewDiffOptions())

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if len(result.Added) != 0 || len(result.Removed) != 0 || len(result.Modified) != 0 {
			t.Errorf("Expected no changes, got added=%v, removed=%v, modified=%v",
				result.Added, result.Removed, result.Modified)
		}
	})

	t.Run("DiffPrimitiveTypes", func(t *testing.T) {
		type Config struct {
			Enabled bool    `json:"enabled"`
			Count   int     `json:"count"`
			Rate    float64 `json:"rate"`
		}

		oldCfg := Config{Enabled: true, Count: 10, Rate: 1.5}
		newCfg := Config{Enabled: false, Count: 15, Rate: 2.0}

		result, err := Diff(oldCfg, newCfg, NewDiffOptions())

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if len(result.Modified) != 3 {
			t.Errorf("Expected 3 modified fields, got %d", len(result.Modified))
		}

		// Verify all primitive changes detected
		fieldsChanged := make(map[string]bool)
		for _, change := range result.Modified {
			fieldsChanged[change.Field] = true
		}

		if !fieldsChanged["Enabled"] || !fieldsChanged["Count"] || !fieldsChanged["Rate"] {
			t.Errorf("Expected changes in Enabled, Count, and Rate fields")
		}
	})
}
