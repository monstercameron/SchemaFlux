package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

func main() {
	if err := run(context.Background(), io.Writer(os.Stdout)); err != nil {
		fmt.Fprintf(os.Stderr, "verify example failed: %v\nCheck your provider configuration and try again.\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== Verify Example ===")
	fmt.Fprintln(out, "Verifies claims against facts, checks logic consistency, and validates sources")
	fmt.Fprintln(out)

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
		Reference: map[string]any{"actual_uptime_q4_2024": 99.7, "contracted_uptime_guarantee": 99.9, "security_incidents_q4_2024": 2},
	}
	fmt.Fprintln(out, "--- Use Case 1: Contract Compliance Audit ---")
	fmt.Fprintf(out, "INPUT: ContractAuditInput{ContractName: %q, Claims: %d, Reference: %v}\n\n", input1.ContractName, len(input1.Claims), input1.Reference)
	// Verify reviews text in v2, so encode the structured claim and pass its reference data as Sources.
	text1, err := json.Marshal(input1)
	if err != nil {
		return fmt.Errorf("encode contract audit input: %w", err)
	}
	result1, err := schemaflux.Verify(ctx, string(text1), schemaflux.VerifyOptions{Sources: []any{input1.Reference}, Strictness: "strict"})
	if err != nil {
		return fmt.Errorf("contract verification: %w", err)
	}
	printJudgment(out, result1.Judgment, result1.ModelTrustScore)
	fmt.Fprintln(out)

	type ResumeInput struct {
		CandidateName   string         `json:"candidate_name"`
		Claims          []string       `json:"resume_claims"`
		BackgroundCheck map[string]any `json:"background_check_data"`
	}
	input2 := ResumeInput{
		CandidateName:   "John Smith",
		Claims:          []string{"BS in Computer Science from MIT, graduated 2018", "5 years experience as Software Engineer at Google", "Led team of 12 engineers on search infrastructure"},
		BackgroundCheck: map[string]any{"education_school": "MIT", "education_degree": "BS Computer Science", "graduation_year": 2019, "employer": "Google", "title": "Software Engineer", "employment_years": 5, "team_size_managed": 4},
	}
	fmt.Fprintln(out, "--- Use Case 2: Resume Verification ---")
	fmt.Fprintf(out, "INPUT: ResumeInput{CandidateName: %q, Claims: %d, BackgroundCheck: %v}\n\n", input2.CandidateName, len(input2.Claims), input2.BackgroundCheck)
	text2, err := json.Marshal(input2)
	if err != nil {
		return fmt.Errorf("encode resume input: %w", err)
	}
	result2, err := schemaflux.Verify(ctx, string(text2), schemaflux.VerifyOptions{Sources: []any{input2.BackgroundCheck}, Strictness: "strict"})
	if err != nil {
		return fmt.Errorf("resume verification: %w", err)
	}
	printJudgment(out, result2.Judgment, result2.ModelTrustScore)
	fmt.Fprintln(out)

	type MarketingInput struct {
		ProductName string         `json:"product_name"`
		AdClaims    []string       `json:"advertising_claims"`
		TestData    map[string]any `json:"clinical_test_data"`
	}
	input3 := MarketingInput{
		ProductName: "SuperVitamin Plus",
		AdClaims:    []string{"Clinically proven to boost energy by 50%", "100% natural ingredients", "Recommended by 9 out of 10 doctors"},
		TestData:    map[string]any{"energy_improvement_percent": 28, "natural_ingredient_percent": 85, "doctor_recommendation_rate": 0.42, "sample_size": 120},
	}
	fmt.Fprintln(out, "--- Use Case 3: Marketing Claims Compliance ---")
	fmt.Fprintf(out, "INPUT: MarketingInput{ProductName: %q, AdClaims: %d, TestData: %v}\n\n", input3.ProductName, len(input3.AdClaims), input3.TestData)
	text3, err := json.Marshal(input3)
	if err != nil {
		return fmt.Errorf("encode marketing input: %w", err)
	}
	result3, err := schemaflux.Verify(ctx, string(text3), schemaflux.VerifyOptions{Sources: []any{input3.TestData}, Strictness: "strict"})
	if err != nil {
		return fmt.Errorf("marketing verification: %w", err)
	}
	printJudgment(out, result3.Judgment, result3.ModelTrustScore)
	fmt.Fprintln(out, "\n=== Verify Example Complete ===")
	return nil
}

func printJudgment[T any](out io.Writer, result schemaflux.Judgment[T], modelTrustScore float64) {
	fmt.Fprintln(out, "OUTPUT: Judgment{")
	fmt.Fprintf(out, "  Verdict: %s,\n", result.Verdict)
	if result.Summary != "" {
		fmt.Fprintf(out, "  Summary: %q,\n", result.Summary)
	}
	fmt.Fprintln(out, "  Issues:")
	for _, issue := range result.Issues {
		fmt.Fprintf(out, "    {%q, %q, %q},\n", issue.Subject, issue.Category, issue.Severity)
	}
	fmt.Fprintln(out, "  -- claimed by the model, not measured --")
	fmt.Fprintf(out, "  ModelTrustScore: %.2f,\n", modelTrustScore)
	fmt.Fprintln(out, "}")
}
