/*
Package schemaflux provides type-safe, production-ready LLM operations for Go applications.

SchemaFlux simplifies working with Large Language Models by providing strongly-typed operations
that handle the complexity of prompt engineering, response parsing, and error handling.

# Features

  - Type-safe operations with Go generics
  - Multiple LLM provider support (OpenAI, Anthropic, local)
  - Batch processing with cost optimization
  - Pipeline and composition support
  - Procedural programming operations
  - Full backward compatibility

# Installation

	go get github.com/monstercameron/schemaflux

# Quick Start

Initialize the library with your API key:

	import "github.com/monstercameron/schemaflux"

	func main() {
	    schemaflux.Init("your-api-key")

	    // Extract structured data
	    type Person struct {
	        Name string `json:"name"`
	        Age  int    `json:"age"`
	    }

	    person, err := schemaflux.Extract[Person]("John Doe, 30 years old")
	}

# Client-Based Usage

For multiple configurations or providers:

	client := schemaflux.NewClient("api-key").
	    WithTimeout(30 * time.Second).
	    WithProvider("anthropic")

	person, err := schemaflux.ClientExtract[Person](client, input)

# Operation Categories

The library is organized into logical operation categories:

Core Operations (data_operations.go):
  - Extract: Extract structured data from text
  - Transform: Transform between data types
  - Generate: Generate new data from templates

Text Operations (text_operations.go):
  - Summarize: Create concise summaries
  - Rewrite: Rewrite with different styles
  - Translate: Translate between languages
  - Expand: Expand text with more detail

Analysis Operations (analysis_operations.go):
  - Classify: Categorize content
  - Score: Score based on criteria
  - Compare: Compare items
  - Similar: Find similar items

Collection Operations (collection_operations.go):
  - Choose: Select best option
  - Filter: Filter by criteria
  - Sort: Sort by criteria

Extended Operations (extended_operations.go):
  - Validate: Validate against rules
  - Format: Format data
  - Merge: Merge multiple sources
  - Question: Answer questions about data
  - Deduplicate: Remove duplicates

Batch Operations (batch_operations.go):
  - ParallelMode: Concurrent processing (5-10x faster)
  - MergedMode: Combined API calls (70-90% cost reduction)
  - SmartBatch: Automatic optimization

Pipeline Operations (pipeline.go):
  - Pipeline: Chain operations
  - Compose: Function composition
  - Map/Reduce: Collection processing

Procedural Operations (procedural_ops.go):
  - Decide: Multi-way decisions
  - StateMachine: State management
  - Workflow: Multi-step workflows
  - Guard: Condition checking

# Batch Processing

Process multiple items efficiently:

	// Parallel processing for speed
	batch := schemaflux.Batch().
	    WithMode(schemaflux.ParallelMode).
	    WithConcurrency(10)

	results := schemaflux.ExtractBatch[Person](batch, inputs)

	// Merged processing for cost savings
	batch := schemaflux.Batch().
	    WithMode(schemaflux.MergedMode).
	    WithBatchSize(50)

	results := schemaflux.ExtractBatch[Invoice](batch, invoices)

# Pipelines

Chain operations together:

	pipeline := schemaflux.NewPipeline("process").
	    Add("extract", extractOp).
	    Add("validate", validateOp).
	    Add("transform", transformOp)

	result := pipeline.Execute(ctx, input)

# Provider Support

Switch between different LLM providers:

	// OpenAI (default)
	client := schemaflux.NewClient(apiKey)

	// Anthropic
	client := schemaflux.NewClient(apiKey).WithProvider("anthropic")

	// Local/Mock for testing
	testClient := schemaflux.NewClient("").WithProvider("local")

# Configuration

Configure via environment variables:

	SCHEMAFLUX_API_KEY=your-api-key
	SCHEMAFLUX_PROVIDER=openai
	SCHEMAFLUX_TIMEOUT=30s
	SCHEMAFLUX_DEBUG=true

Or programmatically:

	client := schemaflux.NewClient(apiKey).
	    WithTimeout(60 * time.Second).
	    WithDebug(true)

# Error Handling

All operations return errors that should be checked:

	result, err := schemaflux.Extract[Data](input)
	if err != nil {
	    // Handle error
	    log.Printf("Extraction failed: %v", err)
	    return err
	}

# Performance

Different modes for different needs:

  - Single operations: Standard performance
  - Parallel batch: 5-10x throughput improvement
  - Merged batch: 70-90% cost reduction
  - Local provider: Instant, free testing

# Testing

Use the local provider for testing:

	testClient := schemaflux.NewClient("").WithProvider("local")

	// Configure custom responses
	provider := schemaflux.NewLocalProvider(schemaflux.ProviderConfig{})
	provider.WithHandler(func(ctx context.Context, req schemaflux.CompletionRequest) (string, error) {
	    return "test response", nil
	})

	testClient.WithProviderInstance(provider)

# Examples

See the examples/ directory for complete applications:
  - SmartTodo: AI-powered task management
  - Data processing pipelines
  - Document analysis
  - Multi-client configurations

# Best Practices

1. Use typed operations instead of raw LLM calls
2. Batch process when handling multiple items
3. Use appropriate intelligence levels (Quick/Fast/Smart)
4. Test with the local provider
5. Handle all errors appropriately
6. Use pipelines for complex workflows
7. Configure timeouts appropriately

# Thread Safety

All operations are thread-safe. Clients can be shared across goroutines.

# Links

Documentation: https://github.com/monstercameron/schemaflux
Issues: https://github.com/monstercameron/schemaflux/issues
Examples: https://github.com/monstercameron/schemaflux/examples
*/
package core
