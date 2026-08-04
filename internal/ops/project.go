// package ops - Project operation for semantic structure transformation
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

// ProjectOptions configures the Project operation
type ProjectOptions struct {
	// Mappings explicitly maps source fields to target fields
	Mappings map[string]string

	// Exclude lists source fields to exclude from projection
	Exclude []string

	// InferMissing allows inferring target fields not in source
	InferMissing bool

	// PreserveNulls keeps null values instead of omitting them
	PreserveNulls bool

	// Common options
	Steering      string
	Mode          types.Mode
	Intelligence  types.Speed
	Context       context.Context
	RequestID     string
	CorrelationID string
}

// FieldMapping describes how a field was mapped
type FieldMapping struct {
	// SourceField is the original field name (empty if inferred)
	SourceField string `json:"source_field,omitempty"`

	// TargetField is the destination field name
	TargetField string `json:"target_field"`

	// Method describes how the mapping was done ("direct", "rename", "transform", "infer")
	Method string `json:"method"`

	// Transformation describes any value transformation applied
	Transformation string `json:"transformation,omitempty"`
}

// ProjectResult contains the projected data and mapping information
type ProjectResult[U any] struct {
	// Projected is the transformed data in the target schema
	Projected U `json:"projected"`

	// Mappings describes how each field was mapped
	Mappings []FieldMapping `json:"mappings"`

	// Lost lists source fields that couldn't be projected
	Lost []string `json:"lost,omitempty"`

	// Inferred lists target fields that were inferred (not from source)
	Inferred []string `json:"inferred,omitempty"`

	// Confidence in the projection quality (0.0-1.0)
	Confidence float64 `json:"confidence"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Project transforms data structure while preserving semantics.
//
// Type parameters:
//   - T: Input data type
//   - U: Output/projected data type
//
// Examples:
//
//	// Example 1: Create public profile from internal user (privacy filtering)
//	type InternalUser struct {
//	    ID           string `json:"id"`
//	    Email        string `json:"email"`
//	    PasswordHash string `json:"password_hash"`
//	    SSN          string `json:"ssn"`
//	    FirstName    string `json:"first_name"`
//	    LastName     string `json:"last_name"`
//	    CreatedAt    string `json:"created_at"`
//	}
//	type PublicProfile struct {
//	    UserID      string `json:"user_id"`
//	    DisplayName string `json:"display_name"`
//	    MemberSince string `json:"member_since"`
//	}
//	result, err := Project[InternalUser, PublicProfile](user, ProjectOptions{
//	    Mappings: map[string]string{"id": "user_id", "created_at": "member_since"},
//	    Exclude: []string{"password_hash", "ssn"},
//	    InferMissing: true,
//	    Steering: "Combine first_name and last_name into display_name",
//	})
//	for _, m := range result.Mappings {
//	    fmt.Printf("%s → %s (%s)\n", m.SourceField, m.TargetField, m.Method)
//	}
//
//	// Example 2: Project order to invoice summary
//	type Order struct {
//	    ID       string      `json:"id"`
//	    Items    []OrderItem `json:"items"`
//	    Customer Customer    `json:"customer"`
//	    Created  string      `json:"created"`
//	}
//	type InvoiceSummary struct {
//	    InvoiceNumber string  `json:"invoice_number"`
//	    CustomerName  string  `json:"customer_name"`
//	    TotalAmount   float64 `json:"total_amount"`
//	    ItemCount     int     `json:"item_count"`
//	    Date          string  `json:"date"`
//	}
//	result, err := Project[Order, InvoiceSummary](order)
//
//	// Example 3: Schema migration with field transforms
//	result, err := Project[OldSchema, NewSchema](data, ProjectOptions{
//	    Mappings: map[string]string{"old_field": "new_field"},
//	    Steering: "Convert date formats from MM/DD/YYYY to ISO8601",
//	})
//	fmt.Printf("Lost fields: %v, Inferred: %v\n", result.Lost, result.Inferred)
func Project[T any, U any](input T, opts ...ProjectOptions) (ProjectResult[U], error) {
	log := logger.GetLogger()
	log.Debug("Starting project operation")

	var result ProjectResult[U]
	result.Metadata = make(map[string]any)

	// Apply defaults
	opt := ProjectOptions{
		InferMissing: false,
		Mode:         types.TransformMode,
		Intelligence: types.Fast,
	}
	if len(opts) > 0 {
		opt = mergeProjectOptions(opt, opts[0])
	}

	// Get context
	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	// Convert input to JSON
	inputJSON, err := json.Marshal(input)
	if err != nil {
		log.Error("Project operation failed: marshal error", "error", err)
		return result, fmt.Errorf("failed to marshal input: %w", err)
	}

	// Get schemas
	inputSchema := GenerateTypeSchema(reflect.TypeOf(input))
	var zero U
	outputSchema := GenerateTypeSchema(reflect.TypeOf(zero))

	// Build mappings description
	mappingsDesc := ""
	if len(opt.Mappings) > 0 {
		var parts []string
		for src, dst := range opt.Mappings {
			parts = append(parts, fmt.Sprintf("- %s → %s", src, dst))
		}
		mappingsDesc = fmt.Sprintf("\n\nExplicit mappings:\n%s", strings.Join(parts, "\n"))
	}

	// Build exclude description
	excludeDesc := ""
	if len(opt.Exclude) > 0 {
		excludeDesc = fmt.Sprintf("\n\nExclude fields: %s", strings.Join(opt.Exclude, ", "))
	}

	inferNote := ""
	if opt.InferMissing {
		inferNote = "\nInferMissing: true - derive target fields not present in source"
	}

	systemPrompt := fmt.Sprintf(`You are a data projection expert. Transform data from one structure to another while preserving semantics.

Source schema: %s
Target schema: %s%s%s%s

Return a JSON object with:
{
  "projected": %s,
  "mappings": [
    {
      "source_field": "original_field",
      "target_field": "destination_field",
      "method": "direct/rename/transform/infer",
      "transformation": "description of any value transformation"
    }
  ],
  "lost": ["fields that couldn't be mapped"],
  "inferred": ["target fields that were inferred"],
  "confidence": 0.0-1.0
}

Methods:
- "direct": Field copied directly (same name and type)
- "rename": Field renamed but value unchanged
- "transform": Value transformed (aggregated, computed, formatted)
- "infer": Value derived/inferred (not from a single source field)

Rules:
- Map source fields to matching target fields semantically
- Preserve data meaning even when field names differ
- Note any source fields that couldn't be projected
- Mark inferred fields clearly`,
		inputSchema, outputSchema, mappingsDesc, excludeDesc, inferNote, outputSchema)

	steeringNote := ""
	if opt.Steering != "" {
		steeringNote = fmt.Sprintf("\n\nAdditional guidance: %s", opt.Steering)
	}

	userPrompt := fmt.Sprintf(`Project this data to the target schema:

%s%s`, string(inputJSON), steeringNote)

	// Build OpOptions for LLM call
	opOpts := types.OpOptions{
		Mode:          opt.Mode,
		Intelligence:  opt.Intelligence,
		Context:       ctx,
		RequestID:     opt.RequestID,
		CorrelationID: opt.CorrelationID,
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opOpts)
	if err != nil {
		log.Error("Project operation LLM call failed", "error", err)
		return result, fmt.Errorf("projection failed: %w", err)
	}

	// Clean up response
	response = strings.TrimSpace(response)
	if strings.HasPrefix(response, "```json") {
		response = strings.TrimPrefix(response, "```json")
		response = strings.TrimSuffix(response, "```")
		response = strings.TrimSpace(response)
	} else if strings.HasPrefix(response, "```") {
		response = strings.TrimPrefix(response, "```")
		response = strings.TrimSuffix(response, "```")
		response = strings.TrimSpace(response)
	}

	// Parse response
	var parsed struct {
		Projected  json.RawMessage `json:"projected"`
		Mappings   []FieldMapping  `json:"mappings"`
		Lost       []string        `json:"lost"`
		Inferred   []string        `json:"inferred"`
		Confidence float64         `json:"confidence"`
	}

	if err := json.Unmarshal([]byte(response), &parsed); err != nil {
		log.Error("Project operation failed: parse error", "error", err, "response", response)
		return result, fmt.Errorf("failed to parse projection result: %w", err)
	}

	// Parse projected data
	if len(parsed.Projected) > 0 {
		if err := json.Unmarshal(parsed.Projected, &result.Projected); err != nil {
			log.Error("Project operation failed: projected data parse error", "error", err)
			return result, fmt.Errorf("failed to parse projected data: %w", err)
		}
	}

	result.Mappings = parsed.Mappings
	result.Lost = parsed.Lost
	result.Inferred = parsed.Inferred
	result.Confidence = parsed.Confidence

	log.Debug("Project operation succeeded",
		"mappings", len(result.Mappings),
		"lost", len(result.Lost),
		"inferred", len(result.Inferred),
		"confidence", result.Confidence)

	return result, nil
}

// mergeProjectOptions merges user options with defaults
func mergeProjectOptions(defaults, user ProjectOptions) ProjectOptions {
	if user.Mappings != nil {
		defaults.Mappings = user.Mappings
	}
	if user.Exclude != nil {
		defaults.Exclude = user.Exclude
	}
	defaults.InferMissing = user.InferMissing
	defaults.PreserveNulls = user.PreserveNulls
	if user.Steering != "" {
		defaults.Steering = user.Steering
	}
	if user.Mode != 0 {
		defaults.Mode = user.Mode
	}
	if user.Intelligence != 0 {
		defaults.Intelligence = user.Intelligence
	}
	if user.Context != nil {
		defaults.Context = user.Context
	}
	return defaults
}
