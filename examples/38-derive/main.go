package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type customerActivity struct {
	ID         string  `json:"customer_id"`
	SignupDate string  `json:"signup_date"`
	Orders     int     `json:"total_orders"`
	Spend      float64 `json:"total_spend"`
	LastOrder  string  `json:"last_order_date"`
}
type customerFeatures struct {
	ID           string  `json:"customer_id"`
	TenureDays   int     `json:"tenure_days"`
	AverageOrder float64 `json:"avg_order_value"`
	ChurnRisk    string  `json:"churn_risk"`
}
type propertyListing struct {
	Address    string `json:"address"`
	SquareFeet int    `json:"sq_ft"`
	YearBuilt  int    `json:"year_built"`
	ListPrice  int    `json:"list_price"`
}
type propertyFeatures struct {
	Address            string  `json:"address"`
	PricePerSquareFoot float64 `json:"price_per_sq_ft"`
	PropertyClass      string  `json:"property_class"`
}
type resume struct {
	Name       string `json:"name"`
	Title      string `json:"current_title"`
	Experience string `json:"experience_summary"`
	Years      int    `json:"years_experience"`
}
type candidateProfile struct {
	Name      string   `json:"name"`
	Seniority string   `json:"seniority_level"`
	Skills    []string `json:"skill_categories"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Derive example: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== Derive Example ===")
	activity := customerActivity{ID: "CUST-8847", SignupDate: "2021-03-15", Orders: 47, Spend: 8542.50, LastOrder: "2024-11-28"}
	features, err := exp.Derive[customerFeatures](ctx, activity, exp.DeriveOptions{Options: schemaflux.Options{
		Tier: schemaflux.TierSmart, Steering: "Use 2024-12-05 as today's date; report low, medium, or high churn risk."},
	})
	if err != nil {
		return fmt.Errorf("derive customer features: %w", err)
	}
	fmt.Fprintf(out, "Customer %s: average order $%.2f; churn risk %s\n", features.Derived.ID, features.Derived.AverageOrder, features.Derived.ChurnRisk)

	property := propertyListing{Address: "1842 Maple Drive", SquareFeet: 2850, YearBuilt: 2018, ListPrice: 875000}
	propertyResult, err := exp.Derive[propertyFeatures](ctx, property, exp.DeriveOptions{Options: schemaflux.Options{
		Tier: schemaflux.TierSmart, Steering: "Classify the property and derive price per square foot."},
	})
	if err != nil {
		return fmt.Errorf("derive property features: %w", err)
	}
	fmt.Fprintf(out, "Property %s: $%.2f/sqft; class %s\n", propertyResult.Derived.Address, propertyResult.Derived.PricePerSquareFoot, propertyResult.Derived.PropertyClass)

	profile := resume{Name: "Alex Rivera", Title: "Engineering Manager", Experience: "Led a team building distributed systems", Years: 11}
	candidate, err := exp.Derive[candidateProfile](ctx, profile, exp.DeriveOptions{Options: schemaflux.Options{
		Tier: schemaflux.TierSmart, Steering: "Infer seniority and skill categories from the stated experience."},
	})
	if err != nil {
		return fmt.Errorf("derive candidate profile: %w", err)
	}
	fmt.Fprintf(out, "Candidate %s: %s; skills %v\n", candidate.Derived.Name, candidate.Derived.Seniority, candidate.Derived.Skills)
	fmt.Fprintln(out, "=== Derive Example Complete ===")
	return nil
}
