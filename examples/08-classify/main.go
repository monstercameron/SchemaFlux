package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// Review represents a customer review.
type Review struct {
	ID      int
	Author  string
	Product string
	Text    string
}

// ClassifiedReview keeps a review with the model's classification claim.
type ClassifiedReview struct {
	Review
	Sentiment       string
	ModelConfidence float64
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Classify example failed: %v\nCheck the provider configuration and retry.\n", err)
		os.Exit(1)
	}
}

// run classifies the same five customer reviews as the original example.
func run(ctx context.Context, out io.Writer) error {
	reviews := []Review{
		{ID: 1, Author: "Sarah M.", Product: "Wireless Headphones", Text: "Amazing sound quality! The bass is incredible and battery lasts for days. Best purchase I've made this year!"},
		{ID: 2, Author: "Mike T.", Product: "Smart Watch", Text: "Total disappointment. Stopped working after 2 weeks. Customer service was unhelpful. Don't waste your money."},
		{ID: 3, Author: "Jessica L.", Product: "Coffee Maker", Text: "It's okay. Makes decent coffee. Nothing special but gets the job done. Average quality for the price."},
		{ID: 4, Author: "Tom K.", Product: "Standing Desk", Text: "Life-changing for my back pain! Easy to assemble, smooth adjustment. Highly recommend for anyone working from home."},
		{ID: 5, Author: "Rachel P.", Product: "Bluetooth Speaker", Text: "Arrived damaged and sound quality is terrible. Bass is non-existent. Returning immediately."},
	}
	// Categories are explicit choices: Classify checks that each answer is one of them.
	categories := []string{"positive", "negative", "neutral"}

	fmt.Fprintln(out, "⭐ Classify Example - Sentiment Analysis")
	fmt.Fprintln(out, "============================================================")
	fmt.Fprint(out, "\n📝 Customer Reviews to Classify:\n\n")

	results := map[string][]ClassifiedReview{
		"positive": {},
		"negative": {},
		"neutral":  {},
	}
	for _, review := range reviews {
		// ModelConfidence is the model's reported score, not a library-verified measurement.
		result, err := schemaflux.Classify(ctx, review.Text, categories, schemaflux.ClassifyOptions{
			Options: schemaflux.Options{Tier: schemaflux.TierFast},
		})
		if err != nil {
			return fmt.Errorf("classify review #%d: %w", review.ID, err)
		}
		classified := ClassifiedReview{Review: review, Sentiment: result.Category, ModelConfidence: result.ModelConfidence}
		results[result.Category] = append(results[result.Category], classified)

		emoji := "😐"
		if result.Category == "positive" {
			emoji = "😊"
		} else if result.Category == "negative" {
			emoji = "😞"
		}
		fmt.Fprintf(out, "Review #%d by %s %s\n", review.ID, review.Author, emoji)
		fmt.Fprintf(out, "  Product:    %s\n", review.Product)
		fmt.Fprintf(out, "  Sentiment:  %s (%.0f%% model confidence)\n", result.Category, result.ModelConfidence*100)
		if result.Reasoning != "" {
			fmt.Fprintf(out, "  Reasoning:  %s\n", result.Reasoning)
		}
		if len(result.Alternatives) > 0 {
			fmt.Fprintln(out, "  Alternatives:")
			for _, alt := range result.Alternatives {
				fmt.Fprintf(out, "    - %s (%.0f%%)\n", alt.Category, alt.ModelConfidence*100)
			}
		}
		fmt.Fprintf(out, "  Review:     %q\n\n", review.Text)
	}

	fmt.Fprintln(out, "📊 Classification Summary:")
	fmt.Fprintln(out, "---")
	fmt.Fprintf(out, "😊 Positive Reviews: %d\n", len(results["positive"]))
	for _, r := range results["positive"] {
		fmt.Fprintf(out, "   - Review #%d: %s ⭐⭐⭐⭐⭐ (%.0f%% confident)\n", r.ID, r.Product, r.ModelConfidence*100)
	}
	fmt.Fprintf(out, "\n😞 Negative Reviews: %d\n", len(results["negative"]))
	for _, r := range results["negative"] {
		fmt.Fprintf(out, "   - Review #%d: %s ⭐ (%.0f%% confident)\n", r.ID, r.Product, r.ModelConfidence*100)
	}
	fmt.Fprintf(out, "\n😐 Neutral Reviews: %d\n", len(results["neutral"]))
	for _, r := range results["neutral"] {
		fmt.Fprintf(out, "   - Review #%d: %s ⭐⭐⭐ (%.0f%% confident)\n", r.ID, r.Product, r.ModelConfidence*100)
	}

	// Summaries use successful results only; this path has all five or returns an error.
	total := len(reviews)
	fmt.Fprintln(out, "\n📈 Sentiment Distribution:")
	fmt.Fprintf(out, "   Positive: %.0f%%\n", float64(len(results["positive"]))/float64(total)*100)
	fmt.Fprintf(out, "   Negative: %.0f%%\n", float64(len(results["negative"]))/float64(total)*100)
	fmt.Fprintf(out, "   Neutral:  %.0f%%\n", float64(len(results["neutral"]))/float64(total)*100)
	fmt.Fprintln(out, "\n✨ Success! All reviews classified with confidence scores")
	return nil
}
