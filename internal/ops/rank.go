// package ops - Rank operation for ordering items by relevance to a query
package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// RankOptions configures the Rank operation
type RankOptions struct {
	CommonOptions
	types.OpOptions

	// Query to rank items against
	Query string

	// Maximum number of results to return (0 for all)
	TopK int

	// IncludeScores is not read: Rank always returns each item's Score.
	IncludeScores bool

	// Minimum relevance score threshold (0.0-1.0)
	MinScore float64

	// Ranking factors to consider. They guide the model and are not checked
	// against its answer, except that FactorScores, when the model returns it,
	// may name only these.
	RankingFactors []string

	// Weight for each ranking factor. Every key has to be one of
	// RankingFactors, or the weight would never reach the model.
	FactorWeights map[string]float64

	// Boost certain item attributes
	BoostFields map[string]float64

	// Penalize certain attributes
	PenalizeFields map[string]float64

	// IncludeExplanation asks for a brief explanation of each item's ranking.
	// An answer that leaves one out is refused.
	IncludeExplanation bool
}

// NewRankOptions creates RankOptions with defaults
func NewRankOptions() RankOptions {
	return RankOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		Query:              "overall relevance",
		TopK:               0, // Return all
		IncludeScores:      true,
		MinScore:           0.0,
		IncludeExplanation: false,
	}
}

// Validate validates RankOptions
func (r RankOptions) Validate() error {
	if err := r.CommonOptions.Validate(); err != nil {
		return err
	}
	if err := checkLockedLimits(r); err != nil {
		return err
	}
	if r.Query == "" {
		return errors.New("query is required for ranking")
	}
	if r.TopK < 0 {
		return fmt.Errorf("topK cannot be negative, got %d", r.TopK)
	}
	if !(r.MinScore >= 0 && r.MinScore <= 1) {
		return fmt.Errorf("min score must be between 0 and 1, got %f", r.MinScore)
	}
	if len(r.FactorWeights) > 0 {
		listed := make(map[string]bool, len(r.RankingFactors))
		for _, factor := range r.RankingFactors {
			listed[factor] = true
		}
		for _, factor := range sortedKeys(r.FactorWeights) {
			if !listed[factor] {
				return fmt.Errorf("factor weight %q is for a factor that is not in RankingFactors", factor)
			}
		}
	}
	numbered := []struct {
		name    string
		numbers map[string]float64
	}{{"factor weight", r.FactorWeights}, {"boost", r.BoostFields}, {"penalty", r.PenalizeFields}}
	for _, group := range numbered {
		for _, key := range sortedKeys(group.numbers) {
			if math.IsNaN(group.numbers[key]) || math.IsInf(group.numbers[key], 0) {
				return fmt.Errorf("%s for %q is not a finite number", group.name, key)
			}
		}
	}
	return nil
}

// WithQuery sets the ranking query
func (r RankOptions) WithQuery(query string) RankOptions {
	r.Query = query
	return r
}

// WithTopK sets the maximum number of results
func (r RankOptions) WithTopK(k int) RankOptions {
	r.TopK = k
	return r
}

// WithIncludeScores enables relevance scores
func (r RankOptions) WithIncludeScores(include bool) RankOptions {
	r.IncludeScores = include
	return r
}

// WithMinScore sets the minimum relevance score
func (r RankOptions) WithMinScore(score float64) RankOptions {
	r.MinScore = score
	return r
}

// WithRankingFactors sets the ranking factors
func (r RankOptions) WithRankingFactors(factors []string) RankOptions {
	r.RankingFactors = factors
	return r
}

// WithFactorWeights sets weights for ranking factors
func (r RankOptions) WithFactorWeights(weights map[string]float64) RankOptions {
	r.FactorWeights = weights
	return r
}

// WithBoostFields sets fields to boost in ranking
func (r RankOptions) WithBoostFields(fields map[string]float64) RankOptions {
	r.BoostFields = fields
	return r
}

// WithPenalizeFields sets fields to penalize in ranking
func (r RankOptions) WithPenalizeFields(fields map[string]float64) RankOptions {
	r.PenalizeFields = fields
	return r
}

// WithIncludeExplanation enables ranking explanations
func (r RankOptions) WithIncludeExplanation(include bool) RankOptions {
	r.IncludeExplanation = include
	return r
}

// WithSteering sets the steering prompt
func (r RankOptions) WithSteering(steering string) RankOptions {
	r.CommonOptions = r.CommonOptions.WithSteering(steering)
	return r
}

// WithMode sets the mode
func (r RankOptions) WithMode(mode types.Mode) RankOptions {
	r.CommonOptions = r.CommonOptions.WithMode(mode)
	return r
}

// WithIntelligence sets the intelligence level
func (r RankOptions) WithIntelligence(intelligence types.Speed) RankOptions {
	r.CommonOptions = r.CommonOptions.WithIntelligence(intelligence)
	return r
}

func (r RankOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(r.CommonOptions, r.OpOptions)
}

func (r RankOptions) optionHalves() (CommonOptions, types.OpOptions) {
	return r.CommonOptions, r.OpOptions
}

// RankedItem represents an item with its ranking information
type RankedItem[T any] struct {
	Item         T                  `json:"item"`
	Index        int                `json:"index"`
	Rank         int                `json:"rank"`
	Score        float64            `json:"score"`
	Explanation  string             `json:"explanation,omitempty"`
	FactorScores map[string]float64 `json:"factor_scores,omitempty"`
}

// RankResult contains the results of ranking. Items are in rank order: Rank
// sorts them by the model's scores itself, so the order is the one the scores
// imply whatever order the model listed them in.
type RankResult[T any] struct {
	Items         []RankedItem[T] `json:"items"`
	Query         string          `json:"query"`
	TotalItems    int             `json:"total_items"`
	ReturnedItems int             `json:"returned_items"`
	// Metadata is not populated.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Rank orders items by their relevance to a query using semantic understanding.
// Unlike Sort (which orders by attributes), Rank uses query-based relevance scoring.
//
// Type parameter T specifies the type of items to rank.
//
// The model scores every item, and Go checks the answer before it is used: each
// item has to be scored exactly once (no repeats, none missing, none that was
// not offered) and every score has to be a number from 0 to 1. An answer that
// breaks one is refused. The order of the result is then worked out here, by
// score and not by the order the model listed the items in; items with the same
// score keep the order the model gave them. MinScore and TopK are applied
// after that, so Rank 1 is the highest score that was kept.
//
// Examples:
//
//	// Basic relevance ranking
//	result, err := Rank(documents, NewRankOptions().
//	    WithQuery("machine learning tutorials").
//	    WithTopK(10))
//
//	// Ranking with custom factors
//	result, err := Rank(products, NewRankOptions().
//	    WithQuery("affordable laptop for students").
//	    WithRankingFactors([]string{"price", "features", "reviews"}).
//	    WithFactorWeights(map[string]float64{"price": 0.4, "features": 0.3, "reviews": 0.3}))
//
//	// Ranking with boosting
//	result, err := Rank(articles, NewRankOptions().
//	    WithQuery("climate change").
//	    WithBoostFields(map[string]float64{"recent": 1.5}).
//	    WithIncludeExplanation(true))
func Rank[T any](items []T, opts RankOptions) (RankResult[T], error) {
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	// The query is the caller's text and can hold user data, so only its size is
	// logged.
	log.Debug("Starting rank operation", "itemCount", len(items), "queryLength", len(opts.Query))

	// Options are checked before the items are looked at: an empty list must not
	// hide a configuration that the first real call would refuse.
	if err := opts.Validate(); err != nil {
		return RankResult[T]{}, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	result := RankResult[T]{Query: opts.Query, TotalItems: len(items)}
	if len(items) == 0 {
		return result, nil
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
	itemsJSON := make([]string, len(items))
	for i, item := range items {
		itemJSON, err := json.Marshal(item)
		if err != nil {
			log.Error("Rank operation failed: marshal error", "itemIndex", i, "error", loggedError(err))
			return RankResult[T]{}, types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal item %d: %w", i, err))
		}
		itemsJSON[i] = fmt.Sprintf("[%d] %s", i, string(itemJSON))
	}

	// Build ranking factors description. Every name and the query are the
	// caller's text, so each is written as a JSON string, and the maps are read
	// in key order so the same call renders the same bytes every time.
	factorsDesc := ""
	if len(opts.RankingFactors) > 0 {
		quotedFactors := make([]string, len(opts.RankingFactors))
		for i, factor := range opts.RankingFactors {
			quotedFactors[i] = parsePromptQuote(factor)
		}
		factorsDesc = fmt.Sprintf("\nRanking factors to consider: %s", strings.Join(quotedFactors, ", "))
		if len(opts.FactorWeights) > 0 {
			weights := make([]string, 0, len(opts.FactorWeights))
			for _, factor := range sortedKeys(opts.FactorWeights) {
				weights = append(weights, fmt.Sprintf("%s=%.2f", parsePromptQuote(factor), opts.FactorWeights[factor]))
			}
			factorsDesc += fmt.Sprintf("\nFactor weights: %s", strings.Join(weights, ", "))
		}
	}

	boostDesc := ""
	if len(opts.BoostFields) > 0 {
		boosts := make([]string, 0, len(opts.BoostFields))
		for _, field := range sortedKeys(opts.BoostFields) {
			boosts = append(boosts, fmt.Sprintf("%s(+%.1fx)", parsePromptQuote(field), opts.BoostFields[field]))
		}
		boostDesc = fmt.Sprintf("\nBoost: %s", strings.Join(boosts, ", "))
	}

	penaltyDesc := ""
	if len(opts.PenalizeFields) > 0 {
		penalties := make([]string, 0, len(opts.PenalizeFields))
		for _, field := range sortedKeys(opts.PenalizeFields) {
			penalties = append(penalties, fmt.Sprintf("%s(-%.1fx)", parsePromptQuote(field), opts.PenalizeFields[field]))
		}
		penaltyDesc = fmt.Sprintf("\nPenalize: %s", strings.Join(penalties, ", "))
	}

	explanationNote := ""
	if opts.IncludeExplanation {
		explanationNote = "\nInclude a brief explanation for each item's ranking."
	}

	systemPrompt := fmt.Sprintf(`You are an expert at semantic relevance ranking. Rank items by their relevance to the query.

Query: %s%s%s%s%s

Score each item from 0.0 to 1.0 based on relevance.
Minimum score threshold: %.2f

Return a JSON object with:
{
  "rankings": [
    {
      "index": 0,
      "score": 0.95,
      "explanation": "Most relevant because..."
    }
  ]
}

Order the rankings from highest to lowest score.`, parsePromptQuote(opts.Query), factorsDesc, boostDesc, penaltyDesc, explanationNote, opts.MinScore)

	userPrompt := fmt.Sprintf("Rank these items by relevance:\n\n%s", strings.Join(itemsJSON, "\n"))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Rank operation LLM call failed", "error", loggedError(err))
		return RankResult[T]{}, fmt.Errorf("ranking failed: %w", err)
	}

	// Parse the response. The index and the score are required of every ranking,
	// and a null for either is refused, so none is read as item 0 or a score of 0.
	// The explanation is asked for only when IncludeExplanation is set, and the
	// check below refuses a blank one then. FactorScores are not in the format the
	// prompt shows, so the model may leave them out.
	type rankingEntry struct {
		Index        int                `json:"index"`
		Score        float64            `json:"score"`
		Explanation  string             `json:"explanation,omitempty"`
		FactorScores map[string]float64 `json:"factor_scores,omitempty"`
	}
	var parsed struct {
		Rankings []rankingEntry `json:"rankings"`
	}

	if err := decodeAnswer("rank", response, &parsed); err != nil {
		log.Error("Rank operation failed: parse error", "error", loggedError(err))
		return RankResult[T]{}, err
	}
	// A null list is an answer the decoder allows for a list. It is not a ranking,
	// and an empty list is told from it by the kind of the refusal below.
	if parsed.Rankings == nil {
		return RankResult[T]{}, types.Newf(types.KindSchemaViolation, "rank", "rankings is null, and null is not an answer for it")
	}
	entries := parsed.Rankings

	// Every item exactly once, each score on the scale it was asked for. The
	// indexes are the answer's own, so a position is reported and never a value.
	indexes := make([]int, len(entries))
	for position, entry := range entries {
		indexes[position] = entry.Index
		if !(entry.Score >= 0 && entry.Score <= 1) {
			return RankResult[T]{}, types.Newf(types.KindSchemaViolation, "rank", "the score of ranking %d is outside 0 to 1", position+1)
		}
	}
	if err := CoversExactlyOnce(len(items), [][]int{indexes}); err != nil {
		log.Error("Rank operation failed: the answer does not rank every item once")
		return RankResult[T]{}, types.Newf(types.KindBatchProtocolViolation, "rank", "the answer does not rank every item exactly once (%d rankings for %d items)", len(entries), len(items))
	}

	// FactorScores may name only the factors that were asked about.
	if len(opts.RankingFactors) > 0 {
		declared := make(map[string]bool, len(opts.RankingFactors))
		for _, factor := range opts.RankingFactors {
			declared[strings.ToLower(strings.TrimSpace(factor))] = true
		}
		for position, entry := range entries {
			for factor := range entry.FactorScores {
				if !declared[strings.ToLower(strings.TrimSpace(factor))] {
					return RankResult[T]{}, types.Newf(types.KindSchemaViolation, "rank", "ranking %d scores a factor that was not asked about", position+1)
				}
			}
		}
	}

	// An explanation that was asked for has to be there.
	if opts.IncludeExplanation {
		for position, entry := range entries {
			if strings.TrimSpace(entry.Explanation) == "" {
				return RankResult[T]{}, types.Newf(types.KindSchemaViolation, "rank", "ranking %d has no explanation, and one was asked for", position+1)
			}
		}
	}

	// The order the scores imply, not the order the model listed them in. The
	// sort is stable, so items with the same score keep the model's order.
	order := make([]int, len(entries))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return entries[order[a]].Score > entries[order[b]].Score
	})

	rank := 1
	for _, position := range order {
		entry := entries[position]

		// Skip items below minimum score
		if entry.Score < opts.MinScore {
			continue
		}

		// Check if we've reached topK
		if opts.TopK > 0 && rank > opts.TopK {
			break
		}

		result.Items = append(result.Items, RankedItem[T]{
			Item:         items[entry.Index],
			Index:        entry.Index,
			Rank:         rank,
			Score:        entry.Score,
			Explanation:  entry.Explanation,
			FactorScores: entry.FactorScores,
		})
		rank++
	}

	result.ReturnedItems = len(result.Items)

	log.Debug("Rank operation succeeded", "returnedItems", result.ReturnedItems)
	return result, nil
}
