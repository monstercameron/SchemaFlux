package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type sale struct {
	Rep     string  `json:"sales_rep"`
	Month   string  `json:"month"`
	Revenue float64 `json:"revenue"`
}
type quarterlySale struct {
	Rep   string  `json:"sales_rep"`
	Jan   float64 `json:"jan_revenue"`
	Feb   float64 `json:"feb_revenue"`
	Total float64 `json:"q1_total"`
}
type order struct {
	ID       string `json:"order_id"`
	Customer struct {
		ID string `json:"id"`
	} `json:"customer"`
	Payment struct {
		Amount float64 `json:"amount"`
	} `json:"payment"`
}
type flatOrder struct {
	ID         string  `json:"order_id"`
	CustomerID string  `json:"customer_id"`
	Amount     float64 `json:"payment_amount"`
}
type response struct {
	Respondent string `json:"respondent_id"`
	Question   string `json:"question"`
	Answer     string `json:"answer"`
}
type respondent struct {
	ID             string `json:"respondent_id"`
	Satisfaction   string `json:"satisfaction"`
	Recommendation string `json:"would_recommend"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Pivot example: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== Pivot Example ===")
	sales := []sale{{Rep: "Alice", Month: "Jan", Revenue: 45000}, {Rep: "Alice", Month: "Feb", Revenue: 52000}}
	quarter, err := exp.Pivot[[]sale, []quarterlySale](ctx, sales, exp.PivotOptions{
		PivotOn: []string{"month"}, GroupBy: []string{"sales_rep"}, Aggregate: "sum",
		Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("pivot sales: %w", err)
	}
	if len(quarter.Pivoted) != 1 || quarter.Pivoted[0].Rep != "Alice" ||
		quarter.Pivoted[0].Jan != sales[0].Revenue || quarter.Pivoted[0].Feb != sales[1].Revenue ||
		quarter.Pivoted[0].Total != sales[0].Revenue+sales[1].Revenue {
		return fmt.Errorf("pivot sales: model totals did not match the source rows")
	}
	fmt.Fprintf(out, "Quarterly revenue for Alice: $%.0f\n", quarter.Pivoted[0].Total)

	nested := order{ID: "ORD-2024-78421"}
	nested.Customer.ID = "CUST-1234"
	nested.Payment.Amount = 299.99
	flat, err := exp.Pivot[order, flatOrder](ctx, nested, exp.PivotOptions{
		Flatten: true, Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("flatten order: %w", err)
	}
	if flat.Pivoted.ID != nested.ID || flat.Pivoted.CustomerID != nested.Customer.ID || flat.Pivoted.Amount != nested.Payment.Amount {
		return fmt.Errorf("flatten order: model values did not match the source")
	}
	fmt.Fprintf(out, "Flattened order: %s; customer %s; amount $%.2f\n", flat.Pivoted.ID, flat.Pivoted.CustomerID, flat.Pivoted.Amount)

	answers := []response{{Respondent: "R001", Question: "satisfaction", Answer: "Very satisfied"}, {Respondent: "R001", Question: "would_recommend", Answer: "Yes"}}
	survey, err := exp.Pivot[[]response, []respondent](ctx, answers, exp.PivotOptions{
		PivotOn: []string{"question"}, GroupBy: []string{"respondent_id"}, Aggregate: "first",
		Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("pivot survey: %w", err)
	}
	if len(survey.Pivoted) != 1 || survey.Pivoted[0].ID != "R001" || survey.Pivoted[0].Satisfaction != "Very satisfied" || survey.Pivoted[0].Recommendation != "Yes" {
		return fmt.Errorf("pivot survey: model values did not match the source rows")
	}
	fmt.Fprintf(out, "Survey for R001: %s; recommends %s\n", survey.Pivoted[0].Satisfaction, survey.Pivoted[0].Recommendation)
	fmt.Fprintln(out, "=== Pivot Example Complete ===")
	return nil
}
