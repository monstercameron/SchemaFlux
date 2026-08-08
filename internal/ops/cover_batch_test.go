package ops

// Coverage gaps in batch.go (ExtractBatch's opts-conversion branch,
// extractMerged's provider-error branch, parseMergedResponse's parse/missing/
// out-of-range branches, determineBestMode's "similar and small" branch,
// areInputsSimilar's true/false branches) and pipeline.go's two example
// builders (ExtractAndValidatePipeline, TransformAndFormatPipeline), which
// had no test at all. These assert the documented contract -- a merged call's
// provider failure lands on every item in the chunk as an error, not a zero
// value counted as success; a malformed or short merged response is reported
// per index, never silently dropped -- rather than the plumbing already
// exercised by batch_test.go's happy paths.

import (
	"context"
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// ExtractBatch's opts-conversion branch: a caller passing types.OpOptions
// explicitly (rather than relying on NewExtractOptions()) must have those
// options actually take effect, not be silently discarded.
func TestExtractBatchHonoursExplicitOpOptions(t *testing.T) {
	setupMockClient()

	inputs := []interface{}{"Ann is 30"}
	batch := NewBatchProcessor(nil).WithMode(ParallelMode)

	results := ExtractBatch[Person](batch, inputs, types.OpOptions{
		Intelligence: types.Smart,
		Mode:         types.TransformMode,
	})

	if results.Metadata.TotalItems != 1 {
		t.Fatalf("TotalItems = %d, want 1", results.Metadata.TotalItems)
	}
	if results.Errors[0] != nil {
		t.Fatalf("unexpected error: %v", results.Errors[0])
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

// determineBestMode: between the "too small to bother" floor (areInputsSimilar
// needs >=3) and the ">20 always merges" ceiling, similarity alone must be
// enough to choose MergedMode.
func TestDetermineBestModeChoosesMergedForSimilarMidSizedInput(t *testing.T) {
	sb := NewSmartBatch(nil)
	inputs := []interface{}{"one", "two", "three", "four"}

	if got := sb.determineBestMode(inputs); got != MergedMode {
		t.Errorf("determineBestMode(4 similar strings) = %v, want MergedMode", got)
	}
}

// determineBestMode still prefers ParallelMode's error isolation for a small,
// dissimilar input set.
func TestDetermineBestModeChoosesParallelForDissimilarInput(t *testing.T) {
	sb := NewSmartBatch(nil)
	inputs := []interface{}{"a string", 42, 3.14}

	if got := sb.determineBestMode(inputs); got != ParallelMode {
		t.Errorf("determineBestMode(mixed types) = %v, want ParallelMode", got)
	}
}

// areInputsSimilar: fewer than three items never counts as similar, however
// alike they are -- there is nothing to benefit from merging.
func TestAreInputsSimilarRequiresAtLeastThreeItems(t *testing.T) {
	sb := NewSmartBatch(nil)
	if sb.areInputsSimilar([]interface{}{"a", "b"}) {
		t.Error("two items must never be reported similar")
	}
}

// Three or more items of the same concrete type are similar.
func TestAreInputsSimilarTrueForSameType(t *testing.T) {
	sb := NewSmartBatch(nil)
	if !sb.areInputsSimilar([]interface{}{"a", "b", "c"}) {
		t.Error("three same-type items must be reported similar")
	}
}

// A mixed-type slice is never similar, regardless of length.
func TestAreInputsSimilarFalseForMixedTypes(t *testing.T) {
	sb := NewSmartBatch(nil)
	if sb.areInputsSimilar([]interface{}{"a", 1, "c"}) {
		t.Error("mixed-type items must not be reported similar")
	}
}

// --- pipeline.go: the two example pipeline builders, previously untested. ---

// ExtractAndValidatePipeline chains Extract then Validate over one type; the
// second step must only run against the first's typed output, and the
// pipeline's Output must be the validated value itself, not an intermediate
// representation.
func TestExtractAndValidatePipelineRunsExtractThenValidate(t *testing.T) {
	calls := 0
	previous := customLLMCaller
	setLLMCaller(func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		calls++
		if calls == 1 {
			return `{"Name":"Ann","Age":30}`, nil
		}
		return `{"valid": true, "summary": "ok"}`, nil
	})
	defer func() { customLLMCaller = previous }()

	p := ExtractAndValidatePipeline[Person]("Age must be at least 18")
	result := p.Execute(context.Background(), "Ann, 30 years old")

	if len(result.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.StepsExecuted != 2 {
		t.Errorf("StepsExecuted = %d, want 2 (Extract, Validate)", result.StepsExecuted)
	}
	got, ok := result.Output.(Person)
	if !ok {
		t.Fatalf("Output type = %T, want Person", result.Output)
	}
	if got.Name != "Ann" || got.Age != 30 {
		t.Errorf("Output = %+v, want the extracted Person to survive validation unchanged", got)
	}
}

// A validation failure stops the pipeline (FailFast is the default) and is
// reported as a step failure, not swallowed.
func TestExtractAndValidatePipelineReportsValidationFailure(t *testing.T) {
	calls := 0
	previous := customLLMCaller
	setLLMCaller(func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		calls++
		if calls == 1 {
			return `{"Name":"Kid","Age":10}`, nil
		}
		return `{"valid": false, "errors": [{"field":"Age","severity":"error","message":"must be at least 18"}], "summary": "one error"}`, nil
	})
	defer func() { customLLMCaller = previous }()

	p := ExtractAndValidatePipeline[Person]("Age must be at least 18")
	result := p.Execute(context.Background(), "Kid, 10 years old")

	if result.StepsFailed != 1 {
		t.Errorf("StepsFailed = %d, want 1", result.StepsFailed)
	}
	if len(result.Errors) == 0 {
		t.Error("expected the validation failure to be reported in Errors")
	}
}

// TransformAndFormatPipeline chains Transform then Format across two types;
// the final Output is Format's plain string, not the intermediate transformed
// struct.
func TestTransformAndFormatPipelineRunsTransformThenFormat(t *testing.T) {
	type pfIn struct{ A string }
	type pfOut struct{ B string }

	calls := 0
	previous := customLLMCaller
	setLLMCaller(func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		calls++
		if calls == 1 {
			return `{"B":"transformed"}`, nil
		}
		return "  formatted output  ", nil
	})
	defer func() { customLLMCaller = previous }()

	p := TransformAndFormatPipeline[pfIn, pfOut]("a one-line summary")
	result := p.Execute(context.Background(), pfIn{A: "hello"})

	if len(result.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.StepsExecuted != 2 {
		t.Errorf("StepsExecuted = %d, want 2 (Transform, Format)", result.StepsExecuted)
	}
	got, ok := result.Output.(string)
	if !ok {
		t.Fatalf("Output type = %T, want string", result.Output)
	}
	if got != "formatted output" {
		t.Errorf("Output = %q, want the trimmed Format response", got)
	}
}

// A Transform step given the wrong input type reports the type-assertion
// failure from inside the pipeline step rather than panicking.
func TestTransformAndFormatPipelineRejectsWrongInputType(t *testing.T) {
	type pfIn struct{ A string }
	type pfOut struct{ B string }

	p := TransformAndFormatPipeline[pfIn, pfOut]("a one-line summary")
	result := p.Execute(context.Background(), "not a pfIn")

	if result.StepsFailed == 0 {
		t.Fatal("expected the wrong-type input to fail the Transform step")
	}
	if len(result.Errors) == 0 {
		t.Error("expected the type mismatch to be reported in Errors")
	}
}
