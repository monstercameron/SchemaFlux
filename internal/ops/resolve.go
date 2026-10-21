// Resolve operation for resolving conflicts between typed sources.

package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// ResolveOptions configures the Resolve operation
type ResolveOptions struct {
	// Strategy for resolving conflicts ("newest", "most-complete", "majority", "authoritative", "merge")
	Strategy string

	// AuthoritativeSource is the index of the source to prefer (for "authoritative" strategy)
	AuthoritativeSource int

	// FieldPriorities maps field names to preferred source indices
	FieldPriorities map[string]int

	// ConflictThreshold is the similarity below which values are considered
	// conflicting (0.0-1.0). Zero is the value of an option that was not set
	// and means the default of 0.8; a number outside 0 to 1 is refused.
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

	// ModelConfidence is the model's own claim about the resolution quality
	// (0.0-1.0). It is zero when no model was asked, which is the single-source
	// case: Metadata["model_asked"] is false there.
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
//
// # What is checked
//
// Strategy must be one of the five named on ResolveOptions, and every source
// index in the options must name a supplied source. The answer must carry
// "resolved", "conflicts", "source_contributions" and a "confidence" between 0
// and 1, and each conflict must name its field, its values, its chosen source
// and its chosen value; a missing key is refused, not read as zero. Every source
// index in the answer must be one of the supplied sources. Under the
// "authoritative" strategy each conflict must be resolved from
// AuthoritativeSource, or from the source FieldPriorities names for that field.
// ConflictThreshold is a request to the model: nothing in Go computes similarity,
// so the answer is not checked against it.
//
// One source returns it as it is, with no model call and a zero
// ModelConfidence. On any error the result is empty.
func Resolve[T any](sources []T, opts ...ResolveOptions) (ResolveResult[T], error) {
	// The first line is written before the options are looked at, so the client is
	// found from the context they carry.
	var callerCtx context.Context
	if len(opts) > 0 {
		callerCtx = opts[0].Context
	}
	log := loggerFor(callerCtx)
	log.Debug("Starting resolve operation", "sourceCount", len(sources))

	// The caller's threshold is looked at before anything else, and before the
	// merge, which drops one that is not above zero: a NaN or a negative value
	// would otherwise be taken for "not set" and replaced by the default
	// without a word. It is refused for one source too, which asks no model.
	if len(opts) > 0 {
		if err := checkResolveThreshold(opts[0]); err != nil {
			return ResolveResult[T]{}, err
		}
	}

	if len(sources) == 0 {
		return ResolveResult[T]{}, types.Newf(types.KindInvalidRequest, "resolve", "no sources to resolve")
	}

	if len(sources) == 1 {
		// One source has nothing to conflict with, so no model is asked and there
		// is no confidence to report: ModelConfidence stays zero, and Metadata
		// says no model was consulted. It was 1.0, a claim nobody made.
		return ResolveResult[T]{
			Resolved:            sources[0],
			SourceContributions: map[int][]string{0: {"*"}},
			Strategy:            "single-source",
			Metadata:            map[string]any{"model_asked": false},
		}, nil
	}

	if len(opts) > 0 {
		if err := validateResolveOptions(opts[0], len(sources)); err != nil {
			return ResolveResult[T]{}, err
		}
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
	if err := checkFlatLockedLimits(opt.Context, opt.Mode, opt.Intelligence, opt.MaxOutputTokens); err != nil {
		return ResolveResult[T]{}, err
	}

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
			log.Error("Resolve operation failed: marshal error", "sourceIndex", i, "error", loggedError(err))
			return ResolveResult[T]{}, types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal source %d: %w", i, err))
		}
		sourcesJSON = append(sourcesJSON, fmt.Sprintf("Source %d: %s", i, string(sourceJSON)))
	}

	// Get type schema
	var zero T
	typeSchema := GenerateTypeSchema(reflect.TypeOf(zero))

	// Build field priorities description, in field order: a map ranged over
	// renders differently on every call, which defeats the provider's prompt
	// cache and makes two runs of one call differ. The field name is the
	// caller's text, so it is quoted.
	fieldPrioritiesDesc := ""
	if len(opt.FieldPriorities) > 0 {
		var parts []string
		for _, field := range sortedKeys(opt.FieldPriorities) {
			quoted, err := promptJSON(field)
			if err != nil {
				quoted = []byte(`""`)
			}
			parts = append(parts, fmt.Sprintf("- %s: prefer source %d", quoted, opt.FieldPriorities[field]))
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
		quoted, err := promptJSON(opt.Steering)
		if err != nil {
			quoted = []byte(`""`)
		}
		steeringNote = fmt.Sprintf("\n\nAdditional guidance: %s", quoted)
	}

	userPrompt := fmt.Sprintf(`Resolve conflicts between these sources:

%s%s`, strings.Join(sourcesJSON, "\n\n"), steeringNote)

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
		log.Error("Resolve operation LLM call failed", "error", loggedError(err))
		return ResolveResult[T]{}, fmt.Errorf("resolution failed: %w", err)
	}

	// Parse the response. The prompt asks for every member, so an answer that
	// leaves one out is refused by name: an answer without "resolved" used to come
	// back as an empty record with a nil error, and a conflict without its
	// chosen_source as source 0. Conflict is the library's own type, and its
	// reasoning is the one member it leaves optional.
	var parsed struct {
		Resolved            json.RawMessage     `json:"resolved"`
		Conflicts           []Conflict          `json:"conflicts"`
		SourceContributions map[string][]string `json:"source_contributions"`
		ModelConfidence     float64             `json:"confidence"`
	}

	if err := decodeAnswer("resolve", response, &parsed); err != nil {
		// The response holds the caller's records, so only its size is logged.
		log.Error("Resolve operation failed: parse error", "error", loggedError(err), "responseBytes", len(response))
		return ResolveResult[T]{}, fmt.Errorf("failed to parse resolution result: %w", err)
	}

	var result ResolveResult[T]
	result.SourceContributions = make(map[int][]string)
	result.Metadata = make(map[string]any)
	result.Strategy = opt.Strategy

	// The reader refuses an answer without a resolved record; a RawMessage can
	// hold a null, which is not one.
	if isJSONNull(parsed.Resolved) {
		return ResolveResult[T]{}, types.Newf(types.KindSchemaViolation, "resolve", "resolved is null")
	}
	if err := decodeCallerAnswer("resolve", string(parsed.Resolved), &result.Resolved); err != nil {
		log.Error("Resolve operation failed: resolved data parse error", "error", loggedError(err))
		return ResolveResult[T]{}, fmt.Errorf("the resolved record does not decode as the source type: %w", err)
	}
	// A list or a map the reader lets be null is an answer to the question only
	// when it is there: an empty conflicts list says there were none, and a null
	// one says nothing.
	if parsed.Conflicts == nil {
		return ResolveResult[T]{}, types.Newf(types.KindSchemaViolation, "resolve", "conflicts is null")
	}
	if parsed.SourceContributions == nil {
		return ResolveResult[T]{}, types.Newf(types.KindSchemaViolation, "resolve", "source_contributions is null")
	}
	if c := parsed.ModelConfidence; c < 0 || c > 1 {
		return ResolveResult[T]{}, types.Newf(types.KindSchemaViolation, "resolve", "the response's confidence is outside 0 to 1")
	}
	result.ModelConfidence = parsed.ModelConfidence

	// Each conflict is checked against the sources the caller supplied. The
	// index is the model's; the bound is the caller's. A conflict that omitted
	// chosen_source used to decode as source 0, which is a valid index and, under
	// the default authoritative source, a passing one.
	for i, conflict := range parsed.Conflicts {
		if err := checkResolveConflict(conflict, i, len(sources)); err != nil {
			return ResolveResult[T]{}, err
		}

		// The "authoritative" strategy documents one thing -- prefer the source
		// at AuthoritativeSource -- and each conflict reports the index it
		// actually chose. That is two integers, comparable in Go, with no model
		// claim in between. A field the caller gave a priority is the exception
		// the options themselves describe: it is expected to come from the
		// source named for it, which is how the documented example (source 0
		// authoritative, "stock" from source 1) can succeed.
		//
		// The other strategies are deliberately not checked here. "newest" and
		// "most-complete" are judgements about the source data that only the model
		// made, and re-deriving them in Go would be inventing a second answer to
		// compare against the first.
		if opt.Strategy == "authoritative" {
			expected := opt.AuthoritativeSource
			if priority, ok := opt.FieldPriorities[conflict.Field]; ok {
				expected = priority
			}
			if conflict.ChosenSource != expected {
				log.Error("A conflict was resolved against the authoritative source",
					"conflictIndex", i, "chosenSource", conflict.ChosenSource, "expectedSource", expected)
				return ResolveResult[T]{}, types.Newf(types.KindInvariantViolation, "resolve",
					"strategy is \"authoritative\" on source %d, but conflict %d was resolved from source %d, not source %d",
					opt.AuthoritativeSource, i, conflict.ChosenSource, expected)
			}
		}
		result.Conflicts = append(result.Conflicts, conflict)
	}

	// Convert source contributions from string keys to int keys. A key that is
	// not a source index is refused: it used to be read as 0 and overwrite
	// source 0's entry, on some runs and not others since map order is random.
	for key, fields := range parsed.SourceContributions {
		idx, err := strconv.Atoi(key)
		if err != nil || idx < 0 || idx >= len(sources) {
			return ResolveResult[T]{}, types.Newf(types.KindSchemaViolation, "resolve",
				"source_contributions names a source that is not one of the %d supplied", len(sources))
		}
		if _, repeated := result.SourceContributions[idx]; repeated {
			return ResolveResult[T]{}, types.Newf(types.KindSchemaViolation, "resolve",
				"source_contributions names source %d twice", idx)
		}
		result.SourceContributions[idx] = fields
	}

	log.Debug("Resolve operation succeeded",
		"conflicts", len(result.Conflicts),
		"confidence", result.ModelConfidence)

	return result, nil
}

// resolveStrategies are the strategies the prompt explains.
var resolveStrategies = []string{"newest", "most-complete", "majority", "authoritative", "merge"}

// validateResolveOptions checks the caller's options against the number of
// sources. A strategy outside the five written in the prompt would otherwise be
// the caller's own text in the system prompt, and an index outside the sources
// names a source that does not exist.
func validateResolveOptions(opt ResolveOptions, sourceCount int) error {
	if opt.Strategy != "" {
		known := false
		for _, name := range resolveStrategies {
			known = known || opt.Strategy == name
		}
		if !known {
			return types.Newf(types.KindInvalidRequest, "resolve",
				"strategy %q is not one of newest, most-complete, majority, authoritative, merge", opt.Strategy)
		}
	}
	if opt.AuthoritativeSource < 0 || opt.AuthoritativeSource >= sourceCount {
		return types.Newf(types.KindInvalidRequest, "resolve",
			"AuthoritativeSource %d is outside the %d source(s) supplied", opt.AuthoritativeSource, sourceCount)
	}
	for _, field := range sortedKeys(opt.FieldPriorities) {
		if idx := opt.FieldPriorities[field]; idx < 0 || idx >= sourceCount {
			return types.Newf(types.KindInvalidRequest, "resolve",
				"the priority for field %q names source %d, outside the %d source(s) supplied", field, idx, sourceCount)
		}
	}
	return nil
}

// checkResolveThreshold refuses a threshold in the caller's options that is not
// a proportion. The threshold is stated to the model as a request; nothing in Go
// computes similarity, so it is not checked against the answer, but 150 or NaN
// would print as nonsense in the prompt. Zero is the value of an option that was
// not set and is accepted: it stands for the default. The error names the option
// and not the value.
func checkResolveThreshold(user ResolveOptions) error {
	if math.IsNaN(user.ConflictThreshold) || user.ConflictThreshold < 0 || user.ConflictThreshold > 1 {
		return types.Newf(types.KindConfiguration, "resolve", "ConflictThreshold has to be a number from 0 to 1")
	}
	return nil
}

// checkResolveConflict checks a conflict the model reported against the number
// of sources. The reader has already refused a conflict that leaves out a member
// the prompt asks for; what it cannot see is a blank field name, an empty list
// of values, and an index that is not one of the sources. The errors name the
// conflict's position and never its field or values, which are the caller's
// data.
func checkResolveConflict(conflict Conflict, index, sourceCount int) error {
	refuse := func(format string, args ...any) error {
		return types.Newf(types.KindSchemaViolation, "resolve", "conflict %d "+format, append([]any{index}, args...)...)
	}

	if strings.TrimSpace(conflict.Field) == "" {
		return refuse("names no field")
	}
	if len(conflict.Values) == 0 {
		return refuse("lists no values")
	}
	for source := range conflict.Values {
		if source < 0 || source >= sourceCount {
			return refuse("lists a value from source %d, but %d source(s) were supplied", source, sourceCount)
		}
	}

	// A conflict citing source 4 out of a three-source list resolved from
	// nothing.
	if conflict.ChosenSource < 0 || conflict.ChosenSource >= sourceCount {
		return types.Newf(types.KindInvariantViolation, "resolve",
			"conflict %d was resolved from source %d, but %d source(s) were supplied", index, conflict.ChosenSource, sourceCount)
	}
	return nil
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
	return defaults
}
