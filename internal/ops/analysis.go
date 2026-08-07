package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// ClassifyResult contains the results of classification.
// Type parameter C specifies the category type (typically string or a custom enum type).
type ClassifyResult[C any] struct {
	// Category is the primary classification result
	Category C `json:"category"`

	// ModelConfidence score for the classification (0.0-1.0)
	ModelConfidence float64 `json:"confidence"`

	// Alternatives are other possible categories with their confidence scores
	Alternatives []ClassifyAlternative[C] `json:"alternatives,omitempty"`

	// Reasoning explains why this category was chosen
	Reasoning string `json:"reasoning,omitempty"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// ClassifyAlternative represents an alternative classification with confidence
type ClassifyAlternative[C any] struct {
	Category C `json:"category"`
	// ModelConfidence is the model's own claim about this result, not a measurement.
	// It is not calibrated and is not comparable across models or prompts.
	ModelConfidence float64 `json:"confidence"`
}

// Classify categorizes any Go type into typed categories.
//
// Type parameter T specifies the input type.
// Type parameter C specifies the category type (typically string or custom enum).
//
// Examples:
//
//	// Classify text into string categories
//	result, err := Classify[string, string]("This product is amazing!",
//	    NewClassifyOptions().WithCategories([]string{"positive", "negative", "neutral"}))
//	fmt.Printf("Category: %s (%.0f%% confidence)\n", result.Category, result.ModelConfidence*100)
//
//	// Classify a struct into custom categories
//	type Sentiment string
//	const (
//	    SentimentPositive Sentiment = "positive"
//	    SentimentNegative Sentiment = "negative"
//	)
//	result, err := Classify[Review, Sentiment](review,
//	    NewClassifyOptions().WithCategories([]string{"positive", "negative"}))
//
//	// Multi-label classification
//	result, err := Classify[Article, string](article, NewClassifyOptions().
//	    WithCategories([]string{"tech", "business", "sports"}).
//	    WithMultiLabel(true).
//	    WithMaxCategories(2))
func Classify[T any, C any](input T, opts ClassifyOptions) (ClassifyResult[C], error) {
	log := logger.GetLogger()
	log.Debug("Starting classify operation")

	var result ClassifyResult[C]
	result.Metadata = make(map[string]any)

	// Validate options
	if err := opts.Validate(); err != nil {
		return result, fmt.Errorf("invalid options: %w", err)
	}

	categories := opts.Categories
	opt := opts.toOpOptions()

	// Build classification instructions
	var instructions []string
	if opts.MultiLabel {
		instructions = append(instructions, "Allow multiple categories")
		if opts.MaxCategories > 0 {
			instructions = append(instructions, fmt.Sprintf("Return at most %d categories", opts.MaxCategories))
		}
	}

	if opts.MinConfidence > 0 {
		instructions = append(instructions, fmt.Sprintf("Minimum confidence: %.2f", opts.MinConfidence))
	}

	if len(opts.CategoryDescriptions) > 0 {
		for _, category := range sortedKeys(opts.CategoryDescriptions) {
			instructions = append(instructions, fmt.Sprintf("%s: %s", category, opts.CategoryDescriptions[category]))
		}
	}

	if len(opts.CategoryExamples) > 0 {
		for _, category := range sortedKeys(opts.CategoryExamples) {
			examples := opts.CategoryExamples[category]
			if len(examples) == 0 {
				continue
			}
			instructions = append(instructions,
				fmt.Sprintf("Examples of %s: %s", category, strings.Join(examples, "; ")))
		}
	}

	if len(instructions) > 0 {
		steering := strings.Join(instructions, ". ")
		if opts.OpOptions.Steering != "" {
			steering = opts.OpOptions.Steering + ". " + steering
		}
		opt.Steering = steering
	}

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	// Convert input to string representation
	inputStr := formatInput(input)

	categoriesJSON, _ := json.Marshal(categories)

	systemPrompt := fmt.Sprintf(`You are a classification expert. Classify the input into the most appropriate category.

Available Categories: %s

Rules:
- Choose the most appropriate category based on semantic meaning
- Provide a confidence score between 0.0 and 1.0
- Include alternative classifications if relevant (sorted by confidence)
- Provide brief reasoning for the classification

Return a JSON object with these fields:
- "category": the primary category (must be one of the available categories)
- "confidence": number between 0 and 1
- "alternatives": array of {category, confidence} for other relevant categories
- "reasoning": brief explanation of the classification`, string(categoriesJSON))

	userPrompt := fmt.Sprintf("Classify this input:\n%s", inputStr)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Classify operation failed", "error", err)
		return result, types.ClassifyError{
			InputShape: types.DescribeValue(inputStr),
			Categories: categories,
			Reason:     err.Error(),
			Err:        err,
		}
	}

	// Parse the structured response
	var llmResult struct {
		Category        string  `json:"category"`
		ModelConfidence float64 `json:"confidence"`
		Alternatives    []struct {
			Category        string  `json:"category"`
			ModelConfidence float64 `json:"confidence"`
		} `json:"alternatives,omitempty"`
		Reasoning string `json:"reasoning,omitempty"`
	}

	if err := ParseJSONStrict(response, &llmResult); err != nil {
		// No response body in the log. Every operator ships their logs
		// somewhere, and the body is the caller's record classified -- X-03.
		log.Error("Classify failed to parse response", "error", err)
		return result, fmt.Errorf("failed to parse classification response: %w", err)
	}

	// Validate the returned category through the shared invariant. This check
	// was the only one of its kind in the library; it is the template now.
	canonical, err := CategoryIn(categories, llmResult.Category)
	if err != nil {
		log.Error("Classify returned a category outside the allowed set", "error", err)
		return result, types.ClassifyError{
			InputShape:      types.DescribeValue(inputStr),
			Categories:      categories,
			Reason:          err.Error(),
			ModelConfidence: llmResult.ModelConfidence,
			Err:             err,
		}
	}
	llmResult.Category = canonical

	// MinConfidence was interpolated into the prompt and never read back, so a
	// caller who set 0.8 -- or who accepted the non-zero default -- received
	// results the model itself scored at 0.2 and had no way to notice. The
	// number is still a model claim, and enforcing it does not make it a
	// measurement; it makes the option mean what it says.
	if err := AtLeastConfidence(llmResult.ModelConfidence, opts.MinConfidence); err != nil {
		log.Error("Classify result is below the configured confidence floor", "error", err)
		return result, types.ClassifyError{
			InputShape:      types.DescribeValue(inputStr),
			Categories:      categories,
			Reason:          err.Error(),
			ModelConfidence: llmResult.ModelConfidence,
			Err:             err,
		}
	}

	// Convert category string to type C via JSON round-trip
	var category C
	catJSON, _ := json.Marshal(llmResult.Category)
	if err := json.Unmarshal(catJSON, &category); err != nil {
		return result, fmt.Errorf("failed to convert category to type: %w", err)
	}

	result.Category = category
	result.ModelConfidence = llmResult.ModelConfidence
	result.Reasoning = llmResult.Reasoning

	// Convert alternatives
	for _, alt := range llmResult.Alternatives {
		var altCat C
		altJSON, _ := json.Marshal(alt.Category)
		if err := json.Unmarshal(altJSON, &altCat); err == nil {
			result.Alternatives = append(result.Alternatives, ClassifyAlternative[C]{
				Category:        altCat,
				ModelConfidence: alt.ModelConfidence,
			})
		}
	}

	log.Debug("Classify operation completed", "category", llmResult.Category, "confidence", result.ModelConfidence)
	return result, nil
}

// formatInput converts any input to a string representation for the LLM
func formatInput(input any) string {
	if str, ok := input.(string); ok {
		return str
	}
	if bytes, err := json.Marshal(input); err == nil {
		return string(bytes)
	}
	return fmt.Sprintf("%v", input)
}

// ScoreResult contains the results of scoring.
type ScoreResult struct {
	// Value is the overall score
	Value float64 `json:"value"`

	// NormalizedValue is the score normalized to 0.0-1.0 range
	NormalizedValue float64 `json:"normalized_value"`

	// Breakdown contains scores for individual criteria
	Breakdown map[string]float64 `json:"breakdown,omitempty"`

	// Reasoning explains the scoring rationale
	Reasoning string `json:"reasoning,omitempty"`

	// Strengths identified in the input
	Strengths []string `json:"strengths,omitempty"`

	// Weaknesses identified in the input
	Weaknesses []string `json:"weaknesses,omitempty"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Score rates any Go type based on specified criteria.
//
// Type parameter T specifies the input type.
//
// Examples:
//
//	// Basic scoring with typed input
//	result, err := Score[Essay](essay, NewScoreOptions().
//	    WithCriteria([]string{"clarity", "grammar", "relevance"}))
//	fmt.Printf("Score: %.1f/10 (%.0f%% normalized)\n", result.Value, result.NormalizedValue*100)
//
//	// Custom scale and rubric with breakdown
//	result, err := Score[Submission](submission, NewScoreOptions().
//	    WithScaleMin(1).
//	    WithScaleMax(5).
//	    WithRubric(map[string]string{
//	        "quality": "Overall quality of work",
//	        "effort": "Evidence of effort and care",
//	    }))
//	for criterion, score := range result.Breakdown {
//	    fmt.Printf("  %s: %.1f\n", criterion, score)
//	}
func Score[T any](input T, opts ScoreOptions) (ScoreResult, error) {
	log := logger.GetLogger()
	log.Debug("Starting score operation")

	var result ScoreResult
	result.Breakdown = make(map[string]float64)
	result.Metadata = make(map[string]any)

	// Validate options
	if err := opts.Validate(); err != nil {
		return result, fmt.Errorf("invalid options: %w", err)
	}

	opt := opts.toOpOptions()

	// Build scoring instructions
	var instructions []string
	instructions = append(instructions, fmt.Sprintf("Score range: %.1f to %.1f", opts.ScaleMin, opts.ScaleMax))

	if len(opts.Criteria) > 0 {
		instructions = append(instructions, fmt.Sprintf("Criteria: %s", strings.Join(opts.Criteria, ", ")))
	}

	if len(opts.Rubric) > 0 {
		for _, criterion := range sortedKeys(opts.Rubric) {
			instructions = append(instructions, fmt.Sprintf("%s: %s", criterion, opts.Rubric[criterion]))
		}
	}

	if opts.IncludeBreakdown {
		instructions = append(instructions,
			"Report a per-criterion breakdown alongside the overall score, so the number can be traced to its parts")
	}

	if opts.Normalize {
		instructions = append(instructions,
			fmt.Sprintf("Normalise the score to the range %.1f to %.1f, so it is comparable across inputs",
				opts.ScaleMin, opts.ScaleMax))
	}

	if len(instructions) > 0 {
		steering := strings.Join(instructions, ". ")
		if opts.OpOptions.Steering != "" {
			steering = opts.OpOptions.Steering + ". " + steering
		}
		opt.Steering = steering
	}

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	inputStr := formatInput(input)

	// Build criteria list for the prompt
	criteriaList := opts.Criteria
	if len(criteriaList) == 0 && len(opts.Rubric) > 0 {
		for criterion := range opts.Rubric {
			criteriaList = append(criteriaList, criterion)
		}
	}

	criteriaJSON, _ := json.Marshal(criteriaList)

	systemPrompt := fmt.Sprintf(`You are a scoring expert. Evaluate the input and provide a comprehensive assessment.

Score Range: %.1f to %.1f
Criteria: %s

Rules:
- Assign an overall score between %.1f and %.1f
- Provide a breakdown score for each criterion
- Identify strengths and weaknesses
- Explain your reasoning

Return a JSON object with these fields:
- "value": overall score (number between %.1f and %.1f)
- "breakdown": object with criterion names as keys and scores as values
- "reasoning": explanation of the scoring
- "strengths": array of identified strengths
- "weaknesses": array of identified weaknesses`,
		opts.ScaleMin, opts.ScaleMax, string(criteriaJSON),
		opts.ScaleMin, opts.ScaleMax,
		opts.ScaleMin, opts.ScaleMax)

	userPrompt := fmt.Sprintf("Score this input:\n%s", inputStr)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Score operation failed", "error", err)
		return result, types.ScoreError{
			InputShape: types.DescribeValue(input),
			Reason:     err.Error(),
			Err:        err,
		}
	}

	// Parse the structured response
	var llmResult struct {
		Value      float64            `json:"value"`
		Breakdown  map[string]float64 `json:"breakdown,omitempty"`
		Reasoning  string             `json:"reasoning,omitempty"`
		Strengths  []string           `json:"strengths,omitempty"`
		Weaknesses []string           `json:"weaknesses,omitempty"`
	}

	if err := ParseJSONStrict(response, &llmResult); err != nil {
		// Fallback: try to parse as simple number for backward compatibility
		response = strings.Trim(response, "\"'")
		score, parseErr := strconv.ParseFloat(response, 64)
		if parseErr != nil {
			log.Error("Score failed to parse response", "error", err, "response", response)
			return result, fmt.Errorf("failed to parse score response: %w", err)
		}
		llmResult.Value = score
	}

	// Normalize to scale
	score := llmResult.Value
	if score < opts.ScaleMin {
		score = opts.ScaleMin
	} else if score > opts.ScaleMax {
		score = opts.ScaleMax
	}

	result.Value = score
	result.NormalizedValue = (score - opts.ScaleMin) / (opts.ScaleMax - opts.ScaleMin)
	result.Breakdown = llmResult.Breakdown
	result.Reasoning = llmResult.Reasoning
	result.Strengths = llmResult.Strengths
	result.Weaknesses = llmResult.Weaknesses

	log.Debug("Score operation completed", "value", result.Value, "normalized", result.NormalizedValue)
	return result, nil
}

// CompareResult contains the results of comparison.
// Type parameter T specifies the type of items being compared.
type CompareResult[T any] struct {
	// ItemA is the first item that was compared
	ItemA T `json:"item_a"`

	// ItemB is the second item that was compared
	ItemB T `json:"item_b"`

	// SimilarityScore indicates overall similarity (0.0-1.0)
	SimilarityScore float64 `json:"similarity_score"`

	// Similarities are aspects where the items are alike
	Similarities []ComparisonPoint `json:"similarities,omitempty"`

	// Differences are aspects where the items differ
	Differences []ComparisonPoint `json:"differences,omitempty"`

	// Verdict is a brief summary of the comparison
	Verdict string `json:"verdict"`

	// AspectScores shows similarity score per aspect
	AspectScores map[string]float64 `json:"aspect_scores,omitempty"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// ComparisonPoint represents a specific similarity or difference
type ComparisonPoint struct {
	Aspect      string `json:"aspect"`
	Description string `json:"description"`
	Severity    string `json:"severity,omitempty"` // for differences: minor, moderate, major
}

// Compare analyzes similarities and differences between two items of the same type.
//
// Type parameter T specifies the type of items being compared.
//
// Examples:
//
//	// Compare two products
//	result, err := Compare[Product](product1, product2, NewCompareOptions())
//	fmt.Printf("Similarity: %.0f%%\n", result.SimilarityScore*100)
//	fmt.Printf("Verdict: %s\n", result.Verdict)
//
//	// Detailed comparison with specific aspects
//	result, err := Compare[Document](doc1, doc2, NewCompareOptions().
//	    WithComparisonAspects([]string{"content", "style", "accuracy"}).
//	    WithFocusOn("differences"))
//	for _, diff := range result.Differences {
//	    fmt.Printf("- %s: %s\n", diff.Aspect, diff.Description)
//	}
func Compare[T any](itemA, itemB T, opts CompareOptions) (CompareResult[T], error) {
	log := logger.GetLogger()
	log.Debug("Starting compare operation")

	var result CompareResult[T]
	result.ItemA = itemA
	result.ItemB = itemB
	result.AspectScores = make(map[string]float64)
	result.Metadata = make(map[string]any)

	// Validate options
	if err := opts.Validate(); err != nil {
		return result, fmt.Errorf("invalid options: %w", err)
	}

	opt := opts.toOpOptions()

	// Build comparison instructions
	var instructions []string

	if len(opts.ComparisonAspects) > 0 {
		instructions = append(instructions, fmt.Sprintf("Compare these aspects: %s", strings.Join(opts.ComparisonAspects, ", ")))
	}

	if opts.FocusOn != "" {
		instructions = append(instructions, fmt.Sprintf("Focus on: %s", opts.FocusOn))
	}

	instructions = append(instructions, fmt.Sprintf("Depth level: %d/10", opts.Depth))

	if opts.IncludeSimilarity {
		instructions = append(instructions,
			"Report a similarity score alongside the differences, not only what differs")
	}

	if len(instructions) > 0 {
		steering := strings.Join(instructions, ". ")
		if opts.OpOptions.Steering != "" {
			steering = opts.OpOptions.Steering + ". " + steering
		}
		opt.Steering = steering
	}

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	itemAString := formatInput(itemA)
	itemBString := formatInput(itemB)

	aspectsJSON, _ := json.Marshal(opts.ComparisonAspects)

	systemPrompt := fmt.Sprintf(`You are a comparison expert. Analyze and compare two items, identifying similarities and differences.

Comparison Aspects: %s

Rules:
- Calculate an overall similarity score between 0.0 and 1.0
- Identify specific similarities with descriptions
- Identify specific differences with descriptions and severity (minor/moderate/major)
- Provide a brief verdict summarizing the comparison
- If aspects are specified, provide per-aspect similarity scores

Return a JSON object with these fields:
- "similarity_score": number between 0 and 1
- "similarities": array of {aspect, description}
- "differences": array of {aspect, description, severity}
- "verdict": brief summary of the comparison
- "aspect_scores": object with aspect names as keys and similarity scores as values`, string(aspectsJSON))

	userPrompt := fmt.Sprintf("Compare these two items:\n\nItem A:\n%s\n\nItem B:\n%s", itemAString, itemBString)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Compare operation failed", "error", err)
		return result, types.CompareError{
			AShape: types.DescribeValue(itemA),
			BShape: types.DescribeValue(itemB),
			Reason: err.Error(),
			Err:    err,
		}
	}

	// Parse the structured response
	var llmResult struct {
		SimilarityScore float64 `json:"similarity_score"`
		Similarities    []struct {
			Aspect      string `json:"aspect"`
			Description string `json:"description"`
		} `json:"similarities,omitempty"`
		Differences []struct {
			Aspect      string `json:"aspect"`
			Description string `json:"description"`
			Severity    string `json:"severity,omitempty"`
		} `json:"differences,omitempty"`
		Verdict      string             `json:"verdict"`
		AspectScores map[string]float64 `json:"aspect_scores,omitempty"`
	}

	if err := ParseJSONStrict(response, &llmResult); err != nil {
		log.Error("Compare failed to parse response", "error", err, "response", response)
		return result, fmt.Errorf("failed to parse comparison response: %w", err)
	}

	result.SimilarityScore = llmResult.SimilarityScore
	result.Verdict = llmResult.Verdict
	result.AspectScores = llmResult.AspectScores

	// Convert similarities
	for _, sim := range llmResult.Similarities {
		result.Similarities = append(result.Similarities, ComparisonPoint{
			Aspect:      sim.Aspect,
			Description: sim.Description,
		})
	}

	// Convert differences
	for _, diff := range llmResult.Differences {
		result.Differences = append(result.Differences, ComparisonPoint{
			Aspect:      diff.Aspect,
			Description: diff.Description,
			Severity:    diff.Severity,
		})
	}

	log.Debug("Compare operation completed", "similarity", result.SimilarityScore)
	return result, nil
}

// SimilarOptions configures the Similar operation
type SimilarOptions struct {
	types.OpOptions
	SimilarityThreshold float64  // Threshold for similarity (0-1)
	Aspects             []string // Specific aspects to compare
}

// NewSimilarOptions creates SimilarOptions with defaults
func NewSimilarOptions() SimilarOptions {
	return SimilarOptions{
		OpOptions: types.OpOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		SimilarityThreshold: 0.7,
	}
}

// Validate validates SimilarOptions
func (opts SimilarOptions) Validate() error {
	if opts.SimilarityThreshold < 0 || opts.SimilarityThreshold > 1 {
		return fmt.Errorf("similarity threshold must be between 0 and 1, got %f", opts.SimilarityThreshold)
	}
	return nil
}

// WithSimilarityThreshold sets the similarity threshold
func (opts SimilarOptions) WithSimilarityThreshold(threshold float64) SimilarOptions {
	opts.SimilarityThreshold = threshold
	return opts
}

// WithAspects sets specific aspects to compare
func (opts SimilarOptions) WithAspects(aspects []string) SimilarOptions {
	opts.Aspects = aspects
	return opts
}

// SimilarResult contains the results of similarity analysis.
type SimilarResult struct {
	// IsSimilar indicates whether the items meet the similarity threshold
	IsSimilar bool `json:"is_similar"`

	// Score is the overall similarity score (0.0-1.0)
	Score float64 `json:"score"`

	// Threshold is the threshold that was used for the comparison
	Threshold float64 `json:"threshold"`

	// MatchedAspects are aspects where the items are similar
	MatchedAspects []AspectMatch `json:"matched_aspects,omitempty"`

	// DifferingAspects are aspects where the items differ
	DifferingAspects []AspectMatch `json:"differing_aspects,omitempty"`

	// Explanation describes why the items are or aren't similar
	Explanation string `json:"explanation"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// AspectMatch represents a matched or differing aspect with its score
type AspectMatch struct {
	Aspect string  `json:"aspect"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason,omitempty"`
}

// Similar checks semantic similarity between two items of the same type.
//
// Type parameter T specifies the type of items being compared.
//
// Examples:
//
//	// Basic similarity check
//	result, err := Similar[string]("AI is great", "Artificial intelligence is wonderful",
//	    NewSimilarOptions())
//	fmt.Printf("Similar: %v (score: %.0f%%)\n", result.IsSimilar, result.Score*100)
//
//	// Custom threshold and aspects
//	result, err := Similar[Document](doc1, doc2, NewSimilarOptions().
//	    WithSimilarityThreshold(0.9).
//	    WithAspects([]string{"meaning", "sentiment", "tone"}))
//	for _, match := range result.MatchedAspects {
//	    fmt.Printf("  %s: %.0f%% - %s\n", match.Aspect, match.Score*100, match.Reason)
//	}
func Similar[T any](itemA, itemB T, opts SimilarOptions) (SimilarResult, error) {
	log := logger.GetLogger()
	log.Debug("Starting similar operation")

	var result SimilarResult
	result.Threshold = opts.SimilarityThreshold
	result.Metadata = make(map[string]any)

	// Validate options
	if err := opts.Validate(); err != nil {
		return result, fmt.Errorf("invalid options: %w", err)
	}

	opt := opts.OpOptions

	// Build similarity instructions
	var instructions []string
	instructions = append(instructions, fmt.Sprintf("Similarity threshold: %.2f", opts.SimilarityThreshold))

	if len(opts.Aspects) > 0 {
		instructions = append(instructions, fmt.Sprintf("Compare aspects: %s", strings.Join(opts.Aspects, ", ")))
	}

	if len(instructions) > 0 {
		steering := strings.Join(instructions, ". ")
		if opts.OpOptions.Steering != "" {
			steering = opts.OpOptions.Steering + ". " + steering
		}
		opt.Steering = steering
	}

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	itemAString := formatInput(itemA)
	itemBString := formatInput(itemB)

	aspectsJSON, _ := json.Marshal(opts.Aspects)

	systemPrompt := fmt.Sprintf(`You are a similarity analyzer. Determine if two items are semantically similar.

Similarity Threshold: %.2f
Aspects to Compare: %s

Rules:
- Calculate an overall similarity score between 0.0 and 1.0
- Items are "similar" if score >= %.2f
- Identify which aspects match and which differ
- Provide reasoning for each aspect comparison
- Explain the overall similarity assessment

Return a JSON object with these fields:
- "is_similar": boolean (true if score >= threshold)
- "score": overall similarity score (0.0-1.0)
- "matched_aspects": array of {aspect, score, reason} for similar aspects
- "differing_aspects": array of {aspect, score, reason} for different aspects
- "explanation": overall explanation of similarity`,
		opts.SimilarityThreshold, string(aspectsJSON), opts.SimilarityThreshold)

	userPrompt := fmt.Sprintf("Compare these items for similarity:\n\nItem A:\n%s\n\nItem B:\n%s", itemAString, itemBString)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Similar operation failed", "error", err)
		return result, fmt.Errorf("similarity check failed: %w", err)
	}

	// Parse the structured response
	var llmResult struct {
		IsSimilar      bool    `json:"is_similar"`
		Score          float64 `json:"score"`
		MatchedAspects []struct {
			Aspect string  `json:"aspect"`
			Score  float64 `json:"score"`
			Reason string  `json:"reason,omitempty"`
		} `json:"matched_aspects,omitempty"`
		DifferingAspects []struct {
			Aspect string  `json:"aspect"`
			Score  float64 `json:"score"`
			Reason string  `json:"reason,omitempty"`
		} `json:"differing_aspects,omitempty"`
		Explanation string `json:"explanation"`
	}

	if err := ParseJSONStrict(response, &llmResult); err != nil {
		// Fallback: try to parse as simple boolean for backward compatibility
		response = strings.TrimSpace(strings.ToLower(response))
		if response == "true" || response == "false" {
			result.IsSimilar = response == "true"
			if result.IsSimilar {
				result.Score = opts.SimilarityThreshold + 0.1
			} else {
				result.Score = opts.SimilarityThreshold - 0.1
			}
			return result, nil
		}
		log.Error("Similar failed to parse response", "error", err, "response", response)
		return result, fmt.Errorf("failed to parse similarity response: %w", err)
	}

	result.IsSimilar = llmResult.IsSimilar
	result.Score = llmResult.Score
	result.Explanation = llmResult.Explanation

	// Convert matched aspects
	for _, match := range llmResult.MatchedAspects {
		result.MatchedAspects = append(result.MatchedAspects, AspectMatch{
			Aspect: match.Aspect,
			Score:  match.Score,
			Reason: match.Reason,
		})
	}

	// Convert differing aspects
	for _, diff := range llmResult.DifferingAspects {
		result.DifferingAspects = append(result.DifferingAspects, AspectMatch{
			Aspect: diff.Aspect,
			Score:  diff.Score,
			Reason: diff.Reason,
		})
	}

	log.Debug("Similar operation completed", "isSimilar", result.IsSimilar, "score", result.Score)
	return result, nil
}
