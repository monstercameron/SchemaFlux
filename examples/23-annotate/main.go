// Example 23: Annotate Operation
// Demonstrates LLM-powered text annotation for entities, sentiment, topics, and keywords

package main

import (
	"fmt"
	"github.com/monstercameron/schemaflux/examples/internal/exampleutil"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	"log"
)

func main() {

	// Initialize SchemaFlux
	if err := exampleutil.Bootstrap(); err != nil {
		log.Fatalf("Failed to initialize SchemaFlux: %v", err)
	}

	// Sample text for annotation (Named Entity Recognition)
	text := `Apple CEO Tim Cook announced new products at the WWDC conference 
	in San Jose, California. The event showcased the latest iPhone and Mac updates.
	Google's Sundar Pichai was also mentioned as a competitor in the AI space.`

	fmt.Println("=== Annotate Example ===")
	fmt.Println("Input text:")
	fmt.Println(text)
	fmt.Println()

	// Example 1: Named Entity Recognition
	fmt.Println("--- Example 1: Named Entity Recognition ---")
	opts := ops.NewAnnotateOptions().
		WithAnnotationTypes([]string{"entities"}).
		WithFormat("structured").
		WithMinConfidence(0.7).
		WithIntelligence(types.Smart) // Use smarter model for complex annotation

	result, err := ops.Annotate(text, opts)
	if err != nil {
		log.Fatalf("Annotation failed: %v", err)
	}

	fmt.Printf("Found %d annotations:\n", len(result.Annotations))
	for _, ann := range result.Annotations {
		fmt.Printf("  - %s (%s): confidence %.2f\n", ann.Text, ann.Type, ann.Confidence)
		if ann.Start >= 0 && ann.End >= 0 {
			fmt.Printf("    Span: %d-%d\n", ann.Start, ann.End)
		}
	}
	fmt.Println()

	// Example 2: Sentiment Annotation
	fmt.Println("--- Example 2: Sentiment Annotation ---")
	reviewText := "The new iPhone is absolutely amazing! Great camera, but the battery life is disappointing."

	sentimentOpts := ops.NewAnnotateOptions().
		WithAnnotationTypes([]string{"sentiment"}).
		WithFormat("structured").
		WithIntelligence(types.Smart)

	sentimentResult, err := ops.Annotate(reviewText, sentimentOpts)
	if err != nil {
		log.Fatalf("Sentiment annotation failed: %v", err)
	}

	fmt.Printf("Input: %s\n", reviewText)
	fmt.Println("Annotations:")
	for _, ann := range sentimentResult.Annotations {
		fmt.Printf("  - %s: %s (confidence %.2f)\n", ann.Type, ann.Value, ann.Confidence)
	}
	fmt.Println()

	// Example 3: Topic Annotation
	fmt.Println("--- Example 3: Topic Annotation ---")
	articleText := `Machine learning is transforming healthcare with new diagnostic tools.
	Researchers at MIT have developed an AI system that can detect cancer earlier than traditional methods.
	The system uses deep learning algorithms trained on millions of medical images.`

	topicOpts := ops.NewAnnotateOptions().
		WithAnnotationTypes([]string{"topics", "keywords"}).
		WithDomain("healthcare").
		WithIntelligence(types.Smart)

	topicResult, err := ops.Annotate(articleText, topicOpts)
	if err != nil {
		log.Fatalf("Topic annotation failed: %v", err)
	}

	fmt.Println("Article topics:")
	for _, ann := range topicResult.Annotations {
		fmt.Printf("  - %s: %s (confidence %.2f)\n", ann.Type, ann.Value, ann.Confidence)
	}

	fmt.Println("\n=== Annotate Example Complete ===")
}
