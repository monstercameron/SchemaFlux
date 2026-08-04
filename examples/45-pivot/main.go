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
// USE CASE 1: Sales Pipeline Dashboard (Rows → Columns)
// ============================================================

// SalesRecord - individual sales transaction
type SalesRecord struct {
	Rep     string  `json:"sales_rep"`
	Month   string  `json:"month"`
	Revenue float64 `json:"revenue"`
	Deals   int     `json:"deals_closed"`
}

// RepQuarterly - pivoted quarterly view per rep
type RepQuarterly struct {
	Rep        string  `json:"sales_rep"`
	Jan        float64 `json:"jan_revenue"`
	Feb        float64 `json:"feb_revenue"`
	Mar        float64 `json:"mar_revenue"`
	Q1Total    float64 `json:"q1_total"`
	TotalDeals int     `json:"total_deals"`
}

// ============================================================
// USE CASE 2: E-commerce Order Flattening (Nested → Flat)
// ============================================================

// NestedOrder - API response with nested structure
type NestedOrder struct {
	OrderID  string `json:"order_id"`
	Customer struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"customer"`
	Shipping struct {
		Address string `json:"address"`
		City    string `json:"city"`
		Zip     string `json:"zip"`
		Method  string `json:"method"`
	} `json:"shipping"`
	Payment struct {
		Method string  `json:"method"`
		Amount float64 `json:"amount"`
		Status string  `json:"status"`
	} `json:"payment"`
}

// FlatOrder - flattened for data warehouse
type FlatOrder struct {
	OrderID       string  `json:"order_id"`
	CustomerID    string  `json:"customer_id"`
	CustomerName  string  `json:"customer_name"`
	CustomerEmail string  `json:"customer_email"`
	ShipAddress   string  `json:"ship_address"`
	ShipCity      string  `json:"ship_city"`
	ShipZip       string  `json:"ship_zip"`
	ShipMethod    string  `json:"ship_method"`
	PaymentMethod string  `json:"payment_method"`
	PaymentAmount float64 `json:"payment_amount"`
	PaymentStatus string  `json:"payment_status"`
}

// ============================================================
// USE CASE 3: Survey Response Pivot (Rows → Columns)
// ============================================================

// SurveyResponse - EAV (Entity-Attribute-Value) format
type SurveyResponse struct {
	RespondentID string `json:"respondent_id"`
	Question     string `json:"question"`
	Answer       string `json:"answer"`
}

// RespondentSurvey - one row per respondent
type RespondentSurvey struct {
	RespondentID   string `json:"respondent_id"`
	Satisfaction   string `json:"satisfaction"`
	Recommendation string `json:"would_recommend"`
	Feedback       string `json:"open_feedback"`
	OverallScore   string `json:"overall_score"`
}

func main() {
	loadEnv()

	if err := schemaflux.InitWithEnv(); err != nil {
		fmt.Printf("Init failed: %v\n", err)
		return
	}

	fmt.Println("=== Pivot Example ===")
	fmt.Println("Restructuring data relationships between typed objects")

	// ============================================================
	// USE CASE 1: Sales Pipeline Dashboard
	// Scenario: Transform monthly sales rows into quarterly columns for dashboard
	// ============================================================
	fmt.Println("\n--- Use Case 1: Sales Pipeline Dashboard ---")

	salesData := []SalesRecord{
		{Rep: "Alice", Month: "Jan", Revenue: 45000, Deals: 5},
		{Rep: "Alice", Month: "Feb", Revenue: 52000, Deals: 6},
		{Rep: "Alice", Month: "Mar", Revenue: 48000, Deals: 5},
		{Rep: "Bob", Month: "Jan", Revenue: 38000, Deals: 4},
		{Rep: "Bob", Month: "Feb", Revenue: 41000, Deals: 4},
		{Rep: "Bob", Month: "Mar", Revenue: 55000, Deals: 7},
		{Rep: "Carol", Month: "Jan", Revenue: 62000, Deals: 8},
		{Rep: "Carol", Month: "Feb", Revenue: 58000, Deals: 7},
		{Rep: "Carol", Month: "Mar", Revenue: 71000, Deals: 9},
	}

	fmt.Println("Raw Sales Data (9 rows):")
	fmt.Println("  Rep     | Month | Revenue  | Deals")
	fmt.Println("  --------|-------|----------|------")
	for _, s := range salesData[:4] {
		fmt.Printf("  %-7s | %-5s | $%6.0f  | %d\n", s.Rep, s.Month, s.Revenue, s.Deals)
	}
	fmt.Println("  ... (5 more rows)")

	salesResult, err := schemaflux.Pivot[[]SalesRecord, []RepQuarterly](salesData, schemaflux.PivotOptions{
		PivotOn:      []string{"Month"},
		GroupBy:      []string{"Rep"},
		Aggregate:    "sum",
		Intelligence: types.Smart,
		Steering:     "Pivot months to columns. Sum deals across all months for total_deals. Calculate q1_total as sum of jan+feb+mar revenue.",
	})
	if err != nil {
		fmt.Printf("Sales pivot failed: %v\n", err)
	} else {
		fmt.Println("\nPivoted Dashboard View (3 rows):")
		fmt.Println("  Rep     | Jan     | Feb     | Mar     | Q1 Total  | Deals")
		fmt.Println("  --------|---------|---------|---------|-----------|------")
		for _, r := range salesResult.Pivoted {
			fmt.Printf("  %-7s | $%5.0f  | $%5.0f  | $%5.0f  | $%6.0f   | %d\n",
				r.Rep, r.Jan, r.Feb, r.Mar, r.Q1Total, r.TotalDeals)
		}
		fmt.Printf("\nStats: %d compressions (9 rows → 3 rows)\n", salesResult.Stats.Compressions)
	}

	// ============================================================
	// USE CASE 2: E-commerce Order Flattening
	// Scenario: Flatten nested API response for data warehouse ingestion
	// ============================================================
	fmt.Println("\n--- Use Case 2: E-commerce Order Flattening ---")

	nestedOrder := NestedOrder{
		OrderID: "ORD-2024-78421",
	}
	nestedOrder.Customer.ID = "CUST-1234"
	nestedOrder.Customer.Name = "John Smith"
	nestedOrder.Customer.Email = "john.smith@email.com"
	nestedOrder.Shipping.Address = "123 Oak Street"
	nestedOrder.Shipping.City = "Portland"
	nestedOrder.Shipping.Zip = "97201"
	nestedOrder.Shipping.Method = "Express"
	nestedOrder.Payment.Method = "Credit Card"
	nestedOrder.Payment.Amount = 299.99
	nestedOrder.Payment.Status = "Completed"

	fmt.Println("Nested API Response:")
	fmt.Println("  {")
	fmt.Println("    order_id: ORD-2024-78421")
	fmt.Println("    customer: { id, name, email }")
	fmt.Println("    shipping: { address, city, zip, method }")
	fmt.Println("    payment:  { method, amount, status }")
	fmt.Println("  }")

	flatResult, err := schemaflux.Pivot[NestedOrder, FlatOrder](nestedOrder, schemaflux.PivotOptions{
		Flatten:      true,
		Intelligence: types.Smart,
		Steering:     "Flatten all nested objects. Use prefixes like customer_, ship_, payment_.",
	})
	if err != nil {
		fmt.Printf("Order flatten failed: %v\n", err)
	} else {
		fmt.Println("\nFlattened for Data Warehouse:")
		fmt.Printf("  order_id:        %s\n", flatResult.Pivoted.OrderID)
		fmt.Printf("  customer_id:     %s\n", flatResult.Pivoted.CustomerID)
		fmt.Printf("  customer_name:   %s\n", flatResult.Pivoted.CustomerName)
		fmt.Printf("  customer_email:  %s\n", flatResult.Pivoted.CustomerEmail)
		fmt.Printf("  ship_address:    %s\n", flatResult.Pivoted.ShipAddress)
		fmt.Printf("  ship_city:       %s\n", flatResult.Pivoted.ShipCity)
		fmt.Printf("  ship_zip:        %s\n", flatResult.Pivoted.ShipZip)
		fmt.Printf("  ship_method:     %s\n", flatResult.Pivoted.ShipMethod)
		fmt.Printf("  payment_method:  %s\n", flatResult.Pivoted.PaymentMethod)
		fmt.Printf("  payment_amount:  $%.2f\n", flatResult.Pivoted.PaymentAmount)
		fmt.Printf("  payment_status:  %s\n", flatResult.Pivoted.PaymentStatus)
		fmt.Printf("\nDepth Change: %d (flattened from 3 levels to 1)\n", flatResult.Stats.DepthChange)
		if len(flatResult.DataLoss) > 0 {
			fmt.Printf("Data Loss: %v\n", flatResult.DataLoss)
		} else {
			fmt.Println("Data Loss: none")
		}
	}

	// ============================================================
	// USE CASE 3: Survey Response Pivot
	// Scenario: Transform EAV survey data to one-row-per-respondent
	// ============================================================
	fmt.Println("\n--- Use Case 3: Survey Response Pivot ---")

	surveyData := []SurveyResponse{
		{RespondentID: "R001", Question: "satisfaction", Answer: "Very Satisfied"},
		{RespondentID: "R001", Question: "would_recommend", Answer: "Yes"},
		{RespondentID: "R001", Question: "open_feedback", Answer: "Great product, fast delivery"},
		{RespondentID: "R001", Question: "overall_score", Answer: "9"},
		{RespondentID: "R002", Question: "satisfaction", Answer: "Satisfied"},
		{RespondentID: "R002", Question: "would_recommend", Answer: "Maybe"},
		{RespondentID: "R002", Question: "open_feedback", Answer: "Good but pricey"},
		{RespondentID: "R002", Question: "overall_score", Answer: "7"},
	}

	fmt.Println("EAV Format (8 rows):")
	fmt.Println("  RespondentID | Question        | Answer")
	fmt.Println("  -------------|-----------------|------------------")
	for _, s := range surveyData[:4] {
		fmt.Printf("  %-12s | %-15s | %s\n", s.RespondentID, s.Question, s.Answer)
	}
	fmt.Println("  ... (4 more rows)")

	surveyResult, err := schemaflux.Pivot[[]SurveyResponse, []RespondentSurvey](surveyData, schemaflux.PivotOptions{
		PivotOn:      []string{"Question"},
		GroupBy:      []string{"RespondentID"},
		Aggregate:    "first",
		Intelligence: types.Smart,
		Steering:     "Pivot questions to become columns. Each respondent becomes one row.",
	})
	if err != nil {
		fmt.Printf("Survey pivot failed: %v\n", err)
	} else {
		fmt.Println("\nPivoted Survey (2 rows):")
		fmt.Println("  ID   | Satisfaction    | Recommend | Feedback                  | Score")
		fmt.Println("  -----|-----------------|-----------|---------------------------|------")
		for _, r := range surveyResult.Pivoted {
			feedback := r.Feedback
			if len(feedback) > 25 {
				feedback = feedback[:22] + "..."
			}
			fmt.Printf("  %-4s | %-15s | %-9s | %-25s | %s\n",
				r.RespondentID, r.Satisfaction, r.Recommendation, feedback, r.OverallScore)
		}
		fmt.Printf("\nCompression: 8 EAV rows → 2 respondent rows\n")
	}

	fmt.Println("\n=== Pivot Example Complete ===")
}
