// Example 24: Cluster Operation
// Groups similar items semantically using LLM intelligence

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

	fmt.Println("=== Cluster Example ===")
	fmt.Println("Groups similar items semantically using LLM")
	fmt.Println()

	// Example 1: Cluster articles by topic
	fmt.Println("--- Example 1: Article Clustering ---")

	type Article struct {
		ID       int    `json:"id"`
		Title    string `json:"title"`
		Category string `json:"category"`
	}

	articles := []Article{
		{ID: 1, Title: "Python is a versatile programming language for data science", Category: "tech"},
		{ID: 2, Title: "Machine learning algorithms improve with more training data", Category: "ai"},
		{ID: 3, Title: "JavaScript frameworks like React are popular for web development", Category: "web"},
		{ID: 4, Title: "Deep learning uses neural networks with multiple layers", Category: "ai"},
		{ID: 5, Title: "Vue.js provides reactive data binding for web applications", Category: "web"},
		{ID: 6, Title: "Natural language processing enables text understanding", Category: "ai"},
		{ID: 7, Title: "TypeScript adds static typing to JavaScript", Category: "web"},
		{ID: 8, Title: "Convolutional neural networks excel at image recognition", Category: "ai"},
		{ID: 9, Title: "Angular is a comprehensive framework for enterprise apps", Category: "web"},
		{ID: 10, Title: "Transformer models have revolutionized NLP tasks", Category: "ai"},
	}

	fmt.Println("Input Articles:")
	for _, a := range articles {
		fmt.Printf("  {ID: %d, Title: %q, Category: %q}\n", a.ID, a.Title, a.Category)
	}
	fmt.Println()

	opts := ops.NewClusterOptions().
		WithNumClusters(3).
		WithNamingStrategy("descriptive").
		WithIncludeOutliers(true).
		WithIntelligence(types.Smart)

	result, err := ops.Cluster(articles, opts)
	if err != nil {
		log.Fatalf("Clustering failed: %v", err)
	}

	fmt.Printf("Output - Created %d clusters:\n", len(result.Clusters))
	for _, cluster := range result.Clusters {
		fmt.Printf("\n  Cluster: %s\n", cluster.Name)
		fmt.Printf("  Description: %s\n", cluster.Description)
		fmt.Printf("  Items (%d):\n", cluster.Size)
		for _, item := range cluster.Items {
			fmt.Printf("    ? Article{ID: %d, Title: %q}\n", item.ID, item.Title)
		}
	}
	fmt.Println()

	// Example 2: Cluster support tickets
	fmt.Println("--- Example 2: Support Ticket Clustering ---")
	type Ticket struct {
		ID      int    `json:"id"`
		Title   string `json:"title"`
		Content string `json:"content"`
	}

	tickets := []Ticket{
		{ID: 1, Title: "Login issue", Content: "Cannot log into my account, password reset not working"},
		{ID: 2, Title: "Slow performance", Content: "The application is very slow when loading dashboard"},
		{ID: 3, Title: "Payment failed", Content: "My credit card payment was declined"},
		{ID: 4, Title: "Account locked", Content: "My account has been locked after multiple login attempts"},
		{ID: 5, Title: "Page load timeout", Content: "Getting timeout errors when accessing reports"},
		{ID: 6, Title: "Refund request", Content: "I need a refund for my subscription"},
		{ID: 7, Title: "Password recovery", Content: "The password reset email never arrives"},
		{ID: 8, Title: "App crashes", Content: "Application crashes when generating large reports"},
	}

	fmt.Println("Input Tickets:")
	for _, t := range tickets {
		fmt.Printf("  {ID: %d, Title: %q, Content: %q}\n", t.ID, t.Title, t.Content)
	}
	fmt.Println()

	ticketOpts := ops.NewClusterOptions().
		WithNumClusters(0). // Auto-detect number of clusters
		WithNamingStrategy("descriptive").
		WithSimilarityThreshold(0.6).
		WithIntelligence(types.Smart)

	ticketResult, err := ops.Cluster(tickets, ticketOpts)
	if err != nil {
		log.Fatalf("Ticket clustering failed: %v", err)
	}

	fmt.Printf("Output - Auto-detected %d ticket clusters:\n", len(ticketResult.Clusters))
	for _, cluster := range ticketResult.Clusters {
		fmt.Printf("\n  Category: %s\n", cluster.Name)
		fmt.Printf("  Keywords: %v\n", cluster.Keywords)
		fmt.Printf("  Tickets (%d):\n", cluster.Size)
		for _, item := range cluster.Items {
			fmt.Printf("    ? Ticket{ID: %d, Title: %q}\n", item.ID, item.Title)
		}
	}

	// Example 3: Outlier Detection
	fmt.Println("\n--- Example 3: Outlier Detection ---")
	if len(ticketResult.Outliers) > 0 {
		fmt.Printf("Found %d outlier items that don't fit clusters:\n", len(ticketResult.Outliers))
		for _, outlier := range ticketResult.Outliers {
			fmt.Printf("  ? Ticket{ID: %d, Title: %q}\n", outlier.ID, outlier.Title)
		}
	} else {
		fmt.Println("No outliers detected - all items fit into clusters")
	}

	fmt.Println("\n=== Cluster Example Complete ===")
}
