// package ops - Pivot operation for restructuring data relationships
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

// PivotOptions configures the Pivot operation
type PivotOptions struct {
	// PivotOn specifies the field(s) to pivot around
	PivotOn []string

	// Aggregate specifies how to aggregate values ("first", "last", "sum", "avg", "list", "concat")
	Aggregate string

	// GroupBy specifies fields to group on before pivoting
	GroupBy []string

	// Flatten converts nested structures to flat key-value pairs
	Flatten bool

	// Common options
	Steering      string
	Mode          types.Mode
	Intelligence  types.Speed
	Context       context.Context
	RequestID     string
	CorrelationID string
}

// PivotMapping describes how data was restructured
type PivotMapping struct {
	// SourcePath is the original location
	SourcePath string `json:"source_path"`

	// TargetPath is the new location
	TargetPath string `json:"target_path"`

	// Transformation describes any value changes
	Transformation string `json:"transformation,omitempty"`

	// Aggregation notes if values were aggregated
	Aggregation string `json:"aggregation,omitempty"`
}

// PivotStats provides statistics about the transformation
type PivotStats struct {
	// SourceFields is the count of fields in input
	SourceFields int `json:"source_fields"`

	// TargetFields is the count of fields in output
	TargetFields int `json:"target_fields"`

	// Expansions is the number of one-to-many transformations
	Expansions int `json:"expansions"`

	// Compressions is the number of many-to-one transformations
	Compressions int `json:"compressions"`

	// DepthChange is the change in nesting depth (positive = deeper)
	DepthChange int `json:"depth_change"`
}

// PivotResult contains the pivoted data and transformation details
type PivotResult[U any] struct {
	// Pivoted is the restructured data
	Pivoted U `json:"pivoted"`

	// Mappings describes how each field was relocated
	Mappings []PivotMapping `json:"mappings"`

	// Stats provides transformation statistics
	Stats PivotStats `json:"stats"`

	// DataLoss lists any fields/values that couldn't be preserved
	DataLoss []string `json:"data_loss,omitempty"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Pivot restructures data relationships between typed objects.
//
// Type parameters:
//   - T: Input data type
//   - U: Output/pivoted data type
//
// Examples:
//
//	// Example 1: Pivot sales rows to quarterly columns for reporting
//	type SalesRow struct {
//	    Product string  `json:"product"`
//	    Quarter string  `json:"quarter"`
//	    Revenue float64 `json:"revenue"`
//	}
//	type ProductSummary struct {
//	    Product   string  `json:"product"`
//	    Q1Revenue float64 `json:"q1_revenue"`
//	    Q2Revenue float64 `json:"q2_revenue"`
//	    Q3Revenue float64 `json:"q3_revenue"`
//	    Q4Revenue float64 `json:"q4_revenue"`
//	    Total     float64 `json:"total"`
//	}
//	sales := []SalesRow{
//	    {Product: "Widget", Quarter: "Q1", Revenue: 10000},
//	    {Product: "Widget", Quarter: "Q2", Revenue: 12000},
//	    {Product: "Gadget", Quarter: "Q1", Revenue: 8000},
//	}
//	result, err := Pivot[[]SalesRow, []ProductSummary](sales, PivotOptions{
//	    PivotOn:   []string{"Quarter"},
//	    GroupBy:   []string{"Product"},
//	    Aggregate: "sum",
//	})
//	fmt.Printf("Stats: %d compressions\n", result.Stats.Compressions)
//
//	// Example 2: Flatten nested user structure for export
//	type NestedUser struct {
//	    ID      string `json:"id"`
//	    Profile struct {
//	        Name  string `json:"name"`
//	        Email string `json:"email"`
//	    } `json:"profile"`
//	    Address struct {
//	        City    string `json:"city"`
//	        Country string `json:"country"`
//	    } `json:"address"`
//	}
//	type FlatUser struct {
//	    ID             string `json:"id"`
//	    ProfileName    string `json:"profile_name"`
//	    ProfileEmail   string `json:"profile_email"`
//	    AddressCity    string `json:"address_city"`
//	    AddressCountry string `json:"address_country"`
//	}
//	result, err := Pivot[NestedUser, FlatUser](user, PivotOptions{Flatten: true})
//	fmt.Printf("Depth change: %d\n", result.Stats.DepthChange)
//
//	// Example 3: Aggregate transactions by category and year
//	result, err := Pivot[[]Transaction, []CategoryYearSummary](txns, PivotOptions{
//	    GroupBy:   []string{"category"},
//	    PivotOn:   []string{"year"},
//	    Aggregate: "sum",
//	})
//	if len(result.DataLoss) > 0 {
//	    fmt.Printf("Warning: lost fields %v\n", result.DataLoss)
//	}
func Pivot[T any, U any](input T, opts ...PivotOptions) (PivotResult[U], error) {
	log := logger.GetLogger()
	log.Debug("Starting pivot operation")

	var result PivotResult[U]
	result.Metadata = make(map[string]any)

	// Apply defaults
	opt := PivotOptions{
		Aggregate:    "first",
		Flatten:      false,
		Mode:         types.TransformMode,
		Intelligence: types.Smart,
	}
	if len(opts) > 0 {
		opt = mergePivotOptions(opt, opts[0])
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
		log.Error("Pivot operation failed: marshal error", "error", err)
		return result, fmt.Errorf("failed to marshal input: %w", err)
	}

	// Get schemas
	inputSchema := GenerateTypeSchema(reflect.TypeOf(input))
	var zero U
	outputSchema := GenerateTypeSchema(reflect.TypeOf(zero))

	// Build pivot description
	pivotDesc := ""
	if len(opt.PivotOn) > 0 {
		pivotDesc = fmt.Sprintf("\n\nPivot on: %s", strings.Join(opt.PivotOn, ", "))
	}

	groupDesc := ""
	if len(opt.GroupBy) > 0 {
		groupDesc = fmt.Sprintf("\n\nGroup by: %s", strings.Join(opt.GroupBy, ", "))
	}

	flattenNote := ""
	if opt.Flatten {
		flattenNote = "\n\nFlatten: true - convert nested structures to flat key-value"
	}

	systemPrompt := fmt.Sprintf(`You are a data restructuring expert. Pivot/reshape data from one structure to another.

Source schema: %s
Target schema: %s%s%s
Aggregate: %s%s

Return a JSON object with:
{
  "pivoted": %s,
  "mappings": [
    {
      "source_path": "original.path.to.field",
      "target_path": "new.path.to.field",
      "transformation": "description of any transformation",
      "aggregation": "aggregation applied if any"
    }
  ],
  "stats": {
    "source_fields": 10,
    "target_fields": 8,
    "expansions": 2,
    "compressions": 1,
    "depth_change": -1
  },
  "data_loss": ["fields that couldn't be preserved"]
}

Pivot operations:
- Rows to columns: Take values from a field and turn them into column headers
- Columns to rows: Take column headers and turn them into field values
- Flatten: Collapse nested structures into flat key-value pairs
- Nest: Create nested structures from flat data
- Group: Aggregate multiple rows into single rows

Aggregation methods:
- "first": Use first value in group
- "last": Use last value in group
- "sum": Sum numeric values
- "avg": Average numeric values
- "list": Collect all values into array
- "concat": Concatenate strings

Preserve data semantics. Document any data loss.`,
		inputSchema, outputSchema, pivotDesc, groupDesc, opt.Aggregate, flattenNote, outputSchema)

	steeringNote := ""
	if opt.Steering != "" {
		steeringNote = fmt.Sprintf("\n\nAdditional guidance: %s", opt.Steering)
	}

	userPrompt := fmt.Sprintf(`Pivot this data to the target structure:

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
		log.Error("Pivot operation LLM call failed", "error", err)
		return result, fmt.Errorf("pivot failed: %w", err)
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
		Pivoted  json.RawMessage `json:"pivoted"`
		Mappings []PivotMapping  `json:"mappings"`
		Stats    PivotStats      `json:"stats"`
		DataLoss []string        `json:"data_loss"`
	}

	if err := json.Unmarshal([]byte(response), &parsed); err != nil {
		log.Error("Pivot operation failed: parse error", "error", err, "response", response)
		return result, fmt.Errorf("failed to parse pivot result: %w", err)
	}

	// Parse pivoted data
	if len(parsed.Pivoted) > 0 {
		if err := json.Unmarshal(parsed.Pivoted, &result.Pivoted); err != nil {
			log.Error("Pivot operation failed: pivoted data parse error", "error", err)
			return result, fmt.Errorf("failed to parse pivoted data: %w", err)
		}
	}

	result.Mappings = parsed.Mappings
	result.Stats = parsed.Stats
	result.DataLoss = parsed.DataLoss

	log.Debug("Pivot operation succeeded",
		"mappings", len(result.Mappings),
		"source_fields", result.Stats.SourceFields,
		"target_fields", result.Stats.TargetFields)

	return result, nil
}

// mergePivotOptions merges user options with defaults
func mergePivotOptions(defaults, user PivotOptions) PivotOptions {
	if user.PivotOn != nil {
		defaults.PivotOn = user.PivotOn
	}
	if user.Aggregate != "" {
		defaults.Aggregate = user.Aggregate
	}
	if user.GroupBy != nil {
		defaults.GroupBy = user.GroupBy
	}
	defaults.Flatten = user.Flatten
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
