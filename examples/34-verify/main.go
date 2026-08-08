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

	// VerifyWithModel, not Verify: the name says a model produced this
	// verdict, rather than the library having checked anything.
	result1, err := ops.VerifyWithModel(input1, opts)
	if err != nil {
		log.Fatalf("Verification failed: %v", err)
	}

	printJudgment(result1)
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

	result2, err := ops.VerifyWithModel(input2, opts2)
	if err != nil {
		log.Fatalf("Resume verification failed: %v", err)
	}

	printJudgment(result2)
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

	result3, err := ops.VerifyWithModel(input3, opts3)
	if err != nil {
		log.Fatalf("Marketing verification failed: %v", err)
	}

	printJudgment(result3)

	fmt.Println("\n=== Verify Example Complete ===")
}

// printJudgment renders the shared shape every review operation returns now.
// The verdict and the issues are the finding; the model's own scores are
// printed under a heading that says they are claims, because they were
// produced by the same process that produced the finding being scored.
func printJudgment(result types.JudgmentResult[any]) {
	fmt.Println("OUTPUT: JudgmentResult{")
	fmt.Printf("  Verdict: %s,\n", result.Verdict)
	if result.Summary != "" {
		fmt.Printf("  Summary: %q,\n", truncate(result.Summary, 100))
	}
	fmt.Println("  Issues: []JudgmentIssue{")
	for _, issue := range result.Issues {
		fmt.Printf("    {Subject: %q, Category: %q, Severity: %q, Message: %q},\n",
			truncate(issue.Subject, 45), issue.Category, issue.Severity, truncate(issue.Message, 50))
	}
	fmt.Println("  },")
	fmt.Println("  -- claimed by the model, not measured --")
	fmt.Printf("  ModelConfidence: %.2f,\n", result.ModelConfidence)
	for name, claim := range result.ModelClaims {
		fmt.Printf("  ModelClaims[%q]: %v,\n", name, claim)
	}
	fmt.Println("}")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
