// 20-complete: Complete partial text using LLM intelligence
// Intelligence: Fast (Cerebras gpt-oss-120b)
// Expectations:
// - Completes partial sentences/paragraphs naturally
// - Uses context for coherent completions
// - Supports different temperatures for creative vs. conservative output
// - CompleteField: completes a specific field in a struct using other fields as context

package main

import (
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/internal/exampleutil"
)

// BlogPost represents a blog post with partial content
type BlogPost struct {
	Title    string   `json:"title"`
	Author   string   `json:"author"`
	Category string   `json:"category"`
	Tags     []string `json:"tags"`
	Body     string   `json:"body"` // This will be completed
}

// ProductDescription represents a product listing
type ProductDescription struct {
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	Category    string  `json:"category"`
	Description string  `json:"description"` // This will be completed
}

func main() {

	fmt.Println("??  Complete Example - Finish Partial Text with LLM")
	fmt.Println("=" + string(make([]byte, 60)))

	// Initialize SchemaFlux with Fast intelligence (Cerebras)
	if err := exampleutil.Bootstrap(); err != nil {
		schemaflux.GetLogger().Error("Failed to initialize SchemaFlux", "error", err)
		return
	}

	// Example 1: Basic sentence completion
	fmt.Println("\n1??  Basic Sentence Completion")
	fmt.Println("-" + string(make([]byte, 60)))

	partial1 := "The benefits of using type-safe LLM operations include"
	fmt.Printf("   Input: %q\n", partial1)

	result1, err := schemaflux.Complete(partial1,
		schemaflux.NewCompleteOptions().
			WithMaxLength(100).
			WithIntelligence(schemaflux.Fast))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Println("   ? CompleteResult:")
		fmt.Printf("      Text:       %s\n", result1.Text)
		fmt.Printf("      Original:   %q\n", result1.Original)
		fmt.Printf("      Length:     %d characters added\n", result1.Length)
		fmt.Printf("      ModelConfidence: %.2f\n", result1.ModelConfidence)
	}

	// Example 2: Code completion
	fmt.Println("\n2??  Code Completion")
	fmt.Println("-" + string(make([]byte, 60)))

	partial2 := "func validateEmail(email string) bool {\n    // Check if email is valid\n    "
	fmt.Printf("   Input:\n   %s\n", partial2)

	result2, err := schemaflux.Complete(partial2,
		schemaflux.NewCompleteOptions().
			WithMaxLength(150).
			WithTemperature(0.3). // Lower temp for code
			WithIntelligence(schemaflux.Fast))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Println("   ? CompleteResult:")
		fmt.Printf("      Text:\n%s\n", result2.Text)
		fmt.Printf("      Length:     %d characters added\n", result2.Length)
		fmt.Printf("      ModelConfidence: %.2f\n", result2.ModelConfidence)
	}

	// Example 3: Email completion with context
	fmt.Println("\n3??  Email Completion with Context")
	fmt.Println("-" + string(make([]byte, 60)))

	partial3 := "Dear Customer Support,\n\nI am writing to request a refund because"
	context3 := []string{
		"Subject: Refund Request - Order #12345",
		"Customer purchased a laptop on Nov 15",
		"Product arrived damaged",
	}
	fmt.Printf("   Context: %v\n", context3)
	fmt.Printf("   Input: %q\n", partial3)

	result3, err := schemaflux.Complete(partial3,
		schemaflux.NewCompleteOptions().
			WithBackground(context3).
			WithMaxLength(150).
			WithIntelligence(schemaflux.Fast))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Println("   ? CompleteResult:")
		fmt.Printf("      Text:       %s\n", result3.Text)
		fmt.Printf("      Length:     %d characters added\n", result3.Length)
		fmt.Printf("      ModelConfidence: %.2f\n", result3.ModelConfidence)
	}

	// Example 4: Creative story completion
	fmt.Println("\n4??  Creative Story Completion (Higher Temperature)")
	fmt.Println("-" + string(make([]byte, 60)))

	partial4 := "The old lighthouse keeper had a secret that nobody in the village knew about. Every night at midnight, he would"
	fmt.Printf("   Input: %q\n", partial4)

	result4, err := schemaflux.Complete(partial4,
		schemaflux.NewCompleteOptions().
			WithMaxLength(100).
			WithTemperature(1.0). // Higher temp for creativity
			WithIntelligence(schemaflux.Fast))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Println("   ? CompleteResult:")
		fmt.Printf("      Text:       %s\n", result4.Text)
		fmt.Printf("      Length:     %d characters added\n", result4.Length)
		fmt.Printf("      ModelConfidence: %.2f\n", result4.ModelConfidence)
	}

	// ========================================
	// CompleteField Examples - Complete a field in a struct
	// ========================================
	fmt.Println("\n" + "=" + string(make([]byte, 60)))
	fmt.Println("?? CompleteField - Complete a Struct Field")
	fmt.Println("=" + string(make([]byte, 60)))

	// Example 5: Complete a blog post body using other fields as context
	fmt.Println("\n5??  Complete Blog Post Body (using Title, Author, Tags as context)")
	fmt.Println("-" + string(make([]byte, 60)))

	blogPost := BlogPost{
		Title:    "The Future of Renewable Energy",
		Author:   "Dr. Sarah Chen",
		Category: "Technology",
		Tags:     []string{"solar", "wind", "sustainability", "climate"},
		Body:     "As the world faces increasing climate challenges, renewable energy sources are becoming",
	}

	fmt.Printf("   Input BlogPost:\n")
	fmt.Printf("      Title:    %s\n", blogPost.Title)
	fmt.Printf("      Author:   %s\n", blogPost.Author)
	fmt.Printf("      Category: %s\n", blogPost.Category)
	fmt.Printf("      Tags:     %v\n", blogPost.Tags)
	fmt.Printf("      Body:     %q\n", blogPost.Body)

	result5, err := schemaflux.CompleteField(blogPost,
		schemaflux.NewCompleteFieldOptions("Body").
			WithMaxLength(200).
			WithIntelligence(schemaflux.Fast))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Println("\n   ? CompleteFieldResult:")
		fmt.Printf("      Field:      %s\n", result5.Field)
		fmt.Printf("      Original:   %q\n", result5.Original)
		fmt.Printf("      Completed:  %s\n", result5.Completed)
		fmt.Printf("      Length:     %d characters added\n", result5.Length)
		fmt.Printf("      ModelConfidence: %.2f\n", result5.ModelConfidence)
		fmt.Printf("\n   ?? Updated BlogPost.Body:\n      %s\n", result5.Data.Body)
	}

	// Example 6: Complete a product description
	fmt.Println("\n6??  Complete Product Description (using Name, Price, Category as context)")
	fmt.Println("-" + string(make([]byte, 60)))

	product := ProductDescription{
		Name:        "EcoSmart Wireless Earbuds",
		Price:       79.99,
		Category:    "Electronics",
		Description: "These premium wireless earbuds feature",
	}

	fmt.Printf("   Input ProductDescription:\n")
	fmt.Printf("      Name:        %s\n", product.Name)
	fmt.Printf("      Price:       $%.2f\n", product.Price)
	fmt.Printf("      Category:    %s\n", product.Category)
	fmt.Printf("      Description: %q\n", product.Description)

	result6, err := schemaflux.CompleteField(product,
		schemaflux.NewCompleteFieldOptions("Description").
			WithMaxLength(150).
			WithTemperature(0.5). // Moderate creativity for product copy
			WithIntelligence(schemaflux.Fast))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Println("\n   ? CompleteFieldResult:")
		fmt.Printf("      Field:      %s\n", result6.Field)
		fmt.Printf("      Original:   %q\n", result6.Original)
		fmt.Printf("      Completed:  %s\n", result6.Completed)
		fmt.Printf("      Length:     %d characters added\n", result6.Length)
		fmt.Printf("      ModelConfidence: %.2f\n", result6.ModelConfidence)
		fmt.Printf("\n   ?? Updated ProductDescription.Description:\n      %s\n", result6.Data.Description)
	}

	fmt.Println()
	fmt.Println("? Success! Partial text and struct fields completed with LLM intelligence")
}
