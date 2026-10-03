package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// OP-101: stable, invocation-local IDs instead of an echoed object or a bare
// index.
//
// Choose, Filter, and Sort used to send the model the raw items and ask for
// the chosen/kept/sorted items back, matched to the caller's own values by
// equality. Two failures came from that: a model that echoed an item with one
// field subtly changed produced a record that looked authoritative and was not
// the caller's, and the model had to spend output tokens reproducing data it
// already had, which is what capped every operation's size (C-06).
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
// object rather than returning it bare gives decodeAnswer a member to
// require, so an empty object or an answer about something else is caught
// before the ID coverage check ever runs. The list may be empty, which is an
// answer for Filter, and may not be null, which readIDList refuses: the reader
// lets a null through for a slice.
type idListResponse struct {
	IDs []string `json:"ids"`
}

// readIDList reads an answer that is a list of ids. A list that is empty is an
// answer, and one that is null is the model leaving it out, so it is refused
// instead of being read as an empty list.
func readIDList(op, response string) (idListResponse, error) {
	var parsed idListResponse
	if err := decodeAnswer(op, response, &parsed); err != nil {
		return idListResponse{}, err
	}
	if parsed.IDs == nil {
		return idListResponse{}, types.Newf(types.KindSchemaViolation, op, "ids is null, and null is not an answer for it")
	}
	return parsed, nil
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
// The answer is the id and nothing else, so an option that asks for more than
// that cannot be honoured or checked: RequireReasoning is put to the model as
// an instruction and nothing comes back for it, and TopN and IncludeScores have
// no place in the answer at all. They are stated or ignored, never verified.
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
		return result, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
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

	// The options as the message carries them, each under its id, and where each
	// option's own JSON lies within that text: the same bytes describe the
	// option to a decision provider (LX-100), so nothing is written twice.
	offered := itemIDs(len(options))
	optionsJSON, described, err := taggedOptionsJSON(options, offered)
	if err != nil {
		return result, types.ChooseError{
			OptionCount: len(options),
			Reason:      fmt.Sprintf("failed to marshal options: %v", err),
			Err:         types.Classified(types.KindInvalidRequest, err),
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

	// The steering and the criteria travel in opOptions.Steering, which callLLM
	// adds to the message once, marked as the caller's. A copy written here as
	// well was not marked and did not pass through the delimiter check, so a
	// string that forged the delimiter was neutralised in one copy and in force
	// in the other, and every call paid for the text twice.
	userPrompt := fmt.Sprintf("Choose the best option from this list:\n%s", optionsJSON)

	// LX-100: the same choice typed, for a provider that answers decisions and
	// reads no prompt. The list is the state, each id is an option described by
	// its item, and the caller's steering and criteria are the guidance. The
	// instructions to give reasoning and to follow a strategy are for a model
	// that writes, are checked on neither path, and are not part of it.
	decision := newDecisionCall(ctx, optionsJSON, chooseGuidance(opts), nil, nil)
	decision.one[0] = llm.DecisionQuestion{Kind: llm.DecisionChoice, Ask: chooseAsk, Options: offered, Criteria: described}
	decision.decision.Questions = decision.one[:]

	response, err := callLLM(decision, systemPrompt, userPrompt, opOptions)
	if err != nil {
		return result, types.ChooseError{
			OptionCount: len(options),
			Reason:      err.Error(),
			Err:         err,
		}
	}

	var parsed idResponse
	if decision.typed {
		// A decision provider answered, and CallLLM wrote its answer in the
		// decision reader's form: the id chosen, by its position.
		answers, _, readErr := readAssessAnswer(response, []assessQuestion{{kind: questionChoice, ask: chooseAsk, labels: offered}}, false)
		if readErr != nil {
			return zeroOf[T](), types.ChooseError{
				OptionCount: len(options),
				Reason:      "the decision provider's answer is not the id of one of the options",
				Err:         readErr,
			}
		}
		parsed.ID = offered[answers[0].choice]
	} else if err := decodeAnswer("choose", response, &parsed); err != nil {
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
	if err := MemberOf(offered, parsed.ID); err != nil {
		return zeroOf[T](), types.ChooseError{
			OptionCount: len(options),
			Reason:      err.Error(),
			Err:         err,
		}
	}

	return options[idPositions(offered)[parsed.ID]], nil
}

// chooseAsk is the question a choice puts to a decision provider. It is the
// library's own and holds nothing of the caller's.
const chooseAsk = "Which option is the best choice?"

// chooseGuidance is what the caller said of how to choose, for a decision
// provider: the steering, and the selection criteria.
func chooseGuidance(opts ChooseOptions) string {
	guidance := strings.TrimSpace(effectiveSteering(opts.CommonOptions, opts.OpOptions))
	if len(opts.Criteria) == 0 {
		return guidance
	}
	criteria := "Selection criteria: " + strings.Join(opts.Criteria, ", ")
	if guidance == "" {
		return criteria
	}
	return guidance + ". " + criteria
}

// taggedOptionsJSON writes options as the message carries them,
// [{"id":"i-000001","item":...},...], byte for byte as json.Marshal writes
// tagItems(options), and returns with it the JSON of each item by itself, in
// order. Each of those is a part of the text returned and not a copy of it.
func taggedOptionsJSON[T any](options []T, ids []string) (string, []string, error) {
	var written bytes.Buffer
	encoder := json.NewEncoder(&written)
	type span struct{ from, to int }
	spans := make([]span, len(options))

	written.WriteByte('[')
	for i := range options {
		if i > 0 {
			written.WriteByte(',')
		}
		written.WriteString(`{"id":"`)
		written.WriteString(ids[i])
		written.WriteString(`","item":`)
		spans[i].from = written.Len()
		// By address, as an element of the slice json.Marshal walks is reached:
		// an item whose MarshalJSON is on the pointer is written by it.
		if err := encoder.Encode(&options[i]); err != nil {
			return "", nil, err
		}
		// The encoder ends every value with a line break.
		written.Truncate(written.Len() - 1)
		spans[i].to = written.Len()
		written.WriteByte('}')
	}
	written.WriteByte(']')

	text := written.String()
	items := make([]string, len(options))
	for i, at := range spans {
		items[i] = text[at.from:at.to]
	}
	return text, items, nil
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
// The answer is a list of ids, so MinConfidence and IncludeReasons are put to
// the model as instructions and nothing in the answer lets Go check the first
// or return the second. They are guidance, not guarantees.
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
func Filter[T any](items []T, opts FilterOptions) (_ []T, runErr error) {
	defer observedPlain(&opts.CommonOptions, &opts.OpOptions, "filter").end(&runErr)
	// Validate options
	if err := opts.Validate(); err != nil {
		return nil, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
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
				useContext(&chunkOpts.CommonOptions, &chunkOpts.OpOptions, ctx)
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
	if callerSteering := effectiveSteering(opts.CommonOptions, opts.OpOptions); callerSteering != "" {
		steering = callerSteering + ". " + steering
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
			Err:       types.Classified(types.KindInvalidRequest, err),
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
	parsed, err := readIDList("filter", response)
	if err != nil {
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
// IncludeScores is put to the model as an instruction and has nowhere to be
// returned: the result is the items in order. It is guidance, not a guarantee.
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
func Sort[T any](items []T, opts SortOptions) (_ []T, runErr error) {
	defer observedPlain(&opts.CommonOptions, &opts.OpOptions, "sort").end(&runErr)
	result, _, err := sortWithStrategy(items, opts)
	return result, err
}

// sortContract is the level SortResult requests and delivers. Sort declares no
// schema on its request, and a level of schema constrained or above needs one
// (see the header of contracttable.go), so the permutation Go checks on a
// whole-list answer, and the scores it orders by otherwise, do not raise it
// above the answer having parsed as JSON.
const sortContract = types.ContractJSONWellFormed

// SortResult runs Sort and returns the record of how it ran, including which
// strategy produced the answer (OP-106): "whole-list" for the single-call id
// protocol, "scoring" when the item count put it straight on the per-item
// path, and "scoring-fallback" when a whole-list answer was attempted and
// rejected. It is the same execution Sort runs -- see extractresult.go for
// why this library attaches the envelope this way rather than giving every
// operation a second, divergent code path.
//
// Every envelope it returns, a refusal and an error included, carries the
// caller's request and correlation ids and the wall-clock time the whole call
// took. Both came from the last provider call alone: an envelope built before
// any call was made had no ids, and a sort that scored every item reported how
// long the last item took. Attempts counts provider calls and is zero for a
// sort that made none (fewer than two items): saying one would claim a call
// that was never sent.
func SortResult[T any](items []T, opts SortOptions) (_ types.Result[[]T], runErr error) {
	start := time.Now()
	requestID := firstSet(opts.CommonOptions.RequestID, opts.OpOptions.RequestID)
	correlationID := firstSet(opts.CommonOptions.CorrelationID, opts.OpOptions.CorrelationID)

	ctx, records := withCallRecording(WithOperationName(resolvedContext(opts.CommonOptions, opts.OpOptions), "sort"))
	// The call is one unit to the installed observers, ending with the kind of
	// whatever error this returns.
	ctx, observed := observeOperation(ctx, "sort")
	defer observed.end(&runErr)
	useContext(&opts.CommonOptions, &opts.OpOptions, ctx)

	// finish is the one place an envelope leaves this function. A call's own
	// record names the ids the call went out under, which are the caller's when
	// it set them and minted ones when it did not, so those are kept; the
	// caller's own fill the envelopes that no call produced.
	finish := func(meta types.Meta) types.Meta {
		if meta.RequestID == "" {
			meta.RequestID = requestID
		}
		if meta.CorrelationID == "" {
			meta.CorrelationID = correlationID
		}
		meta.Elapsed = time.Since(start)
		return meta
	}

	// A sort of fewer than two items sends nothing, so no route has a say in what
	// it delivers. Any other sort is planned and delivered on the route that
	// serves it, as RunOpBatchResult does: with a schema asked of a route that
	// does not enforce one, the level is lower than the sort establishes.
	sends := len(items) > 1
	planned := sortContract
	var planning types.OpOptions
	if sends {
		common := opts.CommonOptions
		common.Timeout = 0 // the timeout is applied once, by resolvedContext
		planning = mergeEmbeddedOpOptions(common, opts.OpOptions)
		planned = plannedContractLevel(ctx, sortContract, planning)
	}
	if floorErr := checkContractFloor(ctx, planned); floorErr != nil {
		return types.Result[[]T]{Meta: finish(types.Meta{Operation: "sort", RequestedContract: sortContract})}, floorErr
	}

	value, strategy, err := sortWithStrategy(items, opts)

	meta := records.meta("sort")
	meta.Strategy = strategy
	meta.RequestedContract = sortContract
	if err == nil {
		meta.DeliveredContract = sortContract
		if sends {
			// The route that answered may not be the one that was planned, so
			// the level is worked out again from it.
			meta.DeliveredContract = negotiatedContractLevel(sortContract, planning, meta.Provider, meta.Model)
		}
	}
	meta = finish(meta)

	if err != nil {
		return types.Result[[]T]{Meta: meta}, err
	}
	// The contract floor of the caller's policy, on the call or on the client:
	// an answer delivered below it is an error and not a quieter success. Only
	// RunOpResult asked; the six other places an envelope is built did not, so
	// a floor set on a client held for none of the operations a caller is
	// most likely to use.
	if policyErr := enforceContractPolicy(ctx, meta.DeliveredContract); policyErr != nil {
		return types.Result[[]T]{Meta: meta}, policyErr
	}
	return types.Result[[]T]{Value: value, Meta: meta}, nil
}

func sortWithStrategy[T any](items []T, opts SortOptions) ([]T, string, error) {
	// Validate options
	if err := opts.Validate(); err != nil {
		return nil, "", types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
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
	if callerSteering := effectiveSteering(opts.CommonOptions, opts.OpOptions); callerSteering != "" {
		steering = callerSteering + ". " + steering
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
			Err:       types.Classified(types.KindInvalidRequest, err),
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
	parsed, err := readIDList("sort", response)
	if err != nil {
		fallback, fallbackErr := sortByScoringFallback(ctx, items, opts, opOptions)
		if fallbackErr == nil {
			return fallback, "scoring-fallback", nil
		}
		// No response body in the Reason. Every caller logs this error and the
		// body is their data reshaped -- the same X-03 that OP-110 removed from
		// the two Filter errors, surviving here.
		return nil, "scoring-fallback", sortFallbackFailed(len(items), "failed to parse sorted ids", err, fallbackErr)
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
		return nil, "scoring-fallback", sortFallbackFailed(len(items), err.Error(), err, fallbackErr)
	}

	return result, "whole-list", nil
}

// sortFallbackFailed is the error for a sort whose whole-list answer was
// refused and whose scoring fallback then failed too.
//
// It carries both causes, the fallback's first. The caller used to be given
// only the refused answer, so a call that ended because its context was
// cancelled or its budget ran out was reported as a malformed answer:
// errors.Is(err, context.Canceled) was false, the budget error could not be
// reached, and a retry rule keyed on the kind saw the wrong one. The strategy
// is the fallback's, since scoring calls were made.
func sortFallbackFailed(itemCount int, reason string, refused, fallbackErr error) error {
	return types.SortError{
		ItemCount: itemCount,
		Reason:    reason + "; the scoring fallback failed as well",
		Err:       errors.Join(fallbackErr, refused),
	}
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
				return 0, types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal item: %w", err))
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

			// The score has to be there and may not be null, which the reader
			// holds a plain number to, so that a score left out is told from a
			// zero. It is held to the 0 to 1 scale the prompt states: a null used
			// to sort the item last as a zero, and 12 or -3 was used as it came,
			// re-ordering the caller's data with no signal.
			var parsed struct {
				RankScore float64 `json:"rank_score"`
			}
			if err := decodeAnswer("sort", response, &parsed); err != nil {
				return 0, err
			}
			if scaleErr := onScale("sort", "the rank score", parsed.RankScore); scaleErr != nil {
				return 0, scaleErr
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
