// Example 26: Compress Operation
// Compresses text/data while preserving essential meaning using an LLM.
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
		log.Fatalf("Compression example failed: %v", err)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== Compress Example ===")
	fmt.Fprintln(out, "Compresses text while preserving essential meaning")
	fmt.Fprintln(out)

	// Example 1: Compress a document struct.
	fmt.Fprintln(out, "--- Example 1: Document Compression ---")
	type Document struct {
		Title   string `json:"title"`
		Content string `json:"content"`
		Author  string `json:"author"`
	}
	doc := Document{
		Title:  "AI in Healthcare",
		Author: "Dr. Smith",
		Content: `Artificial intelligence has transformed the technology landscape in unprecedented ways.
Machine learning algorithms now power everything from search engines to autonomous vehicles.
Deep learning uses neural networks with multiple layers to process complex patterns in data.
The healthcare industry has particularly benefited from AI advancements. Diagnostic systems
can now detect diseases from medical images with accuracy rivaling human experts. Drug
discovery pipelines have been accelerated through AI-powered molecular analysis.
However, the rapid advancement of AI also raises important ethical considerations around
bias in training data, privacy concerns, and potential worker displacement.`,
	}
	fmt.Fprintln(out, "INPUT: Document struct")
	fmt.Fprintf(out, "  Title: %q\n", doc.Title)
	fmt.Fprintf(out, "  Author: %q\n", doc.Author)
	fmt.Fprintf(out, "  Content: (%d chars) %q...\n\n", len(doc.Content), doc.Content[:80])

	result, err := schemaflux.Compress(ctx, doc, schemaflux.CompressOptions{
		Ratio: 0.3, Strategy: "semantic", PreserveInfo: []string{"key facts", "conclusions"},
	})
	if err != nil {
		return fmt.Errorf("compress the document; check the configured provider and retry: %w", err)
	}
	fmt.Fprintln(out, "OUTPUT: CompressResult[Document]")
	fmt.Fprintf(out, "  OriginalSize: %d chars\n", result.OriginalSize)
	fmt.Fprintf(out, "  CompressedSize: %d chars\n", result.CompressedSize)
	fmt.Fprintf(out, "  ActualRatio: %.2f\n", result.ActualRatio)
	fmt.Fprintf(out, "  Compressed: %q\n\n", result.Compressed)

	// Example 2: Compress meeting notes as a string.
	fmt.Fprintln(out, "--- Example 2: Meeting Notes Compression ---")
	meeting := `Project Alpha Update
Attendees: John (PM), Sarah (Dev Lead), Mike (Designer), Lisa (QA)

Status Updates:
- Backend API is 80% complete, on track for Friday delivery
- Frontend has blockers with authentication flow
- Design mockups for v2 features are ready for review
- QA found 3 critical bugs in payment module

Action Items:
1. Sarah to document auth blockers by EOD
2. John to schedule sync with Alex tomorrow
3. Mike to present design mockups Wednesday
4. Lisa to prioritize payment bugs

Next Meeting: Friday 10 AM`
	fmt.Fprintln(out, "INPUT: string")
	fmt.Fprintf(out, "  Content: (%d chars)\n", len(meeting))
	fmt.Fprintf(out, "  %q...\n\n", meeting[:100])

	meetingResult, err := schemaflux.Compress(ctx, meeting, schemaflux.CompressOptions{
		Ratio: 0.4, Strategy: "lossy", Priority: "actions",
	})
	if err != nil {
		return fmt.Errorf("compress the meeting notes; check the configured provider and retry: %w", err)
	}
	fmt.Fprintln(out, "OUTPUT: CompressResult[string]")
	fmt.Fprintf(out, "  OriginalSize: %d chars\n", meetingResult.OriginalSize)
	fmt.Fprintf(out, "  CompressedSize: %d chars\n", meetingResult.CompressedSize)
	fmt.Fprintf(out, "  ActualRatio: %.2f\n", meetingResult.ActualRatio)
	fmt.Fprintf(out, "  Compressed: %q\n", meetingResult.Compressed)
	fmt.Fprintln(out, "\n=== Compress Example Complete ===")
	return nil
}
