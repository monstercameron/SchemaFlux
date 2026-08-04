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
// USE CASE 1: Vendor Selection (Procurement)
// ============================================================

// VendorProposal from RFP responses
type VendorProposal struct {
	VendorName     string  `json:"vendor_name"`
	PricePerUnit   float64 `json:"price_per_unit"`
	DeliveryDays   int     `json:"delivery_days"`
	QualityRating  float64 `json:"quality_rating"`
	MinOrderQty    int     `json:"min_order_qty"`
	PaymentTerms   string  `json:"payment_terms"`
	YearsInBiz     int     `json:"years_in_business"`
	HasCertISO9001 bool    `json:"has_iso9001"`
}

// ============================================================
// USE CASE 2: Cloud Provider Selection
// ============================================================

// CloudQuote from different cloud providers
type CloudQuote struct {
	Provider          string   `json:"provider"`
	MonthlyCost       float64  `json:"monthly_cost"`
	UptimeGuarantee   float64  `json:"uptime_guarantee_pct"`
	DataCenterRegions int      `json:"data_center_regions"`
	SupportTier       string   `json:"support_tier"`
	ComplianceCerts   []string `json:"compliance_certs"`
	ContractMonths    int      `json:"contract_months"`
}

// ============================================================
// USE CASE 3: Insurance Claim Decision
// ============================================================

// InsuranceClaim for adjudication
type InsuranceClaim struct {
	ClaimID       string  `json:"claim_id"`
	ClaimantName  string  `json:"claimant_name"`
	ClaimAmount   float64 `json:"claim_amount"`
	PolicyType    string  `json:"policy_type"`
	IncidentDate  string  `json:"incident_date"`
	FiledDate     string  `json:"filed_date"`
	Documentation string  `json:"documentation"`
	PriorClaims   int     `json:"prior_claims_12mo"`
	PolicyActive  bool    `json:"policy_active"`
}

func main() {
	loadEnv()

	if err := schemaflux.InitWithEnv(); err != nil {
		fmt.Printf("Init failed: %v\n", err)
		return
	}

	fmt.Println("=== Arbitrate Example ===")
	fmt.Println("Rule-based decisions with full audit trail")

	// ============================================================
	// USE CASE 1: Vendor Selection
	// Scenario: Select best vendor from RFP responses for manufacturing parts
	// ============================================================
	fmt.Println("\n--- Use Case 1: Vendor Selection (RFP) ---")

	vendors := []VendorProposal{
		{
			VendorName:     "Acme Manufacturing",
			PricePerUnit:   11.50, // Under $12 now
			DeliveryDays:   10,
			QualityRating:  4.2,
			MinOrderQty:    1000,
			PaymentTerms:   "Net 30",
			YearsInBiz:     25,
			HasCertISO9001: true,
		},
		{
			VendorName:     "QuickParts Inc",
			PricePerUnit:   10.75,
			DeliveryDays:   7,
			QualityRating:  3.8, // Below 4.0
			MinOrderQty:    500,
			PaymentTerms:   "Net 15",
			YearsInBiz:     8,
			HasCertISO9001: false, // No ISO
		},
		{
			VendorName:     "GlobalSupply Co",
			PricePerUnit:   11.25,
			DeliveryDays:   21, // Too slow
			QualityRating:  4.5,
			MinOrderQty:    1200,
			PaymentTerms:   "Net 45",
			YearsInBiz:     15,
			HasCertISO9001: true,
		},
	}

	vendorRules := []string{
		"Price per unit must be under $12.00",
		"Delivery time must be 14 days or less",
		"Quality rating must be 4.0 or higher",
		"Must have ISO 9001 certification",
		"Minimum order quantity must be 1500 or less",
	}

	vendorResult, err := schemaflux.Arbitrate[VendorProposal](vendors, schemaflux.ArbitrateOptions{
		Rules:           vendorRules,
		Weights:         []float64{0.25, 0.20, 0.25, 0.15, 0.15},
		RequireAllRules: false,
		Intelligence:    types.Smart,
		Steering:        "Quality and reliability are top priorities. Price is important but not at expense of quality.",
	})
	if err != nil {
		fmt.Printf("Vendor arbitration failed: %v\n", err)
	} else {
		fmt.Printf("Winner: %s (Score: %.0f%%)\n\n", vendorResult.Winner.VendorName, vendorResult.Scores[vendorResult.WinnerIndex]*100)
		fmt.Println("Vendor Scores:")
		for i, v := range vendors {
			score := vendorResult.Scores[i]
			fmt.Printf("  %s: %.0f%%\n", v.VendorName, score*100)
		}
		fmt.Printf("\nDecision: %s\n", vendorResult.Reasoning)
		fmt.Printf("Confidence: %.0f%%\n", vendorResult.Confidence*100)
	}

	// ============================================================
	// USE CASE 2: Cloud Provider Selection
	// Scenario: Select cloud provider for enterprise migration
	// ============================================================
	fmt.Println("\n--- Use Case 2: Cloud Provider Selection ---")

	cloudQuotes := []CloudQuote{
		{
			Provider:          "AWS",
			MonthlyCost:       45000,
			UptimeGuarantee:   99.99,
			DataCenterRegions: 25,
			SupportTier:       "Enterprise",
			ComplianceCerts:   []string{"HIPAA", "SOC2"},
			ContractMonths:    36,
		},
		{
			Provider:          "Azure",
			MonthlyCost:       42000,
			UptimeGuarantee:   99.95,
			DataCenterRegions: 20,
			SupportTier:       "Business", // Not enterprise!
			ComplianceCerts:   []string{"HIPAA", "SOC2"},
			ContractMonths:    24,
		},
	}

	cloudRules := []string{
		"Uptime must be at least 99.95%",
		"Must have HIPAA certification",
		"Cost under $50,000 monthly",
		"Must have Enterprise support tier",
	}

	cloudResult, err := schemaflux.Arbitrate[CloudQuote](cloudQuotes, schemaflux.ArbitrateOptions{
		Rules:           cloudRules,
		RequireAllRules: true, // Healthcare company - HIPAA is mandatory
		Intelligence:    types.Smart,
		Steering:        "We are a healthcare company so HIPAA is non-negotiable. Cost efficiency matters but compliance comes first.",
	})
	if err != nil {
		fmt.Printf("Cloud arbitration failed: %v\n", err)
	} else {
		fmt.Printf("Selected Provider: %s\n", cloudResult.Winner.Provider)
		fmt.Printf("Monthly Cost: $%.0f\n", cloudResult.Winner.MonthlyCost)
		fmt.Printf("Uptime SLA: %.2f%%\n", cloudResult.Winner.UptimeGuarantee)
		fmt.Println("\nEvaluation Details:")
		for _, eval := range cloudResult.Evaluations {
			provider := cloudQuotes[eval.Index].Provider
			status := "✓ QUALIFIED"
			if eval.Disqualified {
				status = "✗ DISQUALIFIED"
			}
			fmt.Printf("  %s: %s (Score: %.0f%%)\n", provider, status, eval.TotalScore*100)
			if eval.Disqualified {
				fmt.Printf("    Reason: %s\n", eval.DisqualifyReason)
			}
		}
		fmt.Printf("\nReasoning: %s\n", cloudResult.Reasoning)
	}

	// ============================================================
	// USE CASE 3: Insurance Claim Decision
	// Scenario: Adjudicate multiple claims with policy rules
	// ============================================================
	fmt.Println("\n--- Use Case 3: Insurance Claim Adjudication ---")

	claims := []InsuranceClaim{
		{
			ClaimID:       "CLM-001",
			ClaimantName:  "Robert Chen",
			ClaimAmount:   15000,
			PolicyType:    "Auto",
			IncidentDate:  "2024-11-15",
			FiledDate:     "2024-11-18",
			Documentation: "Police report, photos, certified repair estimate",
			PriorClaims:   0,
			PolicyActive:  true,
		},
		{
			ClaimID:       "CLM-002",
			ClaimantName:  "Sarah Miller",
			ClaimAmount:   8500,
			PolicyType:    "Auto",
			IncidentDate:  "2024-10-01",
			FiledDate:     "2024-11-25",
			Documentation: "Self-reported only",
			PriorClaims:   3,
			PolicyActive:  true,
		},
	}

	claimRules := []string{
		"Policy must be active",
		"Filed within 30 days of incident",
		"Must have third-party documentation",
	}

	claimResult, err := schemaflux.Arbitrate[InsuranceClaim](claims, schemaflux.ArbitrateOptions{
		Rules:           claimRules,
		RequireAllRules: true,
		Intelligence:    types.Smart,
		Steering:        "Approve claims that pass all rules. Deny otherwise.",
	})
	if err != nil {
		fmt.Printf("Claim arbitration failed: %v\n", err)
	} else {
		fmt.Println("Claim Adjudication Results:")
		for _, eval := range claimResult.Evaluations {
			claim := claims[eval.Index]
			status := "APPROVED"
			if eval.Disqualified {
				status = "DENIED"
			}
			fmt.Printf("\n  %s - %s ($%.0f) - %s\n",
				claim.ClaimID, claim.ClaimantName, claim.ClaimAmount, status)
			// Show failed rules
			for _, r := range eval.RuleResults {
				if !r.Passed {
					fmt.Printf("    ✗ %s\n", r.Rule)
					if r.Reasoning != "" {
						fmt.Printf("      → %s\n", r.Reasoning)
					}
				}
			}
		}
		if claimResult.Winner.ClaimID != "" {
			fmt.Printf("\nPriority Claim: %s (best documentation, fastest processing)\n",
				claimResult.Winner.ClaimID)
		}
		fmt.Printf("Confidence: %.0f%%\n", claimResult.Confidence*100)
	}

	fmt.Println("\n=== Arbitrate Example Complete ===")
}
