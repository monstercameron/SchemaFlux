package op_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/op"
)

func TestCatalogIsSortedByNameWithNoNameTwice(t *testing.T) {
	t.Parallel()
	entries := op.Catalog()
	if len(entries) == 0 {
		t.Fatalf("the catalogue holds %d operations", len(entries))
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
		if e.Name == "" || e.Name != strings.ToLower(e.Name) {
			t.Errorf("entry %d is named %q: a name is lower case", i, e.Name)
		}
	}
	if !slices.IsSorted(names) {
		t.Error("the catalogue is not sorted by name")
	}
	if len(slices.Compact(slices.Clone(names))) != len(names) {
		t.Error("a name is in the catalogue twice")
	}
}

func TestCatalogHoldsEveryOperationThatAddsARecord(t *testing.T) {
	t.Parallel()
	// The names of the table of Record.Op strings, by package.
	const root = "extract transform generate infer parse complete completefield suggest annotate explain question " +
		"summarize summarizedetail streamsummarize rewrite rewritedetail streamrewrite translate translatedetail " +
		"streamtranslate expand expanddetail streamexpand compress normalize format merge classify score compare " +
		"similar diff cluster deduplicate choose filter sort rank semanticmatch validate verify audit critique " +
		"is isdetail which whichdetail whichornone whichornonedetail rate ratedetail assess assessdetail"
	const exp = "exp.synthesize exp.predict exp.assemble exp.derive exp.interpolate exp.arbitrate exp.resolve exp.settle " +
		"exp.settleadversarial exp.conform exp.project exp.pivot exp.enrich exp.enrichinplace exp.decompose exp.decomposetoslice"
	const other = "redact.text flow.decide flow.match op.run op.runbatch op.runmany"
	var want []string
	for _, group := range []string{root, exp, other} {
		want = append(want, strings.Fields(group)...)
	}
	if got := len(op.Catalog()); got != len(want) {
		t.Errorf("the catalogue holds %d operations, the table of Record.Op names %d", got, len(want))
	}
	for _, name := range want {
		if _, found := op.Lookup(name); !found {
			t.Errorf("%q is not in the catalogue", name)
		}
	}
}

func TestLookupFindsAnEntryByTheNameItsRecordCarriesWhateverTheCase(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"extract", "Extract", "EXTRACT"} {
		e, found := op.Lookup(name)
		if !found || e.Name != "extract" || e.Category != op.CategoryExtract || e.Stability != op.StabilityStable {
			t.Errorf("Lookup(%q) = %+v, %v", name, e, found)
		}
	}
}

func TestLookupOfAnUnknownNameFindsNothing(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "nope", "read-invoice", "exp", "extract ", "ExtractResult"} {
		if e, found := op.Lookup(name); found || e != (op.Entry{}) {
			t.Errorf("Lookup(%q) = %+v, %v", name, e, found)
		}
	}
}

func TestTheShapingOperationsOfExpAndRedactTextAreExperimentalAndTheRestStable(t *testing.T) {
	t.Parallel()
	experimental := map[string]bool{}
	for _, e := range op.Catalog() {
		if e.Stability == op.StabilityExperimental {
			experimental[e.Name] = true
		}
		if e.Stability == op.StabilityUnspecified {
			t.Errorf("%s has no stability", e.Name)
		}
	}
	for name := range experimental {
		if !strings.HasPrefix(name, "exp.") && name != "redact.text" {
			t.Errorf("%s is experimental", name)
		}
	}
	if len(experimental) != 17 {
		t.Errorf("%d operations are experimental, want the sixteen of exp and redact.text", len(experimental))
	}
	for _, name := range []string{"is", "which", "assess", "flow.decide", "sort", "validate", "summarizedetail"} {
		if e, _ := op.Lookup(name); e.Stability != op.StabilityStable {
			t.Errorf("%s is %v, want stable", name, e.Stability)
		}
	}
}

func TestEveryOperationHasACategoryExceptTheRunnersOfThisPackage(t *testing.T) {
	t.Parallel()
	for _, e := range op.Catalog() {
		hasNone := e.Category == 0
		isRunner := strings.HasPrefix(e.Name, "op.")
		if hasNone != isRunner {
			t.Errorf("%s: category %v", e.Name, e.Category)
		}
	}
	for name, want := range map[string]op.Category{
		"classify": op.CategoryClassify, "is": op.CategoryClassify, "choose": op.CategorySelect, "sort": op.CategoryOrder,
		"audit": op.CategoryReview, "validate": op.CategoryValidate, "generate": op.CategoryGenerate,
		"summarize": op.CategoryTransform, "exp.settle": op.CategorySelect, "exp.predict": op.CategoryGenerate,
	} {
		if e, _ := op.Lookup(name); e.Category != want {
			t.Errorf("%s: category %v, want %v", name, e.Category, want)
		}
	}
}

func TestCatalogReturnsASliceTheCallerMayChange(t *testing.T) {
	t.Parallel()
	first := op.Catalog()
	first[0] = op.Entry{Name: "changed"}
	first = append(first[:1], first[2:]...)
	if again := op.Catalog(); again[0].Name == "changed" || len(again) != len(first)+1 {
		t.Error("changing the slice changed the catalogue")
	}
	if _, found := op.Lookup("changed"); found {
		t.Error("Lookup found an entry the caller made")
	}
}
