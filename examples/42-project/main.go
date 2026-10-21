package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type internalOrder struct {
	ID          string  `json:"order_id"`
	ItemCount   int     `json:"item_count"`
	Total       float64 `json:"total"`
	Status      string  `json:"status"`
	CreatedAt   string  `json:"created_at"`
	CustomerSSN string  `json:"customer_ssn"`
	Margin      float64 `json:"margin_pct"`
	Notes       string  `json:"internal_notes"`
}
type publicOrder struct {
	Number    string  `json:"order_number"`
	ItemCount int     `json:"item_count"`
	Total     float64 `json:"total"`
	Status    string  `json:"status"`
	OrderDate string  `json:"order_date"`
}
type transaction struct {
	ID          string  `json:"txn_id"`
	Amount      float64 `json:"amount"`
	Currency    string  `json:"currency"`
	Timestamp   string  `json:"timestamp"`
	MerchantMCC string  `json:"merchant_mcc"`
	CardNumber  string  `json:"card_number"`
}
type analyticsEvent struct {
	ID           string  `json:"event_id"`
	AmountUSD    float64 `json:"amount_usd"`
	EventDate    string  `json:"event_date"`
	MerchantType string  `json:"merchant_type"`
}
type legacyEmployee struct {
	ID         string `json:"emp_no"`
	First      string `json:"fname"`
	Last       string `json:"lname"`
	Department string `json:"dept"`
	HireDate   string `json:"hire_date"`
}
type modernEmployee struct {
	ID         string `json:"employee_id"`
	FullName   string `json:"full_name"`
	Department string `json:"department"`
	StartDate  string `json:"start_date"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Project example: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== Project Example ===")
	order := internalOrder{ID: "ORD-2024-88921", ItemCount: 3, Total: 172.71, Status: "shipped", CreatedAt: "2024-12-01T14:30:00Z", CustomerSSN: "SSN-CANARY-1234", Margin: 0.48, Notes: "internal only"}
	public, err := exp.Project[internalOrder, publicOrder](ctx, order, exp.ProjectOptions{
		Mappings: map[string]string{"order_id": "order_number", "created_at": "order_date"},
		Exclude:  []string{"customer_ssn", "margin_pct", "internal_notes"},
		Options:  schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("project public order: %w", err)
	}
	fmt.Fprintf(out, "Public order: %s; total $%.2f; status %s\n", public.Projected.Number, public.Projected.Total, public.Projected.Status)

	txn := transaction{ID: "TXN-2024120588821", Amount: 156.50, Currency: "USD", Timestamp: "2024-12-05T09:45:30Z", MerchantMCC: "5942", CardNumber: "CARD-CANARY-5678"}
	analytics, err := exp.Project[transaction, analyticsEvent](ctx, txn, exp.ProjectOptions{
		Mappings: map[string]string{"txn_id": "event_id"}, Exclude: []string{"card_number"},
		Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("project analytics event: %w", err)
	}
	fmt.Fprintf(out, "Analytics event: %s; amount $%.2f; type %s\n", analytics.Projected.ID, analytics.Projected.AmountUSD, analytics.Projected.MerchantType)

	legacy := legacyEmployee{ID: "E00421", First: "Sarah", Last: "Johnson", Department: "ENGINEERING", HireDate: "15-MAR-2019"}
	modern, err := exp.Project[legacyEmployee, modernEmployee](ctx, legacy, exp.ProjectOptions{
		Mappings: map[string]string{"emp_no": "employee_id", "hire_date": "start_date"}, InferMissing: true,
		Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("project employee: %w", err)
	}
	fmt.Fprintf(out, "Modern employee: %s; %s; department %s\n", modern.Projected.ID, modern.Projected.FullName, modern.Projected.Department)
	fmt.Fprintln(out, "=== Project Example Complete ===")
	return nil
}
