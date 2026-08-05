// package ops - Rank operation for ordering items by relevance to a query
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

// RankOptions configures the Rank operation
type RankOptions struct {
	CommonOptions
	types.OpOptions

	// Query to rank items against
	Query string

	// Maximum number of results to return (0 for all)
	TopK int

	// Include relevance scores in result
	IncludeScores bool

	// Minimum relevance score threshold (0.0-1.0)
	MinScore float64

	// Ranking factors to consider
	RankingFactors []string

	// Weight for each ranking factor
	FactorWeights map[string]float64

	// Boost certain item attributes
	BoostFields map[string]float64

	// Penalize certain attributes
	PenalizeFields map[string]float64

	// Include explanation for ranking
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
	if r.Query == "" {
		return fmt.Errorf("query is required for ranking")
	}
	if r.TopK < 0 {
		return fmt.Errorf("topK cannot be negative, got %d", r.TopK)
	}
	if r.MinScore < 0 || r.MinScore > 1 {
		return fmt.Errorf("min score must be between 0 and 1, got %f", r.MinScore)
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
	return r.CommonOptions.toOpOptions()
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

// RankResult contains the results of ranking
type RankResult[T any] struct {
	Items         []RankedItem[T] `json:"items"`
	Query         string          `json:"query"`
	TotalItems    int             `json:"total_items"`
	ReturnedItems int             `json:"returned_items"`
	Metadata      map[string]any  `json:"metadata,omitempty"`
}

// Rank orders items by their relevance to a query using semantic understanding.
// Unlike Sort (which orders by attributes), Rank uses query-based relevance scoring.
//
// Type parameter T specifies the type of items to rank.
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
	log := logger.GetLogger()
	log.Debug("Starting rank operation", "itemCount", len(items), "query", opts.Query)

	var result RankResult[T]
	result.Query = opts.Query
	result.TotalItems = len(items)
	result.Metadata = make(map[string]any)

	if len(items) == 0 {
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
	itemsJSON := make([]string, len(items))
	for i, item := range items {
		itemJSON, err := json.Marshal(item)
		if err != nil {
			log.Error("Rank operation failed: marshal error", "itemIndex", i, "error", err)
			return result, fmt.Errorf("failed to marshal item %d: %w", i, err)
		}
		itemsJSON[i] = fmt.Sprintf("[%d] %s", i, string(itemJSON))
	}

	// Build ranking factors description
	factorsDesc := ""
	if len(opts.RankingFactors) > 0 {
		factorsDesc = fmt.Sprintf("\nRanking factors to consider: %s", strings.Join(opts.RankingFactors, ", "))
		if len(opts.FactorWeights) > 0 {
			weights := make([]string, 0, len(opts.FactorWeights))
			for factor, weight := range opts.FactorWeights {
				weights = append(weights, fmt.Sprintf("%s=%.2f", factor, weight))
			}
			factorsDesc += fmt.Sprintf("\nFactor weights: %s", strings.Join(weights, ", "))
		}
	}

	boostDesc := ""
	if len(opts.BoostFields) > 0 {
		boosts := make([]string, 0, len(opts.BoostFields))
		for field, boost := range opts.BoostFields {
			boosts = append(boosts, fmt.Sprintf("%s(+%.1fx)", field, boost))
		}
		boostDesc = fmt.Sprintf("\nBoost: %s", strings.Join(boosts, ", "))
	}

	penaltyDesc := ""
	if len(opts.PenalizeFields) > 0 {
		penalties := make([]string, 0, len(opts.PenalizeFields))
		for field, penalty := range opts.PenalizeFields {
			penalties = append(penalties, fmt.Sprintf("%s(-%.1fx)", field, penalty))
		}
		penaltyDesc = fmt.Sprintf("\nPenalize: %s", strings.Join(penalties, ", "))
	}

	explanationNote := ""
	if opts.IncludeExplanation {
		explanationNote = "\nInclude a brief explanation for each item's ranking."
	}

	systemPrompt := fmt.Sprintf(`You are an expert at semantic relevance ranking. Rank items by their relevance to the query.

Query: "%s"%s%s%s%s

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

Order the rankings from highest to lowest score.`, opts.Query, factorsDesc, boostDesc, penaltyDesc, explanationNote, opts.MinScore)

	userPrompt := fmt.Sprintf("Rank these items by relevance:\n\n%s", strings.Join(itemsJSON, "\n"))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Rank operation LLM call failed", "error", err)
		return result, fmt.Errorf("ranking failed: %w", err)
	}

	// Parse the response
	var parsed struct {
		Rankings []struct {
			Index        int                `json:"index"`
			Score        float64            `json:"score"`
			Explanation  string             `json:"explanation"`
			FactorScores map[string]float64 `json:"factor_scores"`
		} `json:"rankings"`
	}

	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("Rank operation failed: parse error", "error", err)
		return result, fmt.Errorf("failed to parse ranking result: %w", err)
	}

	// Build ranked items
	rank := 1
	for _, r := range parsed.Rankings {
		// Skip items below minimum score
		if r.Score < opts.MinScore {
			continue
		}

		// Check if we've reached topK
		if opts.TopK > 0 && rank > opts.TopK {
			break
		}

		if r.Index >= 0 && r.Index < len(items) {
			rankedItem := RankedItem[T]{
				Item:         items[r.Index],
				Index:        r.Index,
				Rank:         rank,
				Score:        r.Score,
				Explanation:  r.Explanation,
				FactorScores: r.FactorScores,
			}
			result.Items = append(result.Items, rankedItem)
			rank++
		}
	}

	result.ReturnedItems = len(result.Items)

	log.Debug("Rank operation succeeded", "returnedItems", result.ReturnedItems)
	return result, nil
}
