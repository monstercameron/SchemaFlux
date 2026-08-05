// package ops - Interpolate operation for filling gaps in typed sequences
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

// InterpolateOptions configures the Interpolate operation
type InterpolateOptions struct {
	// Method specifies interpolation approach ("linear", "trend", "semantic", "pattern", "auto")
	Method string

	// GapIndices explicitly specifies which indices are gaps (nil/empty values)
	GapIndices []int

	// SequenceField identifies the ordering field (e.g., "date", "index", "timestamp")
	SequenceField string

	// ContextWindow is how many surrounding items to consider
	ContextWindow int

	// Constraints are rules that interpolated values must satisfy
	Constraints []string

	// Common options
	Steering      string
	Mode          types.Mode
	Intelligence  types.Speed
	Context       context.Context
	RequestID     string
	CorrelationID string
}

// FilledItem describes an interpolated value
type FilledItem struct {
	// Index is the position in the sequence
	Index int `json:"index"`

	// Method describes how this value was interpolated
	Method string `json:"method"`

	// BasedOn lists indices used to derive this value
	BasedOn []int `json:"based_on,omitempty"`

	// Reasoning explains the interpolation logic
	Reasoning string `json:"reasoning,omitempty"`

	// ModelConfidence for this interpolated value
	ModelConfidence float64 `json:"confidence"`
}

// InterpolateResult contains the complete sequence with filled gaps
type InterpolateResult[T any] struct {
	// Complete is the sequence with all gaps filled
	Complete []T `json:"complete"`

	// Filled describes each interpolated item
	Filled []FilledItem `json:"filled,omitempty"`

	// GapCount is the number of gaps that were filled
	GapCount int `json:"gap_count"`

	// Method is the primary interpolation method used
	Method string `json:"method"`

	// AverageConfidence across all interpolated values
	AverageConfidence float64 `json:"average_confidence"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Interpolate fills gaps in typed sequences using intelligent inference.
//
// Type parameter T specifies the element type of the sequence.
//
// Examples:
//
//	// Example 1: Fill missing sales data (zero values = gaps)
//	type DailySales struct {
//	    Date    string  `json:"date"`
//	    Revenue float64 `json:"revenue"`
//	    Units   int     `json:"units"`
//	}
//	sales := []DailySales{
//	    {Date: "2024-01-01", Revenue: 5000, Units: 50},
//	    {Date: "2024-01-02", Revenue: 0, Units: 0},      // Missing!
//	    {Date: "2024-01-03", Revenue: 5500, Units: 55},
//	}
//	result, err := Interpolate(sales, InterpolateOptions{
//	    Method: "contextual",
//	    Steering: "Zero values indicate missing data",
//	})
//	for _, f := range result.Filled {
//	    fmt.Printf("Filled index %d using %s\n", f.Index, f.Method)
//	}
//
//	// Example 2: Complete employee performance records
//	type WeeklyPerformance struct {
//	    Week   int     `json:"week"`
//	    Hours  float64 `json:"hours"`
//	    Tasks  int     `json:"tasks"`
//	    Rating float64 `json:"rating"`
//	}
//	result, err := Interpolate(records, InterpolateOptions{
//	    Method: "pattern",
//	    SequenceField: "week",
//	    Steering: "Absent weeks should show projected performance",
//	})
//
//	// Example 3: Fill survey response gaps
//	result, err := Interpolate(responses, InterpolateOptions{
//	    Method: "semantic",
//	    GapIndices: []int{3, 7, 12}, // Explicitly mark gaps
//	    Constraints: []string{"values must be 1-5", "maintain consistency"},
//	})
//	fmt.Printf("Filled %d gaps with %.0f%% avg confidence\n",
//	    result.GapCount, result.AverageConfidence*100)
func Interpolate[T any](items []T, opts ...InterpolateOptions) (InterpolateResult[T], error) {
	log := logger.GetLogger()
	log.Debug("Starting interpolate operation", "itemCount", len(items))

	var result InterpolateResult[T]
	result.Metadata = make(map[string]any)

	if len(items) == 0 {
		return result, fmt.Errorf("no items to interpolate")
	}

	if len(items) < 2 {
		result.Complete = items
		result.Method = "none"
		return result, nil
	}

	// Apply defaults
	opt := InterpolateOptions{
		Method:        "auto",
		ContextWindow: 3,
		Mode:          types.TransformMode,
		Intelligence:  types.Fast,
	}
	if len(opts) > 0 {
		opt = mergeInterpolateOptions(opt, opts[0])
	}

	result.Method = opt.Method

	// Get context
	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	// Convert items to JSON
	itemsJSON, err := json.Marshal(items)
	if err != nil {
		log.Error("Interpolate operation failed: marshal error", "error", err)
		return result, fmt.Errorf("failed to marshal items: %w", err)
	}

	// Get type schema
	var zero T
	typeSchema := GenerateTypeSchema(reflect.TypeOf(zero))

	// Build gap indices description
	gapDesc := ""
	if len(opt.GapIndices) > 0 {
		indices := make([]string, len(opt.GapIndices))
		for i, idx := range opt.GapIndices {
			indices[i] = fmt.Sprintf("%d", idx)
		}
		gapDesc = fmt.Sprintf("\n\nKnown gap indices: %s", strings.Join(indices, ", "))
	}

	// Build constraints description
	constraintsDesc := ""
	if len(opt.Constraints) > 0 {
		constraintsDesc = fmt.Sprintf("\n\nConstraints:\n- %s", strings.Join(opt.Constraints, "\n- "))
	}

	sequenceFieldNote := ""
	if opt.SequenceField != "" {
		sequenceFieldNote = fmt.Sprintf("\nSequence ordered by field: %s", opt.SequenceField)
	}

	systemPrompt := fmt.Sprintf(`You are a data interpolation expert. Fill gaps in sequences using intelligent inference.

Element schema: %s
Method: %s
Context window: %d items%s%s%s

Return a JSON object with:
{
  "complete": [complete sequence with gaps filled],
  "filled": [
    {
      "index": 0,
      "method": "linear/trend/pattern/semantic",
      "based_on": [1, 2],
      "reasoning": "explanation",
      "confidence": 0.0-1.0
    }
  ],
  "gap_count": number,
  "method": "primary method used",
  "average_confidence": 0.0-1.0
}

Methods:
- "linear": Simple linear interpolation between neighbors
- "trend": Follow detected trend patterns
- "semantic": Use meaning/context to infer values
- "pattern": Match repeating patterns in the data
- "auto": Automatically choose best method

Rules:
- Identify gaps (null, empty, or missing values)
- Fill gaps using the specified method
- Ensure interpolated values are consistent with surrounding data
- Provide confidence scores for each filled value`,
		typeSchema, opt.Method, opt.ContextWindow, sequenceFieldNote, gapDesc, constraintsDesc)

	steeringNote := ""
	if opt.Steering != "" {
		steeringNote = fmt.Sprintf("\n\nAdditional guidance: %s", opt.Steering)
	}

	userPrompt := fmt.Sprintf(`Fill gaps in this sequence:

%s%s`, string(itemsJSON), steeringNote)

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
		log.Error("Interpolate operation LLM call failed", "error", err)
		return result, fmt.Errorf("interpolation failed: %w", err)
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
		Complete          []json.RawMessage `json:"complete"`
		Filled            []FilledItem      `json:"filled"`
		GapCount          int               `json:"gap_count"`
		Method            string            `json:"method"`
		AverageConfidence float64           `json:"average_confidence"`
	}

	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("Interpolate operation failed: parse error", "error", err, "response", response)
		return result, fmt.Errorf("failed to parse interpolation result: %w", err)
	}

	// Parse complete sequence
	result.Complete = make([]T, len(parsed.Complete))
	for i, itemJSON := range parsed.Complete {
		if err := json.Unmarshal(itemJSON, &result.Complete[i]); err != nil {
			log.Error("Interpolate operation failed: item parse error", "index", i, "error", err)
			return result, fmt.Errorf("failed to parse item %d: %w", i, err)
		}
	}

	result.Filled = parsed.Filled
	result.GapCount = parsed.GapCount
	if parsed.Method != "" {
		result.Method = parsed.Method
	}
	result.AverageConfidence = parsed.AverageConfidence

	log.Debug("Interpolate operation succeeded",
		"gapCount", result.GapCount,
		"method", result.Method,
		"averageConfidence", result.AverageConfidence)

	return result, nil
}

// mergeInterpolateOptions merges user options with defaults
func mergeInterpolateOptions(defaults, user InterpolateOptions) InterpolateOptions {
	if user.Method != "" {
		defaults.Method = user.Method
	}
	if user.GapIndices != nil {
		defaults.GapIndices = user.GapIndices
	}
	if user.SequenceField != "" {
		defaults.SequenceField = user.SequenceField
	}
	if user.ContextWindow > 0 {
		defaults.ContextWindow = user.ContextWindow
	}
	if user.Constraints != nil {
		defaults.Constraints = user.Constraints
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
	if user.Context != nil {
		defaults.Context = user.Context
	}
	return defaults
}
