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
// USE CASE 1: PCI-DSS Compliance Audit (Payment Data)
// ============================================================

// PaymentRecord for PCI compliance check
type PaymentRecord struct {
	TransactionID  string  `json:"transaction_id"`
	CardNumber     string  `json:"card_number"`
	CVV            string  `json:"cvv"`
	ExpiryDate     string  `json:"expiry_date"`
	CardholderName string  `json:"cardholder_name"`
	Amount         float64 `json:"amount"`
	MerchantID     string  `json:"merchant_id"`
	AuthCode       string  `json:"auth_code"`
	StorageType    string  `json:"storage_type"`
}

// ============================================================
// USE CASE 2: GDPR Data Audit (User Data)
// ============================================================

// UserProfile for GDPR compliance
type UserProfile struct {
	UserID         string `json:"user_id"`
	Email          string `json:"email"`
	FullName       string `json:"full_name"`
	DateOfBirth    string `json:"date_of_birth"`
	Nationality    string `json:"nationality"`
	ConsentGiven   bool   `json:"consent_given"`
	ConsentDate    string `json:"consent_date"`
	DataRetention  string `json:"data_retention"`
	LastAccessedBy string `json:"last_accessed_by"`
	AccessLog      string `json:"access_log"`
}

// ============================================================
// USE CASE 3: Financial Consistency Audit
// ============================================================

// QuarterlyFinancials for consistency check
type QuarterlyFinancials struct {
	Period      string  `json:"period"`
	Revenue     float64 `json:"revenue"`
	COGS        float64 `json:"cost_of_goods_sold"`
	GrossProfit float64 `json:"gross_profit"`
	OpEx        float64 `json:"operating_expenses"`
	NetIncome   float64 `json:"net_income"`
	TaxRate     float64 `json:"tax_rate"`
	TaxPaid     float64 `json:"tax_paid"`
	CashFlow    float64 `json:"cash_flow"`
}

func main() {
	loadEnv()

	if err := schemaflux.InitWithEnv(); err != nil {
		fmt.Printf("Init failed: %v\n", err)
		return
	}

	fmt.Println("=== Audit Example ===")
	fmt.Println("Deep inspection for compliance, security, and consistency")

	// ============================================================
	// USE CASE 1: PCI-DSS Compliance Audit
	// Scenario: Audit payment records for PCI-DSS violations
	// ============================================================
	fmt.Println("\n--- Use Case 1: PCI-DSS Compliance Audit ---")

	// Intentionally non-compliant payment record
	paymentRecord := PaymentRecord{
		TransactionID:  "TXN-2024-88821",
		CardNumber:     "4532123456789012", // VIOLATION: Full PAN stored!
		CVV:            "123",              // VIOLATION: CVV stored!
		ExpiryDate:     "12/2025",
		CardholderName: "John A. Smith",
		Amount:         1499.99,
		MerchantID:     "MERCH-001",
		AuthCode:       "AUTH-789456",
		StorageType:    "plain_text", // VIOLATION: Not encrypted!
	}

	pciResult, err := schemaflux.AuditWithModel[PaymentRecord](paymentRecord, schemaflux.AuditOptions{
		Policies: []string{
			"Full card number (PAN) must not be stored unmasked - only last 4 digits allowed",
			"CVV/CVC must never be stored after authorization",
			"Card data must be encrypted at rest (not plain_text)",
			"Expiry dates should be stored in MM/YY or YYYY-MM format",
		},
		Categories:   []string{"security", "compliance", "pci-dss"},
		Threshold:    0.5,
		Deep:         true,
		Intelligence: types.Smart,
	})
	if err != nil {
		fmt.Printf("PCI audit failed: %v\n", err)
	} else {
		fmt.Printf("PCI-DSS Audit Results:\n")
		fmt.Printf("  Verdict: %s\n", pciResult.Verdict)
		fmt.Printf("  Total Findings: %d\n", len(pciResult.Issues))
		fmt.Printf("\nFindings:\n")
		for _, issue := range pciResult.Issues {
			fmt.Printf("  %s [%s] %s\n", severityLabel(issue.Severity), issue.Subject, issue.Message)
			if issue.Suggestion != "" {
				fmt.Printf("    → %s\n", issue.Suggestion)
			}
		}
	}

	// ============================================================
	// USE CASE 2: GDPR Data Audit
	// Scenario: Audit user data for GDPR compliance issues
	// ============================================================
	fmt.Println("\n--- Use Case 2: GDPR Data Audit ---")

	userProfile := UserProfile{
		UserID:         "USR-EU-12345",
		Email:          "marie.dupont@email.fr",
		FullName:       "Marie Dupont",
		DateOfBirth:    "1992-06-15",
		Nationality:    "French",
		ConsentGiven:   false,        // VIOLATION: No consent!
		ConsentDate:    "",           // VIOLATION: No consent date!
		DataRetention:  "indefinite", // VIOLATION: Must have limit!
		LastAccessedBy: "admin@company.com",
		AccessLog:      "", // VIOLATION: No access log!
	}

	gdprResult, err := schemaflux.AuditWithModel[UserProfile](userProfile, schemaflux.AuditOptions{
		Policies: []string{
			"Explicit consent must be obtained for personal data processing",
			"Consent date must be recorded",
			"Data retention must have a defined period (not indefinite)",
			"Access to personal data must be logged",
			"EU citizen data requires GDPR compliance",
		},
		Categories:   []string{"compliance", "gdpr", "privacy"},
		Threshold:    0.3,
		Intelligence: types.Smart,
	})
	if err != nil {
		fmt.Printf("GDPR audit failed: %v\n", err)
	} else {
		fmt.Printf("GDPR Audit Results:\n")
		fmt.Printf("  Verdict: %s\n", gdprResult.Verdict)
		fmt.Printf("  Total Findings: %d\n", len(gdprResult.Issues))

		byCategory := map[string]int{}
		for _, issue := range gdprResult.Issues {
			byCategory[issue.Category]++
		}
		fmt.Printf("\nBy Category:\n")
		for cat, count := range byCategory {
			fmt.Printf("  %s: %d\n", cat, count)
		}

		fmt.Printf("\nCritical Findings:\n")
		for _, issue := range gdprResult.Issues {
			if issue.Severity != "critical" && issue.Severity != "error" {
				continue
			}
			fmt.Printf("  [%s] %s\n", issue.Subject, issue.Message)
			if issue.Suggestion != "" {
				fmt.Printf("    Fix: %s\n", issue.Suggestion)
			}
		}
	}

	// ============================================================
	// USE CASE 3: Financial Consistency Audit
	// Scenario: Audit quarterly financials for calculation errors
	// ============================================================
	fmt.Println("\n--- Use Case 3: Financial Consistency Audit ---")

	financials := QuarterlyFinancials{
		Period:      "Q4 2024",
		Revenue:     10000000,
		COGS:        4000000,
		GrossProfit: 5500000, // ERROR: Should be 6M (10M - 4M)
		OpEx:        2000000,
		NetIncome:   3000000, // ERROR: Should be 4M (6M - 2M)
		TaxRate:     0.25,
		TaxPaid:     1200000, // ERROR: 25% of 4M = 1M, not 1.2M
		CashFlow:    2800000,
	}

	finResult, err := schemaflux.AuditWithModel[QuarterlyFinancials](financials, schemaflux.AuditOptions{
		Policies: []string{
			"Gross Profit must equal Revenue minus COGS",
			"Net Income before tax must equal Gross Profit minus Operating Expenses",
			"Tax Paid must equal Net Income multiplied by Tax Rate",
			"All financial figures must be non-negative",
		},
		Categories:   []string{"consistency", "accuracy", "financial"},
		Threshold:    0.4,
		Intelligence: types.Smart,
		Steering:     "Calculate expected values and compare. Report discrepancies with expected vs actual values.",
	})
	if err != nil {
		fmt.Printf("Financial audit failed: %v\n", err)
	} else {
		fmt.Printf("Financial Consistency Audit (%s):\n", financials.Period)
		fmt.Printf("  Verdict: %s\n", finResult.Verdict)
		fmt.Printf("  Consistency Issues: %d\n", len(finResult.Issues))
		fmt.Printf("\nDiscrepancies Found:\n")
		for _, issue := range finResult.Issues {
			fmt.Printf("  [%s] %s\n", issue.Subject, issue.Message)
			for _, evidence := range issue.Evidence {
				fmt.Printf("    Evidence: %s\n", evidence)
			}
			if issue.Suggestion != "" {
				fmt.Printf("    Fix: %s\n", issue.Suggestion)
			}
		}
	}

	fmt.Println("\n=== Audit Example Complete ===")
}

// severityLabel renders the shared four-level severity. Audit used to report
// a 0.0-1.0 float and each caller picked its own thresholds for what "high"
// meant; the levels are mapped once in Go now, so two readers of the same
// audit cannot disagree about whether a finding was critical.
func severityLabel(severity string) string {
	switch severity {
	case "critical":
		return "🔴 CRITICAL"
	case "error":
		return "🟠 HIGH"
	case "warning":
		return "🟡 MEDIUM"
	default:
		return "🔵 LOW"
	}
}
