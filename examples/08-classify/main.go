// Example: 08-classify
//
// Operation: Classify[I,C] - Classify items into categories with confidence
//
// Input: []Review (5 customer reviews)
//   - #1: "Amazing sound quality!" (Wireless Headphones) → positive
//   - #2: "Total disappointment..." (Smart Watch) → negative
//   - #3: "It's okay. Makes decent coffee." (Coffee Maker) → neutral
//   - #4: "Life-changing for my back pain!" (Standing Desk) → positive
//   - #5: "Arrived damaged and sound quality terrible" (Speaker) → negative
//
// Expected Output: ClassificationResult for each
//   - Category: "positive" | "negative" | "neutral"
//   - ModelConfidence: 0.0-1.0
//   - Reasoning: explanation of classification
//   - Alternatives: other possible categories with confidence
//
// Provider: Cerebras (gpt-oss-120b via Fast intelligence)
// Expected Duration: ~500-1000ms per review
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux"
)

// Review represents a customer review
type Review struct {
	ID      int
	Author  string
	Product string
	Text    string
}

// ClassifiedReview extends Review with classification results
type ClassifiedReview struct {
	Review
	Sentiment       string
	ModelConfidence float64
}

// loadEnv loads environment variables from a .env file
func loadEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			os.Setenv(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		}
	}
	return scanner.Err()
}

func main() {
	// Load .env file from project root
	if err := loadEnv("../../.env"); err != nil {
		fmt.Printf("Warning: Could not load .env file: %v\n", err)
	}

	// Initialize SchemaFlux
	if err := schemaflux.InitWithEnv(); err != nil {
		schemaflux.GetLogger().Error("Failed to initialize SchemaFlux", "error", err)
		os.Exit(1)
	}

	// Customer reviews to classify
	reviews := []Review{
		{
			ID:      1,
			Author:  "Sarah M.",
			Product: "Wireless Headphones",
			Text:    "Amazing sound quality! The bass is incredible and battery lasts for days. Best purchase I've made this year!",
		},
		{
			ID:      2,
			Author:  "Mike T.",
			Product: "Smart Watch",
			Text:    "Total disappointment. Stopped working after 2 weeks. Customer service was unhelpful. Don't waste your money.",
		},
		{
			ID:      3,
			Author:  "Jessica L.",
			Product: "Coffee Maker",
			Text:    "It's okay. Makes decent coffee. Nothing special but gets the job done. Average quality for the price.",
		},
		{
			ID:      4,
			Author:  "Tom K.",
			Product: "Standing Desk",
			Text:    "Life-changing for my back pain! Easy to assemble, smooth adjustment. Highly recommend for anyone working from home.",
		},
		{
			ID:      5,
			Author:  "Rachel P.",
			Product: "Bluetooth Speaker",
			Text:    "Arrived damaged and sound quality is terrible. Bass is non-existent. Returning immediately.",
		},
	}

	// Categories for classification
	categories := []string{"positive", "negative", "neutral"}

	fmt.Println("⭐ Classify Example - Sentiment Analysis")
	fmt.Println("=" + string(make([]byte, 60)))
	fmt.Println("\n📝 Customer Reviews to Classify:")
	fmt.Println()

	// Classify each review - now with confidence scores and alternatives!
	results := make(map[string][]ClassifiedReview)
	for _, category := range categories {
		results[category] = []ClassifiedReview{}
	}

	for _, review := range reviews {
		classifyOpts := schemaflux.NewClassifyOptions().WithCategories(categories)
		classifyOpts.OpOptions.Intelligence = schemaflux.Fast

		// Use the new generic Classify with typed result
		result, err := schemaflux.Classify[string, string](review.Text, classifyOpts)
		if err != nil {
			schemaflux.GetLogger().Warn("Failed to classify review", "review_id", review.ID, "error", err)
			continue
		}

		classified := ClassifiedReview{
			Review:          review,
			Sentiment:       result.Category,
			ModelConfidence: result.ModelConfidence,
		}
		results[result.Category] = append(results[result.Category], classified)

		// Show individual classification with confidence
		emoji := "😐"
		if result.Category == "positive" {
			emoji = "😊"
		} else if result.Category == "negative" {
			emoji = "😞"
		}

		fmt.Printf("Review #%d by %s %s\n", review.ID, review.Author, emoji)
		fmt.Printf("  Product:    %s\n", review.Product)
		fmt.Printf("  Sentiment:  %s (%.0f%% confidence)\n", result.Category, result.ModelConfidence*100)
		if result.Reasoning != "" {
			fmt.Printf("  Reasoning:  %s\n", result.Reasoning)
		}
		if len(result.Alternatives) > 0 {
			fmt.Printf("  Alternatives:\n")
			for _, alt := range result.Alternatives {
				fmt.Printf("    - %s (%.0f%%)\n", alt.Category, alt.ModelConfidence*100)
			}
		}
		fmt.Printf("  Review:     \"%s\"\n\n", review.Text)
	}

	// Show summary
	fmt.Println("📊 Classification Summary:")
	fmt.Println("---")
	fmt.Printf("😊 Positive Reviews: %d\n", len(results["positive"]))
	for _, r := range results["positive"] {
		fmt.Printf("   - Review #%d: %s ⭐⭐⭐⭐⭐ (%.0f%% confident)\n", r.ID, r.Product, r.ModelConfidence*100)
	}

	fmt.Printf("\n😞 Negative Reviews: %d\n", len(results["negative"]))
	for _, r := range results["negative"] {
		fmt.Printf("   - Review #%d: %s ⭐ (%.0f%% confident)\n", r.ID, r.Product, r.ModelConfidence*100)
	}

	fmt.Printf("\n😐 Neutral Reviews: %d\n", len(results["neutral"]))
	for _, r := range results["neutral"] {
		fmt.Printf("   - Review #%d: %s ⭐⭐⭐ (%.0f%% confident)\n", r.ID, r.Product, r.ModelConfidence*100)
	}

	// Calculate sentiment distribution
	total := len(reviews)
	positivePercent := float64(len(results["positive"])) / float64(total) * 100
	negativePercent := float64(len(results["negative"])) / float64(total) * 100
	neutralPercent := float64(len(results["neutral"])) / float64(total) * 100

	fmt.Println("\n📈 Sentiment Distribution:")
	fmt.Printf("   Positive: %.0f%%\n", positivePercent)
	fmt.Printf("   Negative: %.0f%%\n", negativePercent)
	fmt.Printf("   Neutral:  %.0f%%\n", neutralPercent)

	fmt.Println("\n✨ Success! All reviews classified with confidence scores")
}
