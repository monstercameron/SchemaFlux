package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type salaryTerms struct {
	BaseSalary int `json:"base_salary"`
	RemoteDays int `json:"remote_days"`
	Bonus      int `json:"bonus"`
}

type contractTerms struct {
	Price       float64 `json:"price_per_unit"`
	Quantity    int     `json:"quantity"`
	PaymentDays int     `json:"payment_days"`
}

type acquisitionTerms struct {
	Valuation int `json:"valuation_millions"`
	Earnout   int `json:"earnout_percent"`
	Retention int `json:"retention_months"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Negotiate example: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== Adversarial Negotiation Example ===")
	if err := runSalary(ctx, out); err != nil {
		return err
	}
	if err := runVendor(ctx, out); err != nil {
		return err
	}
	if err := runAcquisition(ctx, out); err != nil {
		return err
	}
	fmt.Fprintln(out, "=== Adversarial Negotiation Example Complete ===")
	return nil
}

func runSalary(ctx context.Context, out io.Writer) error {
	positions := exp.AdversarialContext[salaryTerms]{
		Ours:        exp.AdversarialPosition[salaryTerms]{Position: salaryTerms{BaseSalary: 160000, RemoteDays: 5, Bonus: 20000}},
		Theirs:      exp.AdversarialPosition[salaryTerms]{Position: salaryTerms{BaseSalary: 130000, RemoteDays: 2, Bonus: 5000}},
		OurLeverage: "strong",
	}
	result, err := exp.SettleAdversarial(ctx, positions, exp.SettleAdversarialOptions{Options: schemaflux.Options{
		Tier: schemaflux.TierSmart, Steering: "Keep remote days firm; salary is more flexible.",
	}})
	if err != nil {
		return fmt.Errorf("salary negotiation: %w", err)
	}
	fmt.Fprintf(out, "Salary deal reached: %t; base $%d; remote days %d\n", result.DealReached, result.Deal.BaseSalary, result.Deal.RemoteDays)
	return nil
}

func runVendor(ctx context.Context, out io.Writer) error {
	positions := exp.AdversarialContext[contractTerms]{
		Ours:        exp.AdversarialPosition[contractTerms]{Position: contractTerms{Price: 45, Quantity: 500, PaymentDays: 60}},
		Theirs:      exp.AdversarialPosition[contractTerms]{Position: contractTerms{Price: 65, Quantity: 1000, PaymentDays: 30}},
		OurLeverage: "strong",
	}
	result, err := exp.SettleAdversarial(ctx, positions, exp.SettleAdversarialOptions{Options: schemaflux.Options{
		Tier: schemaflux.TierSmart, Steering: "The seller wants to close this quarter; price is negotiable.",
	}})
	if err != nil {
		return fmt.Errorf("vendor negotiation: %w", err)
	}
	fmt.Fprintf(out, "Vendor deal reached: %t; unit price $%.2f; quantity %d\n", result.DealReached, result.Deal.Price, result.Deal.Quantity)
	return nil
}

func runAcquisition(ctx context.Context, out io.Writer) error {
	positions := exp.AdversarialContext[acquisitionTerms]{
		Ours:        exp.AdversarialPosition[acquisitionTerms]{Position: acquisitionTerms{Valuation: 80, Earnout: 30, Retention: 24}},
		Theirs:      exp.AdversarialPosition[acquisitionTerms]{Position: acquisitionTerms{Valuation: 120, Earnout: 10, Retention: 6}},
		OurLeverage: "weak", Relationship: "competitive",
	}
	result, err := exp.SettleAdversarial(ctx, positions, exp.SettleAdversarialOptions{Options: schemaflux.Options{
		Tier: schemaflux.TierSmart, Steering: "The target has competing bidders.",
	}})
	if err != nil {
		return fmt.Errorf("acquisition negotiation: %w", err)
	}
	fmt.Fprintf(out, "Acquisition deal reached: %t; valuation $%dM; retention %d months\n", result.DealReached, result.Deal.Valuation, result.Deal.Retention)
	return nil
}
