package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "suggestions could not be generated: %v\nCheck your provider configuration and try again.\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "Suggest Example - AI-Powered Recommendations")
	fmt.Fprintln(out, strings.Repeat("=", 60))

	// Example 1: ETL Pipeline Optimization Suggestions
	fmt.Fprintln(out, "\n1. ETL Pipeline Optimization Suggestions")
	fmt.Fprintln(out, strings.Repeat("-", 60))
	etlContext := map[string]any{
		"task": "ETL pipeline optimization", "data_volume": "100GB daily",
		"issues":        []string{"slow processing", "high memory usage", "error rates"},
		"current_stack": []string{"Python", "Pandas", "PostgreSQL"},
	}
	fmt.Fprintln(out, "   Input Context:")
	fmt.Fprintf(out, "      Task:   %s\n", etlContext["task"])
	fmt.Fprintf(out, "      Volume: %s\n", etlContext["data_volume"])
	fmt.Fprintf(out, "      Issues: %v\n", etlContext["issues"])
	fmt.Fprintf(out, "      Stack:  %v\n", etlContext["current_stack"])
	if err := printSuggestions(ctx, out, etlContext, schemaflux.SuggestOptions{TopN: 5, Domain: "data-engineering"}); err != nil {
		return fmt.Errorf("ETL pipeline suggestions: %w", err)
	}

	// Example 2: API Design Suggestions
	fmt.Fprintln(out, "\n2. REST API Design Suggestions")
	fmt.Fprintln(out, strings.Repeat("-", 60))
	apiContext := map[string]any{
		"resource": "user profiles", "operations": []string{"create", "read", "update", "delete"},
		"constraints": []string{"RESTful", "versioned", "JWT authenticated"},
		"fields":      []string{"id", "name", "email", "preferences"},
	}
	fmt.Fprintln(out, "   Input Context:")
	fmt.Fprintf(out, "      Resource:    %s\n", apiContext["resource"])
	fmt.Fprintf(out, "      Operations:  %v\n", apiContext["operations"])
	fmt.Fprintf(out, "      Constraints: %v\n", apiContext["constraints"])
	if err := printSuggestions(ctx, out, apiContext, schemaflux.SuggestOptions{TopN: 4, Domain: "api-design", Strategy: "pattern"}); err != nil {
		return fmt.Errorf("REST API suggestions: %w", err)
	}

	// Example 3: Error Recovery with Constraints
	fmt.Fprintln(out, "\n3. Error Recovery Suggestions (with constraints)")
	fmt.Fprintln(out, strings.Repeat("-", 60))
	errorContext := map[string]any{
		"error": "database connection timeout", "component": "user authentication service",
		"frequency": "5 times per hour", "impact": "user login failures",
	}
	fmt.Fprintln(out, "   Input Context:")
	fmt.Fprintf(out, "      Error:     %s\n", errorContext["error"])
	fmt.Fprintf(out, "      Component: %s\n", errorContext["component"])
	fmt.Fprintf(out, "      Frequency: %s\n", errorContext["frequency"])
	fmt.Fprintf(out, "      Impact:    %s\n", errorContext["impact"])
	if err := printSuggestions(ctx, out, errorContext, schemaflux.SuggestOptions{TopN: 3, Domain: "reliability"}); err != nil {
		return fmt.Errorf("error recovery suggestions: %w", err)
	}

	// Example 4: Code Review Suggestions
	fmt.Fprintln(out, "\n4. Code Review Suggestions")
	fmt.Fprintln(out, strings.Repeat("-", 60))
	codeContext := map[string]any{
		"language": "Go", "file": "user_service.go",
		"issues":   []string{"no error handling", "hardcoded config", "missing tests"},
		"priority": "security and reliability",
	}
	fmt.Fprintln(out, "   Input Context:")
	fmt.Fprintf(out, "      Language: %s\n", codeContext["language"])
	fmt.Fprintf(out, "      File:     %s\n", codeContext["file"])
	fmt.Fprintf(out, "      Issues:   %v\n", codeContext["issues"])
	fmt.Fprintf(out, "      Priority: %s\n", codeContext["priority"])
	if err := printSuggestions(ctx, out, codeContext, schemaflux.SuggestOptions{TopN: 4, Domain: "code-quality"}); err != nil {
		return fmt.Errorf("code review suggestions: %w", err)
	}

	fmt.Fprintln(out, "\nSuccess! Context-aware suggestions generated with LLM intelligence")
	return nil
}

func printSuggestions(ctx context.Context, out io.Writer, input any, opts schemaflux.SuggestOptions) error {
	suggestions, err := schemaflux.Suggest[string](ctx, input, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\n   Suggestions (%d):\n", len(suggestions))
	for i, suggestion := range suggestions {
		fmt.Fprintf(out, "      %d. %s\n", i+1, suggestion)
	}
	return nil
}
