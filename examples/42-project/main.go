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
// USE CASE 1: API Response Projection (Full → Public)
// ============================================================

// InternalOrder - full order from database
type InternalOrder struct {
	OrderID       string  `json:"order_id"`
	CustomerID    string  `json:"customer_id"`
	CustomerEmail string  `json:"customer_email"`
	CustomerSSN   string  `json:"customer_ssn"`
	Items         int     `json:"item_count"`
	Subtotal      float64 `json:"subtotal"`
	Tax           float64 `json:"tax"`
	ShippingCost  float64 `json:"shipping_cost"`
	Total         float64 `json:"total"`
	CostOfGoods   float64 `json:"cost_of_goods"` // Internal margin data
	Margin        float64 `json:"margin_pct"`    // Internal margin data
	Status        string  `json:"status"`
	CreatedAt     string  `json:"created_at"`
	InternalNotes string  `json:"internal_notes"`
}

// PublicOrderResponse - what customers see
type PublicOrderResponse struct {
	OrderNumber   string  `json:"order_number"`
	ItemCount     int     `json:"item_count"`
	Total         float64 `json:"total"`
	Status        string  `json:"status"`
	StatusDisplay string  `json:"status_display"`
	OrderDate     string  `json:"order_date"`
}

// ============================================================
// USE CASE 2: Database → Analytics Schema
// ============================================================

// DBTransaction - raw database record
type DBTransaction struct {
	TxnID        string  `json:"txn_id"`
	UserID       string  `json:"user_id"`
	Amount       float64 `json:"amount"`
	Currency     string  `json:"currency"`
	Timestamp    string  `json:"timestamp"`
	MerchantName string  `json:"merchant_name"`
	MerchantMCC  string  `json:"merchant_mcc"`
	CardLast4    string  `json:"card_last4"`
	CardBIN      string  `json:"card_bin"`
	IPAddress    string  `json:"ip_address"`
	DeviceID     string  `json:"device_id"`
	ResponseCode string  `json:"response_code"`
	AuthCode     string  `json:"auth_code"`
}

// AnalyticsEvent - for analytics pipeline
type AnalyticsEvent struct {
	EventID      string  `json:"event_id"`
	UserHash     string  `json:"user_hash"`
	AmountUSD    float64 `json:"amount_usd"`
	MerchantType string  `json:"merchant_type"`
	EventDate    string  `json:"event_date"`
	DayOfWeek    string  `json:"day_of_week"`
	IsApproved   bool    `json:"is_approved"`
}

// ============================================================
// USE CASE 3: Legacy System Migration
// ============================================================

// LegacyEmployee - old HR system
type LegacyEmployee struct {
	EmpNo      string `json:"emp_no"`
	Fname      string `json:"fname"`
	Lname      string `json:"lname"`
	Mi         string `json:"mi"`
	Dept       string `json:"dept"`
	Title      string `json:"title"`
	HireDate   string `json:"hire_date"`
	TermDate   string `json:"term_date"`
	Salary     int    `json:"salary"`
	Bonus      int    `json:"bonus"`
	MgrEmpNo   string `json:"mgr_emp_no"`
	CostCenter string `json:"cost_center"`
}

// ModernEmployee - new HR platform schema
type ModernEmployee struct {
	EmployeeID string `json:"employee_id"`
	FullName   string `json:"full_name"`
	Department string `json:"department"`
	JobTitle   string `json:"job_title"`
	StartDate  string `json:"start_date"`
	IsActive   bool   `json:"is_active"`
	TotalComp  int    `json:"total_compensation"`
	ManagerID  string `json:"manager_id"`
}

func main() {
	loadEnv()

	if err := schemaflux.InitWithEnv(); err != nil {
		fmt.Printf("Init failed: %v\n", err)
		return
	}

	fmt.Println("=== Project Example ===")
	fmt.Println("Semantic structure transformation between schemas")

	// ============================================================
	// USE CASE 1: API Response Projection
	// Scenario: Strip sensitive/internal data before sending to customer
	// ============================================================
	fmt.Println("\n--- Use Case 1: API Response Projection ---")

	internalOrder := InternalOrder{
		OrderID:       "ORD-2024-88921",
		CustomerID:    "CUST-12345",
		CustomerEmail: "john@example.com",
		CustomerSSN:   "123-45-6789",
		Items:         3,
		Subtotal:      149.97,
		Tax:           12.75,
		ShippingCost:  9.99,
		Total:         172.71,
		CostOfGoods:   89.50,
		Margin:        0.48,
		Status:        "shipped",
		CreatedAt:     "2024-12-01T14:30:00Z",
		InternalNotes: "Expedited by manager - VIP customer",
	}

	orderResult, err := schemaflux.Project[InternalOrder, PublicOrderResponse](internalOrder, schemaflux.ProjectOptions{
		Mappings: map[string]string{
			"order_id":   "order_number",
			"item_count": "item_count",
			"created_at": "order_date",
		},
		Exclude:      []string{"customer_ssn", "cost_of_goods", "margin_pct", "internal_notes", "customer_id", "customer_email"},
		InferMissing: true,
		Intelligence: types.Smart,
		Steering:     "Convert status to human-readable status_display (shipped → 'Shipped - On the way!'). Format order_date as 'Dec 1, 2024'.",
	})
	if err != nil {
		fmt.Printf("Order projection failed: %v\n", err)
	} else {
		fmt.Printf("Internal Order: %s (Margin: %.0f%%, Notes: '%s')\n",
			internalOrder.OrderID, internalOrder.Margin*100, internalOrder.InternalNotes)
		fmt.Printf("\nPublic Response (sensitive data removed):\n")
		fmt.Printf("  Order Number: %s\n", orderResult.Projected.OrderNumber)
		fmt.Printf("  Items: %d\n", orderResult.Projected.ItemCount)
		fmt.Printf("  Total: $%.2f\n", orderResult.Projected.Total)
		fmt.Printf("  Status: %s\n", orderResult.Projected.Status)
		fmt.Printf("  Status Display: %s\n", orderResult.Projected.StatusDisplay)
		fmt.Printf("  Order Date: %s\n", orderResult.Projected.OrderDate)
		fmt.Printf("\nExcluded Fields: %v\n", orderResult.Lost)
		fmt.Printf("ModelConfidence: %.0f%%\n", orderResult.ModelConfidence*100)
	}

	// ============================================================
	// USE CASE 2: Database → Analytics Schema
	// Scenario: Transform transactional data for analytics pipeline
	// ============================================================
	fmt.Println("\n--- Use Case 2: Database → Analytics Schema ---")

	dbTxn := DBTransaction{
		TxnID:        "TXN-2024120588821",
		UserID:       "USR-98765",
		Amount:       156.50,
		Currency:     "USD",
		Timestamp:    "2024-12-05T09:45:30Z",
		MerchantName: "AMAZON.COM",
		MerchantMCC:  "5942",
		CardLast4:    "4532",
		CardBIN:      "411111",
		IPAddress:    "192.168.1.100",
		DeviceID:     "DEV-ABC123",
		ResponseCode: "00",
		AuthCode:     "AUTH789",
	}

	analyticsResult, err := schemaflux.Project[DBTransaction, AnalyticsEvent](dbTxn, schemaflux.ProjectOptions{
		Mappings: map[string]string{
			"txn_id": "event_id",
		},
		Exclude:      []string{"card_last4", "card_bin", "ip_address", "device_id", "auth_code"},
		InferMissing: true,
		Intelligence: types.Smart,
		Steering:     "Hash user_id for privacy (show as 'USR-XXXX'). MCC 5942 = 'Book Stores'. Extract day_of_week from timestamp. ResponseCode '00' = approved.",
	})
	if err != nil {
		fmt.Printf("Analytics projection failed: %v\n", err)
	} else {
		fmt.Printf("Database Record: %s (%s at %s)\n",
			dbTxn.TxnID, dbTxn.MerchantName, dbTxn.Timestamp[:10])
		fmt.Printf("\nAnalytics Event:\n")
		fmt.Printf("  Event ID: %s\n", analyticsResult.Projected.EventID)
		fmt.Printf("  User Hash: %s\n", analyticsResult.Projected.UserHash)
		fmt.Printf("  Amount USD: $%.2f\n", analyticsResult.Projected.AmountUSD)
		fmt.Printf("  Merchant Type: %s\n", analyticsResult.Projected.MerchantType)
		fmt.Printf("  Event Date: %s\n", analyticsResult.Projected.EventDate)
		fmt.Printf("  Day of Week: %s\n", analyticsResult.Projected.DayOfWeek)
		fmt.Printf("  Is Approved: %v\n", analyticsResult.Projected.IsApproved)
		fmt.Printf("\nField Mappings:\n")
		for _, m := range analyticsResult.Mappings {
			fmt.Printf("  %s → %s (%s)\n", m.SourceField, m.TargetField, m.Method)
		}
	}

	// ============================================================
	// USE CASE 3: Legacy System Migration
	// Scenario: Transform legacy HR records to modern schema
	// ============================================================
	fmt.Println("\n--- Use Case 3: Legacy System Migration ---")

	legacyEmp := LegacyEmployee{
		EmpNo:      "E00421",
		Fname:      "Sarah",
		Lname:      "Johnson",
		Mi:         "M",
		Dept:       "ENGINEERING",
		Title:      "SR SOFTWARE ENGINEER",
		HireDate:   "15-MAR-2019",
		TermDate:   "",
		Salary:     145000,
		Bonus:      20000,
		MgrEmpNo:   "E00102",
		CostCenter: "CC-4500",
	}

	modernResult, err := schemaflux.Project[LegacyEmployee, ModernEmployee](legacyEmp, schemaflux.ProjectOptions{
		Mappings: map[string]string{
			"emp_no":     "employee_id",
			"hire_date":  "start_date",
			"mgr_emp_no": "manager_id",
		},
		InferMissing: true,
		Intelligence: types.Smart,
		Steering:     "Combine fname, mi, lname into full_name ('Sarah M. Johnson'). Empty term_date means is_active=true. Add salary+bonus for total_compensation. Convert dept to title case. Convert start_date to ISO 8601.",
	})
	if err != nil {
		fmt.Printf("Legacy migration failed: %v\n", err)
	} else {
		fmt.Printf("Legacy Record: %s - %s %s (%s)\n",
			legacyEmp.EmpNo, legacyEmp.Fname, legacyEmp.Lname, legacyEmp.Dept)
		fmt.Printf("\nModern Schema:\n")
		fmt.Printf("  Employee ID: %s\n", modernResult.Projected.EmployeeID)
		fmt.Printf("  Full Name: %s\n", modernResult.Projected.FullName)
		fmt.Printf("  Department: %s\n", modernResult.Projected.Department)
		fmt.Printf("  Job Title: %s\n", modernResult.Projected.JobTitle)
		fmt.Printf("  Start Date: %s\n", modernResult.Projected.StartDate)
		fmt.Printf("  Is Active: %v\n", modernResult.Projected.IsActive)
		fmt.Printf("  Total Comp: $%d\n", modernResult.Projected.TotalComp)
		fmt.Printf("  Manager ID: %s\n", modernResult.Projected.ManagerID)
		fmt.Printf("\nInferred Fields: %v\n", modernResult.Inferred)
		fmt.Printf("ModelConfidence: %.0f%%\n", modernResult.ModelConfidence*100)
	}

	fmt.Println("\n=== Project Example Complete ===")
}
