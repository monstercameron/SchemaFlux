// package ops - Decompose operation for breaking complex items into parts
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// DecomposeOptions configures the Decompose operation
type DecomposeOptions struct {
	CommonOptions
	types.OpOptions

	// Decomposition strategy ("hierarchical", "sequential", "parallel", "functional")
	Strategy string

	// Maximum depth of decomposition
	MaxDepth int

	// Minimum granularity (stop decomposing when items are this small)
	MinGranularity string

	// Target number of parts (0 for auto)
	TargetParts int

	// Include dependencies between parts
	IncludeDependencies bool

	// Include time/effort estimates for parts
	IncludeEstimates bool

	// Decomposition criteria
	DecomposeBy string

	// Keep parent-child relationships
	PreserveHierarchy bool
}

// NewDecomposeOptions creates DecomposeOptions with defaults
func NewDecomposeOptions() DecomposeOptions {
	return DecomposeOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		Strategy:            "hierarchical",
		MaxDepth:            3,
		TargetParts:         0,
		IncludeDependencies: true,
		IncludeEstimates:    false,
		PreserveHierarchy:   true,
	}
}

// Validate validates DecomposeOptions
func (d DecomposeOptions) Validate() error {
	if err := d.CommonOptions.Validate(); err != nil {
		return err
	}
	validStrategies := map[string]bool{"hierarchical": true, "sequential": true, "parallel": true, "functional": true}
	if d.Strategy != "" && !validStrategies[d.Strategy] {
		return fmt.Errorf("invalid strategy: %s", d.Strategy)
	}
	if d.MaxDepth < 1 {
		return fmt.Errorf("max depth must be at least 1, got %d", d.MaxDepth)
	}
	if d.TargetParts < 0 {
		return fmt.Errorf("target parts cannot be negative, got %d", d.TargetParts)
	}
	return nil
}

// WithStrategy sets the decomposition strategy
func (d DecomposeOptions) WithStrategy(strategy string) DecomposeOptions {
	d.Strategy = strategy
	return d
}

// WithMaxDepth sets the maximum decomposition depth
func (d DecomposeOptions) WithMaxDepth(depth int) DecomposeOptions {
	d.MaxDepth = depth
	return d
}

// WithMinGranularity sets the minimum granularity
func (d DecomposeOptions) WithMinGranularity(granularity string) DecomposeOptions {
	d.MinGranularity = granularity
	return d
}

// WithTargetParts sets the target number of parts
func (d DecomposeOptions) WithTargetParts(parts int) DecomposeOptions {
	d.TargetParts = parts
	return d
}

// WithIncludeDependencies enables dependency tracking
func (d DecomposeOptions) WithIncludeDependencies(include bool) DecomposeOptions {
	d.IncludeDependencies = include
	return d
}

// WithIncludeEstimates enables time/effort estimates
func (d DecomposeOptions) WithIncludeEstimates(include bool) DecomposeOptions {
	d.IncludeEstimates = include
	return d
}

// WithDecomposeBy sets the decomposition criteria
func (d DecomposeOptions) WithDecomposeBy(criteria string) DecomposeOptions {
	d.DecomposeBy = criteria
	return d
}

// WithPreserveHierarchy preserves parent-child relationships
func (d DecomposeOptions) WithPreserveHierarchy(preserve bool) DecomposeOptions {
	d.PreserveHierarchy = preserve
	return d
}

// WithSteering sets the steering prompt
func (d DecomposeOptions) WithSteering(steering string) DecomposeOptions {
	d.CommonOptions = d.CommonOptions.WithSteering(steering)
	return d
}

// WithMode sets the mode
func (d DecomposeOptions) WithMode(mode types.Mode) DecomposeOptions {
	d.CommonOptions = d.CommonOptions.WithMode(mode)
	return d
}

// WithIntelligence sets the intelligence level
func (d DecomposeOptions) WithIntelligence(intelligence types.Speed) DecomposeOptions {
	d.CommonOptions = d.CommonOptions.WithIntelligence(intelligence)
	return d
}

func (d DecomposeOptions) toOpOptions() types.OpOptions {
	return d.CommonOptions.toOpOptions()
}

// DecomposedPart represents a single part of decomposition
type DecomposedPart[T any] struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Description  string         `json:"description,omitempty"`
	Content      T              `json:"content"`
	ParentID     string         `json:"parent_id,omitempty"`
	Children     []string       `json:"children,omitempty"`
	Dependencies []string       `json:"dependencies,omitempty"`
	Depth        int            `json:"depth"`
	Order        int            `json:"order"`
	Estimate     string         `json:"estimate,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// DecomposeResult contains the results of decomposition
type DecomposeResult[T any] struct {
	Original     T                   `json:"original"`
	Parts        []DecomposedPart[T] `json:"parts"`
	RootParts    []string            `json:"root_parts"`
	TotalParts   int                 `json:"total_parts"`
	MaxDepth     int                 `json:"max_depth"`
	Dependencies map[string][]string `json:"dependencies,omitempty"`
	Metadata     map[string]any      `json:"metadata,omitempty"`
}

// Decompose breaks complex items into smaller component parts.
// The inverse of Merge - useful for task breakdown, chunking, and analysis.
//
// Type parameter T specifies the type of the input and output parts.
//
// Examples:
//
//	// Decompose a complex task
//	result, err := Decompose(complexTask, NewDecomposeOptions().
//	    WithStrategy("hierarchical").
//	    WithIncludeEstimates(true))
//
//	// Break down requirements
//	result, err := Decompose(requirement, NewDecomposeOptions().
//	    WithTargetParts(5).
//	    WithIncludeDependencies(true))
//
//	// Chunk a document
//	result, err := Decompose(document, NewDecomposeOptions().
//	    WithStrategy("sequential").
//	    WithDecomposeBy("logical sections"))
func Decompose[T any](input T, opts DecomposeOptions) (DecomposeResult[T], error) {
	log := logger.GetLogger()
	log.Debug("Starting decompose operation")

	var result DecomposeResult[T]
	result.Original = input
	result.Metadata = make(map[string]any)
	result.Dependencies = make(map[string][]string)

	// Validate options
	if err := opts.Validate(); err != nil {
		return result, fmt.Errorf("invalid options: %w", err)
	}

	opt := opts.toOpOptions()

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	// Get type information for output
	outputType := reflect.TypeOf(input)
	typeSchema := GenerateTypeSchema(outputType)

	// Marshal input
	inputJSON, err := json.Marshal(input)
	if err != nil {
		log.Error("Decompose operation failed: marshal error", "error", err)
		return result, fmt.Errorf("failed to marshal input: %w", err)
	}

	// Build strategy description
	strategyDesc := ""
	switch opts.Strategy {
	case "hierarchical":
		strategyDesc = "Break down into a tree structure with parent-child relationships."
	case "sequential":
		strategyDesc = "Break down into sequential steps or stages that follow one another."
	case "parallel":
		strategyDesc = "Break down into independent parts that can be processed in parallel."
	case "functional":
		strategyDesc = "Break down by function or responsibility."
	}

	targetDesc := ""
	if opts.TargetParts > 0 {
		targetDesc = fmt.Sprintf("\nTarget approximately %d parts.", opts.TargetParts)
	}

	granularityDesc := ""
	if opts.MinGranularity != "" {
		granularityDesc = fmt.Sprintf("\nStop decomposing when parts reach this granularity: %s", opts.MinGranularity)
	}

	decomposeByDesc := ""
	if opts.DecomposeBy != "" {
		decomposeByDesc = fmt.Sprintf("\nDecompose by: %s", opts.DecomposeBy)
	}

	dependencyNote := ""
	if opts.IncludeDependencies {
		dependencyNote = "\nIdentify dependencies between parts (which parts depend on others)."
	}

	estimateNote := ""
	if opts.IncludeEstimates {
		estimateNote = "\nInclude time/effort estimates for each part."
	}

	if opts.PreserveHierarchy {
		estimateNote += "\nPreserve the nesting of the source: a part derived from a sub-section stays under its parent rather than being flattened to the top level."
	}

	systemPrompt := fmt.Sprintf(`You are an expert at breaking down complex items into manageable parts.

Strategy: %s%s%s%s%s%s

Maximum depth: %d

Each part should match this schema:
%s

Return a JSON object with:
{
  "parts": [
    {
      "id": "unique-id",
      "name": "Part Name",
      "description": "What this part covers",
      "content": <content matching schema>,
      "parent_id": "parent-id or empty for root",
      "dependencies": ["id1", "id2"],
      "depth": 0,
      "order": 1,
      "estimate": "2 hours"
    }
  ],
  "root_parts": ["id1", "id2"]
}`, strategyDesc, targetDesc, granularityDesc, decomposeByDesc, dependencyNote, estimateNote, opts.MaxDepth, typeSchema)

	userPrompt := fmt.Sprintf("Decompose this into parts:\n\n%s", string(inputJSON))

	var parsed struct {
		Parts     []DecomposedPart[T] `json:"parts"`
		RootParts []string            `json:"root_parts"`
	}
	// Declare the response shape rather than only describing it in prose
	// (S-002, CI-008). The schema is generated from the very struct the answer
	// is decoded into, so it cannot drift from what this operation actually
	// accepts -- and a provider with structured output enforces it instead of
	// being asked politely.
	opt.ResponseFormat = "json"
	if schema := GenerateJSONSchema(reflect.TypeOf(parsed)); schema != nil {
		opt.JSONSchema = schema
		opt.SchemaName = "decompose_response"
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Decompose operation LLM call failed", "error", err)
		return result, fmt.Errorf("decomposition failed: %w", err)
	}

	// Parse the response

	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("Decompose operation failed: parse error", "error", err)
		return result, fmt.Errorf("failed to parse decomposition result: %w", err)
	}

	result.Parts = parsed.Parts
	result.RootParts = parsed.RootParts
	result.TotalParts = len(parsed.Parts)

	// Calculate max depth and build dependency map
	for _, part := range result.Parts {
		if part.Depth > result.MaxDepth {
			result.MaxDepth = part.Depth
		}
		if len(part.Dependencies) > 0 {
			result.Dependencies[part.ID] = part.Dependencies
		}
	}

	log.Debug("Decompose operation succeeded", "totalParts", result.TotalParts, "maxDepth", result.MaxDepth)
	return result, nil
}

// DecomposeToSlice is a convenience function that returns just the flat list of parts
func DecomposeToSlice[T any, U any](input T, opts DecomposeOptions) ([]U, error) {
	log := logger.GetLogger()
	log.Debug("Starting decompose to slice operation")

	var result []U

	// Validate options
	if err := opts.Validate(); err != nil {
		return result, fmt.Errorf("invalid options: %w", err)
	}

	opt := opts.toOpOptions()

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	// Get type information
	inputType := reflect.TypeOf(input)
	var outputSample U
	outputType := reflect.TypeOf(outputSample)

	inputSchema := GenerateTypeSchema(inputType)
	outputSchema := GenerateTypeSchema(outputType)

	// Marshal input
	inputJSON, err := json.Marshal(input)
	if err != nil {
		log.Error("DecomposeToSlice failed: marshal error", "error", err)
		return result, fmt.Errorf("failed to marshal input: %w", err)
	}

	targetDesc := ""
	if opts.TargetParts > 0 {
		targetDesc = fmt.Sprintf("Create approximately %d parts.", opts.TargetParts)
	}

	systemPrompt := fmt.Sprintf(`You are an expert at breaking down complex items.

Input schema:
%s

Output part schema:
%s

Strategy: %s
%s

Return a JSON array of parts matching the output schema.`, inputSchema, outputSchema, opts.Strategy, targetDesc)

	userPrompt := fmt.Sprintf("Break this down into parts:\n\n%s", string(inputJSON))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("DecomposeToSlice LLM call failed", "error", err)
		return result, fmt.Errorf("decomposition failed: %w", err)
	}

	if err := ParseJSONStrict(response, &result); err != nil {
		var wrapped struct {
			Parts []U `json:"parts"`
		}
		if err2 := ParseJSONStrict(response, &wrapped); err2 == nil && len(wrapped.Parts) > 0 {
			result = wrapped.Parts
		} else {
			var single U
			if err2 := ParseJSONStrict(response, &single); err2 == nil {
				result = []U{single}
			} else {
				log.Error("DecomposeToSlice failed: parse error", "error", err)
				return result, fmt.Errorf("failed to parse parts: %w", err)
			}
		}
	}

	log.Debug("DecomposeToSlice succeeded", "partCount", len(result))
	return result, nil
}
