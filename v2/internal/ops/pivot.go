// package ops - Pivot operation for restructuring data relationships
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// PivotOptions configures the Pivot operation
type PivotOptions struct {
	// PivotOn specifies the field(s) to pivot around. Each has to be a field
	// of the input (a dotted path is read by its first segment) when the input
	// has field names to check it against.
	PivotOn []string

	// Aggregate specifies how to aggregate values: one of "first", "last",
	// "sum", "avg", "list", "concat". Any other value is refused.
	Aggregate string

	// GroupBy specifies fields to group on before pivoting, checked the same
	// way as PivotOn.
	GroupBy []string

	// Flatten converts nested structures to flat key-value pairs
	Flatten bool

	// Common options
	Steering     string
	Mode         types.Mode
	Intelligence types.Speed

	// Model pins the exact model for this call, bypassing the Intelligence
	// tier's mapping. These option types spell their common fields out rather
	// than embedding CommonOptions, so the pin has to be declared here too:
	// otherwise `.Model(...)` on the builder would be a setter that changes
	// nothing, which this repository fails the build over.
	Model string
	// Effort is how much the model reasons before it answers. The builder sets
	// it here for the same reason it sets Model here.
	Effort types.Effort

	// MaxOutputTokens is the ceiling on the answer's length. Zero means the tier's.
	MaxOutputTokens int
	// WebSearch declares the provider's web-search tool on the call.
	WebSearch bool

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

	// Mappings describes how each field was relocated. It is the model's own
	// account and is not checked.
	Mappings []PivotMapping `json:"mappings"`

	// Stats provides transformation statistics. It is the model's own account
	// and is not checked.
	Stats PivotStats `json:"stats"`

	// DataLoss names source fields whose name does not appear among the field
	// names of the model's pivoted answer. It is computed here from the answer
	// the model returned and the input, not taken from the model, which would
	// be an audit trail written by the thing being audited.
	//
	// It is a comparison of top-level field names, over every element when
	// either side is a list of objects. A field the pivot renamed therefore
	// appears here, and Mappings is where the model says where it went.
	// DataLossChecked says whether the comparison was made.
	DataLoss []string `json:"data_loss,omitempty"`

	// DataLossChecked reports that DataLoss was computed: the input and the
	// answer both had object field names to compare. When it is false (a pivot
	// of scalars, or of an empty list) an empty DataLoss means nothing was
	// compared, not that nothing was lost.
	DataLossChecked bool `json:"data_loss_checked"`

	// ModelClaimedDataLoss is what the model said it lost. Kept because a
	// disagreement between the two is worth seeing: a field the model says it
	// dropped and the diff says survived is a different problem from the
	// reverse.
	ModelClaimedDataLoss []string `json:"model_claimed_data_loss,omitempty"`

	// Metadata is not populated.
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
	// The first line is written before the options are looked at, so the client is
	// found from the context they carry.
	var callerCtx context.Context
	if len(opts) > 0 {
		callerCtx = opts[0].Context
	}
	log := loggerFor(callerCtx)
	log.Debug("Starting pivot operation")

	// Apply defaults
	opt := NewPivotOptions()
	if len(opts) > 0 {
		opt = mergePivotOptions(opt, opts[0])
	}
	if err := opt.validate(); err != nil {
		return PivotResult[U]{}, err
	}

	// Get context
	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := operationContext(ctx, 0)
	defer cancel()

	// Convert input to JSON
	inputJSON, err := json.Marshal(input)
	if err != nil {
		log.Error("Pivot operation failed: marshal error", "error", loggedError(err))
		return PivotResult[U]{}, types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal input: %w", err))
	}

	// The fields the caller named have to be fields of the input. Only an
	// input with field names can be checked; a list of scalars has none.
	sourceNames, sourceNamed := rawFieldNames(inputJSON)
	if sourceNamed {
		if err := checkFieldNamesIn("pivot", "PivotOn", opt.PivotOn, sourceNames); err != nil {
			return PivotResult[U]{}, err
		}
		if err := checkFieldNamesIn("pivot", "GroupBy", opt.GroupBy, sourceNames); err != nil {
			return PivotResult[U]{}, err
		}
	}

	// Get schemas
	inputSchema := GenerateTypeSchema(reflect.TypeOf(input))
	var zero U
	outputSchema := GenerateTypeSchema(reflect.TypeOf(zero))

	// Build pivot description. The names are the caller's text, so they are
	// written as a JSON list.
	pivotDesc := ""
	if len(opt.PivotOn) > 0 {
		pivotDesc = fmt.Sprintf("\n\nPivot on: %s", parsePromptQuote(opt.PivotOn))
	}

	groupDesc := ""
	if len(opt.GroupBy) > 0 {
		groupDesc = fmt.Sprintf("\n\nGroup by: %s", parsePromptQuote(opt.GroupBy))
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
		steeringNote = fmt.Sprintf("\n\nAdditional guidance: %s", parsePromptQuote(opt.Steering))
	}

	userPrompt := fmt.Sprintf(`Pivot this data to the target structure:

%s%s`, string(inputJSON), steeringNote)

	// Build OpOptions for LLM call
	opOpts := types.OpOptions{
		Mode:            opt.Mode,
		Intelligence:    opt.Intelligence,
		Model:           opt.Model,
		Effort:          opt.Effort,
		MaxOutputTokens: opt.MaxOutputTokens,
		WebSearch:       opt.WebSearch,
		Context:         ctx,
		RequestID:       opt.RequestID,
		CorrelationID:   opt.CorrelationID,
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opOpts)
	if err != nil {
		log.Error("Pivot operation LLM call failed", "error", loggedError(err))
		return PivotResult[U]{}, fmt.Errorf("pivot failed: %w", err)
	}

	// Parse the response. The prompt asks for every member, so an answer that
	// leaves one out is refused by name, a statistic of zero is the model saying
	// zero, and an empty list is how it says there was nothing to report.
	var parsed struct {
		Pivoted  json.RawMessage `json:"pivoted"`
		Mappings []PivotMapping  `json:"mappings"`
		Stats    PivotStats      `json:"stats"`
		DataLoss []string        `json:"data_loss"`
	}

	if err := decodeAnswer("pivot", response, &parsed); err != nil {
		// The response is the caller's data pivoted; it does not belong in a
		// log line.
		log.Error("Pivot operation failed: parse error", "error", loggedError(err))
		return PivotResult[U]{}, fmt.Errorf("failed to parse pivot result: %w", err)
	}

	// The pivoted value is what the caller asked for. The reader refuses an
	// answer without it; a RawMessage can hold a null, which is not a pivot of
	// nothing but an answer to a different question, and a zero U would pass for
	// the result.
	if rawValueAbsent(parsed.Pivoted) {
		log.Error("Pivot operation failed: the pivoted value is null")
		return PivotResult[U]{}, types.Newf(types.KindSchemaViolation, "pivot", "pivoted is null")
	}

	// Decoded into a local, and only assigned to the result once it has
	// passed: json.Unmarshal fills what it can before it reports an error.
	var pivoted U
	if err := decodeCallerAnswer("pivot", string(parsed.Pivoted), &pivoted); err != nil {
		log.Error("Pivot operation failed: pivoted data parse error", "error", loggedError(err))
		return PivotResult[U]{}, fmt.Errorf("failed to parse pivoted data: %w", err)
	}

	result := PivotResult[U]{
		Pivoted:              pivoted,
		Mappings:             parsed.Mappings,
		Stats:                parsed.Stats,
		ModelClaimedDataLoss: parsed.DataLoss,
	}

	// Computed from the answer the model returned and the input, not reported.
	// The names come from the raw answer: the decoded U carries every field of
	// its type whether or not the model supplied it, so a diff over U would be
	// fixed by the pair of types and identical for a perfect answer and for one
	// that dropped everything.
	if producedNames, producedNamed := rawFieldNames(parsed.Pivoted); sourceNamed && producedNamed {
		result.DataLoss = missingFrom(sourceNames, producedNames)
		result.DataLossChecked = true
	}

	log.Debug("Pivot operation succeeded",
		"mappings", len(result.Mappings),
		"source_fields", result.Stats.SourceFields,
		"target_fields", result.Stats.TargetFields)

	return result, nil
}

// pivotAggregates are the aggregation methods the prompt describes.
var pivotAggregates = []string{"first", "last", "sum", "avg", "list", "concat"}

// validate refuses a call that would weaken a lock on its context, and an
// option value the prompt has no meaning for. Aggregate is written into the
// prompt, so only the methods the prompt describes are let through.
func (opt PivotOptions) validate() error {
	// The ceiling a caller asks for is read against the lock as well: it reaches
	// the request now, so a lock that did not look at it could be raised past.
	if err := checkFlatLockedLimits(opt.Context, opt.Mode, opt.Intelligence, opt.MaxOutputTokens); err != nil {
		return err
	}
	known := false
	for _, aggregate := range pivotAggregates {
		if opt.Aggregate == aggregate {
			known = true
			break
		}
	}
	if !known {
		return types.Newf(types.KindConfiguration, "pivot", "Aggregate must be one of %s", strings.Join(pivotAggregates, ", "))
	}
	for i, name := range opt.PivotOn {
		if strings.TrimSpace(name) == "" {
			return types.Newf(types.KindConfiguration, "pivot", "PivotOn entry %d is blank", i+1)
		}
	}
	for i, name := range opt.GroupBy {
		if strings.TrimSpace(name) == "" {
			return types.Newf(types.KindConfiguration, "pivot", "GroupBy entry %d is blank", i+1)
		}
	}
	return nil
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

	common := mergeCallFields(
		callFields{defaults.Steering, defaults.Mode, defaults.Intelligence, defaults.Model, defaults.Effort, defaults.MaxOutputTokens, defaults.WebSearch, defaults.Context, defaults.RequestID, defaults.CorrelationID},
		callFields{user.Steering, user.Mode, user.Intelligence, user.Model, user.Effort, user.MaxOutputTokens, user.WebSearch, user.Context, user.RequestID, user.CorrelationID})
	defaults.Steering, defaults.Mode, defaults.Intelligence, defaults.Model = common.Steering, common.Mode, common.Intelligence, common.Model
	defaults.Effort, defaults.MaxOutputTokens, defaults.WebSearch = common.Effort, common.MaxOutputTokens, common.WebSearch
	defaults.Context, defaults.RequestID, defaults.CorrelationID = common.Context, common.RequestID, common.CorrelationID
	return defaults
}
