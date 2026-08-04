package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
	"github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

func loadEnv() {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
			godotenv.Load(filepath.Join(dir, ".env"))
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
}

func main() {
	loadEnv()
	schemaflux.InitWithEnv()

	fmt.Println("=== Verify Example ===")
	fmt.Println("Verifies claims against facts, checks logic consistency, and validates sources")
	fmt.Println()

	// ==================== USE CASE 1: Contract Compliance Audit ====================
	fmt.Println("--- Use Case 1: Contract Compliance Audit ---")

	type ContractClaim struct {
		ClaimID   string `json:"claim_id"`
		Statement string `json:"statement"`
		Source    string `json:"source"`
	}

	type ContractAuditInput struct {
		ContractName string          `json:"contract_name"`
		Claims       []ContractClaim `json:"claims_to_verify"`
		Reference    map[string]any  `json:"reference_data"`
	}

	input1 := ContractAuditInput{
		ContractName: "Enterprise SLA Agreement #2024-1147",
		Claims: []ContractClaim{
			{ClaimID: "SLA-001", Statement: "Vendor guarantees 99.9% uptime", Source: "Contract Section 3.2"},
			{ClaimID: "SLA-002", Statement: "Vendor met all uptime requirements for Q4 2024", Source: "Vendor Report"},
			{ClaimID: "SLA-003", Statement: "No security incidents occurred in Q4", Source: "Vendor Report"},
		},
		Reference: map[string]any{
			"actual_uptime_q4_2024":       99.7,
			"contracted_uptime_guarantee": 99.9,
			"security_incidents_q4_2024":  2,
		},
	}

	fmt.Println("INPUT: ContractAuditInput{")
	fmt.Printf("  ContractName: %q,\n", input1.ContractName)
	fmt.Println("  Claims: []ContractClaim{")
	for _, c := range input1.Claims {
		fmt.Printf("    {ClaimID: %q, Statement: %q},\n", c.ClaimID, c.Statement)
	}
	fmt.Println("  },")
	fmt.Println("  Reference: map[string]any{")
	fmt.Printf("    \"actual_uptime_q4_2024\": %.1f,\n", input1.Reference["actual_uptime_q4_2024"])
	fmt.Printf("    \"contracted_uptime_guarantee\": %.1f,\n", input1.Reference["contracted_uptime_guarantee"])
	fmt.Printf("    \"security_incidents_q4_2024\": %d,\n", input1.Reference["security_incidents_q4_2024"])
	fmt.Println("  },")
	fmt.Println("}")
	fmt.Println()

	opts := ops.NewVerifyOptions().
		WithSources([]any{input1.Reference}).
		WithCheckFacts(true).
		WithCheckConsistency(true).
		WithStrictness("strict").
		WithIntelligence(types.Smart)

	result1, err := ops.Verify(input1, opts)
	if err != nil {
		log.Fatalf("Verification failed: %v", err)
	}

	fmt.Println("OUTPUT: VerifyResult{")
	fmt.Printf("  OverallVerdict:    %q,\n", result1.OverallVerdict)
	fmt.Printf("  OverallConfidence: %.2f,\n", result1.OverallConfidence)
	fmt.Printf("  TrustScore:        %.2f,\n", result1.TrustScore)
	fmt.Printf("  Summary:           %q,\n", truncate(result1.Summary, 100))
	fmt.Println("  Claims: []ClaimVerification{")
	for _, c := range result1.Claims {
		fmt.Printf("    {Claim: %q, Verdict: %q, Confidence: %.2f, Corrections: %q},\n",
			truncate(c.Claim, 40), c.Verdict, c.Confidence, truncate(c.Corrections, 50))
	}
	fmt.Println("  },")
	if len(result1.ConsistencyIssues) > 0 {
		fmt.Println("  ConsistencyIssues: []ConsistencyIssue{")
		for _, i := range result1.ConsistencyIssues {
			fmt.Printf("    {Type: %q, Description: %q},\n", i.Type, truncate(i.Description, 60))
		}
		fmt.Println("  },")
	}
	fmt.Println("}")
	fmt.Println()

	// ==================== USE CASE 2: Resume Verification ====================
	fmt.Println("--- Use Case 2: Resume Verification ---")

	type ResumeInput struct {
		CandidateName   string         `json:"candidate_name"`
		Claims          []string       `json:"resume_claims"`
		BackgroundCheck map[string]any `json:"background_check_data"`
	}

	input2 := ResumeInput{
		CandidateName: "John Smith",
		Claims: []string{
			"BS in Computer Science from MIT, graduated 2018",
			"5 years experience as Software Engineer at Google",
			"Led team of 12 engineers on search infrastructure",
		},
		BackgroundCheck: map[string]any{
			"education_school":  "MIT",
			"education_degree":  "BS Computer Science",
			"graduation_year":   2019,
			"employer":          "Google",
			"title":             "Software Engineer",
			"employment_years":  5,
			"team_size_managed": 4,
		},
	}

	fmt.Println("INPUT: ResumeInput{")
	fmt.Printf("  CandidateName: %q,\n", input2.CandidateName)
	fmt.Println("  Claims: []string{")
	for _, c := range input2.Claims {
		fmt.Printf("    %q,\n", c)
	}
	fmt.Println("  },")
	fmt.Println("  BackgroundCheck: map[string]any{")
	fmt.Printf("    \"graduation_year\": %d,\n", input2.BackgroundCheck["graduation_year"])
	fmt.Printf("    \"employment_years\": %d,\n", input2.BackgroundCheck["employment_years"])
	fmt.Printf("    \"team_size_managed\": %d,\n", input2.BackgroundCheck["team_size_managed"])
	fmt.Println("  },")
	fmt.Println("}")
	fmt.Println()

	opts2 := ops.NewVerifyOptions().
		WithSources([]any{input2.BackgroundCheck}).
		WithCheckFacts(true).
		WithStrictness("strict").
		WithIntelligence(types.Smart)

	result2, err := ops.Verify(input2, opts2)
	if err != nil {
		log.Fatalf("Resume verification failed: %v", err)
	}

	fmt.Println("OUTPUT: VerifyResult{")
	fmt.Printf("  OverallVerdict:    %q,\n", result2.OverallVerdict)
	fmt.Printf("  OverallConfidence: %.2f,\n", result2.OverallConfidence)
	fmt.Printf("  TrustScore:        %.2f,\n", result2.TrustScore)
	fmt.Println("  Claims: []ClaimVerification{")
	for _, c := range result2.Claims {
		fmt.Printf("    {Claim: %q, Verdict: %q, Confidence: %.2f, Corrections: %q},\n",
			truncate(c.Claim, 50), c.Verdict, c.Confidence, truncate(c.Corrections, 40))
	}
	fmt.Println("  },")
	fmt.Println("}")
	fmt.Println()

	// ==================== USE CASE 3: Marketing Claims Compliance ====================
	fmt.Println("--- Use Case 3: Marketing Claims Compliance ---")

	type MarketingInput struct {
		ProductName string         `json:"product_name"`
		AdClaims    []string       `json:"advertising_claims"`
		TestData    map[string]any `json:"clinical_test_data"`
	}

	input3 := MarketingInput{
		ProductName: "SuperVitamin Plus",
		AdClaims: []string{
			"Clinically proven to boost energy by 50%",
			"100% natural ingredients",
			"Recommended by 9 out of 10 doctors",
		},
		TestData: map[string]any{
			"energy_improvement_percent": 28,
			"natural_ingredient_percent": 85,
			"doctor_recommendation_rate": 0.42,
			"sample_size":                120,
		},
	}

	fmt.Println("INPUT: MarketingInput{")
	fmt.Printf("  ProductName: %q,\n", input3.ProductName)
	fmt.Println("  AdClaims: []string{")
	for _, c := range input3.AdClaims {
		fmt.Printf("    %q,\n", c)
	}
	fmt.Println("  },")
	fmt.Println("  TestData: map[string]any{")
	fmt.Printf("    \"energy_improvement_percent\": %d,\n", input3.TestData["energy_improvement_percent"])
	fmt.Printf("    \"natural_ingredient_percent\": %d,\n", input3.TestData["natural_ingredient_percent"])
	fmt.Printf("    \"doctor_recommendation_rate\": %.2f,\n", input3.TestData["doctor_recommendation_rate"])
	fmt.Println("  },")
	fmt.Println("}")
	fmt.Println()

	opts3 := ops.NewVerifyOptions().
		WithSources([]any{input3.TestData}).
		WithCheckFacts(true).
		WithStrictness("strict").
		WithIntelligence(types.Smart)

	result3, err := ops.Verify(input3, opts3)
	if err != nil {
		log.Fatalf("Marketing verification failed: %v", err)
	}

	fmt.Println("OUTPUT: VerifyResult{")
	fmt.Printf("  OverallVerdict:    %q,\n", result3.OverallVerdict)
	fmt.Printf("  OverallConfidence: %.2f,\n", result3.OverallConfidence)
	fmt.Printf("  TrustScore:        %.2f,\n", result3.TrustScore)
	fmt.Println("  Claims: []ClaimVerification{")
	for _, c := range result3.Claims {
		fmt.Printf("    {Claim: %q, Verdict: %q, Confidence: %.2f, Corrections: %q},\n",
			truncate(c.Claim, 45), c.Verdict, c.Confidence, truncate(c.Corrections, 40))
	}
	fmt.Println("  },")
	fmt.Println("}")

	fmt.Println("\n=== Verify Example Complete ===")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
