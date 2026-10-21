package pricing_test

import (
	"errors"
	"fmt"
	"slices"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/pricing"
)

// Price is what a call cost, from the tokens the provider reported.
func ExamplePrice() {
	usage := schemaflux.Usage{Input: 12_000, Output: 800}
	cost := pricing.Price(usage, "gpt-5", "openai")
	fmt.Printf("priced %v: $%.4f in, $%.4f out, $%.4f in all\n", cost.Priced, cost.Input, cost.Output, cost.Total)
	// Output:
	// priced true: $0.0150 in, $0.0080 out, $0.0230 in all
}

// A model with no price is not free. Its cost is reported as not priced, so a
// total made from it says it is a floor.
func ExamplePrice_unpriced() {
	cost := pricing.Price(schemaflux.Usage{Input: 12_000, Output: 800}, "a-model-nobody-priced", "acme")
	fmt.Println("priced:", cost.Priced, "total:", cost.Total)
	// Output:
	// priced: false total: 0
}

// Register gives the library the price of a model it does not know, in US
// dollars per million tokens, and returns the function that puts back the price
// the table held before.
func ExampleRegister() {
	restore, err := pricing.Register(pricing.Model{
		Provider: "acme",
		Name:     "acme-large-example",
		Input:    3,
		Output:   15,
		Cached:   0.3,
	})
	fmt.Println(err)
	defer restore()

	cost := pricing.Price(schemaflux.Usage{Input: 1_000_000, Cached: 500_000, Output: 100_000}, "acme-large-example", "acme")
	fmt.Printf("$%.2f\n", cost.Total)

	_, err = pricing.Register(pricing.Model{Provider: "acme", Name: "acme-free-example"})
	fmt.Println(errors.Is(err, schemaflux.ErrConfiguration))
	// Output:
	// <nil>
	// $3.15
	// true
}

// Models lists what the table has a price for.
func ExampleModels() {
	models := pricing.Models()
	fmt.Println(slices.Contains(models, "gpt-5"), slices.Contains(models, "jev-latest"), slices.Contains(models, "not-a-model"))
	// Output:
	// true true false
}

// Spent says what the process has spent, and whether the figure is complete: a
// call on a model with no price makes it a floor.
func ExampleSpent() {
	usd, complete := pricing.Spent(time.Now().Add(-24*time.Hour), nil)
	fmt.Println(usd, complete)
	// Output:
	// 0 true
}

// SetLimits sets limits for the process, and returns the function that puts back
// what was there. With Enforce set, a call is refused with ErrBudgetExceeded
// once a period's spend has reached its limit; without it the limits only call
// OnThreshold.
func ExampleSetLimits() {
	restore, err := pricing.SetLimits(pricing.Limits{
		Daily:   5,
		Weekly:  20,
		Enforce: true,
		OnThreshold: func(spent, limit float64, period string) {
			fmt.Printf("%s spend $%.2f of $%.2f\n", period, spent, limit)
		},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer restore()
	fmt.Println("limits set")

	// A ceiling that cannot be enforced is refused and nothing changes.
	_, err = pricing.SetLimits(pricing.Limits{Daily: -1})
	fmt.Println(errors.Is(err, schemaflux.ErrConfiguration))
	// Output:
	// limits set
	// true
}

// Export writes the ledger as a report. An empty ledger is a header alone.
func ExampleExport() {
	report, err := pricing.Export(time.Time{}, "csv")
	fmt.Print(report)
	fmt.Println(err)
	// Output:
	// time,request_id,correlation_id,operation,model,provider,input_tokens,output_tokens,total_tokens,cost,priced
	// <nil>
}

// Summarize adds up the ledger; a figure over a call with no price is marked.
func ExampleSummarize() {
	s := pricing.Summarize(time.Time{}, map[string]string{"operation": "extract"})
	fmt.Println(s.Requests, s.Unpriced, s.Cost.Total, s.Cost.Priced)
	// Output:
	// 0 0 0 true
}
