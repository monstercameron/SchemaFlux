package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/monstercameron/schemaflux/internal/types"
)

// PL-003: the MDSP batch protocol, sharing OP-101's id scheme rather than
// inventing a second one.
//
// collection.go already solved "stable invocation-local ids out, exact
// coverage validated in Go, caller order reconstructed, output position
// never trusted" for Choose/Filter/Sort. itemID, itemIDs, idPositions, and
// tagItems (collection.go) are that scheme; resolveSubsetByIDs and
// resolvePermutationByIDs (also collection.go) are its two reconstruction
// shapes. This file adds one more consumer of the same vocabulary --
// NewIDBatchAlgebra, which builds a BatchAlgebra.Encode/Merge pair any
// stable batched operation can use to get the protocol for free -- and one
// thing collection.go's callers did not need: a per-failure-class
// breakdown, because SubsetOf/SameMultiset each return a single aggregate
// error and PL-003 asks a response that omits, duplicates, invents, and
// reorders ids to each be distinguishable.

// BatchCoverage is the breakdown of how a batch response's ids compared to
// the ids offered: which were never answered, which were answered more than
// once, and which were never offered at all.
//
// The three lists hold ids, not values -- the ids are this package's own
// synthetic identifiers ("i-000007"), not the caller's data, so naming them
// is not the AGENTS.md payload violation naming a caller's value would be.
type BatchCoverage struct {
	Offered  int
	Answered int

	// Missing lists ids that were offered and never appear in the response.
	Missing []string

	// Duplicate lists ids that were offered and appear more than once in the
	// response.
	Duplicate []string

	// Unknown lists ids present in the response that were never offered --
	// invented, or a domain id mistaken for the protocol id.
	Unknown []string
}

// OK reports whether the response covered every offered id exactly once.
func (c BatchCoverage) OK() bool {
	return len(c.Missing) == 0 && len(c.Duplicate) == 0 && len(c.Unknown) == 0
}

// Error renders the coverage failure as counts, never as the ids' positions
// in the caller's data (there are none to leak -- these are protocol ids),
// but still without implying the ids matter to the caller reading the log.
func (c BatchCoverage) Error() string {
	if c.OK() {
		return ""
	}
	return fmt.Sprintf("%d offered, %d answered, %d missing, %d duplicated, %d unknown",
		c.Offered, c.Answered, len(c.Missing), len(c.Duplicate), len(c.Unknown))
}

// classifyBatchIDs compares the ids a batch response answered with against
// the n ids offered for the items it was asked about, using the exact
// itemIDs/idPositions vocabulary OP-101 established.
//
// It is a diagnostic layer, not a second membership test: the ids it
// classifies against are the same offered set SubsetOf/SameMultiset already
// check, computed the same way (itemIDs). What it adds is telling apart the
// three ways a response can fail coverage, which a single aggregate error
// cannot -- "the result is not a permutation" does not say whether an item
// was dropped, duplicated, or invented, and PL-003 requires the batch
// protocol to say which.
func classifyBatchIDs(n int, answered []string) BatchCoverage {
	offered := itemIDs(n)
	offeredSet := idPositions(offered)

	counts := make(map[string]int, len(answered))
	for _, id := range answered {
		counts[id]++
	}

	var missing []string
	for _, id := range offered {
		if counts[id] == 0 {
			missing = append(missing, id)
		}
	}

	var duplicate, unknown []string
	seenDuplicate := make(map[string]bool)
	seenUnknown := make(map[string]bool)
	for _, id := range answered {
		if _, offeredID := offeredSet[id]; !offeredID {
			if !seenUnknown[id] {
				unknown = append(unknown, id)
				seenUnknown[id] = true
			}
			continue
		}
		if counts[id] > 1 && !seenDuplicate[id] {
			duplicate = append(duplicate, id)
			seenDuplicate[id] = true
		}
	}

	// Map iteration built these lists (via counts), so they are sorted before
	// they can reach anything rendered or serialized -- AGENTS.md's rule that
	// map-derived output is sorted, because an unsorted list would make two
	// runs over the identical failure disagree in a diff.
	sort.Strings(missing)
	sort.Strings(duplicate)
	sort.Strings(unknown)

	return BatchCoverage{
		Offered:   n,
		Answered:  len(answered),
		Missing:   missing,
		Duplicate: duplicate,
		Unknown:   unknown,
	}
}

// BatchItemCodec is what a stable operation supplies to get PL-003's MDSP
// protocol without hand-rolling it: how to decode one item's answer once its
// id has resolved it to a position.
type BatchItemCodec[In, Out any] struct {
	// DecodeItem turns one item's raw answer into an Out. It runs once per
	// item, after coverage has already been validated -- a decode failure on
	// one item fails the whole chunk, the same way withRepair treats any
	// other decode failure, rather than silently dropping that one item from
	// an otherwise-successful batch.
	DecodeItem func(raw json.RawMessage) (Out, error)
}

// idBatchItem is the wire shape one item takes in an MDSP response: the id
// it was tagged with, and its answer.
type idBatchItem struct {
	ID     string          `json:"id"`
	Output json.RawMessage `json:"output"`
}

// idBatchResponse is the wire shape a whole MDSP batch answers with.
type idBatchResponse struct {
	Items []idBatchItem `json:"items"`
}

// NewIDBatchAlgebra builds a BatchAlgebra whose Encode tags every item with
// an invocation-local id (tagItems) and whose Merge resolves the response by
// id, requiring exact coverage before decoding a single item -- the id
// protocol OP-101 established, reused rather than reimplemented.
//
// Exact coverage is required regardless of Kind: an itemwise batch (kind
// Independent) has no item it can drop, since every input demands exactly
// one output, and an aggregate batch (kind Permutation, Partition, ...)
// needs the same per-item pass to hold before its GlobalValidate can mean
// anything -- GlobalValidate runs over Merge's output, and Merge cannot
// produce one output per item if the ids did not cover every item exactly
// once. Kind.RequiresExactCoverage documents this; the one kind that would
// answer false, Subset, is Filter's existing contract and is intentionally
// not built through this constructor -- collection.go already implements it
// and is out of scope to touch here.
func NewIDBatchAlgebra[In, Out any](class types.BatchClass, kind types.AlgebraKind, systemPrompt string, codec BatchItemCodec[In, Out]) BatchAlgebra[In, Out] {
	return BatchAlgebra[In, Out]{
		Class: class,
		Kind:  kind,
		Encode: func(items []In, _ types.OpOptions) (string, string, error) {
			tagged := tagItems(items)
			payload, err := json.Marshal(tagged)
			if err != nil {
				return "", "", fmt.Errorf("encoding batch of %d items: %w", len(items), err)
			}
			user := fmt.Sprintf("Process these items:\n%s", string(payload))
			return systemPrompt, user, nil
		},
		Merge: func(body string, items []In) ([]Out, error) {
			var parsed idBatchResponse
			if err := ParseJSONStrict(body, &parsed); err != nil {
				return nil, fmt.Errorf("batch response is not the expected {\"items\":[{\"id\":...,\"output\":...}]} shape: %w", err)
			}

			ids := make([]string, len(parsed.Items))
			byID := make(map[string]json.RawMessage, len(parsed.Items))
			for i, item := range parsed.Items {
				ids[i] = item.ID
				byID[item.ID] = item.Output
			}

			coverage := classifyBatchIDs(len(items), ids)
			if !coverage.OK() {
				return nil, fmt.Errorf("mdsp batch failed id coverage: %s", coverage.Error())
			}

			offered := itemIDs(len(items))
			outputs := make([]Out, len(items))
			for i, id := range offered {
				value, err := codec.DecodeItem(byID[id])
				if err != nil {
					return nil, fmt.Errorf("item %s: %w", id, err)
				}
				outputs[i] = value
			}
			return outputs, nil
		},
	}
}

// RunOpBatch runs op's declared batch algebra over one chunk of items: encode
// the batch request, call the model once, merge the response by id, and --
// for a BatchAggregate operation -- run the whole-set validation nothing
// called before this. It is Encode, Merge, and GlobalValidate actually
// running, which is the point A-001's Revised line left for PL-003 to fill.
//
// It does not repair: a batch failure here fails the chunk, and recovering
// it (progressive isolation of the unresolved ids, a smaller MDSP retry, an
// atomic fallback) is PL-009, which this delivery does not build. Feeding a
// chunk's own already-invalid response back into the model as a repair
// candidate here would be a second, divergent repair path from the one
// withRepair (repair.go) implements for atomic calls -- worse than not
// repairing at all.
func RunOpBatch[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt types.OpOptions) ([]Out, error) {
	if op.Batch.Encode == nil || op.Batch.Merge == nil {
		return nil, fmt.Errorf("op %s: batch class %s declared but no Encode/Merge to run it", op.ID, op.Batch.Class)
	}
	if len(items) == 0 {
		return nil, nil
	}

	system, user, err := op.Batch.Encode(items, opt)
	if err != nil {
		return nil, fmt.Errorf("op %s: encoding batch of %d items: %w", op.ID, len(items), err)
	}

	body, err := callLLM(ctx, system, user, opt)
	if err != nil {
		return nil, fmt.Errorf("op %s: batch call over %d items: %w", op.ID, len(items), err)
	}

	outputs, err := op.Batch.Merge(body, items)
	if err != nil {
		return nil, fmt.Errorf("op %s: merging batch response: %w", op.ID, err)
	}

	if op.Batch.Class == types.BatchAggregate && op.Batch.GlobalValidate != nil {
		if err := op.Batch.GlobalValidate(items, outputs); err != nil {
			return nil, fmt.Errorf("op %s: batch failed global validation: %w", op.ID, err)
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
func RunOpBatchResult[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt types.OpOptions) (types.Result[[]Out], error) {
	ctx, records := withCallRecording(ctx)

	value, err := RunOpBatch(ctx, op, items, opt)

	meta := envelopeFrom(records.collect(), op.ID.String())
	meta.Strategy = types.ShapeMDSP.String()

	if err != nil {
		return types.Result[[]Out]{Meta: meta}, err
	}
	return types.Result[[]Out]{Value: value, Meta: meta}, nil
}
