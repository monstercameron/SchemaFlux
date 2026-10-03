// Match operation for finding best matches between collections.

package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// MatchOptions configures the Match operation
type MatchOptions struct {
	CommonOptions
	types.OpOptions

	// Matching strategy ("best-fit", "all-matches", "one-to-one", "one-to-many").
	// Only "one-to-one" is checked against the answer; the others are guidance.
	Strategy string

	// Minimum similarity threshold for a match (0.0-1.0). A pair scored below it
	// is refused, not dropped.
	Threshold float64

	// Maximum number of matches per source item (0 for unlimited)
	MaxMatches int

	// Fields to use for matching
	MatchFields []string

	// Field weights for matching
	FieldWeights map[string]float64

	// Include match explanations
	IncludeExplanations bool

	// Allow partial matches. When it is false, a pair the model flags as
	// partial is refused.
	AllowPartial bool

	// Matching criteria (natural language). Guidance to the model, not checked.
	MatchCriteria string

	// Bidirectional matching (match A->B and B->A). A pair that another
	// returned pair beats, for its source or for its target, is refused.
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
func (m MatchOptions) WithMaxMatches(limit int) MatchOptions {
	m.MaxMatches = limit
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
	return mergeEmbeddedOpOptions(m.CommonOptions, m.OpOptions)
}

func (m MatchOptions) optionHalves() (CommonOptions, types.OpOptions) {
	return m.CommonOptions, m.OpOptions
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
	// AverageScore is the mean of the pairs' scores, worked out here; it is 0
	// when there are no pairs.
	AverageScore float64 `json:"average_score"`
	// Metadata is not written by SemanticMatch.
	Metadata map[string]any `json:"metadata,omitempty"`
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
//
// What the answer has to satisfy, beyond being the right shape: every pair
// names a source and a target that exist, once; every score is a number from 0
// to 1 and meets Threshold; no source has more pairs than MaxMatches; with the
// "one-to-one" strategy no source and no target appears twice; with
// AllowPartial false no pair is flagged partial; with Bidirectional no pair is
// beaten by another returned pair for the same source or the same target; and
// the unmatched lists are exactly the sources and targets that have no pair. An
// answer that breaks one is refused, and the result beside the error is empty.
//
// MatchFields, FieldWeights and MatchCriteria are guidance to the model and are
// not checked: what a model weighed to reach a score is not something the
// answer lets Go verify. The "best-fit", "all-matches" and "one-to-many"
// strategies are guidance too, apart from the limit MaxMatches puts on them.
func SemanticMatch[S any, T any](sources []S, targets []T, opts MatchOptions) (MatchResult[S, T], error) {
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting match operation", "sourceCount", len(sources), "targetCount", len(targets))

	if len(sources) == 0 || len(targets) == 0 {
		// Nothing can be matched, so no model is asked: every item there is
		// is unmatched.
		var result MatchResult[S, T]
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
		return MatchResult[S, T]{}, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	opt := opts.toOpOptions()

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var cancel context.CancelFunc
	ctx, cancel = operationContext(ctx, 0)
	defer cancel()

	// Convert items to JSON
	sourcesJSON := make([]string, len(sources))
	for i, item := range sources {
		itemJSON, err := promptJSON(item)
		if err != nil {
			log.Error("Match operation failed: marshal source error", "index", i, "error", err)
			return MatchResult[S, T]{}, types.Newf(types.KindInvalidRequest, "match", "source %d could not be written as JSON", i)
		}
		sourcesJSON[i] = fmt.Sprintf("[S%d] %s", i, string(itemJSON))
	}

	targetsJSON := make([]string, len(targets))
	for i, item := range targets {
		itemJSON, err := promptJSON(item)
		if err != nil {
			log.Error("Match operation failed: marshal target error", "index", i, "error", err)
			return MatchResult[S, T]{}, types.Newf(types.KindInvalidRequest, "match", "target %d could not be written as JSON", i)
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

	// The field names, weights and criteria are the caller's text. They are
	// written as JSON strings, and the weights in the order of their names, so
	// that the same options give the same prompt and a value cannot read as one
	// of the instructions.
	fieldsDesc := ""
	if len(opts.MatchFields) > 0 {
		fieldsDesc = fmt.Sprintf("\nMatch on these fields: %s", quotedList(opts.MatchFields))
		if len(opts.FieldWeights) > 0 {
			weights := make([]string, 0, len(opts.FieldWeights))
			for _, field := range sortedKeys(opts.FieldWeights) {
				weights = append(weights, fmt.Sprintf("%s=%.1fx", quotedText(field), opts.FieldWeights[field]))
			}
			fieldsDesc += fmt.Sprintf("\nField weights: %s", strings.Join(weights, ", "))
		}
	}

	criteriaDesc := ""
	if opts.MatchCriteria != "" {
		criteriaDesc = fmt.Sprintf("\nMatching criteria: %s", quotedText(opts.MatchCriteria))
	}

	partialDesc := ""
	if opts.AllowPartial {
		partialDesc = "\nAllow partial matches (flag them in output)."
	}

	explanationNote := ""
	if opts.IncludeExplanations {
		explanationNote = "\nInclude brief explanations for why items matched."
	}

	if opts.Bidirectional {
		explanationNote += "\nA match must hold in both directions: the target must be the best match for the source, and the source the best match for the target."
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
		return MatchResult[S, T]{}, fmt.Errorf("matching failed: %w", err)
	}

	result, err := readMatchAnswer(sources, targets, opts, response)
	if err != nil {
		log.Error("Match operation refused the answer", "error", err)
		return MatchResult[S, T]{}, err
	}

	log.Debug("Match operation succeeded",
		"totalMatches", result.TotalMatches,
		"unmatchedSources", len(result.UnmatchedSources),
		"unmatchedTargets", len(result.UnmatchedTargets))
	return result, nil
}

// matchEntry is one pair of the answer as the model wrote it. The two indices
// and the score stay raw so that a pair that leaves one out can be told from a
// pair that says 0.
type matchEntry struct {
	SourceIndex json.RawMessage    `json:"source_index"`
	TargetIndex json.RawMessage    `json:"target_index"`
	Score       json.RawMessage    `json:"score"`
	Explanation string             `json:"explanation"`
	FieldScores map[string]float64 `json:"field_scores"`
	IsPartial   bool               `json:"is_partial"`
}

// readMatchAnswer reads the model's answer as a relation between the sources and
// the targets it was given, and refuses one that is not a consistent relation.
func readMatchAnswer[S any, T any](sources []S, targets []T, opts MatchOptions, response string) (MatchResult[S, T], error) {
	const op = "match"
	refuse := func(kind types.ErrorKind, format string, args ...any) (MatchResult[S, T], error) {
		return MatchResult[S, T]{}, types.Newf(kind, op, format, args...)
	}

	answer, err := readAnswerObject(op, response)
	if err != nil {
		return MatchResult[S, T]{}, err
	}
	entries, err := answer.elements("matches")
	if err != nil {
		return MatchResult[S, T]{}, err
	}
	unmatchedSources, err := answer.wholeNumbers("unmatched_sources")
	if err != nil {
		return MatchResult[S, T]{}, err
	}
	unmatchedTargets, err := answer.wholeNumbers("unmatched_targets")
	if err != nil {
		return MatchResult[S, T]{}, err
	}

	var result MatchResult[S, T]
	type pairKey struct{ source, target int }
	seenPair := make(map[pairKey]bool, len(entries))
	perSource := make(map[int]int, len(entries))
	perTarget := make(map[int]int, len(entries))
	var totalScore float64

	for n, raw := range entries {
		var entry matchEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return refuse(types.KindSchemaViolation, "pair %d is not of the kind that was asked for", n)
		}
		source, sourceOK := wholeNumberOf(entry.SourceIndex)
		target, targetOK := wholeNumberOf(entry.TargetIndex)
		if !sourceOK || !targetOK {
			return refuse(types.KindSchemaViolation, "pair %d does not give both a source index and a target index", n)
		}
		if source < 0 || source >= len(sources) || target < 0 || target >= len(targets) {
			return refuse(types.KindSchemaViolation, "pair %d names a source or target that does not exist", n)
		}
		score, ok := fractionOf(entry.Score)
		if !ok {
			return refuse(types.KindSchemaViolation, "pair %d has no score from 0 to 1", n)
		}
		key := pairKey{source, target}
		if seenPair[key] {
			return refuse(types.KindInvariantViolation, "pair %d repeats an earlier pair", n)
		}
		seenPair[key] = true

		// The score is the model's claim; the threshold is the caller's line,
		// and a caller who sets one is deciding what counts as a match at all.
		if score < opts.Threshold {
			return refuse(types.KindInvariantViolation, "a pair scored %.2f, below the requested threshold of %.2f", score, opts.Threshold)
		}
		if entry.IsPartial && !opts.AllowPartial {
			return refuse(types.KindInvariantViolation, "pair %d is flagged partial and partial matches were not allowed", n)
		}

		perSource[source]++
		perTarget[target]++
		// MaxMatches is stated per source item, so the count is per source and
		// not over the whole list.
		if opts.MaxMatches > 0 && perSource[source] > opts.MaxMatches {
			return refuse(types.KindInvariantViolation, "source %d has more than the requested maximum of %d match(es)", source, opts.MaxMatches)
		}
		if opts.Strategy == "one-to-one" && (perSource[source] > 1 || perTarget[target] > 1) {
			return refuse(types.KindInvariantViolation, "the one-to-one strategy was asked for and source %d or target %d is matched more than once", source, target)
		}

		result.Matches = append(result.Matches, MatchPair[S, T]{
			Source:      sources[source],
			SourceIndex: source,
			Target:      targets[target],
			TargetIndex: target,
			Score:       score,
			Explanation: entry.Explanation,
			FieldScores: entry.FieldScores,
			IsPartial:   entry.IsPartial,
		})
		totalScore += score
	}

	// Bidirectional: the pair must be the best of those returned for its source
	// and for its target. A higher-scored pair for either side means the pair
	// was not the best match in both directions. Only the returned pairs can be
	// compared; a better pair the model left out cannot be seen.
	if opts.Bidirectional {
		bestForSource := make(map[int]float64, len(perSource))
		bestForTarget := make(map[int]float64, len(perTarget))
		for _, pair := range result.Matches {
			if pair.Score > bestForSource[pair.SourceIndex] {
				bestForSource[pair.SourceIndex] = pair.Score
			}
			if pair.Score > bestForTarget[pair.TargetIndex] {
				bestForTarget[pair.TargetIndex] = pair.Score
			}
		}
		for n, pair := range result.Matches {
			if pair.Score < bestForSource[pair.SourceIndex] || pair.Score < bestForTarget[pair.TargetIndex] {
				return refuse(types.KindInvariantViolation, "bidirectional matching was asked for and pair %d is not the best match for its source and its target", n)
			}
		}
	}

	if err := checkUnmatched("sources", len(sources), perSource, unmatchedSources); err != nil {
		return refuse(types.KindInvariantViolation, "%v", err)
	}
	if err := checkUnmatched("targets", len(targets), perTarget, unmatchedTargets); err != nil {
		return refuse(types.KindInvariantViolation, "%v", err)
	}

	result.UnmatchedSources = unmatchedSources
	result.UnmatchedTargets = unmatchedTargets
	result.TotalMatches = len(result.Matches)
	if result.TotalMatches > 0 {
		result.AverageScore = totalScore / float64(result.TotalMatches)
	}
	return result, nil
}

// checkUnmatched checks the model's list of unmatched items against the pairs:
// each index is inside the collection and listed once, none is also in a pair,
// and every item that is in no pair is listed. matched counts the pairs each
// index is in.
func checkUnmatched(kind string, total int, matched map[int]int, listed []int) error {
	seen := make(map[int]bool, len(listed))
	for _, index := range listed {
		switch {
		case index < 0 || index >= total:
			return types.Classified(types.KindMalformedOutput, fmt.Errorf("the unmatched %s name an item that does not exist", kind))
		case seen[index]:
			return types.Classified(types.KindMalformedOutput, fmt.Errorf("the unmatched %s list an item twice", kind))
		case matched[index] > 0:
			return types.Classified(types.KindMalformedOutput, fmt.Errorf("an item is both matched and listed among the unmatched %s", kind))
		}
		seen[index] = true
	}
	for index := 0; index < total; index++ {
		if matched[index] == 0 && !seen[index] {
			return types.Classified(types.KindMalformedOutput, fmt.Errorf("an item of the %s is neither matched nor listed as unmatched", kind))
		}
	}
	return nil
}

// MatchOne finds matches for a single source item against a collection
func MatchOne[S any, T any](source S, targets []T, opts MatchOptions) ([]MatchPair[S, T], error) {
	result, err := SemanticMatch([]S{source}, targets, opts)
	if err != nil {
		return nil, err
	}
	return result.Matches, nil
}
