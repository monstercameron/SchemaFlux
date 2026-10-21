// Example 23: Annotate Operation
// Demonstrates text annotation for entities, sentiment, topics, and keywords.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		log.Printf("Annotation failed: %v. Check the input and provider configuration, then retry.", err)
	}
}

func run(ctx context.Context, out io.Writer) error {
	// Sample text for named entity recognition.
	text := `Apple CEO Tim Cook announced new products at the WWDC conference
	in San Jose, California. The event showcased the latest iPhone and Mac updates.
	Google's Sundar Pichai was also mentioned as a competitor in the AI space.`
	fmt.Fprintln(out, "=== Annotate Example ===")
	fmt.Fprintln(out, "Input text:")
	fmt.Fprintln(out, text)
	fmt.Fprintln(out)

	fmt.Fprintln(out, "--- Example 1: Named Entity Recognition ---")
	// Ask for entity spans and discard model claims below the requested threshold.
	result, err := schemaflux.Annotate(ctx, text, schemaflux.AnnotateOptions{Types: []string{"entities"}, MinConfidence: 0.7, Tier: schemaflux.TierSmart})
	if err != nil {
		return fmt.Errorf("annotate named entities: %w", err)
	}
	fmt.Fprintf(out, "Found %d annotations:\n", len(result.Annotations))
	for _, ann := range result.Annotations {
		// ModelConfidence is the model's claim, not a library-verified score.
		fmt.Fprintf(out, "  - %s (%s): confidence %.2f\n", ann.Text, ann.Type, ann.ModelConfidence)
		if ann.Start >= 0 && ann.End >= 0 {
			fmt.Fprintf(out, "    Span: %d-%d\n", ann.Start, ann.End)
		}
	}
	fmt.Fprintln(out)

	fmt.Fprintln(out, "--- Example 2: Sentiment Annotation ---")
	// Sentiment is a separate annotation kind; it can report mixed reactions.
	reviewText := "The new iPhone is absolutely amazing! Great camera, but the battery life is disappointing."
	sentimentResult, err := schemaflux.Annotate(ctx, reviewText, schemaflux.AnnotateOptions{Types: []string{"sentiment"}, Tier: schemaflux.TierSmart})
	if err != nil {
		return fmt.Errorf("annotate sentiment: %w", err)
	}
	fmt.Fprintf(out, "Input: %s\n", reviewText)
	fmt.Fprintln(out, "Annotations:")
	for _, ann := range sentimentResult.Annotations {
		fmt.Fprintf(out, "  - %s: %s (confidence %.2f)\n", ann.Type, ann.Value, ann.ModelConfidence)
	}
	fmt.Fprintln(out)

	fmt.Fprintln(out, "--- Example 3: Topic Annotation ---")
	// A domain hint narrows the topics and keywords to the article's subject area.
	articleText := `Machine learning is transforming healthcare with new diagnostic tools.
	Researchers at MIT have developed an AI system that can detect cancer earlier than traditional methods.
	The system uses deep learning algorithms trained on millions of medical images.`
	topicResult, err := schemaflux.Annotate(ctx, articleText, schemaflux.AnnotateOptions{Types: []string{"topics", "keywords"}, Domain: "healthcare", Tier: schemaflux.TierSmart})
	if err != nil {
		return fmt.Errorf("annotate topics: %w", err)
	}
	fmt.Fprintln(out, "Article topics:")
	for _, ann := range topicResult.Annotations {
		fmt.Fprintf(out, "  - %s: %s (confidence %.2f)\n", ann.Type, ann.Value, ann.ModelConfidence)
	}
	fmt.Fprintln(out, "\n=== Annotate Example Complete ===")
	return nil
}
