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
	if err := godotenv.Load(); err == nil {
		return
	}
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
// USE CASE 1: Customer 360 Profile (CRM + Support + Analytics)
// ============================================================

// Customer360 - unified customer view
type Customer360 struct {
	CustomerID     string   `json:"customer_id"`
	FullName       string   `json:"full_name"`
	Email          string   `json:"email"`
	Phone          string   `json:"phone"`
	AccountStatus  string   `json:"account_status"`
	TotalSpend     float64  `json:"total_spend"`
	OrderCount     int      `json:"order_count"`
	AvgOrderValue  float64  `json:"avg_order_value"`
	SupportTickets int      `json:"support_tickets"`
	NPS            int      `json:"nps_score"`
	Segment        string   `json:"customer_segment"`
	Tags           []string `json:"tags"`
}

// ============================================================
// USE CASE 2: Investment Research Report
// ============================================================

// InvestmentReport - composed from multiple research sources
type InvestmentReport struct {
	Ticker       string  `json:"ticker"`
	CompanyName  string  `json:"company_name"`
	Sector       string  `json:"sector"`
	CurrentPrice float64 `json:"current_price"`
	TargetPrice  float64 `json:"target_price"`
	Rating       string  `json:"rating"`
	MarketCap    float64 `json:"market_cap_billions"`
	PE           float64 `json:"pe_ratio"`
	Revenue      float64 `json:"revenue_billions"`
	GrowthRate   float64 `json:"yoy_growth_pct"`
	Thesis       string  `json:"investment_thesis"`
	Risks        string  `json:"key_risks"`
}

// ============================================================
// USE CASE 3: Product Catalog Entry
// ============================================================

// ProductCatalog - assembled from multiple systems
type ProductCatalog struct {
	SKU          string   `json:"sku"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Category     string   `json:"category"`
	Price        float64  `json:"price"`
	Cost         float64  `json:"cost"`
	Margin       float64  `json:"margin_pct"`
	StockLevel   int      `json:"stock_level"`
	Warehouse    string   `json:"warehouse"`
	Supplier     string   `json:"supplier"`
	LeadTimeDays int      `json:"lead_time_days"`
	Tags         []string `json:"tags"`
}

func main() {
	loadEnv()

	if err := schemaflux.InitWithEnv(); err != nil {
		fmt.Printf("Init failed: %v\n", err)
		return
	}

	fmt.Println("=== Compose Example ===")
	fmt.Println("Building complex objects from multiple parts/sources")

	// ============================================================
	// USE CASE 1: Customer 360 Profile
	// Scenario: Combine data from CRM, support, and analytics systems
	// ============================================================
	fmt.Println("\n--- Use Case 1: Customer 360 Profile ---")

	// From CRM system
	crmData := map[string]any{
		"customer_id":    "CUST-88421",
		"full_name":      "Sarah Johnson",
		"email":          "sarah.j@email.com",
		"phone":          "+1-555-234-5678",
		"account_status": "premium",
	}

	// From e-commerce/orders system
	ordersData := map[string]any{
		"customer_id":     "CUST-88421",
		"total_spend":     12450.00,
		"order_count":     47,
		"avg_order_value": 264.89,
	}

	// From support system
	supportData := map[string]any{
		"customer_id":     "CUST-88421",
		"support_tickets": 3,
		"nps_score":       9,
		"tags":            []string{"loyal", "tech-savvy"},
	}

	// From analytics/segmentation
	analyticsData := map[string]any{
		"customer_id":      "CUST-88421",
		"customer_segment": "VIP",
		"tags":             []string{"high-value", "early-adopter"}, // Overlaps with support!
	}

	custParts := []any{crmData, ordersData, supportData, analyticsData}

	custResult, err := schemaflux.Assemble[Customer360](custParts, schemaflux.ComposeOptions{
		MergeStrategy: "smart",
		Intelligence:  types.Smart,
		Steering:      "Combine all tags from different sources. Use CRM as primary for contact info.",
	})
	if err != nil {
		fmt.Printf("Customer 360 composition failed: %v\n", err)
	} else {
		fmt.Printf("Sources: CRM, Orders, Support, Analytics\n\n")
		fmt.Printf("Composed Customer 360:\n")
		fmt.Printf("  ID: %s\n", custResult.Composed.CustomerID)
		fmt.Printf("  Name: %s\n", custResult.Composed.FullName)
		fmt.Printf("  Email: %s\n", custResult.Composed.Email)
		fmt.Printf("  Phone: %s\n", custResult.Composed.Phone)
		fmt.Printf("  Status: %s\n", custResult.Composed.AccountStatus)
		fmt.Printf("  Total Spend: $%.2f\n", custResult.Composed.TotalSpend)
		fmt.Printf("  Order Count: %d\n", custResult.Composed.OrderCount)
		fmt.Printf("  Avg Order: $%.2f\n", custResult.Composed.AvgOrderValue)
		fmt.Printf("  Support Tickets: %d\n", custResult.Composed.SupportTickets)
		fmt.Printf("  NPS Score: %d\n", custResult.Composed.NPS)
		fmt.Printf("  Segment: %s\n", custResult.Composed.Segment)
		fmt.Printf("  Tags: %v\n", custResult.Composed.Tags)
		fmt.Printf("\nConflicts Resolved: %d\n", custResult.ConflictsResolved)
		fmt.Printf("Completeness: %.0f%%\n", custResult.Completeness*100)
	}

	// ============================================================
	// USE CASE 2: Investment Research Report
	// Scenario: Combine analyst reports, market data, financials
	// ============================================================
	fmt.Println("\n--- Use Case 2: Investment Research Report ---")

	// From market data feed
	marketData := map[string]any{
		"ticker":              "NVDA",
		"company_name":        "NVIDIA Corporation",
		"current_price":       142.50,
		"market_cap_billions": 3500.0,
	}

	// From analyst report
	analystReport := map[string]any{
		"ticker":            "NVDA",
		"rating":            "Strong Buy",
		"target_price":      175.00,
		"investment_thesis": "Dominant position in AI/ML accelerators with expanding data center TAM",
		"key_risks":         "Customer concentration, China export restrictions, cyclical demand",
	}

	// From financial statements
	financials := map[string]any{
		"ticker":           "NVDA",
		"sector":           "Technology",
		"revenue_billions": 60.9,
		"yoy_growth_pct":   122.0,
		"pe_ratio":         65.5,
	}

	investParts := []any{marketData, analystReport, financials}

	investResult, err := schemaflux.Assemble[InvestmentReport](investParts, schemaflux.ComposeOptions{
		MergeStrategy: "smart",
		Intelligence:  types.Smart,
		Steering:      "Market data is real-time and most current for price. Analyst report for qualitative assessment.",
	})
	if err != nil {
		fmt.Printf("Investment report composition failed: %v\n", err)
	} else {
		fmt.Printf("Sources: Market Data, Analyst Report, Financials\n\n")
		fmt.Printf("Composed Investment Report:\n")
		fmt.Printf("  Ticker: %s - %s\n", investResult.Composed.Ticker, investResult.Composed.CompanyName)
		fmt.Printf("  Sector: %s\n", investResult.Composed.Sector)
		fmt.Printf("  Current Price: $%.2f\n", investResult.Composed.CurrentPrice)
		fmt.Printf("  Target Price: $%.2f\n", investResult.Composed.TargetPrice)
		fmt.Printf("  Rating: %s\n", investResult.Composed.Rating)
		fmt.Printf("  Market Cap: $%.1fB\n", investResult.Composed.MarketCap)
		fmt.Printf("  P/E Ratio: %.1f\n", investResult.Composed.PE)
		fmt.Printf("  Revenue: $%.1fB\n", investResult.Composed.Revenue)
		fmt.Printf("  YoY Growth: %.0f%%\n", investResult.Composed.GrowthRate)
		fmt.Printf("  Thesis: %s\n", investResult.Composed.Thesis)
		fmt.Printf("  Risks: %s\n", investResult.Composed.Risks)
		fmt.Printf("\nCompleteness: %.0f%%\n", investResult.Completeness*100)
	}

	// ============================================================
	// USE CASE 3: Product Catalog Entry
	// Scenario: Combine PIM, inventory, and procurement data
	// ============================================================
	fmt.Println("\n--- Use Case 3: Product Catalog Entry ---")

	// From Product Information Management (PIM)
	pimData := map[string]any{
		"sku":         "ELEC-LAPTOP-001",
		"name":        "ProBook 15 Laptop",
		"description": "15.6\" business laptop with Intel i7, 16GB RAM, 512GB SSD",
		"category":    "Electronics > Computers > Laptops",
		"tags":        []string{"business", "portable", "high-performance"},
	}

	// From inventory/warehouse system
	inventoryData := map[string]any{
		"sku":         "ELEC-LAPTOP-001",
		"stock_level": 145,
		"warehouse":   "Warehouse East",
	}

	// From procurement/supplier system
	procurementData := map[string]any{
		"sku":            "ELEC-LAPTOP-001",
		"cost":           650.00,
		"price":          899.99,
		"supplier":       "TechDistributor Inc.",
		"lead_time_days": 14,
	}

	productParts := []any{pimData, inventoryData, procurementData}

	productResult, err := schemaflux.Assemble[ProductCatalog](productParts, schemaflux.ComposeOptions{
		MergeStrategy: "smart",
		FillGaps:      true, // Calculate margin if not provided
		Intelligence:  types.Smart,
		Steering:      "Calculate margin_pct from price and cost: ((price-cost)/price)*100",
	})
	if err != nil {
		fmt.Printf("Product catalog composition failed: %v\n", err)
	} else {
		fmt.Printf("Sources: PIM, Inventory, Procurement\n\n")
		fmt.Printf("Composed Product Catalog Entry:\n")
		fmt.Printf("  SKU: %s\n", productResult.Composed.SKU)
		fmt.Printf("  Name: %s\n", productResult.Composed.Name)
		fmt.Printf("  Description: %s\n", productResult.Composed.Description)
		fmt.Printf("  Category: %s\n", productResult.Composed.Category)
		fmt.Printf("  Price: $%.2f\n", productResult.Composed.Price)
		fmt.Printf("  Cost: $%.2f\n", productResult.Composed.Cost)
		fmt.Printf("  Margin: %.1f%%\n", productResult.Composed.Margin)
		fmt.Printf("  Stock: %d units\n", productResult.Composed.StockLevel)
		fmt.Printf("  Warehouse: %s\n", productResult.Composed.Warehouse)
		fmt.Printf("  Supplier: %s\n", productResult.Composed.Supplier)
		fmt.Printf("  Lead Time: %d days\n", productResult.Composed.LeadTimeDays)
		fmt.Printf("  Tags: %v\n", productResult.Composed.Tags)
		fmt.Printf("\nGaps Filled: %v\n", productResult.GapsFilled)
		fmt.Printf("Completeness: %.0f%%\n", productResult.Completeness*100)
	}

	fmt.Println("\n=== Compose Example Complete ===")
}
