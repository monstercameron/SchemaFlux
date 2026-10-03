// Interpolate operation for filling gaps in typed sequences.

package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// InterpolateOptions configures the Interpolate operation
type InterpolateOptions struct {
	// Method specifies the interpolation approach: "linear", "trend",
	// "semantic", "pattern" or "auto". Any other value is refused.
	Method string

	// GapIndices explicitly specifies which indices are gaps (nil/empty values).
	// An index outside the sequence is refused. When it is set, every gap has to
	// come back reported as filled, and every item that is not a gap has to come
	// back unchanged.
	GapIndices []int

	// SequenceField identifies the ordering field (e.g., "date", "index", "timestamp")
	SequenceField string

	// ContextWindow is how many surrounding items to consider. Zero is the
	// value of an option that was not set and means the default of 3; a
	// negative value is refused.
	ContextWindow int

	// Constraints are rules that interpolated values must satisfy. They are
	// guidance to the model: Go does not check the filled values against them.
	Constraints []string

	// Common options
	Steering     string
	Mode         types.Mode
	Intelligence types.Speed

	// Model pins the exact model for this call, bypassing the Intelligence
	// tier's mapping (TC-005). These option types spell their common fields
	// out rather than embedding CommonOptions, so the pin has to be declared
	// here too -- otherwise `.Model(...)` on the builder would be a setter
	// that changes nothing, which this repository fails the build over.
	Model string
	// Effort is how much the model reasons before it answers (LX-105). The
	// builder sets it here for the same reason it sets Model here.
	Effort types.Effort

	// MaxOutputTokens is the ceiling on the answer's length. Zero means the tier's.
	MaxOutputTokens int
	// WebSearch declares the provider's web-search tool on the call.
	WebSearch bool

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

	// GapCount is the number of gaps that were filled. It equals len(Filled):
	// an answer whose count disagrees with its own list is refused.
	GapCount int `json:"gap_count"`

	// Method is the primary interpolation method the model reported using, or
	// the method that was asked for when it reported none. It is "none" when the
	// sequence was too short to have a gap between items, in which case no model
	// was asked.
	Method string `json:"method"`

	// AverageConfidence is the mean of the filled items' ModelConfidence values,
	// worked out here from what the model reported for each one. It is 0 when
	// nothing was filled, and then it means that no value was reported, not that
	// the model had no confidence.
	AverageConfidence float64 `json:"average_confidence"`

	// Metadata contains additional operation information. Interpolate does not
	// write to it.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// interpolateMethods are the methods the prompt describes.
var interpolateMethods = map[string]bool{
	"auto": true, "linear": true, "trend": true, "semantic": true, "pattern": true,
}

// Interpolate fills gaps in typed sequences using intelligent inference.
//
// Type parameter T specifies the element type of the sequence. Only the first
// of opts is used.
//
// A sequence of fewer than two items has nothing to interpolate between: it is
// returned as it is with Method "none", no model is asked, and GapCount and
// AverageConfidence are 0.
//
// What the answer has to satisfy, beyond being the right shape: it has as many
// items as the input; every item that differs from the input is reported in
// Filled; every declared gap is reported in Filled; with GapIndices set, no item
// that is not a gap differs. An answer that breaks one of these is refused with
// an invariant violation, and the result beside the error is empty.
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
//	    Method: "semantic",
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
	// The first line is written before the options are looked at, so the client is
	// found from the context they carry.
	var callerCtx context.Context
	if len(opts) > 0 {
		callerCtx = opts[0].Context
	}
	log := loggerFor(callerCtx)
	log.Debug("Starting interpolate operation", "itemCount", len(items))

	// The caller's window is looked at before the merge, which drops one that
	// is not above zero: a negative value would otherwise be taken for "not set"
	// and replaced by the default without a word.
	if len(opts) > 0 {
		if err := checkInterpolateWindow(opts[0]); err != nil {
			return InterpolateResult[T]{}, err
		}
	}

	if len(items) == 0 {
		return InterpolateResult[T]{}, types.Newf(types.KindInvalidRequest, "interpolate", "no items to interpolate")
	}

	if len(items) < 2 {
		return InterpolateResult[T]{Complete: append([]T(nil), items...), Method: "none"}, nil
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
	if err := checkFlatLockedLimits(opt.Context, opt.Mode, opt.Intelligence, opt.MaxOutputTokens); err != nil {
		return InterpolateResult[T]{}, err
	}

	if err := checkInterpolateOptions(opt, len(items)); err != nil {
		return InterpolateResult[T]{}, err
	}

	// Get context
	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := operationContext(ctx, 0)
	defer cancel()

	// Convert items to JSON
	itemsJSON, err := promptJSON(items)
	if err != nil {
		log.Error("Interpolate operation failed: marshal error", "error", err)
		return InterpolateResult[T]{}, types.Newf(types.KindInvalidRequest, "interpolate", "the items could not be written as JSON")
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

	// Build constraints description. The constraints are the caller's text, so
	// each is written as a JSON string and cannot read as one of the rules below.
	constraintsDesc := ""
	if len(opt.Constraints) > 0 {
		quoted := make([]string, len(opt.Constraints))
		for i, constraint := range opt.Constraints {
			quoted[i] = quotedText(constraint)
		}
		constraintsDesc = fmt.Sprintf("\n\nConstraints:\n- %s", strings.Join(quoted, "\n- "))
	}

	sequenceFieldNote := ""
	if opt.SequenceField != "" {
		sequenceFieldNote = fmt.Sprintf("\nSequence ordered by field: %s", quotedText(opt.SequenceField))
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

	userPrompt := fmt.Sprintf(`Fill gaps in this sequence:

%s`, string(itemsJSON))

	// Build OpOptions for LLM call. The steering goes through the options and
	// not into the message by hand, so that the caller's text is delimited as
	// the caller's.
	opOpts := types.OpOptions{
		Mode:            opt.Mode,
		Intelligence:    opt.Intelligence,
		Model:           opt.Model,
		Effort:          opt.Effort,
		MaxOutputTokens: opt.MaxOutputTokens,
		WebSearch:       opt.WebSearch,
		Steering:        opt.Steering,
		Context:         ctx,
		RequestID:       opt.RequestID,
		CorrelationID:   opt.CorrelationID,
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opOpts)
	if err != nil {
		log.Error("Interpolate operation LLM call failed", "error", err)
		return InterpolateResult[T]{}, fmt.Errorf("interpolation failed: %w", err)
	}

	result, err := readInterpolateAnswer(items, opt.GapIndices, opt.Method, response)
	if err != nil {
		// The response body stays out of the log: it is built from the
		// caller's items, and a refusal is not a licence to print them.
		log.Error("Interpolate operation refused the answer", "error", err)
		return InterpolateResult[T]{}, err
	}

	log.Debug("Interpolate operation succeeded",
		"gapCount", result.GapCount,
		"averageConfidence", result.AverageConfidence)

	return result, nil
}

// checkInterpolateWindow refuses a context window in the caller's options that
// no reading of the option could use. Zero is the value of an option that was
// not set and is accepted: it stands for the default. The error names the
// option and not the value.
func checkInterpolateWindow(user InterpolateOptions) error {
	if user.ContextWindow < 0 {
		return types.Newf(types.KindConfiguration, "interpolate", "ContextWindow cannot be negative")
	}
	return nil
}

// checkInterpolateOptions refuses an option value the operation cannot honour.
func checkInterpolateOptions(opt InterpolateOptions, items int) error {
	if !interpolateMethods[opt.Method] {
		return types.Newf(types.KindInvalidRequest, "interpolate",
			"the method is not one of auto, linear, trend, semantic or pattern")
	}
	for _, index := range opt.GapIndices {
		if index < 0 || index >= items {
			return types.Newf(types.KindInvalidRequest, "interpolate",
				"gap index %d is outside the sequence of %d items", index, items)
		}
	}
	return nil
}

// readInterpolateAnswer reads the model's answer against the items it was
// given. gapIndices are the gaps the caller declared, or nil when it left the
// model to find them.
func readInterpolateAnswer[T any](items []T, gapIndices []int, requestedMethod, response string) (InterpolateResult[T], error) {
	const op = "interpolate"

	answer, err := readAnswerObject(op, response)
	if err != nil {
		return InterpolateResult[T]{}, err
	}

	completeRaw, err := answer.elements("complete")
	if err != nil {
		return InterpolateResult[T]{}, err
	}
	// The completed sequence must be the same length as the input. A caller
	// indexing the result against their own input would otherwise read the
	// wrong element or run off the end.
	if len(completeRaw) != len(items) {
		return InterpolateResult[T]{}, types.Newf(types.KindInvariantViolation, op,
			"the answer has %d items and the input had %d; filling gaps must not change the sequence's length",
			len(completeRaw), len(items))
	}
	complete := make([]T, len(completeRaw))
	for i, raw := range completeRaw {
		if isJSONNull(raw) {
			return InterpolateResult[T]{}, types.Newf(types.KindSchemaViolation, op, "item %d of the answer is null", i)
		}
		if err := json.Unmarshal(raw, &complete[i]); err != nil {
			return InterpolateResult[T]{}, types.Newf(types.KindSchemaViolation, op, "item %d of the answer does not fit the element type", i)
		}
	}

	filled, filledAt, err := readFilledItems(answer, len(items))
	if err != nil {
		return InterpolateResult[T]{}, err
	}

	reportedCount, err := answerWholeNumber(answer, "gap_count")
	if err != nil {
		return InterpolateResult[T]{}, err
	}
	if reportedCount != len(filled) {
		return InterpolateResult[T]{}, types.Newf(types.KindInvariantViolation, op,
			"the answer reports %d gaps filled and lists %d", reportedCount, len(filled))
	}
	if _, err := answer.fraction("average_confidence"); err != nil {
		return InterpolateResult[T]{}, err
	}
	// The method is the one thing the answer may leave out: the caller named
	// the method that was asked for, and that is what is reported when the
	// model reports none. A method that is there has to be a string.
	var method string
	if err := answer.decodeOptional("method", &method); err != nil {
		return InterpolateResult[T]{}, err
	}
	if strings.TrimSpace(method) == "" {
		method = requestedMethod
	}

	// Compared as JSON, which is how the item went to the model and how it came
	// back. A comparison of the decoded structs would call a correct answer
	// changed whenever the type has a field JSON cannot carry: a `json:"-"`
	// field, an unexported one, an empty slice that omitempty dropped.
	declared := make(map[int]bool, len(gapIndices))
	for _, index := range gapIndices {
		declared[index] = true
	}
	for i := range items {
		changed := canonicalKey(items[i]) != canonicalKey(complete[i])
		switch {
		case changed && len(declared) > 0 && !declared[i]:
			// The caller said which items are gaps, and the rest are to be left
			// alone. A model that rewrites a value it was only asked to keep
			// produces a result that still looks well formed.
			return InterpolateResult[T]{}, types.Newf(types.KindInvariantViolation, op,
				"item %d was not declared a gap but came back changed; filling gaps must leave the rest alone", i)
		case changed && !filledAt[i]:
			return InterpolateResult[T]{}, types.Newf(types.KindInvariantViolation, op,
				"item %d came back changed and is not reported as filled", i)
		case declared[i] && !filledAt[i]:
			return InterpolateResult[T]{}, types.Newf(types.KindInvariantViolation, op,
				"item %d was declared a gap and is not reported as filled", i)
		case len(declared) > 0 && filledAt[i] && !declared[i]:
			return InterpolateResult[T]{}, types.Newf(types.KindInvariantViolation, op,
				"item %d is reported as filled but was not declared a gap", i)
		}
	}

	var total float64
	for _, item := range filled {
		total += item.ModelConfidence
	}
	result := InterpolateResult[T]{
		Complete: complete,
		Filled:   filled,
		GapCount: len(filled),
		Method:   method,
	}
	if len(filled) > 0 {
		result.AverageConfidence = total / float64(len(filled))
	}
	return result, nil
}

// readFilledItems reads the "filled" list: every entry names an index inside the
// sequence, once, and carries the confidence the model gave it. filledAt is the
// set of indices named.
func readFilledItems(answer answerObject, items int) ([]FilledItem, map[int]bool, error) {
	entries, err := answer.elements("filled")
	if err != nil {
		return nil, nil, err
	}
	filled := make([]FilledItem, 0, len(entries))
	filledAt := make(map[int]bool, len(entries))
	for n, entry := range entries {
		var fields struct {
			Index           json.RawMessage   `json:"index"`
			Method          string            `json:"method"`
			BasedOn         []json.RawMessage `json:"based_on"`
			Reasoning       string            `json:"reasoning"`
			ModelConfidence json.RawMessage   `json:"confidence"`
		}
		if err := json.Unmarshal(entry, &fields); err != nil {
			return nil, nil, answer.refuse("entry %d of \"filled\" is not of the kind that was asked for", n)
		}
		index, ok := wholeNumberOf(fields.Index)
		if !ok {
			return nil, nil, answer.refuse("entry %d of \"filled\" has no whole-number index", n)
		}
		if index < 0 || index >= items {
			return nil, nil, answer.refuse("entry %d of \"filled\" names an index outside the sequence", n)
		}
		if filledAt[index] {
			return nil, nil, answer.refuse("entry %d of \"filled\" repeats an index", n)
		}
		confidence, ok := fractionOf(fields.ModelConfidence)
		if !ok {
			return nil, nil, answer.refuse("entry %d of \"filled\" has no confidence from 0 to 1", n)
		}
		basedOn := make([]int, 0, len(fields.BasedOn))
		for _, raw := range fields.BasedOn {
			position, ok := wholeNumberOf(raw)
			if !ok || position < 0 || position >= items {
				return nil, nil, answer.refuse("entry %d of \"filled\" is based on an index outside the sequence", n)
			}
			basedOn = append(basedOn, position)
		}
		if len(basedOn) == 0 {
			basedOn = nil
		}
		filledAt[index] = true
		filled = append(filled, FilledItem{
			Index:           index,
			Method:          fields.Method,
			BasedOn:         basedOn,
			Reasoning:       fields.Reasoning,
			ModelConfidence: confidence,
		})
	}
	return filled, filledAt, nil
}

// answerWholeNumber reads a member that has to be a whole number.
func answerWholeNumber(answer answerObject, name string) (int, error) {
	raw, err := answer.raw(name)
	if err != nil {
		return 0, err
	}
	number, ok := wholeNumberOf(raw)
	if !ok {
		return 0, answer.refuse("%q is not a whole number", name)
	}
	return number, nil
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
	if user.Mode != types.ModeUnset {
		defaults.Mode = user.Mode
	}
	if user.Intelligence != types.TierUnset {
		defaults.Intelligence = user.Intelligence
	}
	if user.Model != "" {
		defaults.Model = user.Model
	}
	if user.Effort != types.EffortDefault {
		defaults.Effort = user.Effort
	}
	if user.MaxOutputTokens > 0 {
		defaults.MaxOutputTokens = user.MaxOutputTokens
	}
	if user.WebSearch {
		defaults.WebSearch = true
	}
	if user.Context != nil {
		defaults.Context = user.Context
	}
	if user.RequestID != "" {
		defaults.RequestID = user.RequestID
	}
	if user.CorrelationID != "" {
		defaults.CorrelationID = user.CorrelationID
	}
	return defaults
}
