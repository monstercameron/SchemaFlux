package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/types"
)

// loadEnv loads environment variables from .env files
func loadEnv() {
	// Try to load from current directory first
	if err := godotenv.Load(); err == nil {
		return
	}
	// Try parent directories
	dir, _ := os.Getwd()
	for i := 0; i < 3; i++ {
		envPath := filepath.Join(dir, ".env")
		if err := godotenv.Load(envPath); err == nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// ============================================================
// USE CASE 1: Customer Record Deduplication (CRM Merge)
// ============================================================

// CustomerRecord from different source systems
type CustomerRecord struct {
	CustomerID  string `json:"customer_id"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	Address     string `json:"address"`
	LastContact string `json:"last_contact"`
	Status      string `json:"status"`
}

// ============================================================
// USE CASE 2: Product Catalog Consolidation (Multi-Vendor)
// ============================================================

// ProductData from multiple vendors/warehouses
type ProductData struct {
	SKU         string  `json:"sku"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Price       float64 `json:"price"`
	Stock       int     `json:"stock"`
	Weight      float64 `json:"weight_kg"`
}

// ============================================================
// USE CASE 3: Employee Record Reconciliation (HR Systems)
// ============================================================

// EmployeeRecord from HRIS, Payroll, Directory
type EmployeeRecord struct {
	EmployeeID string `json:"employee_id"`
	FullName   string `json:"full_name"`
	Email      string `json:"email"`
	Department string `json:"department"`
	Title      string `json:"title"`
	Manager    string `json:"manager"`
}

func main() {
	loadEnv()

	if err := schemaflux.InitWithEnv(); err != nil {
		fmt.Printf("Init failed: %v\n", err)
		return
	}

	fmt.Println("=== Resolve Example ===")
	fmt.Println("Reconciling conflicting data from multiple sources")

	// ============================================================
	// USE CASE 1: Customer Record Deduplication
	// Scenario: Same customer in CRM, Sales DB, and Marketing with conflicts
	// ============================================================
	fmt.Println("\n--- Use Case 1: Customer Record Deduplication ---")

	customerSources := []CustomerRecord{
		{
			CustomerID:  "CUST-5521",
			Name:        "John Smith",
			Email:       "john.smith@email.com",
			Phone:       "555-123-4567",
			Address:     "123 Main St, Boston, MA",
			LastContact: "2024-01-15",
			Status:      "active",
		},
		{
			CustomerID:  "CUST-5521",
			Name:        "John A. Smith",
			Email:       "jsmith@work.com",                   // Work email - conflict!
			Phone:       "(555) 123-4567",                    // Same number, different format
			Address:     "123 Main Street, Boston, MA 02101", // More complete
			LastContact: "2024-01-20",                        // More recent
			Status:      "active",
		},
		{
			CustomerID:  "CUST-5521",
			Name:        "John Smith",
			Email:       "john.smith@email.com",
			Phone:       "",                        // Missing phone
			Address:     "456 Oak Ave, Boston, MA", // OLD address!
			LastContact: "2023-12-01",              // Old contact date
			Status:      "inactive",                // Status conflict!
		},
	}

	custResult, err := schemaflux.Resolve[CustomerRecord](customerSources, schemaflux.ResolveOptions{
		Strategy:     "most-complete",
		Intelligence: types.Smart,
		Steering:     "Prefer more recent LastContact dates. Ignore Marketing source (source 2) for address since it's outdated. Active status trumps inactive.",
	})
	if err != nil {
		fmt.Printf("Customer resolution failed: %v\n", err)
	} else {
		fmt.Printf("Sources: CRM (0), Sales DB (1), Marketing (2)\n")
		fmt.Printf("\nResolved Customer:\n")
		fmt.Printf("  ID: %s\n", custResult.Resolved.CustomerID)
		fmt.Printf("  Name: %s\n", custResult.Resolved.Name)
		fmt.Printf("  Email: %s\n", custResult.Resolved.Email)
		fmt.Printf("  Phone: %s\n", custResult.Resolved.Phone)
		fmt.Printf("  Address: %s\n", custResult.Resolved.Address)
		fmt.Printf("  LastContact: %s\n", custResult.Resolved.LastContact)
		fmt.Printf("  Status: %s\n", custResult.Resolved.Status)
		fmt.Printf("\nConflicts Resolved: %d\n", len(custResult.Conflicts))
		for _, c := range custResult.Conflicts {
			fmt.Printf("  - %s: chose source %d (%s)\n", c.Field, c.ChosenSource, c.Resolution)
		}
		fmt.Printf("ModelConfidence: %.0f%%\n", custResult.ModelConfidence*100)
	}

	// ============================================================
	// USE CASE 2: Product Catalog Consolidation
	// Scenario: Same SKU from 3 warehouses with different data quality
	// ============================================================
	fmt.Println("\n--- Use Case 2: Product Catalog Consolidation ---")

	productSources := []ProductData{
		{
			SKU:         "PROD-7890",
			Name:        "Wireless Headphones",
			Description: "High-quality wireless headphones",
			Price:       79.99,
			Stock:       150,
			Weight:      0.25,
		},
		{
			SKU:         "PROD-7890",
			Name:        "Wireless Bluetooth Headphones Pro",
			Description: "Premium wireless headphones with noise cancellation, 30-hour battery, foldable design",
			Price:       89.99, // Higher price - conflict!
			Stock:       75,
			Weight:      0.28, // Slightly different weight
		},
		{
			SKU:         "PROD-7890",
			Name:        "BT Headphones",
			Description: "", // Missing description!
			Price:       79.99,
			Stock:       200, // Highest stock
			Weight:      0.0, // Missing weight!
		},
	}

	prodResult, err := schemaflux.Resolve[ProductData](productSources, schemaflux.ResolveOptions{
		Strategy:     "most-complete",
		Intelligence: types.Smart,
		FieldPriorities: map[string]int{
			"price": 0, // Trust Warehouse A for pricing
			"stock": 2, // Trust Vendor Feed for real-time stock
		},
		Steering: "Source 1 has the best product description. For stock, use the actual count not aggregated.",
	})
	if err != nil {
		fmt.Printf("Product resolution failed: %v\n", err)
	} else {
		fmt.Printf("Sources: Warehouse A (0), Warehouse B (1), Vendor Feed (2)\n")
		fmt.Printf("\nResolved Product:\n")
		fmt.Printf("  SKU: %s\n", prodResult.Resolved.SKU)
		fmt.Printf("  Name: %s\n", prodResult.Resolved.Name)
		fmt.Printf("  Description: %s\n", prodResult.Resolved.Description)
		fmt.Printf("  Price: $%.2f\n", prodResult.Resolved.Price)
		fmt.Printf("  Stock: %d units\n", prodResult.Resolved.Stock)
		fmt.Printf("  Weight: %.2f kg\n", prodResult.Resolved.Weight)
		fmt.Printf("\nConflicts Resolved: %d\n", len(prodResult.Conflicts))
		for _, c := range prodResult.Conflicts {
			fmt.Printf("  - %s: chose source %d (%s)\n", c.Field, c.ChosenSource, c.Resolution)
		}
		fmt.Printf("ModelConfidence: %.0f%%\n", prodResult.ModelConfidence*100)
	}

	// ============================================================
	// USE CASE 3: Employee Record Reconciliation
	// Scenario: HRIS, Payroll, and Directory have different employee info
	// ============================================================
	fmt.Println("\n--- Use Case 3: Employee Record Reconciliation ---")

	employeeSources := []EmployeeRecord{
		{
			EmployeeID: "EMP-1234",
			FullName:   "Sarah Johnson",
			Email:      "sarah.johnson@company.com",
			Department: "Engineering",
			Title:      "Senior Software Engineer",
			Manager:    "Mike Chen",
		},
		{
			EmployeeID: "EMP-1234",
			FullName:   "Sarah M. Johnson",
			Email:      "s.johnson@company.com", // Short email variant
			Department: "Platform Engineering",  // Reorg! More specific dept
			Title:      "Staff Engineer",        // Promotion!
			Manager:    "Michael Chen",          // Same manager, full name
		},
		{
			EmployeeID: "EMP-1234",
			FullName:   "Sarah Johnson",
			Email:      "sarah.johnson@company.com",
			Department: "Engineering",
			Title:      "Software Engineer", // OLD title
			Manager:    "",                  // Missing manager
		},
	}

	empResult, err := schemaflux.Resolve[EmployeeRecord](employeeSources, schemaflux.ResolveOptions{
		Strategy:            "authoritative",
		AuthoritativeSource: 1, // Payroll is most up-to-date
		Intelligence:        types.Smart,
		Steering:            "Payroll (source 1) has the most current title and department due to recent reorg. HRIS may be stale.",
	})
	if err != nil {
		fmt.Printf("Employee resolution failed: %v\n", err)
	} else {
		fmt.Printf("Sources: HRIS (0), Payroll (1), Directory (2)\n")
		fmt.Printf("\nResolved Employee:\n")
		fmt.Printf("  ID: %s\n", empResult.Resolved.EmployeeID)
		fmt.Printf("  Name: %s\n", empResult.Resolved.FullName)
		fmt.Printf("  Email: %s\n", empResult.Resolved.Email)
		fmt.Printf("  Department: %s\n", empResult.Resolved.Department)
		fmt.Printf("  Title: %s\n", empResult.Resolved.Title)
		fmt.Printf("  Manager: %s\n", empResult.Resolved.Manager)
		fmt.Printf("\nConflicts Resolved: %d\n", len(empResult.Conflicts))
		for _, c := range empResult.Conflicts {
			fmt.Printf("  - %s: chose source %d (%s)\n", c.Field, c.ChosenSource, c.Resolution)
		}
		fmt.Printf("Strategy: %s (source %d authoritative)\n", empResult.Strategy, 1)
		fmt.Printf("ModelConfidence: %.0f%%\n", empResult.ModelConfidence*100)
	}

	fmt.Println("\n=== Resolve Example Complete ===")
}
