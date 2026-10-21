package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Cluster example failed. Check the provider configuration and try again:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== Cluster Example ===")
	fmt.Fprintln(out, "Groups similar items semantically using an LLM")
	fmt.Fprintln(out)

	// Example 1: Cluster articles by topic.
	fmt.Fprintln(out, "--- Example 1: Article Clustering ---")
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
	fmt.Fprintln(out, "Input Articles:")
	for _, a := range articles {
		fmt.Fprintf(out, "  {ID: %d, Title: %q, Category: %q}\n", a.ID, a.Title, a.Category)
	}
	fmt.Fprintln(out)

	// NumClusters is a target; MaxClusterSize is enforced by Go.
	result, err := schemaflux.Cluster(ctx, articles, schemaflux.ClusterOptions{
		Options:        schemaflux.Options{Tier: schemaflux.TierSmart},
		NumClusters:    3,
		NamingStrategy: "descriptive",
		MaxClusterSize: 10,
	})
	if err != nil {
		return fmt.Errorf("cluster articles: %w", err)
	}
	fmt.Fprintf(out, "Output - Created %d clusters:\n", len(result.Clusters))
	for _, cluster := range result.Clusters {
		fmt.Fprintf(out, "\n  Cluster: %s\n", cluster.Name)
		fmt.Fprintf(out, "  Description: %s\n", cluster.Description)
		fmt.Fprintf(out, "  Items (%d):\n", cluster.Size)
		for _, item := range cluster.Items {
			fmt.Fprintf(out, "    Article{ID: %d, Title: %q}\n", item.ID, item.Title)
		}
	}
	fmt.Fprintln(out)

	// Example 2: Cluster support tickets; NumClusters zero asks the model to choose.
	fmt.Fprintln(out, "--- Example 2: Support Ticket Clustering ---")
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
	fmt.Fprintln(out, "Input Tickets:")
	for _, t := range tickets {
		fmt.Fprintf(out, "  {ID: %d, Title: %q, Content: %q}\n", t.ID, t.Title, t.Content)
	}
	fmt.Fprintln(out)

	ticketResult, err := schemaflux.Cluster(ctx, tickets, schemaflux.ClusterOptions{
		Options:             schemaflux.Options{Tier: schemaflux.TierSmart},
		NamingStrategy:      "descriptive",
		SimilarityThreshold: 0.6,
	})
	if err != nil {
		return fmt.Errorf("cluster support tickets: %w", err)
	}
	fmt.Fprintf(out, "Output - Auto-detected %d ticket clusters:\n", len(ticketResult.Clusters))
	for _, cluster := range ticketResult.Clusters {
		fmt.Fprintf(out, "\n  Category: %s\n", cluster.Name)
		fmt.Fprintf(out, "  Keywords: %v\n", cluster.Keywords)
		fmt.Fprintf(out, "  Tickets (%d):\n", cluster.Size)
		for _, item := range cluster.Items {
			fmt.Fprintf(out, "    Ticket{ID: %d, Title: %q}\n", item.ID, item.Title)
		}
	}

	// Example 3: Outliers are items returned outside the partition's clusters.
	fmt.Fprintln(out, "\n--- Example 3: Outlier Detection ---")
	if len(ticketResult.Outliers) > 0 {
		fmt.Fprintf(out, "Found %d outlier items that don't fit clusters:\n", len(ticketResult.Outliers))
		for _, outlier := range ticketResult.Outliers {
			fmt.Fprintf(out, "  Ticket{ID: %d, Title: %q}\n", outlier.ID, outlier.Title)
		}
	} else {
		fmt.Fprintln(out, "No outliers detected - all items fit into clusters")
	}
	fmt.Fprintln(out, "\n=== Cluster Example Complete ===")
	return nil
}
