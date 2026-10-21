package ops

// Coverage gaps in batch.go (ExtractBatch's opts-conversion branch,
// extractMerged's provider-error branch, parseMergedResponse's parse/missing/
// out-of-range branches). These assert the documented contract -- a merged call's
// provider failure lands on every item in the chunk as an error, not a zero
// value counted as success; a malformed or short merged response is reported
// per index, never silently dropped -- rather than the plumbing already
// exercised by batch_test.go's happy paths.

import (
	"context"
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// ExtractBatch's opts-conversion branch: a caller passing types.OpOptions
// explicitly (rather than relying on NewExtractOptions()) must have those
// options actually take effect, not be silently discarded.
func TestExtractBatchHonoursExplicitOpOptions(t *testing.T) {
	withNoDefaultProvider(t)
	var model string
	provider := &batchProvider{respond: func(_ context.Context, req llm.CompletionRequest) (string, error) {
		model = req.Model
		return `{"Name":"Ann","Age":30}`, nil
	}}

	inputs := []interface{}{"Ann is 30"}
	for _, mode := range []BatchMode{ParallelMode, MergedMode} {
		model = ""
		batch := NewBatchProcessor(provider).WithMode(mode)
		if mode == MergedMode {
			provider.respond = func(_ context.Context, req llm.CompletionRequest) (string, error) {
				model = req.Model
				return `[{"index":0,"data":{"Name":"Ann","Age":30}}]`, nil
			}
		}

		results := ExtractBatch[Person](batch, inputs, types.OpOptions{
			Intelligence: types.Smart,
			Mode:         types.TransformMode,
			Model:        "pinned-model-for-the-test",
		})

		if results.Metadata.TotalItems != 1 {
			t.Fatalf("mode %v: TotalItems = %d, want 1", mode, results.Metadata.TotalItems)
		}
		if results.Errors[0] != nil {
			t.Fatalf("mode %v: unexpected error: %v", mode, results.Errors[0])
		}
		// The model the caller pinned in the options is the one the request named.
		if model != "pinned-model-for-the-test" {
			t.Errorf("mode %v: the request named the model %q, want the one pinned in the options", mode, model)
		}
	}
}

// extractMerged: a provider failure for a chunk must produce one error per
// item in that chunk, each result left at its zero value -- never a partial
// success miscounted, and never fewer errors than items.
func TestExtractMergedProviderFailureErrorsEveryItemInTheChunk(t *testing.T) {
	wantErr := errors.New("provider unavailable")
	provider := &captureProvider{errors: []error{wantErr, wantErr, wantErr}}

	batch := NewBatchProcessor(provider).WithMode(MergedMode).WithBatchSize(10)
	inputs := []interface{}{"a", "b", "c"}

	results := ExtractBatch[Person](batch, inputs)

	if results.Metadata.APICallsMade != 0 {
		t.Errorf("APICallsMade = %d, want 0 -- the one call failed", results.Metadata.APICallsMade)
	}
	if len(results.Errors) != len(inputs) {
		t.Fatalf("got %d errors, want %d (one per item)", len(results.Errors), len(inputs))
	}
	for i, err := range results.Errors {
		if err == nil {
			t.Errorf("item %d: error = nil, want the provider failure reported", i)
		}
	}
	if results.Metadata.Succeeded != 0 {
		t.Errorf("Succeeded = %d, want 0", results.Metadata.Succeeded)
	}
	if results.Metadata.Failed != len(inputs) {
		t.Errorf("Failed = %d, want %d", results.Metadata.Failed, len(inputs))
	}
}

// parseMergedResponse: a well-formed array covering every index parses
// cleanly with no errors.
func TestParseMergedResponseWellFormed(t *testing.T) {
	response := `[{"index":0,"data":{"Name":"Ann","Age":30}},{"index":1,"data":{"Name":"Bo","Age":40}}]`
	results, errs := parseMergedResponse[Person](response, 2)

	for i, err := range errs {
		if err != nil {
			t.Fatalf("index %d: unexpected error: %v", i, err)
		}
	}
	if results[0].Name != "Ann" || results[1].Name != "Bo" {
		t.Errorf("results = %+v, want Ann then Bo", results)
	}
}

// A response that is not the promised JSON array reports one error per
// expected slot, not a silent empty result.
func TestParseMergedResponseMalformedReportsEverySlot(t *testing.T) {
	results, errs := parseMergedResponse[Person]("not json at all", 3)

	if len(results) != 3 || len(errs) != 3 {
		t.Fatalf("got %d results / %d errors, want 3/3", len(results), len(errs))
	}
	for i, err := range errs {
		if err == nil {
			t.Errorf("index %d: error = nil, want the parse failure reported", i)
		}
	}
}

// An index the response never mentions is reported missing, not silently
// left as an unflagged zero value that a caller would read as "succeeded
// with an empty struct".
func TestParseMergedResponseMissingIndexIsAnError(t *testing.T) {
	response := `[{"index":0,"data":{"Name":"Ann","Age":30}}]`
	results, errs := parseMergedResponse[Person](response, 2)

	if errs[0] != nil {
		t.Errorf("index 0: unexpected error: %v", errs[0])
	}
	if errs[1] == nil {
		t.Fatal("index 1: error = nil, want 'no result for index 1'")
	}
	if results[1].Name != "" {
		t.Errorf("index 1: results[1] = %+v, want the zero value alongside its error", results[1])
	}
}

// An index outside [0, expectedCount) is a response describing more items
// than were sent; it must be dropped rather than panicking on an
// out-of-bounds write, and the expected slots it did not cover still report
// missing.
func TestParseMergedResponseOutOfRangeIndexIsDropped(t *testing.T) {
	response := `[{"index":5,"data":{"Name":"Ghost","Age":1}}]`
	results, errs := parseMergedResponse[Person](response, 1)

	if len(results) != 1 || len(errs) != 1 {
		t.Fatalf("got %d results / %d errors, want 1/1", len(results), len(errs))
	}
	if errs[0] == nil {
		t.Error("the only expected slot (index 0) was never populated and must report an error")
	}
}

// A data payload that cannot unmarshal into T at its index is reported at
// that index specifically, distinct from a missing index.
func TestParseMergedResponseBadItemDataIsPerIndexError(t *testing.T) {
	response := `[{"index":0,"data":"not-an-object"}]`
	_, errs := parseMergedResponse[Person](response, 1)

	if errs[0] == nil {
		t.Fatal("expected an unmarshal error for index 0's malformed data")
	}
}
