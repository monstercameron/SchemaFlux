// 20-complete demonstrates completing partial text and struct fields with v2.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// BlogPost represents a blog post with partial content.
type BlogPost struct {
	Title    string   `json:"title"`
	Author   string   `json:"author"`
	Category string   `json:"category"`
	Tags     []string `json:"tags"`
	Body     string   `json:"body"` // This will be completed.
}

// ProductDescription represents a product listing.
type ProductDescription struct {
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	Category    string  `json:"category"`
	Description string  `json:"description"` // This will be completed.
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Complete example failed: %v\nCheck your provider configuration and credentials, then run it again.\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	if _, err := fmt.Fprintln(out, "Complete Example - Finish Partial Text with LLM", strings.Repeat("=", 60)); err != nil {
		return err
	}

	// Example 1: Basic sentence completion.
	partial1 := "The benefits of using type-safe LLM operations include"
	fmt.Fprintln(out, "\n1. Basic Sentence Completion", strings.Repeat("-", 60))
	fmt.Fprintf(out, "   Input: %q\n", partial1)
	result1, err := schemaflux.Complete(ctx, partial1, schemaflux.CompleteOptions{MaxLength: 100, Tier: schemaflux.TierFast})
	if err != nil {
		return fmt.Errorf("complete the basic sentence: %w", err)
	}
	fmt.Fprintln(out, "   CompleteResult:")
	fmt.Fprintf(out, "      Text:   %s\n      Original: %q\n      Length: %d characters added\n", result1.Text, result1.Original, result1.Length)

	// Example 2: Code completion. A lower Effort is the v2 control for a simpler
	// code continuation; the old sampling temperature option is not in v2.
	partial2 := "func validateEmail(email string) bool {\n    // Check if email is valid\n    "
	fmt.Fprintln(out, "\n2. Code Completion", strings.Repeat("-", 60))
	fmt.Fprintf(out, "   Input:\n   %s\n", partial2)
	result2, err := schemaflux.Complete(ctx, partial2, schemaflux.CompleteOptions{MaxLength: 150, Effort: schemaflux.EffortLow, Tier: schemaflux.TierFast})
	if err != nil {
		return fmt.Errorf("complete the code example: %w", err)
	}
	fmt.Fprintf(out, "   Text:\n%s\n   Length: %d characters added\n", result2.Text, result2.Length)

	// Example 3: Earlier messages provide context for an email completion.
	partial3 := "Dear Customer Support,\n\nI am writing to request a refund because"
	context3 := []string{"Subject: Refund Request - Order #12345", "Customer purchased a laptop on Nov 15", "Product arrived damaged"}
	fmt.Fprintln(out, "\n3. Email Completion with Context", strings.Repeat("-", 60))
	fmt.Fprintf(out, "   Context: %v\n   Input: %q\n", context3, partial3)
	result3, err := schemaflux.Complete(ctx, partial3, schemaflux.CompleteOptions{Background: context3, MaxLength: 150, Tier: schemaflux.TierFast})
	if err != nil {
		return fmt.Errorf("complete the refund email: %w", err)
	}
	fmt.Fprintf(out, "   Text: %s\n   Length: %d characters added\n", result3.Text, result3.Length)

	// Example 4: A more exploratory tier suits a creative continuation. v2
	// expresses model choice through Tier; sampling temperature is not exposed.
	partial4 := "The old lighthouse keeper had a secret that nobody in the village knew about. Every night at midnight, he would"
	fmt.Fprintln(out, "\n4. Creative Story Completion", strings.Repeat("-", 60))
	fmt.Fprintf(out, "   Input: %q\n", partial4)
	result4, err := schemaflux.Complete(ctx, partial4, schemaflux.CompleteOptions{MaxLength: 100, Tier: schemaflux.TierSmart})
	if err != nil {
		return fmt.Errorf("complete the lighthouse story: %w", err)
	}
	fmt.Fprintf(out, "   Text: %s\n   Length: %d characters added\n", result4.Text, result4.Length)

	fmt.Fprintln(out, "\n", strings.Repeat("=", 60), "\nCompleteField - Complete a Struct Field\n", strings.Repeat("=", 60))

	// Example 5: Other fields in the struct give the model context for Body.
	blogPost := BlogPost{Title: "The Future of Renewable Energy", Author: "Dr. Sarah Chen", Category: "Technology", Tags: []string{"solar", "wind", "sustainability", "climate"}, Body: "As the world faces increasing climate challenges, renewable energy sources are becoming"}
	fmt.Fprintln(out, "\n5. Complete Blog Post Body")
	fmt.Fprintf(out, "   Title: %s\n   Author: %s\n   Category: %s\n   Tags: %v\n   Body: %q\n", blogPost.Title, blogPost.Author, blogPost.Category, blogPost.Tags, blogPost.Body)
	result5, err := schemaflux.CompleteField(ctx, blogPost, "Body", schemaflux.CompleteOptions{MaxLength: 200, Tier: schemaflux.TierFast})
	if err != nil {
		return fmt.Errorf("complete the blog post body: %w", err)
	}
	fmt.Fprintf(out, "   Field: %s\n   Original: %q\n   Completed: %s\n   Length: %d characters added\n   Updated BlogPost.Body: %s\n", result5.Field, result5.Original, result5.Completed, result5.Length, result5.Data.Body)

	// Example 6: Product details are supplied as context for Description.
	product := ProductDescription{Name: "EcoSmart Wireless Earbuds", Price: 79.99, Category: "Electronics", Description: "These premium wireless earbuds feature"}
	fmt.Fprintln(out, "\n6. Complete Product Description")
	fmt.Fprintf(out, "   Name: %s\n   Price: $%.2f\n   Category: %s\n   Description: %q\n", product.Name, product.Price, product.Category, product.Description)
	result6, err := schemaflux.CompleteField(ctx, product, "Description", schemaflux.CompleteOptions{MaxLength: 150, Tier: schemaflux.TierFast})
	if err != nil {
		return fmt.Errorf("complete the product description: %w", err)
	}
	fmt.Fprintf(out, "   Field: %s\n   Original: %q\n   Completed: %s\n   Length: %d characters added\n   Updated ProductDescription.Description: %s\n", result6.Field, result6.Original, result6.Completed, result6.Length, result6.Data.Description)
	fmt.Fprintln(out, "\nSuccess! Partial text and struct fields completed.")
	return nil
}
