// 22-suggest: Generate context-aware suggestions using LLM intelligence
// Intelligence: Smart (default)
// Expectations:
// - Generates actionable recommendations based on context
// - Supports typed output (strings or custom structs)
// - Can filter by domain, constraints, and categories
// - Returns ranked suggestions

package main

import (
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/internal/exampleutil"
)

func main() {

	fmt.Println("?? Suggest Example - AI-Powered Recommendations")
	fmt.Println("=" + string(make([]byte, 60)))

	// Initialize SchemaFlux
	if err := exampleutil.Bootstrap(); err != nil {
		schemaflux.GetLogger().Error("Failed to initialize SchemaFlux", "error", err)
		return
	}

	// Example 1: ETL Pipeline Optimization Suggestions
	fmt.Println("\n1??  ETL Pipeline Optimization Suggestions")
	fmt.Println("-" + string(make([]byte, 60)))

	etlContext := map[string]any{
		"task":          "ETL pipeline optimization",
		"data_volume":   "100GB daily",
		"issues":        []string{"slow processing", "high memory usage", "error rates"},
		"current_stack": []string{"Python", "Pandas", "PostgreSQL"},
	}

	fmt.Printf("   Input Context:\n")
	fmt.Printf("      Task:   %s\n", etlContext["task"])
	fmt.Printf("      Volume: %s\n", etlContext["data_volume"])
	fmt.Printf("      Issues: %v\n", etlContext["issues"])
	fmt.Printf("      Stack:  %v\n", etlContext["current_stack"])

	suggestions1, err := schemaflux.Suggest[string](etlContext,
		schemaflux.NewSuggestOptions().
			WithTopN(5).
			WithDomain("data-engineering"))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Printf("\n   ? Suggestions (%d):\n", len(suggestions1))
		for i, s := range suggestions1 {
			fmt.Printf("      %d. %s\n", i+1, s)
		}
	}

	// Example 2: API Design Suggestions
	fmt.Println("\n2??  REST API Design Suggestions")
	fmt.Println("-" + string(make([]byte, 60)))

	apiContext := map[string]any{
		"resource":    "user profiles",
		"operations":  []string{"create", "read", "update", "delete"},
		"constraints": []string{"RESTful", "versioned", "JWT authenticated"},
		"fields":      []string{"id", "name", "email", "preferences"},
	}

	fmt.Printf("   Input Context:\n")
	fmt.Printf("      Resource:    %s\n", apiContext["resource"])
	fmt.Printf("      Operations:  %v\n", apiContext["operations"])
	fmt.Printf("      Constraints: %v\n", apiContext["constraints"])

	suggestions2, err := schemaflux.Suggest[string](apiContext,
		schemaflux.NewSuggestOptions().
			WithTopN(4).
			WithDomain("api-design").
			WithStrategy(schemaflux.SuggestPattern))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Printf("\n   ? Suggestions (%d):\n", len(suggestions2))
		for i, s := range suggestions2 {
			fmt.Printf("      %d. %s\n", i+1, s)
		}
	}

	// Example 3: Error Recovery with Constraints
	fmt.Println("\n3??  Error Recovery Suggestions (with constraints)")
	fmt.Println("-" + string(make([]byte, 60)))

	errorContext := map[string]any{
		"error":     "database connection timeout",
		"component": "user authentication service",
		"frequency": "5 times per hour",
		"impact":    "user login failures",
	}

	fmt.Printf("   Input Context:\n")
	fmt.Printf("      Error:     %s\n", errorContext["error"])
	fmt.Printf("      Component: %s\n", errorContext["component"])
	fmt.Printf("      Frequency: %s\n", errorContext["frequency"])
	fmt.Printf("      Impact:    %s\n", errorContext["impact"])

	suggestions3, err := schemaflux.Suggest[string](errorContext,
		schemaflux.NewSuggestOptions().
			WithTopN(3).
			WithDomain("reliability"))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Printf("\n   ? Suggestions (%d):\n", len(suggestions3))
		for i, s := range suggestions3 {
			fmt.Printf("      %d. %s\n", i+1, s)
		}
	}

	// Example 4: Code Review Suggestions
	fmt.Println("\n4??  Code Review Suggestions")
	fmt.Println("-" + string(make([]byte, 60)))

	codeContext := map[string]any{
		"language": "Go",
		"file":     "user_service.go",
		"issues":   []string{"no error handling", "hardcoded config", "missing tests"},
		"priority": "security and reliability",
	}

	fmt.Printf("   Input Context:\n")
	fmt.Printf("      Language: %s\n", codeContext["language"])
	fmt.Printf("      File:     %s\n", codeContext["file"])
	fmt.Printf("      Issues:   %v\n", codeContext["issues"])
	fmt.Printf("      Priority: %s\n", codeContext["priority"])

	suggestions4, err := schemaflux.Suggest[string](codeContext,
		schemaflux.NewSuggestOptions().
			WithTopN(4).
			WithDomain("code-quality"))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Printf("\n   ? Suggestions (%d):\n", len(suggestions4))
		for i, s := range suggestions4 {
			fmt.Printf("      %d. %s\n", i+1, s)
		}
	}

	fmt.Println()
	fmt.Println("? Success! Context-aware suggestions generated with LLM intelligence")
}
