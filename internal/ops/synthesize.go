// package ops - Synthesize operation for combining multiple sources into new insights
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
		return fmt.Errorf("invalid strategy: %s", s.Strategy)
	}
	validResolutions := map[string]bool{"latest-wins": true, "source-priority": true, "majority": true, "llm-decide": true}
	if s.ConflictResolution != "" && !validResolutions[s.ConflictResolution] {
		return fmt.Errorf("invalid conflict resolution: %s", s.ConflictResolution)
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
	return s.CommonOptions.toOpOptions()
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
	log := logger.GetLogger()
	log.Debug("Starting synthesize operation", "sourceCount", len(sources))

	var result SynthesizeResult[T]
	result.SourceCoverage = make(map[int]float64)
	result.Metadata = make(map[string]any)

	if len(sources) == 0 {
		return result, fmt.Errorf("no sources provided for synthesis")
	}

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

	// Convert sources to JSON
	sourcesJSON := make([]string, len(sources))
	for i, source := range sources {
		sourceJSON, err := json.Marshal(source)
		if err != nil {
			log.Error("Synthesize operation failed: marshal error", "index", i, "error", err)
			return result, fmt.Errorf("failed to marshal source %d: %w", i, err)
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

	focusDesc := ""
	if len(opts.FocusAreas) > 0 {
		focusDesc = fmt.Sprintf("\nFocus on: %s", strings.Join(opts.FocusAreas, ", "))
	}

	excludeDesc := ""
	if len(opts.ExcludeAspects) > 0 {
		excludeDesc = fmt.Sprintf("\nExclude: %s", strings.Join(opts.ExcludeAspects, ", "))
	}

	if opts.OutputStructure != "" {
		excludeDesc += fmt.Sprintf("\nOrganise the synthesised output as: %s", opts.OutputStructure)
	}

	var zero T
	targetSchema := GenerateTypeSchema(reflect.TypeOf(zero))

	systemPrompt := fmt.Sprintf(`You are an expert at synthesizing information from multiple sources.

Strategy: %s
Conflict resolution: %s%s%s%s%s

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
- Populate fields with task-relevant synthesized content`, strategyDesc, resolutionDesc, citeNote, insightsNote, focusDesc, excludeDesc, targetSchema)

	userPrompt := fmt.Sprintf("Synthesize these sources:\n\n%s", strings.Join(sourcesJSON, "\n\n"))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Synthesize operation LLM call failed", "error", err)
		return result, fmt.Errorf("synthesis failed: %w", err)
	}

	// Parse the response
	var parsed struct {
		Synthesized    json.RawMessage     `json:"synthesized"`
		Facts          []SynthesisFact     `json:"facts"`
		Insights       []SynthesisInsight  `json:"insights"`
		Conflicts      []SynthesisConflict `json:"conflicts"`
		SourceCoverage map[int]float64     `json:"source_coverage"`
	}

	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("Synthesize operation failed: parse error", "error", err)
		return result, fmt.Errorf("failed to parse synthesis result: %w", err)
	}

	if err := json.Unmarshal(parsed.Synthesized, &result.Synthesized); err != nil {
		var synthesizedString string
		if err2 := json.Unmarshal(parsed.Synthesized, &synthesizedString); err2 == nil {
			if err3 := ParseJSONStrict(synthesizedString, &result.Synthesized); err3 != nil {
				var zero T
				if _, ok := any(zero).(string); ok {
					result.Synthesized = any(synthesizedString).(T)
				} else {
					log.Error("Synthesize operation failed: synthesized payload parse error", "error", err3)
					return result, fmt.Errorf("failed to parse synthesized payload: %w", err3)
				}
			}
		} else {
			var zero T
			if _, ok := any(zero).(string); ok {
				result.Synthesized = any(string(parsed.Synthesized)).(T)
			} else {
				log.Error("Synthesize operation failed: synthesized payload parse error", "error", err)
				return result, fmt.Errorf("failed to parse synthesized payload: %w", err)
			}
		}
	}
	result.Facts = parsed.Facts
	result.Insights = parsed.Insights
	result.Conflicts = parsed.Conflicts
	result.SourceCoverage = parsed.SourceCoverage

	log.Debug("Synthesize operation succeeded",
		"factCount", len(result.Facts),
		"insightCount", len(result.Insights),
		"conflictCount", len(result.Conflicts))
	return result, nil
}
