package ops

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Documentation that overstates a security property is a defect in the same way
// a wrong return value is: a caller reads it, believes it, and ships. These
// checks keep the disclosures in place, because a doc comment has nothing else
// holding it there.

// docComment returns the doc comment of a top-level function, or "".
func docComment(t *testing.T, name string) string {
	t.Helper()

	fileSet := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	for _, entry := range entries {
		fileName := entry.Name()
		if entry.IsDir() || filepath.Ext(fileName) != ".go" || strings.HasSuffix(fileName, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, fileName, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", fileName, err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv != nil || function.Name.Name != name {
				continue
			}
			if function.Doc == nil {
				return ""
			}
			return function.Doc.Text()
		}
	}
	t.Fatalf("no top-level function named %s", name)
	return ""
}

// The redaction operations carry known S1 defects. Until M05 lands, every entry
// point has to say so where a caller will see it.
func TestRedactionOperationsDiscloseTheirState(t *testing.T) {
	cases := []struct {
		name    string
		mustSay []string
	}{
		{"Redact", []string{"NOT PRODUCTION READY", "T-07", "T-08", "T-11", "T-13"}},
		{"RedactWithResult", []string{"NOT PRODUCTION READY", "T-09"}},
		{"RedactLLM", []string{"NOT PRODUCTION READY", "T-13"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := docComment(t, tc.name)
			if doc == "" {
				t.Fatalf("%s has no doc comment at all", tc.name)
			}
			for _, phrase := range tc.mustSay {
				if !strings.Contains(doc, phrase) {
					t.Errorf("the %s doc comment does not mention %q", tc.name, phrase)
				}
			}
			if !strings.Contains(doc, "ADVERSARIAL_API_REVIEW.md") {
				t.Errorf("the %s doc comment should point at the review", tc.name)
			}
		})
	}
}

// A warning that says only "be careful" is not a warning. Each must name the
// concrete failure a caller would otherwise discover in production.
func TestRedactionDisclosuresAreSpecific(t *testing.T) {
	doc := docComment(t, "Redact")

	for _, specific := range []string{
		"substring",  // T-07: how the field match actually works
		"reversible", // T-11: what jumble is
		"empty map",  // T-09: what the audit returns
	} {
		if !strings.Contains(doc, specific) {
			t.Errorf("the disclosure does not name the failure %q", specific)
		}
	}

	if !strings.Contains(doc, "Do not use this") {
		t.Error("the disclosure should say plainly what not to do with it")
	}
}

// Project's Exclude is a real mechanism now, so its documentation has to state
// both what it guarantees and where the guarantee stops. It used to advertise
// "privacy filtering" over a prompt hint.
func TestProjectDocumentsWhatExcludeGuarantees(t *testing.T) {
	doc := docComment(t, "Project")
	if doc == "" {
		t.Fatal("Project has no doc comment")
	}

	for _, phrase := range []string{
		"before it is serialised",  // the guarantee
		"never reach the provider", // stated plainly
		"not a classifier",         // the limit
		"still sent",               // the concrete case the limit covers
	} {
		if !strings.Contains(doc, phrase) {
			t.Errorf("the Project doc comment does not say %q", phrase)
		}
	}

	// The old framing claimed a property the mechanism did not have.
	if strings.Contains(doc, "privacy filtering") {
		t.Error("the doc comment still advertises 'privacy filtering'")
	}
}

// The README carries the same disclosure, because that is where someone
// evaluating the library looks first.
func TestREADMEDisclosesRedactionState(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	readme := string(raw)

	if !strings.Contains(readme, "not production ready") {
		t.Fatal("the README does not disclose the redaction state")
	}
	for _, phrase := range []string{"Redacting", "LLMRedacting", "ADVERSARIAL_API_REVIEW.md"} {
		if !strings.Contains(readme, phrase) {
			t.Errorf("the README disclosure does not mention %q", phrase)
		}
	}
}
