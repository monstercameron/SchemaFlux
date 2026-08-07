package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Priority is the kind of category type a caller writes. Under the old
// signature `C any`, an int-kinded version of this compiled and failed at run
// time; the ~string constraint moves that to the compiler.
type Priority string

func installClassifyResponse(t *testing.T, body string) {
	t.Helper()
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return body, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
}

func labelOptions() ClassifyOptions {
	return NewClassifyOptions().
		WithCategories([]string{"billing", "technical", "account"}).
		WithMinConfidence(0)
}

// OP-203. MultiLabel and MaxCategories changed the prompt and had nowhere to
// put an answer: the result carried exactly one category, so a caller who asked
// for multi-label classification got the same shape back and no way to tell
// whether the option had done anything.
func TestMultiLabelCategoriesReachTheResult(t *testing.T) {
	installClassifyResponse(t,
		`{"categories":["billing","account"],"confidence":0.9,"reasoning":"both apply"}`)

	result, err := Classify[string, Priority]("my card was declined on my account",
		labelOptions().WithMultiLabel(true))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}

	if len(result.Categories) != 2 {
		t.Fatalf("Categories = %v, want two", result.Categories)
	}
	if result.Categories[0] != "billing" || result.Categories[1] != "account" {
		t.Errorf("Categories = %v", result.Categories)
	}
	// Category stays meaningful for a caller who does not care about the
	// distinction.
	if result.Category != "billing" {
		t.Errorf("Category = %q, want the first assigned category", result.Category)
	}
}

// A single-label answer still populates Categories, so a caller can read one
// field regardless of the mode.
func TestSingleLabelStillPopulatesCategories(t *testing.T) {
	installClassifyResponse(t, `{"category":"technical","confidence":0.9}`)

	result, err := Classify[string, Priority]("the page will not load", labelOptions())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(result.Categories) != 1 || result.Categories[0] != "technical" {
		t.Errorf("Categories = %v, want exactly the primary category", result.Categories)
	}
}

// Every assigned category is checked against the offered set, not just the
// primary one.
func TestEveryAssignedCategoryIsChecked(t *testing.T) {
	installClassifyResponse(t,
		`{"categories":["billing","sales"],"confidence":0.9}`)

	_, err := Classify[string, Priority]("my card was declined",
		labelOptions().WithMultiLabel(true))
	if err == nil {
		t.Fatal("Classify accepted a multi-label answer containing a category it never offered")
	}
	if !strings.Contains(err.Error(), "sales") {
		t.Errorf("the error does not name the offending category: %v", err)
	}
}

// OP-203's other half: MaxCategories was an instruction with no consequence.
func TestMaxCategoriesIsEnforced(t *testing.T) {
	installClassifyResponse(t,
		`{"categories":["billing","technical","account"],"confidence":0.9}`)

	_, err := Classify[string, Priority]("everything is broken",
		labelOptions().WithMultiLabel(true).WithMaxCategories(2))
	if err == nil {
		t.Fatal("Classify returned three categories when at most two were requested")
	}
	if !strings.Contains(err.Error(), "above the 2 requested") {
		t.Errorf("the error should name the limit: %v", err)
	}

	// At the limit is fine.
	installClassifyResponse(t, `{"categories":["billing","technical"],"confidence":0.9}`)
	if _, err := Classify[string, Priority]("two problems",
		labelOptions().WithMultiLabel(true).WithMaxCategories(2)); err != nil {
		t.Errorf("Classify = %v at exactly the limit", err)
	}
}

// Duplicates collapse rather than counting against MaxCategories twice.
func TestDuplicateCategoriesCollapse(t *testing.T) {
	installClassifyResponse(t,
		`{"categories":["billing","Billing","BILLING"],"confidence":0.9}`)

	result, err := Classify[string, Priority]("a billing problem",
		labelOptions().WithMultiLabel(true).WithMaxCategories(1))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(result.Categories) != 1 {
		t.Errorf("Categories = %v, want one after case-folding duplicates", result.Categories)
	}
}

// OP-204. The category came back as a string and was converted through a JSON
// round trip, so only string-kinded C survived. C is constrained now, and the
// conversion is direct -- this asserts a named string type round-trips.
func TestNamedStringTypesAreSupported(t *testing.T) {
	installClassifyResponse(t, `{"category":"account","confidence":0.9}`)

	result, err := Classify[string, Priority]("close my account", labelOptions())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if result.Category != Priority("account") {
		t.Errorf("Category = %q", result.Category)
	}
	// The named type is preserved, not flattened to string.
	if _, ok := any(result.Category).(Priority); !ok {
		t.Error("the category lost its named type")
	}
}

// An alternative outside the offered set is dropped rather than failing the
// call: the primary answer is the contract, and a stray suggestion alongside it
// is not worth discarding a good result over.
func TestStrayAlternativesAreDroppedNotFatal(t *testing.T) {
	installClassifyResponse(t,
		`{"category":"billing","confidence":0.9,"alternatives":[{"category":"sales","confidence":0.3},{"category":"account","confidence":0.2}]}`)

	result, err := Classify[string, Priority]("my card was declined", labelOptions())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(result.Alternatives) != 1 || result.Alternatives[0].Category != "account" {
		t.Errorf("Alternatives = %v, want only the offered one", result.Alternatives)
	}
}
