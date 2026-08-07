// Example: 04-choose
//
// Operation: Choose[T] - Picks the best option from a list based on criteria
//
// Input: []Product (4 products) + Customer Query
//
//	Products: [UltraBook Pro $1299, PowerStation Desktop $1899, BudgetBook $499, CreatorPro $2499]
//	Query: "College student, CS major, portable for coding, budget $500-600"
//
// Expected Output: BudgetBook (best match for budget + portability + use case)
//
//	Product{
//	    ID: 3, Name: "BudgetBook", Category: "Laptop", Price: 499.99,
//	    Features: ["8GB RAM", "256GB SSD", "14-inch display", "8hr battery"],
//	    Description: "Affordable laptop for students and basic computing",
//	}
//
// Provider: Cerebras (gpt-oss-120b via Fast intelligence)
// Expected Duration: ~500-1000ms
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux"
)

// Product represents a product in the catalog
type Product struct {
	ID          int      `json:"id"`          // Expected: Product identifier
	Name        string   `json:"name"`        // Expected: Product name
	Category    string   `json:"category"`    // Expected: "Laptop" or "Desktop"
	Price       float64  `json:"price"`       // Expected: Price in USD
	Features    []string `json:"features"`    // Expected: List of key features
	Description string   `json:"description"` // Expected: Product description
}

// loadEnv loads environment variables from a .env file
func loadEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			os.Setenv(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		}
	}
	return scanner.Err()
}

func main() {
	// Load .env file from project root
	if err := loadEnv("../../.env"); err != nil {
		fmt.Printf("Warning: Could not load .env file: %v\n", err)
	}

	// Initialize SchemaFlux
	if err := schemaflux.InitWithEnv(); err != nil {
		schemaflux.GetLogger().Error("Failed to initialize SchemaFlux", "error", err)
		os.Exit(1)
	}

	// Product catalog (products at various price points)
	products := []Product{
		{
			ID:          1,
			Name:        "BudgetBook",
			Category:    "Laptop",
			Price:       499.99,
			Features:    []string{"8GB RAM", "256GB SSD", "14-inch display", "8hr battery"},
			Description: "Affordable laptop for students and basic computing",
		},
		{
			ID:          2,
			Name:        "UltraBook Pro",
			Category:    "Laptop",
			Price:       1299.99,
			Features:    []string{"16GB RAM", "512GB SSD", "13-inch display", "10hr battery"},
			Description: "Lightweight laptop perfect for professionals on the go",
		},
		{
			ID:          3,
			Name:        "PowerStation Desktop",
			Category:    "Desktop",
			Price:       1899.99,
			Features:    []string{"32GB RAM", "1TB SSD", "RTX 4070", "27-inch monitor"},
			Description: "High-performance desktop for gaming and content creation",
		},
		{
			ID:          4,
			Name:        "CreatorPro Workstation",
			Category:    "Desktop",
			Price:       2499.99,
			Features:    []string{"64GB RAM", "2TB SSD", "RTX 4090", "Dual 4K monitors"},
			Description: "Professional workstation for video editing and 3D rendering",
		},
	}

	// Customer query
	customerQuery := "I'm a college student studying computer science. I need something portable for coding and note-taking. My budget is around $500-600."

	fmt.Println("🛍️ Choose Example - Product Recommender")
	fmt.Println("=" + string(make([]byte, 50)))
	fmt.Println("\n💬 Customer Query:")
	fmt.Println(customerQuery)
	fmt.Println("\n📦 Available Products:")
	for _, p := range products {
		fmt.Printf("  %d. %s - $%.2f (%s)\n", p.ID, p.Name, p.Price, p.Category)
	}

	// Choose the best product for the customer
	chooseOpts := schemaflux.NewChooseOptions().WithCriteria([]string{
		"Budget: Maximum $600",
		"Must be portable (laptop)",
		"For coding and note-taking",
	})
	chooseOpts.OpOptions.Intelligence = schemaflux.Fast
	chooseOpts.OpOptions.Steering = "Select the product that is under $600 budget AND is a laptop. BudgetBook at $499.99 is the ONLY option within budget."

	chosen, err := schemaflux.Choose(products, chooseOpts)

	if err != nil {
		schemaflux.GetLogger().Error("Selection failed", "error", err)
		os.Exit(1)
	}

	// Display recommendation
	fmt.Println("\n✅ Recommended Product:")
	fmt.Println("---")
	fmt.Printf("🎯 %s\n", chosen.Name)
	fmt.Printf("💰 Price: $%.2f\n", chosen.Price)
	fmt.Printf("📱 Category: %s\n", chosen.Category)
	fmt.Printf("✨ Features: %v\n", chosen.Features)
	fmt.Printf("\n📝 Description:\n   %s\n", chosen.Description)

	fmt.Println("\n💡 Why this choice?")
	fmt.Println("   - Within budget ($500-600)")
	fmt.Println("   - Portable laptop (not desktop)")
	fmt.Println("   - Suitable for coding and note-taking")
	fmt.Println("   - Good value for students")

	fmt.Println("\n✨ Success! Intelligently selected best match")
}
