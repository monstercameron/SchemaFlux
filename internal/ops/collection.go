package ops

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Choose selects the best option from a list with specialized options.
// Uses semantic matching instead of index-based selection for reliability.
//
// Examples:
//
//	// Basic selection
//	best, err := Choose(options, NewChooseOptions().
//	    WithCriteria([]string{"quality", "price"}))
//
//	// Selection with reasoning
//	best, err := Choose(candidates, NewChooseOptions().
//	    WithRequireReasoning(true).
//	    WithTopN(3))
func Choose[T any](options []T, opts ChooseOptions) (T, error) {
	var result T

	// Validate options
	if err := opts.Validate(); err != nil {
		return result, fmt.Errorf("invalid options: %w", err)
	}

	if len(options) == 0 {
		return result, types.ChooseError{
			OptionCount: 0,
			Reason:      "no options provided",
		}
	}

	if len(options) == 1 {
		return options[0], nil
	}

	opOptions := opts.toOpOptions()

	// Build selection instructions
	var instructions []string

	if len(opts.Criteria) > 0 {
		instructions = append(instructions, fmt.Sprintf("Selection criteria: %s", strings.Join(opts.Criteria, ", ")))
	}

	if opts.RequireReasoning {
		instructions = append(instructions, "Provide reasoning for your choice")
	}

	if opts.TopN > 1 {
		instructions = append(instructions, fmt.Sprintf("Return top %d options", opts.TopN))
	}

	if opts.Strategy != "" {
		instructions = append(instructions, fmt.Sprintf("Use %s strategy", opts.Strategy))
	}

	if len(instructions) > 0 {
		steering := strings.Join(instructions, ". ")
		if opts.OpOptions.Steering != "" {
			steering = opts.OpOptions.Steering + ". " + steering
		}
		opOptions.Steering = steering
	}

	ctx, cancel := operationContext(opts.OpOptions.Context, config.GetTimeout())
	defer cancel()

	optionsJSON, err := json.Marshal(options)
	if err != nil {
		return result, types.ChooseError{
			OptionCount: len(options),
			Reason:      fmt.Sprintf("failed to marshal options: %v", err),
		}
	}

	// Use object-based selection instead of index-based
	systemPrompt := `You are a selection expert. Choose the best option from the provided list.

Rules:
- Evaluate all options carefully against the criteria
- Select the single most appropriate option that MATCHES ALL criteria
- Return the COMPLETE selected option as a JSON object
- Return ONLY the JSON object of your choice, nothing else
- Do NOT wrap in markdown code blocks
- Pay close attention to constraints like price limits and requirements`

	// Include steering/criteria in the user prompt for better adherence
	userPrompt := fmt.Sprintf("Choose the best option from this list:\n%s", string(optionsJSON))
	if opOptions.Steering != "" {
		userPrompt = fmt.Sprintf("Selection Requirements: %s\n\nChoose the best option from this list:\n%s", opOptions.Steering, string(optionsJSON))
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opOptions)
	if err != nil {
		return result, types.ChooseError{
			OptionCount: len(options),
			Reason:      err.Error(),
			Err:         err,
		}
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

	// Parse the selected option directly
	if err := ParseJSONStrict(response, &result); err != nil {
		return zeroOf[T](), types.ChooseError{
			OptionCount: len(options),
			Reason:      fmt.Sprintf("failed to parse selected option: %v", err),
			Err:         err,
		}
	}

	// The model returns a copy of the option, not the option. Match it back to
	// the input and return the caller's own value: an echoed record with a
	// subtly altered price or ID is the failure this guards against, and it is
	// far likelier than an invented one.
	chosen, _, err := resolveSelection(result, options)
	if err != nil {
		return zeroOf[T](), types.ChooseError{
			OptionCount: len(options),
			Reason:      err.Error(),
			Err:         err,
		}
	}

	return chosen, nil
}

// zeroOf returns the zero value of T, for the error paths that must not return
// a partially-populated result.
func zeroOf[T any]() T {
	var zero T
	return zero
}

// Filter semantically filters items with specialized options.
// Returns the actual matching objects instead of using index-based selection.
//
// Examples:
//
//	// Basic filtering
//	filtered, err := Filter(items, NewFilterOptions().
//	    WithCriteria("items with positive sentiment"))
//
//	// Complex filtering with confidence
//	filtered, err := Filter(products, NewFilterOptions().
//	    WithCriteria("electronics under $500").
//	    WithMinConfidence(0.8).
//	    WithIncludeReasons(true))
func Filter[T any](items []T, opts FilterOptions) ([]T, error) {
	// Validate options
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("invalid options: %w", err)
	}

	if len(items) == 0 {
		return items, nil
	}

	opOptions := opts.toOpOptions()

	// Build filter instructions
	var instructions []string

	instructions = append(instructions, fmt.Sprintf("Filter criteria: %s", opts.Criteria))

	if opts.KeepMatching {
		instructions = append(instructions, "Keep items that match the criteria")
	} else {
		instructions = append(instructions, "Remove items that match the criteria")
	}

	if opts.MinConfidence > 0 {
		instructions = append(instructions, fmt.Sprintf("Minimum confidence: %.2f", opts.MinConfidence))
	}

	if opts.IncludeReasons {
		instructions = append(instructions, "Include reasons for each decision")
	}

	steering := strings.Join(instructions, ". ")
	if opts.CommonOptions.Steering != "" {
		steering = opts.CommonOptions.Steering + ". " + steering
	}
	opOptions.Steering = steering

	ctx, cancel := operationContext(opts.OpOptions.Context, config.GetTimeout())
	defer cancel()

	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return nil, types.FilterError{
			ItemCount: len(items),
			Reason:    fmt.Sprintf("failed to marshal items: %v", err),
		}
	}

	// Use object-based filtering instead of index-based
	// The rule below used to read "Include items that match the criteria"
	// unconditionally, while the steering said "Remove items that match" when
	// KeepMatching was false. The model received two contradictory orders and
	// the library reported whichever it obeyed as success.
	keepRule := "- Return the items that match the criteria and discard the rest"
	if !opts.KeepMatching {
		keepRule = "- Discard the items that match the criteria and return the rest"
	}

	systemPrompt := `You are a filtering expert. Filter items based on the specified criteria.

Rules:
- Evaluate each item against the criteria
` + keepRule + `
- Return every kept item exactly as it was given, byte for byte; do not edit, reformat, or invent values
- Return a JSON array containing the COMPLETE objects that should be kept
- If the items are primitive values like strings, return a JSON array of those same primitive values
- Never return an object wrapper such as {} or {"item": ...}
- Return ONLY the JSON array of objects, nothing else
- Do NOT wrap in markdown code blocks

Examples:
- Input ["a", "b"] -> Output ["a"]
- Input [{"id":1},{"id":2}] -> Output [{"id":2}]`

	userPrompt := fmt.Sprintf("Filter these items:\n%s", string(itemsJSON))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opOptions)
	if err != nil {
		return nil, types.FilterError{
			ItemCount: len(items),
			Reason:    err.Error(),
			Err:       err,
		}
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

	// Parse the filtered objects directly. There used to be a fallback here
	// that parsed the body as a single item and returned a one-element slice,
	// so a malformed array silently collapsed a filter to one result (C-03).
	var result []T
	if err := ParseJSONStrict(response, &result); err != nil {
		return nil, types.FilterError{
			ItemCount: len(items),
			Reason:    fmt.Sprintf("failed to parse filtered items: %v", err),
			Err:       err,
		}
	}

	// A filter selects; it does not author. Every returned item must have been
	// in the input, no item may repeat, and the result carries the caller's own
	// values in the caller's own order.
	kept, err := resolveSubset(result, items)
	if err != nil {
		return nil, types.FilterError{
			ItemCount: len(items),
			Reason:    err.Error(),
			Err:       err,
		}
	}

	return kept, nil
}

// Sort orders items semantically with specialized options.
// Returns the items in sorted order without using index-based reordering.
//
// Examples:
//
//	// Basic sorting
//	sorted, err := Sort(items, NewSortOptions().
//	    WithCriteria("by importance"))
//
//	// Multi-criteria sorting
//	sorted, err := Sort(products, NewSortOptions().
//	    WithCriteria("by quality").
//	    WithSecondaryCriteria([]string{"by price", "by popularity"}).
//	    WithDirection("descending"))
func Sort[T any](items []T, opts SortOptions) ([]T, error) {
	// Validate options
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("invalid options: %w", err)
	}

	if len(items) <= 1 {
		return items, nil
	}

	opOptions := opts.toOpOptions()

	// Build sort instructions
	var instructions []string

	instructions = append(instructions, fmt.Sprintf("Sort criteria: %s", opts.Criteria))

	if opts.Direction != "" {
		instructions = append(instructions, fmt.Sprintf("Direction: %s", opts.Direction))
	}

	if len(opts.SecondaryCriteria) > 0 {
		instructions = append(instructions, fmt.Sprintf("Secondary criteria: %s", strings.Join(opts.SecondaryCriteria, ", ")))
	}

	if opts.ComparisonLogic != "" {
		instructions = append(instructions, fmt.Sprintf("Comparison logic: %s", opts.ComparisonLogic))
	}

	if opts.Stable {
		instructions = append(instructions, "Maintain relative order of equal elements")
	}

	if opts.IncludeScores {
		instructions = append(instructions, "Include sort scores")
	}

	steering := strings.Join(instructions, ". ")
	if opts.CommonOptions.Steering != "" {
		steering = opts.CommonOptions.Steering + ". " + steering
	}
	opOptions.Steering = steering

	ctx, cancel := operationContext(opts.OpOptions.Context, config.GetTimeout())
	defer cancel()

	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return nil, types.SortError{
			ItemCount: len(items),
			Reason:    fmt.Sprintf("failed to marshal items: %v", err),
		}
	}

	// Use object-based sorting instead of index-based
	systemPrompt := fmt.Sprintf(`You are a sorting expert. Sort items based on the specified criteria.

Rules:
- Evaluate all items according to the sorting criteria
- Arrange them in the proper order
- Return a JSON array containing ALL the COMPLETE objects in sorted order
- If the items are primitive values like strings, return a JSON array of those same primitive values in sorted order
- Never return an object wrapper such as {"item": ...}
- Return ONLY the JSON array of objects, nothing else
- Do NOT wrap in markdown code blocks
- Include every item exactly once in the sorted output
- The output is invalid unless it contains exactly %d items

Examples:
- Input ["low", "critical", "medium"] -> Output ["critical", "medium", "low"]
- Input [{"id":1},{"id":2}] -> Output [{"id":2},{"id":1}]`, len(items))

	userPrompt := fmt.Sprintf("Sort these items:\n%s", string(itemsJSON))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opOptions)
	if err != nil {
		return nil, types.SortError{
			ItemCount: len(items),
			Reason:    err.Error(),
			Err:       err,
		}
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

	// Parse the sorted objects directly
	var result []T
	if err := ParseJSONStrict(response, &result); err != nil {
		fallback, fallbackErr := sortByScoringFallback(items, opts, opOptions)
		if fallbackErr == nil {
			return fallback, nil
		}
		return nil, types.SortError{
			ItemCount: len(items),
			Reason:    fmt.Sprintf("failed to parse sorted items: %v (response: %s)", err, response),
		}
	}

	if len(result) != len(items) {
		fallback, fallbackErr := sortByScoringFallback(items, opts, opOptions)
		if fallbackErr == nil {
			return fallback, nil
		}
		return nil, types.SortError{
			ItemCount: len(items),
			Reason:    fmt.Sprintf("received %d items for %d input items", len(result), len(items)),
		}
	}

	return result, nil
}

func sortByScoringFallback[T any](items []T, opts SortOptions, opOptions types.OpOptions) ([]T, error) {
	ctx, cancel := operationContext(opOptions.Context, config.GetTimeout())
	defer cancel()

	type scoredItem struct {
		Index int
		Item  T
		Score float64
	}

	scored := make([]scoredItem, 0, len(items))
	for i, item := range items {
		itemJSON, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal item %d: %w", i, err)
		}

		systemPrompt := `You are a sorting scorer.

Return a JSON object with:
{
  "rank_score": 0.0-1.0
}

Rules:
- Higher rank_score means the item should appear EARLIER in the final sorted output
- Score according to the requested sort order and criteria
- Consider all criteria together
- Return only valid JSON`

		userPrompt := fmt.Sprintf("Sorting criteria: %s\nDirection: %s\nSecondary criteria: %s\n\nItem:\n%s",
			opts.Criteria,
			opts.Direction,
			strings.Join(opts.SecondaryCriteria, ", "),
			string(itemJSON),
		)

		response, err := callLLM(ctx, systemPrompt, userPrompt, opOptions)
		if err != nil {
			return nil, err
		}

		var parsed struct {
			RankScore float64 `json:"rank_score"`
		}
		if err := ParseJSONStrict(response, &parsed); err != nil {
			return nil, err
		}

		scored = append(scored, scoredItem{
			Index: i,
			Item:  item,
			Score: parsed.RankScore,
		})
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score == scored[j].Score {
			return scored[i].Index < scored[j].Index
		}
		return scored[i].Score > scored[j].Score
	})

	result := make([]T, len(scored))
	for i, item := range scored {
		result[i] = item.Item
	}
	return result, nil
}

func interfaceSlice[T any](items []T) []any {
	result := make([]any, len(items))
	for i, item := range items {
		result[i] = item
	}
	return result
}
