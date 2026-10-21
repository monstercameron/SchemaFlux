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
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== Rank Example ===")
	fmt.Fprintln(out, "Ranks items by semantic relevance to a query")
	fmt.Fprintln(out)

	// Example 1: Rank articles by relevance.
	fmt.Fprintln(out, "--- Example 1: Article Relevance Ranking ---")
	type Article struct {
		Title   string `json:"title"`
		Content string `json:"content"`
		Author  string `json:"author"`
	}
	articles := []Article{
		{Title: "Introduction to Python", Content: "Python basics for beginners", Author: "Alice"},
		{Title: "Advanced Go Patterns", Content: "Design patterns in Go programming", Author: "Bob"},
		{Title: "Go Concurrency Deep Dive", Content: "Goroutines and channels explained", Author: "Charlie"},
		{Title: "Web Development with React", Content: "Building modern web apps", Author: "Diana"},
		{Title: "Go Performance Optimization", Content: "Making Go programs faster", Author: "Eve"},
		{Title: "Python Data Science", Content: "Using pandas and numpy", Author: "Frank"},
	}
	fmt.Fprintln(out, "INPUT: []Article")
	for _, a := range articles {
		fmt.Fprintf(out, "  {Title: %q, Content: %q, Author: %q}\n", a.Title, a.Content, a.Author)
	}
	query := "Go programming patterns and best practices"
	fmt.Fprintf(out, "\nQuery: %q\n\n", query)
	result, err := schemaflux.Rank(ctx, articles, query, schemaflux.RankOptions{TopK: 3, IncludeExplanation: true, Options: schemaflux.Options{Tier: schemaflux.TierSmart}})
	if err != nil {
		return fmt.Errorf("ranking articles failed; check the query and provider configuration: %w", err)
	}
	fmt.Fprintln(out, "OUTPUT: RankResult[Article]")
	fmt.Fprintf(out, "  TotalItems: %d, ReturnedItems: %d\n", result.TotalItems, len(result.Items))
	fmt.Fprintln(out, "  Items (RankedItem[Article]):")
	for i, item := range result.Items {
		fmt.Fprintf(out, "    %d. Score: %.2f\n", i+1, item.Score)
		fmt.Fprintf(out, "       Item: Article{Title: %q, Author: %q}\n", item.Item.Title, item.Item.Author)
		if item.Explanation != "" {
			fmt.Fprintf(out, "       Explanation: %s\n", item.Explanation)
		}
	}
	fmt.Fprintln(out)

	// Example 2: Rank candidates for a job.
	fmt.Fprintln(out, "--- Example 2: Candidate Ranking ---")
	type Candidate struct {
		Name       string   `json:"name"`
		Experience int      `json:"experience_years"`
		Skills     []string `json:"skills"`
		Education  string   `json:"education"`
	}
	candidates := []Candidate{
		{Name: "Alice", Experience: 5, Skills: []string{"Go", "Python", "Kubernetes"}, Education: "MS Computer Science"},
		{Name: "Bob", Experience: 3, Skills: []string{"Java", "Spring", "AWS"}, Education: "BS Software Engineering"},
		{Name: "Charlie", Experience: 7, Skills: []string{"Go", "Docker", "Terraform"}, Education: "PhD Distributed Systems"},
		{Name: "Diana", Experience: 2, Skills: []string{"JavaScript", "React", "Node"}, Education: "BS Computer Science"},
		{Name: "Eve", Experience: 4, Skills: []string{"Go", "gRPC", "Microservices"}, Education: "MS Software Engineering"},
	}
	fmt.Fprintln(out, "INPUT: []Candidate")
	for _, c := range candidates {
		fmt.Fprintf(out, "  {Name: %q, Experience: %d, Skills: %v}\n", c.Name, c.Experience, c.Skills)
	}
	jobQuery := "Senior Go developer with cloud infrastructure experience"
	fmt.Fprintf(out, "\nQuery: %q\n\n", jobQuery)
	candidateResult, err := schemaflux.Rank(ctx, candidates, jobQuery, schemaflux.RankOptions{
		TopK: 3, BoostFields: map[string]float64{"skills": 2.0, "experience_years": 1.5}, IncludeExplanation: true,
		Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("ranking candidates failed; check the query and provider configuration: %w", err)
	}
	fmt.Fprintln(out, "OUTPUT: RankResult[Candidate]")
	for i, item := range candidateResult.Items {
		fmt.Fprintf(out, "  %d. Score: %.2f\n", i+1, item.Score)
		fmt.Fprintf(out, "     Item: Candidate{Name: %q, Experience: %d, Skills: %v}\n", item.Item.Name, item.Item.Experience, item.Item.Skills)
		if item.Explanation != "" {
			fmt.Fprintf(out, "     Explanation: %s\n", item.Explanation)
		}
	}
	fmt.Fprintln(out)

	// Example 3: Boost desirable product fields and penalize suspicious listings.
	fmt.Fprintln(out, "--- Example 3: Ranking with Boost/Penalize ---")
	type Product struct {
		Name      string  `json:"name"`
		Price     float64 `json:"price"`
		Rating    float64 `json:"rating"`
		InStock   bool    `json:"in_stock"`
		SpamScore float64 `json:"spam_score"` // Higher means more suspicious.
	}
	products := []Product{
		{Name: "Premium Widget A", Price: 99.99, Rating: 4.8, InStock: true, SpamScore: 0.1},
		{Name: "Budget Widget B", Price: 29.99, Rating: 4.2, InStock: true, SpamScore: 0.8},
		{Name: "Luxury Widget C", Price: 199.99, Rating: 4.9, InStock: false, SpamScore: 0.05},
		{Name: "Standard Widget D", Price: 49.99, Rating: 4.5, InStock: true, SpamScore: 0.2},
	}
	productQuery := "Best value widget for everyday use"
	productResult, err := schemaflux.Rank(ctx, products, productQuery, schemaflux.RankOptions{
		BoostFields: map[string]float64{"rating": 2.0, "in_stock": 1.5}, PenalizeFields: map[string]float64{"spam_score": 0.5},
		Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("ranking products failed; check the query and provider configuration: %w", err)
	}
	fmt.Fprintf(out, "Query: '%s'\n", productQuery)
	fmt.Fprintln(out, "Ranked products:")
	for i, item := range productResult.Items {
		fmt.Fprintf(out, "  %d. %s - $%.2f (Rating: %.1f, Score: %.2f)\n", i+1, item.Item.Name, item.Item.Price, item.Item.Rating, item.Score)
	}
	fmt.Fprintln(out, "\n=== Rank Example Complete ===")
	return nil
}
