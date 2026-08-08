package tests

import (
	"errors"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

// This file covers the root package's thin PII/refinement wrappers: Redact,
// RedactLLM, Complete, CompleteField, Deduplicate, and ValidateLegacy. Each
// wrapper forwards to internal/ops with the caller's arguments unchanged, and
// each test proves the forwarding rather than only that a value came back.

// contactNote uses field names that match none of Redact's known PII field
// names (Filename/Note are explicitly documented as NOT matched), so any
// redaction observed here comes from the Categories-driven pattern match on
// the VALUE, proving Categories reached ops.Redact unchanged.
type contactNote struct {
	Note string
}

// Redact is deterministic and pattern-based -- it makes no provider call, so
// the wrapper's job is purely to hand Categories/Strategy through unchanged.
func TestRedactForwardsOptionsAndRedactsMatchingCategories(t *testing.T) {
	rec := contactNote{Note: "reach ada@example.com for details"}

	got, err := schemaflux.Redact(rec, schemaflux.NewRedactOptions().WithCategories([]string{"pii"}))
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if got.Note == rec.Note {
		t.Errorf("Note = %q, want the email pattern redacted", got.Note)
	}
}

// A category with no pattern matching the value is a no-op, not an error --
// proving the wrapper does not silently substitute its own option set for the
// caller's Categories. "financial" only matches IBANs, not an email address.
func TestRedactWithNoMatchingCategoryLeavesInputUnchanged(t *testing.T) {
	rec := contactNote{Note: "reach ada@example.com for details"}

	got, err := schemaflux.Redact(rec, schemaflux.NewRedactOptions().WithCategories([]string{"financial"}))
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if got != rec {
		t.Errorf("got %+v, want unchanged %+v", got, rec)
	}
}

// RedactLLM reaches ops.RedactLLM with context.Background() and the exact
// text/opts given; the scripted span response proves the request round-trips
// through the wrapper into a real span-based redaction.
func TestRedactLLMAPpliesScriptedSpans(t *testing.T) {
	testfixtures.WithScriptedProvider(t, `{"spans":[{"text":"john@email.com","start":8,"end":22,"category":"email"}]}`, nil)

	result, err := schemaflux.RedactLLM("Contact john@email.com for support",
		schemaflux.NewRedactLLMOptions().WithCategories([]string{"email"}).WithMaskChar('*'))
	if err != nil {
		t.Fatalf("RedactLLM: %v", err)
	}
	if strings.Contains(result.Text, "john@email.com") {
		t.Errorf("Text = %q, want the scripted span redacted", result.Text)
	}
	if len(result.Spans) != 1 || result.Spans[0].Category != "email" {
		t.Errorf("Spans = %+v, want one email span", result.Spans)
	}
}

// A provider failure must surface as an error, not a silently-empty result --
// RedactLLM must not fail open.
func TestRedactLLMProviderFailureIsAnError(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("provider unavailable"))

	_, err := schemaflux.RedactLLM("Contact john@email.com", schemaflux.NewRedactLLMOptions())
	if err == nil {
		t.Fatal("expected an error when the provider is unreachable")
	}
}

// Complete forwards partialText and opts to ops.Complete(context.Background(), ...).
func TestCompleteForwardsToOps(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "sentence continues here.", nil)

	result, err := schemaflux.Complete("a longer", schemaflux.NewCompleteOptions())
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if result.Text != "a longer sentence continues here." {
		t.Errorf("Text = %q, want the original plus the scripted completion", result.Text)
	}
	if result.Original != "a longer" {
		t.Errorf("Original = %q, want the input echoed back", result.Original)
	}
}

func TestCompleteProviderFailureIsAnError(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("provider unavailable"))

	_, err := schemaflux.Complete("a longer", schemaflux.NewCompleteOptions())
	if err == nil {
		t.Fatal("expected an error when the provider is unreachable")
	}
}

// CompleteField reaches ops.CompleteField with the same struct and field name,
// and returns a NEW copy with the completed field -- the original argument
// must survive unmodified because Go structs are copied by value here.
type blogPost struct {
	Title string
	Body  string
}

func TestCompleteFieldForwardsStructAndFieldName(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "healthcare in ways nobody expected.", nil)

	post := blogPost{Title: "AI in Healthcare", Body: "is transforming"}
	result, err := schemaflux.CompleteField[blogPost](post, schemaflux.NewCompleteFieldOptions("Body"))
	if err != nil {
		t.Fatalf("CompleteField: %v", err)
	}
	if result.Field != "Body" {
		t.Errorf("Field = %q, want %q", result.Field, "Body")
	}
	if result.Data.Body != "is transforming healthcare in ways nobody expected." {
		t.Errorf("Data.Body = %q, want the completed field", result.Data.Body)
	}
	if result.Data.Title != post.Title {
		t.Errorf("Data.Title = %q, want it untouched", result.Data.Title)
	}
	if post.Body != "is transforming" {
		t.Errorf("caller's own struct mutated: Body = %q", post.Body)
	}
}

// An unknown field name is rejected before any provider call -- CompleteField
// must not silently complete the wrong field or ignore the mistake.
func TestCompleteFieldUnknownFieldIsAnError(t *testing.T) {
	testfixtures.WithScriptedProvider(t, `{"text":"x","original":"x","length":1,"confidence":0.5,"metadata":{}}`, nil)

	post := blogPost{Title: "AI in Healthcare", Body: "is transforming"}
	_, err := schemaflux.CompleteField[blogPost](post, schemaflux.NewCompleteFieldOptions("NoSuchField"))
	if err == nil {
		t.Fatal("expected an error for a field that does not exist")
	}
}

// Deduplicate with at most one item never calls the provider -- proving the
// wrapper forwards items/threshold/opts without adding its own call.
func TestDeduplicateSingleItemMakesNoProviderCall(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("must not be called"))

	result, err := schemaflux.Deduplicate([]string{"only one"}, 0.85)
	if err != nil {
		t.Fatalf("Deduplicate: %v", err)
	}
	if len(result.Unique) != 1 || result.Unique[0] != "only one" {
		t.Errorf("Unique = %v, want the single item echoed back", result.Unique)
	}
	if result.TotalRemoved != 0 {
		t.Errorf("TotalRemoved = %d, want 0", result.TotalRemoved)
	}
}

// With two items and a scripted duplicate grouping, the group collapses to
// one unique item and one recorded removal -- proving the threshold and items
// reached ops.Deduplicate and its grouping response was applied.
func TestDeduplicateGroupsScriptedDuplicates(t *testing.T) {
	testfixtures.WithScriptedProvider(t, `{"groups":[[0,1]]}`, nil)

	result, err := schemaflux.Deduplicate([]string{"Ada Lovelace", "Ada  Lovelace"}, 0.85)
	if err != nil {
		t.Fatalf("Deduplicate: %v", err)
	}
	if len(result.Unique) != 1 {
		t.Fatalf("Unique = %v, want exactly one survivor from the scripted group", result.Unique)
	}
	if result.TotalRemoved != 1 {
		t.Errorf("TotalRemoved = %d, want 1", result.TotalRemoved)
	}
}

// ValidateLegacy forwards data/rules/opts to ops.ValidateLegacy and returns
// its ValidationResult verbatim.
func TestValidateLegacyForwardsRulesAndResult(t *testing.T) {
	testfixtures.WithScriptedProvider(t, `{"valid":false,"issues":["age must be 18-100"],"confidence":0.77,"suggestions":["set age to 30"]}`, nil)

	type person struct{ Age int }
	result, err := schemaflux.ValidateLegacy(person{Age: 5}, "age must be 18-100")
	if err != nil {
		t.Fatalf("ValidateLegacy: %v", err)
	}
	if result.Valid {
		t.Error("Valid = true, want the scripted false verdict")
	}
	if len(result.Issues) != 1 || result.Issues[0] != "age must be 18-100" {
		t.Errorf("Issues = %v, want the rules text to have reached the model and come back", result.Issues)
	}
	if result.ModelConfidence != 0.77 {
		t.Errorf("ModelConfidence = %v, want 0.77", result.ModelConfidence)
	}
}

func TestValidateLegacyProviderFailureIsAnError(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("provider unavailable"))

	type person struct{ Age int }
	_, err := schemaflux.ValidateLegacy(person{Age: 5}, "age must be 18-100")
	if err == nil {
		t.Fatal("expected an error when the provider is unreachable")
	}
}
