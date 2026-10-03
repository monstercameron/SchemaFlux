package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// Product represents a product to compare.
type Product struct {
	Name     string            `json:"name"`
	Price    float64           `json:"price"`
	Features []string          `json:"features"`
	Specs    map[string]string `json:"specs"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	productA := Product{
		Name: "UltraPhone Pro Max", Price: 1299.99,
		Features: []string{"6.7-inch OLED display", "108MP triple camera", "5000mAh battery", "5G connectivity", "IP68 water resistance"},
		Specs:    map[string]string{"Processor": "Snapdragon 8 Gen 3", "RAM": "12GB", "Storage": "256GB", "OS": "Android 14"},
	}
	productB := Product{
		Name: "SmartPhone Elite", Price: 1099.99,
		Features: []string{"6.5-inch AMOLED display", "64MP dual camera", "4500mAh battery", "5G connectivity", "Premium aluminum build"},
		Specs:    map[string]string{"Processor": "A17 Bionic", "RAM": "8GB", "Storage": "128GB", "OS": "iOS 17"},
	}

	fmt.Fprintln(out, "🔍 Compare Example - Product Comparison")
	fmt.Fprintln(out, strings.Repeat("=", 60))
	fmt.Fprintf(out, "\n📱 Product A:\n   Name:  %s ($%.2f)\n   Features: %v\n", productA.Name, productA.Price, productA.Features)
	fmt.Fprintf(out, "\n📱 Product B:\n   Name:  %s ($%.2f)\n   Features: %v\n", productB.Name, productB.Price, productB.Features)

	// Aspects and depth guide the comparison; Compare checks that every requested aspect is scored.
	result, err := schemaflux.Compare(ctx, productA, productB, schemaflux.CompareOptions{
		Aspects: []string{"camera", "battery", "display", "performance", "value"},
		FocusOn: "both",
		Depth:   7,
		Options: schemaflux.Options{Tier: schemaflux.TierFast},
	})
	if err != nil {
		return fmt.Errorf("comparison failed; check your provider configuration and try again: %w", err)
	}

	fmt.Fprintln(out, "\n✅ Comparison Results:")
	fmt.Fprintln(out, "---")
	fmt.Fprintf(out, "📊 Overall Similarity: %.0f%%\n📝 Verdict: %s\n", result.SimilarityScore*100, result.Verdict)
	if len(result.AspectScores) > 0 {
		fmt.Fprintln(out, "\n📈 Similarity by Aspect:")
		aspects := make([]string, 0, len(result.AspectScores))
		for aspect := range result.AspectScores {
			aspects = append(aspects, aspect)
		}
		sort.Strings(aspects)
		for _, aspect := range aspects {
			score := result.AspectScores[aspect]
			filled := int(score * 10)
			if filled > 10 {
				filled = 10
			}
			fmt.Fprintf(out, "   %s: %s%s %.0f%%\n", aspect, strings.Repeat("█", filled), strings.Repeat("░", 10-filled), score*100)
		}
	}
	if len(result.Similarities) > 0 {
		fmt.Fprintln(out, "\n✅ Similarities:")
		for _, point := range result.Similarities {
			fmt.Fprintf(out, "   • [%s] %s\n", point.Aspect, point.Description)
		}
	}
	if len(result.Differences) > 0 {
		fmt.Fprintln(out, "\n❌ Differences:")
		for _, point := range result.Differences {
			fmt.Fprintf(out, "   %s [%s] %s\n", severityIcon(point.Severity), point.Aspect, point.Description)
		}
	}
	fmt.Fprintf(out, "\n🎯 Recommendation based on comparison:\n   Choose %s for:\n   • Photography enthusiasts\n   • Heavy multitasking\n   • Android ecosystem preference\n\n   Choose %s for:\n   • Apple ecosystem users\n   • Better value for money\n   • Premium build quality\n", productA.Name, productB.Name)
	fmt.Fprintln(out, "\n✨ Success! Typed product comparison with detailed analysis")
	return nil
}

func severityIcon(severity string) string {
	switch severity {
	case "major":
		return "🔴"
	case "moderate":
		return "🟡"
	case "minor":
		return "🟢"
	default:
		return "⚪"
	}
}
