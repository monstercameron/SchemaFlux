package ops

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// The batch protocol.
//
// A batched request carries several items, each tagged with an id the library
// made for this request, and the answer names the id each of its outputs is for.
// An output is attributed to an item by the id its element carries and by nothing
// else: not by its position in the answer, and not by what it says. The ids are
// random, made fresh for every request, and offered to the model with nothing to
// tell it which item is which in order, so that a model that drops an answer
// cannot shift the rest up (the ids do not run in a sequence it can renumber),
// and an answer to an earlier request cannot be taken for one to a later request
// (no id is ever made twice). An element whose id was not offered in this
// request, or was offered and is answered more than once, is not an answer to
// any item and fills none.
//
// What this cannot detect is a model that writes item A's answer under item B's
// id on purpose or by a slip that leaves the id valid: nothing in Go can compare
// an answer with its question, so an answer is believed to be about the item whose
// id it carries. A caller for whom that is not good enough sends one request per
// item (RunOpManyPartial).
//
// The checks that decide whether a response covered what was offered are in Go
// and are the same ones collection.go makes for Choose, Filter and Sort over the
// ids of those operations. This file adds the per-failure-class breakdown they do
// not need: a response that omits, duplicates and invents ids has to say which of
// the three it did.

// batchPromptPrefix starts the user prompt of every batched request: what
// follows it is the JSON list of the tagged items.
const batchPromptPrefix = "Process these items:\n"

// The ids of a batched request are batchIDPrefix and batchIDRandomChars symbols
// of batchIDAlphabet, 60 bits of randomness read from crypto/rand, so that a
// model cannot guess one it was not given and two requests do not share one. The
// alphabet leaves out the letters a model copies wrongly (l and o) and the digits
// 0 and 1. The length is a constant because the planner prices the protocol's
// overhead from it (protocolOverheadBytes).
const (
	batchIDPrefix      = "i-"
	batchIDRandomChars = 12
	batchIDLength      = len(batchIDPrefix) + batchIDRandomChars
	batchIDAlphabet    = "abcdefghijkmnpqrstuvwxyz23456789"
)

// newBatchIDs makes n ids for one request, distinct from one another and from
// every id any other request was given.
func newBatchIDs(n int) []string {
	ids := make([]string, n)
	seen := make(map[string]struct{}, n)
	var raw [batchIDRandomChars]byte
	for made := 0; made < n; {
		// crypto/rand.Read never returns an error: it ends the program rather than
		// hand back bytes that are not random.
		_, _ = rand.Read(raw[:])
		var id strings.Builder
		id.Grow(batchIDLength)
		id.WriteString(batchIDPrefix)
		for _, value := range raw {
			id.WriteByte(batchIDAlphabet[int(value)%len(batchIDAlphabet)])
		}
		candidate := id.String()
		if _, taken := seen[candidate]; taken {
			continue
		}
		seen[candidate] = struct{}{}
		ids[made] = candidate
		made++
	}
	return ids
}

// batchCannotCarryEvidence reports whether op requires evidence, which the answer to
// a batch does not carry: an item answered in a batch is checked against the
// operation's invariants and normalized, and nothing more, so an operation that
// requires evidence for its answers would be delivered unchecked from a batch and
// refused from a request of its own. Such an operation runs one request per item.
func batchCannotCarryEvidence[In, Out any](op Op[In, Out]) bool {
	return op.Contract.EvidenceRequired || op.Contract.EvidencePolicy != types.EvidenceNone
}

// offeredBatchIDs reads the ids out of the user prompt of a batched request, which
// is the one thing that says which ids were offered in that request: the encoder
// made them, and the engines that send the request are told them by the text that
// is sent. want is how many items the request was asked to carry. A prompt that is
// not the library's own encoding, or that carries another number of ids, or an id
// twice or none, is refused with a configuration error before anything is sent,
// since no answer to it could be attributed to an item.
func offeredBatchIDs(user string, want int) ([]string, error) {
	refusal := types.Newf(types.KindConfiguration, "batch", "the request does not carry the ids the batch protocol attributes answers by, so no answer to it could be attributed to an item")
	payload, found := strings.CutPrefix(user, batchPromptPrefix)
	if !found {
		return nil, refusal
	}
	var tagged []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(payload), &tagged); err != nil || len(tagged) != want {
		return nil, refusal
	}
	ids := make([]string, len(tagged))
	seen := make(map[string]struct{}, len(tagged))
	for i, element := range tagged {
		if _, repeated := seen[element.ID]; repeated || element.ID == "" {
			return nil, refusal
		}
		seen[element.ID] = struct{}{}
		ids[i] = element.ID
	}
	return ids, nil
}

// BatchCoverage is the breakdown of how a batch response's ids compared to
// the ids offered: which were never answered, which were answered more than
// once, and which were never offered at all.
//
// Missing and Duplicate list ids the library made for the request, in the order
// they were offered. Unknown lists what the model wrote that was not offered; it
// is the model's text, and is never put in an error or a log.
type BatchCoverage struct {
	Offered  int
	Answered int

	// Missing lists ids that were offered and never appear in the response.
	Missing []string

	// Duplicate lists ids that were offered and appear more than once in the
	// response.
	Duplicate []string

	// Unknown lists ids present in the response that were never offered --
	// invented, an id of an earlier request, or a domain id mistaken for the
	// protocol id.
	Unknown []string
}

// OK reports whether the response covered every offered id exactly once.
func (c BatchCoverage) OK() bool {
	return len(c.Missing) == 0 && len(c.Duplicate) == 0 && len(c.Unknown) == 0
}

// Complete reports whether every offered id was answered exactly once, whatever
// else the response held: an element with an id nobody offered fills no item and
// leaves none unanswered, so it is not a reason to ask again or to ask for less.
func (c BatchCoverage) Complete() bool {
	return len(c.Missing) == 0 && len(c.Duplicate) == 0
}

// Error renders the coverage failure as counts: never an id, which are the
// request's own and mean nothing to a reader, and never a value.
func (c BatchCoverage) Error() string {
	if c.OK() {
		return ""
	}
	return fmt.Sprintf("%d offered, %d answered, %d missing, %d duplicated, %d unknown",
		c.Offered, c.Answered, len(c.Missing), len(c.Duplicate), len(c.Unknown))
}

// classifyBatchIDs compares the ids a batch response answered with against the
// canonical ids of n items (itemIDs), which is the form Merge reads.
func classifyBatchIDs(n int, answered []string) BatchCoverage {
	return classifyOfferedIDs(itemIDs(n), answered)
}

// classifyOfferedIDs compares the ids a batch response answered with against the
// ids offered for the items it was asked about.
//
// It tells apart the three ways a response can fail coverage, which a single
// aggregate error cannot: "the result is not a permutation" does not say whether
// an item was dropped, duplicated, or invented.
func classifyOfferedIDs(offered []string, answered []string) BatchCoverage {
	offeredSet := make(map[string]struct{}, len(offered))
	for _, id := range offered {
		offeredSet[id] = struct{}{}
	}

	counts := make(map[string]int, len(answered))
	for _, id := range answered {
		counts[id]++
	}

	var missing []string
	var duplicate []string
	for _, id := range offered {
		switch {
		case counts[id] == 0:
			missing = append(missing, id)
		case counts[id] > 1:
			duplicate = append(duplicate, id)
		}
	}

	var unknown []string
	seenUnknown := make(map[string]bool)
	for _, id := range answered {
		if _, offeredID := offeredSet[id]; !offeredID && !seenUnknown[id] {
			unknown = append(unknown, id)
			seenUnknown[id] = true
		}
	}

	return BatchCoverage{
		Offered:   len(offered),
		Answered:  len(answered),
		Missing:   missing,
		Duplicate: duplicate,
		Unknown:   unknown,
	}
}

// BatchItemCodec is what a stable operation supplies to get the id protocol
// without hand-rolling it: how to decode one item's answer once its id has
// resolved it to a position.
type BatchItemCodec[In, Out any] struct {
	// DecodeItem turns one item's raw answer into an Out. It is required: a
	// codec without one is refused by NewIDBatchAlgebra's Encode, by RunOpBatch
	// and by RunOpManyRecover as a configuration error, before anything is sent.
	// It runs once per item, once the item's id has been found and answered
	// exactly once. What a decode failure on one item costs depends on the path:
	// RunOpBatch, which may not be partial, fails the whole chunk, the same way
	// withRepair treats any other decode failure, rather than silently dropping
	// that one item from an otherwise-successful batch; RunOpManyRecover keeps the
	// items that decoded and asks again only for that one.
	DecodeItem func(raw json.RawMessage) (Out, error)
}

// idBatchItem is the wire shape one item takes in a batch response: the id it was
// tagged with, and its answer. Both are asked for in every element, so an
// element that leaves either out is not what was asked for, and is refused on its
// own: it costs the item it answers (readIDBatchAnswers), not the response. The
// answer is a json.RawMessage, which the reader only checks for being there; a
// null answer is refused per item, where the id is known.
type idBatchItem struct {
	ID     string          `json:"id"`
	Output json.RawMessage `json:"output"`
}

// idBatchResponse is the wire shape a whole batch answers with.
type idBatchResponse struct {
	Items []idBatchItem `json:"items"`
}

// idBatchAnswer is one element of a batch response as it was read.
type idBatchAnswer struct {
	// id is the item the element answers, or "" for an element that names none
	// (it has no id, a null one, or is null itself), which answers no item.
	id     string
	output json.RawMessage
	// refusal is what the reader found missing from an element that names its
	// item, in the library's words, or "" when the element was complete.
	refusal string
}

// readIDBatchElement reads one element of the response's list on its own. An
// element that is null names no item and comes back with no id. One that names
// its item and leaves out what the protocol asks for comes back with the reader's
// own message as the refusal. One that is not an object, or whose id is not a
// string, cannot be attributed to an item and is refused as unreadable.
func readIDBatchElement(position int, element json.RawMessage) (idBatchAnswer, error) {
	if isNullOrEmptyJSON(element) {
		return idBatchAnswer{}, nil
	}
	var item idBatchItem
	if err := decodeAnswer("batch", string(element), &item); err != nil {
		// The reader refused the element and has left nothing of it, so what it
		// names is read again without holding it to the protocol.
		var named idBatchItem
		if json.Unmarshal(element, &named) != nil {
			return idBatchAnswer{}, types.Newf(types.KindMalformedOutput, "batch", "element %d: %s", position, refusalMessage(err))
		}
		return idBatchAnswer{id: named.ID, output: named.Output, refusal: refusalMessage(err)}, nil
	}
	return idBatchAnswer{id: item.ID, output: item.Output}, nil
}

// readIDBatchAnswers reads the response as far as the list of items, and then
// each element on its own, so that an element that leaves out what the protocol
// asks for is that item's problem and not the whole response's.
//
// The response has to be an object holding a list of items, which may be empty and
// may not be null: without one nothing in it can be attributed, and it is refused
// as a whole. So is an element that is not an object, or whose id is not a
// string, which cannot be attributed to an item either. An element that is null,
// or has no id or a null one, names no item and comes back with the id "", where
// id coverage counts it as an id nobody offered. An element that names its item
// and leaves out its output comes back with the reader's own message as the
// refusal, and no output.
//
// A response in which every element is complete, which is the answer that was
// asked for, is read in one decode against the whole type. Only a response that
// is refused is read again element by element, to find what to keep.
func readIDBatchAnswers(body string) ([]idBatchAnswer, error) {
	var whole idBatchResponse
	if err := decodeAnswer("batch", body, &whole); err == nil && whole.Items != nil {
		answers := make([]idBatchAnswer, len(whole.Items))
		for i, item := range whole.Items {
			answers[i] = idBatchAnswer{id: item.ID, output: item.Output}
		}
		return answers, nil
	}

	var envelope struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := decodeAnswer("batch", body, &envelope); err != nil {
		return nil, err
	}
	if envelope.Items == nil {
		return nil, types.Newf(types.KindSchemaViolation, "batch", "items is null, and null is not an answer for it")
	}
	answers := make([]idBatchAnswer, len(envelope.Items))
	for position, element := range envelope.Items {
		answer, err := readIDBatchElement(position, element)
		if err != nil {
			return nil, err
		}
		answers[position] = answer
	}
	return answers, nil
}

// readIDBatchAnswersPartial is readIDBatchAnswers for a path that keeps what it
// can: a response that is refused as a whole, because it was cut off or because
// one element in it is unreadable, still holds the complete elements that came
// before the cut, and each of those names its item. cutOff says that the answer
// is such a remainder and not the whole response. A response with nothing to keep
// is refused with the error readIDBatchAnswers gives.
func readIDBatchAnswersPartial(body string) (answers []idBatchAnswer, cutOff bool, err error) {
	answers, err = readIDBatchAnswers(body)
	if err == nil {
		return answers, false, nil
	}
	if kept := salvageIDBatchAnswers(body); len(kept) > 0 {
		return kept, true, nil
	}
	return nil, false, err
}

// salvageIDBatchAnswers reads the elements of the response's list one at a time
// and returns the ones it could read before the first it could not: a response cut
// off in the middle of an element leaves every element before it whole.
func salvageIDBatchAnswers(body string) []idBatchAnswer {
	decoder := json.NewDecoder(strings.NewReader(cleanJSON(body)))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil
	}
	for decoder.More() {
		token, err := decoder.Token()
		key, isKey := token.(string)
		if err != nil || !isKey {
			return nil
		}
		if key != "items" {
			var skipped json.RawMessage
			if decoder.Decode(&skipped) != nil {
				return nil
			}
			continue
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim('[') {
			return nil
		}
		var answers []idBatchAnswer
		for decoder.More() {
			var element json.RawMessage
			if decoder.Decode(&element) != nil {
				break
			}
			answer, readErr := readIDBatchElement(len(answers), element)
			if readErr != nil {
				break
			}
			answers = append(answers, answer)
		}
		return answers
	}
	return nil
}

// attributedAnswer is what a response said about one offered id.
type attributedAnswer struct {
	// count is how many elements carried the id.
	count   int
	output  json.RawMessage
	refusal string
}

// attributeAnswers attributes each element of a response to the item whose id it
// carries, among the ids offered for the request, and compares what was answered
// with what was offered. byPosition[i] is what the response said about offered[i]:
// the first element that carried its id, and how many did. An element with an id
// that was not offered fills nothing.
func attributeAnswers(offered []string, answers []idBatchAnswer) (byPosition []attributedAnswer, coverage BatchCoverage) {
	position := make(map[string]int, len(offered))
	for i, id := range offered {
		position[id] = i
	}
	byPosition = make([]attributedAnswer, len(offered))
	ids := make([]string, len(answers))
	for k, answer := range answers {
		ids[k] = answer.id
		at, offeredID := position[answer.id]
		if !offeredID {
			continue
		}
		byPosition[at].count++
		if byPosition[at].count == 1 {
			byPosition[at].output = answer.output
			byPosition[at].refusal = answer.refusal
		}
	}
	return byPosition, classifyOfferedIDs(offered, ids)
}

// NewIDBatchAlgebra builds a BatchAlgebra whose Encode tags every item with an id
// made for that request and whose Merge decodes the answer once its ids have been
// resolved, requiring exact coverage before decoding a single item.
//
// Encode makes new ids on every call, so what it returns is the request for one
// send. Merge reads an answer whose ids are the canonical ones (itemIDs), not the
// ids Encode offered: RunOpBatch, RunOpMany and RunOpManyRecover, which know the ids
// of the request they sent, resolve the model's ids to the items' and hand Merge
// the answer in that form, and a body of ids Encode offered is refused by Merge as
// covering nothing. An answer is attributed to an item by the id it carries and by
// nothing else. What no check can catch is a model that writes one item's answer
// under another item's id; a caller for whom that matters sends one request per
// item (RunOpManyPartial).
//
// Exact coverage is required regardless of Kind: an itemwise batch (kind
// Independent) has no item it can drop, since every input demands exactly one
// output, and an aggregate batch (kind Permutation, Partition, ...) needs the same
// per-item pass to hold before its GlobalValidate can mean anything --
// GlobalValidate runs over Merge's output, and Merge cannot produce one output per
// item if the ids did not cover every item exactly once. Kind.RequiresExactCoverage
// documents this; the one kind that would answer false, Subset, is Filter's
// existing contract and is intentionally not built through this constructor --
// collection.go already implements it.
//
// A codec with no DecodeItem makes an algebra whose Encode and Merge refuse every
// call with a configuration error: nothing is sent.
func NewIDBatchAlgebra[In, Out any](class types.BatchClass, kind types.AlgebraKind, systemPrompt string, codec BatchItemCodec[In, Out]) BatchAlgebra[In, Out] {
	return BatchAlgebra[In, Out]{
		Class:          class,
		Kind:           kind,
		checksEachItem: true,
		Encode: func(items []In, _ types.OpOptions) (string, string, error) {
			if codec.DecodeItem == nil {
				return "", "", types.Newf(types.KindConfiguration, "batch", "the codec has no DecodeItem to read one item's answer with")
			}
			ids := newBatchIDs(len(items))
			tagged := make([]taggedItem[In], len(items))
			for i, item := range items {
				tagged[i] = taggedItem[In]{ID: ids[i], Item: item}
			}
			payload, err := json.Marshal(tagged)
			if err != nil {
				// RunOpBatch adds which batch of how many items this was.
				return "", "", types.Newf(types.KindInvalidRequest, "batch", "the items could not be written as JSON")
			}
			return systemPrompt, batchPromptPrefix + string(payload), nil
		},
		Merge: func(body string, items []In) ([]Out, error) {
			if codec.DecodeItem == nil {
				return nil, types.Newf(types.KindConfiguration, "batch", "the codec has no DecodeItem to read one item's answer with")
			}
			// This is the path that may not return a partial batch, so any element
			// that is bad refuses the whole answer, below.
			answers, err := readIDBatchAnswers(body)
			if err != nil {
				return nil, batchShapeFailure(err)
			}

			offered := itemIDs(len(items))
			byPosition, coverage := attributeAnswers(offered, answers)
			if !coverage.OK() {
				return nil, types.Newf(types.KindBatchProtocolViolation, "batch", "the answer did not cover its items exactly: %s", coverage.Error())
			}

			outputs := make([]Out, len(items))
			for i := range offered {
				// An item that was named and answered with nothing is not an
				// answer. A codec that decodes into a struct would turn a null
				// into the zero value and return it as the item's result, so it
				// is refused here, before the codec sees it.
				if isNullOrEmptyJSON(byPosition[i].output) {
					return nil, types.Newf(types.KindBatchProtocolViolation, "batch", "item %d was answered with no output%s", i+1, parenthesised(byPosition[i].refusal))
				}
				value, err := codec.DecodeItem(byPosition[i].output)
				if err != nil {
					return nil, &types.OperationError{
						Kind: kindOrDefault(err, types.KindMalformedOutput), Op: "batch",
						Message: fmt.Sprintf("the answer for item %d does not decode", i+1), Cause: err,
					}
				}
				outputs[i] = value
			}
			return outputs, nil
		},
	}
}

// batchShapeFailure is the error for an answer that is not the shape the
// protocol asks for. The reason it gives is the reader's own, which names the
// member that was wrong in the library's words and holds nothing the model wrote.
func batchShapeFailure(reason error) error {
	return &types.OperationError{
		Kind:    kindOrDefault(reason, types.KindMalformedOutput),
		Op:      "batch",
		Message: "the answer is not the {\"items\":[{\"id\":...,\"output\":...}]} shape: " + reason.Error(),
		Cause:   reason,
	}
}

// translateBatchAnswer resolves the ids of a batch answer to the canonical ids
// Merge reads, for the strict path that may not return a partial batch. The ids
// are the ones the request offered. A response that does not cover them exactly,
// or answers an item with nothing, is refused with the kind that says so and no
// text of the model's.
func translateBatchAnswer(body string, offered []string) (string, error) {
	answers, err := readIDBatchAnswers(body)
	if err != nil {
		return "", batchShapeFailure(err)
	}
	byPosition, coverage := attributeAnswers(offered, answers)
	if !coverage.OK() {
		return "", types.Newf(types.KindBatchProtocolViolation, "batch", "the answer did not cover its items exactly: %s", coverage.Error())
	}
	canonical := make([]idBatchItem, len(offered))
	for i := range offered {
		if isNullOrEmptyJSON(byPosition[i].output) {
			return "", types.Newf(types.KindBatchProtocolViolation, "batch", "item %d was answered with no output%s", i+1, parenthesised(byPosition[i].refusal))
		}
		canonical[i] = idBatchItem{ID: itemID(i), Output: byPosition[i].output}
	}
	translated, err := json.Marshal(idBatchResponse{Items: canonical})
	if err != nil {
		return "", types.Newf(types.KindInvariantViolation, "batch", "the answer could not be written again with the ids of its items")
	}
	return string(translated), nil
}

// partialMerge is what mergeIDBatchPartial resolved of one batched response.
type partialMerge[Out any] struct {
	// resolved is local-subset-index -> Out for every item whose id was answered
	// exactly once among the ids offered and decoded without error.
	resolved map[int]Out

	// unresolved lists the local-subset index of every other item, in ascending
	// order: the set the cascade asks again.
	unresolved []int

	// causes says why, for each of those: the id was never named, was named more
	// than once, was answered with nothing, or its answer did not decode. A cause
	// is a classified error that carries nothing of the answer, which is the
	// model's.
	causes map[int]error

	// coverage is the raw classification, carried out so the cascade can turn it
	// into an AdaptiveChunkState outcome.
	coverage BatchCoverage

	// cutOff is true when the response was refused as a whole and only the
	// elements before the cut were kept.
	cutOff bool

	// err is set when nothing could be read at all: the body is not the
	// protocol's shape, or the codec cannot decode.
	err error
}

// mergeIDBatchPartial decodes as many of subsetItems' answers out of body as the
// response's own id coverage allows, instead of requiring exact coverage the way
// the batch's Merge does. It is the "keep the valid items" step of the recovering
// cascade: a missing id, a duplicated id (which id's payload is the real one is
// not decidable from the response alone), or one item's own decode failure marks
// only that one item unresolved -- it never invalidates the ids that did resolve
// cleanly, the way Merge's all-or-nothing coverage check does. An element whose
// id was not offered in this request resolves no item and fills no other.
//
// offered is the ids the request carried, in the order of subsetItems.
//
// A body that does not parse as the expected {"items":[{"id":...,"output":...}]}
// shape, and has no complete element to keep, resolves nothing and marks every
// offered item unresolved: coverage is reported as Offered-only, and the causes
// are left to err, which says it once for all of them. A codec with no DecodeItem
// is the same, with a configuration error.
func mergeIDBatchPartial[In, Out any](body string, offered []string, subsetItems []In, codec BatchItemCodec[In, Out]) partialMerge[Out] {
	all := func() []int {
		unresolved := make([]int, len(subsetItems))
		for i := range subsetItems {
			unresolved[i] = i
		}
		return unresolved
	}
	if codec.DecodeItem == nil {
		// Nothing can be decoded, and calling the nil function would end the run
		// in a panic. RunOpManyRecover refuses such a codec before it sends
		// anything; this is for any other caller.
		return partialMerge[Out]{unresolved: all(), coverage: BatchCoverage{Offered: len(subsetItems)},
			err: types.Newf(types.KindConfiguration, "batch", "the codec has no DecodeItem to read one item's answer with")}
	}
	if len(offered) != len(subsetItems) {
		return partialMerge[Out]{unresolved: all(), coverage: BatchCoverage{Offered: len(subsetItems)},
			err: types.Newf(types.KindInvariantViolation, "batch", "%d ids were offered for %d items", len(offered), len(subsetItems))}
	}

	answers, cutOff, parseErr := readIDBatchAnswersPartial(body)
	if parseErr != nil {
		return partialMerge[Out]{unresolved: all(), coverage: BatchCoverage{Offered: len(subsetItems)},
			err: batchShapeFailure(parseErr)}
	}

	byPosition, coverage := attributeAnswers(offered, answers)

	merged := partialMerge[Out]{
		resolved: make(map[int]Out, len(subsetItems)),
		causes:   map[int]error{},
		coverage: coverage,
		cutOff:   cutOff,
	}
	for i := range offered {
		switch {
		case byPosition[i].count == 0:
			merged.unresolved = append(merged.unresolved, i)
			merged.causes[i] = types.Newf(types.KindBatchProtocolViolation, "batch", "the response has no answer for item %d", i+1)
			continue
		case byPosition[i].count > 1:
			// Duplicated: there is no one trustworthy payload to decode.
			merged.unresolved = append(merged.unresolved, i)
			merged.causes[i] = types.Newf(types.KindBatchProtocolViolation, "batch", "the response answers item %d %d times", i+1, byPosition[i].count)
			continue
		}
		// Named and answered with nothing: unresolved, as a missing id is.
		if isNullOrEmptyJSON(byPosition[i].output) {
			merged.unresolved = append(merged.unresolved, i)
			merged.causes[i] = types.Newf(types.KindBatchProtocolViolation, "batch", "item %d was answered with no output%s", i+1, parenthesised(byPosition[i].refusal))
			continue
		}
		value, decodeErr := codec.DecodeItem(byPosition[i].output)
		if decodeErr != nil {
			merged.unresolved = append(merged.unresolved, i)
			merged.causes[i] = itemDecodeCause(i, decodeErr)
			continue
		}
		merged.resolved[i] = value
	}
	return merged
}

// itemDecodeCause is the reason an item's answer was not accepted when its
// codec could not read it. It keeps the kind the codec classified the failure
// as, and a failure nobody classified is a schema violation: the answer did not
// have the shape that was asked for. The text names the field a type mismatch
// was found at and the type it was read into, both of which are the caller's
// own names; the decoder's own message is not used, because for a number that
// does not fit an integer it repeats the number. The codec's error stays in the
// chain and not in the text.
func itemDecodeCause(position int, decodeErr error) error {
	// A decoder that panicked, or ended its goroutine, is not an answer of the
	// wrong shape: it is the caller's code failing on this item, and it is
	// reported as that. The panic's value is not in the error.
	if types.NarrowingOf(decodeErr) == types.NarrowPanic {
		cause := types.Newf(types.KindInvariantViolation, "batch", "item %d: the decoder panicked or ended its goroutine without returning", position+1)
		cause.Narrow = types.NarrowPanic
		return cause
	}
	kind := kindOrDefault(decodeErr, types.KindSchemaViolation)
	var mismatch *json.UnmarshalTypeError
	if errors.As(decodeErr, &mismatch) && mismatch.Field != "" && mismatch.Type != nil {
		return &types.OperationError{Kind: kind, Op: "batch", Cause: decodeErr,
			Message: fmt.Sprintf("item %d: the field /%s does not fit the type it is decoded into (%s)",
				position+1, strings.ReplaceAll(mismatch.Field, ".", "/"), mismatch.Type)}
	}
	return &types.OperationError{Kind: kind, Op: "batch", Cause: decodeErr,
		Message: fmt.Sprintf("item %d: the output does not decode into the result type", position+1)}
}

// RunOpBatch runs op's declared batch algebra over one chunk of items: encode
// the batch request, call the model once, merge the response by id, and --
// for a BatchAggregate operation -- run the whole-set validation nothing
// called before this. It is Encode, Merge, and GlobalValidate actually
// running.
//
// For a batch built by NewIDBatchAlgebra the answer is attributed by the ids
// this request offered, and by nothing else; a model that writes one item's
// answer under another's id cannot be caught, and a caller for whom that matters
// sends one request per item. A batch with an Encode and Merge of its own is as
// good as they are.
//
// It does not repair: a batch failure here fails the chunk. Feeding a
// chunk's own already-invalid response back into the model as a repair
// candidate here would be a second, divergent repair path from the one
// withRepair (repair.go) implements for atomic calls -- worse than not
// repairing at all.
//
// The errors it returns are classified and say which step failed in the
// library's words. What a vendor, a caller's Merge, invariant or GlobalValidate
// reported is the error's cause, and is not in its text. The caller's own
// functions (Encode, Merge, the invariants, Normalize, GlobalValidate) run on the
// caller's goroutine here: a panic in one reaches the caller, and so does a
// runtime.Goexit. RunOpManyRecover runs them where both are an item's failure.
func RunOpBatch[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt types.OpOptions) ([]Out, error) {
	// The client the call runs on is fixed first, and its deadline binds a call
	// whose context has none: this path derives none of its own.
	ctx, _ = PinExec(ctx)
	ctx, cancel := clientDeadline(ctx)
	defer cancel()

	name := operationLabel(op.ID)
	if op.Batch.Encode == nil || op.Batch.Merge == nil {
		return nil, types.Newf(types.KindConfiguration, name, "batch class %s is declared but there is no Encode and Merge to run it", op.Batch.Class)
	}
	if len(items) == 0 {
		return nil, nil
	}
	if batchCannotCarryEvidence(op) {
		return nil, types.Newf(types.KindConfiguration, name, "the operation requires evidence, which the answer to a batch does not carry: it runs one request per item")
	}

	system, user, err := op.Batch.Encode(items, opt)
	if err != nil {
		return nil, failureOf(name, kindOrDefault(err, types.KindConfiguration), err, "the batch of %d items could not be encoded", len(items))
	}
	var offered []string
	if op.Batch.checksEachItem {
		if offered, err = offeredBatchIDs(user, len(items)); err != nil {
			return nil, failureOf(name, types.KindConfiguration, err, "the batch of %d items could not be sent", len(items))
		}
	}

	body, err := callLLM(ctx, system, user, opt)
	if err != nil {
		return nil, failureOf(name, batchFailureKind(err), err, "the request for a batch of %d items failed", len(items))
	}
	if offered != nil {
		if body, err = translateBatchAnswer(body, offered); err != nil {
			return nil, failureOf(name, kindOrDefault(err, types.KindMalformedOutput), err, "the answer to a batch of %d items could not be used", len(items))
		}
	}

	outputs, err := op.Batch.Merge(body, items)
	if err != nil {
		return nil, failureOf(name, kindOrDefault(err, types.KindMalformedOutput), err, "the answer to a batch of %d items could not be merged", len(items))
	}

	// An itemwise batch answers every item once. A merge that returns some
	// other number of outputs has lost track of which output is whose, and
	// that was found only by whoever indexed them afterwards.
	if op.Batch.Class == types.BatchItemwise && len(outputs) != len(items) {
		return nil, types.Newf(types.KindInvariantViolation, name,
			"the batch was merged into %d outputs for %d items", len(outputs), len(items))
	}

	// Every output is held to the operation's invariants and then normalized,
	// as RunOp does for one. Neither was done here: the invariants were not
	// run at all while RunOpBatchResult reported the level that says they
	// were, and a batch's outputs differed from a single call's for the same
	// operation by whatever Normalize does.
	for i, output := range outputs {
		for _, invariant := range op.Contract.Invariants {
			if invErr := invariant(output); invErr != nil {
				return nil, failureOf(name, kindOrDefault(invErr, types.KindInvariantViolation), invErr, "output %d of the batch broke a check of the operation", i+1)
			}
		}
		if op.Contract.Normalize != nil {
			outputs[i] = op.Contract.Normalize(output)
		}
	}

	if op.Batch.Class == types.BatchAggregate && op.Batch.GlobalValidate != nil {
		if err := op.Batch.GlobalValidate(items, outputs); err != nil {
			return nil, failureOf(name, kindOrDefault(err, types.KindInvariantViolation), err, "the batch broke a check made over all of its items")
		}
	}

	return outputs, nil
}

// RunOpBatchResult runs RunOpBatch and additionally returns the execution
// envelope, the same way RunOpResult wraps RunOp -- one execution path, not
// a second one that could drift (T-01's lesson, cited in op.go for the same
// reason).
//
// Meta.Strategy carries the shape this chunk ran under ("mdsp"), reusing the
// field Sort already uses to report which of its own strategies produced an
// answer -- PL-002's requirement that the chosen shape be recorded in the
// envelope, without adding a field to types.Meta this task is not permitted
// to edit.
func RunOpBatchResult[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt types.OpOptions) (_ types.Result[[]Out], runErr error) {
	ctx, records := withCallRecording(WithOperationName(ctx, op.ID.Name))
	// The call is one unit to the installed observers, ending with the kind of
	// whatever error this returns.
	ctx, observed := observeOperation(ctx, op.ID.Name)
	defer observed.end(&runErr)

	// What a batch of this operation establishes (batchContractLevel), on the
	// route it is about to take.
	declared := plannedContractLevel(ctx, batchContractLevel(op), opt)
	if floorErr := checkContractFloor(ctx, declared); floorErr != nil {
		return types.Result[[]Out]{Meta: types.Meta{Operation: op.ID.String(), RequestedContract: declared}}, floorErr
	}

	value, err := RunOpBatch(ctx, op, items, opt)

	meta := records.meta(op.ID.String())
	meta.Strategy = types.ShapeMDSP.String()
	meta.RequestedContract = declared
	if err == nil {
		// The route that answered may not be the one that was planned, a
		// fallback's alternate say, so the level is worked out again from it.
		meta.DeliveredContract = negotiatedContractLevel(batchContractLevel(op), opt, meta.Provider, meta.Model)
	}

	if err != nil {
		return types.Result[[]Out]{Meta: meta}, err
	}
	// The contract floor of the caller's policy, on the call or on the client:
	// an answer delivered below it is an error and not a quieter success. Only
	// RunOpResult asked; the six other places an envelope is built did not, so
	// a floor set on a client held for none of the operations a caller is
	// most likely to use.
	//
	// What is checked is the level the answering route earned, worked out above,
	// and not the planned one the floor was checked against before the call. They
	// differ when a route other than the planned one answered and enforces less.
	if policyErr := enforceContractPolicy(ctx, meta.DeliveredContract); policyErr != nil {
		return types.Result[[]Out]{Meta: meta}, policyErr
	}
	return types.Result[[]Out]{Value: value, Meta: meta}, nil
}
