package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

const article = `
Artificial Intelligence Revolution in Healthcare

The healthcare industry is experiencing a profound transformation driven by artificial
intelligence technologies. Machine learning algorithms are now capable of analyzing
medical images with accuracy that rivals or exceeds human radiologists. In a recent
study published in Nature Medicine, an AI system detected breast cancer in mammograms
with 94.5% accuracy, compared to 88% for human radiologists.

Beyond diagnostics, AI is revolutionizing drug discovery. Traditional pharmaceutical
research takes 10-15 years and costs over $2 billion per drug. AI-powered platforms
can now screen millions of molecular combinations in weeks, identifying promising
candidates for further testing. Atomwise, a San Francisco-based startup, used AI to
discover two potential Ebola treatments in just one day - a process that would have
taken years using conventional methods.

Patient care is also being transformed through AI-powered virtual health assistants
and predictive analytics. These systems can monitor patient vital signs in real-time,
predict potential health emergencies before they occur, and provide personalized
treatment recommendations based on individual genetic profiles and medical histories.

However, challenges remain. Data privacy concerns, algorithmic bias, and the need for
regulatory frameworks are critical issues that must be addressed. The FDA has approved
only a handful of AI-based medical devices, and questions about liability when AI
systems make errors remain unresolved.

Despite these challenges, experts predict that AI will become an integral part of
healthcare within the next decade, potentially saving millions of lives and reducing
healthcare costs by up to 30%. The key will be ensuring that these technologies are
deployed ethically and equitably, benefiting all patients regardless of their
socioeconomic status.
`

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Summarize example failed: %v\nCheck your provider configuration and API key, then try again.\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "Summarize Example - Article Condensation")
	fmt.Fprintln(out, "============================================================")
	fmt.Fprintf(out, "\nOriginal Article Length: %d characters\n", len(article))
	fmt.Fprintln(out, "\nOriginal Article:")
	fmt.Fprintln(out, "---")
	fmt.Fprint(out, article)
	fmt.Fprintln(out, "---")

	// Example 1: a plain string summary. TargetLength is a request to the model.
	fmt.Fprintln(out, "\nExample 1: Simple Summary (string to string)")
	fmt.Fprintln(out, "----------------------------------------")
	summary, err := schemaflux.Summarize(ctx, article, schemaflux.SummarizeOptions{
		TargetLength: 3,
		LengthUnit:   "sentences",
		Options:      schemaflux.Options{Tier: schemaflux.TierFast, Steering: "Create a concise summary capturing key points: AI in diagnostics, drug discovery, patient care, and challenges."},
	})
	if err != nil {
		return fmt.Errorf("summarize the article: %w", err)
	}
	fmt.Fprintln(out, "\nSummary:")
	fmt.Fprintln(out, "---")
	fmt.Fprintln(out, summary)
	fmt.Fprintln(out, "---")

	// Example 2: detail includes model-reported key points and confidence, while
	// CompressionRatio and Length are measured by the library.
	fmt.Fprintln(out, "\nExample 2: Summary with Metadata (SummarizeDetail)")
	fmt.Fprintln(out, "----------------------------------------")
	result, err := schemaflux.SummarizeDetail(ctx, article, schemaflux.SummarizeOptions{
		TargetLength: 3,
		LengthUnit:   "sentences",
		Options:      schemaflux.Options{Tier: schemaflux.TierFast},
	})
	if err != nil {
		return fmt.Errorf("summarize the article with details: %w", err)
	}
	fmt.Fprintln(out, "\nSummary with Metadata:")
	fmt.Fprintln(out, "---")
	fmt.Fprintln(out, result.Text)
	fmt.Fprintln(out, "---")
	fmt.Fprintln(out, "\nRich Metadata:")
	fmt.Fprintf(out, "   Compression Ratio: %.1f%% of original\n", result.CompressionRatio*100)
	fmt.Fprintf(out, "   Model Confidence (model-reported): %.0f%%\n", result.ModelConfidence*100)
	if len(result.KeyPoints) > 0 {
		fmt.Fprintln(out, "\nKey Points Extracted:")
		for i, point := range result.KeyPoints {
			fmt.Fprintf(out, "   %d. %s\n", i+1, point)
		}
	}
	fmt.Fprintln(out, "\nSummary Statistics:")
	fmt.Fprintf(out, "   Original:    %d characters\n", len(article))
	fmt.Fprintf(out, "   Summary:     %d characters\n", len(result.Text))
	fmt.Fprintf(out, "   Reduction:   %.1f%% smaller\n", (1-result.CompressionRatio)*100)
	fmt.Fprintln(out, "\nSuccess! SummarizeDetail provides rich insights beyond just text")
	return nil
}
