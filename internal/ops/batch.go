// package ops - Batch operations for efficient bulk processing
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// BatchMode defines how batch operations are processed
type BatchMode int

const (
	// ParallelMode processes items concurrently with separate API calls
	ParallelMode BatchMode = iota
	// MergedMode combines multiple items into a single API call
	MergedMode
)

// BatchProcessor handles batch operations with different processing modes
type BatchProcessor struct {
	provider      llm.Provider
	mode          BatchMode
	maxConcurrent int
	maxBatchSize  int
	timeout       time.Duration
}

// BatchResult contains the results of a batch operation
type BatchResult[T any] struct {
	Results  []T
	Errors   []error
	Metadata BatchMetadata
}

// BatchMetadata provides metrics about the batch operation
type BatchMetadata struct {
	Mode         BatchMode
	TotalItems   int
	Succeeded    int
	Failed       int
	Duration     time.Duration
	APICallsMade int
}

// NewBatchProcessor creates a new batch processor for a given provider.
func NewBatchProcessor(provider llm.Provider) *BatchProcessor {
	return &BatchProcessor{
		provider:      provider,
		mode:          ParallelMode,
		maxConcurrent: 10,
		maxBatchSize:  50,
		timeout:       5 * time.Minute,
	}
}

// Global Batch function for backward compatibility
func Batch() *BatchProcessor {
	return NewBatchProcessor(defaultProvider)
}

// WithMode sets the batch processing mode
func (batchProcessor *BatchProcessor) WithMode(mode BatchMode) *BatchProcessor {
	batchProcessor.mode = mode
	return batchProcessor
}

// WithConcurrency sets the maximum concurrent operations for ParallelMode
func (batchProcessor *BatchProcessor) WithConcurrency(concurrency int) *BatchProcessor {
	batchProcessor.maxConcurrent = concurrency
	return batchProcessor
}

// WithBatchSize sets the maximum items per API call for MergedMode
func (batchProcessor *BatchProcessor) WithBatchSize(size int) *BatchProcessor {
	batchProcessor.maxBatchSize = size
	return batchProcessor
}

// WithTimeout sets the timeout for the batch operation
func (batchProcessor *BatchProcessor) WithTimeout(timeout time.Duration) *BatchProcessor {
	batchProcessor.timeout = timeout
	return batchProcessor
}

// ExtractBatch performs batch extraction based on the configured mode
// Note: Go doesn't support type parameters on methods, so we use a function
func ExtractBatch[T any](batchProcessor *BatchProcessor, inputs []interface{}, opts ...types.OpOptions) BatchResult[T] {
	// Convert legacy OpOptions to ExtractOptions for compatibility
	var extractOpts ExtractOptions
	if len(opts) > 0 {
		converted := ConvertOpOptions(opts[0], "extract")
		if eo, ok := converted.(ExtractOptions); ok {
			extractOpts = eo
		} else {
			extractOpts = NewExtractOptions()
		}
	} else {
		extractOpts = NewExtractOptions()
	}

	switch batchProcessor.mode {
	case MergedMode:
		return extractMerged[T](batchProcessor, inputs, extractOpts)
	default:
		return extractParallel[T](batchProcessor, inputs, extractOpts)
	}
}

// extractParallel processes items concurrently with separate API calls
func extractParallel[T any](batchProcessor *BatchProcessor, inputs []interface{}, opts ExtractOptions) BatchResult[T] {
	startTime := time.Now()
	results := make([]T, len(inputs))
	errors := make([]error, len(inputs))

	// Semaphore for concurrency control
	semaphore := make(chan struct{}, batchProcessor.maxConcurrent)
	var wg sync.WaitGroup

	ctx, cancel := context.WithTimeout(context.Background(), batchProcessor.timeout)
	defer cancel()

	apiCalls := 0
	var apiCallsMu sync.Mutex

	for i, input := range inputs {
		wg.Add(1)
		go func(idx int, input interface{}) {
			defer wg.Done()

			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				errors[idx] = ctx.Err()
				return
			}

			result, err := Extract[T](input, opts)
			if err == nil {
				results[idx] = result
				apiCallsMu.Lock()
				apiCalls++
				apiCallsMu.Unlock()
			} else {
				errors[idx] = err
			}
		}(i, input)
	}

	wg.Wait()

	// Calculate metadata
	succeeded := 0
	for _, err := range errors {
		if err == nil {
			succeeded++
		}
	}

	return BatchResult[T]{
		Results: results,
		Errors:  errors,
		Metadata: BatchMetadata{
			Mode:         ParallelMode,
			TotalItems:   len(inputs),
			Succeeded:    succeeded,
			Failed:       len(inputs) - succeeded,
			Duration:     time.Since(startTime),
			APICallsMade: apiCalls,
		},
	}
}

// extractMerged combines multiple items into fewer API calls
func extractMerged[T any](batchProcessor *BatchProcessor, inputs []interface{}, opts ExtractOptions) BatchResult[T] {
	startTime := time.Now()
	var allResults []T
	var allErrors []error
	apiCalls := 0

	// Process in chunks
	chunks := batchProcessor.createChunks(inputs, batchProcessor.maxBatchSize)

	for _, chunk := range chunks {
		// Create merged prompt
		mergedPrompt := batchProcessor.createMergedExtractPrompt(chunk)

		// Get type information for response parsing
		var sample T
		typeInfo := GenerateTypeSchema(reflect.TypeOf(sample))

		systemPrompt := fmt.Sprintf(`You are a data extraction expert. Extract structured data for multiple items.

Output JSON array where each element matches this schema:
%s

Return format: [{"index": 0, "data": {...}}, {"index": 1, "data": {...}}, ...]`, typeInfo)

		opOptions := opts.toOpOptions()
		ctx, cancel := context.WithTimeout(context.Background(), batchProcessor.timeout)

		// Use provider from batchProcessor if available, otherwise default
		var response string
		var err error
		if batchProcessor.provider != nil {
			response, err = CallLLM(ctx, batchProcessor.provider, systemPrompt, mergedPrompt, opOptions)
		} else {
			response, err = callLLM(ctx, systemPrompt, mergedPrompt, opOptions)
		}
		cancel()

		if err != nil {
			// Add error for each item in chunk
			for range chunk {
				allErrors = append(allErrors, err)
				allResults = append(allResults, *new(T))
			}
			continue
		}

		apiCalls++

		// Parse merged response
		results, parseErrors := parseMergedResponse[T](response, len(chunk))
		allResults = append(allResults, results...)
		allErrors = append(allErrors, parseErrors...)

		// Estimate tokens saved (rough calculation)
	}

	// Calculate metadata
	succeeded := 0
	for _, err := range allErrors {
		if err == nil {
			succeeded++
		}
	}

	return BatchResult[T]{
		Results: allResults,
		Errors:  allErrors,
		Metadata: BatchMetadata{
			Mode:         MergedMode,
			TotalItems:   len(inputs),
			Succeeded:    succeeded,
			Failed:       len(inputs) - succeeded,
			Duration:     time.Since(startTime),
			APICallsMade: apiCalls,
		},
	}
}

// createChunks splits inputs into chunks of specified size
func (batchProcessor *BatchProcessor) createChunks(inputs []interface{}, chunkSize int) [][]interface{} {
	var chunks [][]interface{}

	for i := 0; i < len(inputs); i += chunkSize {
		end := i + chunkSize
		if end > len(inputs) {
			end = len(inputs)
		}
		chunks = append(chunks, inputs[i:end])
	}

	return chunks
}

// createMergedExtractPrompt creates a single prompt for multiple items
func (batchProcessor *BatchProcessor) createMergedExtractPrompt(items []interface{}) string {
	prompt := "Extract structured data for each of the following items:\n\n"

	for i, item := range items {
		prompt += fmt.Sprintf("Item %d:\n%v\n\n", i, item)
	}

	prompt += "Return a JSON array with extracted data for each item in order."
	return prompt
}

// parseMergedResponse parses the response from a merged API call
func parseMergedResponse[T any](response string, expectedCount int) ([]T, []error) {
	results := make([]T, expectedCount)
	errors := make([]error, expectedCount)

	// Try to parse as JSON array
	var parsed []struct {
		Index int             `json:"index"`
		Data  json.RawMessage `json:"data"`
	}

	if err := ParseJSONStrict(response, &parsed); err != nil {
		// If parsing fails, return error for all items
		for i := range errors {
			errors[i] = fmt.Errorf("failed to parse merged response: %w", err)
		}
		return results, errors
	}

	// Map parsed results to correct indices
	for _, item := range parsed {
		if item.Index >= 0 && item.Index < expectedCount {
			var result T
			if err := json.Unmarshal(item.Data, &result); err != nil {
				errors[item.Index] = err
			} else {
				results[item.Index] = result
			}
		}
	}

	// Mark any missing indices as errors
	for i := range results {
		if errors[i] == nil {
			// Check if we got a result for this index
			var zero T
			if reflect.DeepEqual(results[i], zero) {
				errors[i] = fmt.Errorf("no result for index %d", i)
			}
		}
	}

	return results, errors
}

// SmartBatch automatically selects the best batch mode based on input characteristics
type SmartBatch struct {
	provider llm.Provider
}

// NewSmartBatch creates an intelligent batch processor.
func NewSmartBatch(provider llm.Provider) *SmartBatch {
	return &SmartBatch{provider: provider}
}

// ExtractSmart automatically chooses the best batch mode
func ExtractSmart[T any](smartBatch *SmartBatch, inputs []interface{}, opts ...types.OpOptions) BatchResult[T] {
	mode := smartBatch.determineBestMode(inputs)

	batch := NewBatchProcessor(smartBatch.provider)
	batch = batch.WithMode(mode)

	// Adjust parameters based on mode
	if mode == MergedMode {
		batch = batch.WithBatchSize(20)
	} else {
		batch = batch.WithConcurrency(10)
	}

	return ExtractBatch[T](batch, inputs, opts...)
}

// determineBestMode analyzes inputs to choose the optimal processing mode
func (smartBatch *SmartBatch) determineBestMode(inputs []interface{}) BatchMode {
	// Use MergedMode for many similar, simple items
	if len(inputs) > 20 {
		return MergedMode
	}

	// Check similarity of inputs
	if smartBatch.areInputsSimilar(inputs) {
		return MergedMode
	}

	// Default to ParallelMode for better error isolation
	return ParallelMode
}

// areInputsSimilar checks if inputs are similar enough for merged processing
func (smartBatch *SmartBatch) areInputsSimilar(inputs []interface{}) bool {
	if len(inputs) < 3 {
		return false // Too few items to benefit from merging
	}

	// Simple heuristic: check if all inputs are the same type
	firstType := reflect.TypeOf(inputs[0])
	for _, input := range inputs[1:] {
		if reflect.TypeOf(input) != firstType {
			return false
		}
	}

	return true
}
