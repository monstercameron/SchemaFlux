package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type sensor struct {
	Hour string  `json:"timestamp"`
	Temp float64 `json:"temp_celsius"`
}
type stockPrice struct {
	Date  string  `json:"date"`
	Close float64 `json:"close"`
}
type survey struct {
	Question int    `json:"question_num"`
	Response string `json:"response"`
	Score    int    `json:"score"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Interpolate example: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== Interpolate Example ===")
	sensors := []sensor{{Hour: "10:00", Temp: 22.5}, {Hour: "11:00"}, {Hour: "12:00", Temp: 24.1}}
	readings, err := exp.Interpolate(ctx, sensors, exp.InterpolateOptions{
		Method: "trend", GapIndices: []int{1}, SequenceField: "timestamp",
		Options: schemaflux.Options{Tier: schemaflux.TierSmart, Steering: "The middle sensor reading was lost."},
	})
	if err != nil {
		return fmt.Errorf("interpolate sensor: %w", err)
	}
	fmt.Fprintf(out, "Inferred sensor temperature at 11:00: %.1f°C\n", readings.Complete[1].Temp)

	prices := []stockPrice{{Date: "2024-01-08", Close: 186.40}, {Date: "2024-01-09"}, {Date: "2024-01-10", Close: 190.80}}
	stock, err := exp.Interpolate(ctx, prices, exp.InterpolateOptions{
		Method: "pattern", GapIndices: []int{1}, SequenceField: "date",
		Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("interpolate stock: %w", err)
	}
	fmt.Fprintf(out, "Model-inferred close for missing day: $%.2f (not market data)\n", stock.Complete[1].Close)

	answers := []survey{{Question: 1, Response: "Very satisfied", Score: 5}, {Question: 2}, {Question: 3, Response: "Would recommend", Score: 5}}
	completed, err := exp.Interpolate(ctx, answers, exp.InterpolateOptions{
		Method: "semantic", GapIndices: []int{1}, SequenceField: "question_num",
		Constraints: []string{"score should be 1-5"},
		Options:     schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("interpolate survey: %w", err)
	}
	fmt.Fprintf(out, "Model-inferred answer to question 2: %q (score %d)\n", completed.Complete[1].Response, completed.Complete[1].Score)
	fmt.Fprintln(out, "=== Interpolate Example Complete ===")
	return nil
}
