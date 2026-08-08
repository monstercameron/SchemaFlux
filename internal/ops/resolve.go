// package ops - Resolve operation for resolving conflicts between typed sources
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// ResolveOptions configures the Resolve operation
type ResolveOptions struct {
	// Strategy for resolving conflicts ("newest", "most-complete", "majority", "authoritative", "merge")
	Strategy string

	// AuthoritativeSource is the index of the source to prefer (for "authoritative" strategy)
	AuthoritativeSource int

	// FieldPriorities maps field names to preferred source indices
	FieldPriorities map[string]int

	// ConflictThreshold is the similarity below which values are considered conflicting (0.0-1.0)
	ConflictThreshold float64

	// Common options
	Steering     string
	Mode         types.Mode
	Intelligence types.Speed

	// Model pins the exact model for this call, bypassing the Intelligence
	// tier's mapping (TC-005). These option types spell their common fields
	// out rather than embedding CommonOptions, so the pin has to be declared
	// here too -- otherwise `.Model(...)` on the builder would be a setter
	// that changes nothing, which this repository fails the build over.
	Model         string
	Context       context.Context
	RequestID     string
	CorrelationID string
}

// Conflict describes a disagreement between sources
type Conflict struct {
	// Field is the field name where conflict occurred
	Field string `json:"field"`

	// Values maps source index to the conflicting value
	Values map[int]any `json:"values"`

	// Resolution describes how the conflict was resolved
	Resolution string `json:"resolution"`

	// ChosenSource is the index of the source whose value was chosen
	ChosenSource int `json:"chosen_source"`

	// ChosenValue is the value that was selected
	ChosenValue any `json:"chosen_value"`

	// Reasoning explains why this resolution was made
	Reasoning string `json:"reasoning,omitempty"`
}

// ResolveResult contains the resolved data and conflict information
type ResolveResult[T any] struct {
	// Resolved is the unified result
	Resolved T `json:"resolved"`

	// Conflicts lists all conflicts found and how they were resolved
	Conflicts []Conflict `json:"conflicts,omitempty"`

	// SourceContributions maps source index to fields contributed
	SourceContributions map[int][]string `json:"source_contributions"`

	// Strategy describes how resolution was performed
	Strategy string `json:"strategy"`

	// ModelConfidence in the resolution quality (0.0-1.0)
	ModelConfidence float64 `json:"confidence"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Resolve reconciles conflicts when multiple typed sources disagree.
//
// Type parameter T specifies the type of sources being resolved.
//
// Examples:
//
//	// Example 1: Merge duplicate customer records from different systems
//	type Customer struct {
//	    ID    string `json:"id"`
//	    Name  string `json:"name"`
//	    Email string `json:"email"`
//	    Phone string `json:"phone"`
//	}
//	sources := []Customer{
//	    {ID: "C1", Name: "John Smith", Email: "john@old.com", Phone: ""},
//	    {ID: "C1", Name: "John A. Smith", Email: "john@new.com", Phone: "555-1234"},
//	}
//	result, err := Resolve(sources, ResolveOptions{Strategy: "most-complete"})
//	fmt.Printf("Resolved: %s, %s\n", result.Resolved.Name, result.Resolved.Email)
//	for _, c := range result.Conflicts {
//	    fmt.Printf("Conflict on %s: chose %v\n", c.Field, c.ChosenValue)
//	}
//
//	// Example 2: Product data from multiple vendors
//	type Product struct {
//	    SKU   string  `json:"sku"`
//	    Name  string  `json:"name"`
//	    Price float64 `json:"price"`
//	    Stock int     `json:"stock"`
//	}
//	result, err := Resolve(vendorProducts, ResolveOptions{
//	    Strategy: "authoritative",
//	    AuthoritativeSource: 0, // Primary vendor
//	    FieldPriorities: map[string]int{"stock": 1}, // Real-time stock from source 1
//	})
//
//	// Example 3: Config from env, file, and defaults
//	result, err := Resolve([]Config{envConfig, fileConfig, defaultConfig}, ResolveOptions{
//	    Strategy: "newest",
//	    Steering: "Prefer environment variables over file config",
//	})
func Resolve[T any](sources []T, opts ...ResolveOptions) (ResolveResult[T], error) {
	log := logger.GetLogger()
	log.Debug("Starting resolve operation", "sourceCount", len(sources))

	var result ResolveResult[T]
	result.SourceContributions = make(map[int][]string)
	result.Metadata = make(map[string]any)

	if len(sources) == 0 {
		return result, fmt.Errorf("no sources to resolve")
	}

	if len(sources) == 1 {
		result.Resolved = sources[0]
		result.SourceContributions[0] = []string{"*"}
		result.Strategy = "single-source"
		result.ModelConfidence = 1.0
		return result, nil
	}

	// Apply defaults
	opt := ResolveOptions{
		Strategy:          "most-complete",
		ConflictThreshold: 0.8,
		Mode:              types.TransformMode,
		Intelligence:      types.Fast,
	}
	if len(opts) > 0 {
		opt = mergeResolveOptions(opt, opts[0])
	}

	result.Strategy = opt.Strategy

	// Get context
	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := operationContext(ctx, 0)
	defer cancel()

	// Convert sources to JSON with indices
	var sourcesJSON []string
	for i, source := range sources {
		sourceJSON, err := json.Marshal(source)
		if err != nil {
			log.Error("Resolve operation failed: marshal error", "sourceIndex", i, "error", err)
			return result, fmt.Errorf("failed to marshal source %d: %w", i, err)
		}
		sourcesJSON = append(sourcesJSON, fmt.Sprintf("Source %d: %s", i, string(sourceJSON)))
	}

	// Get type schema
	var zero T
	typeSchema := GenerateTypeSchema(reflect.TypeOf(zero))

	// Build field priorities description
	fieldPrioritiesDesc := ""
	if len(opt.FieldPriorities) > 0 {
		var parts []string
		for field, sourceIdx := range opt.FieldPriorities {
			parts = append(parts, fmt.Sprintf("- %s: prefer source %d", field, sourceIdx))
		}
		fieldPrioritiesDesc = fmt.Sprintf("\n\nField priorities:\n%s", strings.Join(parts, "\n"))
	}

	systemPrompt := fmt.Sprintf(`You are a data reconciliation expert. Resolve conflicts between multiple data sources.

Strategy: %s
Conflict threshold: %.0f%% similarity%s

Return a JSON object with:
{
  "resolved": %s,
  "conflicts": [
    {
      "field": "field_name",
      "values": {"0": "value from source 0", "1": "value from source 1"},
      "resolution": "how it was resolved",
      "chosen_source": 0,
      "chosen_value": "the chosen value",
      "reasoning": "why this value was chosen"
    }
  ],
  "source_contributions": {"0": ["field1", "field2"], "1": ["field3"]},
  "confidence": 0.0-1.0
}

Strategy explanations:
- "newest": Prefer more recent/updated data
- "most-complete": Prefer sources with more non-null fields
- "majority": Use value that appears most often
- "authoritative": Prefer source at index %d
- "merge": Combine best values from each source`,
		opt.Strategy, opt.ConflictThreshold*100, fieldPrioritiesDesc, typeSchema, opt.AuthoritativeSource)

	steeringNote := ""
	if opt.Steering != "" {
		steeringNote = fmt.Sprintf("\n\nAdditional guidance: %s", opt.Steering)
	}

	userPrompt := fmt.Sprintf(`Resolve conflicts between these sources:

%s%s`, strings.Join(sourcesJSON, "\n\n"), steeringNote)

	// Build OpOptions for LLM call
	opOpts := types.OpOptions{
		Mode:          opt.Mode,
		Intelligence:  opt.Intelligence,
		Model:         opt.Model,
		Context:       ctx,
		RequestID:     opt.RequestID,
		CorrelationID: opt.CorrelationID,
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opOpts)
	if err != nil {
		log.Error("Resolve operation LLM call failed", "error", err)
		return result, fmt.Errorf("resolution failed: %w", err)
	}

	// Parse response
	var parsed struct {
		Resolved            json.RawMessage     `json:"resolved"`
		Conflicts           []Conflict          `json:"conflicts"`
		SourceContributions map[string][]string `json:"source_contributions"`
		ModelConfidence     float64             `json:"confidence"`
	}

	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("Resolve operation failed: parse error", "error", err, "response", response)
		return result, fmt.Errorf("failed to parse resolution result: %w", err)
	}

	// Parse resolved data
	if len(parsed.Resolved) > 0 {
		if err := json.Unmarshal(parsed.Resolved, &result.Resolved); err != nil {
			log.Error("Resolve operation failed: resolved data parse error", "error", err)
			return result, fmt.Errorf("failed to parse resolved data: %w", err)
		}
	}

	result.Conflicts = parsed.Conflicts
	result.ModelConfidence = parsed.ModelConfidence

	// The "authoritative" strategy documents exactly one thing -- prefer the
	// source at AuthoritativeSource -- and each conflict reports the index it
	// actually chose. That is two integers, comparable in Go, with no model
	// claim in between; nothing was comparing them, so the strategy was prose
	// in a prompt and nothing else.
	//
	// The other strategies are deliberately not checked here. "newest" and
	// "most-complete" are judgements about the source data that only the model
	// made, and re-deriving them in Go would be inventing a second answer to
	// compare against the first.
	if opt.Strategy == "authoritative" {
		for i, conflict := range result.Conflicts {
			if conflict.ChosenSource != opt.AuthoritativeSource {
				log.Error("A conflict was resolved against the authoritative source",
					"conflictIndex", i, "field", conflict.Field,
					"chosenSource", conflict.ChosenSource, "authoritativeSource", opt.AuthoritativeSource)
				return result, fmt.Errorf(
					"resolve: strategy is \"authoritative\" on source %d, but conflict %d (%s) was resolved from source %d",
					opt.AuthoritativeSource, i, conflict.Field, conflict.ChosenSource)
			}
		}
	}

	// A conflict citing source 4 out of a three-source list resolved from
	// nothing. The index is the model's; the bound is the caller's.
	for i, conflict := range result.Conflicts {
		if conflict.ChosenSource < 0 || conflict.ChosenSource >= len(sources) {
			log.Error("A conflict cited a source index outside the supplied list",
				"conflictIndex", i, "chosenSource", conflict.ChosenSource, "sourceCount", len(sources))
			return result, fmt.Errorf(
				"resolve: conflict %d was resolved from source %d, but %d source(s) were supplied",
				i, conflict.ChosenSource, len(sources))
		}
	}

	// Convert source contributions from string keys to int keys
	for key, fields := range parsed.SourceContributions {
		var idx int
		fmt.Sscanf(key, "%d", &idx)
		result.SourceContributions[idx] = fields
	}

	log.Debug("Resolve operation succeeded",
		"conflicts", len(result.Conflicts),
		"confidence", result.ModelConfidence)

	return result, nil
}

// mergeResolveOptions merges user options with defaults
func mergeResolveOptions(defaults, user ResolveOptions) ResolveOptions {
	if user.Strategy != "" {
		defaults.Strategy = user.Strategy
	}
	if user.AuthoritativeSource != 0 {
		defaults.AuthoritativeSource = user.AuthoritativeSource
	}
	if user.FieldPriorities != nil {
		defaults.FieldPriorities = user.FieldPriorities
	}
	if user.ConflictThreshold > 0 {
		defaults.ConflictThreshold = user.ConflictThreshold
	}
	if user.Steering != "" {
		defaults.Steering = user.Steering
	}
	if user.Mode != 0 {
		defaults.Mode = user.Mode
	}
	if user.Intelligence != 0 {
		defaults.Intelligence = user.Intelligence
	}
	if user.Model != "" {
		defaults.Model = user.Model
	}
	if user.Context != nil {
		defaults.Context = user.Context
	}
	return defaults
}
