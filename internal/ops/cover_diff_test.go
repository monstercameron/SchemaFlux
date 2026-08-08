package ops

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// cover_diff_test.go raises coverage of internal/ops/diff.go's under-tested
// branches: compareData's non-struct refusal and pointer handling,
// compareField's per-kind branches, formatValue's invalid-value case,
// generateDiffSummary's marshal-failure and empty-body refusals, and
// diffImpl's "changes exist but Intelligence is Fast with nothing Modified"
// path, where the existing table in diff_test.go never sends anything but
// struct pairs with Modified fields present.

// --- compareData ---

func TestCompareDataRejectsNonStructTypes(t *testing.T) {
	_, err := compareData("old", "new", NewDiffOptions())
	if err == nil {
		t.Fatal("compareData accepted non-struct input")
	}
	if !strings.Contains(err.Error(), "struct types") {
		t.Fatalf("error = %q, want it to name the struct requirement", err.Error())
	}
}

func TestCompareDataBothNilPointersAreNoOp(t *testing.T) {
	type S struct{ A int }
	var oldPtr, newPtr *S

	result, err := compareData(oldPtr, newPtr, NewDiffOptions())
	if err != nil {
		t.Fatalf("compareData: %v", err)
	}
	if len(result.Added)+len(result.Removed)+len(result.Modified) != 0 {
		t.Fatalf("result = %+v, want no changes for two nil pointers", result)
	}
}

func TestCompareDataMixedNilPointerIsRefused(t *testing.T) {
	type S struct{ A int }
	oldPtr := &S{A: 1}
	var newPtr *S

	_, err := compareData(oldPtr, newPtr, NewDiffOptions())
	if err == nil {
		t.Fatal("compareData accepted a nil paired with a non-nil pointer")
	}
	if !strings.Contains(err.Error(), "nil and non-nil") {
		t.Fatalf("error = %q, want it to name the nil/non-nil mismatch", err.Error())
	}
}

func TestCompareDataDereferencesNonNilPointers(t *testing.T) {
	type S struct{ A int }
	oldPtr := &S{A: 1}
	newPtr := &S{A: 2}

	result, err := compareData(oldPtr, newPtr, NewDiffOptions())
	if err != nil {
		t.Fatalf("compareData: %v", err)
	}
	if len(result.Modified) != 1 || result.Modified[0].Field != "A" {
		t.Fatalf("Modified = %+v, want a single A change", result.Modified)
	}
}

// --- compareField ---

func TestCompareFieldBothInvalidIsNoChange(t *testing.T) {
	if change := compareField("F", reflect.Value{}, reflect.Value{}, false); change != nil {
		t.Fatalf("compareField(invalid, invalid) = %+v, want nil", change)
	}
}

func TestCompareFieldOneInvalidIsStructureChanged(t *testing.T) {
	change := compareField("F", reflect.ValueOf(1), reflect.Value{}, false)
	if change == nil || change.ChangeType != "structure_changed" {
		t.Fatalf("compareField(valid, invalid) = %+v, want structure_changed", change)
	}
}

func TestCompareFieldBothNilPointersIsNoChange(t *testing.T) {
	var a, b *int
	change := compareField("F", reflect.ValueOf(a), reflect.ValueOf(b), false)
	if change != nil {
		t.Fatalf("compareField(nil ptr, nil ptr) = %+v, want nil", change)
	}
}

func TestCompareFieldOneNilPointerIsModified(t *testing.T) {
	x := 5
	var nilPtr *int
	change := compareField("F", reflect.ValueOf(&x), reflect.ValueOf(nilPtr), false)
	if change == nil || change.ChangeType != "modified" {
		t.Fatalf("compareField(ptr, nil ptr) = %+v, want modified", change)
	}
}

func TestCompareFieldDereferencesNonNilPointers(t *testing.T) {
	a, b := 1, 2
	change := compareField("F", reflect.ValueOf(&a), reflect.ValueOf(&b), false)
	if change == nil || change.ChangeType != "modified" || change.OldValue != 1 || change.NewValue != 2 {
		t.Fatalf("compareField(ptr(1), ptr(2)) = %+v, want a modified 1->2", change)
	}
}

func TestCompareFieldKindMismatchIsTypeChanged(t *testing.T) {
	change := compareField("F", reflect.ValueOf(1), reflect.ValueOf("one"), false)
	if change == nil || change.ChangeType != "type_changed" {
		t.Fatalf("compareField(int, string) = %+v, want type_changed", change)
	}
}

func TestCompareFieldStructDeepCompare(t *testing.T) {
	type Nested struct{ X int }
	a := Nested{X: 1}
	b := Nested{X: 2}

	t.Run("deep compare on reports the change", func(t *testing.T) {
		change := compareField("F", reflect.ValueOf(a), reflect.ValueOf(b), true)
		if change == nil || change.ChangeType != "structure_changed" {
			t.Fatalf("compareField(deep=true) = %+v, want structure_changed", change)
		}
	})
	t.Run("deep compare off reports nothing", func(t *testing.T) {
		change := compareField("F", reflect.ValueOf(a), reflect.ValueOf(b), false)
		if change != nil {
			t.Fatalf("compareField(deep=false) = %+v, want nil (struct compare skipped)", change)
		}
	})
	t.Run("identical structs report nothing regardless", func(t *testing.T) {
		change := compareField("F", reflect.ValueOf(a), reflect.ValueOf(a), true)
		if change != nil {
			t.Fatalf("compareField(identical structs) = %+v, want nil", change)
		}
	})
}

func TestCompareFieldSlice(t *testing.T) {
	same := []int{1, 2, 3}
	changed := []int{1, 2, 4}

	if change := compareField("F", reflect.ValueOf(same), reflect.ValueOf(same), false); change != nil {
		t.Fatalf("compareField(identical slices) = %+v, want nil", change)
	}
	change := compareField("F", reflect.ValueOf(same), reflect.ValueOf(changed), false)
	if change == nil || change.ChangeType != "modified" {
		t.Fatalf("compareField(different slices) = %+v, want modified", change)
	}
}

func TestCompareFieldMap(t *testing.T) {
	same := map[string]int{"a": 1}
	changed := map[string]int{"a": 2}

	if change := compareField("F", reflect.ValueOf(same), reflect.ValueOf(same), false); change != nil {
		t.Fatalf("compareField(identical maps) = %+v, want nil", change)
	}
	change := compareField("F", reflect.ValueOf(same), reflect.ValueOf(changed), false)
	if change == nil || change.ChangeType != "modified" {
		t.Fatalf("compareField(different maps) = %+v, want modified", change)
	}
}

func TestCompareFieldPrimitiveEqualIsNoChange(t *testing.T) {
	if change := compareField("F", reflect.ValueOf(7), reflect.ValueOf(7), false); change != nil {
		t.Fatalf("compareField(7, 7) = %+v, want nil", change)
	}
}

// --- formatValue ---

func TestFormatValueInvalidReturnsNil(t *testing.T) {
	if v := formatValue(reflect.Value{}); v != nil {
		t.Fatalf("formatValue(invalid) = %v, want nil", v)
	}
}

func TestFormatValueValidReturnsInterface(t *testing.T) {
	if v := formatValue(reflect.ValueOf(42)); v != 42 {
		t.Fatalf("formatValue(42) = %v, want 42", v)
	}
}

// --- generateDiffSummary ---

// unmarshalableData contains a channel, which encoding/json cannot marshal --
// the shape needed to exercise generateDiffSummary's marshal-failure
// refusals directly, since ordinary struct data always marshals cleanly.
type unmarshalableData struct {
	C chan int
}

func TestGenerateDiffSummaryRefusesUnmarshalableOldData(t *testing.T) {
	_, err := generateDiffSummary(unmarshalableData{C: make(chan int)}, unmarshalableData{}, comparisonResult{}, NewDiffOptions())
	if err == nil {
		t.Fatal("generateDiffSummary accepted old data that cannot be marshaled")
	}
}

func TestGenerateDiffSummaryRefusesUnmarshalableNewData(t *testing.T) {
	_, err := generateDiffSummary(unmarshalableData{}, unmarshalableData{C: make(chan int)}, comparisonResult{}, NewDiffOptions())
	if err == nil {
		t.Fatal("generateDiffSummary accepted new data that cannot be marshaled")
	}
}

func TestGenerateDiffSummaryRefusesEmptyResponse(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "   ", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	_, err := generateDiffSummary(struct{ A int }{1}, struct{ A int }{2}, comparisonResult{}, NewDiffOptions())
	if err == nil {
		t.Fatal("generateDiffSummary accepted an all-whitespace body as a summary")
	}
}

func TestGenerateDiffSummarySurfacesLLMFailure(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", ErrNoProvider
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	_, err := generateDiffSummary(struct{ A int }{1}, struct{ A int }{2}, comparisonResult{}, NewDiffOptions())
	if err == nil {
		t.Fatal("generateDiffSummary did not surface the provider's failure")
	}
}

func TestGenerateDiffSummaryIncludesBackground(t *testing.T) {
	var seenPrompt string
	setLLMCaller(func(_ context.Context, _, user string, _ types.OpOptions) (string, error) {
		seenPrompt = user
		return "a summary", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	summary, err := generateDiffSummary(struct{ A int }{1}, struct{ A int }{2}, comparisonResult{},
		NewDiffOptions().WithBackground("inventory reconciliation"))
	if err != nil {
		t.Fatalf("generateDiffSummary: %v", err)
	}
	if summary != "a summary" {
		t.Fatalf("summary = %q", summary)
	}
	if !strings.Contains(seenPrompt, "inventory reconciliation") {
		t.Fatalf("user prompt does not carry the configured background: %q", seenPrompt)
	}
}

// --- diffImpl ---

// TestDiffFastIntelligenceWithOnlyAddedFieldsSkipsSummary is the branch
// diff_test.go's table never reaches: Fast intelligence, changes exist, but
// none of them are Modified -- so neither the "generate a summary" condition
// nor the "no changes" fallback fires, and Summary is left the zero value.
func TestDiffFastIntelligenceWithOnlyAddedFieldsSkipsSummary(t *testing.T) {
	type Old struct{ A int }
	type New struct {
		A int
		B int
	}

	result, err := Diff[any](Old{A: 1}, New{A: 1, B: 2}, NewDiffOptions().WithIntelligence(types.Fast))
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(result.Added) == 0 {
		t.Fatal("expected an added field (B) to be detected")
	}
	if len(result.Modified) != 0 {
		t.Fatalf("Modified = %v, want none", result.Modified)
	}
	if result.Summary != "" {
		t.Fatalf("Summary = %q, want empty: Fast intelligence with no Modified fields should not call the model", result.Summary)
	}
	if result.SummaryError != nil {
		t.Fatalf("SummaryError = %v, want nil: the model was never called on this path", result.SummaryError)
	}
}

// TestDiffSummaryErrorIsReportedNotFatal proves a failed summary generation
// does not fail the whole Diff call -- SummaryError carries the reason and
// the structural diff, already computed, is still returned.
func TestDiffSummaryErrorIsReportedNotFatal(t *testing.T) {
	type Item struct{ Name string }

	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", ErrNoProvider
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	result, err := Diff(Item{Name: "old"}, Item{Name: "new"}, NewDiffOptions())
	if err != nil {
		t.Fatalf("Diff returned an error instead of reporting SummaryError: %v", err)
	}
	if result.Summary != "" {
		t.Fatalf("Summary = %q, want empty on a failed summary generation", result.Summary)
	}
	if result.SummaryError == nil {
		t.Fatal("SummaryError = nil, want the provider's failure recorded")
	}
	if len(result.Modified) != 1 {
		t.Fatalf("Modified = %v, want the structural diff still computed", result.Modified)
	}
}

// TestDiffRejectsNonStructInput proves the struct-only refusal is visible
// through the public Diff entry point, wrapped as a comparison failure.
func TestDiffRejectsNonStructInput(t *testing.T) {
	_, err := Diff[string]("old", "new", NewDiffOptions())
	if err == nil {
		t.Fatal("Diff accepted non-struct input")
	}
	if !strings.Contains(err.Error(), "comparison failed") {
		t.Fatalf("error = %q, want it wrapped as a comparison failure", err.Error())
	}
}
