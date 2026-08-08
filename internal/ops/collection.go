package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/types"
)

// OP-101: stable, invocation-local IDs instead of an echoed object or a bare
// index.
//
// Choose, Filter, and Sort used to send the model the raw items and ask for
// the chosen/kept/sorted items back, matched to the caller's own values by
// equality (collection_identity.go). Two failures came from that: a model
// that echoed an item with one field subtly changed produced a record that
// looked authoritative and was not the caller's, and the model had to spend
// output tokens reproducing data it already had, which is what capped every
// operation's size (C-06).
//
// A bare positional index was tried before object echoing and abandoned for
// a real reason: an index is only meaningful if the model returns every
// element in the order it received them, and the failure being guarded
// against -- an omitted or reordered item -- is exactly the case where it
// does not. It is also easy for a model to confuse with a data field, or to
// do arithmetic on.
//
// The fix here is neither: assign a stable, opaque ID ("i-000001") to each
// item before it is sent, ask the model to answer using IDs only, and
// validate exact coverage in Go before reconstructing the caller's order.
// Two items with identical content still get distinct IDs, so a filter or a
// choice is unambiguous regardless of duplicate input values, and the
// output the model has to produce is a handful of bytes per item rather than
// the item itself -- output length stops scaling with item size.
//
// Coverage is checked with the same invariant library OP-102/103/105
// attached (SubsetOf, SameMultiset), run over the ID slices instead of the
// value slices: a subset of IDs is exactly the contract Filter has, and a
// permutation of IDs is exactly the contract Sort has, so the existing
// checks apply unchanged.

// itemID renders a zero-based position as a stable, invocation-local
// identifier. It is deliberately not the model's problem to compute: the
// model only ever has to copy a token it was given back verbatim.
func itemID(position int) string {
	return fmt.Sprintf("i-%06d", position+1)
}

// taggedItem pairs a caller's item with its invocation-local ID for encoding
// into the prompt. The model is asked to reason about Item and answer with
// ID; it is never asked to reproduce Item.
type taggedItem[T any] struct {
	ID   string `json:"id"`
	Item T      `json:"item"`
}

// tagItems assigns a stable ID to each item, in input order.
func tagItems[T any](items []T) []taggedItem[T] {
	tagged := make([]taggedItem[T], len(items))
	for i, item := range items {
		tagged[i] = taggedItem[T]{ID: itemID(i), Item: item}
	}
	return tagged
}

// itemIDs returns the IDs assigned to n items, in input order -- the set an
// invariant check compares the model's answer against.
func itemIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = itemID(i)
	}
	return ids
}

// idPositions maps each assigned ID back to its position in the input, so a
// validated answer can be turned into the caller's own values without a
// second linear scan per ID.
func idPositions(ids []string) map[string]int {
	positions := make(map[string]int, len(ids))
	for i, id := range ids {
		positions[id] = i
	}
	return positions
}

// resolveSubsetByIDs validates that ids is a subset of the IDs assigned to
// items -- SubsetOf run over IDs rather than values, which is what makes the
// check work regardless of duplicate item content -- and returns the
// caller's own items, in input order.
func resolveSubsetByIDs[T any](items []T, ids []string) ([]T, error) {
	offered := itemIDs(len(items))
	if err := SubsetOf(offered, ids); err != nil {
		return nil, err
	}

	positions := idPositions(offered)
	keep := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		keep[positions[id]] = struct{}{}
	}

	result := make([]T, 0, len(keep))
	for i, item := range items {
		if _, ok := keep[i]; ok {
			result = append(result, item)
		}
	}
	return result, nil
}

// resolvePermutationByIDs validates that ids is a permutation of the IDs
// assigned to items -- SameMultiset run over IDs, which is the exact-coverage
// check a sort needs: every ID present exactly once -- and returns the
// caller's own items in the order the IDs were given, which is the sorted
// order the model is answering with.
func resolvePermutationByIDs[T any](items []T, ids []string) ([]T, error) {
	offered := itemIDs(len(items))
	if err := SameMultiset(offered, ids); err != nil {
		return nil, err
	}

	positions := idPositions(offered)
	result := make([]T, len(ids))
	for i, id := range ids {
		result[i] = items[positions[id]]
	}
	return result, nil
}

// idListResponse is what Filter and Sort ask the model to answer with: the
// ids of the relevant items, and nothing else. Wrapping the array in an
// object rather than returning it bare gives ParseJSONStrict a field name to
// require, so an empty object or an answer about something else is caught
// before the ID coverage check ever runs.
type idListResponse struct {
	IDs []string `json:"ids"`
}

// idResponse is what Choose asks the model to answer with: the id of the
// chosen option.
type idResponse struct {
	ID string `json:"id"`
}

// zeroOf returns the zero value of T, for the error paths that must not return
// a partially-populated result.
func zeroOf[T any]() T {
	var zero T
	return zero
}

// Choose selects the best option from a list with specialized options.
//
// The model answers with the ID of its choice, not a copy of the option: an
// echoed record with an altered field has nothing to alter, because the
// model never sees the option's fields in the answer channel, only in the
// question.
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

	if opts.Strategy != "" {
		instructions = append(instructions, fmt.Sprintf("Use %s strategy", opts.Strategy))
	}

	if len(instructions) > 0 {
		steering := strings.Join(instructions, ". ")
		if effectiveSteering(opts.CommonOptions, opts.OpOptions) != "" {
			steering = effectiveSteering(opts.CommonOptions, opts.OpOptions) + ". " + steering
		}
		opOptions.Steering = steering
	}

	ctx, cancel := operationContext(resolvedContext(opts.CommonOptions, opts.OpOptions), config.GetTimeout())
	defer cancel()

	tagged := tagItems(options)
	optionsJSON, err := json.Marshal(tagged)
	if err != nil {
		return result, types.ChooseError{
			OptionCount: len(options),
			Reason:      fmt.Sprintf("failed to marshal options: %v", err),
		}
	}

	systemPrompt := `You are a selection expert. Choose the best option from the provided list.

Each option is given as {"id": "...", "item": ...}. The id is what you answer with.

Rules:
- Evaluate all options carefully against the criteria
- Select the single most appropriate option that MATCHES ALL criteria
- Return ONLY a JSON object {"id": "..."} naming the id of your choice
- Do NOT return the option itself, invent an id, or wrap in markdown code blocks
- Pay close attention to constraints like price limits and requirements

Example:
- Input [{"id":"i-000001","item":{"name":"A"}},{"id":"i-000002","item":{"name":"B"}}] -> Output {"id":"i-000002"}`

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

	var parsed idResponse
	if err := ParseJSONStrict(response, &parsed); err != nil {
		return zeroOf[T](), types.ChooseError{
			OptionCount: len(options),
			Reason:      fmt.Sprintf("failed to parse the selected id: %v", err),
			Err:         err,
		}
	}

	// The id must be one the caller's own items were tagged with. MemberOf is
	// the same check OP-102 attached, run over ids instead of values: a
	// selection that names something never offered -- including an item's own
	// domain id, which is a plausible-looking mistake for a model that
	// forgets the protocol -- is refused rather than guessed at.
	offered := itemIDs(len(options))
	if err := MemberOf(offered, parsed.ID); err != nil {
		return zeroOf[T](), types.ChooseError{
			OptionCount: len(options),
			Reason:      err.Error(),
			Err:         err,
		}
	}

	return options[idPositions(offered)[parsed.ID]], nil
}

// Filter semantically filters items with specialized options.
//
// The model answers with the ids of the items to keep, not the items
// themselves, so there is nothing for it to invent, edit, or duplicate a
// value into: SubsetOf runs over the id list, and a caller's own items come
// back for whichever ids were valid and offered.
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
//
// Filter selects the items matching the criteria.
//
// A collection too large for its id-only answer to fit the output budget is
// split into chunks and filtered a chunk at a time, then concatenated. That
// is safe for a filter because the merge is a concatenation: each item's
// fate depends only on the criteria, not on the other items. It is
// deliberately NOT done for Sort, whose merge needs to know how to interleave
// two sorted runs -- see MapReduce if you want to build that.
//
// The chunks run with bounded concurrency, and a failing chunk fails the call:
// a filter that silently returned the items from the chunks that happened to
// succeed would be the fail-open behaviour this library has spent its life
// removing.
func Filter[T any](items []T, opts FilterOptions) ([]T, error) {
	// Validate options
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("invalid options: %w", err)
	}

	if len(items) == 0 {
		return items, nil
	}

	opOptions := opts.toOpOptions()

	// Split rather than refuse when the whole collection's id answer cannot
	// fit the output budget. With an id-only answer this triggers on item
	// count far more rarely than the old echo-everything protocol did --
	// which is the point of OP-101 -- but a collection can still be large
	// enough that even one id per item does not fit.
	if fits := filterChunkSize(len(items), opOptions.Intelligence); fits > 0 && fits < len(items) {
		ctx, cancel := operationContext(resolvedContext(opts.CommonOptions, opts.OpOptions), config.GetTimeout())
		defer cancel()

		kept, _, err := MapReduceFlat(ctx, items,
			MapReduceOptions{ChunkSize: fits},
			func(ctx context.Context, chunk []T) ([]T, error) {
				chunkOpts := opts
				chunkOpts.OpOptions.Context = ctx
				return filterOneChunk(chunk, chunkOpts)
			})
		if err != nil {
			return nil, types.FilterError{ItemCount: len(items), Reason: err.Error(), Err: err}
		}
		return kept, nil
	}

	return filterOneChunk(items, opts)
}

// filterChunkSize returns the largest number of items whose id-only answer
// fits the output budget, or 0 when the whole collection already fits.
//
// The old version divided the *input's* marshalled size by item count to
// estimate the answer's size, because the answer used to echo every kept
// item back -- output scaled with item size. It no longer does: an id costs
// the same handful of bytes regardless of what the item behind it looks
// like, so the boundary here is about item *count* against a fixed per-id
// cost, not about how large any one item is.
func filterChunkSize(itemCount int, intelligence types.Speed) int {
	if itemCount == 0 {
		return 0
	}

	idsPayload, err := json.Marshal(itemIDs(itemCount))
	if err != nil {
		return 0
	}
	if checkEchoBudget("Filter", string(idsPayload), itemCount, intelligence, 1.0) == nil {
		return 0
	}

	maxTokens := config.GetMaxTokens(intelligence)
	perItem := estimateTokens(string(idsPayload)) / itemCount
	if perItem <= 0 {
		return 0
	}

	// Two thirds of the budget, so a chunk that runs a little long still fits.
	fits := (maxTokens * 2 / 3) / perItem
	if fits < 1 {
		fits = 1
	}
	if fits >= itemCount {
		return 0
	}
	return fits
}

func filterOneChunk[T any](items []T, opts FilterOptions) ([]T, error) {
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

	ctx, cancel := operationContext(resolvedContext(opts.CommonOptions, opts.OpOptions), config.GetTimeout())
	defer cancel()

	tagged := tagItems(items)
	itemsJSON, err := json.Marshal(tagged)
	if err != nil {
		return nil, types.FilterError{
			ItemCount: len(items),
			Reason:    fmt.Sprintf("failed to marshal items: %v", err),
		}
	}

	// Filter's worst case is keeping everything, so its answer's ceiling is
	// one id per item -- see filterChunkSize's comment for why that no longer
	// scales with item size.
	idsPayload, err := json.Marshal(itemIDs(len(items)))
	if err != nil {
		return nil, types.FilterError{ItemCount: len(items), Reason: fmt.Sprintf("failed to estimate output size: %v", err)}
	}
	if err := checkEchoBudget("Filter", string(idsPayload), len(items), opOptions.Intelligence, 1.0); err != nil {
		return nil, types.FilterError{ItemCount: len(items), Reason: err.Error(), Err: err}
	}

	// The rule below used to read "Include items that match the criteria"
	// unconditionally, while the steering said "Remove items that match" when
	// KeepMatching was false. The model received two contradictory orders and
	// the library reported whichever it obeyed as success.
	keepRule := "- Return the ids of the items that match the criteria and discard the rest"
	if !opts.KeepMatching {
		keepRule = "- Discard the ids of the items that match the criteria and return the ids of the rest"
	}

	systemPrompt := `You are a filtering expert. Filter items based on the specified criteria.

Each item is given as {"id": "...", "item": ...}. Ids are what you answer with.

Rules:
- Evaluate each item against the criteria
` + keepRule + `
- Return a JSON object {"ids": [...]} listing the ids of the kept items, and nothing else
- Do NOT return the items themselves, invent an id that was not offered, or list the same id twice
- Do NOT wrap in markdown code blocks

Examples:
- Input [{"id":"i-000001","item":"a"},{"id":"i-000002","item":"b"}] -> Output {"ids":["i-000001"]}
- Input [{"id":"i-000001","item":{"id":1}},{"id":"i-000002","item":{"id":2}}] -> Output {"ids":["i-000002"]}`

	userPrompt := fmt.Sprintf("Filter these items:\n%s", string(itemsJSON))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opOptions)
	if err != nil {
		return nil, types.FilterError{
			ItemCount: len(items),
			Reason:    err.Error(),
			Err:       err,
		}
	}

	// Parse the kept ids. There used to be a fallback here that parsed the
	// body as a single item and returned a one-element slice, so a malformed
	// array silently collapsed a filter to one result (C-03); an id list has
	// no equivalent single-value shape to fall back into.
	var parsed idListResponse
	if err := ParseJSONStrict(response, &parsed); err != nil {
		return nil, types.FilterError{
			ItemCount: len(items),
			Reason:    fmt.Sprintf("failed to parse filtered ids: %v", err),
			Err:       err,
		}
	}

	// A filter selects; it does not author. Every id must have been offered,
	// none may repeat, and the result carries the caller's own values in the
	// caller's own order.
	kept, err := resolveSubsetByIDs(items, parsed.IDs)
	if err != nil {
		return nil, types.FilterError{
			ItemCount: len(items),
			Reason:    err.Error(),
			Err:       err,
		}
	}

	return kept, nil
}

// sortScoringThreshold is the item count above which Sort goes straight to
// independent per-item scoring (OP-106) instead of asking for a whole-list
// answer.
//
// A whole-list answer -- even an id-only one -- is a single call that has to
// hold every item's context in one request and name every item exactly once
// in the reply; a model is more likely to drop, duplicate, or reorder an id
// as the list grows; and it is one API round trip, so nothing else can start
// until the whole answer comes back. Per-item scoring is one small,
// independent call per item, runs with bounded concurrency, and its answer
// cannot be malformed in a way that loses or duplicates an item -- the
// caller's own objects are sorted in Go. Below the threshold, Sort still
// prefers the whole-list answer -- it is one call instead of N -- and only
// falls back to scoring when that answer is rejected.
const sortScoringThreshold = 50

// Sort orders items semantically with specialized options.
//
// Above sortScoringThreshold, or when the whole-list answer is rejected,
// Sort scores items independently and sorts in Go (OP-106): see
// sortByScoringFallback for why that strategy cannot lose, duplicate, or
// edit an item.
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
	result, _, err := sortWithStrategy(items, opts)
	return result, err
}

// SortResult runs Sort and returns the record of how it ran, including which
// strategy produced the answer (OP-106): "whole-list" for the single-call id
// protocol, "scoring" when the item count put it straight on the per-item
// path, and "scoring-fallback" when a whole-list answer was attempted and
// rejected. It is the same execution Sort runs -- see extractresult.go for
// why this library attaches the envelope this way rather than giving every
// operation a second, divergent code path.
func SortResult[T any](items []T, opts SortOptions) (types.Result[[]T], error) {
	ctx, records := withCallRecording(resolvedContext(opts.CommonOptions, opts.OpOptions))
	opts.OpOptions.Context = ctx

	value, strategy, err := sortWithStrategy(items, opts)

	meta := envelopeFrom(records.collect(), "sort")
	meta.Strategy = strategy

	if err != nil {
		return types.Result[[]T]{Meta: meta}, err
	}
	return types.Result[[]T]{Value: value, Meta: meta}, nil
}

func sortWithStrategy[T any](items []T, opts SortOptions) ([]T, string, error) {
	// Validate options
	if err := opts.Validate(); err != nil {
		return nil, "", fmt.Errorf("invalid options: %w", err)
	}

	if len(items) <= 1 {
		return items, "trivial", nil
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

	ctx, cancel := operationContext(resolvedContext(opts.CommonOptions, opts.OpOptions), config.GetTimeout())
	defer cancel()

	// OP-106: above the threshold, per-item scoring is the primary strategy,
	// not a fallback tried after a whole-list answer fails.
	if len(items) > sortScoringThreshold {
		result, err := sortByScoringFallback(ctx, items, opts, opOptions)
		if err != nil {
			return nil, "scoring", types.SortError{ItemCount: len(items), Reason: err.Error(), Err: err}
		}
		return result, "scoring", nil
	}

	tagged := tagItems(items)
	itemsJSON, err := json.Marshal(tagged)
	if err != nil {
		return nil, "", types.SortError{
			ItemCount: len(items),
			Reason:    fmt.Sprintf("failed to marshal items: %v", err),
		}
	}

	// A sort's answer names every item once, so its ceiling is one id per
	// item -- far smaller than echoing every item back, and independent of
	// item size.
	idsPayload, err := json.Marshal(itemIDs(len(items)))
	if err != nil {
		return nil, "", types.SortError{ItemCount: len(items), Reason: fmt.Sprintf("failed to estimate output size: %v", err)}
	}
	if err := checkEchoBudget("Sort", string(idsPayload), len(items), opOptions.Intelligence, 1.0); err != nil {
		return nil, "", types.SortError{ItemCount: len(items), Reason: err.Error(), Err: err}
	}

	systemPrompt := fmt.Sprintf(`You are a sorting expert. Sort items based on the specified criteria.

Each item is given as {"id": "...", "item": ...}. Ids are what you answer with.

Rules:
- Evaluate all items according to the sorting criteria
- Return a JSON object {"ids": [...]} listing every id, in sorted order, and nothing else
- The output is invalid unless "ids" contains exactly %d ids, each appearing exactly once
- Do NOT return the items themselves, invent an id, or wrap in markdown code blocks

Example:
- Input [{"id":"i-000001","item":"low"},{"id":"i-000002","item":"critical"},{"id":"i-000003","item":"medium"}] -> Output {"ids":["i-000002","i-000003","i-000001"]}`, len(items))

	userPrompt := fmt.Sprintf("Sort these items:\n%s", string(itemsJSON))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opOptions)
	if err != nil {
		return nil, "", types.SortError{
			ItemCount: len(items),
			Reason:    err.Error(),
			Err:       err,
		}
	}

	// Parse the sorted ids.
	var parsed idListResponse
	if err := ParseJSONStrict(response, &parsed); err != nil {
		fallback, fallbackErr := sortByScoringFallback(ctx, items, opts, opOptions)
		if fallbackErr == nil {
			return fallback, "scoring-fallback", nil
		}
		// No response body in the Reason. Every caller logs this error and the
		// body is their data reshaped -- the same X-03 that OP-110 removed from
		// the two Filter errors, surviving here.
		return nil, "whole-list", types.SortError{
			ItemCount: len(items),
			Reason:    fmt.Sprintf("failed to parse sorted ids: %v", err),
			Err:       err,
		}
	}

	// A sort's answer is a permutation of the offered ids, checked with the
	// same SameMultiset OP-105 attached, run over ids instead of values: a
	// model that answered with one id twice and dropped another is caught
	// here exactly as it would have been over the echoed items.
	result, err := resolvePermutationByIDs(items, parsed.IDs)
	if err != nil {
		fallback, fallbackErr := sortByScoringFallback(ctx, items, opts, opOptions)
		if fallbackErr == nil {
			return fallback, "scoring-fallback", nil
		}
		return nil, "whole-list", types.SortError{
			ItemCount: len(items),
			Reason:    err.Error(),
			Err:       err,
		}
	}

	return result, "whole-list", nil
}

// sortByScoringFallback scores each item independently and sorts by score in
// Go. Because the model is only ever shown one item at a time and never asked
// to reproduce the list, items cannot be lost, duplicated, or edited, and
// Stable is honoured exactly because Go's sort is doing it rather than the
// model claiming to. OP-106 promotes this from a fallback to the primary
// strategy above sortScoringThreshold; below it, Sort still tries the
// cheaper whole-list answer first and only reaches this when that answer is
// rejected.
//
// Scoring calls run with bounded concurrency through MapReduce (CF-009):
// each item is its own chunk, so the worker pool MapReduce already implements
// is reused rather than a second copy of it, and results come back in item
// order regardless of which call finished first.
func sortByScoringFallback[T any](ctx context.Context, items []T, opts SortOptions, opOptions types.OpOptions) ([]T, error) {
	type scoredItem struct {
		Index int
		Item  T
		Score float64
	}

	scores, _, err := MapReduce(ctx, items,
		MapReduceOptions{ChunkSize: 1, Concurrency: DefaultConcurrency},
		func(ctx context.Context, chunk []T) (float64, error) {
			item := chunk[0]
			itemJSON, err := json.Marshal(item)
			if err != nil {
				return 0, fmt.Errorf("failed to marshal item: %w", err)
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
				return 0, err
			}

			var parsed struct {
				RankScore float64 `json:"rank_score"`
			}
			if err := ParseJSONStrict(response, &parsed); err != nil {
				return 0, err
			}
			return parsed.RankScore, nil
		})
	if err != nil {
		return nil, err
	}

	scored := make([]scoredItem, len(items))
	for i, item := range items {
		scored[i] = scoredItem{Index: i, Item: item, Score: scores[i]}
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
