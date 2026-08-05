// package ops - Match operation for finding best matches between collections
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// MatchOptions configures the Match operation
type MatchOptions struct {
	CommonOptions
	types.OpOptions

	// Matching strategy ("best-fit", "all-matches", "one-to-one", "one-to-many")
	Strategy string

	// Minimum similarity threshold for a match (0.0-1.0)
	Threshold float64

	// Maximum number of matches per source item (0 for unlimited)
	MaxMatches int

	// Fields to use for matching
	MatchFields []string

	// Field weights for matching
	FieldWeights map[string]float64

	// Include match explanations
	IncludeExplanations bool

	// Allow partial matches
	AllowPartial bool

	// Matching criteria (natural language)
	MatchCriteria string

	// Bidirectional matching (match A->B and B->A)
	Bidirectional bool
}

// NewMatchOptions creates MatchOptions with defaults
func NewMatchOptions() MatchOptions {
	return MatchOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		Strategy:            "best-fit",
		Threshold:           0.5,
		MaxMatches:          1,
		IncludeExplanations: true,
		AllowPartial:        true,
		Bidirectional:       false,
	}
}

// Validate validates MatchOptions
func (m MatchOptions) Validate() error {
	if err := m.CommonOptions.Validate(); err != nil {
		return err
	}
	validStrategies := map[string]bool{"best-fit": true, "all-matches": true, "one-to-one": true, "one-to-many": true}
	if m.Strategy != "" && !validStrategies[m.Strategy] {
		return fmt.Errorf("invalid strategy: %s", m.Strategy)
	}
	if m.Threshold < 0 || m.Threshold > 1 {
		return fmt.Errorf("threshold must be between 0 and 1, got %f", m.Threshold)
	}
	if m.MaxMatches < 0 {
		return fmt.Errorf("max matches cannot be negative, got %d", m.MaxMatches)
	}
	return nil
}

// WithStrategy sets the matching strategy
func (m MatchOptions) WithStrategy(strategy string) MatchOptions {
	m.Strategy = strategy
	return m
}

// WithThreshold sets the similarity threshold
func (m MatchOptions) WithThreshold(threshold float64) MatchOptions {
	m.Threshold = threshold
	return m
}

// WithMaxMatches sets the maximum matches per source item
func (m MatchOptions) WithMaxMatches(max int) MatchOptions {
	m.MaxMatches = max
	return m
}

// WithMatchFields sets the fields to use for matching
func (m MatchOptions) WithMatchFields(fields []string) MatchOptions {
	m.MatchFields = fields
	return m
}

// WithFieldWeights sets field weights for matching
func (m MatchOptions) WithFieldWeights(weights map[string]float64) MatchOptions {
	m.FieldWeights = weights
	return m
}

// WithIncludeExplanations enables match explanations
func (m MatchOptions) WithIncludeExplanations(include bool) MatchOptions {
	m.IncludeExplanations = include
	return m
}

// WithAllowPartial allows partial matches
func (m MatchOptions) WithAllowPartial(allow bool) MatchOptions {
	m.AllowPartial = allow
	return m
}

// WithMatchCriteria sets the matching criteria
func (m MatchOptions) WithMatchCriteria(criteria string) MatchOptions {
	m.MatchCriteria = criteria
	return m
}

// WithBidirectional enables bidirectional matching
func (m MatchOptions) WithBidirectional(bidirectional bool) MatchOptions {
	m.Bidirectional = bidirectional
	return m
}

// WithSteering sets the steering prompt
func (m MatchOptions) WithSteering(steering string) MatchOptions {
	m.CommonOptions = m.CommonOptions.WithSteering(steering)
	return m
}

// WithMode sets the mode
func (m MatchOptions) WithMode(mode types.Mode) MatchOptions {
	m.CommonOptions = m.CommonOptions.WithMode(mode)
	return m
}

// WithIntelligence sets the intelligence level
func (m MatchOptions) WithIntelligence(intelligence types.Speed) MatchOptions {
	m.CommonOptions = m.CommonOptions.WithIntelligence(intelligence)
	return m
}

func (m MatchOptions) toOpOptions() types.OpOptions {
	return m.CommonOptions.toOpOptions()
}

// MatchPair represents a single match between source and target
type MatchPair[S any, T any] struct {
	Source      S                  `json:"source"`
	SourceIndex int                `json:"source_index"`
	Target      T                  `json:"target"`
	TargetIndex int                `json:"target_index"`
	Score       float64            `json:"score"`
	Explanation string             `json:"explanation,omitempty"`
	FieldScores map[string]float64 `json:"field_scores,omitempty"`
	IsPartial   bool               `json:"is_partial,omitempty"`
}

// MatchResult contains the results of matching
type MatchResult[S any, T any] struct {
	Matches          []MatchPair[S, T] `json:"matches"`
	UnmatchedSources []int             `json:"unmatched_sources"`
	UnmatchedTargets []int             `json:"unmatched_targets"`
	TotalMatches     int               `json:"total_matches"`
	AverageScore     float64           `json:"average_score"`
	Metadata         map[string]any    `json:"metadata,omitempty"`
}

// SemanticMatch finds the best matches between two collections using semantic understanding.
// Useful for entity resolution, deduplication, record linking, and fuzzy matching.
// Note: Named SemanticMatch to avoid conflict with the control-flow Match operation.
//
// Type parameters:
//   - S: Source item type
//   - T: Target item type
//
// Examples:
//
//	// Match resumes to job requirements
//	result, err := SemanticMatch(candidates, requirements, NewMatchOptions().
//	    WithStrategy("best-fit").
//	    WithThreshold(0.7))
//
//	// Product matching with field weights
//	result, err := SemanticMatch(products1, products2, NewMatchOptions().
//	    WithMatchFields([]string{"name", "description", "sku"}).
//	    WithFieldWeights(map[string]float64{"sku": 2.0, "name": 1.5}))
//
//	// One-to-one matching for deduplication
//	result, err := SemanticMatch(records, masterList, NewMatchOptions().
//	    WithStrategy("one-to-one").
//	    WithThreshold(0.85))
func SemanticMatch[S any, T any](sources []S, targets []T, opts MatchOptions) (MatchResult[S, T], error) {
	log := logger.GetLogger()
	log.Debug("Starting match operation", "sourceCount", len(sources), "targetCount", len(targets))

	var result MatchResult[S, T]
	result.Metadata = make(map[string]any)

	if len(sources) == 0 || len(targets) == 0 {
		for i := range sources {
			result.UnmatchedSources = append(result.UnmatchedSources, i)
		}
		for i := range targets {
			result.UnmatchedTargets = append(result.UnmatchedTargets, i)
		}
		return result, nil
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

	// Convert items to JSON
	sourcesJSON := make([]string, len(sources))
	for i, item := range sources {
		itemJSON, err := json.Marshal(item)
		if err != nil {
			log.Error("Match operation failed: marshal source error", "index", i, "error", err)
			return result, fmt.Errorf("failed to marshal source %d: %w", i, err)
		}
		sourcesJSON[i] = fmt.Sprintf("[S%d] %s", i, string(itemJSON))
	}

	targetsJSON := make([]string, len(targets))
	for i, item := range targets {
		itemJSON, err := json.Marshal(item)
		if err != nil {
			log.Error("Match operation failed: marshal target error", "index", i, "error", err)
			return result, fmt.Errorf("failed to marshal target %d: %w", i, err)
		}
		targetsJSON[i] = fmt.Sprintf("[T%d] %s", i, string(itemJSON))
	}

	// Build strategy description
	strategyDesc := ""
	switch opts.Strategy {
	case "best-fit":
		strategyDesc = "Find the single best match for each source item."
	case "all-matches":
		strategyDesc = "Find all matches above threshold for each source item."
	case "one-to-one":
		strategyDesc = "Each source matches at most one target, and vice versa."
	case "one-to-many":
		strategyDesc = "Each source can match multiple targets."
	}

	maxMatchesDesc := ""
	if opts.MaxMatches > 0 {
		maxMatchesDesc = fmt.Sprintf("\nMaximum %d matches per source item.", opts.MaxMatches)
	}

	fieldsDesc := ""
	if len(opts.MatchFields) > 0 {
		fieldsDesc = fmt.Sprintf("\nMatch on these fields: %s", strings.Join(opts.MatchFields, ", "))
		if len(opts.FieldWeights) > 0 {
			weights := make([]string, 0, len(opts.FieldWeights))
			for field, weight := range opts.FieldWeights {
				weights = append(weights, fmt.Sprintf("%s=%.1fx", field, weight))
			}
			fieldsDesc += fmt.Sprintf("\nField weights: %s", strings.Join(weights, ", "))
		}
	}

	criteriaDesc := ""
	if opts.MatchCriteria != "" {
		criteriaDesc = fmt.Sprintf("\nMatching criteria: %s", opts.MatchCriteria)
	}

	partialDesc := ""
	if opts.AllowPartial {
		partialDesc = "\nAllow partial matches (flag them in output)."
	}

	explanationNote := ""
	if opts.IncludeExplanations {
		explanationNote = "\nInclude brief explanations for why items matched."
	}

	systemPrompt := fmt.Sprintf(`You are an expert at semantic matching and entity resolution.

Strategy: %s%s%s%s%s%s

Minimum similarity threshold: %.2f

Return a JSON object with:
{
  "matches": [
    {
      "source_index": 0,
      "target_index": 2,
      "score": 0.87,
      "explanation": "Why they match",
      "is_partial": false
    }
  ],
  "unmatched_sources": [1, 3],
  "unmatched_targets": [0, 4]
}`, strategyDesc, maxMatchesDesc, fieldsDesc, criteriaDesc, partialDesc, explanationNote, opts.Threshold)

	userPrompt := fmt.Sprintf("Match these source items:\n%s\n\nTo these target items:\n%s",
		strings.Join(sourcesJSON, "\n"), strings.Join(targetsJSON, "\n"))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Match operation LLM call failed", "error", err)
		return result, fmt.Errorf("matching failed: %w", err)
	}

	// Parse the response
	var parsed struct {
		Matches []struct {
			SourceIndex int                `json:"source_index"`
			TargetIndex int                `json:"target_index"`
			Score       float64            `json:"score"`
			Explanation string             `json:"explanation"`
			FieldScores map[string]float64 `json:"field_scores"`
			IsPartial   bool               `json:"is_partial"`
		} `json:"matches"`
		UnmatchedSources []int `json:"unmatched_sources"`
		UnmatchedTargets []int `json:"unmatched_targets"`
	}

	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("Match operation failed: parse error", "error", err)
		return result, fmt.Errorf("failed to parse match result: %w", err)
	}

	// Build match pairs
	var totalScore float64
	for _, m := range parsed.Matches {
		if m.SourceIndex >= 0 && m.SourceIndex < len(sources) &&
			m.TargetIndex >= 0 && m.TargetIndex < len(targets) {
			pair := MatchPair[S, T]{
				Source:      sources[m.SourceIndex],
				SourceIndex: m.SourceIndex,
				Target:      targets[m.TargetIndex],
				TargetIndex: m.TargetIndex,
				Score:       m.Score,
				Explanation: m.Explanation,
				FieldScores: m.FieldScores,
				IsPartial:   m.IsPartial,
			}
			result.Matches = append(result.Matches, pair)
			totalScore += m.Score
		}
	}

	result.UnmatchedSources = parsed.UnmatchedSources
	result.UnmatchedTargets = parsed.UnmatchedTargets
	result.TotalMatches = len(result.Matches)
	if result.TotalMatches > 0 {
		result.AverageScore = totalScore / float64(result.TotalMatches)
	}

	log.Debug("Match operation succeeded",
		"totalMatches", result.TotalMatches,
		"unmatchedSources", len(result.UnmatchedSources),
		"unmatchedTargets", len(result.UnmatchedTargets))
	return result, nil
}

// MatchOne finds matches for a single source item against a collection
func MatchOne[S any, T any](source S, targets []T, opts MatchOptions) ([]MatchPair[S, T], error) {
	result, err := SemanticMatch([]S{source}, targets, opts)
	if err != nil {
		return nil, err
	}
	return result.Matches, nil
}
