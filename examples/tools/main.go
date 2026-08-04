// Package main demonstrates all SchemaFlux tool primitives.
// Run with: go run ./examples/tools/...
package main

import (
	"context"
	"fmt"
	"os"
	stdstrings "strings"

	"github.com/monstercameron/schemaflux/internal/tools"
)

func main() {
	fmt.Println("╔══════════════════════════════════════════════════════════════════╗")
	fmt.Println("║           SchemaFlux Tool Primitives - Examples                  ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════════╝")
	fmt.Println()

	// List all registered tools
	allTools := tools.List()
	fmt.Printf("📦 Total registered tools: %d\n\n", len(allTools))

	// Group by category
	categories := tools.ToolCategories()
	for category, names := range categories {
		fmt.Printf("📁 %s (%d tools)\n", category, len(names))
		for _, name := range names {
			if tool, ok := tools.Get(name); ok {
				stub := ""
				if tool.IsStub {
					stub = " [stub]"
				}
				fmt.Printf("   • %s%s\n", name, stub)
			}
		}
		fmt.Println()
	}

	// Run examples if requested
	if len(os.Args) > 1 {
		runExamples(os.Args[1])
	} else {
		fmt.Println("Run with category name to see examples:")
		fmt.Println("  go run ./examples/tools/... compute")
		fmt.Println("  go run ./examples/tools/... data")
		fmt.Println("  go run ./examples/tools/... file")
		fmt.Println("  go run ./examples/tools/... http")
		fmt.Println("  go run ./examples/tools/... time")
		fmt.Println("  go run ./examples/tools/... finance")
		fmt.Println("  go run ./examples/tools/... template")
		fmt.Println("  go run ./examples/tools/... cache")
		fmt.Println("  go run ./examples/tools/... database")
		fmt.Println("  go run ./examples/tools/... ai")
		fmt.Println("  go run ./examples/tools/... all")
	}
}

func runExamples(category string) {
	ctx := context.Background()

	switch category {
	case "compute", "computation":
		runComputeExamples(ctx)
	case "data":
		runDataExamples(ctx)
	case "file":
		runFileExamples(ctx)
	case "http":
		runHTTPExamples(ctx)
	case "time":
		runTimeExamples(ctx)
	case "finance":
		runFinanceExamples(ctx)
	case "template":
		runTemplateExamples(ctx)
	case "cache", "security":
		runCacheSecurityExamples(ctx)
	case "database":
		runDatabaseExamples(ctx)
	case "ai", "audio", "messaging", "image", "exec":
		runAIExamples(ctx)
	case "all":
		runComputeExamples(ctx)
		runDataExamples(ctx)
		runFileExamples(ctx)
		runHTTPExamples(ctx)
		runTimeExamples(ctx)
		runFinanceExamples(ctx)
		runTemplateExamples(ctx)
		runCacheSecurityExamples(ctx)
		runDatabaseExamples(ctx)
		runAIExamples(ctx)
	default:
		fmt.Printf("Unknown category: %s\n", category)
	}
}

func printResult(name string, result tools.Result, err error) {
	fmt.Printf("\n🔧 %s\n", name)
	fmt.Println(strings.Repeat("-", 60))
	if err != nil {
		fmt.Printf("❌ Error: %v\n", err)
		return
	}
	if !result.Success {
		fmt.Printf("⚠️  Failed: %s\n", result.Error)
		return
	}
	fmt.Printf("✅ Result: %v\n", result.Data)
	if len(result.Metadata) > 0 {
		fmt.Printf("   Meta: %v\n", result.Metadata)
	}
}

// strings provides string utilities used across examples
var strings = struct {
	Repeat func(string, int) string
}{
	Repeat: stdstrings.Repeat,
}
