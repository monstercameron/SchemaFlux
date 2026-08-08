// package ops - Compose operation for building complex objects from parts
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// ComposeOptions configures the Compose operation
type ComposeOptions struct {
	// Template provides a structure hint for composition
	Template string

	// MergeStrategy specifies how to handle conflicts ("first", "last", "combine", "smart")
	MergeStrategy string

	// FillGaps allows inferring missing required fields
	FillGaps bool

	// Validate ensures the composed result matches the target schema
	Validate bool

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

// ComposedField describes how a field was composed
type ComposedField struct {
	// Field is the path to the composed field
	Field string `json:"field"`

	// Sources lists which input parts contributed to this field
	Sources []int `json:"sources"`

	// Method describes how the value was determined
	// "single" - from one source, "merged" - combined from multiple, "inferred" - filled gap
	Method string `json:"method"`

	// Conflicts notes if there were conflicting values
	Conflicts bool `json:"conflicts"`

	// Resolution explains how conflicts were resolved (if any)
	Resolution string `json:"resolution,omitempty"`
}

// ComposeResult contains the composed object and assembly information
type ComposeResult[T any] struct {
	// Composed is the assembled object
	Composed T `json:"composed"`

	// FieldSources documents where each field came from
	FieldSources []ComposedField `json:"field_sources"`

	// ConflictsResolved counts how many conflicts were resolved
	ConflictsResolved int `json:"conflicts_resolved"`

	// GapsFilled lists fields that were inferred (not from any part)
	GapsFilled []string `json:"gaps_filled,omitempty"`

	// UnusedParts lists indices of parts that contributed nothing
	UnusedParts []int `json:"unused_parts,omitempty"`

	// Completeness is the ratio of filled fields to total required fields (0.0-1.0)
	Completeness float64 `json:"completeness"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Assemble builds a complex typed object from multiple parts.
//
// Type parameters:
//   - T: Target type to compose
//
// Examples:
//
//	// Example 1: Build company profile from multiple data sources
//	type CompanyProfile struct {
//	    Name        string   `json:"name"`
//	    Industry    string   `json:"industry"`
//	    Employees   int      `json:"employees"`
//	    Revenue     float64  `json:"revenue"`
//	    Products    []string `json:"products"`
//	    Competitors []string `json:"competitors"`
//	}
//	parts := []any{
//	    map[string]any{"name": "Acme Inc", "industry": "Tech"},          // LinkedIn
//	    map[string]any{"employees": 500, "revenue": 50000000},           // SEC filing
//	    map[string]any{"products": []string{"Widget", "Gadget"}},       // Product DB
//	    map[string]any{"competitors": []string{"TechCorp", "InnoLabs"}}, // Research
//	}
//	result, err := Assemble[CompanyProfile](parts, ComposeOptions{
//	    MergeStrategy: "smart",
//	})
//	for _, fs := range result.FieldSources {
//	    fmt.Printf("%s came from sources %v (%s)\n", fs.Field, fs.Sources, fs.Method)
//	}
//
//	// Example 2: Compose document from sections
//	type Document struct {
//	    Title      string `json:"title"`
//	    Abstract   string `json:"abstract"`
//	    Body       string `json:"body"`
//	    Conclusion string `json:"conclusion"`
//	}
//	result, err := Assemble[Document]([]any{titleSection, abstractSection, bodySection, conclusionSection}, ComposeOptions{
//	    Template: "Academic paper structure",
//	    FillGaps: true,
//	})
//	fmt.Printf("Completeness: %.0f%%\n", result.Completeness*100)
//
//	// Example 3: Merge user data from multiple forms
//	result, err := Assemble[UserProfile]([]any{step1Data, step2Data, step3Data}, ComposeOptions{
//	    MergeStrategy: "last",  // Later form submissions win
//	})
//	if result.ConflictsResolved > 0 {
//	    fmt.Printf("Resolved %d conflicts\n", result.ConflictsResolved)
//	}
func Assemble[T any](parts []any, opts ...ComposeOptions) (ComposeResult[T], error) {
	log := logger.GetLogger()
	log.Debug("Starting compose operation", "parts", len(parts))

	var result ComposeResult[T]
	result.Metadata = make(map[string]any)

	// Apply defaults
	opt := ComposeOptions{
		MergeStrategy: "smart",
		FillGaps:      false,
		Validate:      true,
		Mode:          types.TransformMode,
		Intelligence:  types.Smart,
	}
	if len(opts) > 0 {
		opt = mergeComposeOptions(opt, opts[0])
	}

	// Get context
	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	// Convert parts to JSON with indices
	partsJSON := make([]string, len(parts))
	for i, part := range parts {
		data, err := json.Marshal(part)
		if err != nil {
			log.Error("Compose operation failed: marshal error for part", "index", i, "error", err)
			return result, fmt.Errorf("failed to marshal part %d: %w", i, err)
		}
		partsJSON[i] = fmt.Sprintf(`{"index": %d, "data": %s}`, i, string(data))
	}
	allPartsJSON := "[" + strings.Join(partsJSON, ",\n") + "]"

	// Get target schema
	var zero T
	targetSchema := GenerateTypeSchema(reflect.TypeOf(zero))

	// Build template description
	templateDesc := ""
	if opt.Template != "" {
		templateDesc = fmt.Sprintf("\n\nTemplate/guidance: %s", opt.Template)
	}

	systemPrompt := fmt.Sprintf(`You are a data composition expert. Assemble a complete object from multiple parts.

Target schema: %s%s

Merge strategy: %s
Fill gaps: %v

Return a JSON object with:
{
  "composed": %s,
  "field_sources": [
    {
      "field": "path.to.field",
      "sources": [0, 2],
      "method": "single/merged/inferred",
      "conflicts": true/false,
      "resolution": "explanation if conflicts"
    }
  ],
  "conflicts_resolved": 0,
  "gaps_filled": ["field1", "field2"],
  "unused_parts": [1, 3],
  "completeness": 0.0-1.0
}

Merge strategies:
- "first": Use first source's value
- "last": Use last source's value
- "combine": Merge arrays, concatenate strings where appropriate
- "smart": Use best judgment based on data quality and completeness

Rules:
- Map each part's fields to the target schema
- Track which parts contributed to each field
- Note and resolve conflicts when multiple parts provide the same field
- Mark unused parts that contributed nothing
- Only fill gaps if FillGaps is true`,
		targetSchema, templateDesc, opt.MergeStrategy, opt.FillGaps, targetSchema)

	steeringNote := ""
	if opt.Steering != "" {
		steeringNote = fmt.Sprintf("\n\nAdditional guidance: %s", opt.Steering)
	}

	userPrompt := fmt.Sprintf(`Compose these parts into a single object:

%s%s`, allPartsJSON, steeringNote)

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
		log.Error("Compose operation LLM call failed", "error", err)
		return result, fmt.Errorf("composition failed: %w", err)
	}

	// Parse response
	var parsed struct {
		Composed          json.RawMessage `json:"composed"`
		FieldSources      []ComposedField `json:"field_sources"`
		ConflictsResolved int             `json:"conflicts_resolved"`
		GapsFilled        []string        `json:"gaps_filled"`
		UnusedParts       []int           `json:"unused_parts"`
		Completeness      float64         `json:"completeness"`
	}

	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("Compose operation failed: parse error", "error", err, "response", response)
		return result, fmt.Errorf("failed to parse composition result: %w", err)
	}

	// Parse composed object
	if len(parsed.Composed) > 0 {
		if err := json.Unmarshal(parsed.Composed, &result.Composed); err != nil {
			log.Error("Compose operation failed: composed object parse error", "error", err)
			return result, fmt.Errorf("failed to parse composed object: %w", err)
		}
	}

	result.FieldSources = parsed.FieldSources
	result.ConflictsResolved = parsed.ConflictsResolved
	result.GapsFilled = parsed.GapsFilled
	result.UnusedParts = parsed.UnusedParts
	result.Completeness = parsed.Completeness

	log.Debug("Compose operation succeeded",
		"field_sources", len(result.FieldSources),
		"conflicts_resolved", result.ConflictsResolved,
		"completeness", result.Completeness)

	return result, nil
}

// mergeComposeOptions merges user options with defaults
func mergeComposeOptions(defaults, user ComposeOptions) ComposeOptions {
	if user.Template != "" {
		defaults.Template = user.Template
	}
	if user.MergeStrategy != "" {
		defaults.MergeStrategy = user.MergeStrategy
	}
	defaults.FillGaps = user.FillGaps
	defaults.Validate = user.Validate
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
