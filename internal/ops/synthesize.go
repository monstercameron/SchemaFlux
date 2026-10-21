// package ops - Synthesize operation for combining multiple sources into new insights
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// SynthesizeOptions configures the Synthesize operation
type SynthesizeOptions struct {
	CommonOptions
	types.OpOptions

	// Synthesis strategy ("merge", "compare", "integrate", "reconcile")
	Strategy string

	// Conflict resolution ("latest-wins", "source-priority", "majority", "llm-decide")
	ConflictResolution string

	// Source priorities (for source-priority resolution)
	SourcePriorities []int

	// Include citations/references to sources
	CiteSources bool

	// Generate insights beyond simple merging
	GenerateInsights bool

	// Include confidence for synthesized facts
	IncludeConfidence bool

	// Output structure preference
	OutputStructure string

	// Focus areas for synthesis
	FocusAreas []string

	// Exclude certain aspects
	ExcludeAspects []string
}

// NewSynthesizeOptions creates SynthesizeOptions with defaults
func NewSynthesizeOptions() SynthesizeOptions {
	return SynthesizeOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		Strategy:           "integrate",
		ConflictResolution: "llm-decide",
		CiteSources:        true,
		GenerateInsights:   true,
		IncludeConfidence:  false,
	}
}

// Validate validates SynthesizeOptions
func (s SynthesizeOptions) Validate() error {
	if err := s.CommonOptions.Validate(); err != nil {
		return err
	}
	validStrategies := map[string]bool{"merge": true, "compare": true, "integrate": true, "reconcile": true}
	if s.Strategy != "" && !validStrategies[s.Strategy] {
		return fmt.Errorf("synthesis strategy must be merge, compare, integrate, or reconcile")
	}
	validResolutions := map[string]bool{"latest-wins": true, "source-priority": true, "majority": true, "llm-decide": true}
	if s.ConflictResolution != "" && !validResolutions[s.ConflictResolution] {
		return fmt.Errorf("conflict resolution must be latest-wins, source-priority, majority, or llm-decide")
	}
	return nil
}

// WithStrategy sets the synthesis strategy
func (s SynthesizeOptions) WithStrategy(strategy string) SynthesizeOptions {
	s.Strategy = strategy
	return s
}

// WithConflictResolution sets the conflict resolution method
func (s SynthesizeOptions) WithConflictResolution(resolution string) SynthesizeOptions {
	s.ConflictResolution = resolution
	return s
}

// WithSourcePriorities sets source priorities
func (s SynthesizeOptions) WithSourcePriorities(priorities []int) SynthesizeOptions {
	s.SourcePriorities = priorities
	return s
}

// WithCiteSources enables source citations
func (s SynthesizeOptions) WithCiteSources(cite bool) SynthesizeOptions {
	s.CiteSources = cite
	return s
}

// WithGenerateInsights enables insight generation
func (s SynthesizeOptions) WithGenerateInsights(generate bool) SynthesizeOptions {
	s.GenerateInsights = generate
	return s
}

// WithIncludeConfidence enables confidence scores
func (s SynthesizeOptions) WithIncludeConfidence(include bool) SynthesizeOptions {
	s.IncludeConfidence = include
	return s
}

// WithOutputStructure sets the output structure
func (s SynthesizeOptions) WithOutputStructure(structure string) SynthesizeOptions {
	s.OutputStructure = structure
	return s
}

// WithFocusAreas sets focus areas
func (s SynthesizeOptions) WithFocusAreas(areas []string) SynthesizeOptions {
	s.FocusAreas = areas
	return s
}

// WithExcludeAspects sets aspects to exclude
func (s SynthesizeOptions) WithExcludeAspects(aspects []string) SynthesizeOptions {
	s.ExcludeAspects = aspects
	return s
}

// WithSteering sets the steering prompt
func (s SynthesizeOptions) WithSteering(steering string) SynthesizeOptions {
	s.CommonOptions = s.CommonOptions.WithSteering(steering)
	return s
}

// WithMode sets the mode
func (s SynthesizeOptions) WithMode(mode types.Mode) SynthesizeOptions {
	s.CommonOptions = s.CommonOptions.WithMode(mode)
	return s
}

// WithIntelligence sets the intelligence level
func (s SynthesizeOptions) WithIntelligence(intelligence types.Speed) SynthesizeOptions {
	s.CommonOptions = s.CommonOptions.WithIntelligence(intelligence)
	return s
}

func (s SynthesizeOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(s.CommonOptions, s.OpOptions)
}

func (s SynthesizeOptions) optionHalves() (CommonOptions, types.OpOptions) {
	return s.CommonOptions, s.OpOptions
}

// SynthesisFact represents a synthesized fact with optional citation
type SynthesisFact struct {
	Fact    string `json:"fact"`
	Sources []int  `json:"sources"`
	// ModelConfidence is the model's own claim about this result, not a measurement.
	// It is not calibrated and is not comparable across models or prompts.
	ModelConfidence float64  `json:"confidence,omitempty"`
	Conflicts       []string `json:"conflicts,omitempty"`
}

// SynthesisInsight represents a generated insight
type SynthesisInsight struct {
	Insight    string `json:"insight"`
	Supporting []int  `json:"supporting_sources"`
	Type       string `json:"type"` // "pattern", "gap", "contradiction", "trend"
	// ModelConfidence is the model's own claim about this result, not a measurement.
	// It is not calibrated and is not comparable across models or prompts.
	ModelConfidence float64 `json:"confidence,omitempty"`
}

// SynthesisConflict represents a conflict between sources
type SynthesisConflict struct {
	Topic      string         `json:"topic"`
	Positions  map[int]string `json:"positions"` // source index -> position
	Resolution string         `json:"resolution"`
	Chosen     int            `json:"chosen_source"`
}

// SynthesizeResult contains the results of synthesis
type SynthesizeResult[T any] struct {
	Synthesized    T                   `json:"synthesized"`
	Facts          []SynthesisFact     `json:"facts,omitempty"`
	Insights       []SynthesisInsight  `json:"insights,omitempty"`
	Conflicts      []SynthesisConflict `json:"conflicts,omitempty"`
	SourceCoverage map[int]float64     `json:"source_coverage"`
	Metadata       map[string]any      `json:"metadata,omitempty"`
}

// Synthesize combines multiple sources into a unified result with new insights.
// Unlike Merge (simple combination), Synthesize generates insights and handles conflicts.
//
// Type parameter T specifies the output type.
//
// Examples:
//
//	// Synthesize research from multiple sources
//	result, err := Synthesize[Report](sources, NewSynthesizeOptions().
//	    WithStrategy("integrate").
//	    WithCiteSources(true).
//	    WithGenerateInsights(true))
//
//	// Reconcile conflicting data
//	result, err := Synthesize[Record](records, NewSynthesizeOptions().
//	    WithStrategy("reconcile").
//	    WithConflictResolution("source-priority").
//	    WithSourcePriorities([]int{0, 2, 1}))
//
//	// Compare and contrast sources
//	result, err := Synthesize[Analysis](articles, NewSynthesizeOptions().
//	    WithStrategy("compare").
//	    WithFocusAreas([]string{"methodology", "conclusions"}))
func Synthesize[T any](sources []any, opts SynthesizeOptions) (SynthesizeResult[T], error) {
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting synthesize operation", "sourceCount", len(sources))

	if len(sources) == 0 {
		return SynthesizeResult[T]{}, types.Classified(types.KindConfiguration, fmt.Errorf("no sources provided for synthesis"))
	}

	// Validate options
	if err := opts.Validate(); err != nil {
		return SynthesizeResult[T]{}, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}
	if err := checkSourcePriorities(opts, len(sources)); err != nil {
		return SynthesizeResult[T]{}, err
	}

	opt := opts.toOpOptions()

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var cancel context.CancelFunc
	ctx, cancel = operationContext(ctx, 0)
	defer cancel()

	// Convert sources to JSON
	sourcesJSON := make([]string, len(sources))
	for i, source := range sources {
		sourceJSON, err := json.Marshal(source)
		if err != nil {
			log.Error("Synthesize operation failed: marshal error", "index", i, "error", loggedError(err))
			return SynthesizeResult[T]{}, types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal source %d: %w", i, err))
		}
		sourcesJSON[i] = fmt.Sprintf("[Source %d]\n%s", i, string(sourceJSON))
	}

	// Build strategy description
	strategyDesc := ""
	switch opts.Strategy {
	case "merge":
		strategyDesc = "Combine all sources into a unified whole."
	case "compare":
		strategyDesc = "Compare and contrast the sources, highlighting differences."
	case "integrate":
		strategyDesc = "Integrate sources into a cohesive narrative with insights."
	case "reconcile":
		strategyDesc = "Reconcile conflicting information between sources."
	}

	resolutionDesc := ""
	switch opts.ConflictResolution {
	case "latest-wins":
		resolutionDesc = "When sources conflict, prefer the last source."
	case "source-priority":
		if len(opts.SourcePriorities) > 0 {
			priorities := make([]string, len(opts.SourcePriorities))
			for i, p := range opts.SourcePriorities {
				priorities[i] = fmt.Sprintf("%d", p)
			}
			resolutionDesc = fmt.Sprintf("Source priority order: %s", strings.Join(priorities, " > "))
		}
	case "majority":
		resolutionDesc = "When sources conflict, use the majority view."
	case "llm-decide":
		resolutionDesc = "Use judgment to resolve conflicts based on credibility and context."
	}

	citeNote := ""
	if opts.CiteSources {
		citeNote = "\nCite sources by index (e.g., [Source 0]) when presenting facts."
	}

	insightsNote := ""
	if opts.GenerateInsights {
		insightsNote = "\nGenerate insights: patterns, gaps, contradictions, and trends across sources."
	}

	// The three values below are the caller's and are written into the system
	// prompt, so each is quoted as JSON in place: a newline in one cannot start
	// a line of its own and read as one of the library's rules.
	focusDesc := ""
	if len(opts.FocusAreas) > 0 {
		focusDesc = fmt.Sprintf("\nFocus on: %s", quotedList(opts.FocusAreas))
	}

	excludeDesc := ""
	if len(opts.ExcludeAspects) > 0 {
		excludeDesc = fmt.Sprintf("\nExclude: %s", quotedList(opts.ExcludeAspects))
	}

	structureDesc := ""
	if opts.OutputStructure != "" {
		structureDesc = fmt.Sprintf("\nOrganise the synthesised output as: %s", quotedText(opts.OutputStructure))
	}

	var zero T
	targetSchema := GenerateTypeSchema(reflect.TypeOf(zero))

	systemPrompt := fmt.Sprintf(`You are an expert at synthesizing information from multiple sources.

Strategy: %s
Conflict resolution: %s%s%s%s%s%s

The "synthesized" field must match this target schema:
%s

Return a JSON object with:
{
  "synthesized": {},
  "facts": [
    {
      "fact": "Key fact from sources",
      "sources": [0, 2],
      "conflicts": ["any conflicting info"]
    }
  ],
  "insights": [
    {
      "insight": "Pattern or trend observed",
      "supporting_sources": [0, 1, 2],
      "type": "pattern|gap|contradiction|trend"
    }
  ],
  "conflicts": [
    {
      "topic": "What the conflict is about",
      "positions": {"0": "Source 0 says...", "1": "Source 1 says..."},
      "resolution": "How it was resolved",
      "chosen_source": 0
    }
  ],
  "source_coverage": {"0": 0.8, "1": 0.9}
}

Rules for "synthesized":
- It must be a valid instance of the target schema above
- Do not replace it with a free-form string unless the target schema itself is a string
- Populate fields with task-relevant synthesized content`, strategyDesc, resolutionDesc, citeNote, insightsNote, focusDesc, excludeDesc, structureDesc, targetSchema)

	userPrompt := fmt.Sprintf("Synthesize these sources:\n\n%s", strings.Join(sourcesJSON, "\n\n"))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Synthesize operation LLM call failed", "error", loggedError(err))
		return SynthesizeResult[T]{}, fmt.Errorf("synthesis failed: %w", err)
	}

	// Parse the response. The prompt asks for every member, so an answer that
	// leaves one out is refused by name. The insights are the exception: they are
	// asked for only when GenerateInsights is on, so the member is optional here,
	// and below an answer without them is refused when the option asked for them.
	var parsed struct {
		Synthesized    json.RawMessage     `json:"synthesized"`
		Facts          []SynthesisFact     `json:"facts"`
		Insights       []SynthesisInsight  `json:"insights,omitempty"` // asked for only under GenerateInsights
		Conflicts      []SynthesisConflict `json:"conflicts"`
		SourceCoverage map[int]float64     `json:"source_coverage"`
	}

	if err := decodeAnswer("synthesize", response, &parsed); err != nil {
		log.Error("Synthesize operation failed: parse error", "error", loggedError(err))
		return SynthesizeResult[T]{}, fmt.Errorf("failed to parse synthesis result: %w", err)
	}

	// An empty list says the model found no insights; a missing or null one says
	// it did not answer.
	if opts.GenerateInsights && parsed.Insights == nil {
		log.Error("Synthesize operation failed: the answer has no insights")
		return SynthesizeResult[T]{}, types.Newf(types.KindSchemaViolation, "synthesize",
			"insights is missing, and insights were asked for")
	}

	synthesized, err := decodeSynthesized[T](parsed.Synthesized)
	if err != nil {
		log.Error("Synthesize operation failed: synthesized payload", "error", loggedError(err))
		return SynthesizeResult[T]{}, err
	}

	if err := checkSynthesisReferences(parsed.Facts, parsed.Insights, parsed.Conflicts, parsed.SourceCoverage, len(sources), opts.CiteSources); err != nil {
		log.Error("Synthesize operation failed: the answer does not fit the sources", "error", loggedError(err))
		return SynthesizeResult[T]{}, err
	}

	result := SynthesizeResult[T]{
		Synthesized:    synthesized,
		Facts:          parsed.Facts,
		Insights:       parsed.Insights,
		Conflicts:      parsed.Conflicts,
		SourceCoverage: parsed.SourceCoverage,
		Metadata:       make(map[string]any),
	}
	if result.SourceCoverage == nil {
		// A model that reported no coverage has reported none, and the empty map
		// says so. It used to replace the empty map with nil.
		result.SourceCoverage = make(map[int]float64)
	}

	log.Debug("Synthesize operation succeeded",
		"factCount", len(result.Facts),
		"insightCount", len(result.Insights),
		"conflictCount", len(result.Conflicts))
	return result, nil
}

// insightTypes are the words SynthesisInsight.Type may hold.
var insightTypes = []string{"pattern", "gap", "contradiction", "trend"}

// checkSourcePriorities refuses a SourcePriorities list that no answer could
// follow: an index outside the sources, a source named twice, or the
// source-priority resolution chosen with no priorities. It was a sentence in
// the prompt and was not looked at.
func checkSourcePriorities(opts SynthesizeOptions, sourceCount int) error {
	if opts.ConflictResolution == "source-priority" && len(opts.SourcePriorities) == 0 {
		return types.Newf(types.KindConfiguration, "synthesize",
			"the source-priority resolution needs SourcePriorities")
	}
	seen := make(map[int]bool, len(opts.SourcePriorities))
	for i, priority := range opts.SourcePriorities {
		if priority < 0 || priority >= sourceCount {
			return types.Newf(types.KindConfiguration, "synthesize",
				"source priority %d names source %d, but %d source(s) were supplied", i, priority, sourceCount)
		}
		if seen[priority] {
			return types.Newf(types.KindConfiguration, "synthesize",
				"source priority %d names source %d, which an earlier priority already named", i, priority)
		}
		seen[priority] = true
	}
	return nil
}

// decodeSynthesized reads the "synthesized" value into a T, or refuses it.
//
// A value that is missing, null or empty is refused, and so is one that shares
// no field with a struct T: json.Unmarshal takes null and {} into any T without
// complaint and leaves every field zero. For a string T the value has to be a
// JSON string and not an empty one. For any other T the value is decoded as
// JSON, and as a last resort as a JSON string that itself holds JSON, which is
// how a model that double-encodes answers. The result is assigned only when a
// decode succeeded, so a decode that failed half way leaves nothing behind.
func decodeSynthesized[T any](raw json.RawMessage) (T, error) {
	var zero T
	refuse := func(format string, args ...any) (T, error) {
		return zero, types.Newf(types.KindSchemaViolation, "synthesize", format, args...)
	}

	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return refuse("the response has no synthesized value")
	}

	var value T
	if reflect.TypeOf(&value).Elem().Kind() == reflect.String {
		var plain string
		if err := json.Unmarshal(raw, &plain); err != nil {
			return refuse("the synthesized value is not a string, which the target type is")
		}
		if strings.TrimSpace(plain) == "" {
			return refuse("the synthesized value is an empty string")
		}
		reflect.ValueOf(&value).Elem().SetString(plain)
		return value, nil
	}

	if err := decodeCallerAnswer("synthesize", text, &value); err == nil {
		return value, nil
	}

	var inner string
	if err := json.Unmarshal(raw, &inner); err == nil {
		var decoded T
		if err := decodeCallerAnswer("synthesize", inner, &decoded); err == nil {
			return decoded, nil
		}
	}
	return refuse("the synthesized value does not fit the target type")
}

// checkSynthesisReferences holds everything the model said about the sources to
// the sources that were given, as verify.go does for a claim's citations: an
// index has to be one of them, coverage is a fraction, an insight has one of
// the four types, and a model's own confidences are on the 0 to 1 scale. A fact
// has to cite a source when CiteSources was asked for.
func checkSynthesisReferences(facts []SynthesisFact, insights []SynthesisInsight, conflicts []SynthesisConflict, coverage map[int]float64, sourceCount int, citeSources bool) error {
	inRange := func(index int) bool { return index >= 0 && index < sourceCount }
	refuse := func(format string, args ...any) error {
		return types.Newf(types.KindInvariantViolation, "synthesize", format, args...)
	}

	for i, fact := range facts {
		if citeSources && len(fact.Sources) == 0 {
			return refuse("fact %d cites no source, and citations were asked for", i)
		}
		for _, source := range fact.Sources {
			if !inRange(source) {
				return refuse("fact %d cites source %d, but %d source(s) were supplied", i, source, sourceCount)
			}
		}
		if err := onScale("synthesize", "the confidence of fact "+strconv.Itoa(i), fact.ModelConfidence); err != nil {
			return err
		}
	}
	for i, insight := range insights {
		if !oneOf(insight.Type, insightTypes) {
			return refuse("insight %d has a type that is not pattern, gap, contradiction or trend", i)
		}
		for _, source := range insight.Supporting {
			if !inRange(source) {
				return refuse("insight %d names source %d, but %d source(s) were supplied", i, source, sourceCount)
			}
		}
		if err := onScale("synthesize", "the confidence of insight "+strconv.Itoa(i), insight.ModelConfidence); err != nil {
			return err
		}
	}
	for i, conflict := range conflicts {
		if !inRange(conflict.Chosen) {
			return refuse("conflict %d chooses source %d, but %d source(s) were supplied", i, conflict.Chosen, sourceCount)
		}
		for _, position := range sortedIntKeys(conflict.Positions) {
			if !inRange(position) {
				return refuse("conflict %d reports a position for source %d, but %d source(s) were supplied", i, position, sourceCount)
			}
		}
	}
	for _, source := range sortedIntKeys(coverage) {
		if !inRange(source) {
			return refuse("the coverage names source %d, but %d source(s) were supplied", source, sourceCount)
		}
		if err := onScale("synthesize", "the coverage of source "+strconv.Itoa(source), coverage[source]); err != nil {
			return err
		}
	}
	return nil
}

// sortedIntKeys returns a map's integer keys in ascending order, so the first
// failure a check reports does not depend on map iteration order.
func sortedIntKeys[V any](m map[int]V) []int {
	keys := make([]int, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	return keys
}
